package whatsapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/briefing"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func TestBridgeMorningBrief0700WIB(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	mgr, err := NewManager(hub, Options{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer mgr.Close()

	var capturedPhone, capturedText string
	bridge, err := NewBridge(mgr, BridgeConfig{
		DeviceID:     "flagship-wa",
		RecipientJID: "6281234567890",
		SendText: func(_ context.Context, _, phone, text string) (*SendResult, error) {
			capturedPhone = phone
			capturedText = text
			return &SendResult{MessageID: "wamid.flagship.001", ID: "wamid.flagship.001"}, nil
		},
	})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}

	beforeBrief := time.Date(2026, 11, 27, 6, 30, 0, 0, briefing.WIBZone)
	nextRun := bridge.NextScheduledWIB(beforeBrief)
	if nextRun.Hour() != 7 || nextRun.Minute() != 0 {
		t.Fatalf("expected 07:00 WIB, got %s", nextRun.Format(time.RFC3339))
	}

	res, err := bridge.TriggerMorningBrief(context.Background(), briefing.MorningMetrics{
		Date:            time.Date(2026, 11, 27, 7, 0, 0, 0, briefing.WIBZone),
		MRRIDR:          125_000_000,
		MRRGrowthPct:    12.5,
		ActiveCustomers: 340,
		OverdueInvoices: 2,
		NASHealthyNodes: 4,
		NASTotalNodes:   4,
		NASCPUUsagePct:  28.4,
		DiskUsedBytes:   400 * 1024 * 1024 * 1024,
		DiskTotalBytes:  1024 * 1024 * 1024 * 1024,
		CIPassingRepos:  24,
		CITotalRepos:    24,
	})
	if err != nil {
		t.Fatalf("trigger morning brief: %v", err)
	}
	if !res.Delivered || res.MessageID != "wamid.flagship.001" || res.OverallStatus != "GREEN" {
		t.Fatalf("unexpected morning brief result: %+v", res)
	}
	if capturedPhone != "6281234567890" || !strings.Contains(capturedText, "07:00 WIB") {
		t.Fatalf("unexpected outbound message: phone=%q text=%q", capturedPhone, capturedText)
	}
}

func TestBridgePairingAndRedaction(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	mgr, err := NewManager(hub, Options{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer mgr.Close()

	if _, err := mgr.EnsureDevice(context.Background(), "flagship-wa", "Flagship Primary"); err != nil {
		t.Fatalf("ensure device: %v", err)
	}
	bridge, err := NewBridge(mgr, BridgeConfig{DeviceID: "flagship-wa"})
	if err != nil {
		t.Fatalf("new bridge: %v", err)
	}

	status, err := bridge.PairingStatus(context.Background())
	if err != nil || status.DeviceID != "flagship-wa" {
		t.Fatalf("pairing status err=%v status=%+v", err, status)
	}

	redacted := RedactCredentialFields(map[string]any{
		"device_id":     "flagship-wa",
		"session_token": "wa-secret-session-xyz",
		"noise_key":     "raw-key-bytes",
		"status":        "connected",
	})
	if redacted["session_token"] != "[REDACTED]" || redacted["noise_key"] != "[REDACTED]" {
		t.Fatalf("credentials not redacted: %+v", redacted)
	}
	if redacted["device_id"] != "flagship-wa" || redacted["status"] != "connected" {
		t.Fatalf("non-sensitive fields altered: %+v", redacted)
	}
}

func TestBridgeV1RoutesSmoke(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	mgr, err := NewManager(hub, Options{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer mgr.Close()

	app := echo.New()
	api := app.Group("/api")
	mgr.RegisterRoutes(app, api)

	t.Run("GET /api/v1/system/health", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/system/health", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var payload struct {
			Code    string         `json:"code"`
			Results map[string]any `json:"results"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		if payload.Results["status"] != "ok" {
			t.Fatalf("unexpected health status: %+v", payload.Results)
		}
	})

	t.Run("GET /api/v1/system/metrics", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/system/metrics", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "gowa_devices_total") {
			t.Fatalf("missing gowa_devices_total in metrics: %s", rec.Body.String())
		}
	})
}
