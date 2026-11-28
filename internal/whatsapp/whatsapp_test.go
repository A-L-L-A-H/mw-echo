package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func openTestManager(t *testing.T, opts Options) (*Manager, *echo.Echo, *store.HubStore) {
	t.Helper()
	tempDir := t.TempDir()
	hub, err := store.Open(tempDir)
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	mgr, err := NewManager(hub, opts)
	if err != nil {
		_ = hub.Close()
		t.Fatalf("new whatsapp manager: %v", err)
	}
	t.Cleanup(func() {
		_ = mgr.Close()
		_ = hub.Close()
	})

	app := echo.New()
	app.HideBanner = true
	api := app.Group("/api")
	mgr.RegisterRoutes(app, api)
	return mgr, app, hub
}

func TestPureGoSQLStoreInitialization(t *testing.T) {
	mgr, _, hub := openTestManager(t, Options{})

	sessionsDBPath := filepath.Join(hub.DataDir(), "whatsapp_sessions.db")
	if _, err := os.Stat(sessionsDBPath); err != nil {
		t.Fatalf("expected whatsapp_sessions.db at %s: %v", sessionsDBPath, err)
	}

	devices, err := mgr.container.GetAllDevices(context.Background())
	if err != nil {
		t.Fatalf("whatsmeow sqlstore GetAllDevices failed: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("expected 0 initial whatsmeow sessions, got %d", len(devices))
	}

	stats, err := mgr.Stats(context.Background())
	if err != nil {
		t.Fatalf("query initial stats: %v", err)
	}
	if stats.Engine != "whatsmeow" || stats.DevicesCount != 0 || stats.ChatsCount != 0 || stats.MessagesCount != 0 {
		t.Fatalf("unexpected initial stats: %+v", stats)
	}
}

func TestGowaBasicAuthAndDeviceCRUD(t *testing.T) {
	_, app, _ := openTestManager(t, Options{
		BasicUser: "gowa-admin",
		BasicPass: "s3cr3t-pass",
	})

	// Unauthenticated request must fail with 401 and GOWA ERROR envelope.
	unauthReq := httptest.NewRequest(http.MethodGet, "/app/info", nil)
	unauthRec := httptest.NewRecorder()
	app.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without basic auth, got %d", unauthRec.Code)
	}
	var errEnv gowaEnvelope
	if err := json.Unmarshal(unauthRec.Body.Bytes(), &errEnv); err != nil || errEnv.Code != "ERROR" {
		t.Fatalf("expected GOWA ERROR envelope on 401, got %q", unauthRec.Body.String())
	}

	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte("gowa-admin:s3cr3t-pass"))

	// Authenticated /app/info returns SUCCESS envelope and whatsmeow metadata.
	infoReq := httptest.NewRequest(http.MethodGet, "/app/info", nil)
	infoReq.Header.Set("Authorization", authHeader)
	infoRec := httptest.NewRecorder()
	app.ServeHTTP(infoRec, infoReq)
	if infoRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /app/info, got %d: %s", infoRec.Code, infoRec.Body.String())
	}
	var infoBody struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Results map[string]string `json:"results"`
	}
	if err := json.Unmarshal(infoRec.Body.Bytes(), &infoBody); err != nil {
		t.Fatalf("decode /app/info: %v", err)
	}
	if infoBody.Code != "SUCCESS" || infoBody.Results["engine"] != "whatsmeow" || infoBody.Results["driver"] != "gowa" {
		t.Fatalf("unexpected /app/info payload: %+v", infoBody)
	}

	// POST /devices creates a named device.
	createPayload := `{"device_id":"sales-bot","label":"Sales Desk","webhook_url":"https://example.invalid/hook"}`
	createReq := httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(createPayload))
	createReq.Header.Set("Authorization", authHeader)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	app.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 on POST /devices, got %d: %s", createRec.Code, createRec.Body.String())
	}

	// GET /devices lists the created device inside GOWA envelope.
	listReq := httptest.NewRequest(http.MethodGet, "/devices", nil)
	listReq.Header.Set("Authorization", authHeader)
	listRec := httptest.NewRecorder()
	app.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET /devices, got %d", listRec.Code)
	}
	var listBody struct {
		Code    string           `json:"code"`
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode GET /devices: %v", err)
	}
	if listBody.Code != "SUCCESS" || len(listBody.Results) != 1 || listBody.Results[0]["device_id"] != "sales-bot" {
		t.Fatalf("unexpected GET /devices results: %+v", listBody)
	}

	// PATCH /devices/:id/webhook updates webhook settings.
	patchReq := httptest.NewRequest(http.MethodPatch, "/devices/sales-bot/webhook", strings.NewReader(`{"webhook_url":"https://hook.local/v2","webhook_secret":"topsecret"}`))
	patchReq.Header.Set("Authorization", authHeader)
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	app.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on PATCH /devices/:id/webhook, got %d: %s", patchRec.Code, patchRec.Body.String())
	}

	// DELETE /devices/:id removes it cleanly.
	delReq := httptest.NewRequest(http.MethodDelete, "/devices/sales-bot", nil)
	delReq.Header.Set("Authorization", authHeader)
	delRec := httptest.NewRecorder()
	app.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on DELETE /devices/:id, got %d", delRec.Code)
	}
}

func TestQRPNGGenerationAndStaticServing(t *testing.T) {
	mgr, app, _ := openTestManager(t, Options{})

	if _, err := mgr.EnsureDevice(context.Background(), "desk-1", "Front Desk"); err != nil {
		t.Fatalf("ensure device: %v", err)
	}
	sess := mgr.getOrCreateSession("desk-1")
	if err := mgr.applySessionQR(sess, "2@whatsmeow-qr-token-verification-payload"); err != nil {
		t.Fatalf("apply session QR: %v", err)
	}

	// Verify /statics/qr/desk-1.png returns valid PNG header magic bytes.
	qrReq := httptest.NewRequest(http.MethodGet, "/statics/qr/desk-1.png", nil)
	qrRec := httptest.NewRecorder()
	app.ServeHTTP(qrRec, qrReq)
	if qrRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for QR PNG, got %d: %s", qrRec.Code, qrRec.Body.String())
	}
	if contentType := qrRec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "image/png") {
		t.Fatalf("expected image/png Content-Type, got %q", contentType)
	}
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if qrRec.Body.Len() < len(pngMagic) || !bytes.Equal(qrRec.Body.Bytes()[:8], pngMagic) {
		t.Fatalf("response is not a valid PNG binary (len=%d)", qrRec.Body.Len())
	}

	// Verify /devices/desk-1/status exposes qr, qr_link, and qr_data_url.
	statusReq := httptest.NewRequest(http.MethodGet, "/devices/desk-1/status", nil)
	statusRec := httptest.NewRecorder()
	app.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for device status, got %d", statusRec.Code)
	}
	var statusBody struct {
		Code    string         `json:"code"`
		Results map[string]any `json:"results"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusBody); err != nil {
		t.Fatalf("decode device status: %v", err)
	}
	if statusBody.Results["qr_link"] != "/statics/qr/desk-1.png" {
		t.Fatalf("expected qr_link=/statics/qr/desk-1.png, got %v", statusBody.Results["qr_link"])
	}
	qrDataURL, _ := statusBody.Results["qr_data_url"].(string)
	if !strings.HasPrefix(qrDataURL, "data:image/png;base64,") {
		t.Fatalf("expected base64 PNG data URL, got %q", qrDataURL)
	}
}

func TestIncomingMessagePersistenceWebhookSignatureAndAutoReply(t *testing.T) {
	const signingSecret = "whsec_verify_hmac_256"

	var webhookMu sync.Mutex
	var capturedBody []byte
	var capturedSignature string
	var capturedDeviceHeader string
	webhookDone := make(chan struct{}, 1)

	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		webhookMu.Lock()
		capturedBody = bodyBytes
		capturedSignature = r.Header.Get("X-Hub-Signature-256")
		capturedDeviceHeader = r.Header.Get("X-Device-Id")
		webhookMu.Unlock()
		w.WriteHeader(http.StatusOK)
		select {
		case webhookDone <- struct{}{}:
		default:
		}
	}))
	defer webhookServer.Close()

	var autoReplyMu sync.Mutex
	var repliedAgent, repliedSender, repliedPrompt string
	autoReplyDone := make(chan struct{}, 1)

	mgr, app, _ := openTestManager(t, Options{
		AutoReply: func(ctx context.Context, agentID, sender, text string) (string, error) {
			autoReplyMu.Lock()
			repliedAgent = agentID
			repliedSender = sender
			repliedPrompt = text
			autoReplyMu.Unlock()
			select {
			case autoReplyDone <- struct{}{}:
			default:
			}
			return "Acknowledged: " + text, nil
		},
	})

	ctx := context.Background()
	if _, err := mgr.EnsureDevice(ctx, "support", "Support Line"); err != nil {
		t.Fatalf("ensure device: %v", err)
	}
	enableReply := true
	webhookURL := webhookServer.URL
	secretCopy := signingSecret
	agentCopy := "support-agent"
	if _, err := mgr.UpdateDeviceConfig(ctx, "support", DeviceConfigPatch{
		WebhookURL:    &webhookURL,
		WebhookSecret: &secretCopy,
		AIAutoReply:   &enableReply,
		AIAgentID:     &agentCopy,
	}); err != nil {
		t.Fatalf("update device config: %v", err)
	}

	chatJID := types.NewJID("6281234567890", types.DefaultUserServer)
	incomingEvt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chatJID,
				Sender:   chatJID,
				IsFromMe: false,
			},
			ID:        types.MessageID("MSG-IN-001"),
			PushName:  "Budi Santoso",
			Timestamp: time.Date(2026, 11, 26, 12, 30, 0, 0, time.UTC),
		},
		Message: &waE2E.Message{
			Conversation: proto.String("Halo, status order saya bagaimana?"),
		},
	}

	if err := mgr.HandleMessageEvent(ctx, "support", incomingEvt); err != nil {
		t.Fatalf("handle incoming message: %v", err)
	}

	// Wait for async webhook delivery and verify HMAC-SHA256 signature.
	select {
	case <-webhookDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for webhook delivery")
	}

	webhookMu.Lock()
	expectedMAC := hmac.New(sha256.New, []byte(signingSecret))
	expectedMAC.Write(capturedBody)
	expectedSig := "sha256=" + hex.EncodeToString(expectedMAC.Sum(nil))
	if capturedSignature != expectedSig {
		webhookMu.Unlock()
		t.Fatalf("HMAC mismatch: got %q, want %q", capturedSignature, expectedSig)
	}
	if capturedDeviceHeader != "support" {
		webhookMu.Unlock()
		t.Fatalf("expected X-Device-Id=support, got %q", capturedDeviceHeader)
	}
	webhookMu.Unlock()

	// Wait for AutoReply hook trigger.
	select {
	case <-autoReplyDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for AutoReply invocation")
	}
	mgr.WaitBackground()

	autoReplyMu.Lock()
	if repliedAgent != "support-agent" || repliedSender != chatJID.String() || repliedPrompt != "Halo, status order saya bagaimana?" {
		autoReplyMu.Unlock()
		t.Fatalf("unexpected auto-reply arguments: agent=%q sender=%q text=%q", repliedAgent, repliedSender, repliedPrompt)
	}
	autoReplyMu.Unlock()

	// Verify GOWA /chats and /chat/:jid/messages endpoints.
	chatsReq := httptest.NewRequest(http.MethodGet, "/chats", nil)
	chatsReq.Header.Set("X-Device-Id", "support")
	chatsRec := httptest.NewRecorder()
	app.ServeHTTP(chatsRec, chatsReq)
	if chatsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET /chats, got %d: %s", chatsRec.Code, chatsRec.Body.String())
	}
	var gowaChats struct {
		Code    string `json:"code"`
		Results struct {
			Entries []ChatRecord `json:"data"`
		} `json:"results"`
	}
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &gowaChats); err != nil {
		t.Fatalf("decode GET /chats: %v", err)
	}
	if gowaChats.Code != "SUCCESS" || len(gowaChats.Results.Entries) != 1 {
		t.Fatalf("unexpected GOWA /chats payload: %+v", gowaChats)
	}
	if gowaChats.Results.Entries[0].Name != "Budi Santoso" || gowaChats.Results.Entries[0].UnreadCount != 1 {
		t.Fatalf("unexpected persisted chat: %+v", gowaChats.Results.Entries[0])
	}

	msgsReq := httptest.NewRequest(http.MethodGet, "/chat/"+chatJID.String()+"/messages?device_id=support", nil)
	msgsRec := httptest.NewRecorder()
	app.ServeHTTP(msgsRec, msgsReq)
	if msgsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on GET /chat/:jid/messages, got %d", msgsRec.Code)
	}
	var gowaMsgs struct {
		Code    string `json:"code"`
		Results struct {
			Entries []MessageRecord `json:"data"`
		} `json:"results"`
	}
	if err := json.Unmarshal(msgsRec.Body.Bytes(), &gowaMsgs); err != nil {
		t.Fatalf("decode GET /chat/:jid/messages: %v", err)
	}
	if gowaMsgs.Code != "SUCCESS" || len(gowaMsgs.Results.Entries) != 1 || gowaMsgs.Results.Entries[0].ID != "MSG-IN-001" {
		t.Fatalf("unexpected GOWA messages payload: %+v", gowaMsgs)
	}

	// Verify /api/whatsapp/* React UI management endpoints.
	uiStatsReq := httptest.NewRequest(http.MethodGet, "/api/whatsapp/stats", nil)
	uiStatsRec := httptest.NewRecorder()
	app.ServeHTTP(uiStatsRec, uiStatsReq)
	if uiStatsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /api/whatsapp/stats, got %d", uiStatsRec.Code)
	}
	var uiStats Stats
	if err := json.Unmarshal(uiStatsRec.Body.Bytes(), &uiStats); err != nil {
		t.Fatalf("decode /api/whatsapp/stats: %v", err)
	}
	if uiStats.DevicesCount != 1 || uiStats.ChatsCount != 1 || uiStats.MessagesCount != 1 || uiStats.Engine != "whatsmeow" {
		t.Fatalf("unexpected /api/whatsapp/stats: %+v", uiStats)
	}

	uiChatsReq := httptest.NewRequest(http.MethodGet, "/api/whatsapp/chats?deviceId=support&q=Budi", nil)
	uiChatsRec := httptest.NewRecorder()
	app.ServeHTTP(uiChatsRec, uiChatsReq)
	if uiChatsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /api/whatsapp/chats, got %d", uiChatsRec.Code)
	}
	var uiChatsBody struct {
		Chats []uiChatRecord `json:"chats"`
		Count int            `json:"count"`
	}
	if err := json.Unmarshal(uiChatsRec.Body.Bytes(), &uiChatsBody); err != nil {
		t.Fatalf("decode /api/whatsapp/chats: %v", err)
	}
	if uiChatsBody.Count != 1 || uiChatsBody.Chats[0].JID != chatJID.String() || uiChatsBody.Chats[0].UnreadCount != 1 {
		t.Fatalf("unexpected /api/whatsapp/chats payload: %+v", uiChatsBody)
	}

	uiMsgsReq := httptest.NewRequest(http.MethodGet, "/api/whatsapp/chats/"+chatJID.String()+"/messages?deviceId=support",
		nil)
	uiMsgsRec := httptest.NewRecorder()
	app.ServeHTTP(uiMsgsRec, uiMsgsReq)
	if uiMsgsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /api/whatsapp/chats/:jid/messages, got %d", uiMsgsRec.Code)
	}
	var uiMsgsBody struct {
		Messages []uiMessageRecord `json:"messages"`
		Count    int               `json:"count"`
	}
	if err := json.Unmarshal(uiMsgsRec.Body.Bytes(), &uiMsgsBody); err != nil {
		t.Fatalf("decode /api/whatsapp/chats/:jid/messages: %v", err)
	}
	if uiMsgsBody.Count != 1 || uiMsgsBody.Messages[0].ChatJID != chatJID.String() || uiMsgsBody.Messages[0].Body != "Halo, status order saya bagaimana?" {
		t.Fatalf("unexpected /api/whatsapp/chats/:jid/messages payload: %+v", uiMsgsBody)
	}

	// Verify PATCH /api/whatsapp/devices/:id/config and GET /api/whatsapp/devices.
	cfgReq := httptest.NewRequest(http.MethodPatch, "/api/whatsapp/devices/support/config", strings.NewReader(`{"label":"Support VIP","aiAutoReply":false,"archiveToPool":true}`))
	cfgReq.Header.Set("Content-Type", "application/json")
	cfgRec := httptest.NewRecorder()
	app.ServeHTTP(cfgRec, cfgReq)
	if cfgRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on PATCH /api/whatsapp/devices/:id/config, got %d: %s", cfgRec.Code, cfgRec.Body.String())
	}
	var updatedDev DeviceRecord
	if err := json.Unmarshal(cfgRec.Body.Bytes(), &updatedDev); err != nil {
		t.Fatalf("decode updated device: %v", err)
	}
	if updatedDev.Label != "Support VIP" || updatedDev.AIAutoReply || !updatedDev.ArchiveToPool ||
		!updatedDev.HasWebhookSecret {
		t.Fatalf("unexpected updated device config: %+v", updatedDev)
	}
}
