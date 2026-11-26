package worldradar

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// ArxivConnector acquires arXiv Atom XML feeds and normalizes preprint records.
type ArxivConnector struct{}

func (c *ArxivConnector) ID() string { return "arxiv" }

type arxivAtomFeed struct {
	XMLName xml.Name         `xml:"feed"`
	Title   string           `xml:"title"`
	Entries []arxivAtomEntry `xml:"entry"`
}

type arxivAtomEntry struct {
	ID        string `xml:"id"`
	Title     string `xml:"title"`
	Summary   string `xml:"summary"`
	Published string `xml:"published"`
	Updated   string `xml:"updated"`
	Authors   []struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Links []struct {
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
		Type string `xml:"type,attr"`
	} `xml:"link"`
	Categories []struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
}

// Collect fetches and parses an arXiv Atom feed using conditional GET.
func (c *ArxivConnector) Collect(ctx context.Context, req CollectRequest) CollectResult {
	statusCode, respHeaders, bodyBytes, err := performConditionalGET(
		ctx, req.HTTPClient, req.Target.SourceURI, req.Target.ETag,
		"application/atom+xml, application/xml, text/xml",
	)
	if err != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: err.Error(),
		}
	}
	if statusCode == http.StatusNotModified {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag),
			NextCursor: req.Target.Cursor,
		}
	}
	if statusCode == http.StatusTooManyRequests {
		return CollectResult{
			Status:       RunStatusRateLimited,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			RetryAfter:   parseRetryAfter(respHeaders, req.Now),
			ErrorMessage: "arxiv rate limited (http 429)",
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: fmt.Sprintf("arxiv http status %d", statusCode),
		}
	}

	nextETag := firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag)
	records, parseErr := parseArxivAtom(bodyBytes, req.Target.SourceURI, req.Now)
	if parseErr != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: parseErr.Error(),
		}
	}
	if len(records) == 0 {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       nextETag,
			NextCursor: req.Target.Cursor,
		}
	}
	return CollectResult{
		Status:     RunStatusOK,
		ETag:       nextETag,
		NextCursor: req.Target.Cursor,
		Records:    records,
	}
}

func parseArxivAtom(bodyBytes []byte, feedURI string, now time.Time) ([]CollectedRecord, error) {
	if strings.TrimSpace(string(bodyBytes)) == "" {
		return nil, nil
	}
	var doc arxivAtomFeed
	if err := xml.Unmarshal(bodyBytes, &doc); err != nil || !strings.EqualFold(doc.XMLName.Local, "feed") {
		return nil, fmt.Errorf("invalid arxiv atom XML document")
	}

	records := make([]CollectedRecord, 0, len(doc.Entries))
	for _, entry := range doc.Entries {
		title := normalizeClusterTextPreserveCase(entry.Title)
		summary := normalizeClusterTextPreserveCase(entry.Summary)
		if title == "" && summary == "" {
			continue
		}

		rawID := strings.TrimSpace(entry.ID)
		shortID := rawID
		if idx := strings.Index(shortID, "/abs/"); idx >= 0 {
			shortID = shortID[idx+5:]
		}
		shortID = strings.Trim(shortID, "/")

		var linkHref string
		for _, l := range entry.Links {
			href := strings.TrimSpace(l.Href)
			if href == "" {
				continue
			}
			if l.Rel == "alternate" || l.Rel == "" {
				linkHref = href
				break
			}
			if linkHref == "" {
				linkHref = href
			}
		}
		sourceURI := firstNonEmpty(linkHref, rawID, feedURI)

		var firstAuthor string
		var authors []string
		for _, a := range entry.Authors {
			name := strings.TrimSpace(a.Name)
			if name != "" {
				authors = append(authors, name)
				if firstAuthor == "" {
					firstAuthor = name
				}
			}
		}

		var categoryTerms []string
		for _, cat := range entry.Categories {
			if term := strings.TrimSpace(cat.Term); term != "" {
				categoryTerms = append(categoryTerms, term)
			}
		}

		rawPub := firstNonEmpty(strings.TrimSpace(entry.Published), strings.TrimSpace(entry.Updated))
		pubTime, pubPrecision := parseTimestampWithPrecision(rawPub, now)

		strongID := ""
		if shortID != "" {
			strongID = "arxiv:" + shortID
		}
		extID := firstNonEmpty(rawID, shortID, sourceURI, title)

		payloadBytes, _ := json.Marshal(map[string]any{
			"id":         rawID,
			"arxivId":    shortID,
			"authors":    authors,
			"categories": categoryTerms,
			"published":  strings.TrimSpace(entry.Published),
			"updated":    strings.TrimSpace(entry.Updated),
		})

		records = append(records, CollectedRecord{
			ExternalID:    extID,
			SourceURI:     sourceURI,
			Title:         title,
			Summary:       summary,
			Language:      "en",
			MimeType:      "application/atom+xml",
			RawExcerpt:    truncateText(firstNonEmpty(summary, title), 2000),
			OccurredAt:    pubTime.UTC().Format(time.RFC3339Nano),
			PublishedAt:   pubTime.UTC().Format(time.RFC3339Nano),
			TimePrecision: pubPrecision,
			EntityName:    firstAuthor,
			EntityType:    "person",
			StrongID:      strongID,
			Domains:       []string{"D12", "D06"},
			PayloadJSON:   string(payloadBytes),
		})
	}
	return records, nil
}

// CyberKEVConnector acquires CISA Known Exploited Vulnerabilities (KEV) JSON catalogs.
type CyberKEVConnector struct{}

func (c *CyberKEVConnector) ID() string { return "cyber_kev" }

type cyberKEVEnvelope struct {
	Vulnerabilities []cyberKEVEntry `json:"vulnerabilities"`
}

type cyberKEVEntry struct {
	CVEID                      string `json:"cveID"`
	VendorProject              string `json:"vendorProject"`
	Product                    string `json:"product"`
	VulnerabilityName          string `json:"vulnerabilityName"`
	DateAdded                  string `json:"dateAdded"`
	ShortDescription           string `json:"shortDescription"`
	RequiredAction             string `json:"requiredAction"`
	KnownRansomwareCampaignUse string `json:"knownRansomwareCampaignUse"`
}

// Collect fetches and parses a CISA KEV JSON feed using conditional GET.
func (c *CyberKEVConnector) Collect(ctx context.Context, req CollectRequest) CollectResult {
	statusCode, respHeaders, bodyBytes, err := performConditionalGET(
		ctx, req.HTTPClient, req.Target.SourceURI, req.Target.ETag,
		"application/json",
	)
	if err != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: err.Error(),
		}
	}
	if statusCode == http.StatusNotModified {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag),
			NextCursor: req.Target.Cursor,
		}
	}
	if statusCode == http.StatusTooManyRequests {
		return CollectResult{
			Status:       RunStatusRateLimited,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			RetryAfter:   parseRetryAfter(respHeaders, req.Now),
			ErrorMessage: "cyber_kev rate limited (http 429)",
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: fmt.Sprintf("cyber_kev http status %d", statusCode),
		}
	}

	nextETag := firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag)
	trimmed := strings.TrimSpace(string(bodyBytes))
	if trimmed == "" {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       nextETag,
			NextCursor: req.Target.Cursor,
		}
	}

	var envelope cyberKEVEnvelope
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: fmt.Sprintf("decode cyber_kev json: %v", err),
		}
	}
	if len(envelope.Vulnerabilities) == 0 {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       nextETag,
			NextCursor: req.Target.Cursor,
		}
	}

	records := make([]CollectedRecord, 0, len(envelope.Vulnerabilities))
	for _, vuln := range envelope.Vulnerabilities {
		cveID := strings.TrimSpace(vuln.CVEID)
		entityName := strings.TrimSpace(strings.TrimSpace(vuln.VendorProject) + " " + strings.TrimSpace(vuln.Product))
		vulnName := strings.TrimSpace(vuln.VulnerabilityName)
		title := firstNonEmpty(vulnName, cveID)
		if cveID != "" && vulnName != "" && !strings.Contains(strings.ToUpper(vulnName), strings.ToUpper(cveID)) {
			title = cveID + ": " + vulnName
		}

		summary := strings.TrimSpace(vuln.ShortDescription)
		if action := strings.TrimSpace(vuln.RequiredAction); action != "" {
			if summary != "" {
				summary += " | Required action: " + action
			} else {
				summary = action
			}
		}
		if title == "" && summary == "" {
			continue
		}

		pubTime, pubPrecision := parseTimestampWithPrecision(vuln.DateAdded, req.Now)
		urgency := 0.92
		if strings.EqualFold(strings.TrimSpace(vuln.KnownRansomwareCampaignUse), "Known") {
			urgency = 0.98
		}
		rawPayload, _ := json.Marshal(vuln)

		records = append(records, CollectedRecord{
			ExternalID:    firstNonEmpty(cveID, title),
			SourceURI:     req.Target.SourceURI,
			Title:         title,
			Summary:       summary,
			Language:      "en",
			MimeType:      "application/json",
			RawExcerpt:    truncateText(firstNonEmpty(summary, title), 2000),
			OccurredAt:    pubTime.UTC().Format(time.RFC3339Nano),
			PublishedAt:   pubTime.UTC().Format(time.RFC3339Nano),
			TimePrecision: pubPrecision,
			EntityName:    entityName,
			EntityType:    "software",
			StrongID:      cveID,
			Domains:       []string{"D10"},
			ImpactHint:    0.9,
			UrgencyHint:   urgency,
			PayloadJSON:   string(rawPayload),
		})
	}
	if len(records) == 0 {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       nextETag,
			NextCursor: req.Target.Cursor,
		}
	}
	return CollectResult{
		Status:     RunStatusOK,
		ETag:       nextETag,
		NextCursor: req.Target.Cursor,
		Records:    records,
	}
}

// USGSGeoConnector acquires USGS GeoJSON earthquake and tsunami feeds.
type USGSGeoConnector struct{}

func (c *USGSGeoConnector) ID() string { return "usgs_geo" }

type usgsGeoEnvelope struct {
	Features []usgsGeoFeature `json:"features"`
}

type usgsGeoFeature struct {
	ID         string `json:"id"`
	Properties struct {
		Mag     float64 `json:"mag"`
		Place   string  `json:"place"`
		Time    int64   `json:"time"`
		URL     string  `json:"url"`
		Title   string  `json:"title"`
		Tsunami int     `json:"tsunami"`
	} `json:"properties"`
	Geometry struct {
		Coordinates []float64 `json:"coordinates"`
	} `json:"geometry"`
}

type usgsGeoPayload struct {
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	DepthKm   float64 `json:"depthKm"`
	Magnitude float64 `json:"magnitude"`
	Tsunami   int     `json:"tsunami"`
}

// Collect fetches and parses a USGS GeoJSON feed using conditional GET.
func (c *USGSGeoConnector) Collect(ctx context.Context, req CollectRequest) CollectResult {
	statusCode, respHeaders, bodyBytes, err := performConditionalGET(
		ctx, req.HTTPClient, req.Target.SourceURI, req.Target.ETag,
		"application/geo+json, application/json",
	)
	if err != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: err.Error(),
		}
	}
	if statusCode == http.StatusNotModified {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag),
			NextCursor: req.Target.Cursor,
		}
	}
	if statusCode == http.StatusTooManyRequests {
		return CollectResult{
			Status:       RunStatusRateLimited,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			RetryAfter:   parseRetryAfter(respHeaders, req.Now),
			ErrorMessage: "usgs_geo rate limited (http 429)",
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: fmt.Sprintf("usgs_geo http status %d", statusCode),
		}
	}

	nextETag := firstNonEmpty(respHeaders.Get("ETag"), req.Target.ETag)
	trimmed := strings.TrimSpace(string(bodyBytes))
	if trimmed == "" {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       nextETag,
			NextCursor: req.Target.Cursor,
		}
	}

	var envelope usgsGeoEnvelope
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return CollectResult{
			Status:       RunStatusConnectorFailed,
			ETag:         req.Target.ETag,
			NextCursor:   req.Target.Cursor,
			ErrorMessage: fmt.Sprintf("decode usgs_geo json: %v", err),
		}
	}
	if len(envelope.Features) == 0 {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       nextETag,
			NextCursor: req.Target.Cursor,
		}
	}

	fallbackNow := req.Now
	if fallbackNow.IsZero() {
		fallbackNow = time.Now().UTC()
	}

	records := make([]CollectedRecord, 0, len(envelope.Features))
	for _, feat := range envelope.Features {
		coords := feat.Geometry.Coordinates
		var lon, lat, depthKm float64
		if len(coords) > 0 {
			lon = coords[0]
		}
		if len(coords) > 1 {
			lat = coords[1]
		}
		if len(coords) > 2 {
			depthKm = coords[2]
		}

		mag := feat.Properties.Mag
		place := strings.TrimSpace(feat.Properties.Place)
		tsunami := feat.Properties.Tsunami

		title := firstNonEmpty(
			strings.TrimSpace(feat.Properties.Title),
			fmt.Sprintf("M%.1f Earthquake - %s", mag, firstNonEmpty(place, "Unknown Location")),
		)
		summary := fmt.Sprintf("Magnitude %.1f seismic event at %s (depth %.1f km, tsunami=%d)",
			mag, firstNonEmpty(place, "unknown location"), depthKm, tsunami)

		occTime := fallbackNow.UTC()
		precision := "unknown"
		if feat.Properties.Time > 0 {
			occTime = time.UnixMilli(feat.Properties.Time).UTC()
			precision = "second"
		}

		impact := math.Max(0, math.Min(1.0, mag/9.0))
		urgency := math.Max(0.2, math.Min(1.0, mag/10.0))
		if mag >= 6.0 || tsunami > 0 {
			boosted := math.Max(0.85, impact+0.15)
			if tsunami > 0 {
				boosted = math.Max(boosted, 0.95)
			}
			urgency = math.Min(1.0, boosted)
		}

		payloadBytes, _ := json.Marshal(usgsGeoPayload{
			Lat:       lat,
			Lon:       lon,
			DepthKm:   depthKm,
			Magnitude: mag,
			Tsunami:   tsunami,
		})

		extID := firstNonEmpty(strings.TrimSpace(feat.ID), strings.TrimSpace(feat.Properties.URL), title)
		strongID := ""
		if strings.TrimSpace(feat.ID) != "" {
			strongID = "usgs:" + strings.TrimSpace(feat.ID)
		}

		records = append(records, CollectedRecord{
			ExternalID:    extID,
			SourceURI:     firstNonEmpty(strings.TrimSpace(feat.Properties.URL), req.Target.SourceURI),
			Title:         title,
			Summary:       summary,
			Language:      "en",
			MimeType:      "application/geo+json",
			RawExcerpt:    truncateText(summary, 2000),
			OccurredAt:    occTime.Format(time.RFC3339Nano),
			PublishedAt:   occTime.Format(time.RFC3339Nano),
			TimePrecision: precision,
			EntityName:    place,
			EntityType:    "location",
			StrongID:      strongID,
			Domains:       []string{"D14"},
			ImpactHint:    impact,
			UrgencyHint:   urgency,
			PayloadJSON:   string(payloadBytes),
		})
	}
	if len(records) == 0 {
		return CollectResult{
			Status:     RunStatusNoNewData,
			ETag:       nextETag,
			NextCursor: req.Target.Cursor,
		}
	}
	return CollectResult{
		Status:     RunStatusOK,
		ETag:       nextETag,
		NextCursor: req.Target.Cursor,
		Records:    records,
	}
}

func normalizeClusterTextPreserveCase(raw string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
}
