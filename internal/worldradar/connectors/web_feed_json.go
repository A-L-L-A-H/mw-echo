package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// JSONFeedSchema specifies JSON Feed 1.1 or custom JSON feed schema (Section 3.2.3).
type JSONFeedSchema string

const (
	JSONFeedSchema11     JSONFeedSchema = "jsonfeed_1.1"
	JSONFeedSchemaCustom JSONFeedSchema = "custom"
)

// WebFeedJSONConnector implements the Connector contract for JSON Feed 1.1 and custom JSON feeds.
// Cursor format: "<item_id>|<date_published_rfc3339>|<feed_hash>"
type WebFeedJSONConnector struct {
	SourceID     string
	FeedURL      string
	Schema       JSONFeedSchema
	LicenseClass string
	HTTPClient   *http.Client
	Checkpoint   Checkpoint
}

type jsonFeedDoc struct {
	Version     string            `json:"version"`
	Title       string            `json:"title"`
	HomePageURL string            `json:"home_page_url"`
	FeedURL     string            `json:"feed_url"`
	Authors     []jsonFeedAuthor  `json:"authors"`
	Hubs        []jsonFeedHub     `json:"hubs"`
	Items       []json.RawMessage `json:"items"`
}

type jsonFeedAuthor struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type jsonFeedHub struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type jsonFeedAttachment struct {
	URL       string `json:"url"`
	MimeType  string `json:"mime_type"`
	Title     string `json:"title,omitempty"`
	SizeBytes int64  `json:"size_in_bytes,omitempty"`
}

var knownWebFeedItemFields = map[string]bool{
	"id": true, "url": true, "external_url": true, "title": true,
	"content_html": true, "content_text": true, "summary": true,
	"image": true, "banner_image": true, "date_published": true,
	"date_modified": true, "publishedAt": true, "authors": true,
	"tags": true, "language": true, "attachments": true,
	"metrics": true, "place": true, "entities": true,
	"retracted": true, "retracts_id": true,
}

// Type returns ConnectorWebFeedJSON ("web_feed_json").
func (c *WebFeedJSONConnector) Type() ConnectorType { return ConnectorWebFeedJSON }

// Discover fetches the JSON Feed 1.1 document and emits unseen items with deterministic cursoring.
func (c *WebFeedJSONConnector) Discover(ctx context.Context, checkpoint Checkpoint) ([]DiscoveredItem, Checkpoint,
	error) {
	if strings.TrimSpace(c.FeedURL) == "" {
		return nil, checkpoint, fmt.Errorf("web_feed_json feed URL is required")
	}
	statusCode, _, body, err := doHTTPGet(ctx, c.HTTPClient, c.FeedURL, map[string]string{
		"Accept": "application/feed+json, application/json",
	})
	if err != nil {
		return nil, checkpoint, err
	}
	if statusCode == http.StatusNotModified {
		return nil, checkpoint, nil
	}
	if statusCode == http.StatusTooManyRequests {
		return nil, checkpoint, fmt.Errorf("web_feed_json rate limited (http 429)")
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, checkpoint, fmt.Errorf("web_feed_json http status %d", statusCode)
	}

	if strings.TrimSpace(string(body)) == "" {
		return nil, checkpoint, nil
	}

	var doc jsonFeedDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, checkpoint, fmt.Errorf("decode web_feed_json: %w", err)
	}
	feedHash := HashSHA256Hex(body)
	prevID, prevPub, prevFeedHash := parseWebFeedCursor(checkpoint.Cursor)
	if prevFeedHash != "" && prevFeedHash == feedHash {
		return nil, checkpoint, nil
	}

	items := make([]DiscoveredItem, 0, len(doc.Items))
	for _, rawItem := range doc.Items {
		var rawMap map[string]any
		if err := json.Unmarshal(rawItem, &rawMap); err != nil {
			continue
		}
		itemID := getStringField(rawMap, "id")
		itemURL := firstNonEmpty(getStringField(rawMap, "url"), getStringField(rawMap, "external_url"))
		title := getStringField(rawMap, "title")
		rawDate := firstNonEmpty(getStringField(rawMap, "date_published"), getStringField(rawMap, "publishedAt"),
			getStringField(rawMap, "date_modified"))
		pubAt := parseFlexibleTime(rawDate, time.Time{})

		fp := HashSHA256Hex([]byte(itemID + "|" + rawDate + "|" + title))
		items = append(items, DiscoveredItem{
			Locator:     firstNonEmpty(itemURL, itemID),
			Title:       title,
			PublishedAt: pubAt,
			Metadata: map[string]string{
				"item_id":        itemID,
				"item_url":       itemURL,
				"date_published": rawDate,
				"feed_hash":      feedHash,
				"raw_json":       string(rawItem),
			},
			Fingerprint: fp,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].PublishedAt.Equal(items[j].PublishedAt) {
			return items[i].Metadata["item_id"] < items[j].Metadata["item_id"]
		}
		return items[i].PublishedAt.Before(items[j].PublishedAt)
	})

	filtered := make([]DiscoveredItem, 0, len(items))
	latestID := prevID
	latestPub := prevPub
	for _, it := range items {
		iid := it.Metadata["item_id"]
		if !prevPub.IsZero() && !it.PublishedAt.IsZero() {
			if it.PublishedAt.Before(prevPub) {
				continue
			}
			if it.PublishedAt.Equal(prevPub) && prevID != "" && iid <= prevID {
				continue
			}
		} else if prevID != "" && iid == prevID {
			continue
		}
		filtered = append(filtered, it)
		if it.PublishedAt.After(latestPub) || (it.PublishedAt.Equal(latestPub) && iid > latestID) {
			latestPub = it.PublishedAt
			latestID = iid
		}
	}

	nextCP := checkpoint
	nextCP.ConnectorType = ConnectorWebFeedJSON
	if nextCP.SourceID == "" {
		nextCP.SourceID = c.SourceID
	}
	if len(filtered) > 0 {
		nextCP.Cursor = fmt.Sprintf("%s|%s|%s", latestID, latestPub.UTC().Format(time.RFC3339Nano), feedHash)
		if latestPub.IsZero() {
			nextCP.LastSeenAt = time.Now().UTC()
		} else {
			nextCP.LastSeenAt = latestPub.UTC()
		}
		nextCP.ItemCount = checkpoint.ItemCount + int64(len(filtered))
		nextCP.Hash = nextCP.ComputeHash()
	}
	c.Checkpoint = nextCP
	return filtered, nextCP, nil
}

// Acquire parses the full JSON Feed item while preserving unknown schema-drift fields in Metadata.
func (c *WebFeedJSONConnector) Acquire(_ context.Context, item DiscoveredItem) (Observation, error) {
	rawPayload := []byte(item.Metadata["raw_json"])
	if len(rawPayload) == 0 {
		rawPayload, _ = json.Marshal(item)
	}
	rawHash := HashSHA256Hex(rawPayload)

	var rawMap map[string]any
	_ = json.Unmarshal(rawPayload, &rawMap)

	title := firstNonEmpty(getStringField(rawMap, "title"), item.Title)
	summary := firstNonEmpty(getStringField(rawMap, "summary"), getStringField(rawMap, "content_text"))
	body := firstNonEmpty(getStringField(rawMap, "content_text"), getStringField(rawMap, "content_html"), summary, title)
	lang := firstNonEmpty(getStringField(rawMap, "language"), "en")
	canonicalURL := firstNonEmpty(getStringField(rawMap, "url"), getStringField(rawMap, "external_url"), item.Locator)
	itemID := firstNonEmpty(getStringField(rawMap, "id"), item.Metadata["item_id"])
	datePublished := firstNonEmpty(getStringField(rawMap, "date_published"), getStringField(rawMap, "publishedAt"))

	meta := map[string]any{
		"item_id":        itemID,
		"date_published": datePublished,
	}
	var unknownFields []string
	for k, v := range rawMap {
		if !knownWebFeedItemFields[k] {
			meta[k] = v
			unknownFields = append(unknownFields, k)
		}
	}
	if len(unknownFields) > 0 {
		sort.Strings(unknownFields)
		meta["_unknown_fields"] = unknownFields
	}
	if attachments, ok := rawMap["attachments"]; ok {
		meta["attachments"] = attachments
	}
	if authors, ok := rawMap["authors"]; ok {
		meta["authors"] = authors
	}
	if retracted, ok := rawMap["retracted"].(bool); ok && retracted {
		meta["retracted"] = true
	}
	if retractsID := getStringField(rawMap, "retracts_id"); retractsID != "" {
		meta["retracts_id"] = retractsID
	}

	var metrics map[string]any
	if m, ok := rawMap["metrics"].(map[string]any); ok {
		metrics = m
	}
	var place map[string]any
	if p, ok := rawMap["place"].(map[string]any); ok {
		place = p
	}
	var entities []string
	if ents, ok := rawMap["entities"].([]any); ok {
		for _, e := range ents {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				entities = append(entities, strings.TrimSpace(s))
			}
		}
	}
	var tags []string
	if rawTags, ok := rawMap["tags"].([]any); ok {
		for _, tg := range rawTags {
			if s, ok := tg.(string); ok && strings.TrimSpace(s) != "" {
				tags = append(tags, strings.TrimSpace(s))
			}
		}
	}

	contentHash := HashSHA256Hex([]byte(NormalizeNFKC(title) + "\x00" + NormalizeNFKC(summary) + "\x00" + NormalizeNFKC(body)))
	schemaVer := firstNonEmpty(string(c.Schema), string(JSONFeedSchema11))

	return Observation{
		ObservationID: DeterministicObservationID(c.SourceID, schemaVer, canonicalURL, contentHash),
		SourceID:      c.SourceID,
		SourceVersion: schemaVer,
		ConnectorType: ConnectorWebFeedJSON,
		CanonicalURL:  canonicalURL,
		Title:         title,
		Summary:       summary,
		Body:          body,
		Language:      lang,
		PayloadType:   "structured_record",
		LicenseClass:  firstNonEmpty(c.LicenseClass, "redistribute_allowed"),
		PublishedAt:   item.PublishedAt.UTC(),
		ObservedAt:    item.PublishedAt.UTC(),
		CapturedAt:    time.Now().UTC(),
		RawPayload:    rawPayload,
		RawHash:       rawHash,
		ContentHash:   contentHash,
		Metadata:      meta,
		Metrics:       metrics,
		Entities:      entities,
		Place:         place,
		Tags:          tags,
	}, nil
}

// Validate checks required JSON Feed 1.1 fields (id, url, title, date_published) and detects schema drift.
func (c *WebFeedJSONConnector) Validate(obs Observation) ValidationResult {
	var reasons []string
	if strings.TrimSpace(getStringFromMeta(obs.Metadata, "item_id")) == "" {
		reasons = append(reasons, "missing_required_field:id")
	}
	if strings.TrimSpace(obs.CanonicalURL) == "" {
		reasons = append(reasons, "missing_required_field:url")
	}
	if strings.TrimSpace(obs.Title) == "" {
		reasons = append(reasons, "missing_required_field:title")
	}
	if strings.TrimSpace(getStringFromMeta(obs.Metadata, "date_published")) == "" && obs.PublishedAt.IsZero() {
		reasons = append(reasons, "missing_required_field:date_published")
	}

	var unknownFields []string
	if uf, ok := obs.Metadata["_unknown_fields"].([]string); ok {
		unknownFields = uf
	}
	drift := len(unknownFields) > 0

	if len(reasons) > 0 {
		return ValidationResult{
			Valid:           false,
			ValidationState: "quarantined",
			ReasonCodes:     reasons,
			SchemaDrift:     drift,
			UnknownFields:   unknownFields,
		}
	}
	return ValidationResult{
		Valid:           true,
		ValidationState: "accepted",
		SchemaDrift:     drift,
		UnknownFields:   unknownFields,
	}
}

// Normalize canonicalizes the Web Feed JSON observation.
func (c *WebFeedJSONConnector) Normalize(obs Observation) (NormalizedObservation, error) {
	return StandardNormalize(obs, nil)
}

func parseWebFeedCursor(cursor string) (string, time.Time, string) {
	if strings.TrimSpace(cursor) == "" {
		return "", time.Time{}, ""
	}
	parts := strings.SplitN(cursor, "|", 3)
	id := strings.TrimSpace(parts[0])
	var pub time.Time
	var feedHash string
	if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
		if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(parts[1])); err == nil {
			pub = t.UTC()
		} else if t, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[1])); err == nil {
			pub = t.UTC()
		}
	}
	if len(parts) >= 3 {
		feedHash = strings.TrimSpace(parts[2])
	}
	return id, pub, feedHash
}

func getStringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func getStringFromMeta(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}
