package connectors

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// RSSConnector implements the Connector contract for RSS 2.0 and Atom XML feeds (Section 3.2.1).
// Cursor format: "<last_guid>|<last_pub_date_rfc3339>"
// GUID preference: atom:id > guid > link
type RSSConnector struct {
	SourceID     string
	FeedURL      string
	LicenseClass string
	HTTPClient   *http.Client
	Checkpoint   Checkpoint
}

type rssDocument struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title    string       `xml:"title"`
		Language string       `xml:"language"`
		TTL      int          `xml:"ttl"`
		Items    []rssItemXML `xml:"item"`
	} `xml:"channel"`
}

type rssItemXML struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	PubDate     string   `xml:"pubDate"`
	Description string   `xml:"description"`
	Categories  []string `xml:"category"`
	Retracted   bool     `xml:"retracted"`
	RetractsID  string   `xml:"retractsId"`
}

type atomDocument struct {
	XMLName xml.Name       `xml:"feed"`
	Title   string         `xml:"title"`
	Entries []atomEntryXML `xml:"entry"`
}

type atomEntryXML struct {
	ID         string `xml:"id"`
	Title      string `xml:"title"`
	Updated    string `xml:"updated"`
	Published  string `xml:"published"`
	Summary    string `xml:"summary"`
	Content    string `xml:"content"`
	Retracted  bool   `xml:"retracted"`
	RetractsID string `xml:"retractsId"`
	Link       struct {
		Href string `xml:"href,attr"`
	} `xml:"link"`
	Categories []struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
}

// Type returns ConnectorRSS ("rss").
func (c *RSSConnector) Type() ConnectorType { return ConnectorRSS }

// Discover fetches the RSS/Atom feed and emits items newer than the checkpoint cursor.
func (c *RSSConnector) Discover(ctx context.Context, checkpoint Checkpoint) ([]DiscoveredItem, Checkpoint, error) {
	if strings.TrimSpace(c.FeedURL) == "" {
		return nil, checkpoint, fmt.Errorf("rss feed URL is required")
	}
	statusCode, _, body, err := doHTTPGet(ctx, c.HTTPClient, c.FeedURL, map[string]string{
		"Accept": "application/rss+xml, application/atom+xml, application/xml, text/xml",
	})
	if err != nil {
		return nil, checkpoint, err
	}
	if statusCode == http.StatusNotModified {
		return nil, checkpoint, nil
	}
	if statusCode == http.StatusTooManyRequests {
		return nil, checkpoint, fmt.Errorf("rss rate limited (http 429)")
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, checkpoint, fmt.Errorf("rss http status %d", statusCode)
	}

	items, err := c.parseFeed(body)
	if err != nil {
		return nil, checkpoint, err
	}
	if len(items) == 0 {
		return nil, checkpoint, nil
	}

	// Sort chronologically oldest-first so checkpoint advances monotonically.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].PublishedAt.Equal(items[j].PublishedAt) {
			return items[i].Metadata["guid"] < items[j].Metadata["guid"]
		}
		return items[i].PublishedAt.Before(items[j].PublishedAt)
	})

	prevGUID, prevPub := parseRSSCursor(checkpoint.Cursor)
	filtered := make([]DiscoveredItem, 0, len(items))
	latestGUID := prevGUID
	latestPub := prevPub

	for _, it := range items {
		guid := it.Metadata["guid"]
		if !prevPub.IsZero() {
			if it.PublishedAt.Before(prevPub) {
				continue
			}
			if it.PublishedAt.Equal(prevPub) && prevGUID != "" && guid <= prevGUID {
				continue
			}
		} else if prevGUID != "" && guid == prevGUID {
			continue
		}
		filtered = append(filtered, it)
		if it.PublishedAt.After(latestPub) || (it.PublishedAt.Equal(latestPub) && guid > latestGUID) {
			latestPub = it.PublishedAt
			latestGUID = guid
		}
	}

	nextCP := checkpoint
	nextCP.ConnectorType = ConnectorRSS
	if nextCP.SourceID == "" {
		nextCP.SourceID = c.SourceID
	}
	if len(filtered) > 0 {
		nextCP.Cursor = formatRSSCursor(latestGUID, latestPub)
		nextCP.LastSeenAt = latestPub.UTC()
		nextCP.ItemCount = checkpoint.ItemCount + int64(len(filtered))
		nextCP.Hash = nextCP.ComputeHash()
	}
	c.Checkpoint = nextCP
	return filtered, nextCP, nil
}

// Acquire materializes an Observation from a DiscoveredItem produced by Discover.
func (c *RSSConnector) Acquire(_ context.Context, item DiscoveredItem) (Observation, error) {
	rawPayload := []byte(item.Metadata["raw_json"])
	if len(rawPayload) == 0 {
		rawPayload, _ = json.Marshal(item)
	}
	now := time.Now().UTC()
	rawHash := HashSHA256Hex(rawPayload)
	title := item.Title
	summary := item.Metadata["description"]
	body := firstNonEmpty(item.Metadata["content"], summary, title)
	contentHash := HashSHA256Hex([]byte(NormalizeNFKC(title) + "\x00" + NormalizeNFKC(summary)))

	var tags []string
	if rawCats := item.Metadata["categories"]; rawCats != "" {
		tags = strings.Split(rawCats, ",")
	}
	meta := map[string]any{
		"guid": item.Metadata["guid"],
	}
	if item.Metadata["retracted"] == "true" {
		meta["retracted"] = true
	}
	if item.Metadata["retracts_id"] != "" {
		meta["retracts_id"] = item.Metadata["retracts_id"]
	}
	if item.Metadata["extra_fields"] != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(item.Metadata["extra_fields"]), &extra); err == nil {
			for k, v := range extra {
				meta[k] = v
			}
		}
	}

	sourceVer := "rss-2.0"
	if item.Metadata["feed_format"] == "atom" {
		sourceVer = "atom-1.0"
	}

	return Observation{
		ObservationID: DeterministicObservationID(c.SourceID, sourceVer, item.Locator, contentHash),
		SourceID:      c.SourceID,
		SourceVersion: sourceVer,
		ConnectorType: ConnectorRSS,
		CanonicalURL:  item.Locator,
		Title:         title,
		Summary:       summary,
		Body:          body,
		Language:      firstNonEmpty(item.Metadata["language"], "en"),
		PayloadType:   "text",
		LicenseClass:  firstNonEmpty(c.LicenseClass, "redistribute_allowed"),
		PublishedAt:   item.PublishedAt.UTC(),
		ObservedAt:    item.PublishedAt.UTC(),
		CapturedAt:    now,
		RawPayload:    rawPayload,
		RawHash:       rawHash,
		ContentHash:   contentHash,
		Metadata:      meta,
		Tags:          tags,
	}, nil
}

// Validate verifies required RSS observation invariants.
func (c *RSSConnector) Validate(obs Observation) ValidationResult {
	var reasons []string
	if strings.TrimSpace(obs.Title) == "" && strings.TrimSpace(obs.Summary) == "" {
		reasons = append(reasons, "missing_title_and_summary")
	}
	if strings.TrimSpace(obs.CanonicalURL) == "" {
		reasons = append(reasons, "missing_canonical_url")
	}
	if obs.RawHash == "" || obs.ContentHash == "" {
		reasons = append(reasons, "missing_integrity_hash")
	}
	if len(reasons) > 0 {
		return ValidationResult{
			Valid:           false,
			ValidationState: "quarantined",
			ReasonCodes:     reasons,
		}
	}
	return ValidationResult{
		Valid:           true,
		ValidationState: "accepted",
	}
}

// Normalize converts an RSS observation into canonical UTF-8 NFKC, UTC, PII-gated form.
func (c *RSSConnector) Normalize(obs Observation) (NormalizedObservation, error) {
	return StandardNormalize(obs, nil)
}

func (c *RSSConnector) parseFeed(body []byte) ([]DiscoveredItem, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, nil
	}

	var rss rssDocument
	if err := xml.Unmarshal(body, &rss); err == nil && strings.EqualFold(rss.XMLName.Local, "rss") {
		lang := firstNonEmpty(rss.Channel.Language, "en")
		out := make([]DiscoveredItem, 0, len(rss.Channel.Items))
		for _, it := range rss.Channel.Items {
			title := strings.TrimSpace(it.Title)
			desc := strings.TrimSpace(it.Description)
			if title == "" && desc == "" {
				continue
			}
			guid := firstNonEmpty(it.GUID, it.Link, title)
			locator := firstNonEmpty(it.Link, it.GUID, c.FeedURL+"#"+guid)
			pubAt := parseFlexibleTime(it.PubDate, time.Now().UTC())
			rawJSON, _ := json.Marshal(it)
			fp := HashSHA256Hex([]byte(guid + "|" + pubAt.UTC().Format(time.RFC3339Nano) + "|" + title))

			meta := map[string]string{
				"guid":        guid,
				"description": desc,
				"language":    lang,
				"categories":  strings.Join(it.Categories, ","),
				"feed_format": "rss",
				"raw_json":    string(rawJSON),
			}
			if it.Retracted {
				meta["retracted"] = "true"
			}
			if strings.TrimSpace(it.RetractsID) != "" {
				meta["retracts_id"] = strings.TrimSpace(it.RetractsID)
			}

			out = append(out, DiscoveredItem{
				Locator:     locator,
				Title:       title,
				PublishedAt: pubAt,
				Metadata:    meta,
				Fingerprint: fp,
			})
		}
		return out, nil
	}

	var atom atomDocument
	if err := xml.Unmarshal(body, &atom); err == nil && strings.EqualFold(atom.XMLName.Local, "feed") {
		out := make([]DiscoveredItem, 0, len(atom.Entries))
		for _, entry := range atom.Entries {
			title := strings.TrimSpace(entry.Title)
			summary := firstNonEmpty(entry.Summary, entry.Content)
			if title == "" && summary == "" {
				continue
			}
			// GUID preference: atom:id > guid > link
			guid := firstNonEmpty(entry.ID, entry.Link.Href, title)
			locator := firstNonEmpty(entry.Link.Href, entry.ID, c.FeedURL+"#"+guid)
			pubAt := parseFlexibleTime(firstNonEmpty(entry.Published, entry.Updated), time.Now().UTC())
			var cats []string
			for _, cat := range entry.Categories {
				if strings.TrimSpace(cat.Term) != "" {
					cats = append(cats, strings.TrimSpace(cat.Term))
				}
			}
			rawJSON, _ := json.Marshal(entry)
			fp := HashSHA256Hex([]byte(guid + "|" + pubAt.UTC().Format(time.RFC3339Nano) + "|" + title))

			meta := map[string]string{
				"guid":        guid,
				"description": summary,
				"content":     strings.TrimSpace(entry.Content),
				"language":    "en",
				"categories":  strings.Join(cats, ","),
				"feed_format": "atom",
				"raw_json":    string(rawJSON),
			}
			if entry.Retracted {
				meta["retracted"] = "true"
			}
			if strings.TrimSpace(entry.RetractsID) != "" {
				meta["retracts_id"] = strings.TrimSpace(entry.RetractsID)
			}

			out = append(out, DiscoveredItem{
				Locator:     locator,
				Title:       title,
				PublishedAt: pubAt,
				Metadata:    meta,
				Fingerprint: fp,
			})
		}
		return out, nil
	}

	return nil, fmt.Errorf("invalid RSS/Atom XML payload")
}

func parseRSSCursor(cursor string) (string, time.Time) {
	if strings.TrimSpace(cursor) == "" {
		return "", time.Time{}
	}
	parts := strings.SplitN(cursor, "|", 2)
	guid := strings.TrimSpace(parts[0])
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		return guid, time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(parts[1]))
	if err != nil {
		t, err = time.Parse(time.RFC3339, strings.TrimSpace(parts[1]))
		if err != nil {
			return guid, time.Time{}
		}
	}
	return guid, t.UTC()
}

func formatRSSCursor(guid string, pub time.Time) string {
	if pub.IsZero() {
		return guid
	}
	return guid + "|" + pub.UTC().Format(time.RFC3339Nano)
}
