package connectors_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/worldradar/connectors"
)

func TestRSSConnector_DiscoverAcquireValidateNormalizeAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Wire</title>
    <item>
      <title>Ｆｉｂｅｒ   Outage Report</title>
      <link>https://wire.example/item-1</link>
      <guid>guid-1</guid>
      <pubDate>Thu, 26 Nov 2026 10:00:00 +0200</pubDate>
      <description>Contact ops@wire.example or @netops_lead</description>
      <category>D10</category>
    </item>
    <item>
      <title>Second Item</title>
      <link>https://wire.example/item-2</link>
      <guid>guid-2</guid>
      <pubDate>Thu, 26 Nov 2026 11:00:00 GMT</pubDate>
      <description>Follow-up advisory</description>
      <category>D22</category>
    </item>
  </channel>
</rss>`))
	}))
	defer srv.Close()

	conn := &connectors.RSSConnector{
		SourceID:   "src-rss-1",
		FeedURL:    srv.URL,
		HTTPClient: srv.Client(),
	}
	if conn.Type() != connectors.ConnectorRSS {
		t.Fatalf("expected type rss, got %s", conn.Type())
	}

	items, cp1, err := conn.Discover(ctx, connectors.Checkpoint{SourceID: "src-rss-1"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 discovered items, got %d", len(items))
	}
	if !cp1.VerifyHash() || cp1.ItemCount != 2 {
		t.Fatalf("invalid checkpoint: %+v", cp1)
	}

	// Idempotent second discover with cp1 yields 0 items.
	items2, cp2, err := conn.Discover(ctx, cp1)
	if err != nil {
		t.Fatalf("second Discover: %v", err)
	}
	if len(items2) != 0 || cp2.Cursor != cp1.Cursor {
		t.Fatalf("expected 0 new items on same checkpoint, got %d", len(items2))
	}

	obs, err := conn.Acquire(ctx, items[0])
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if val := conn.Validate(obs); !val.Valid || val.ValidationState != "accepted" {
		t.Fatalf("Validate failed: %+v", val)
	}
	norm, err := conn.Normalize(obs)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if norm.Title != "Fiber Outage Report" {
		t.Fatalf("expected NFKC normalized title 'Fiber Outage Report', got %q", norm.Title)
	}
	if norm.PublishedAt.Location() != time.UTC || norm.PublishedAt.Hour() != 8 {
		t.Fatalf("expected UTC 08:00 conversion from +0200, got %s", norm.PublishedAt)
	}
	if strings.Contains(norm.Summary, "ops@wire.example") || strings.Contains(norm.Summary, "@netops_lead") {
		t.Fatalf("expected PII redacted in summary, got %q", norm.Summary)
	}
	if !strings.Contains(norm.Summary, "[REDACTED]") || !strings.Contains(norm.Summary, "anon_") {
		t.Fatalf("expected [REDACTED] and anon_ pseudonym in summary, got %q", norm.Summary)
	}
}

func TestGitHubReleasesConnector_PaginationDraftFilterAndSecretGuard(t *testing.T) {
	ctx := context.Background()
	const secretPAT = "ghp_superSecretTokenValue12345"

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secretPAT {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "" || page == "1" {
			w.Header().Set("Link", `<`+srv.URL+`/repos/acme/core/releases?page=2>; rel="next"`)
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":           101,
					"tag_name":     "v1.0.0",
					"name":         "Acme Core v1.0.0",
					"html_url":     "https://github.com/acme/core/releases/tag/v1.0.0",
					"body":         "Production release",
					"draft":        false,
					"prerelease":   false,
					"published_at": "2026-11-25T10:00:00Z",
				},
				{
					"id":           102,
					"tag_name":     "v1.1.0-draft",
					"name":         "Draft",
					"html_url":     "https://github.com/acme/core/releases/tag/v1.1.0-draft",
					"draft":        true,
					"prerelease":   false,
					"published_at": "2026-11-26T10:00:00Z",
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":           103,
				"tag_name":     "v1.1.0",
				"name":         "Acme Core v1.1.0",
				"html_url":     "https://github.com/acme/core/releases/tag/v1.1.0",
				"body":         "Second page release",
				"draft":        false,
				"prerelease":   false,
				"published_at": "2026-11-26T12:00:00Z",
				"assets": []map[string]any{
					{"id": 1, "name": "core-linux.tar.gz", "size": 1024},
				},
			},
		})
	}))
	defer srv.Close()

	conn := &connectors.GitHubReleasesConnector{
		SourceID:   "src-gh-1",
		Owner:      "acme",
		Repo:       "core",
		Token:      secretPAT,
		APIBase:    srv.URL,
		HTTPClient: srv.Client(),
	}
	items, cp, err := conn.Discover(ctx, connectors.Checkpoint{SourceID: "src-gh-1"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 non-draft releases across 2 pages, got %d", len(items))
	}
	if !strings.HasPrefix(cp.Cursor, "103|") {
		t.Fatalf("expected cursor starting with 103|, got %q", cp.Cursor)
	}

	obs, err := conn.Acquire(ctx, items[1])
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	norm, err := conn.Normalize(obs)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	rawObsJSON, _ := json.Marshal(norm)
	if strings.Contains(string(rawObsJSON), secretPAT) {
		t.Fatal("secret PAT leaked into normalized observation")
	}
}

func TestWebFeedJSONConnector_ValidationAndSchemaDrift(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/feed+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "https://jsonfeed.org/version/1.1",
			"title":   "Global Feed",
			"items": []map[string]any{
				{
					"id":             "jf-1",
					"url":            "https://feed.example/jf-1",
					"title":          "Grid Expansion Milestone",
					"content_text":   "100MW storage online",
					"date_published": "2026-11-26T09:00:00Z",
					"new_metric":     42.5,
					"new_name":       "renamed-field-value",
				},
			},
		})
	}))
	defer srv.Close()

	conn := &connectors.WebFeedJSONConnector{
		SourceID:   "src-jf-1",
		FeedURL:    srv.URL,
		Schema:     connectors.JSONFeedSchema11,
		HTTPClient: srv.Client(),
	}
	items, cp, err := conn.Discover(ctx, connectors.Checkpoint{SourceID: "src-jf-1"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(items) != 1 || cp.ItemCount != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	obs, err := conn.Acquire(ctx, items[0])
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	val := conn.Validate(obs)
	if !val.Valid || !val.SchemaDrift || len(val.UnknownFields) != 2 {
		t.Fatalf("expected valid with schema drift and 2 unknown fields, got %+v", val)
	}
	if obs.Metadata["new_metric"] != 42.5 || obs.Metadata["new_name"] != "renamed-field-value" {
		t.Fatalf("expected unknown fields retained in metadata, got %+v", obs.Metadata)
	}
}

func TestIDXBEIConnector_PaginationAuthAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	const apiKey = "bei-secret-key-999"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-BEI-API-Key") != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		tok := r.URL.Query().Get("page_token")
		w.Header().Set("Content-Type", "application/json")
		if tok == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"dataset_id":      "IDX.FINANCIAL.QTR",
				"next_page_token": "page-2",
				"records": []map[string]any{
					{
						"record_id":           "bei-001",
						"dataset_type":        "financial",
						"ticker":              "BBCA",
						"company_name":        "Bank Central Asia Tbk",
						"title":               "BBCA Q3 2026 Earnings",
						"summary":             "Net profit rose 12%",
						"url":                 "https://idx.co.id/bei-001",
						"ingestion_timestamp": "2026-11-26T08:00:00Z",
					},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"dataset_id":      "IDX.FINANCIAL.QTR",
			"next_page_token": "",
			"records": []map[string]any{
				{
					"record_id":           "bei-002",
					"dataset_type":        "corporate_action",
					"ticker":              "TLKM",
					"company_name":        "Telkom Indonesia Tbk",
					"title":               "TLKM Interim Dividend",
					"summary":             "Dividend declared",
					"url":                 "https://idx.co.id/bei-002",
					"ingestion_timestamp": "2026-11-26T09:00:00Z",
				},
			},
		})
	}))
	defer srv.Close()

	conn := &connectors.IDXBEIConnector{
		SourceID:   "src-bei-1",
		Endpoint:   srv.URL,
		APIKey:     apiKey,
		DatasetID:  "IDX.FINANCIAL.QTR",
		HTTPClient: srv.Client(),
	}
	items, cp, err := conn.Discover(ctx, connectors.Checkpoint{SourceID: "src-bei-1"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(items) != 2 || cp.ItemCount != 2 {
		t.Fatalf("expected 2 records across pages, got %d", len(items))
	}
	obs, err := conn.Acquire(ctx, items[0])
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if val := conn.Validate(obs); !val.Valid {
		t.Fatalf("Validate failed: %+v", val)
	}
	norm, err := conn.Normalize(obs)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	b, _ := json.Marshal(norm)
	if strings.Contains(string(b), apiKey) {
		t.Fatal("X-BEI-API-Key leaked into normalized observation")
	}
}
