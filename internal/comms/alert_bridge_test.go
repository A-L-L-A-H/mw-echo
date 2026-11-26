package comms_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/comms"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestAlertBridgeCooldownAndSeverityFilter(t *testing.T) {
	ctx := context.Background()
	var discordHits atomic.Int32
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		discordHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhookServer.Close()

	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	mgr, err := comms.NewManager(hub, comms.Options{HTTPClient: webhookServer.Client()})
	if err != nil {
		t.Fatalf("new comms manager: %v", err)
	}
	if _, err := mgr.UpsertChannel(ctx, comms.ChannelUpsertInput{
		Kind:   comms.KindDiscord,
		Name:   "ops-alerts",
		Secret: webhookServer.URL,
	}); err != nil {
		t.Fatalf("upsert discord channel: %v", err)
	}

	bridge := comms.NewAlertBridge(mgr, 10*time.Minute)

	// Non-critical severity suppressed
	lowOutcome, err := bridge.Dispatch(ctx, comms.AlertIncident{
		Source:   "disk_doctor",
		Severity: "P2",
		Title:    "Disk C: at 75%",
		Summary:  "Advisory usage level",
	})
	if err != nil {
		t.Fatalf("dispatch P2 failed: %v", err)
	}
	if lowOutcome.Dispatched || !lowOutcome.Suppressed || lowOutcome.SuppressionReason != "below_severity_threshold" {
		t.Fatalf("expected P2 to be suppressed, got %+v", lowOutcome)
	}

	// First P0 incident dispatches
	firstOutcome, err := bridge.Dispatch(ctx, comms.AlertIncident{
		Source:    "disk_doctor",
		Severity:  "CRITICAL",
		DedupeKey: "disk:C:critical",
		Title:     "Drive C: 97% full",
		Summary:   "Only 4.2 GB remaining on C:",
		Metadata:  map[string]string{"drive": "C:", "usedPct": "97.1"},
	})
	if err != nil {
		t.Fatalf("dispatch first critical alert failed: %v", err)
	}
	if !firstOutcome.Dispatched || firstOutcome.ChannelCount != 1 || discordHits.Load() != 1 {
		t.Fatalf("expected 1 dispatch, got %+v (hits=%d)", firstOutcome, discordHits.Load())
	}

	// Duplicate within cooldown suppressed
	secondOutcome, err := bridge.Dispatch(ctx, comms.AlertIncident{
		Source:    "disk_doctor",
		Severity:  "P0",
		DedupeKey: "disk:C:critical",
		Title:     "Drive C: 97% full",
		Summary:   "Still critical",
	})
	if err != nil {
		t.Fatalf("dispatch duplicate critical alert failed: %v", err)
	}
	if secondOutcome.Dispatched || !secondOutcome.Suppressed || secondOutcome.SuppressionReason != "cooldown_active" {
		t.Fatalf("expected duplicate to be suppressed by cooldown, got %+v", secondOutcome)
	}
	if discordHits.Load() != 1 {
		t.Fatalf("expected webhook hit count to remain 1, got %d", discordHits.Load())
	}
}
