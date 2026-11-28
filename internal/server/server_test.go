package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/server"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func setupTestEcho(t *testing.T, token string) (*store.HubStore, http.Handler) {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	return hub, server.NewEcho(hub, server.Config{AuthToken: token})
}

func TestDocsFTS5TasksStorageAndMCP(t *testing.T) {
	_, handler := setupTestEcho(t, "")

	// 1. Upsert & search Markdown doc via FTS5
	putReq := httptest.NewRequest(http.MethodPut, "/api/docs", strings.NewReader(`{"project":"mw","relPath":"guide/coolify.md","content":"# Self Update Guide\n\nMoonWitness supports Coolify-style one-click GHCR updates."}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	handler.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/docs expected 200, got %d: %s", putRec.Code, putRec.Body.String())
	}

	searchRec := httptest.NewRecorder()
	handler.ServeHTTP(searchRec, httptest.NewRequest(http.MethodGet, "/api/docs?q=Coolify", nil))
	if searchRec.Code != http.StatusOK || !strings.Contains(searchRec.Body.String(), "guide/coolify.md") {
		t.Fatalf("FTS5 doc search failed: %d %s", searchRec.Code, searchRec.Body.String())
	}

	// 2. Create, patch, and export TaskOS v2 task
	createTaskReq := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"code":"ECHO-001","project":"mw","title":"Unify docs hub","priority":"p0","status":"todo"}`))
	createTaskReq.Header.Set("Content-Type", "application/json")
	createTaskRec := httptest.NewRecorder()
	handler.ServeHTTP(createTaskRec, createTaskReq)
	if createTaskRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/tasks expected 201, got %d", createTaskRec.Code)
	}

	patchReq := httptest.NewRequest(http.MethodPatch, "/api/tasks/ECHO-001", strings.NewReader(`{"status":"done"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	handler.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK || !strings.Contains(patchRec.Body.String(), `"status":"done"`) {
		t.Fatalf("PATCH task failed: %d %s", patchRec.Code, patchRec.Body.String())
	}

	exportRec := httptest.NewRecorder()
	handler.ServeHTTP(exportRec, httptest.NewRequest(http.MethodGet, "/api/export/tasks.json", nil))
	if exportRec.Code != http.StatusOK || !strings.Contains(exportRec.Body.String(), `"moonwitness-tasks/v2"`) {
		t.Fatalf("export tasks.json failed: %d %s", exportRec.Code, exportRec.Body.String())
	}

	// 3. Upload, deduplicate, and download storage blob
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("project", "mw")
	_ = writer.WriteField("logicalPath", "design/poster.txt")
	part, _ := writer.CreateFormFile("file", "poster.txt")
	_, _ = io.WriteString(part, "moonwitness-binary-payload-v1")
	_ = writer.Close()

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/storage/upload", &body)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploadRec := httptest.NewRecorder()
	handler.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusCreated {
		t.Fatalf("upload failed: %d %s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploaded store.StorageObject
	_ = json.Unmarshal(uploadRec.Body.Bytes(), &uploaded)

	dlRec := httptest.NewRecorder()
	handler.ServeHTTP(dlRec, httptest.NewRequest(http.MethodGet, "/api/storage/"+uploaded.ID+"/download", nil))
	if dlRec.Code != http.StatusOK || dlRec.Body.String() != "moonwitness-binary-payload-v1" {
		t.Fatalf("download mismatch: %d %q", dlRec.Code, dlRec.Body.String())
	}

	// 4. MCP JSON-RPC tool search
	mcpReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo_search_docs","arguments":{"query":"GHCR"}}}`))
	mcpReq.Header.Set("Content-Type", "application/json")
	mcpRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpRec, mcpReq)
	if mcpRec.Code != http.StatusOK || !strings.Contains(mcpRec.Body.String(), "guide/coolify.md") {
		t.Fatalf("MCP search failed: %d %s", mcpRec.Code, mcpRec.Body.String())
	}
}

func TestRepoImportAndAuthGuard(t *testing.T) {
	_, handler := setupTestEcho(t, "secret-token-xyz")

	// Unauthenticated request rejected
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", unauthRec.Code)
	}

	// Prepare tiny sample repo
	fakeRepo := t.TempDir()
	_ = os.MkdirAll(filepath.Join(fakeRepo, "docs", "tasks"), 0o755)
	_ = os.WriteFile(filepath.Join(fakeRepo, "docs", "intro.md"), []byte("# Intro\nHello Echo Hub"), 0o644)
	_ = os.WriteFile(filepath.Join(fakeRepo, "docs", "tasks", "tasks.json"), []byte(`{"tasks":[{"code":"T-1","project":"demo","title":"Ship Echo","status":"todo","priority":"p1"}]}`), 0o644)

	payload, _ := json.Marshal(map[string]any{"repoDir": fakeRepo, "project": "demo", "includeAssets": false})
	req := httptest.NewRequest(http.MethodPost, "/api/import/repo", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token-xyz")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"docsImported":1`) || !strings.Contains(rec.Body.String(), `"tasksImported":1`) {
		t.Fatalf("import failed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestNineDrivePoolGDriveS3AndRoutingPolicies(t *testing.T) {
	hub, handler := setupTestEcho(t, "")

	// Mock Google Drive REST v3 + OAuth2 token endpoint
	driveStore := map[string][]byte{}
	fakeDrive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-access-token","expires_in":3600}`))
		case r.URL.Path == "/drive/v3/about":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"storageQuota":{"limit":"16106127360","usage":"1073741824"},"user":{"emailAddress":"pool1@gmail.com"}}`))
		case r.URL.Path == "/drive/v3/files" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"files":[{"id":"folder-root-1","name":"mw-echo"}]}`))
		case r.URL.Path == "/upload/drive/v3/files" && r.Method == http.MethodPost:
			// Parse multipart related body and extract second part (media bytes)
			_, params, _ := strings.Cut(r.Header.Get("Content-Type"), "boundary=")
			mr := multipart.NewReader(r.Body, strings.TrimSpace(params))
			_, _ = mr.NextPart() // metadata JSON
			mediaPart, err := mr.NextPart()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			raw, _ := io.ReadAll(mediaPart)
			driveStore["gd-file-001"] = raw
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"gd-file-001"}`))
		case strings.HasPrefix(r.URL.Path, "/drive/v3/files/") && r.Method == http.MethodGet:
			id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
			raw, ok := driveStore[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(raw)
		case strings.HasPrefix(r.URL.Path, "/drive/v3/files/") && r.Method == http.MethodDelete:
			id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
			delete(driveStore, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeDrive.Close()

	// Mock S3 endpoint verifying AWS4-HMAC-SHA256 signature header
	s3Store := map[string][]byte{}
	fakeS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKTEST/") {
			http.Error(w, "missing sigv4", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			s3Store[r.URL.Path] = raw
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			raw, ok := s3Store[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(raw)
		case http.MethodDelete:
			delete(s3Store, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer fakeS3.Close()

	// 1. Register Google Drive provider (15 GiB quota -> largest free space!)
	gdBody, _ := json.Marshal(map[string]any{
		"name":       "gdrive-main",
		"kind":       "gdrive",
		"quotaBytes": int64(15 * 1024 * 1024 * 1024),
		"priority":   20,
		"gdrive": map[string]any{
			"clientId":     "cid",
			"clientSecret": "csecret-never-leak",
			"refreshToken": "rtoken-never-leak",
			"tokenUrl":     fakeDrive.URL + "/token",
			"apiBase":      fakeDrive.URL + "/drive/v3",
			"uploadBase":   fakeDrive.URL + "/upload/drive/v3",
		},
	})
	gdReq := httptest.NewRequest(http.MethodPost, "/api/providers", bytes.NewReader(gdBody))
	gdReq.Header.Set("Content-Type", "application/json")
	gdRec := httptest.NewRecorder()
	handler.ServeHTTP(gdRec, gdReq)
	if gdRec.Code != http.StatusCreated {
		t.Fatalf("add gdrive failed: %d %s", gdRec.Code, gdRec.Body.String())
	}
	var gdProv struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(gdRec.Body.Bytes(), &gdProv)

	// 2. Register S3 provider (5 GiB quota, higher priority=5)
	s3Body, _ := json.Marshal(map[string]any{
		"name":       "s3-r2",
		"kind":       "s3",
		"quotaBytes": int64(5 * 1024 * 1024 * 1024),
		"priority":   5,
		"s3": map[string]any{
			"endpoint":       fakeS3.URL,
			"bucket":         "mw-bucket",
			"region":         "auto",
			"accessKey":      "AKTEST",
			"secretKey":      "s3-secret-never-leak",
			"forcePathStyle": true,
		},
	})
	s3Req := httptest.NewRequest(http.MethodPost, "/api/providers", bytes.NewReader(s3Body))
	s3Req.Header.Set("Content-Type", "application/json")
	s3Rec := httptest.NewRecorder()
	handler.ServeHTTP(s3Rec, s3Req)
	if s3Rec.Code != http.StatusCreated {
		t.Fatalf("add s3 failed: %d %s", s3Rec.Code, s3Rec.Body.String())
	}

	// 3. Sync Google Drive live quota and verify secrets are never returned in GET /api/providers
	syncRec := httptest.NewRecorder()
	handler.ServeHTTP(syncRec, httptest.NewRequest(http.MethodPost, "/api/providers/"+gdProv.ID+"/sync-quota", nil))
	if syncRec.Code != http.StatusOK || !strings.Contains(syncRec.Body.String(), "pool1@gmail.com") {
		t.Fatalf("sync gdrive quota failed: %d %s", syncRec.Code, syncRec.Body.String())
	}

	listProvRec := httptest.NewRecorder()
	handler.ServeHTTP(listProvRec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if strings.Contains(listProvRec.Body.String(), "never-leak") {
		t.Fatalf("provider secrets leaked in GET /api/providers!")
	}

	// 4. Upload under default 'most_free' -> routes to gdrive-main (14 GiB free > 5 GiB free), zero local disk blobs!
	var upBuf bytes.Buffer
	mw := multipart.NewWriter(&upBuf)
	_ = mw.WriteField("project", "mw")
	_ = mw.WriteField("logicalPath", "media/hero.bin")
	part, _ := mw.CreateFormFile("file", "hero.bin")
	_, _ = io.WriteString(part, "gdrive-streamed-content")
	_ = mw.Close()

	upReq := httptest.NewRequest(http.MethodPost, "/api/storage/upload", &upBuf)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upRec := httptest.NewRecorder()
	handler.ServeHTTP(upRec, upReq)
	if upRec.Code != http.StatusCreated || !strings.Contains(upRec.Body.String(), `"providerKind":"gdrive"`) {
		t.Fatalf("expected upload routed to gdrive, got %d %s", upRec.Code, upRec.Body.String())
	}
	var uploadedGD struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(upRec.Body.Bytes(), &uploadedGD)

	// Confirm ZERO files written to local blobs/ directory!
	localBlobEntries, _ := os.ReadDir(filepath.Join(hub.DataDir(), "blobs"))
	if len(localBlobEntries) != 0 {
		t.Fatalf("expected 0 local disk blobs when routed to Google Drive, found %d", len(localBlobEntries))
	}

	// Download streams back from Google Drive
	dlGD := httptest.NewRecorder()
	handler.ServeHTTP(dlGD, httptest.NewRequest(http.MethodGet, "/api/storage/"+uploadedGD.ID+"/download", nil))
	if dlGD.Code != http.StatusOK || dlGD.Body.String() != "gdrive-streamed-content" {
		t.Fatalf("gdrive download failed: %d %q", dlGD.Code, dlGD.Body.String())
	}

	// 5. Switch routing policy to 'priority' -> routes to s3-r2 (priority=5 < 20)
	polReq := httptest.NewRequest(http.MethodPut, "/api/providers/policy", strings.NewReader(`{"routingPolicy":"priority"}`))
	polReq.Header.Set("Content-Type", "application/json")
	polRec := httptest.NewRecorder()
	handler.ServeHTTP(polRec, polReq)
	if polRec.Code != http.StatusOK {
		t.Fatalf("set policy failed: %d", polRec.Code)
	}

	var s3Buf bytes.Buffer
	mw2 := multipart.NewWriter(&s3Buf)
	_ = mw2.WriteField("project", "mw")
	_ = mw2.WriteField("logicalPath", "media/s3-file.bin")
	part2, _ := mw2.CreateFormFile("file", "s3-file.bin")
	_, _ = io.WriteString(part2, "s3-sigv4-content")
	_ = mw2.Close()

	s3UpReq := httptest.NewRequest(http.MethodPost, "/api/storage/upload", &s3Buf)
	s3UpReq.Header.Set("Content-Type", mw2.FormDataContentType())
	s3UpRec := httptest.NewRecorder()
	handler.ServeHTTP(s3UpRec, s3UpReq)
	if s3UpRec.Code != http.StatusCreated || !strings.Contains(s3UpRec.Body.String(), `"providerKind":"s3"`) {
		t.Fatalf("expected upload routed to s3 under priority policy, got %d %s", s3UpRec.Code, s3UpRec.Body.String())
	}
	var uploadedS3 struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(s3UpRec.Body.Bytes(), &uploadedS3)

	dlS3 := httptest.NewRecorder()
	handler.ServeHTTP(dlS3, httptest.NewRequest(http.MethodGet, "/api/storage/"+uploadedS3.ID+"/download", nil))
	if dlS3.Code != http.StatusOK || dlS3.Body.String() != "s3-sigv4-content" {
		t.Fatalf("s3 download failed: %d %q", dlS3.Code, dlS3.Body.String())
	}

	// Delete removes from remote provider cleanly
	delGD := httptest.NewRecorder()
	handler.ServeHTTP(delGD, httptest.NewRequest(http.MethodDelete, "/api/storage/"+uploadedGD.ID, nil))
	if delGD.Code != http.StatusOK || len(driveStore) != 0 {
		t.Fatalf("gdrive delete failed: %d, remaining=%d", delGD.Code, len(driveStore))
	}
}

func TestProjectsAIGatewayAndObservatoryRoutes(t *testing.T) {
	_, handler := setupTestEcho(t, "")

	// 1. Projects CRUD + auto progress rollup
	projReq := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"slug":"echo-core","name":"Echo Core","description":"Unified Windows control hub","defaultAgent":"claude-code"}`))
	projReq.Header.Set("Content-Type", "application/json")
	projRec := httptest.NewRecorder()
	handler.ServeHTTP(projRec, projReq)
	if projRec.Code != http.StatusOK || !strings.Contains(projRec.Body.String(), `"slug":"echo-core"`) {
		t.Fatalf("POST /api/projects failed: %d %s", projRec.Code, projRec.Body.String())
	}

	taskReq := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"code":"EC-1","project":"echo-core","milestone":"M1","title":"Ship UI","priority":"p1","status":"done"}`))
	taskReq.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), taskReq)

	getProjRec := httptest.NewRecorder()
	handler.ServeHTTP(getProjRec, httptest.NewRequest(http.MethodGet, "/api/projects/echo-core", nil))
	if getProjRec.Code != http.StatusOK || !strings.Contains(getProjRec.Body.String(), `"progressPct":100`) {
		t.Fatalf("GET /api/projects/echo-core failed: %d %s", getProjRec.Code, getProjRec.Body.String())
	}

	// 2. AI Gateway routes (/v1/models & /api/ai/agents)
	modelsRec := httptest.NewRecorder()
	handler.ServeHTTP(modelsRec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if modelsRec.Code != http.StatusOK || !strings.Contains(modelsRec.Body.String(), `"object":"list"`) {
		t.Fatalf("GET /v1/models failed: %d %s", modelsRec.Code, modelsRec.Body.String())
	}

	agentsRec := httptest.NewRecorder()
	handler.ServeHTTP(agentsRec, httptest.NewRequest(http.MethodGet, "/api/ai/agents", nil))
	if agentsRec.Code != http.StatusOK || !strings.Contains(agentsRec.Body.String(), "claude-code") {
		t.Fatalf("GET /api/ai/agents failed: %d %s", agentsRec.Code, agentsRec.Body.String())
	}

	// 3. Observatory routes (/v1/traces, /api/observatory/stats, /api/observatory/services)
	traceReq := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"mw-cli"}}]},"scopeSpans":[{"spans":[{"name":"run","startTimeUnixNano":"1000000000","endTimeUnixNano":"1050000000"}]}]}]}`))
	traceReq.Header.Set("Content-Type", "application/json")
	traceRec := httptest.NewRecorder()
	handler.ServeHTTP(traceRec, traceReq)
	if traceRec.Code != http.StatusOK || !strings.Contains(traceRec.Body.String(), `"accepted":1`) {
		t.Fatalf("POST /v1/traces failed: %d %s", traceRec.Code, traceRec.Body.String())
	}

	svcRec := httptest.NewRecorder()
	handler.ServeHTTP(svcRec, httptest.NewRequest(http.MethodGet, "/api/observatory/services", nil))
	if svcRec.Code != http.StatusOK || !strings.Contains(svcRec.Body.String(), `"probesRun"`) {
		t.Fatalf("GET /api/observatory/services failed: %d %s", svcRec.Code, svcRec.Body.String())
	}

	// 4. Embedded React SPA serves index.html and does not swallow unknown /api routes
	spaRec := httptest.NewRecorder()
	handler.ServeHTTP(spaRec, httptest.NewRequest(http.MethodGet, "/", nil))
	if spaRec.Code != http.StatusOK || !strings.Contains(spaRec.Body.String(), `<div id="root">`) {
		t.Fatalf("GET / expected embedded React root, got %d %s", spaRec.Code, spaRec.Body.String())
	}
	missRec := httptest.NewRecorder()
	handler.ServeHTTP(missRec, httptest.NewRequest(http.MethodGet, "/api/nonexistent-endpoint", nil))
	if missRec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/nonexistent-endpoint expected 404, got %d", missRec.Code)
	}

	// 5. Native GOWA WhatsApp, Coding Agent, Gamification, and Shadow Sidekick endpoints
	pingRec := httptest.NewRecorder()
	handler.ServeHTTP(pingRec, httptest.NewRequest(http.MethodGet, "/api/ping", nil))
	if pingRec.Code != http.StatusOK || !strings.Contains(pingRec.Body.String(), `"ok":true`) {
		t.Fatalf("GET /api/ping failed: %d %s", pingRec.Code, pingRec.Body.String())
	}

	gowaInfoRec := httptest.NewRecorder()
	handler.ServeHTTP(gowaInfoRec, httptest.NewRequest(http.MethodGet, "/app/info", nil))
	if gowaInfoRec.Code != http.StatusOK || !strings.Contains(gowaInfoRec.Body.String(), `"SUCCESS"`) {
		t.Fatalf("GET /app/info failed: %d %s", gowaInfoRec.Code, gowaInfoRec.Body.String())
	}

	waStatsRec := httptest.NewRecorder()
	handler.ServeHTTP(waStatsRec, httptest.NewRequest(http.MethodGet, "/api/whatsapp/stats", nil))
	if waStatsRec.Code != http.StatusOK || !strings.Contains(waStatsRec.Body.String(), `"whatsmeow"`) {
		t.Fatalf("GET /api/whatsapp/stats failed: %d %s", waStatsRec.Code, waStatsRec.Body.String())
	}

	toolsRec := httptest.NewRecorder()
	handler.ServeHTTP(toolsRec, httptest.NewRequest(http.MethodGet, "/api/agent/tools", nil))
	if toolsRec.Code != http.StatusOK || !strings.Contains(toolsRec.Body.String(), "edit_file") {
		t.Fatalf("GET /api/agent/tools failed: %d %s", toolsRec.Code, toolsRec.Body.String())
	}

	gameRec := httptest.NewRecorder()
	handler.ServeHTTP(gameRec, httptest.NewRequest(http.MethodGet, "/api/game/profile", nil))
	if gameRec.Code != http.StatusOK || !strings.Contains(gameRec.Body.String(), `"rankTitle"`) {
		t.Fatalf("GET /api/game/profile failed: %d %s", gameRec.Code, gameRec.Body.String())
	}

	personasRec := httptest.NewRecorder()
	handler.ServeHTTP(personasRec, httptest.NewRequest(http.MethodGet, "/api/assistant/personas", nil))
	if personasRec.Code != http.StatusOK || !strings.Contains(personasRec.Body.String(), "tactician") {
		t.Fatalf("GET /api/assistant/personas failed: %d %s", personasRec.Code, personasRec.Body.String())
	}
}

func TestWorldRadarAndStorageBackupRoutes(t *testing.T) {
	_, handler := setupTestEcho(t, "")

	// 1. World Radar domains (D01..D23), packs, and sources health
	domainsRec := httptest.NewRecorder()
	handler.ServeHTTP(domainsRec, httptest.NewRequest(http.MethodGet, "/api/radar/domains", nil))
	if domainsRec.Code != http.StatusOK ||
		!strings.Contains(domainsRec.Body.String(), `"count":23`) ||
		!strings.Contains(domainsRec.Body.String(), `"D01"`) ||
		!strings.Contains(domainsRec.Body.String(), `"D23"`) {
		t.Fatalf("GET /api/radar/domains failed: %d %s", domainsRec.Code, domainsRec.Body.String())
	}

	packsRec := httptest.NewRecorder()
	handler.ServeHTTP(packsRec, httptest.NewRequest(http.MethodGet, "/api/radar/packs", nil))
	if packsRec.Code != http.StatusOK || !strings.Contains(packsRec.Body.String(), `"packs"`) {
		t.Fatalf("GET /api/radar/packs failed: %d %s", packsRec.Code, packsRec.Body.String())
	}

	healthRec := httptest.NewRecorder()
	handler.ServeHTTP(healthRec, httptest.NewRequest(http.MethodGet, "/api/radar/sources/health", nil))
	if healthRec.Code != http.StatusOK || !strings.Contains(healthRec.Body.String(), `"sources"`) {
		t.Fatalf("GET /api/radar/sources/health failed: %d %s", healthRec.Code, healthRec.Body.String())
	}

	// 2. MCP tools/list exposes echo_radar_events & echo_radar_run and echo_radar_events executes
	mcpListReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	mcpListReq.Header.Set("Content-Type", "application/json")
	mcpListRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpListRec, mcpListReq)
	if mcpListRec.Code != http.StatusOK ||
		!strings.Contains(mcpListRec.Body.String(), "echo_radar_events") ||
		!strings.Contains(mcpListRec.Body.String(), "echo_radar_run") {
		t.Fatalf("MCP tools/list missing radar tools: %d %s", mcpListRec.Code, mcpListRec.Body.String())
	}

	mcpCallReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo_radar_events","arguments":{"limit":5}}}`))
	mcpCallReq.Header.Set("Content-Type", "application/json")
	mcpCallRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpCallRec, mcpCallReq)
	if mcpCallRec.Code != http.StatusOK || strings.Contains(mcpCallRec.Body.String(), `"error"`) {
		t.Fatalf("MCP echo_radar_events failed: %d %s", mcpCallRec.Code, mcpCallRec.Body.String())
	}

	// 3. POST /api/storage/backup snapshots DBs to 9Drive system-backups and awards +35 XP
	backupRec := httptest.NewRecorder()
	handler.ServeHTTP(backupRec, httptest.NewRequest(http.MethodPost, "/api/storage/backup", nil))
	if backupRec.Code != http.StatusOK ||
		!strings.Contains(backupRec.Body.String(), `"backups"`) ||
		!strings.Contains(backupRec.Body.String(), "mw-echo.db") ||
		!strings.Contains(backupRec.Body.String(), "system-backups") {
		t.Fatalf("POST /api/storage/backup failed: %d %s", backupRec.Code, backupRec.Body.String())
	}

	feedRec := httptest.NewRecorder()
	handler.ServeHTTP(feedRec, httptest.NewRequest(http.MethodGet, "/api/game/feed", nil))
	if feedRec.Code != http.StatusOK || !strings.Contains(feedRec.Body.String(), "vault_backup") {
		t.Fatalf("expected vault_backup XP event in /api/game/feed, got %d %s", feedRec.Code, feedRec.Body.String())
	}
}

func TestFlowsBlueprintsAndExpandedMCPRoutes(t *testing.T) {
	_, handler := setupTestEcho(t, "")

	// 1. Seeded starter flows on GET /api/flows
	flowsRec := httptest.NewRecorder()
	handler.ServeHTTP(flowsRec, httptest.NewRequest(http.MethodGet, "/api/flows", nil))
	if flowsRec.Code != http.StatusOK ||
		!strings.Contains(flowsRec.Body.String(), "flow-starter-radar-brief") ||
		!strings.Contains(flowsRec.Body.String(), "flow-starter-9drive-snapshot") {
		t.Fatalf("GET /api/flows missing seeded starters: %d %s", flowsRec.Code, flowsRec.Body.String())
	}

	// 2. Create, run (task creation), and list runs for a flow
	createFlowBody := `{
		"id":"flow-test-task",
		"name":"Test Task Flow",
		"triggerKind":"manual",
		"actionTier":"A1",
		"enabled":true,
		"approved":true,
		"steps":[{"id":"s1","kind":"create_task","config":{"project":"echo","code":"FLOW-1","title":"Verify {{trigger}}","priority":"p1"}}]
	}`
	createFlowReq := httptest.NewRequest(http.MethodPost, "/api/flows", strings.NewReader(createFlowBody))
	createFlowReq.Header.Set("Content-Type", "application/json")
	createFlowRec := httptest.NewRecorder()
	handler.ServeHTTP(createFlowRec, createFlowReq)
	if createFlowRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/flows expected 201, got %d %s", createFlowRec.Code, createFlowRec.Body.String())
	}

	runFlowReq := httptest.NewRequest(http.MethodPost, "/api/flows/flow-test-task/run", strings.NewReader(`{"triggerPayload":"wave-d1"}`))
	runFlowReq.Header.Set("Content-Type", "application/json")
	runFlowRec := httptest.NewRecorder()
	handler.ServeHTTP(runFlowRec, runFlowReq)
	if runFlowRec.Code != http.StatusOK || !strings.Contains(runFlowRec.Body.String(), `"status":"ok"`) {
		t.Fatalf("POST /api/flows/flow-test-task/run failed: %d %s", runFlowRec.Code, runFlowRec.Body.String())
	}

	runsRec := httptest.NewRecorder()
	handler.ServeHTTP(runsRec, httptest.NewRequest(http.MethodGet, "/api/flows/flow-test-task/runs", nil))
	if runsRec.Code != http.StatusOK || !strings.Contains(runsRec.Body.String(), `"count":1`) {
		t.Fatalf("GET /api/flows/flow-test-task/runs failed: %d %s", runsRec.Code, runsRec.Body.String())
	}

	// 3. Curated PaaS blueprints + FTS5 search + compose YAML
	bpRec := httptest.NewRecorder()
	handler.ServeHTTP(bpRec, httptest.NewRequest(http.MethodGet, "/api/blueprints?q=postgres", nil))
	if bpRec.Code != http.StatusOK || !strings.Contains(bpRec.Body.String(), "curated:postgres-16") {
		t.Fatalf("GET /api/blueprints?q=postgres failed: %d %s", bpRec.Code, bpRec.Body.String())
	}

	composeRec := httptest.NewRecorder()
	handler.ServeHTTP(composeRec, httptest.NewRequest(http.MethodGet, "/api/blueprints/curated:mw-echo/compose", nil))
	if composeRec.Code != http.StatusOK || !strings.Contains(composeRec.Body.String(),
		"ghcr.io/a-l-l-a-h/mw-echo:latest") {
		t.Fatalf("GET /api/blueprints/curated:mw-echo/compose failed: %d %s", composeRec.Code, composeRec.Body.String())
	}

	// 4. MCP tools/list + echo_search_blueprints + echo_run_flow
	mcpListReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":10,"method":"tools/list"}`))
	mcpListReq.Header.Set("Content-Type", "application/json")
	mcpListRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpListRec, mcpListReq)
	if mcpListRec.Code != http.StatusOK ||
		!strings.Contains(mcpListRec.Body.String(), "echo_search_blueprints") ||
		!strings.Contains(mcpListRec.Body.String(), "echo_run_flow") {
		t.Fatalf("MCP tools/list missing blueprints/flow tools: %d %s", mcpListRec.Code, mcpListRec.Body.String())
	}

	mcpBPReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"echo_search_blueprints","arguments":{"query":"garage"}}}`))
	mcpBPReq.Header.Set("Content-Type", "application/json")
	mcpBPRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpBPRec, mcpBPReq)
	if mcpBPRec.Code != http.StatusOK || !strings.Contains(mcpBPRec.Body.String(), "garage-s3") {
		t.Fatalf("MCP echo_search_blueprints failed: %d %s", mcpBPRec.Code, mcpBPRec.Body.String())
	}

	mcpFlowReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"echo_run_flow","arguments":{"id":"flow-test-task","triggerPayload":"mcp-run"}}}`))
	mcpFlowReq.Header.Set("Content-Type", "application/json")
	mcpFlowRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpFlowRec, mcpFlowReq)
	if mcpFlowRec.Code != http.StatusOK || !strings.Contains(mcpFlowRec.Body.String(),
		"FLOW-1") || !strings.Contains(mcpFlowRec.Body.String(), "mcp-run") {
		t.Fatalf("MCP echo_run_flow failed: %d %s", mcpFlowRec.Code, mcpFlowRec.Body.String())
	}
}

func TestWaveD2RoutesAndMCPTools(t *testing.T) {
	_, handler := setupTestEcho(t, "")

	for _, endpoint := range []string{
		"/api/comms/channels",
		"/api/vaultenv/keys",
		"/api/desktop/status",
		"/api/desktop/doctor",
		"/api/mesh/nodes",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d: %s", endpoint, rec.Code, rec.Body.String())
		}
	}

	// Store a secret in VaultEnv and verify echo_env_keys never leaks plaintext
	setSecretReq := httptest.NewRequest(http.MethodPut, "/api/vaultenv/secret", strings.NewReader(`{"project":"alpha","envName":"dev","keyName":"API_KEY","value":"super-secret-plaintext-999"}`))
	setSecretReq.Header.Set("Content-Type", "application/json")
	setSecretRec := httptest.NewRecorder()
	handler.ServeHTTP(setSecretRec, setSecretReq)
	if setSecretRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/vaultenv/secret expected 200, got %d: %s", setSecretRec.Code, setSecretRec.Body.String())
	}

	mcpListReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":20,"method":"tools/list"}`))
	mcpListReq.Header.Set("Content-Type", "application/json")
	mcpListRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpListRec, mcpListReq)
	for _, toolName := range []string{"echo_comms_send", "echo_env_keys", "echo_desktop_doctor",
		"echo_local_ai_discover"} {
		if !strings.Contains(mcpListRec.Body.String(), toolName) {
			t.Fatalf("MCP tools/list missing %s: %s", toolName, mcpListRec.Body.String())
		}
	}

	mcpEnvReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":21,"method":"tools/call","params":{"name":"echo_env_keys","arguments":{"project":"alpha","env":"dev"}}}`))
	mcpEnvReq.Header.Set("Content-Type", "application/json")
	mcpEnvRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpEnvRec, mcpEnvReq)
	if mcpEnvRec.Code != http.StatusOK || !strings.Contains(mcpEnvRec.Body.String(),
		"API_KEY") || strings.Contains(mcpEnvRec.Body.String(), "super-secret-plaintext-999") {
		t.Fatalf("MCP echo_env_keys failed or leaked plaintext: %d %s", mcpEnvRec.Code, mcpEnvRec.Body.String())
	}

	mcpDocReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":22,"method":"tools/call","params":{"name":"echo_desktop_doctor","arguments":{}}}`))
	mcpDocReq.Header.Set("Content-Type", "application/json")
	mcpDocRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpDocRec, mcpDocReq)
	if mcpDocRec.Code != http.StatusOK || !strings.Contains(mcpDocRec.Body.String(), "sqlite_integrity") {
		t.Fatalf("MCP echo_desktop_doctor failed: %d %s", mcpDocRec.Code, mcpDocRec.Body.String())
	}
}

func TestV1HandlersAndWorldRadarWhatsAppMCP(t *testing.T) {
	_, handler := setupTestEcho(t, "")

	// 1. WorldRadar /api/v1/* endpoints (health, instances create/list, events query)
	wrHealthRec := httptest.NewRecorder()
	handler.ServeHTTP(wrHealthRec, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if wrHealthRec.Code != http.StatusOK || !strings.Contains(wrHealthRec.Body.String(), `"database":true`) {
		t.Fatalf("GET /api/v1/health failed: %d %s", wrHealthRec.Code, wrHealthRec.Body.String())
	}

	createInstReq := httptest.NewRequest(http.MethodPost, "/api/v1/instances", strings.NewReader(`{"name":"spec-radar","version":"1.0.0","taxonomy_version":"v1.0.0","config_json":"{}"}`))
	createInstReq.Header.Set("Content-Type", "application/json")
	createInstRec := httptest.NewRecorder()
	handler.ServeHTTP(createInstRec, createInstReq)
	if createInstRec.Code != http.StatusCreated || !strings.Contains(createInstRec.Body.String(), "spec-radar") {
		t.Fatalf("POST /api/v1/instances failed: %d %s", createInstRec.Code, createInstRec.Body.String())
	}

	var createdInst struct {
		InstanceID string `json:"instance_id"`
	}
	_ = json.Unmarshal(createInstRec.Body.Bytes(), &createdInst)
	if createdInst.InstanceID == "" {
		t.Fatalf("expected non-empty instance_id in %s", createInstRec.Body.String())
	}

	eventsV1Rec := httptest.NewRecorder()
	handler.ServeHTTP(eventsV1Rec, httptest.NewRequest(http.MethodGet, "/api/v1/events?limit=10", nil))
	if eventsV1Rec.Code != http.StatusOK || !strings.Contains(eventsV1Rec.Body.String(), `"events"`) {
		t.Fatalf("GET /api/v1/events failed: %d %s", eventsV1Rec.Code, eventsV1Rec.Body.String())
	}

	// 2. WhatsApp GOWA /api/v1/* endpoints
	for _, endpoint := range []string{
		"/api/v1/system/health",
		"/api/v1/system/metrics",
		"/api/v1/inbox",
		"/api/v1/assets/index.html",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d: %s", endpoint, rec.Code, rec.Body.String())
		}
	}

	// 3. Assistant & Gamification endpoints (/api/* and /api/v1/*)
	for _, endpoint := range []string{
		"/api/assistant/context",
		"/api/assistant/contract",
		"/api/game/stamps",
		"/api/v1/assistant/contract",
		"/api/v1/game/profile",
		"/api/v1/game/stamps",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s expected 200, got %d: %s", endpoint, rec.Code, rec.Body.String())
		}
	}

	// 4. MCP tools/list exposes worldradar_* and echo_wa_* tools
	mcpListReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":30,"method":"tools/list"}`))
	mcpListReq.Header.Set("Content-Type", "application/json")
	mcpListRec := httptest.NewRecorder()
	handler.ServeHTTP(mcpListRec, mcpListReq)
	if mcpListRec.Code != http.StatusOK {
		t.Fatalf("MCP tools/list failed: %d %s", mcpListRec.Code, mcpListRec.Body.String())
	}
	for _, toolName := range []string{
		"echo_wa_devices",
		"echo_wa_send_message",
		"echo_wa_list_chats",
		"echo_wa_get_messages",
		"worldradar_query_events",
		"worldradar_get_event",
		"worldradar_list_sources",
		"worldradar_register_source",
		"worldradar_trigger_ingest",
		"worldradar_get_freshness",
		"worldradar_get_taxonomy",
	} {
		if !strings.Contains(mcpListRec.Body.String(), toolName) {
			t.Fatalf("MCP tools/list missing %s", toolName)
		}
	}

	// 5. MCP tools/call executes worldradar_get_taxonomy, worldradar_query_events, and echo_wa_devices
	taxReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":31,"method":"tools/call","params":{"name":"worldradar_get_taxonomy","arguments":{"version":"v1.0.0"}}}`))
	taxReq.Header.Set("Content-Type", "application/json")
	taxRec := httptest.NewRecorder()
	handler.ServeHTTP(taxRec, taxReq)
	if taxRec.Code != http.StatusOK || strings.Contains(taxRec.Body.String(), `"error"`) || !strings.Contains(taxRec.Body.String(), "D01") {
		t.Fatalf("MCP worldradar_get_taxonomy failed: %d %s", taxRec.Code, taxRec.Body.String())
	}

	wrQueryReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":32,"method":"tools/call","params":{"name":"worldradar_query_events","arguments":{"instance_id":"`+createdInst.InstanceID+`","limit":5}}}`))
	wrQueryReq.Header.Set("Content-Type", "application/json")
	wrQueryRec := httptest.NewRecorder()
	handler.ServeHTTP(wrQueryRec, wrQueryReq)
	if wrQueryRec.Code != http.StatusOK || strings.Contains(wrQueryRec.Body.String(), `"error"`) {
		t.Fatalf("MCP worldradar_query_events failed: %d %s", wrQueryRec.Code, wrQueryRec.Body.String())
	}

	waDevReq := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":33,"method":"tools/call","params":{"name":"echo_wa_devices","arguments":{}}}`))
	waDevReq.Header.Set("Content-Type", "application/json")
	waDevRec := httptest.NewRecorder()
	handler.ServeHTTP(waDevRec, waDevReq)
	if waDevRec.Code != http.StatusOK || strings.Contains(waDevRec.Body.String(), `"error"`) {
		t.Fatalf("MCP echo_wa_devices failed: %d %s", waDevRec.Code, waDevRec.Body.String())
	}
}
