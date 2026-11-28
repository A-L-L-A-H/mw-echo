package whatsapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestEnvelopeSerializationAndValidation(t *testing.T) {
	textEnv := SendMessageEnvelope{
		DeviceID: "ops-1",
		Phone:    "628123456789",
		Message:  "Hello from GOWA",
	}
	if err := textEnv.Validate(); err != nil {
		t.Fatalf("expected valid text envelope, got %v", err)
	}
	if textEnv.Type != "text" {
		t.Fatalf("expected normalized type=text, got %q", textEnv.Type)
	}

	interactiveEnv := SendMessageEnvelope{
		Phone: "628123456789",
		Interactive: &InteractiveEnvelope{
			Header: "Order #42",
			Body:   "Confirm dispatch?",
			Footer: "mw-echo",
			Buttons: []InteractiveButton{
				{ID: "approve", Title: "Approve"},
				{ID: "hold", Title: "Hold"},
			},
		},
	}
	if err := interactiveEnv.Validate(); err != nil {
		t.Fatalf("expected valid interactive envelope, got %v", err)
	}
	if interactiveEnv.Type != "interactive" {
		t.Fatalf("expected normalized type=interactive, got %q", interactiveEnv.Type)
	}
	rendered := RenderInteractiveText(interactiveEnv.Interactive)
	if !strings.Contains(rendered, "*Order #42*") || !strings.Contains(rendered,
		"[1] Approve") || !strings.Contains(rendered, "_mw-echo_") {
		t.Fatalf("unexpected interactive render: %q", rendered)
	}

	rawJSON, err := json.Marshal(interactiveEnv)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var roundTrip SendMessageEnvelope
	if err := json.Unmarshal(rawJSON, &roundTrip); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if len(roundTrip.Interactive.Buttons) != 2 || roundTrip.Interactive.Buttons[0].ID != "approve" {
		t.Fatalf("unexpected interactive roundtrip: %+v", roundTrip.Interactive)
	}

	invalidCases := []SendMessageEnvelope{
		{Message: "missing phone"},
		{Phone: "628111", Type: "text"},
		{Phone: "628111", Type: "image"},
		{Phone: "628111", Type: "interactive"},
		{Phone: "628111", Type: "sticker", Message: "unsupported"},
	}
	for i, tc := range invalidCases {
		if err := tc.Validate(); err == nil {
			t.Fatalf("case %d: expected validation error, got nil", i)
		}
	}
}

func TestWebhookHMACVectorsAndConfigEndpoint(t *testing.T) {
	secret := []byte("top-secret-key")
	payload := []byte(`{"event":"message","device_id":"default"}`)

	sig := SignWebhookPayload(secret, payload)
	// Verified SHA-256 HMAC vector:
	if !strings.HasPrefix(sig, "sha256=") || len(sig) != len("sha256=")+64 {
		t.Fatalf("unexpected signature format: %q", sig)
	}
	if !VerifyWebhookSignature(secret, payload, sig) {
		t.Fatal("expected VerifyWebhookSignature to accept valid signature")
	}
	if VerifyWebhookSignature(secret, []byte(`{"event":"tampered"}`), sig) {
		t.Fatal("expected VerifyWebhookSignature to reject tampered payload")
	}
	if VerifyWebhookSignature([]byte("wrong-secret"), payload, sig) {
		t.Fatal("expected VerifyWebhookSignature to reject wrong secret")
	}
	if VerifyWebhookSignature(nil, payload, sig) {
		t.Fatal("expected VerifyWebhookSignature to reject empty secret")
	}
	if VerifyWebhookSignature(secret, payload, strings.TrimPrefix(sig, "sha256=")) {
		t.Fatal("expected VerifyWebhookSignature to require sha256= prefix")
	}

	// Deterministic known RFC 4231 / standard HMAC-SHA256 vector check.
	knownSig := SignWebhookPayload([]byte("key"), []byte("The quick brown fox jumps over the lazy dog"))
	const wantKnown = "sha256=f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	if knownSig != wantKnown {
		t.Fatalf("HMAC vector mismatch: got %q, want %q", knownSig, wantKnown)
	}

	_, app, _ := openTestManager(t, Options{})
	cfgBody := `{"device_id":"alerts","webhook_url":"https://hooks.local/wa","webhook_secret":"whsec_123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/config", strings.NewReader(cfgBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/webhooks/config failed: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Code    string         `json:"code"`
		Results map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode webhook config response: %v", err)
	}
	if env.Code != "SUCCESS" || env.Results["webhook_url"] != "https://hooks.local/wa" ||
		env.Results["has_webhook_secret"] != true {
		t.Fatalf("unexpected webhook config response: %+v", env)
	}
}

func TestV1AuthQRStateMachinePairValidationAndLogout(t *testing.T) {
	mgr, app, _ := openTestManager(t, Options{})
	ctx := context.Background()

	if _, err := mgr.EnsureDevice(ctx, "auth-dev", "Auth Device"); err != nil {
		t.Fatalf("ensure device: %v", err)
	}

	// Simulate whatsmeow QR channel transitions without a live socket.
	sess := mgr.getOrCreateSession("auth-dev")
	qrChan := make(chan whatsmeow.QRChannelItem, 3)
	firstReady := make(chan struct{}, 1)
	qrChan <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "2@simulated-qr-token"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.consumeQRChannel("auth-dev", sess, qrChan, firstReady)
	}()

	select {
	case <-firstReady:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for QR code ready signal")
	}

	// GET /api/v1/auth/qr returns valid PNG image bytes.
	qrReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/qr?device_id=auth-dev", nil)
	qrRec := httptest.NewRecorder()
	app.ServeHTTP(qrRec, qrReq)
	if qrRec.Code != http.StatusOK || !strings.HasPrefix(qrRec.Header().Get("Content-Type"), "image/png") {
		t.Fatalf("expected PNG from /api/v1/auth/qr, got %d (%s)", qrRec.Code, qrRec.Header().Get("Content-Type"))
	}
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if qrRec.Body.Len() < 8 || !bytes.Equal(qrRec.Body.Bytes()[:8], pngMagic) {
		t.Fatalf("invalid PNG header from /api/v1/auth/qr")
	}

	// GET /api/v1/auth/qr?format=json returns JSON envelope with QR data URL.
	qrJSONReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/qr?device_id=auth-dev&format=json", nil)
	qrJSONRec := httptest.NewRecorder()
	app.ServeHTTP(qrJSONRec, qrJSONReq)
	if qrJSONRec.Code != http.StatusOK || !strings.Contains(qrJSONRec.Body.String(), "data:image/png;base64,") {
		t.Fatalf("expected JSON QR payload, got %d %s", qrJSONRec.Code, qrJSONRec.Body.String())
	}

	// Transition QR channel to success and verify state clears QR & becomes connected.
	qrChan <- whatsmeow.QRChannelSuccess
	close(qrChan)
	<-done

	status, err := mgr.GetDeviceStatus(ctx, "auth-dev")
	if err != nil {
		t.Fatalf("get device status: %v", err)
	}
	if status.Status != "connected" || !status.Connected || !status.LoggedIn || status.QR != "" {
		t.Fatalf("unexpected post-QR success state: %+v", status)
	}

	// POST /api/v1/auth/pair validates required phone parameter.
	pairMissingReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pair", strings.NewReader(`{"device_id":"auth-dev"}`))
	pairMissingReq.Header.Set("Content-Type", "application/json")
	pairMissingRec := httptest.NewRecorder()
	app.ServeHTTP(pairMissingRec, pairMissingReq)
	if pairMissingRec.Code != http.StatusBadRequest || !strings.Contains(pairMissingRec.Body.String(), `"ERROR"`) {
		t.Fatalf("expected 400 ERROR for missing phone, got %d %s", pairMissingRec.Code, pairMissingRec.Body.String())
	}

	// POST /api/v1/auth/logout clears session and sets logged_out.
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader(`{"device_id":"auth-dev"}`))
	logoutReq.Header.Set("Content-Type", "application/json")
	logoutRec := httptest.NewRecorder()
	app.ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusOK || !strings.Contains(logoutRec.Body.String(), `"logged_out"`) {
		t.Fatalf("expected 200 logged_out, got %d %s", logoutRec.Code, logoutRec.Body.String())
	}
	postLogout, err := mgr.GetDeviceStatus(ctx, "auth-dev")
	if err != nil {
		t.Fatalf("status after logout: %v", err)
	}
	if postLogout.Status != "logged_out" || postLogout.Connected || postLogout.LoggedIn {
		t.Fatalf("expected logged_out disconnected state, got %+v", postLogout)
	}
}

func TestV1NineDriveZeroDiskMediaStreamUploadAndDownload(t *testing.T) {
	var mu sync.Mutex
	storedObjects := make(map[string]struct {
		filename string
		mimeType string
		data     []byte
	})

	_, app, _ := openTestManager(t, Options{
		MediaUploadStream: func(ctx context.Context, project, logicalPath, filename, mimeType string,
			reader io.Reader) (string, error) {
			raw, err := io.ReadAll(reader)
			if err != nil {
				return "", err
			}
			objID := "9drive_obj_001"
			mu.Lock()
			storedObjects[objID] = struct {
				filename string
				mimeType string
				data     []byte
			}{filename: filename, mimeType: mimeType, data: raw}
			mu.Unlock()
			return objID, nil
		},
		MediaDownloadStream: func(ctx context.Context, objectID string) (string, string, io.ReadCloser, error) {
			mu.Lock()
			obj, ok := storedObjects[objectID]
			mu.Unlock()
			if !ok {
				return "", "", nil, io.EOF
			}
			return obj.filename, obj.mimeType, io.NopCloser(bytes.NewReader(obj.data)), nil
		},
	})

	payload := []byte("zero-disk-9drive-media-stream-content")
	sum := sha256.Sum256(payload)
	wantSHA := hex.EncodeToString(sum[:])

	var formBuf bytes.Buffer
	mw := multipart.NewWriter(&formBuf)
	_ = mw.WriteField("device_id", "media-dev")
	part, err := mw.CreateFormFile("file", "invoice.pdf")
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	_, _ = part.Write(payload)
	_ = mw.Close()

	upReq := httptest.NewRequest(http.MethodPost, "/api/v1/media/upload", &formBuf)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upRec := httptest.NewRecorder()
	app.ServeHTTP(upRec, upReq)
	if upRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/media/upload failed: %d %s", upRec.Code, upRec.Body.String())
	}

	var upEnv struct {
		Code    string        `json:"code"`
		Results MediaMetadata `json:"results"`
	}
	if err := json.Unmarshal(upRec.Body.Bytes(), &upEnv); err != nil {
		t.Fatalf("decode media upload: %v", err)
	}
	if upEnv.Code != "SUCCESS" || upEnv.Results.SHA256 != wantSHA || upEnv.Results.SizeBytes != int64(len(payload)) || upEnv.Results.StorageObjectID != "9drive_obj_001" {
		t.Fatalf("unexpected media metadata: %+v", upEnv)
	}

	// Stream download directly back from 9Drive adapter.
	dlReq := httptest.NewRequest(http.MethodGet, "/api/v1/media/download/"+upEnv.Results.MediaID, nil)
	dlRec := httptest.NewRecorder()
	app.ServeHTTP(dlRec, dlReq)
	if dlRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/media/download failed: %d %s", dlRec.Code, dlRec.Body.String())
	}
	if !bytes.Equal(dlRec.Body.Bytes(), payload) {
		t.Fatalf("downloaded bytes mismatch: got %q, want %q", dlRec.Body.String(), string(payload))
	}
	if dlRec.Header().Get("X-Content-SHA256") != wantSHA {
		t.Fatalf("expected X-Content-SHA256=%s, got %q", wantSHA, dlRec.Header().Get("X-Content-SHA256"))
	}
}

func TestV1SystemHealthMetricsInboxAssetsAndAIGatewayAdapter(t *testing.T) {
	var capturedAgentHeader string
	var capturedPrompt string
	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		capturedAgentHeader = r.Header.Get("X-MW-Agent")
		body, _ := io.ReadAll(r.Body)
		capturedPrompt = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Order #42 is in transit."}}]}`))
	}))
	defer aiServer.Close()

	mgr, app, _ := openTestManager(t, Options{
		AIGatewayURL: aiServer.URL,
	})
	ctx := context.Background()
	if _, err := mgr.EnsureDevice(ctx, "cx-1", "CX Line"); err != nil {
		t.Fatalf("ensure device: %v", err)
	}
	enableAI := true
	agentID := "cx-gemini"
	if _, err := mgr.UpdateDeviceConfig(ctx, "cx-1", DeviceConfigPatch{
		AIAutoReply: &enableAI,
		AIAgentID:   &agentID,
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// Inject 3 incoming messages to exercise inbox pagination & AI Gateway adapter.
	chatJID := types.NewJID("628555000111", types.DefaultUserServer)
	for _, msg := range []struct {
		id   string
		body string
		ts   time.Time
	}{
		{id: "M1", body: "First ping", ts: time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)},
		{id: "M2", body: "Second ping", ts: time.Date(2026, 11, 27, 10, 1, 0, 0, time.UTC)},
		{id: "M3", body: "Where is order #42?", ts: time.Date(2026, 11, 27, 10, 2, 0, 0, time.UTC)},
	} {
		err := mgr.HandleMessageEvent(ctx, "cx-1", &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chatJID, Sender: chatJID},
				ID:            types.MessageID(msg.id),
				PushName:      "Siti",
				Timestamp:     msg.ts,
			},
			Message: &waE2E.Message{Conversation: proto.String(msg.body)},
		})
		if err != nil {
			t.Fatalf("handle message %s: %v", msg.id, err)
		}
	}
	mgr.WaitBackground()

	if capturedAgentHeader != "cx-gemini" || !strings.Contains(capturedPrompt, "Where is order #42?") {
		t.Fatalf("AI gateway adapter did not forward expected agent/prompt: agent=%q prompt=%q", capturedAgentHeader,
			capturedPrompt)
	}

	// Verify /api/v1/system/health.
	healthRec := httptest.NewRecorder()
	app.ServeHTTP(healthRec, httptest.NewRequest(http.MethodGet, "/api/v1/system/health", nil))
	if healthRec.Code != http.StatusOK || !strings.Contains(healthRec.Body.String(), `"whatsmeow"`) {
		t.Fatalf("GET /api/v1/system/health failed: %d %s", healthRec.Code, healthRec.Body.String())
	}

	// Verify /api/v1/system/metrics Prometheus output.
	metricsRec := httptest.NewRecorder()
	app.ServeHTTP(metricsRec, httptest.NewRequest(http.MethodGet, "/api/v1/system/metrics", nil))
	if metricsRec.Code != http.StatusOK || !strings.Contains(metricsRec.Body.String(), "gowa_messages_total 3") {
		t.Fatalf("GET /api/v1/system/metrics failed: %d %s", metricsRec.Code, metricsRec.Body.String())
	}

	// Verify /api/v1/inbox pagination.
	inboxRec := httptest.NewRecorder()
	app.ServeHTTP(inboxRec, httptest.NewRequest(http.MethodGet, "/api/v1/inbox?device_id=cx-1&limit=2&offset=0", nil))
	if inboxRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/inbox failed: %d %s", inboxRec.Code, inboxRec.Body.String())
	}
	var inboxEnv struct {
		Code    string `json:"code"`
		Results struct {
			Messages []MessageRecord `json:"messages"`
			Total    int             `json:"total"`
			HasMore  bool            `json:"has_more"`
		} `json:"results"`
	}
	if err := json.Unmarshal(inboxRec.Body.Bytes(), &inboxEnv); err != nil {
		t.Fatalf("decode /api/v1/inbox: %v", err)
	}
	if inboxEnv.Results.Total != 3 || len(inboxEnv.Results.Messages) != 2 || !inboxEnv.Results.HasMore || inboxEnv.Results.Messages[0].ID != "M3" {
		t.Fatalf("unexpected inbox pagination: %+v", inboxEnv.Results)
	}

	// Verify embedded static assets and traversal protection.
	assetRec := httptest.NewRecorder()
	app.ServeHTTP(assetRec, httptest.NewRequest(http.MethodGet, "/api/v1/assets/whatsapp.js", nil))
	if assetRec.Code != http.StatusOK || !strings.Contains(assetRec.Body.String(), "/api/v1/system/health") {
		t.Fatalf("GET /api/v1/assets/whatsapp.js failed: %d %s", assetRec.Code, assetRec.Body.String())
	}
	badAssetRec := httptest.NewRecorder()
	app.ServeHTTP(badAssetRec, httptest.NewRequest(http.MethodGet, "/api/v1/assets/..%2Fmanager.go", nil))
	if badAssetRec.Code != http.StatusBadRequest && badAssetRec.Code != http.StatusNotFound {
		t.Fatalf("expected 400/404 for traversal asset path, got %d", badAssetRec.Code)
	}
}

func TestMCPOfflineStdioAndContractStubs(t *testing.T) {
	mgr, _, _ := openTestManager(t, Options{})
	ctx := context.Background()
	if _, err := mgr.EnsureDevice(ctx, "mcp-dev", "MCP Device"); err != nil {
		t.Fatalf("ensure device: %v", err)
	}

	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo_wa_devices","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo_wa_send_message","arguments":{"deviceId":"mcp-dev","phone":"628123456","message":"offline ping"}}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := mgr.ServeMCPStdio(ctx, strings.NewReader(requests), &out); err != nil {
		t.Fatalf("ServeMCPStdio failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 JSON-RPC responses, got %d: %s", len(lines), out.String())
	}

	var listResp mcpRPCResponse
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil || listResp.Error != nil {
		t.Fatalf("tools/list failed: %s", lines[1])
	}
	if !strings.Contains(lines[1], "echo_wa_devices") || !strings.Contains(lines[1], "echo_wa_send_message") {
		t.Fatalf("tools/list missing expected tools: %s", lines[1])
	}

	var devResp mcpRPCResponse
	if err := json.Unmarshal([]byte(lines[2]), &devResp); err != nil || devResp.Error != nil {
		t.Fatalf("echo_wa_devices failed: %s", lines[2])
	}
	if !strings.Contains(lines[2], "mcp-dev") {
		t.Fatalf("echo_wa_devices missing created device: %s", lines[2])
	}

	// Without a live WhatsApp daemon, echo_wa_send_message must return a clean JSON-RPC error instead of panicking.
	var sendResp mcpRPCResponse
	if err := json.Unmarshal([]byte(lines[3]), &sendResp); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	if sendResp.Error == nil || !strings.Contains(sendResp.Error.Message, "not connected") {
		t.Fatalf("expected clean offline error from echo_wa_send_message, got: %s", lines[3])
	}
}
