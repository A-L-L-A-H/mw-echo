package comms_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/comms"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestAlertBridgeDispatch(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	var mu sync.Mutex
	var calls []string
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mockServer.Close()

	mgr, err := comms.NewManager(hub, comms.Options{
		HTTPClient: mockServer.Client(),
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	ctx := context.Background()
	if _, err := mgr.UpsertChannel(ctx, comms.ChannelUpsertInput{
		ID:        "tg-ops",
		Kind:      comms.KindTelegram,
		Name:      "Ops Telegram",
		Secret:    "7192837465:AAH8xSecretBotToken",
		TargetRef: "-10099887766",
	}); err != nil {
		t.Fatalf("upsert telegram channel: %v", err)
	}

	bridge := comms.NewAlertBridge(mgr, 15*time.Minute)

	incident := comms.AlertIncident{
		Source:   "diskdoctor",
		Severity: "P0",
		Title:    "Disk failure detected",
		Summary:  "Critical disk error on /dev/sda",
	}

	result, err := bridge.Dispatch(ctx, incident)
	if err != nil {
		t.Fatalf("AlertBridge.Dispatch failed: %v", err)
	}

	if !result.Dispatched {
		t.Error("expected P0 incident to be dispatched")
	}
	if result.Severity != "P0" {
		t.Errorf("expected severity 'P0', got '%s'", result.Severity)
	}
	if result.ChannelCount == 0 {
		t.Error("expected at least 1 channel dispatched")
	}

	incident2 := comms.AlertIncident{
		Source:   "diskdoctor",
		Severity: "P0",
		Title:    "Disk failure detected",
		Summary:  "Critical disk error on /dev/sda",
	}

	result2, err := bridge.Dispatch(ctx, incident2)
	if err != nil {
		t.Fatalf("AlertBridge.Dispatch (cooldown) failed: %v", err)
	}

	if result2.Dispatched {
		t.Error("expected cooldown to suppress second identical incident")
	}
	if result2.SuppressionReason != "cooldown_active" {
		t.Errorf("expected suppression reason 'cooldown_active', got '%s'", result2.SuppressionReason)
	}

	incident3 := comms.AlertIncident{
		Source:   "worldradar",
		Severity: "INFO",
		Title:    "Informational message",
		Summary:  "Just info",
	}

	result3, err := bridge.Dispatch(ctx, incident3)
	if err != nil {
		t.Fatalf("AlertBridge.Dispatch (INFO) failed: %v", err)
	}

	if result3.Dispatched {
		t.Error("expected INFO severity to not be dispatched")
	}
	if result3.SuppressionReason != "below_severity_threshold" {
		t.Errorf("expected suppression reason 'below_severity_threshold', got '%s'", result3.SuppressionReason)
	}

	incident4 := comms.AlertIncident{
		Source: "system",
	}

	result4, err := bridge.Dispatch(ctx, incident4)
	if err != nil {
		t.Fatalf("AlertBridge.Dispatch (empty) failed: %v", err)
	}

	if !result4.Suppressed {
		t.Error("expected empty alert to be suppressed")
	}
	t.Logf("AlertBridge dispatch tests passed!")
}
