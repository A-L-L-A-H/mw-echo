package worldradar_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/A-L-L-A-H/mw-echo/internal/worldradar"
	"github.com/labstack/echo/v4"
)

func openTestEngine(t *testing.T, opts worldradar.Options) (*worldradar.Engine, *store.HubStore) {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	eng, err := worldradar.NewEngine(hub, opts)
	if err != nil {
		t.Fatalf("new worldradar engine: %v", err)
	}
	return eng, hub
}

func TestTaxonomyAndPackValidation(t *testing.T) {
	domains := worldradar.AllDomains()
	if len(domains) != 23 {
		t.Fatalf("expected 23 canonical domains, got %d", len(domains))
	}
	lenses := worldradar.AllLenses()
	if len(lenses) != 20 {
		t.Fatalf("expected 20 detection lenses, got %d", len(lenses))
	}
	packs := worldradar.AllPacks()
	if len(packs) != 8 {
		t.Fatalf("expected 8 alpha packs, got %d", len(packs))
	}
	for _, pack := range packs {
		if err := worldradar.ValidatePack(pack); err != nil {
			t.Fatalf("alpha pack %s failed validation: %v", pack.ID, err)
		}
	}

	if err := worldradar.ValidatePack(worldradar.PackManifest{
		ID:      "invalid-id",
		Name:    "Bad",
		Domains: []string{"D01"},
		Lenses:  []string{"L01"},
	}); err == nil {
		t.Fatal("expected invalid pack id to be rejected")
	}

	if err := worldradar.ValidatePack(worldradar.PackManifest{
		ID:      "radar.bad-domain",
		Name:    "Bad Domain",
		Domains: []string{"D99"},
		Lenses:  []string{"L01"},
	}); err == nil {
		t.Fatal("expected unknown domain to be rejected")
	}

	if err := worldradar.ValidatePack(worldradar.PackManifest{
		ID:      "radar.bad-lens",
		Name:    "Bad Lens",
		Domains: []string{"D07"},
		Lenses:  []string{"L99"},
	}); err == nil {
		t.Fatal("expected unknown lens to be rejected")
	}
}

func TestScenario1_SyndicatedRSSFeedsSingleSourceAndZeroDuplicateArtifacts(t *testing.T) {
	ctx := context.Background()
	eng, hub := openTestEngine(t, worldradar.Options{})

	buildFeed := func(linkURL string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Syndicate Mirror</title>
    <item>
      <title>Major Undersea Cable Cut Disrupts Regional Transit</title>
      <link>` + linkURL + `</link>
      <guid>story-cable-cut-001</guid>
      <pubDate>Thu, 26 Nov 2026 10:00:00 GMT</pubDate>
      <description>Three regional carriers rerouted traffic after an undersea fiber outage.</description>
    </item>
  </channel>
</rss>`
	}

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(buildFeed("https://mirror-a.example/cable-cut")))
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(buildFeed("https://mirror-b.example/cable-cut")))
	}))
	defer srvB.Close()
	srvC := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(buildFeed("https://mirror-c.example/cable-cut")))
	}))
	defer srvC.Close()

	inst, err := eng.InstallPack(ctx, "radar.cloud-isp-infra", "Syndication Check", "monitor")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}

	for idx, feedURL := range []string{srvA.URL, srvB.URL, srvC.URL} {
		_, addErr := eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
			TargetType:        "feed",
			CanonicalName:     "Mirror " + string(rune('A'+idx)),
			ConnectorID:       "rss",
			SourceURI:         feedURL,
			IndependenceGroup: "wire-syndicate",
		})
		if addErr != nil {
			t.Fatalf("add target %d: %v", idx, addErr)
		}
	}

	summary, err := eng.RunInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("run instance: %v", err)
	}
	if summary.EventsTouched != 1 {
		t.Fatalf("expected 1 clustered event, got %d", summary.EventsTouched)
	}

	events, err := eng.ListEvents(ctx, "", 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event row, got %d", len(events))
	}

	detail, err := eng.GetEventDetail(ctx, events[0].ID)
	if err != nil {
		t.Fatalf("get event detail: %v", err)
	}
	if len(detail.Evidence) != 3 {
		t.Fatalf("expected 3 radar_event_evidence rows, got %d", len(detail.Evidence))
	}
	if detail.Assessment == nil {
		t.Fatal("expected non-nil assessment")
	}
	if detail.Assessment.ConfidenceLabel != "single_source" {
		t.Fatalf("expected confidence single_source for shared syndicate group, got %s", detail.Assessment.ConfidenceLabel)
	}
	if detail.Assessment.IndependentSources != 1 {
		t.Fatalf("expected independent_sources=1, got %d", detail.Assessment.IndependentSources)
	}

	// Re-run all 3 feeds -> 0 duplicate radar_raw_artifacts
	if _, err := eng.RunInstance(ctx, inst.ID); err != nil {
		t.Fatalf("second run instance: %v", err)
	}
	var artifactCount int
	if err := hub.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_raw_artifacts`).Scan(&artifactCount); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if artifactCount != 3 {
		t.Fatalf("expected 3 deduplicated artifacts after re-run, got %d", artifactCount)
	}
}

func TestScenario2_RepublishedOldStoryPreservesOccurredAt(t *testing.T) {
	ctx := context.Background()
	eng, _ := openTestEngine(t, worldradar.Options{})

	oldOccurred := "2024-05-01T08:30:00Z"
	todayPub := time.Now().UTC().Format(time.RFC1123)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rssXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Archive Wire</title>
    <item>
      <title>Retrospective on 2024 Grid Incident</title>
      <link>https://archive.example/grid-2024</link>
      <guid>grid-2024-republish</guid>
      <pubDate>` + todayPub + `</pubDate>
      <occurredAt>` + oldOccurred + `</occurredAt>
      <description>Republished analysis of the May 2024 grid frequency event.</description>
    </item>
  </channel>
</rss>`
		_, _ = w.Write([]byte(rssXML))
	}))
	defer srv.Close()

	inst, err := eng.InstallPack(ctx, "radar.world-events", "Archive Check", "monitor")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	_, err = eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
		CanonicalName: "Archive RSS",
		ConnectorID:   "rss",
		SourceURI:     srv.URL,
	})
	if err != nil {
		t.Fatalf("add target: %v", err)
	}

	if _, err := eng.RunInstance(ctx, inst.ID); err != nil {
		t.Fatalf("run instance: %v", err)
	}

	events, err := eng.ListEvents(ctx, "", 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("expected 1 event, got %d (err=%v)", len(events), err)
	}
	ev := events[0]
	if !strings.HasPrefix(ev.OccurredAt, "2024-05-01T08:30:00") {
		t.Fatalf("expected occurred_at preserved as 2024-05-01T08:30:00Z, got %s", ev.OccurredAt)
	}
	pubTime, err := time.Parse(time.RFC3339Nano, ev.PublishedAt)
	if err != nil {
		t.Fatalf("parse published_at: %v", err)
	}
	obsTime, err := time.Parse(time.RFC3339Nano, ev.ObservedAt)
	if err != nil {
		t.Fatalf("parse observed_at: %v", err)
	}
	if pubTime.Year() < 2026 || obsTime.Year() < 2026 {
		t.Fatalf("expected current published_at and observed_at, got pub=%s obs=%s", ev.PublishedAt, ev.ObservedAt)
	}
}

func TestScenario3_EmptyFeedAnd304ReturnNoNewDataWithoutFakeEvents(t *testing.T) {
	ctx := context.Background()
	eng, _ := openTestEngine(t, worldradar.Options{})

	var callCount atomic.Int32
	srvConditional := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			w.Header().Set("ETag", `"etag-v1"`)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Empty</title></channel></rss>`))
			return
		}
		if r.Header.Get("If-None-Match") != `"etag-v1"` {
			t.Errorf("expected If-None-Match header %q, got %q", `"etag-v1"`, r.Header.Get("If-None-Match"))
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srvConditional.Close()

	inst, err := eng.InstallPack(ctx, "radar.open-source-dev", "304 Check", "monitor")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	_, err = eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
		CanonicalName: "Empty/304 Feed",
		ConnectorID:   "rss",
		SourceURI:     srvConditional.URL,
	})
	if err != nil {
		t.Fatalf("add target: %v", err)
	}

	firstRun, err := eng.RunInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if firstRun.Status != worldradar.RunStatusNoNewData ||
		firstRun.TargetReports[0].Status != worldradar.RunStatusNoNewData {
		t.Fatalf("expected no_new_data on empty feed, got %+v", firstRun)
	}

	secondRun, err := eng.RunInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if secondRun.Status != worldradar.RunStatusNoNewData ||
		secondRun.TargetReports[0].Status != worldradar.RunStatusNoNewData {
		t.Fatalf("expected no_new_data on 304, got %+v", secondRun)
	}

	events, err := eng.ListEvents(ctx, "", 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events for empty/304 runs, got %d", len(events))
	}
}

func TestScenario4_RateLimit429PreservesCursorAndRecordsConnectorHealth(t *testing.T) {
	ctx := context.Background()

	var upstreamHits atomic.Int32
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Retry-After", "120")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ghServer.Close()

	eng, _ := openTestEngine(t, worldradar.Options{GitHubAPIBase: ghServer.URL})

	inst, err := eng.InstallPack(ctx, "radar.open-source-dev", "RateLimit Check", "monitor")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	tgt, err := eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
		TargetType:    "repository",
		CanonicalName: "acme/core",
		ConnectorID:   "github",
		SourceURI:     "acme/core",
		Cursor:        "cursor-checkpoint-42",
	})
	if err != nil {
		t.Fatalf("add target: %v", err)
	}

	summary, err := eng.RunInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("run instance: %v", err)
	}
	if len(summary.TargetReports) != 1 || summary.TargetReports[0].Status != worldradar.RunStatusRateLimited {
		t.Fatalf("expected rate_limited target status, got %+v", summary.TargetReports)
	}

	targets, err := eng.ListWatchTargets(ctx, inst.ID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("list watch targets: %v", err)
	}
	if targets[0].Cursor != tgt.Cursor || targets[0].Cursor != "cursor-checkpoint-42" {
		t.Fatalf("expected preserved cursor %q, got %q", "cursor-checkpoint-42", targets[0].Cursor)
	}

	healthList, err := eng.ListConnectorHealth(ctx)
	if err != nil {
		t.Fatalf("list connector health: %v", err)
	}
	if len(healthList) != 1 || healthList[0].LastStatus != worldradar.RunStatusRateLimited ||
		healthList[0].RateLimitedUntil == "" {
		t.Fatalf("expected rate_limited connector health with rate_limited_until, got %+v", healthList)
	}

	// Subsequent immediate run must respect rate-limit backoff and NOT hit upstream again.
	summaryBackoff, err := eng.RunInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("backoff run: %v", err)
	}
	if summaryBackoff.TargetReports[0].Status != worldradar.RunStatusRateLimited {
		t.Fatalf("expected backoff rate_limited status, got %+v", summaryBackoff)
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("expected upstream called once due to rate-limit backoff, got %d", upstreamHits.Load())
	}
}

func TestScenario5_StrictEntityResolutionDoesNotBlindlyMerge(t *testing.T) {
	ctx := context.Background()
	eng, _ := openTestEngine(t, worldradar.Options{})

	feedPayload := map[string]any{
		"items": []map[string]any{
			{
				"id":          "ent-1",
				"title":       "Acme Corp Opens New Fab",
				"url":         "https://news.example/acme-1",
				"publishedAt": "2026-11-26T09:00:00Z",
				"summary":     "Acme Corp US expansion.",
				"entity":      "Acme Corp",
				"strongId":    "LEI:1111",
			},
			{
				"id":          "ent-2",
				"title":       "Acme Corp Files Bankruptcy in Europe",
				"url":         "https://news.example/acme-2",
				"publishedAt": "2026-11-26T09:05:00Z",
				"summary":     "Unrelated Acme Corp entity in EU.",
				"entity":      "Acme Corp",
				"strongId":    "LEI:2222",
			},
			{
				"id":          "ent-3",
				"title":       "Acme Corp Mentioned Without Registry ID",
				"url":         "https://news.example/acme-3",
				"publishedAt": "2026-11-26T09:10:00Z",
				"summary":     "Ambiguous Acme Corp mention with missing strongId.",
				"entity":      "Acme Corp",
				"strongId":    "",
			},
			{
				"id":          "ent-4",
				"title":       "Acme Corp Releases Q4 Output",
				"url":         "https://news.example/acme-4",
				"publishedAt": "2026-11-26T09:15:00Z",
				"summary":     "Matches LEI:1111 and canonical name.",
				"entity":      "Acme Corp",
				"strongId":    "LEI:1111",
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(feedPayload)
	}))
	defer srv.Close()

	inst, err := eng.InstallPack(ctx, "radar.law-regulation", "Entity Strictness", "monitor")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	if _, err := eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
		CanonicalName: "Corporate Registry Feed",
		ConnectorID:   "web_feed",
		SourceURI:     srv.URL,
	}); err != nil {
		t.Fatalf("add target: %v", err)
	}

	if _, err := eng.RunInstance(ctx, inst.ID); err != nil {
		t.Fatalf("run instance: %v", err)
	}

	entities, err := eng.ListEntities(ctx, "", 50)
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	// ent-1 (LEI:1111) + ent-4 (LEI:1111) merge -> 1; ent-2 (LEI:2222) -> 2; ent-3 (missing) -> 3.
	if len(entities) != 3 {
		t.Fatalf("expected 3 separate Acme Corp entities (only matching name+strongId merges), got %d: %+v", len(entities),
			entities)
	}

	reviewCount := 0
	for _, ent := range entities {
		if ent.ReviewState == "review_required" {
			reviewCount++
		}
	}
	if reviewCount < 2 {
		t.Fatalf("expected conflicting/missing strongId entities flagged review_required, got %d", reviewCount)
	}
}

func TestScenario6_HighImpactLowConfidenceCreatesVerificationRequiredAlert(t *testing.T) {
	ctx := context.Background()
	eng, _ := openTestEngine(t, worldradar.Options{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id":          "unverified-critical-1",
					"title":       "Critical Emergency Cloud Backbone Outage Claimed",
					"url":         "https://rumor.example/outage-claim",
					"publishedAt": "2026-11-26T11:00:00Z",
					"summary":     "Single uncorroborated source reports critical emergency outage.",
					"impact":      95,
					"urgency":     92,
				},
			},
		})
	}))
	defer srv.Close()

	inst, err := eng.InstallPack(ctx, "radar.cloud-isp-infra", "Alert Triage", "alert")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	if _, err := eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
		TargetType:        "feed",
		CanonicalName:     "Single Blog",
		ConnectorID:       "web_feed",
		SourceURI:         srv.URL,
		IndependenceGroup: "unverified-blog",
	}); err != nil {
		t.Fatalf("add target: %v", err)
	}

	if _, err := eng.RunInstance(ctx, inst.ID); err != nil {
		t.Fatalf("run instance: %v", err)
	}

	alerts, err := eng.ListAlerts(ctx, "open", 20)
	if err != nil || len(alerts) != 1 {
		t.Fatalf("expected 1 open alert, got %d (err=%v)", len(alerts), err)
	}
	if alerts[0].AlertKind != "verification_required" {
		t.Fatalf("expected alert_kind verification_required, got %s", alerts[0].AlertKind)
	}
	if alerts[0].ConfidenceLabel != "single_source" {
		t.Fatalf("expected single_source confidence, got %s", alerts[0].ConfidenceLabel)
	}
}

func TestScenario7_ReportValidationRejectsUngroundedObservedFacts(t *testing.T) {
	ctx := context.Background()

	// Part A: Direct validation rejects empty and non-existent evidenceRefs.
	eng, _ := openTestEngine(t, worldradar.Options{})
	inst, err := eng.InstallPack(ctx, "radar.open-source-dev", "Report Grounding", "monitor")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}

	errEmptyRefs := eng.ValidateReport(ctx, worldradar.RadarReport{
		InstanceID: inst.ID,
		Title:      "Ungrounded",
		ObservedFacts: []worldradar.ReportFact{
			{Statement: "Claimed without citation", EvidenceRefs: nil},
		},
	})
	if errEmptyRefs == nil || !errors.Is(errEmptyRefs, worldradar.ErrValidation) {
		t.Fatalf("expected validation error for empty evidenceRefs, got %v", errEmptyRefs)
	}

	errFakeRef := eng.ValidateReport(ctx, worldradar.RadarReport{
		InstanceID: inst.ID,
		Title:      "Fake Ref",
		ObservedFacts: []worldradar.ReportFact{
			{Statement: "Claimed with non-existent ref", EvidenceRefs: []string{"art-does-not-exist"}},
		},
		EvidenceRefs: []string{"art-does-not-exist"},
	})
	if errFakeRef == nil || !errors.Is(errFakeRef, worldradar.ErrValidation) {
		t.Fatalf("expected validation error for non-existent evidenceRef, got %v", errFakeRef)
	}

	// Part B: GenerateReport with real collected evidence succeeds via deterministic fallback,
	// and rejects ungrounded GatewayURL output.
	feedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id":          "rel-1",
					"title":       "Runtime v2.0 Released",
					"url":         "https://oss.example/v2",
					"publishedAt": "2026-11-26T10:00:00Z",
					"summary":     "Major release milestone.",
				},
			},
		})
	}))
	defer feedServer.Close()

	if _, err := eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
		CanonicalName: "OSS Feed",
		ConnectorID:   "web_feed",
		SourceURI:     feedServer.URL,
	}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	if _, err := eng.RunInstance(ctx, inst.ID); err != nil {
		t.Fatalf("run instance: %v", err)
	}

	report, err := eng.GenerateReport(ctx, inst.ID, "Grounded Brief", "D07")
	if err != nil {
		t.Fatalf("generate grounded report: %v", err)
	}
	if len(report.ObservedFacts) == 0 || len(report.EvidenceRefs) == 0 {
		t.Fatalf("expected non-empty grounded observedFacts and evidenceRefs, got %+v", report)
	}

	// Gateway returning ungrounded fact MUST fail validation.
	ungroundedGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "mock-llm",
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": `{"observedFacts":[{"statement":"Invented fact","evidenceRefs":["art-hallucinated"]}],"reportedClaims":[],"inferences":[],"unknowns":[],"evidenceRefs":["art-hallucinated"]}`,
					},
				},
			},
		})
	}))
	defer ungroundedGateway.Close()

	engWithBadGW, hub2 := openTestEngine(t, worldradar.Options{GatewayURL: ungroundedGateway.URL})
	_ = hub2
	inst2, _ := engWithBadGW.InstallPack(ctx, "radar.open-source-dev", "GW Rejection", "monitor")
	_, _ = engWithBadGW.AddWatchTarget(ctx, inst2.ID, worldradar.WatchTargetInput{
		CanonicalName: "OSS Feed",
		ConnectorID:   "web_feed",
		SourceURI:     feedServer.URL,
	})
	_, _ = engWithBadGW.RunInstance(ctx, inst2.ID)
	if _, gwErr := engWithBadGW.GenerateReport(ctx, inst2.ID, "Bad GW", "D07"); gwErr == nil {
		t.Fatal("expected GenerateReport to reject ungrounded gateway response")
	}
}

func TestScenario8_PausedInstanceStopsCollectionAndRetainsHistory(t *testing.T) {
	ctx := context.Background()
	eng, hub := openTestEngine(t, worldradar.Options{})

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id":          "hist-1",
					"title":       "Historical Signal Before Pause",
					"url":         "https://feed.example/hist-1",
					"publishedAt": "2026-11-26T08:00:00Z",
					"summary":     "Should persist after pause.",
				},
			},
		})
	}))
	defer srv.Close()

	inst, err := eng.InstallPack(ctx, "radar.open-source-dev", "Pause Check", "monitor")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	if _, err := eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
		CanonicalName: "Feed",
		ConnectorID:   "web_feed",
		SourceURI:     srv.URL,
	}); err != nil {
		t.Fatalf("add target: %v", err)
	}
	if _, err := eng.RunInstance(ctx, inst.ID); err != nil {
		t.Fatalf("initial run: %v", err)
	}

	paused, err := eng.PauseInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("pause instance: %v", err)
	}
	if paused.Status != "paused" {
		t.Fatalf("expected paused status, got %s", paused.Status)
	}

	summary, runErr := eng.RunInstance(ctx, inst.ID)
	if !errors.Is(runErr, worldradar.ErrInstancePaused) {
		t.Fatalf("expected ErrInstancePaused, got %v", runErr)
	}
	if summary.Status != worldradar.RunStatusPaused {
		t.Fatalf("expected paused summary status, got %s", summary.Status)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected paused instance not to call upstream again, hits=%d", hits.Load())
	}

	var artifactCount int
	if err := hub.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_raw_artifacts`).Scan(&artifactCount); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if artifactCount != 1 {
		t.Fatalf("expected historical artifact retained after pause, got %d", artifactCount)
	}
}

func TestScenario9_CrossDomainEventWithSharedEvidenceArtifact(t *testing.T) {
	ctx := context.Background()
	eng, _ := openTestEngine(t, worldradar.Options{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id":          "cross-1",
					"title":       "Open-Weight Agent SDK v1.0 Ships Tool Sandbox",
					"url":         "https://ai-oss.example/sdk-v1",
					"publishedAt": "2026-11-26T12:00:00Z",
					"summary":     "Bridges AI autonomous agent orchestration and open source tooling.",
					"domains":     []string{"D06", "D07"},
				},
			},
		})
	}))
	defer srv.Close()

	inst, err := eng.InstallPack(ctx, "radar.ai-agent-discovery", "Cross Domain Check", "monitor")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	if _, err := eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
		CanonicalName: "AI OSS Feed",
		ConnectorID:   "web_feed",
		SourceURI:     srv.URL,
	}); err != nil {
		t.Fatalf("add target: %v", err)
	}

	if _, err := eng.RunInstance(ctx, inst.ID); err != nil {
		t.Fatalf("run instance: %v", err)
	}

	eventsD06, err := eng.ListEvents(ctx, "D06", 10)
	if err != nil || len(eventsD06) != 1 {
		t.Fatalf("expected event visible under D06, got %d (err=%v)", len(eventsD06), err)
	}
	eventsD07, err := eng.ListEvents(ctx, "D07", 10)
	if err != nil || len(eventsD07) != 1 {
		t.Fatalf("expected event visible under D07, got %d (err=%v)", len(eventsD07), err)
	}
	if eventsD06[0].ID != eventsD07[0].ID {
		t.Fatalf("expected same clustered event ID across D06 and D07")
	}

	detail, err := eng.GetEventDetail(ctx, eventsD06[0].ID)
	if err != nil {
		t.Fatalf("get event detail: %v", err)
	}
	if len(detail.Event.Domains) != 2 || detail.Event.Domains[0] != "D06" || detail.Event.Domains[1] != "D07" {
		t.Fatalf("expected domains [D06 D07], got %v", detail.Event.Domains)
	}
	if len(detail.Evidence) != 1 {
		t.Fatalf("expected 1 shared evidence artifact, got %d", len(detail.Evidence))
	}
}

func TestRESTRoutesEndToEnd(t *testing.T) {
	eng, _ := openTestEngine(t, worldradar.Options{})
	router := echo.New()
	apiGroup := router.Group("/api")
	eng.RegisterRoutes(apiGroup)

	// 1. GET /api/radar/domains
	reqDomains := httptest.NewRequest(http.MethodGet, "/api/radar/domains", nil)
	recDomains := httptest.NewRecorder()
	router.ServeHTTP(recDomains, reqDomains)
	if recDomains.Code != http.StatusOK {
		t.Fatalf("GET /domains status=%d body=%s", recDomains.Code, recDomains.Body.String())
	}

	// 2. GET /api/radar/domains/D06/niches
	reqNiches := httptest.NewRequest(http.MethodGet, "/api/radar/domains/D06/niches", nil)
	recNiches := httptest.NewRecorder()
	router.ServeHTTP(recNiches, reqNiches)
	if recNiches.Code != http.StatusOK {
		t.Fatalf("GET /domains/D06/niches status=%d", recNiches.Code)
	}

	// 3. GET /api/radar/lenses & /packs
	for _, routePath := range []string{"/api/radar/lenses", "/api/radar/packs", "/api/radar/sources/health"} {
		req := httptest.NewRequest(http.MethodGet, routePath, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d", routePath, rec.Code)
		}
	}

	// 4. POST /api/radar/packs/radar.cyber-defensive/install
	installBody := bytes.NewBufferString(`{"name":"Cyber Watch","mode":"alert"}`)
	reqInstall := httptest.NewRequest(http.MethodPost, "/api/radar/packs/radar.cyber-defensive/install", installBody)
	reqInstall.Header.Set("Content-Type", "application/json")
	recInstall := httptest.NewRecorder()
	router.ServeHTTP(recInstall, reqInstall)
	if recInstall.Code != http.StatusCreated {
		t.Fatalf("POST /packs/install status=%d body=%s", recInstall.Code, recInstall.Body.String())
	}
	var createdInst worldradar.RadarInstance
	if err := json.Unmarshal(recInstall.Body.Bytes(), &createdInst); err != nil {
		t.Fatalf("decode installed instance: %v", err)
	}

	// 5. POST /api/radar/feedback
	fbBody := bytes.NewBufferString(`{"targetKind":"radar_instance","targetId":"` + createdInst.ID + `","vote":"up","reason":"accurate baseline"}`)
	reqFB := httptest.NewRequest(http.MethodPost, "/api/radar/feedback", fbBody)
	reqFB.Header.Set("Content-Type", "application/json")
	recFB := httptest.NewRecorder()
	router.ServeHTTP(recFB, reqFB)
	if recFB.Code != http.StatusCreated {
		t.Fatalf("POST /feedback status=%d body=%s", recFB.Code, recFB.Body.String())
	}
}
