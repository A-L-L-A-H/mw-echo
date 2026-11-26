package memvault

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func openTestManager(t *testing.T) *Manager {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() {
		_ = hub.Close()
	})
	manager, err := NewManager(hub)
	if err != nil {
		t.Fatalf("new memvault manager: %v", err)
	}
	return manager
}

func buildTestZIP(t *testing.T, filename string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)
	entryWriter, err := zipWriter.Create(filename)
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entryWriter.Write(content); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buf.Bytes()
}

func TestImportChatGPTJSONDedupeAndClassification(t *testing.T) {
	ctx := context.Background()
	manager := openTestManager(t)

	exportJSON := []byte(`[
		{
			"id": "conv-arch-1",
			"title": "SQLite WAL Architecture",
			"create_time": 1700000000,
			"current_node": "n2",
			"mapping": {
				"root": {"id": "root", "parent": null, "children": ["n1"], "message": null},
				"n1": {
					"id": "n1",
					"parent": "root",
					"children": ["n2"],
					"message": {
						"id": "m1",
						"author": {"role": "user"},
						"create_time": 1700000001,
						"content": {"content_type": "text", "parts": ["Explain SQLite WAL mode with FTS5 indexing."]}
					}
				},
				"n2": {
					"id": "n2",
					"parent": "n1",
					"children": [],
					"message": {
						"id": "m2",
						"author": {"role": "assistant"},
						"create_time": 1700000002,
						"content": {"content_type": "text", "parts": ["WAL mode enables concurrent readers alongside FTS5 virtual tables."]}
					}
				}
			}
		},
		{
			"id": "conv-child-1",
			"title": "Kindergarten Schedule",
			"create_time": 1700000100,
			"messages": [
				{"role": "user", "content": "Help me plan my toddler bedtime and pediatric checkup."},
				{"role": "assistant", "content": "Here is a gentle routine for your child."}
			]
		}
	]`)

	firstReport, err := manager.ImportChatGPTJSON(ctx, exportJSON)
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	if firstReport.ImportedConversations != 2 {
		t.Fatalf("expected 2 imported conversations, got %d", firstReport.ImportedConversations)
	}
	if firstReport.ImportedMessages != 4 {
		t.Fatalf("expected 4 imported messages, got %d", firstReport.ImportedMessages)
	}
	if firstReport.ChildSensitiveFlagged != 1 {
		t.Fatalf("expected 1 child_sensitive flagged, got %d", firstReport.ChildSensitiveFlagged)
	}

	// Re-importing the exact same payload must deduplicate via SHA-256.
	secondReport, err := manager.ImportChatGPTJSON(ctx, exportJSON)
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if secondReport.ImportedConversations != 0 || secondReport.SkippedDuplicates != 2 {
		t.Fatalf("expected 0 imported and 2 skipped duplicates, got imported=%d skipped=%d",
			secondReport.ImportedConversations, secondReport.SkippedDuplicates)
	}

	convs, err := manager.ListConversations(ctx, "", 10)
	if err != nil {
		t.Fatalf("list conversations failed: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("expected 2 stored conversations, got %d", len(convs))
	}
	for _, conv := range convs {
		if conv.AllowCloudLLM {
			t.Fatalf("expected allow_cloud_llm=false on fresh import for %s", conv.Title)
		}
		if conv.Title == "Kindergarten Schedule" && conv.Sensitivity != SensitivityChildSensitive {
			t.Fatalf("expected child_sensitive for Kindergarten Schedule, got %s", conv.Sensitivity)
		}
		if conv.Title == "SQLite WAL Architecture" && conv.Sensitivity != SensitivityPrivate {
			t.Fatalf("expected private for SQLite WAL Architecture, got %s", conv.Sensitivity)
		}
	}
}

func TestImportZIPAndPrivacyFirewall(t *testing.T) {
	ctx := context.Background()
	manager := openTestManager(t)

	conversationsJSON := []byte(`[
		{
			"title": "Public Go Notes",
			"messages": [
				{"role": "user", "content": "Quantum telemetry pipeline benchmark in pure Go."}
			]
		},
		{
			"title": "Private Journal",
			"messages": [
				{"role": "user", "content": "Quantum telemetry personal reflections and private notes."}
			]
		},
		{
			"title": "Child Homework",
			"messages": [
				{"role": "user", "content": "Quantum telemetry science project for my son in elementary school."}
			]
		}
	]`)

	zipBytes := buildTestZIP(t, "export/conversations.json", conversationsJSON)
	report, err := manager.ImportZIP(ctx, zipBytes)
	if err != nil {
		t.Fatalf("import zip failed: %v", err)
	}
	if report.ImportedConversations != 3 || report.ChildSensitiveFlagged != 1 {
		t.Fatalf("unexpected zip import report: %+v", report)
	}

	// Local search (cloudSafeOnly=false) returns all 3 matching conversations.
	localHits, err := manager.Search(ctx, "quantum telemetry", false, 20)
	if err != nil {
		t.Fatalf("local search failed: %v", err)
	}
	if len(localHits) != 3 {
		t.Fatalf("expected 3 local search hits, got %d", len(localHits))
	}

	// Cloud-safe search (cloudSafeOnly=true) returns 0 hits before any conversation is opted in.
	initialCloudHits, err := manager.Search(ctx, "quantum telemetry", true, 20)
	if err != nil {
		t.Fatalf("initial cloud-safe search failed: %v", err)
	}
	if len(initialCloudHits) != 0 {
		t.Fatalf("expected 0 cloud-safe hits initially, got %d", len(initialCloudHits))
	}

	convs, err := manager.ListConversations(ctx, "", 10)
	if err != nil {
		t.Fatalf("list conversations failed: %v", err)
	}
	var publicID, privateID, childID string
	for _, conv := range convs {
		switch conv.Title {
		case "Public Go Notes":
			publicID = conv.ID
		case "Private Journal":
			privateID = conv.ID
		case "Child Homework":
			childID = conv.ID
		}
	}

	// Hard Privacy Firewall: reject allowCloudLLM=true on child_sensitive.
	if _, err := manager.SetPrivacy(ctx, childID, SensitivityChildSensitive, true); !errors.Is(err, ErrPrivacyViolation) {
		t.Fatalf("expected ErrPrivacyViolation for child_sensitive with allowCloudLLM=true, got %v", err)
	}

	// Even if a private conversation had allowCloudLLM toggled, cloudSafeOnly requires sensitivity='public' AND allow_cloud_llm=1.
	if _, err := manager.SetPrivacy(ctx, privateID, SensitivityPrivate, true); err != nil {
		t.Fatalf("set private conversation privacy failed: %v", err)
	}
	if _, err := manager.SetPrivacy(ctx, publicID, SensitivityPublic, true); err != nil {
		t.Fatalf("set public conversation privacy failed: %v", err)
	}

	cloudHits, err := manager.Search(ctx, "quantum telemetry", true, 20)
	if err != nil {
		t.Fatalf("cloud-safe search failed: %v", err)
	}
	if len(cloudHits) != 1 {
		t.Fatalf("expected exactly 1 cloud-safe hit, got %d", len(cloudHits))
	}
	if cloudHits[0].ConversationID != publicID || cloudHits[0].Sensitivity != SensitivityPublic || !cloudHits[0].AllowCloudLLM {
		t.Fatalf("unexpected cloud-safe hit: %+v", cloudHits[0])
	}

	stats, err := manager.Stats(ctx)
	if err != nil {
		t.Fatalf("stats failed: %v", err)
	}
	if stats.TotalConversations != 3 || stats.TotalMessages != 3 || stats.CloudSafeCount != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if stats.BySensitivity[SensitivityChildSensitive] != 1 || stats.BySensitivity[SensitivityPrivate] != 1 || stats.BySensitivity[SensitivityPublic] != 1 {
		t.Fatalf("unexpected sensitivity breakdown: %+v", stats.BySensitivity)
	}
}

func TestMemvaultRoutes(t *testing.T) {
	manager := openTestManager(t)
	echoServer := echo.New()
	apiGroup := echoServer.Group("/api")
	manager.RegisterRoutes(apiGroup)

	importBody := []byte(`{"title":"Route Test","messages":[{"role":"user","content":"Hello memvault route search"}]}`)
	importReq := httptest.NewRequest(http.MethodPost, "/api/memvault/import", bytes.NewReader(importBody))
	importReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	importRec := httptest.NewRecorder()
	echoServer.ServeHTTP(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("POST /api/memvault/import status=%d body=%s", importRec.Code, importRec.Body.String())
	}

	var importReport ImportReport
	if err := json.Unmarshal(importRec.Body.Bytes(), &importReport); err != nil {
		t.Fatalf("decode import report: %v", err)
	}
	if len(importReport.ConversationIDs) != 1 {
		t.Fatalf("expected 1 conversation ID, got %d", len(importReport.ConversationIDs))
	}
	convID := importReport.ConversationIDs[0]

	// Reject child_sensitive + allowCloudLLM=true with HTTP 403.
	forbiddenReq := httptest.NewRequest(http.MethodPatch, "/api/memvault/conversations/"+convID+"/privacy",
		bytes.NewBufferString(`{"sensitivity":"child_sensitive","allowCloudLLM":true}`))
	forbiddenReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	forbiddenRec := httptest.NewRecorder()
	echoServer.ServeHTTP(forbiddenRec, forbiddenReq)
	if forbiddenRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for child_sensitive cloud opt-in, got %d body=%s", forbiddenRec.Code, forbiddenRec.Body.String())
	}

	// Promote to public + allowCloudLLM=true.
	promoteReq := httptest.NewRequest(http.MethodPatch, "/api/memvault/conversations/"+convID+"/privacy",
		bytes.NewBufferString(`{"sensitivity":"public","allowCloudLLM":true}`))
	promoteReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	promoteRec := httptest.NewRecorder()
	echoServer.ServeHTTP(promoteRec, promoteReq)
	if promoteRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for public opt-in, got %d body=%s", promoteRec.Code, promoteRec.Body.String())
	}

	// Search with cloudSafeOnly=true.
	searchReq := httptest.NewRequest(http.MethodGet, "/api/memvault/search?q=memvault&cloudSafeOnly=true", nil)
	searchRec := httptest.NewRecorder()
	echoServer.ServeHTTP(searchRec, searchReq)
	if searchRec.Code != http.StatusOK {
		t.Fatalf("GET /api/memvault/search status=%d body=%s", searchRec.Code, searchRec.Body.String())
	}

	// Get conversation detail.
	detailReq := httptest.NewRequest(http.MethodGet, "/api/memvault/conversations/"+convID, nil)
	detailRec := httptest.NewRecorder()
	echoServer.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("GET /api/memvault/conversations/:id status=%d body=%s", detailRec.Code, detailRec.Body.String())
	}

	// Stats endpoint.
	statsReq := httptest.NewRequest(http.MethodGet, "/api/memvault/stats", nil)
	statsRec := httptest.NewRecorder()
	echoServer.ServeHTTP(statsRec, statsReq)
	if statsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/memvault/stats status=%d body=%s", statsRec.Code, statsRec.Body.String())
	}
}
