package worldradar_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
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

func TestV2_AllEightPacksValidateAndIncludePhase6(t *testing.T) {
	packs := worldradar.AllPacks()
	if len(packs) != 8 {
		t.Fatalf("expected 8 alpha packs, got %d", len(packs))
	}
	byID := make(map[string]worldradar.PackManifest, len(packs))
	for _, p := range packs {
		if err := worldradar.ValidatePack(p); err != nil {
			t.Fatalf("ValidatePack(%s) failed: %v", p.ID, err)
		}
		byID[p.ID] = p
	}

	social, ok := byID["radar.social-trends"]
	if !ok {
		t.Fatal("missing radar.social-trends pack")
	}
	if social.Name != "Social & Media Narrative Radar" || social.DefaultMode != "monitor" {
		t.Fatalf("unexpected radar.social-trends metadata: %+v", social)
	}
	if len(social.Domains) != 1 || social.Domains[0] != "D11" || len(social.Lenses) != 5 {
		t.Fatalf("unexpected radar.social-trends domains/lenses: %+v", social)
	}

	biz, ok := byID["radar.business-competitor"]
	if !ok {
		t.Fatal("missing radar.business-competitor pack")
	}
	if biz.Name != "Business & Competitor Radar" || biz.DefaultMode != "monitor" {
		t.Fatalf("unexpected radar.business-competitor metadata: %+v", biz)
	}
	if len(biz.Domains) != 1 || biz.Domains[0] != "D05" || len(biz.Lenses) != 5 {
		t.Fatalf("unexpected radar.business-competitor domains/lenses: %+v", biz)
	}
}

func TestV2_ArxivCyberKEVAndUSGSGeoConnectorsAndIngestion(t *testing.T) {
	ctx := context.Background()
	eng, _ := openTestEngine(t, worldradar.Options{})

	arxivSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>arXiv Query: cs.AI</title>
  <entry>
    <id>http://arxiv.org/abs/2611.04200v1</id>
    <updated>2026-11-26T08:00:00Z</updated>
    <published>2026-11-25T18:00:00Z</published>
    <title>Deterministic Agent Memory Verification</title>
    <summary>We propose formal invariant checks for autonomous tool-calling agents.</summary>
    <author><name>Dr. Ada Lovelace</name></author>
    <link href="http://arxiv.org/abs/2611.04200v1" rel="alternate" type="text/html"/>
    <category term="cs.AI" scheme="http://arxiv.org/schemas/atom"/>
  </entry>
</feed>`))
	}))
	defer arxivSrv.Close()

	kevSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vulnerabilities": []map[string]any{
				{
					"cveID":                      "CVE-2026-9901",
					"vendorProject":              "Acme",
					"product":                    "EdgeGateway",
					"vulnerabilityName":          "Acme EdgeGateway Remote Code Execution",
					"dateAdded":                  "2026-11-26",
					"shortDescription":           "Unauthenticated RCE in edge management API.",
					"requiredAction":             "Apply vendor patch 4.2.1 immediately.",
					"knownRansomwareCampaignUse": "Known",
				},
				{
					"cveID":                      "CVE-2026-9902",
					"vendorProject":              "Beta",
					"product":                    "RouterOS",
					"vulnerabilityName":          "Beta RouterOS Auth Bypass",
					"dateAdded":                  "2026-11-25",
					"shortDescription":           "Privilege escalation in admin portal.",
					"requiredAction":             "Upgrade firmware.",
					"knownRansomwareCampaignUse": "Unknown",
				},
			},
		})
	}))
	defer kevSrv.Close()

	usgsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/geo+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "FeatureCollection",
			"features": []map[string]any{
				{
					"id": "us7000v201",
					"properties": map[string]any{
						"mag":     6.8,
						"place":   "120km SE of Sendai, Japan",
						"time":    int64(1795680000000),
						"url":     "https://earthquake.usgs.gov/earthquakes/eventpage/us7000v201",
						"title":   "M 6.8 - 120km SE of Sendai, Japan",
						"tsunami": 1,
					},
					"geometry": map[string]any{
						"type":        "Point",
						"coordinates": []float64{142.15, 37.42, 35.5},
					},
				},
			},
		})
	}))
	defer usgsSrv.Close()

	// Direct connector assertions
	arxivRes := (&worldradar.ArxivConnector{}).Collect(ctx, worldradar.CollectRequest{
		Target: worldradar.WatchTarget{SourceURI: arxivSrv.URL},
		Now:    time.Now().UTC(),
	})
	if arxivRes.Status != worldradar.RunStatusOK || len(arxivRes.Records) != 1 {
		t.Fatalf("arxiv collect failed: %+v", arxivRes)
	}
	arxRec := arxivRes.Records[0]
	if arxRec.EntityName != "Dr. Ada Lovelace" || arxRec.StrongID != "arxiv:2611.04200v1" {
		t.Fatalf("unexpected arxiv entity/strongId: entity=%q strongId=%q", arxRec.EntityName, arxRec.StrongID)
	}
	if len(arxRec.Domains) != 2 || arxRec.Domains[0] != "D12" || arxRec.Domains[1] != "D06" {
		t.Fatalf("expected arxiv domains [D12 D06], got %v", arxRec.Domains)
	}

	kevRes := (&worldradar.CyberKEVConnector{}).Collect(ctx, worldradar.CollectRequest{
		Target: worldradar.WatchTarget{SourceURI: kevSrv.URL},
		Now:    time.Now().UTC(),
	})
	if kevRes.Status != worldradar.RunStatusOK || len(kevRes.Records) != 2 {
		t.Fatalf("cyber_kev collect failed: %+v", kevRes)
	}
	if kevRes.Records[0].ExternalID != "CVE-2026-9901" || kevRes.Records[0].StrongID != "CVE-2026-9901" || kevRes.Records[0].EntityName != "Acme EdgeGateway" {
		t.Fatalf("unexpected kev[0] fields: %+v", kevRes.Records[0])
	}
	if kevRes.Records[0].ImpactHint != 0.9 || kevRes.Records[0].UrgencyHint != 0.98 {
		t.Fatalf("expected ransomware KEV impact=0.9 urgency=0.98, got impact=%v urgency=%v", kevRes.Records[0].ImpactHint, kevRes.Records[0].UrgencyHint)
	}
	if kevRes.Records[1].UrgencyHint != 0.92 {
		t.Fatalf("expected non-ransomware KEV urgency=0.92, got %v", kevRes.Records[1].UrgencyHint)
	}

	usgsRes := (&worldradar.USGSGeoConnector{}).Collect(ctx, worldradar.CollectRequest{
		Target: worldradar.WatchTarget{SourceURI: usgsSrv.URL},
		Now:    time.Now().UTC(),
	})
	if usgsRes.Status != worldradar.RunStatusOK || len(usgsRes.Records) != 1 {
		t.Fatalf("usgs_geo collect failed: %+v", usgsRes)
	}
	geoRec := usgsRes.Records[0]
	if len(geoRec.Domains) != 1 || geoRec.Domains[0] != "D14" {
		t.Fatalf("expected D14 domain on usgs_geo, got %v", geoRec.Domains)
	}
	expectedImpact := math.Min(1.0, 6.8/9.0)
	if math.Abs(geoRec.ImpactHint-expectedImpact) > 1e-9 || geoRec.UrgencyHint < 0.85 {
		t.Fatalf("unexpected usgs_geo hints: impact=%v urgency=%v", geoRec.ImpactHint, geoRec.UrgencyHint)
	}
	expectedOcc := time.UnixMilli(1795680000000).UTC().Format(time.RFC3339Nano)
	if geoRec.OccurredAt != expectedOcc {
		t.Fatalf("expected occurredAt %s, got %s", expectedOcc, geoRec.OccurredAt)
	}
	var geoPayload struct {
		Lat       float64 `json:"lat"`
		Lon       float64 `json:"lon"`
		DepthKm   float64 `json:"depthKm"`
		Magnitude float64 `json:"magnitude"`
		Tsunami   int     `json:"tsunami"`
	}
	if err := json.Unmarshal([]byte(geoRec.PayloadJSON), &geoPayload); err != nil {
		t.Fatalf("unmarshal usgs payload: %v", err)
	}
	if geoPayload.Lat != 37.42 || geoPayload.Lon != 142.15 || geoPayload.DepthKm != 35.5 || geoPayload.Magnitude != 6.8 || geoPayload.Tsunami != 1 {
		t.Fatalf("unexpected usgs payload: %+v", geoPayload)
	}

	// End-to-end ingestion through Engine + BuildEntityGraph
	inst, err := eng.InstallPack(ctx, "radar.world-events", "V2 Connectors", "alert")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	for _, tgt := range []worldradar.WatchTargetInput{
		{CanonicalName: "arXiv AI", ConnectorID: "arxiv", SourceURI: arxivSrv.URL},
		{CanonicalName: "CISA KEV", ConnectorID: "cyber_kev", SourceURI: kevSrv.URL},
		{CanonicalName: "USGS Geo", ConnectorID: "usgs_geo", SourceURI: usgsSrv.URL},
	} {
		if _, addErr := eng.AddWatchTarget(ctx, inst.ID, tgt); addErr != nil {
			t.Fatalf("add target %s: %v", tgt.ConnectorID, addErr)
		}
	}

	summary, err := eng.RunInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("run instance: %v", err)
	}
	if summary.RecordsIngest != 4 || summary.EventsTouched != 4 {
		t.Fatalf("expected 4 ingested records and 4 events, got %+v", summary)
	}

	graph, err := eng.BuildEntityGraph(ctx, 50)
	if err != nil {
		t.Fatalf("BuildEntityGraph: %v", err)
	}
	if len(graph.Nodes) < 8 || len(graph.Edges) != 4 {
		t.Fatalf("expected >=8 nodes and 4 edges in entity graph, got nodes=%d edges=%d", len(graph.Nodes), len(graph.Edges))
	}
}

func TestV2_ProposalsApproveRejectAndRoutes(t *testing.T) {
	ctx := context.Background()
	eng, _ := openTestEngine(t, worldradar.Options{})
	inst, err := eng.InstallPack(ctx, "radar.social-trends", "Social Radar", "monitor")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}

	// 1. Propose + Approve -> attaches watch target
	prop1, err := eng.ProposeTarget(ctx, worldradar.ProposalInput{
		InstanceID:    inst.ID,
		TargetType:    "feed",
		CanonicalName: "arXiv cs.CL",
		ConnectorID:   "arxiv",
		SourceURI:     "https://export.arxiv.org/api/query?search_query=cat:cs.CL",
		Reason:        "Emerging NLP discourse",
	})
	if err != nil {
		t.Fatalf("ProposeTarget 1: %v", err)
	}
	if prop1.Status != "pending" {
		t.Fatalf("expected pending status, got %s", prop1.Status)
	}

	approved, err := eng.ApproveProposal(ctx, prop1.ID)
	if err != nil {
		t.Fatalf("ApproveProposal: %v", err)
	}
	if approved.Status != "approved" || approved.DecidedAt == "" {
		t.Fatalf("expected approved proposal with decidedAt, got %+v", approved)
	}
	targets, err := eng.ListWatchTargets(ctx, inst.ID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("expected 1 watch target after approval, got %d (err=%v)", len(targets), err)
	}

	// 2. Propose + Reject -> NEVER reset back to pending when proposed again
	rejectedURI := "https://spam.example/rss.xml"
	prop2, err := eng.ProposeTarget(ctx, worldradar.ProposalInput{
		InstanceID:    inst.ID,
		TargetType:    "feed",
		CanonicalName: "Spam Feed",
		ConnectorID:   "rss",
		SourceURI:     rejectedURI,
		Reason:        "Candidate mirror",
	})
	if err != nil {
		t.Fatalf("ProposeTarget 2: %v", err)
	}
	rejected, err := eng.RejectProposal(ctx, prop2.ID, "low signal-to-noise source")
	if err != nil {
		t.Fatalf("RejectProposal: %v", err)
	}
	if rejected.Status != "rejected" || rejected.DecisionReason != "low signal-to-noise source" {
		t.Fatalf("expected rejected status and reason, got %+v", rejected)
	}

	reproposed, err := eng.ProposeTarget(ctx, worldradar.ProposalInput{
		InstanceID:    inst.ID,
		TargetType:    "feed",
		CanonicalName: "Spam Feed Renamed",
		ConnectorID:   "rss",
		SourceURI:     rejectedURI,
		Reason:        "Retry discovery",
	})
	if err != nil {
		t.Fatalf("re-propose rejected URI: %v", err)
	}
	if reproposed.ID != rejected.ID || reproposed.Status != "rejected" {
		t.Fatalf("rejected proposal must NEVER reset to pending; got %+v", reproposed)
	}

	// Verify rejected URI was NOT added to watch targets
	targetsAfter, _ := eng.ListWatchTargets(ctx, inst.ID)
	if len(targetsAfter) != 1 {
		t.Fatalf("expected still 1 watch target (rejected never added), got %d", len(targetsAfter))
	}

	// 3. REST routes check
	router := echo.New()
	eng.RegisterRoutes(router.Group("/api"))

	reqGraph := httptest.NewRequest(http.MethodGet, "/api/radar/graph?limit=25", nil)
	recGraph := httptest.NewRecorder()
	router.ServeHTTP(recGraph, reqGraph)
	if recGraph.Code != http.StatusOK {
		t.Fatalf("GET /api/radar/graph status=%d body=%s", recGraph.Code, recGraph.Body.String())
	}

	reqList := httptest.NewRequest(http.MethodGet, "/api/radar/proposals?instanceId="+inst.ID, nil)
	recList := httptest.NewRecorder()
	router.ServeHTTP(recList, reqList)
	if recList.Code != http.StatusOK || !strings.Contains(recList.Body.String(), prop1.ID) {
		t.Fatalf("GET /api/radar/proposals status=%d body=%s", recList.Code, recList.Body.String())
	}

	createBody := bytes.NewBufferString(`{"instanceId":"` + inst.ID + `","targetType":"feed","canonicalName":"USGS Live","connectorId":"usgs_geo","sourceUri":"https://earthquake.usgs.gov/earthquakes/feed/v1.0/summary/all_hour.geojson"}`)
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/radar/proposals", createBody)
	reqCreate.Header.Set("Content-Type", "application/json")
	recCreate := httptest.NewRecorder()
	router.ServeHTTP(recCreate, reqCreate)
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("POST /api/radar/proposals status=%d body=%s", recCreate.Code, recCreate.Body.String())
	}
	var prop3 worldradar.DiscoveryProposal
	if err := json.Unmarshal(recCreate.Body.Bytes(), &prop3); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}

	reqReject := httptest.NewRequest(http.MethodPost, "/api/radar/proposals/"+prop3.ID+"/reject", bytes.NewBufferString(`{"reason":"duplicate"}`))
	reqReject.Header.Set("Content-Type", "application/json")
	recReject := httptest.NewRecorder()
	router.ServeHTTP(recReject, reqReject)
	if recReject.Code != http.StatusOK {
		t.Fatalf("POST /proposals/:id/reject status=%d body=%s", recReject.Code, recReject.Body.String())
	}

	// Approve endpoint on a fresh proposal
	reqCreate2 := httptest.NewRequest(http.MethodPost, "/api/radar/proposals", bytes.NewBufferString(`{"instanceId":"`+inst.ID+`","targetType":"feed","canonicalName":"CISA KEV","connectorId":"cyber_kev","sourceUri":"https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"}`))
	reqCreate2.Header.Set("Content-Type", "application/json")
	recCreate2 := httptest.NewRecorder()
	router.ServeHTTP(recCreate2, reqCreate2)
	if recCreate2.Code != http.StatusCreated {
		t.Fatalf("POST /proposals 2 status=%d", recCreate2.Code)
	}
	var prop4 worldradar.DiscoveryProposal
	_ = json.Unmarshal(recCreate2.Body.Bytes(), &prop4)

	reqApprove := httptest.NewRequest(http.MethodPost, "/api/radar/proposals/"+prop4.ID+"/approve", nil)
	recApprove := httptest.NewRecorder()
	router.ServeHTTP(recApprove, reqApprove)
	if recApprove.Code != http.StatusOK {
		t.Fatalf("POST /proposals/:id/approve status=%d body=%s", recApprove.Code, recApprove.Body.String())
	}
}

func TestV2_StartSchedulerAndCleanShutdownOnWindows(t *testing.T) {
	ctx := context.Background()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	eng, err := worldradar.NewEngine(hub, worldradar.Options{})
	if err != nil {
		_ = hub.Close()
		t.Fatalf("new worldradar engine: %v", err)
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id":          "sched-1",
					"title":       "Scheduled Poll Signal",
					"url":         "https://sched.example/1",
					"publishedAt": "2026-11-26T13:00:00Z",
					"summary":     "Collected by background scheduler.",
				},
			},
		})
	}))
	defer srv.Close()

	inst, err := eng.InstallPack(ctx, "radar.business-competitor", "Auto Poll Radar", "monitor")
	if err != nil {
		_ = hub.Close()
		t.Fatalf("install pack: %v", err)
	}
	// Enable autoPoll in config_json
	if _, err := hub.DB().ExecContext(ctx, `
UPDATE radar_instances SET config_json = json_set(config_json, '$.autoPoll', json('true')) WHERE id = ?`, inst.ID,
	); err != nil {
		_ = hub.Close()
		t.Fatalf("enable autoPoll: %v", err)
	}
	if _, err := eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
		CanonicalName: "Scheduled Feed",
		ConnectorID:   "web_feed",
		SourceURI:     srv.URL,
	}); err != nil {
		_ = hub.Close()
		t.Fatalf("add target: %v", err)
	}

	eng.StartScheduler(25 * time.Millisecond)

	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
	}
	if hits.Load() == 0 {
		_ = hub.Close()
		t.Fatal("expected StartScheduler to poll autoPoll=true instance")
	}

	// hub.Close invokes registered engine.Close before closing SQLite and releasing t.TempDir() locks.
	if err := hub.Close(); err != nil {
		t.Fatalf("hub.Close (via RegisterCloser -> engine.Close) failed: %v", err)
	}
	// Idempotent Close check
	if err := eng.Close(); err != nil {
		t.Fatalf("second eng.Close failed: %v", err)
	}
}
