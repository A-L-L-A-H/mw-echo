package comms

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestLegacySchemaMigrationSupportsNtfyAndResend(t *testing.T) {
	dir := t.TempDir()
	hub, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	defer func() { _ = hub.Close() }()

	_, err = hub.DB().Exec(`
CREATE TABLE comms_channels (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('telegram','discord')),
  name TEXT NOT NULL,
  secret_enc TEXT NOT NULL,
  secret_hint TEXT NOT NULL DEFAULT '',
  target_ref TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
INSERT INTO comms_channels VALUES ('legacy-tg', 'telegram', 'Legacy', 'enc', 'hint', '-1001', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
`)
	if err != nil {
		t.Fatalf("seed legacy table: %v", err)
	}

	mgr, err := NewManager(hub, Options{})
	if err != nil {
		t.Fatalf("new manager with legacy migration: %v", err)
	}

	ctx := context.Background()
	ntfyRec, err := mgr.UpsertChannel(ctx, ChannelUpsertInput{
		ID:        "ntfy-free",
		Kind:      KindNtfy,
		Name:      "Public ntfy",
		TargetRef: "https://ntfy.sh/mw-ops-topic",
	})
	if err != nil {
		t.Fatalf("upsert secretless ntfy after migration: %v", err)
	}
	if ntfyRec.SecretConfigured {
		t.Fatal("expected secretless ntfy channel to have SecretConfigured=false")
	}

	channels, err := mgr.ListChannels(ctx)
	if err != nil || len(channels) != 2 {
		t.Fatalf("expected legacy + ntfy rows preserved (2), got len=%d err=%v", len(channels), err)
	}
}

func TestNtfyResendDeliveryAndAlertBridge(t *testing.T) {
	hub := openTestHub(t)

	var gotNtfyTitle, gotNtfyPriority, gotNtfyAuth, gotNtfyBody string
	ntfySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNtfyTitle = r.Header.Get("Title")
		gotNtfyPriority = r.Header.Get("Priority")
		gotNtfyAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotNtfyBody = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfySrv.Close()

	var gotResendAuth string
	var gotResendPayload struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		Text    string   `json:"text"`
	}
	var resendStatus = http.StatusOK
	resendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotResendAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotResendPayload)
		w.WriteHeader(resendStatus)
	}))
	defer resendSrv.Close()

	mgr, err := NewManager(hub, Options{
		ResendAPIBase: resendSrv.URL,
		ResendFrom:    "billing@mw.local",
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	ctx := context.Background()
	if _, err := mgr.UpsertChannel(ctx, ChannelUpsertInput{
		ID:        "ntfy-p0",
		Kind:      KindNtfy,
		Name:      "Ntfy P0",
		TargetRef: ntfySrv.URL + "/mw-p0",
	}); err != nil {
		t.Fatalf("upsert ntfy: %v", err)
	}
	if _, err := mgr.UpsertChannel(ctx, ChannelUpsertInput{
		ID:        "resend-ops",
		Kind:      KindResend,
		Name:      "Resend Ops",
		Secret:    "re_test_123",
		TargetRef: "ops@example.com",
	}); err != nil {
		t.Fatalf("upsert resend: %v", err)
	}

	res, err := mgr.AlertBridge().Dispatch(ctx, AlertIncident{
		Source:   "backup",
		Severity: "P0",
		Title:    "Offsite Backup OK",
		Summary:  "PG dump encrypted",
	})
	if err != nil || !res.Dispatched || res.ChannelCount != 2 {
		t.Fatalf("expected 2 alert deliveries, got %+v err=%v", res, err)
	}
	if !strings.Contains(gotNtfyTitle, "Offsite Backup OK") || gotNtfyPriority != "urgent" || gotNtfyAuth != "" || !strings.Contains(gotNtfyBody, "PG dump encrypted") {
		t.Fatalf("unexpected ntfy headers/body: title=%q prio=%q auth=%q body=%q", gotNtfyTitle, gotNtfyPriority, gotNtfyAuth, gotNtfyBody)
	}
	if gotResendAuth != "Bearer re_test_123" || len(gotResendPayload.To) != 1 || gotResendPayload.To[0] != "ops@example.com" || gotResendPayload.From != "billing@mw.local" {
		t.Fatalf("unexpected resend request: auth=%q payload=%+v", gotResendAuth, gotResendPayload)
	}

	resendStatus = http.StatusBadRequest
	if _, err := mgr.Send(ctx, SendInput{ChannelID: "resend-ops", Title: "Fail", Body: "Test"}); err == nil {
		t.Fatal("expected HTTP 400 error on failed resend delivery")
	}
}
