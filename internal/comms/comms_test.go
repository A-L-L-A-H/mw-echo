package comms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/storage"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func openTestHub(t *testing.T) *store.HubStore {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	return hub
}

func TestChannelEncryptionAndRedaction(t *testing.T) {
	hub := openTestHub(t)
	mgr, err := NewManager(hub, Options{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	ctx := context.Background()
	rawBotToken := "7192837465:AAH8xSecretBotToken9988"
	shortSecret := "tok123"

	tgChannel, err := mgr.UpsertChannel(ctx, ChannelUpsertInput{
		ID:        "tg-ops",
		Kind:      KindTelegram,
		Name:      "Ops Telegram",
		Secret:    rawBotToken,
		TargetRef: "-10099887766",
	})
	if err != nil {
		t.Fatalf("upsert telegram channel: %v", err)
	}
	if tgChannel.SecretHint != "7192...9988" {
		t.Fatalf("expected masked hint 7192...9988, got %q", tgChannel.SecretHint)
	}
	if !tgChannel.SecretConfigured {
		t.Fatal("expected SecretConfigured=true")
	}

	shortChannel, err := mgr.UpsertChannel(ctx, ChannelUpsertInput{
		ID:        "tg-short",
		Kind:      KindTelegram,
		Name:      "Short Token",
		Secret:    shortSecret,
		TargetRef: "12345",
	})
	if err != nil {
		t.Fatalf("upsert short secret channel: %v", err)
	}
	if shortChannel.SecretHint != "configured" {
		t.Fatalf("expected 'configured' hint for short secret, got %q", shortChannel.SecretHint)
	}

	// Verify AES-256-GCM encryption at rest in SQLite.
	var storedCiphertext string
	if err := hub.DB().QueryRow(`SELECT secret_enc FROM comms_channels WHERE id = ?`, tgChannel.ID).Scan(&storedCiphertext); err != nil {
		t.Fatalf("query secret_enc: %v", err)
	}
	if storedCiphertext == "" || storedCiphertext == rawBotToken || strings.Contains(storedCiphertext, "AAH8xSecret") {
		t.Fatalf("secret_enc is not encrypted at rest: %q", storedCiphertext)
	}

	secretBox, err := storage.OpenSecretBox(hub.DataDir())
	if err != nil {
		t.Fatalf("open secret box: %v", err)
	}
	decrypted, err := secretBox.Decrypt(storedCiphertext)
	if err != nil {
		t.Fatalf("decrypt stored secret: %v", err)
	}
	if string(decrypted) != rawBotToken {
		t.Fatalf("decrypted secret mismatch: got %q, want %q", string(decrypted), rawBotToken)
	}

	// Verify ListChannels redacts raw secret and preserves it across secret-less edit.
	listed, err := mgr.ListChannels(ctx)
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(listed))
	}
	serialized, err := json.Marshal(listed)
	if err != nil {
		t.Fatalf("marshal channels: %v", err)
	}
	if strings.Contains(string(serialized), rawBotToken) || strings.Contains(string(serialized),
		shortSecret) || strings.Contains(string(serialized), storedCiphertext) {
		t.Fatalf("raw or encrypted secret leaked in ListChannels output: %s", string(serialized))
	}

	disabledFlag := false
	edited, err := mgr.UpsertChannel(ctx, ChannelUpsertInput{
		ID:        "tg-ops",
		Kind:      KindTelegram,
		Name:      "Ops Telegram Renamed",
		Secret:    "",
		TargetRef: "-10011223344",
		Enabled:   &disabledFlag,
	})
	if err != nil {
		t.Fatalf("edit channel without secret: %v", err)
	}
	if edited.SecretHint != "7192...9988" || !edited.SecretConfigured || edited.Enabled {
		t.Fatalf("unexpected edited channel state: %+v", edited)
	}
	var preservedCiphertext string
	if err := hub.DB().QueryRow(`SELECT secret_enc FROM comms_channels WHERE id = ?`, "tg-ops").Scan(&preservedCiphertext); err != nil {
		t.Fatalf("query preserved secret_enc: %v", err)
	}
	if preservedCiphertext != storedCiphertext {
		t.Fatal("expected secret_enc to be preserved on empty-secret edit")
	}

	if err := mgr.DeleteChannel(ctx, "tg-short"); err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	if _, err := mgr.GetChannel(ctx, "tg-short"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestTelegramSendAndDeliveryLedger(t *testing.T) {
	type capturedTelegramReq struct {
		Path      string
		ChatID    string `json:"chat_id"`
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode"`
	}

	var mu sync.Mutex
	var captured []capturedTelegramReq

	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ := io.ReadAll(r.Body)
		var parsed capturedTelegramReq
		_ = json.Unmarshal(rawBody, &parsed)
		parsed.Path = r.URL.Path

		mu.Lock()
		captured = append(captured, parsed)
		mu.Unlock()

		if strings.Contains(parsed.Text, "FORCE_FAIL") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer tgServer.Close()

	hub := openTestHub(t)
	mgr, err := NewManager(hub, Options{
		HTTPClient:      tgServer.Client(),
		TelegramAPIBase: tgServer.URL,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	ctx := context.Background()
	rawToken := "998877:AAZZTestTelegramToken"
	if _, err := mgr.UpsertChannel(ctx, ChannelUpsertInput{
		ID:        "tg-primary",
		Kind:      KindTelegram,
		Name:      "Primary TG",
		Secret:    rawToken,
		TargetRef: "-100555444",
	}); err != nil {
		t.Fatalf("upsert telegram channel: %v", err)
	}

	// Send resolving by Kind with fallback to channel TargetRef.
	delivOK, err := mgr.Send(ctx, SendInput{
		Kind:  KindTelegram,
		Title: "Alert <P0>",
		Body:  "Node latency & packet loss elevated",
	})
	if err != nil {
		t.Fatalf("send telegram failed: %v", err)
	}
	if delivOK.Status != DeliveryStatusSent || delivOK.TargetRef != "-100555444" {
		t.Fatalf("unexpected telegram delivery: %+v", delivOK)
	}

	// Send failing request and confirm delivery ledger still records it.
	delivFail, err := mgr.Send(ctx, SendInput{
		ChannelID: "tg-primary",
		TargetRef: "999000",
		Title:     "Fail Test",
		Body:      "FORCE_FAIL",
	})
	if err == nil {
		t.Fatal("expected error for failed telegram response")
	}
	if delivFail.Status != DeliveryStatusFailed || !strings.Contains(delivFail.ErrorMsg, "400") {
		t.Fatalf("expected failed delivery record with errorMsg, got %+v", delivFail)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("expected 2 telegram HTTP requests, got %d", len(captured))
	}
	expectedPath := "/bot" + rawToken + "/sendMessage"
	if captured[0].Path != expectedPath {
		t.Fatalf("unexpected telegram path: got %q, want %q", captured[0].Path, expectedPath)
	}
	if captured[0].ChatID != "-100555444" || captured[0].ParseMode != "HTML" {
		t.Fatalf("unexpected telegram payload: %+v", captured[0])
	}
	if !strings.Contains(captured[0].Text, "<b>Alert &lt;P0&gt;</b>") || !strings.Contains(captured[0].Text,
		"latency &amp; packet") {
		t.Fatalf("unexpected HTML formatted text: %q", captured[0].Text)
	}

	ledger, err := mgr.ListDeliveries(ctx, 10)
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(ledger) != 2 {
		t.Fatalf("expected 2 persisted deliveries, got %d", len(ledger))
	}
}

func TestDiscordEmbedAndPriorityColors(t *testing.T) {
	type discordPayload struct {
		Username string `json:"username"`
		Embeds   []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Color       int    `json:"color"`
			Footer      struct {
				Text string `json:"text"`
			} `json:"footer"`
		} `json:"embeds"`
	}

	var mu sync.Mutex
	var payloads []discordPayload

	dcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ := io.ReadAll(r.Body)
		var parsed discordPayload
		_ = json.Unmarshal(rawBody, &parsed)
		mu.Lock()
		payloads = append(payloads, parsed)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer dcServer.Close()

	hub := openTestHub(t)
	mgr, err := NewManager(hub, Options{HTTPClient: dcServer.Client()})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	ctx := context.Background()
	if _, err := mgr.UpsertChannel(ctx, ChannelUpsertInput{
		ID:     "dc-ops",
		Kind:   KindDiscord,
		Name:   "Discord Ops",
		Secret: dcServer.URL + "/api/webhooks/123456/secret",
	}); err != nil {
		t.Fatalf("upsert discord channel: %v", err)
	}

	tiers := []struct {
		tier  string
		color int
	}{
		{"P0", 0xFF0055},
		{"p1", 0xFF9900},
		{"P2", 0xFFE600},
		{"", 0x00F5FF},
	}

	for _, tc := range tiers {
		deliv, err := mgr.Send(ctx, SendInput{
			Kind:         KindDiscord,
			Title:        "Signal " + tc.tier,
			Body:         "Payload body",
			PriorityTier: tc.tier,
		})
		if err != nil {
			t.Fatalf("send discord %q: %v", tc.tier, err)
		}
		if deliv.Status != DeliveryStatusSent {
			t.Fatalf("expected sent status for %q, got %+v", tc.tier, deliv)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != len(tiers) {
		t.Fatalf("expected %d discord payloads, got %d", len(tiers), len(payloads))
	}
	for idx, tc := range tiers {
		payload := payloads[idx]
		if payload.Username != "MW-ECHO // R!OTS" {
			t.Fatalf("unexpected discord username: %q", payload.Username)
		}
		if len(payload.Embeds) != 1 {
			t.Fatalf("expected 1 embed, got %d", len(payload.Embeds))
		}
		embed := payload.Embeds[0]
		if embed.Color != tc.color {
			t.Fatalf("tier %q: got color 0x%06X, want 0x%06X", tc.tier, embed.Color, tc.color)
		}
		if embed.Footer.Text == "" {
			t.Fatal("expected non-empty footer text")
		}
	}

	ledger, err := mgr.ListDeliveries(ctx, 10)
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(ledger) != 4 {
		t.Fatalf("expected 4 discord deliveries, got %d", len(ledger))
	}
}

func TestBroadcastFanoutAndRoutes(t *testing.T) {
	var mu sync.Mutex
	tgCalls := 0
	dcCalls := 0

	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tgCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer tgServer.Close()

	dcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dcCalls++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer dcServer.Close()

	hub := openTestHub(t)
	mgr, err := NewManager(hub, Options{
		HTTPClient:      tgServer.Client(),
		TelegramAPIBase: tgServer.URL,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	app := echo.New()
	mgr.RegisterRoutes(app.Group("/api"))

	performJSON := func(method, target string, body any) *httptest.ResponseRecorder {
		var bodyReader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			bodyReader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, target, bodyReader)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}

	// 1. Create active Telegram, active Discord, and disabled Discord channel via POST /api/comms/channels.
	r1 := performJSON(http.MethodPost, "/api/comms/channels", ChannelUpsertInput{
		ID:        "ch-tg",
		Kind:      KindTelegram,
		Name:      "TG Room",
		Secret:    "111222:SecretTelegramToken",
		TargetRef: "-100111",
	})
	if r1.Code != http.StatusCreated {
		t.Fatalf("expected 201 for telegram upsert, got %d: %s", r1.Code, r1.Body.String())
	}

	r2 := performJSON(http.MethodPost, "/api/comms/channels", ChannelUpsertInput{
		ID:     "ch-dc-on",
		Kind:   KindDiscord,
		Name:   "DC Active",
		Secret: dcServer.URL + "/webhook-active",
	})
	if r2.Code != http.StatusCreated {
		t.Fatalf("expected 201 for discord upsert, got %d: %s", r2.Code, r2.Body.String())
	}

	disabled := false
	r3 := performJSON(http.MethodPost, "/api/comms/channels", ChannelUpsertInput{
		ID:      "ch-dc-off",
		Kind:    KindDiscord,
		Name:    "DC Muted",
		Secret:  dcServer.URL + "/webhook-muted",
		Enabled: &disabled,
	})
	if r3.Code != http.StatusCreated {
		t.Fatalf("expected 201 for disabled discord upsert, got %d: %s", r3.Code, r3.Body.String())
	}

	// 2. GET /api/comms/channels verifies redaction.
	listResp := performJSON(http.MethodGet, "/api/comms/channels", nil)
	if listResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on list channels, got %d", listResp.Code)
	}
	if strings.Contains(listResp.Body.String(), "SecretTelegramToken") || strings.Contains(listResp.Body.String(),
		"webhook-active") {
		t.Fatalf("GET /channels leaked raw secret: %s", listResp.Body.String())
	}

	// 3. POST /api/comms/send
	sendResp := performJSON(http.MethodPost, "/api/comms/send", SendInput{
		Kind:         KindDiscord,
		Title:        "Direct Dispatch",
		Body:         "Single channel test",
		PriorityTier: "P1",
	})
	if sendResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on POST /send, got %d: %s", sendResp.Code, sendResp.Body.String())
	}

	// 4. POST /api/comms/broadcast fans out ONLY to the 2 enabled channels.
	bcastResp := performJSON(http.MethodPost, "/api/comms/broadcast", map[string]string{
		"title":        "R!OTS Flash Alert",
		"body":         "Corroborated signal detected",
		"priorityTier": "P0",
	})
	if bcastResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on POST /broadcast, got %d: %s", bcastResp.Code, bcastResp.Body.String())
	}
	var bcastBody struct {
		Deliveries []DeliveryRecord `json:"deliveries"`
		Count      int              `json:"count"`
	}
	if err := json.Unmarshal(bcastResp.Body.Bytes(), &bcastBody); err != nil {
		t.Fatalf("decode broadcast response: %v", err)
	}
	if bcastBody.Count != 2 || len(bcastBody.Deliveries) != 2 {
		t.Fatalf("expected 2 broadcast deliveries, got %+v", bcastBody)
	}

	mu.Lock()
	gotTG, gotDC := tgCalls, dcCalls
	mu.Unlock()
	if gotTG != 1 || gotDC != 2 {
		t.Fatalf("expected tgCalls=1 and dcCalls=2 (1 direct + 1 broadcast), got tg=%d dc=%d", gotTG, gotDC)
	}

	// 5. GET /api/comms/deliveries returns 3 deliveries total.
	delivResp := performJSON(http.MethodGet, "/api/comms/deliveries?limit=10", nil)
	if delivResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET /deliveries, got %d", delivResp.Code)
	}
	var delivBody struct {
		Deliveries []DeliveryRecord `json:"deliveries"`
		Count      int              `json:"count"`
	}
	if err := json.Unmarshal(delivResp.Body.Bytes(), &delivBody); err != nil {
		t.Fatalf("decode deliveries response: %v", err)
	}
	if delivBody.Count != 3 || len(delivBody.Deliveries) != 3 {
		t.Fatalf("expected 3 deliveries, got %+v", delivBody)
	}

	// 6. DELETE /api/comms/channels/:id
	delResp := performJSON(http.MethodDelete, "/api/comms/channels/ch-dc-off", nil)
	if delResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on DELETE /channels/:id, got %d: %s", delResp.Code, delResp.Body.String())
	}
	if !strings.Contains(delResp.Body.String(), `"deleted":true`) {
		t.Fatalf("unexpected delete response body: %s", delResp.Body.String())
	}
}
