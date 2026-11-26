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
