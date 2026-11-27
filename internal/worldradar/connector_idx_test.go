package worldradar_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/worldradar"
)

func TestIDXBEIConnector_AnnouncementsStocksETagAndIngestion(t *testing.T) {
	ctx := context.Background()
	eng, db := openTestEngine(t, worldradar.Options{})

	const feedETag = `"idx-feed-v1"`
	var requestCount atomic.Int32

	idxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.Header.Get("If-None-Match") == feedETag {
			w.Header().Set("ETag", feedETag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", feedETag)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"announcements": []map[string]any{
				{
					"id":          "IDX-ANN-2026-001",
					"ticker":      "bbca",
					"companyName": "Bank Central Asia Tbk",
					"title":       "Laporan Keuangan Kuartal III 2026",
					"summary":     "Laba bersih tumbuh 12.4% YoY dengan ekspansi kredit korporasi.",
					"category":    "financial_report",
					"publishedAt": "2026-11-26T09:00:00Z",
					"url":         "https://www.idx.co.id/announcements/IDX-ANN-2026-001",
					"changePct":   1.25,
					"volumeRatio": 1.10,
				},
			},
			"stocks": []map[string]any{
				{
					"code":        "goto",
					"name":        "GoTo Gojek Tokopedia Tbk",
					"lastPrice":   84.0,
					"changePct":   -7.69,
					"volumeRatio": 4.25,
					"updatedAt":   "2026-11-26T09:15:00Z",
					"url":         "https://www.idx.co.id/stocks/GOTO",
				},
			},
		})
	}))
	defer idxServer.Close()

	connector := &worldradar.IDXBEIConnector{}
	if connector.ID() != "idx_bei" {
		t.Fatalf("expected connector ID idx_bei, got %q", connector.ID())
	}

	firstCollect := connector.Collect(ctx, worldradar.CollectRequest{
		Target: worldradar.WatchTarget{SourceURI: idxServer.URL},
		Now:    time.Now().UTC(),
	})
	if firstCollect.Status != worldradar.RunStatusOK {
		t.Fatalf("expected RunStatusOK, got %s (err=%s)", firstCollect.Status, firstCollect.ErrorMessage)
	}
	if firstCollect.ETag != feedETag {
		t.Fatalf("expected ETag %q, got %q", feedETag, firstCollect.ETag)
	}
	if len(firstCollect.Records) != 2 {
		t.Fatalf("expected 2 records (1 announcement + 1 stock), got %d", len(firstCollect.Records))
	}

	annRec := firstCollect.Records[0]
	if annRec.StrongID != "IDX:BBCA" || annRec.EntityName != "Bank Central Asia Tbk" {
		t.Fatalf("unexpected announcement entity/strongId: entity=%q strongId=%q", annRec.EntityName, annRec.StrongID)
	}
	if len(annRec.Domains) != 2 || annRec.Domains[0] != "D04" || annRec.Domains[1] != "D05" {
		t.Fatalf("expected domains [D04 D05], got %v", annRec.Domains)
	}

	stockRec := firstCollect.Records[1]
	if stockRec.StrongID != "IDX:GOTO" || stockRec.EntityName != "GoTo Gojek Tokopedia Tbk" {
		t.Fatalf("unexpected stock entity/strongId: entity=%q strongId=%q", stockRec.EntityName, stockRec.StrongID)
	}
	if stockRec.ImpactHint <= annRec.ImpactHint || stockRec.UrgencyHint <= annRec.UrgencyHint {
		t.Fatalf("expected abnormal stock move to have higher hints than normal announcement: stock=(%.2f,%.2f) ann=(%.2f,%.2f)",
			stockRec.ImpactHint, stockRec.UrgencyHint, annRec.ImpactHint, annRec.UrgencyHint)
	}
	if stockRec.ImpactHint < 0.70 || stockRec.UrgencyHint < 0.75 {
		t.Fatalf("expected scaled abnormal hints (>=0.70, >=0.75), got (%.2f, %.2f)", stockRec.ImpactHint,
			stockRec.UrgencyHint)
	}

	var stockPayload struct {
		Ticker      string  `json:"ticker"`
		ChangePct   float64 `json:"changePct"`
		VolumeRatio float64 `json:"volumeRatio"`
		Exchange    string  `json:"exchange"`
	}
	if err := json.Unmarshal([]byte(stockRec.PayloadJSON), &stockPayload); err != nil {
		t.Fatalf("unmarshal stock payload: %v", err)
	}
	if stockPayload.Ticker != "GOTO" || stockPayload.ChangePct != -7.69 || stockPayload.VolumeRatio != 4.25 || stockPayload.Exchange != "IDX" {
		t.Fatalf("unexpected stock payload: %+v", stockPayload)
	}

	// Conditional GET with ETag -> 304 no_new_data
	secondCollect := connector.Collect(ctx, worldradar.CollectRequest{
		Target: worldradar.WatchTarget{
			SourceURI: idxServer.URL,
			ETag:      feedETag,
		},
		Now: time.Now().UTC(),
	})
	if secondCollect.Status != worldradar.RunStatusNoNewData {
		t.Fatalf("expected RunStatusNoNewData on 304, got %s", secondCollect.Status)
	}

	// End-to-end Engine.RunInstance ingestion into radar_events
	inst, err := eng.InstallPack(ctx, "radar.world-events", "IDX Market Radar", "alert")
	if err != nil {
		t.Fatalf("install pack: %v", err)
	}
	if _, err := eng.AddWatchTarget(ctx, inst.ID, worldradar.WatchTargetInput{
		CanonicalName: "IDX BEI Disclosures & Anomalies",
		ConnectorID:   "idx_bei",
		SourceURI:     idxServer.URL,
	}); err != nil {
		t.Fatalf("add idx_bei watch target: %v", err)
	}

	runSummary, err := eng.RunInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("RunInstance: %v", err)
	}
	if runSummary.RecordsIngest != 2 || runSummary.EventsTouched != 2 {
		t.Fatalf("expected 2 ingested records and 2 events touched, got %+v", runSummary)
	}

	var eventCount int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM radar_events`).Scan(&eventCount); err != nil {
		t.Fatalf("query radar_events count: %v", err)
	}
	if eventCount != 2 {
		t.Fatalf("expected 2 rows in radar_events, got %d", eventCount)
	}
}
