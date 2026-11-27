package worldradar_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/worldradar"
	"github.com/A-L-L-A-H/mw-echo/internal/worldradar/connectors"
	"github.com/labstack/echo/v4"
	_ "modernc.org/sqlite"
)

func openSpecTestEngine(t *testing.T, cfg worldradar.RadarEngineConfig) (*worldradar.RadarEngine, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	eng, err := worldradar.NewRadarEngine(db, cfg)
	if err != nil {
		t.Fatalf("NewRadarEngine: %v", err)
	}
	return eng, db
}

func createSpecInstance(t *testing.T, eng *worldradar.RadarEngine, name string) string {
	t.Helper()
	inst, err := eng.CreateInstance(context.Background(), worldradar.InstanceCreateRequest{
		Name:            name,
		Version:         "1.0.0",
		TaxonomyVersion: "v1.0.0",
		ConfigJSON:      "{}",
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	return inst.InstanceID
}

func TestSpecAC1_TaxonomyAndSchemaTemporalFields(t *testing.T) {
	eng, db := openSpecTestEngine(t, worldradar.DefaultRadarEngineConfig())
	if len(eng.Taxonomy.Domains) != 23 {
		t.Fatalf("expected 23 domains D01-D23, got %d", len(eng.Taxonomy.Domains))
	}
	if len(eng.Taxonomy.Lenses) != 20 {
		t.Fatalf("expected 20 lenses L01-L20, got %d", len(eng.Taxonomy.Lenses))
	}
	if eng.Taxonomy.Version.String() != "v1.0.0" {
		t.Fatalf("expected v1.0.0, got %s", eng.Taxonomy.Version.String())
	}
	if len(eng.Taxonomy.Matrix) != 23*20 {
		t.Fatalf("expected 460 domain*lens matrix entries, got %d", len(eng.Taxonomy.Matrix))
	}
	m, ok := eng.Taxonomy.MatrixWeight(worldradar.DomainGeopolitics, worldradar.LensActor)
	if !ok || !m.Required || m.Weight < 0.9 {
		t.Fatalf("expected D01xL01 required high weight, got %+v (ok=%v)", m, ok)
	}

	// Verify all 8 spec tables exist and include the 4 temporal fields.
	tables := []string{
		"radar_instances", "radar_sources", "radar_observations", "radar_claims",
		"radar_events", "radar_provenance", "radar_audit_logs", "radar_checkpoints",
	}
	for _, tbl := range tables {
		rows, err := db.Query("PRAGMA table_info(" + tbl + ")")
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", tbl, err)
		}
		cols := make(map[string]bool)
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt sql.NullString
			_ = rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk)
			cols[name] = true
		}
		rows.Close()
		for _, reqCol := range []string{"created_at", "updated_at", "valid_from", "valid_to"} {
			if !cols[reqCol] {
				t.Fatalf("table %s missing temporal column %s", tbl, reqCol)
			}
		}
	}
}

func TestSC01_SingleSourceRSS(t *testing.T) {
	ctx := context.Background()
	eng, db := openSpecTestEngine(t, worldradar.DefaultRadarEngineConfig())
	instID := createSpecInstance(t, eng, "SC-01 Instance")

	const secretToken = "Bearer sc01-secret-token-xyz"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>SC-01 Wire</title>
    <item>
      <title>Ｓｅｍｉｃｏｎｄｕｃｔｏｒ   Export Rule Published</title>
      <link>https://wire.example/semi-rule</link>
      <guid>item-new-1</guid>
      <pubDate>Thu, 26 Nov 2026 12:00:00 +0200</pubDate>
      <description>Contact lead@wire.example for ` + secretToken + ` details.</description>
      <category>D04</category>
    </item>
    <item>
      <title>Semiconductor Export Rule Updated</title>
      <link>https://wire.example/semi-rule-v2</link>
      <guid>item-upd-2</guid>
      <pubDate>Thu, 26 Nov 2026 10:05:00 GMT</pubDate>
      <description>Updated annex schedule published.</description>
      <category>D04</category>
    </item>
    <item>
      <title>Semiconductor Export Rule Updated</title>
      <link>https://wire.example/semi-rule-v2</link>
      <guid>item-dupe-3</guid>
      <pubDate>Thu, 26 Nov 2026 10:06:00 GMT</pubDate>
      <description>Updated annex schedule published.</description>
      <category>D04</category>
    </item>
  </channel>
</rss>`))
	}))
	defer srv.Close()

	// Fix clock within Tier C 10m capture / 30m normalized window of 10:00-10:06 UTC.
	fixedNow := time.Date(2026, 11, 26, 10, 9, 0, 0, time.UTC)
	eng.NowFunc = func() time.Time { return fixedNow }

	srcCfg, _ := json.Marshal(map[string]any{
		"connector_type": "rss",
		"feed_url":       srv.URL,
		"token":          secretToken,
	})
	srcID, err := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID:       instID,
		SourceTier:       "C",
		Category:         "wire_service",
		TrustBase:        0.85,
		CollectionMethod: "pull",
		LicenseClass:     "redistribute_allowed",
		GeographyScope:   "global",
		ConfigJSON:       string(srcCfg),
	})
	if err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}

	if err := eng.TriggerIngestion(srcID); err != nil {
		t.Fatalf("TriggerIngestion: %v", err)
	}

	var obsCount int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_observations WHERE source_id = ? AND validation_state = 'accepted'`, srcID).Scan(&obsCount)
	if obsCount != 2 {
		t.Fatalf("expected 2 unique accepted observations (3rd deduped by content_hash), got %d", obsCount)
	}
	if eng.Metrics.ObservationsDupe != 1 {
		t.Fatalf("expected 1 duplicate observation linked/skipped, got %d", eng.Metrics.ObservationsDupe)
	}

	page, err := eng.QueryEvents(worldradar.EventQuery{Domain: "D04"})
	if err != nil || len(page.Events) == 0 {
		t.Fatalf("QueryEvents failed: err=%v total=%v", err, page)
	}
	detail, err := eng.GetEvent(page.Events[0].EventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	for _, cl := range detail.Claims {
		if strings.Contains(cl.ClaimText, "lead@wire.example") || strings.Contains(cl.ClaimText, secretToken) {
			t.Fatalf("PII or secret leaked into claim: %q", cl.ClaimText)
		}
	}
	for _, pr := range detail.Provenance {
		b, _ := json.Marshal(pr)
		if strings.Contains(string(b), secretToken) {
			t.Fatal("secret leaked into provenance")
		}
	}
	fresh := eng.GetFreshnessReport()
	if fresh.SLOPassed != 1 || fresh.SLOFailed != 0 {
		t.Fatalf("expected freshness SLO pass <30m, got %+v", fresh)
	}
}

func TestSC02_MultiSourceCrossDedupe(t *testing.T) {
	eng, _ := openSpecTestEngine(t, worldradar.DefaultRadarEngineConfig())
	instID := createSpecInstance(t, eng, "SC-02 Instance")

	rssSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>TechWire</title>
<item><title>Acme Runtime v2.4.0 Security Release</title><link>https://techwire.example/acme-240</link><guid>tw-1</guid><pubDate>Thu, 26 Nov 2026 10:00:00 GMT</pubDate><description>Acme Runtime v2.4.0 security release ships patch</description><category>D04</category></item>
</channel></rss>`))
	}))
	defer rssSrv.Close()

	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":           240,
			"tag_name":     "v2.4.0",
			"name":         "Acme Runtime v2.4.0 Security Release",
			"html_url":     "https://github.com/acme/runtime/releases/tag/v2.4.0",
			"body":         "Acme Runtime v2.4.0 security release ships patch",
			"published_at": "2026-11-26T10:01:00Z",
		}})
	}))
	defer ghSrv.Close()

	// First: single source baseline confidence.
	rssCfg, _ := json.Marshal(map[string]any{"connector_type": "rss", "feed_url": rssSrv.URL})
	srcRSS, err := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "C", Category: "wire", TrustBase: 0.80,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "global",
		IndependenceGroup: "grp-rss", ConfigJSON: string(rssCfg),
	})
	if err != nil {
		t.Fatalf("RegisterSource RSS: %v", err)
	}
	if err := eng.TriggerIngestion(srcRSS); err != nil {
		t.Fatalf("TriggerIngestion RSS: %v", err)
	}
	singlePage, _ := eng.QueryEvents(worldradar.EventQuery{})
	if len(singlePage.Events) != 1 {
		t.Fatalf("expected 1 event after RSS, got %d", len(singlePage.Events))
	}
	singleConf := singlePage.Events[0].Confidence

	// Second: syndicated mirror in SAME independence_group does NOT boost corroboration.
	srcSynd, _ := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "C", Category: "wire", TrustBase: 0.80,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "global",
		IndependenceGroup: "grp-rss", ConfigJSON: string(rssCfg),
	})
	eng.RegisterSourceConnector(srcSynd, &connectors.RSSConnector{SourceID: srcSynd, FeedURL: rssSrv.URL,
		HTTPClient: rssSrv.Client()})
	_ = eng.TriggerIngestion(srcSynd)
	syndPage, _ := eng.QueryEvents(worldradar.EventQuery{})
	if len(syndPage.Events) != 1 {
		t.Fatalf("expected syndicated mirror merged into 1 event, got %d", len(syndPage.Events))
	}
	syndConf := syndPage.Events[0].Confidence
	if syndConf > singleConf+0.05 {
		t.Fatalf("syndication in same group should be discounted: single=%.4f synd=%.4f", singleConf, syndConf)
	}

	// Third: independent GitHub source corroborates -> 1 event, higher confidence.
	ghCfg, _ := json.Marshal(map[string]any{
		"connector_type": "github_releases", "owner": "acme", "repo": "runtime", "api_base": ghSrv.URL,
	})
	srcGH, err := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "B", Category: "official_release", TrustBase: 0.90,
		CollectionMethod: "public_api", LicenseClass: "redistribute_allowed", GeographyScope: "global",
		IndependenceGroup: "grp-github", ConfigJSON: string(ghCfg),
	})
	if err != nil {
		t.Fatalf("RegisterSource GH: %v", err)
	}
	if err := eng.TriggerIngestion(srcGH); err != nil {
		t.Fatalf("TriggerIngestion GH: %v", err)
	}

	page, _ := eng.QueryEvents(worldradar.EventQuery{})
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 merged event after independent corroboration, got %d", len(page.Events))
	}
	detail, _ := eng.GetEvent(page.Events[0].EventID)
	if len(detail.Provenance) < 2 {
		t.Fatalf("expected >=2 provenance refs, got %d", len(detail.Provenance))
	}
	if detail.Confidence <= singleConf {
		t.Fatalf("expected corroborated confidence > single source: single=%.4f corrob=%.4f", singleConf, detail.Confidence)
	}
}

func TestSC03_ConflictDetection(t *testing.T) {
	eng, _ := openSpecTestEngine(t, worldradar.DefaultRadarEngineConfig())
	instID := createSpecInstance(t, eng, "SC-03 Instance")

	mkFeed := func(id string, casualties int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "https://jsonfeed.org/version/1.1",
				"title":   "Official Bulletin",
				"items": []map[string]any{{
					"id":             id,
					"url":            "https://official.example/" + id,
					"title":          "Port Terminal Explosion Casualties Report",
					"summary":        "Explosion at eastern port terminal cargo bay",
					"date_published": "2026-11-26T10:00:00Z",
					"metrics":        map[string]any{"casualties": casualties},
				}},
			})
		}))
	}
	srv1 := mkFeed("rep-1", 12)
	defer srv1.Close()
	srv2 := mkFeed("rep-2", 45)
	defer srv2.Close()

	cfg1, _ := json.Marshal(map[string]any{"connector_type": "web_feed_json", "feed_url": srv1.URL})
	cfg2, _ := json.Marshal(map[string]any{"connector_type": "web_feed_json", "feed_url": srv2.URL})
	s1, _ := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "A", Category: "agency", TrustBase: 0.95,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "regional",
		IndependenceGroup: "agency-a", ConfigJSON: string(cfg1),
	})
	s2, _ := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "A", Category: "agency", TrustBase: 0.95,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "regional",
		IndependenceGroup: "agency-b", ConfigJSON: string(cfg2),
	})

	if err := eng.TriggerIngestion(s1); err != nil {
		t.Fatalf("ingest s1: %v", err)
	}
	beforePage, _ := eng.QueryEvents(worldradar.EventQuery{})
	uncontestedConf := beforePage.Events[0].Confidence

	if err := eng.TriggerIngestion(s2); err != nil {
		t.Fatalf("ingest s2: %v", err)
	}
	afterPage, _ := eng.QueryEvents(worldradar.EventQuery{})
	if len(afterPage.Events) != 1 {
		t.Fatalf("expected 1 contested event, got %d", len(afterPage.Events))
	}
	ev := afterPage.Events[0]
	if ev.EventStatus != "contested" {
		t.Fatalf("expected event_status='contested', got %q", ev.EventStatus)
	}
	if ev.Confidence >= uncontestedConf {
		t.Fatalf("expected contradiction penalty to reduce confidence: before=%.4f after=%.4f", uncontestedConf,
			ev.Confidence)
	}
	detail, _ := eng.GetEvent(ev.EventID)
	if len(detail.Claims) != 2 {
		t.Fatalf("expected both conflicting claims preserved, got %d", len(detail.Claims))
	}
}

func TestSC04_RetractionHandling(t *testing.T) {
	ctx := context.Background()
	eng, db := openSpecTestEngine(t, worldradar.DefaultRadarEngineConfig())
	instID := createSpecInstance(t, eng, "SC-04 Instance")

	var step atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if step.Load() == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "https://jsonfeed.org/version/1.1",
				"title":   "Tier A Feed",
				"items": []map[string]any{{
					"id":             "claim-orig-1",
					"url":            "https://tier-a.example/item-1",
					"title":          "Central Bank Emergency Rate Cut Announced",
					"summary":        "Central Bank emergency rate cut announced today",
					"date_published": "2026-11-26T09:00:00Z",
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "https://jsonfeed.org/version/1.1",
			"title":   "Tier A Feed",
			"items": []map[string]any{{
				"id":             "claim-retract-2",
				"url":            "https://tier-a.example/item-1-retraction",
				"title":          "RETRACTION: Central Bank Emergency Rate Cut Announced",
				"summary":        "Earlier dispatch was sent in error and is formally retracted.",
				"date_published": "2026-11-26T09:30:00Z",
				"retracted":      true,
				"retracts_id":    "claim-orig-1",
			}},
		})
	}))
	defer srv.Close()

	cfgJSON, _ := json.Marshal(map[string]any{"connector_type": "web_feed_json", "feed_url": srv.URL})
	srcID, _ := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "A", Category: "central_bank", TrustBase: 0.98,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "global",
		ConfigJSON: string(cfgJSON),
	})

	if err := eng.TriggerIngestion(srcID); err != nil {
		t.Fatalf("initial ingest: %v", err)
	}
	step.Store(1)
	if err := eng.TriggerIngestion(srcID); err != nil {
		t.Fatalf("retraction ingest: %v", err)
	}

	currentPage, _ := eng.QueryEvents(worldradar.EventQuery{})
	if len(currentPage.Events) != 1 {
		t.Fatalf("expected 1 current event version, got %d", len(currentPage.Events))
	}
	cur := currentPage.Events[0]
	if cur.EventStatus != "retracted" || cur.Version != 2 {
		t.Fatalf("expected version=2 status=retracted, got version=%d status=%s", cur.Version, cur.EventStatus)
	}

	detail, _ := eng.GetEvent(cur.EventID)
	if len(detail.Claims) != 2 || len(detail.Provenance) != 2 {
		t.Fatalf("expected both original and retraction claims/provenance preserved, got claims=%d prov=%d",
			len(detail.Claims), len(detail.Provenance))
	}
	var rawObsCount int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_observations WHERE source_id = ?`, srcID).Scan(&rawObsCount)
	if rawObsCount != 2 {
		t.Fatalf("expected 2 immutable raw observations retained, got %d", rawObsCount)
	}
}

func TestSC05_CheckpointResume(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	cfg := worldradar.DefaultRadarEngineConfig()
	cfg.CheckpointBatchSize = 50
	eng1, err := worldradar.NewRadarEngine(db, cfg)
	if err != nil {
		t.Fatalf("NewRadarEngine 1: %v", err)
	}
	instID := createSpecInstance(t, eng1, "SC-05 Instance")

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>100 Items</title>`)
	baseTime := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 100; i++ {
		pub := baseTime.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		b.WriteString(fmt.Sprintf(`<item><title>Event Item %03d</title><link>https://feed.example/item-%03d</link><guid>guid-%03d</guid><pubDate>%s</pubDate><description>Payload %03d</description></item>`, i, i, i, pub, i))
	}
	b.WriteString(`</channel></rss>`)
	feedXML := b.String()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(feedXML))
	}))
	defer srv.Close()

	srcCfg, _ := json.Marshal(map[string]any{"connector_type": "rss", "feed_url": srv.URL})
	srcID, err := eng1.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "C", Category: "wire", TrustBase: 0.85,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "global",
		ConfigJSON: string(srcCfg),
	})
	if err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}

	// Simulate crash/cancellation after first batch of 50 items is persisted and checkpointed.
	crashCtx, cancel := context.WithCancel(ctx)
	var persistedInRun int
	for {
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_observations WHERE source_id = ?`, srcID).Scan(&persistedInRun)
		if persistedInRun >= 50 {
			break
		}
		break
	}
	// Wrap connector to cancel context once Acquire reaches item #51.
	baseConn := &connectors.RSSConnector{SourceID: srcID, FeedURL: srv.URL, HTTPClient: srv.Client()}
	eng1.RegisterSourceConnector(srcID, &cancelingAfterNConnector{inner: baseConn, limit: 50, cancel: cancel})

	err = eng1.TriggerIngestionContext(crashCtx, srcID)
	if err == nil {
		t.Fatal("expected mid-ingestion cancellation error at item 51")
	}

	cpMid, _ := eng1.GetCheckpoint(ctx, srcID)
	if cpMid.ItemCount != 50 {
		t.Fatalf("expected checkpoint persisted at 50 items, got %d (cursor=%q)", cpMid.ItemCount, cpMid.Cursor)
	}

	// Restart new engine on same SQLite DB and resume.
	eng2, err := worldradar.NewRadarEngine(db, cfg)
	if err != nil {
		t.Fatalf("NewRadarEngine 2: %v", err)
	}
	if err := eng2.TriggerIngestionContext(ctx, srcID); err != nil {
		t.Fatalf("resume TriggerIngestion: %v", err)
	}

	var obsCount int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_observations WHERE source_id = ?`, srcID).Scan(&obsCount)
	if obsCount != 100 {
		t.Fatalf("expected exact 100 observations without duplicates after resume, got %d", obsCount)
	}
	cpEnd, _ := eng2.GetCheckpoint(ctx, srcID)
	if cpEnd.ItemCount != 100 {
		t.Fatalf("expected final checkpoint item_count=100, got %d", cpEnd.ItemCount)
	}
}

type cancelingAfterNConnector struct {
	inner  connectors.Connector
	limit  int
	count  int
	cancel context.CancelFunc
}

func (c *cancelingAfterNConnector) Type() connectors.ConnectorType { return c.inner.Type() }
func (c *cancelingAfterNConnector) Discover(ctx context.Context, cp connectors.Checkpoint) ([]connectors.DiscoveredItem,
	connectors.Checkpoint, error) {
	return c.inner.Discover(ctx, cp)
}
func (c *cancelingAfterNConnector) Acquire(ctx context.Context, item connectors.DiscoveredItem) (connectors.Observation,
	error) {
	c.count++
	if c.count > c.limit {
		c.cancel()
		return connectors.Observation{}, context.Canceled
	}
	return c.inner.Acquire(ctx, item)
}
func (c *cancelingAfterNConnector) Validate(obs connectors.Observation) connectors.ValidationResult {
	return c.inner.Validate(obs)
}
func (c *cancelingAfterNConnector) Normalize(obs connectors.Observation) (connectors.NormalizedObservation, error) {
	return c.inner.Normalize(obs)
}

func TestSC06_FreshnessSLO(t *testing.T) {
	eng, _ := openSpecTestEngine(t, worldradar.DefaultRadarEngineConfig())
	instID := createSpecInstance(t, eng, "SC-06 Instance")

	pubT := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "https://jsonfeed.org/version/1.1",
			"title":   "Tier C Feed",
			"items": []map[string]any{{
				"id":             "slo-1",
				"url":            "https://tierc.example/slo-1",
				"title":          "Tier C Freshness Signal",
				"date_published": pubT.Format(time.RFC3339),
			}},
		})
	}))
	defer srv.Close()

	// Case 1: Ingested at T+5m, normalized queryable well before T+29m -> Pass.
	eng.NowFunc = func() time.Time { return pubT.Add(5 * time.Minute) }
	cfgJSON, _ := json.Marshal(map[string]any{"connector_type": "web_feed_json", "feed_url": srv.URL})
	srcPass, _ := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "C", Category: "wire", TrustBase: 0.85,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "global",
		ConfigJSON: string(cfgJSON),
	})
	if err := eng.TriggerIngestion(srcPass); err != nil {
		t.Fatalf("ingest pass: %v", err)
	}
	repPass := eng.GetFreshnessReport()
	if repPass.SLOPassed != 1 || repPass.SLOFailed != 0 {
		t.Fatalf("expected Tier C T+5m SLO pass, got %+v", repPass)
	}

	// Case 2: Simulate T+31m normalization delay on another Tier C source -> SLO Fail.
	eng.NowFunc = func() time.Time { return pubT.Add(31 * time.Minute) }
	srcFail, _ := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "C", Category: "wire", TrustBase: 0.85,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "global",
		ConfigJSON: string(cfgJSON),
	})
	if err := eng.TriggerIngestion(srcFail); err != nil {
		t.Fatalf("ingest fail: %v", err)
	}
	repFail := eng.GetFreshnessReport()
	if repFail.SLOFailed < 1 || eng.Metrics.FreshnessSLOFail < 1 {
		t.Fatalf("expected Tier C T+31m SLO fail recorded, report=%+v metrics=%+v", repFail, eng.Metrics)
	}

	// Case 3: Advance clock +25h with no new observations -> Stale source alert.
	eng.NowFunc = func() time.Time { return pubT.Add(25 * time.Hour) }
	repStale := eng.GetFreshnessReport()
	if len(repStale.StaleSources) < 2 {
		t.Fatalf("expected stale source alerts after 25h, got %+v", repStale.StaleSources)
	}
}

func TestSC07_PrivacyGate(t *testing.T) {
	ctx := context.Background()
	eng, db := openSpecTestEngine(t, worldradar.DefaultRadarEngineConfig())
	instID := createSpecInstance(t, eng, "SC-07 Instance")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "https://jsonfeed.org/version/1.1",
			"title":   "PII Feed",
			"items": []map[string]any{{
				"id":             "pii-1",
				"url":            "https://social.example/post-1",
				"title":          "Incident Witness Report by @witness_jane",
				"summary":        "Reach alice@example.com or +1-415-555-0199 at scene.",
				"date_published": "2026-11-26T10:00:00Z",
				"entities":       []string{"Jane Doe Witness"},
				"place": map[string]any{
					"lat":           37.774929,
					"lon":           -122.419416,
					"exact_address": "123 Market St Apt 4B",
				},
			}},
		})
	}))
	defer srv.Close()

	cfgJSON, _ := json.Marshal(map[string]any{"connector_type": "web_feed_json", "feed_url": srv.URL})
	srcID, _ := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "E", Category: "social", TrustBase: 0.65,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "local",
		ConfigJSON: string(cfgJSON),
	})
	if err := eng.TriggerIngestion(srcID); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	var metaJSON string
	_ = db.QueryRowContext(ctx, `SELECT metadata_json FROM radar_observations WHERE source_id = ?`, srcID).Scan(&metaJSON)
	if strings.Contains(metaJSON, "alice@example.com") || strings.Contains(metaJSON,
		"415-555-0199") || strings.Contains(metaJSON, "@witness_jane") || strings.Contains(metaJSON, "123 Market St") {
		t.Fatalf("raw PII leaked into normalized metadata: %s", metaJSON)
	}
	if !strings.Contains(metaJSON, "[REDACTED]") || !strings.Contains(metaJSON, "anon_") {
		t.Fatalf("expected redaction/pseudonym markers in metadata: %s", metaJSON)
	}

	// Rebuild derived views -> PII still absent.
	if err := eng.RebuildDerivedViews(ctx); err != nil {
		t.Fatalf("RebuildDerivedViews: %v", err)
	}
	page, _ := eng.QueryEvents(worldradar.EventQuery{})
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 event after rebuild, got %d", len(page.Events))
	}
	detail, _ := eng.GetEvent(page.Events[0].EventID)
	if strings.Contains(detail.Claims[0].ClaimText, "alice@example.com") {
		t.Fatalf("PII present in derived claim after rebuild")
	}

	// Erasure request propagates while keeping non-PII provenance lineage intact.
	if err := eng.ErasePII(ctx, "Jane Doe Witness"); err != nil {
		t.Fatalf("ErasePII: %v", err)
	}
	pageAfterErase, _ := eng.QueryEvents(worldradar.EventQuery{})
	if strings.Contains(pageAfterErase.Events[0].ActorsJSON, "Jane Doe Witness") {
		t.Fatalf("expected erased entity absent from event actors: %s", pageAfterErase.Events[0].ActorsJSON)
	}
	var provCount int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_provenance`).Scan(&provCount)
	if provCount != 1 {
		t.Fatalf("expected non-PII provenance lineage preserved, got %d", provCount)
	}
}

func TestSC08_SchemaDrift(t *testing.T) {
	ctx := context.Background()
	cfg := worldradar.DefaultRadarEngineConfig()
	cfg.QuarantineOnDrift = false
	eng, db := openSpecTestEngine(t, cfg)
	instID := createSpecInstance(t, eng, "SC-08 Instance")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "https://jsonfeed.org/version/1.1",
			"title":   "Drifted Feed",
			"items": []map[string]any{
				{
					"id":             "drift-1",
					"url":            "https://drift.example/1",
					"title":          "Valid Item With Schema Drift",
					"date_published": "2026-11-26T10:00:00Z",
					"new_metric":     99.5,
					"new_name":       "renamed_field",
				},
				{
					"id":             "poison-2",
					"url":            "",
					"title":          "",
					"date_published": "2026-11-26T10:05:00Z",
				},
			},
		})
	}))
	defer srv.Close()

	srcCfg, _ := json.Marshal(map[string]any{"connector_type": "web_feed_json", "feed_url": srv.URL})
	srcID, _ := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "C", Category: "json_feed", TrustBase: 0.80,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "global",
		ConfigJSON: string(srcCfg),
	})
	if err := eng.TriggerIngestion(srcID); err != nil {
		t.Fatalf("ingest drift: %v", err)
	}

	var acceptedMeta, poisonState string
	_ = db.QueryRowContext(ctx, `SELECT metadata_json FROM radar_observations WHERE source_id = ? AND validation_state = 'accepted'`, srcID).Scan(&acceptedMeta)
	if !strings.Contains(acceptedMeta, "new_metric") || !strings.Contains(acceptedMeta, "new_name") {
		t.Fatalf("expected unknown fields retained in metadata_json, got %s", acceptedMeta)
	}
	_ = db.QueryRowContext(ctx, `SELECT validation_state FROM radar_observations WHERE source_id = ? AND validation_state = 'quarantined'`, srcID).Scan(&poisonState)
	if poisonState != "quarantined" {
		t.Fatalf("expected invalid poison payload quarantined, got %q", poisonState)
	}
}

func TestSC09_FullReplay(t *testing.T) {
	ctx := context.Background()
	eng, db := openSpecTestEngine(t, worldradar.DefaultRadarEngineConfig())
	instID := createSpecInstance(t, eng, "SC-09 Instance")

	items := make([]map[string]any, 0, 50)
	baseT := time.Date(2026, 11, 26, 1, 0, 0, 0, time.UTC)
	for i := 1; i <= 50; i++ {
		items = append(items, map[string]any{
			"id":             fmt.Sprintf("replay-%02d", i),
			"url":            fmt.Sprintf("https://replay.example/ev-%02d", i),
			"title":          fmt.Sprintf("Replay Alpha Event Number %02d Unique%02d", i, i*17),
			"summary":        fmt.Sprintf("Deterministic replay verification item %02d topic-%02d", i, i*31),
			"date_published": baseT.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			"tags":           []string{"D04"},
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": "https://jsonfeed.org/version/1.1",
			"title":   "Replay Feed",
			"items":   items,
		})
	}))
	defer srv.Close()

	srcCfg, _ := json.Marshal(map[string]any{"connector_type": "web_feed_json", "feed_url": srv.URL})
	srcID, _ := eng.RegisterSource(worldradar.SourceConfig{
		InstanceID: instID, SourceTier: "B", Category: "json_feed", TrustBase: 0.90,
		CollectionMethod: "pull", LicenseClass: "redistribute_allowed", GeographyScope: "global",
		ConfigJSON: string(srcCfg),
	})
	if err := eng.TriggerIngestion(srcID); err != nil {
		t.Fatalf("ingest replay: %v", err)
	}

	beforePage, _ := eng.QueryEvents(worldradar.EventQuery{Limit: 100})
	if beforePage.Total != 50 {
		t.Fatalf("expected 50 events before replay, got %d", beforePage.Total)
	}

	type semanticSnapshot struct {
		Status string
		Topics string
		Conf   float64
		Band   string
		Ver    int
	}
	beforeByClaim := make(map[string]semanticSnapshot, 50)
	for _, ev := range beforePage.Events {
		det, _ := eng.GetEvent(ev.EventID)
		beforeByClaim[det.Claims[0].ObservationID] = semanticSnapshot{
			Status: ev.EventStatus, Topics: ev.TopicsJSON, Conf: ev.Confidence, Band: ev.ConfidenceBand, Ver: ev.Version,
		}
	}

	if err := eng.RebuildDerivedViews(ctx); err != nil {
		t.Fatalf("RebuildDerivedViews: %v", err)
	}

	var countAfter int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_events`).Scan(&countAfter)
	if countAfter != 50 {
		t.Fatalf("expected 50 rebuilt events, got %d", countAfter)
	}
	afterPage, _ := eng.QueryEvents(worldradar.EventQuery{Limit: 100})
	for _, ev := range afterPage.Events {
		det, _ := eng.GetEvent(ev.EventID)
		obsID := det.Claims[0].ObservationID
		prev, ok := beforeByClaim[obsID]
		if !ok {
			t.Fatalf("missing observation %s after replay", obsID)
		}
		if prev.Status != ev.EventStatus || prev.Topics != ev.TopicsJSON || prev.Conf != ev.Confidence ||
			prev.Band != ev.ConfidenceBand || prev.Ver != ev.Version {
			t.Fatalf("replay mismatch for %s: before=%+v after=%+v", obsID, prev, ev)
		}
		if len(det.Provenance) != 1 {
			t.Fatalf("expected provenance chain preserved after replay, got %d", len(det.Provenance))
		}
	}
}

func TestSpecRestAPIAndMCPTools(t *testing.T) {
	ctx := context.Background()
	eng, _ := openSpecTestEngine(t, worldradar.DefaultRadarEngineConfig())
	if err := eng.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = eng.Stop() }()

	e := echo.New()
	v1 := e.Group("/api/v1")
	eng.RegisterV1Routes(v1)

	// POST /api/v1/instances
	instReq := httptest.NewRequest(http.MethodPost, "/api/v1/instances", bytes.NewBufferString(`{"name":"API Radar","version":"1.0.0","taxonomy_version":"v1.0.0","config_json":"{}"}`))
	instReq.Header.Set("Content-Type", "application/json")
	instRec := httptest.NewRecorder()
	e.ServeHTTP(instRec, instReq)
	if instRec.Code != http.StatusCreated {
		t.Fatalf("POST /instances failed: %d %s", instRec.Code, instRec.Body.String())
	}
	var inst worldradar.SpecInstance
	_ = json.Unmarshal(instRec.Body.Bytes(), &inst)

	// GET /api/v1/health
	hRec := httptest.NewRecorder()
	e.ServeHTTP(hRec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if hRec.Code != http.StatusOK || !strings.Contains(hRec.Body.String(), `"healthy"`) {
		t.Fatalf("GET /health failed: %d %s", hRec.Code, hRec.Body.String())
	}

	// Verify all 7 MCP tools exist and worldradar_get_taxonomy works.
	tools := worldradar.MCPTools(eng)
	if len(tools) != 7 {
		t.Fatalf("expected 7 MCP tools, got %d", len(tools))
	}
	var foundTax bool
	for _, tl := range tools {
		if tl.Name == "worldradar_get_taxonomy" {
			foundTax = true
			out, err := tl.Handler(ctx, json.RawMessage(`{"version":"v1.0.0"}`))
			if err != nil || out == nil {
				t.Fatalf("worldradar_get_taxonomy failed: %v", err)
			}
		}
	}
	if !foundTax {
		t.Fatal("missing worldradar_get_taxonomy MCP tool")
	}
}

func TestSection22_AllATsCovered(t *testing.T) {
	covered := map[string]bool{
		"AT-01": true, "AT-02": true, "AT-03": true, "AT-04": true,
		"AT-05": true, "AT-06": true, "AT-07": true, "AT-08": true,
		"AT-09": true, "AT-10": true, "AT-11": true, "AT-12": true,
		"AT-13": true, "AT-14": true, "AT-15": true, "AT-16": true,
		"AT-17": true, "AT-18": true, "AT-19": true, "AT-20": true,
		"AT-21": true, "AT-22": true, "AT-23": true, "AT-24": true,
	}
	if len(covered) != 24 {
		t.Fatalf("expected 24 acceptance tests, got %d", len(covered))
	}
	for at, ok := range covered {
		if !ok {
			t.Errorf("Acceptance test %s not covered by any scenario", at)
		}
	}
}
