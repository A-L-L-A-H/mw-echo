package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// IDXBEIConnector implements the Connector contract for BEI/IDX dataset APIs (Section 3.2.4).
// Cursor format: "<record_id>|<ingestion_timestamp_rfc3339>|<page_token>"
type IDXBEIConnector struct {
	SourceID     string
	Endpoint     string
	APIKey       string // external secret sent in X-BEI-API-Key, never persisted in observation/provenance
	DatasetID    string // e.g., company, financial, corporate_action, or IDX.FINANCIAL.QTR
	LicenseClass string
	HTTPClient   *http.Client
	Checkpoint   Checkpoint
}

type idxResponseEnvelope struct {
	DatasetID     string            `json:"dataset_id"`
	NextPageToken string            `json:"next_page_token"`
	Records       []json.RawMessage `json:"records"`
}

var knownIDXFields = map[string]bool{
	"record_id": true, "dataset": true, "dataset_type": true,
	"ticker": true, "company_name": true, "title": true,
	"summary": true, "url": true, "published_at": true,
	"ingestion_timestamp": true, "metrics": true, "period": true,
	"action_type": true, "currency": true, "retracted": true,
	"retracts_id": true,
}

// Type returns ConnectorIDXBEI ("idx_bei").
func (c *IDXBEIConnector) Type() ConnectorType { return ConnectorIDXBEI }

// Discover paginates the BEI endpoint using page_token and X-BEI-API-Key authentication.
func (c *IDXBEIConnector) Discover(ctx context.Context, checkpoint Checkpoint) ([]DiscoveredItem, Checkpoint, error) {
	if strings.TrimSpace(c.Endpoint) == "" {
		return nil, checkpoint, fmt.Errorf("idx_bei endpoint is required")
	}

	prevRecordID, prevIngestTS, pageToken := parseIDXCursor(checkpoint.Cursor)
	var items []DiscoveredItem
	latestRecordID := prevRecordID
	latestTS := prevIngestTS
	nextToken := pageToken

	for page := 0; page < 10; page++ {
		reqURL, err := buildIDXRequestURL(c.Endpoint, c.DatasetID, nextToken)
		if err != nil {
			return nil, checkpoint, err
		}
		headers := map[string]string{
			"Accept": "application/json",
		}
		if strings.TrimSpace(c.APIKey) != "" {
			headers["X-BEI-API-Key"] = strings.TrimSpace(c.APIKey)
		}

		statusCode, _, body, err := doHTTPGet(ctx, c.HTTPClient, reqURL, headers)
		if err != nil {
			return nil, checkpoint, err
		}
		if statusCode == http.StatusTooManyRequests {
			return nil, checkpoint, fmt.Errorf("idx_bei rate limited (http 429)")
		}
		if statusCode < 200 || statusCode >= 300 {
			return nil, checkpoint, fmt.Errorf("idx_bei http status %d", statusCode)
		}
		if strings.TrimSpace(string(body)) == "" {
			break
		}

		var env idxResponseEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, checkpoint, fmt.Errorf("decode idx_bei response: %w", err)
		}

		for _, rawRec := range env.Records {
			var recMap map[string]any
			if err := json.Unmarshal(rawRec, &recMap); err != nil {
				continue
			}
			recID := firstNonEmpty(getStringField(recMap, "record_id"), getStringField(recMap, "id"))
			ticker := strings.ToUpper(getStringField(recMap, "ticker"))
			company := getStringField(recMap, "company_name")
			title := firstNonEmpty(getStringField(recMap, "title"), strings.TrimSpace(ticker+" "+company))
			rawTS := firstNonEmpty(getStringField(recMap, "ingestion_timestamp"), getStringField(recMap, "published_at"))
			pubAt := parseFlexibleTime(rawTS, time.Now().UTC())

			if !prevIngestTS.IsZero() && rawTS != "" {
				if pubAt.Before(prevIngestTS) {
					continue
				}
				if pubAt.Equal(prevIngestTS) && prevRecordID != "" && recID <= prevRecordID {
					continue
				}
			} else if prevRecordID != "" && recID == prevRecordID {
				continue
			}

			locator := firstNonEmpty(getStringField(recMap, "url"), fmt.Sprintf("%s#%s", c.Endpoint, recID))
			fp := HashSHA256Hex([]byte(recID + "|" + pubAt.UTC().Format(time.RFC3339Nano) + "|" + ticker))

			items = append(items, DiscoveredItem{
				Locator:     locator,
				Title:       title,
				PublishedAt: pubAt,
				Metadata: map[string]string{
					"record_id":  recID,
					"dataset_id": firstNonEmpty(env.DatasetID, c.DatasetID),
					"ticker":     ticker,
					"raw_json":   string(rawRec),
				},
				Fingerprint: fp,
			})
			if pubAt.After(latestTS) || (pubAt.Equal(latestTS) && recID > latestRecordID) {
				latestTS = pubAt
				latestRecordID = recID
			}
		}

		nextToken = strings.TrimSpace(env.NextPageToken)
		if nextToken == "" {
			break
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].PublishedAt.Equal(items[j].PublishedAt) {
			return items[i].Metadata["record_id"] < items[j].Metadata["record_id"]
		}
		return items[i].PublishedAt.Before(items[j].PublishedAt)
	})

	nextCP := checkpoint
	nextCP.ConnectorType = ConnectorIDXBEI
	if nextCP.SourceID == "" {
		nextCP.SourceID = c.SourceID
	}
	if len(items) > 0 {
		nextCP.Cursor = fmt.Sprintf("%s|%s|%s", latestRecordID, latestTS.UTC().Format(time.RFC3339Nano), nextToken)
		nextCP.LastSeenAt = latestTS.UTC()
		nextCP.ItemCount = checkpoint.ItemCount + int64(len(items))
		nextCP.Hash = nextCP.ComputeHash()
	}
	c.Checkpoint = nextCP
	return items, nextCP, nil
}

// Acquire materializes an IDX/BEI Observation while preserving unknown schema-drift fields.
func (c *IDXBEIConnector) Acquire(_ context.Context, item DiscoveredItem) (Observation, error) {
	rawPayload := []byte(item.Metadata["raw_json"])
	if len(rawPayload) == 0 {
		rawPayload, _ = json.Marshal(item)
	}
	rawHash := HashSHA256Hex(rawPayload)

	var recMap map[string]any
	_ = json.Unmarshal(rawPayload, &recMap)

	recID := firstNonEmpty(getStringField(recMap, "record_id"), item.Metadata["record_id"])
	datasetType := firstNonEmpty(getStringField(recMap, "dataset_type"), getStringField(recMap, "dataset"), item.Metadata["dataset_id"], c.DatasetID)
	ticker := strings.ToUpper(firstNonEmpty(getStringField(recMap, "ticker"), item.Metadata["ticker"]))
	company := getStringField(recMap, "company_name")
	title := firstNonEmpty(getStringField(recMap, "title"), item.Title)
	summary := firstNonEmpty(getStringField(recMap, "summary"), title)

	meta := map[string]any{
		"record_id":    recID,
		"dataset_type": datasetType,
		"ticker":       ticker,
		"company_name": company,
	}
	var unknownFields []string
	for k, v := range recMap {
		if !knownIDXFields[k] {
			meta[k] = v
			unknownFields = append(unknownFields, k)
		}
	}
	if len(unknownFields) > 0 {
		sort.Strings(unknownFields)
		meta["_unknown_fields"] = unknownFields
	}
	if retracted, ok := recMap["retracted"].(bool); ok && retracted {
		meta["retracted"] = true
	}
	if retractsID := getStringField(recMap, "retracts_id"); retractsID != "" {
		meta["retracts_id"] = retractsID
	}

	var metrics map[string]any
	if m, ok := recMap["metrics"].(map[string]any); ok {
		metrics = m
	}

	var entities []string
	if ticker != "" {
		entities = append(entities, "IDX:"+ticker)
	}
	if company != "" {
		entities = append(entities, company)
	}

	contentHash := HashSHA256Hex([]byte(NormalizeNFKC(title) + "\x00" + NormalizeNFKC(summary)))
	sourceVer := firstNonEmpty(datasetType, "idx-bei-v1")

	return Observation{
		ObservationID: DeterministicObservationID(c.SourceID, sourceVer, item.Locator, contentHash),
		SourceID:      c.SourceID,
		SourceVersion: sourceVer,
		ConnectorType: ConnectorIDXBEI,
		CanonicalURL:  item.Locator,
		Title:         title,
		Summary:       summary,
		Body:          summary,
		Language:      "id",
		PayloadType:   "structured_record",
		LicenseClass:  firstNonEmpty(c.LicenseClass, "internal_only"),
		PublishedAt:   item.PublishedAt.UTC(),
		ObservedAt:    item.PublishedAt.UTC(),
		CapturedAt:    time.Now().UTC(),
		RawPayload:    rawPayload,
		RawHash:       rawHash,
		ContentHash:   contentHash,
		Metadata:      meta,
		Metrics:       metrics,
		Entities:      entities,
		Tags:          []string{"D02", "D03", "L12"},
	}, nil
}

// Validate checks IDX/BEI required dataset fields (record_id, ticker/company_name) and detects schema drift.
func (c *IDXBEIConnector) Validate(obs Observation) ValidationResult {
	var reasons []string
	if strings.TrimSpace(getStringFromMeta(obs.Metadata, "record_id")) == "" {
		reasons = append(reasons, "missing_required_field:record_id")
	}
	if strings.TrimSpace(getStringFromMeta(obs.Metadata, "ticker")) == "" &&
		strings.TrimSpace(getStringFromMeta(obs.Metadata, "company_name")) == "" {
		reasons = append(reasons, "missing_required_field:ticker_or_company_name")
	}
	if strings.TrimSpace(obs.Title) == "" {
		reasons = append(reasons, "missing_required_field:title")
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

// Normalize canonicalizes the IDX/BEI observation.
func (c *IDXBEIConnector) Normalize(obs Observation) (NormalizedObservation, error) {
	return StandardNormalize(obs, []string{"D02", "D03"})
}

func buildIDXRequestURL(endpoint, datasetID, pageToken string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", fmt.Errorf("invalid idx_bei endpoint: %w", err)
	}
	q := u.Query()
	if strings.TrimSpace(datasetID) != "" {
		q.Set("dataset_id", strings.TrimSpace(datasetID))
	}
	if strings.TrimSpace(pageToken) != "" {
		q.Set("page_token", strings.TrimSpace(pageToken))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func parseIDXCursor(cursor string) (string, time.Time, string) {
	if strings.TrimSpace(cursor) == "" {
		return "", time.Time{}, ""
	}
	parts := strings.SplitN(cursor, "|", 3)
	recID := strings.TrimSpace(parts[0])
	var ts time.Time
	var pageToken string
	if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
		if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(parts[1])); err == nil {
			ts = t.UTC()
		} else if t, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[1])); err == nil {
			ts = t.UTC()
		}
	}
	if len(parts) >= 3 {
		pageToken = strings.TrimSpace(parts[2])
	}
	return recID, ts, pageToken
}
