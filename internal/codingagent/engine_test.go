package codingagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func openTestHub(t *testing.T) *store.HubStore {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open test hub store: %v", err)
	}
	t.Cleanup(func() {
		_ = hub.Close()
	})
	return hub
}

func makeCompletionPayload(modelName, content string, toolCalls []ToolCall, promptTok, completionTok int) map[string]any {
	msgObj := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if len(toolCalls) > 0 {
		msgObj["tool_calls"] = toolCalls
	}
	return map[string]any{
		"id":      "chatcmpl_mock",
		"object":  "chat.completion",
		"created": time.Now().UTC().Unix(),
		"model":   modelName,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msgObj,
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     promptTok,
			"completion_tokens": completionTok,
			"total_tokens":      promptTok + completionTok,
		},
	}
}

func TestReActMultiTurnToolLoopAndGatewayHeaders(t *testing.T) {
	hub := openTestHub(t)
	workDir := t.TempDir()

	// Seed a workspace file plus skipped directories to verify list_dir filtering.
	mainPath := filepath.Join(workDir, "app.go")
	if err := os.WriteFile(mainPath, []byte("package main\n\nconst Greeting = \"hello\"\n"), 0o644); err != nil {
		t.Fatalf("seed app.go: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, ".git"), 0o755); err != nil {
		t.Fatalf("seed .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("seed .git/HEAD: %v", err)
	}

	var stepCounter atomic.Int32
	var seenAgentHeaders []string
	var headerMu sync.Mutex

	mockGateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		headerMu.Lock()
		seenAgentHeaders = append(seenAgentHeaders, req.Header.Get(headerAgentProfile))
		headerMu.Unlock()

		var decodedReq struct {
			Model    string                   `json:"model"`
			Messages []map[string]any         `json:"messages"`
			Tools    []map[string]interface{} `json:"tools"`
		}
		if err := json.NewDecoder(req.Body).Decode(&decodedReq); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if len(decodedReq.Tools) < 8 {
			http.Error(writer, "expected 8 tool definitions", http.StatusBadRequest)
			return
		}

		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set(headerEchoProvider, "mock-bifrost")
		writer.Header().Set(headerEchoModel, "mock-coder-1")
		writer.Header().Set(headerEchoCache, "MISS")
		writer.Header().Set(headerEchoRTKSaved, "42")

		turnNum := stepCounter.Add(1)
		switch turnNum {
		case 1:
			_ = json.NewEncoder(writer).Encode(makeCompletionPayload("mock-coder-1", "", []ToolCall{
				{
					ID:   "call_list_1",
					Type: "function",
					Function: ToolCallFunction{
						Name:      "list_dir",
						Arguments: `{"path":"."}`,
					},
				},
			}, 120, 25))
		case 2:
			_ = json.NewEncoder(writer).Encode(makeCompletionPayload("mock-coder-1", "", []ToolCall{
				{
					ID:   "call_read_2",
					Type: "function",
					Function: ToolCallFunction{
						Name:      "read_file",
						Arguments: `{"path":"app.go"}`,
					},
				},
			}, 160, 20))
		case 3:
			_ = json.NewEncoder(writer).Encode(makeCompletionPayload("mock-coder-1", "", []ToolCall{
				{
					ID:   "call_edit_3",
					Type: "function",
					Function: ToolCallFunction{
						Name:      "edit_file",
						Arguments: `{"path":"app.go","target":"const Greeting = \"hello\"","replacement":"const Greeting = \"hello world\""}`,
					},
				},
			}, 210, 35))
		default:
			_ = json.NewEncoder(writer).Encode(makeCompletionPayload("mock-coder-1", "Updated Greeting constant in app.go.", nil, 250, 18))
		}
	}))
	defer mockGateway.Close()

	engine, err := NewEngine(hub, Options{
		DefaultGatewayURL: mockGateway.URL + "/v1/chat/completions",
		HTTPClient:        mockGateway.Client(),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	var observedTypes []string
	reply, err := engine.RunOneShot(context.Background(), workDir, "", "", ModeAct, "Update Greeting in app.go", func(ev AgentEvent) {
		observedTypes = append(observedTypes, ev.Type)
	})
	if err != nil {
		t.Fatalf("RunOneShot failed: %v", err)
	}
	if reply != "Updated Greeting constant in app.go." {
		t.Fatalf("unexpected final reply: %q", reply)
	}

	// Verify real file mutation on disk.
	updatedBytes, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read mutated app.go: %v", err)
	}
	if !strings.Contains(string(updatedBytes), `const Greeting = "hello world"`) {
		t.Fatalf("app.go was not updated: %s", string(updatedBytes))
	}

	// Verify default X-MW-Agent header was sent on all 4 gateway calls.
	headerMu.Lock()
	if len(seenAgentHeaders) != 4 {
		t.Fatalf("expected 4 gateway calls, got %d", len(seenAgentHeaders))
	}
	for idx, headerVal := range seenAgentHeaders {
		if headerVal != DefaultAgentProfile {
			t.Fatalf("call %d expected X-MW-Agent=%q, got %q", idx+1, DefaultAgentProfile, headerVal)
		}
	}
	headerMu.Unlock()

	// Verify session telemetry (tokens + RTK savings) persisted in SQLite.
	sessions, err := engine.ListSessions(context.Background(), 10)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("expected 1 persisted session, got %d (err=%v)", len(sessions), err)
	}
	sess := sessions[0]
	if sess.TotalTokensIn != 120+160+210+250 {
		t.Fatalf("expected TotalTokensIn=740, got %d", sess.TotalTokensIn)
	}
	if sess.TotalTokensOut != 25+20+35+18 {
		t.Fatalf("expected TotalTokensOut=98, got %d", sess.TotalTokensOut)
	}
	if sess.RTKTokensSaved != 42*4 {
		t.Fatalf("expected RTKTokensSaved=168, got %d", sess.RTKTokensSaved)
	}

	// Verify list_dir skipped .git directory in persisted tool output.
	messages, err := engine.ListMessages(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	foundListTool := false
	for _, msg := range messages {
		if msg.Role == "tool" && msg.ToolName == "list_dir" {
			foundListTool = true
			if strings.Contains(msg.Content, ".git") {
				t.Fatalf("list_dir should skip .git, got: %s", msg.Content)
			}
			if !strings.Contains(msg.Content, "app.go") {
				t.Fatalf("list_dir should contain app.go, got: %s", msg.Content)
			}
		}
	}
	if !foundListTool {
		t.Fatalf("expected persisted list_dir tool message")
	}

	// Verify event lifecycle sequence contains core events.
	joinedEvents := strings.Join(observedTypes, ",")
	for _, requiredEvent := range []string{"turn_start", "step", "tool_call", "tool_result", "assistant_message", "turn_end"} {
		if !strings.Contains(joinedEvents, requiredEvent) {
			t.Fatalf("expected event %q in stream: %s", requiredEvent, joinedEvents)
		}
	}
}

func TestPlanModeBlocksMutatingToolsAndAllowsReadOnly(t *testing.T) {
	hub := openTestHub(t)
	workDir := t.TempDir()

	sampleFile := filepath.Join(workDir, "notes.txt")
	if err := os.WriteFile(sampleFile, []byte("alpha\nbeta needle\ngamma\n"), 0o644); err != nil {
		t.Fatalf("seed notes.txt: %v", err)
	}

	engine, err := NewEngine(hub, Options{})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	session, err := engine.CreateSession(context.Background(), SessionCreateInput{
		Title:   "Plan Verification",
		WorkDir: workDir,
		Mode:    ModePlan,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Blocked tools in plan mode.
	if _, err := engine.ExecuteTool(context.Background(), session, "write_file", `{"path":"new.txt","content":"nope"}`); err == nil || !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("expected write_file to be blocked in plan mode, got err=%v", err)
	}
	if _, err := engine.ExecuteTool(context.Background(), session, "edit_file", `{"path":"notes.txt","target":"alpha","replacement":"ALPHA"}`); err == nil || !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("expected edit_file to be blocked in plan mode, got err=%v", err)
	}
	if _, err := engine.ExecuteTool(context.Background(), session, "run_shell", `{"command":"echo blocked"}`); err == nil || !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("expected run_shell to be blocked in plan mode, got err=%v", err)
	}

	// Allowed read-only tools in plan mode.
	readOut, err := engine.ExecuteTool(context.Background(), session, "read_file", `{"path":"notes.txt","start_line":1,"end_line":3}`)
	if err != nil || !strings.Contains(readOut, "beta needle") {
		t.Fatalf("expected read_file to succeed in plan mode, out=%q err=%v", readOut, err)
	}
	grepOut, err := engine.ExecuteTool(context.Background(), session, "grep_search", `{"pattern":"needle"}`)
	if err != nil || !strings.Contains(grepOut, "notes.txt:2:beta needle") {
		t.Fatalf("expected grep_search to succeed in plan mode, out=%q err=%v", grepOut, err)
	}

	// Slash command switches mode to act and back to plan cleanly without invoking gateway.
	actOutcome, err := engine.RunPrompt(context.Background(), session.ID, "/act")
	if err != nil || actOutcome.Session.Mode != ModeAct {
		t.Fatalf("expected /act slash command to switch mode to act, got mode=%q err=%v", actOutcome.Session.Mode, err)
	}
	planOutcome, err := engine.RunPrompt(context.Background(), session.ID, "/plan")
	if err != nil || planOutcome.Session.Mode != ModePlan {
		t.Fatalf("expected /plan slash command to switch mode to plan, got mode=%q err=%v", planOutcome.Session.Mode, err)
	}
}

func TestPathTraversalSandboxBlocksEscape(t *testing.T) {
	hub := openTestHub(t)
	parentDir := t.TempDir()
	workDir := filepath.Join(parentDir, "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	outsideFile := filepath.Join(parentDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("top-secret"), 0o644); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}

	engine, err := NewEngine(hub, Options{})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	session, err := engine.CreateSession(context.Background(), SessionCreateInput{
		WorkDir: workDir,
		Mode:    ModeAct,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	escapeTargets := []string{
		"../../etc/passwd",
		"../secret.txt",
		outsideFile,
	}
	for _, escapeTarget := range escapeTargets {
		if _, err := resolveSafePath(workDir, escapeTarget); err == nil {
			t.Fatalf("expected resolveSafePath to block %q", escapeTarget)
		}
		readArgs, _ := json.Marshal(map[string]string{"path": escapeTarget})
		if _, err := engine.ExecuteTool(context.Background(), session, "read_file", string(readArgs)); err == nil {
			t.Fatalf("expected read_file to block %q", escapeTarget)
		}
		writeArgs, _ := json.Marshal(map[string]string{"path": escapeTarget, "content": "pwn"})
		if _, err := engine.ExecuteTool(context.Background(), session, "write_file", string(writeArgs)); err == nil {
			t.Fatalf("expected write_file to block %q", escapeTarget)
		}
	}

	// Ensure outside file remained untouched.
	checkBytes, err := os.ReadFile(outsideFile)
	if err != nil || string(checkBytes) != "top-secret" {
		t.Fatalf("outside file was modified: %q err=%v", string(checkBytes), err)
	}
}

func TestForkSessionAndCompactSession(t *testing.T) {
	hub := openTestHub(t)
	workDir := t.TempDir()

	var callCount atomic.Int32
	mockGateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		turn := callCount.Add(1)
		_ = json.NewEncoder(writer).Encode(makeCompletionPayload("mock-model", "Reply #"+string(rune('0'+turn)), nil, 50, 15))
	}))
	defer mockGateway.Close()

	engine, err := NewEngine(hub, Options{
		DefaultGatewayURL: mockGateway.URL,
		HTTPClient:        mockGateway.Client(),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	session, err := engine.CreateSession(context.Background(), SessionCreateInput{
		Title:   "Origin Session",
		WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	for idx := 0; idx < 4; idx++ {
		if _, err := engine.RunPrompt(context.Background(), session.ID, "Question turn"); err != nil {
			t.Fatalf("RunPrompt %d: %v", idx+1, err)
		}
	}

	// Origin now has 8 messages (4 user + 4 assistant).
	origMsgs, err := engine.ListMessages(context.Background(), session.ID)
	if err != nil || len(origMsgs) != 8 {
		t.Fatalf("expected 8 messages in origin session, got %d (err=%v)", len(origMsgs), err)
	}

	// Fork up to seq 4.
	partialFork, err := engine.ForkSession(context.Background(), session.ID, "Branch At 4", 4)
	if err != nil {
		t.Fatalf("ForkSession upToSeq=4: %v", err)
	}
	partialMsgs, err := engine.ListMessages(context.Background(), partialFork.ID)
	if err != nil || len(partialMsgs) != 4 {
		t.Fatalf("expected 4 messages in partial fork, got %d (err=%v)", len(partialMsgs), err)
	}

	// Fork full session without upToSeq.
	fullFork, err := engine.ForkSession(context.Background(), session.ID, "Full Branch")
	if err != nil {
		t.Fatalf("ForkSession full: %v", err)
	}
	fullMsgs, err := engine.ListMessages(context.Background(), fullFork.ID)
	if err != nil || len(fullMsgs) != 8 {
		t.Fatalf("expected 8 messages in full fork, got %d (err=%v)", len(fullMsgs), err)
	}

	// Compact origin session and confirm message count shrinks cleanly while preserving recent turns.
	compacted, err := engine.CompactSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("CompactSession: %v", err)
	}
	if !compacted.Compacted {
		t.Fatalf("expected Compacted=true")
	}
	if compacted.KeptMessages != 5 {
		t.Fatalf("expected 5 messages (1 summary + 4 recent), got %d", compacted.KeptMessages)
	}
	if !strings.Contains(compacted.Summary, "Compacted Conversation Summary") {
		t.Fatalf("unexpected compaction summary: %q", compacted.Summary)
	}

	afterMsgs, err := engine.ListMessages(context.Background(), session.ID)
	if err != nil || len(afterMsgs) != 5 {
		t.Fatalf("expected 5 persisted messages after compact, got %d (err=%v)", len(afterMsgs), err)
	}
	if afterMsgs[0].Role != "system" || afterMsgs[0].Seq != 1 || afterMsgs[0].Meta == nil || !afterMsgs[0].Meta.Compacted {
		t.Fatalf("expected first message after compact to be compacted system summary, got: %+v", afterMsgs[0])
	}

	// Verify forked sessions remained completely isolated from compaction on origin.
	fullMsgsAfter, err := engine.ListMessages(context.Background(), fullFork.ID)
	if err != nil || len(fullMsgsAfter) != 8 {
		t.Fatalf("expected full fork to remain untouched with 8 messages, got %d", len(fullMsgsAfter))
	}
}

func TestHubToolsAndRunShellAndAbort(t *testing.T) {
	hub := openTestHub(t)
	workDir := t.TempDir()

	if _, err := hub.UpsertDoc(context.Background(), store.DocRecord{
		Project: "demo",
		RelPath: "docs/architecture.md",
		Title:   "Architecture Overview",
		Content: "# Architecture\nNative Go coding agent wired to AI gateway.\n",
	}); err != nil {
		t.Fatalf("UpsertDoc: %v", err)
	}
	if _, err := hub.UpsertTask(context.Background(), store.TaskRecord{
		Code:    "CA-01",
		Project: "demo",
		Title:   "Build Coding Agent",
		Status:  "todo",
	}); err != nil {
		t.Fatalf("UpsertTask: %v", err)
	}

	gateEntered := make(chan struct{}, 1)
	releaseSlow := make(chan struct{})
	slowGateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		select {
		case gateEntered <- struct{}{}:
		default:
		}
		select {
		case <-req.Context().Done():
		case <-releaseSlow:
		}
	}))
	defer func() {
		close(releaseSlow)
		slowGateway.CloseClientConnections()
		slowGateway.Close()
	}()

	engine, err := NewEngine(hub, Options{
		DefaultGatewayURL: slowGateway.URL,
		HTTPClient:        slowGateway.Client(),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	session, err := engine.CreateSession(context.Background(), SessionCreateInput{
		WorkDir: workDir,
		Project: "demo",
		Mode:    ModeAct,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Test hub_search_docs.
	docsOut, err := engine.ExecuteTool(context.Background(), session, "hub_search_docs", `{"query":"gateway","project":"demo"}`)
	if err != nil || !strings.Contains(docsOut, "docs/architecture.md") {
		t.Fatalf("hub_search_docs unexpected result: out=%q err=%v", docsOut, err)
	}

	// Test hub_tasks list + update.
	tasksListOut, err := engine.ExecuteTool(context.Background(), session, "hub_tasks", `{"action":"list","project":"demo"}`)
	if err != nil || !strings.Contains(tasksListOut, "CA-01") {
		t.Fatalf("hub_tasks list unexpected result: out=%q err=%v", tasksListOut, err)
	}
	tasksUpdateOut, err := engine.ExecuteTool(context.Background(), session, "hub_tasks", `{"action":"update","code":"CA-01","status":"done"}`)
	if err != nil || !strings.Contains(tasksUpdateOut, `"status": "done"`) {
		t.Fatalf("hub_tasks update unexpected result: out=%q err=%v", tasksUpdateOut, err)
	}

	// Test run_shell execution.
	shellOut, err := engine.ExecuteTool(context.Background(), session, "run_shell", `{"command":"echo mw-echo-shell-ok","timeout_sec":10}`)
	if err != nil || !strings.Contains(shellOut, "exit_code: 0") || !strings.Contains(shellOut, "mw-echo-shell-ok") {
		t.Fatalf("run_shell unexpected result: out=%q err=%v", shellOut, err)
	}

	// Test live AbortSession cancels in-flight gateway turn.
	doneErrCh := make(chan error, 1)
	go func() {
		_, runErr := engine.RunPrompt(context.Background(), session.ID, "long running turn")
		doneErrCh <- runErr
	}()

	select {
	case <-gateEntered:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for gateway request to start")
	}

	if !engine.AbortSession(session.ID) {
		t.Fatalf("expected AbortSession to return true for active turn")
	}

	select {
	case runErr := <-doneErrCh:
		if runErr != context.Canceled {
			t.Fatalf("expected context.Canceled after abort, got %v", runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for aborted turn to finish")
	}
}

func TestHTTPRoutesEndToEndAndSSE(t *testing.T) {
	hub := openTestHub(t)
	workDir := t.TempDir()

	mockGateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set(headerEchoProvider, "route-mock")
		writer.Header().Set(headerEchoModel, "route-model-v1")
		writer.Header().Set(headerEchoCache, "HIT")
		writer.Header().Set(headerEchoRTKSaved, "15")
		_ = json.NewEncoder(writer).Encode(makeCompletionPayload("route-model-v1", "Route reply complete.", nil, 40, 10))
	}))
	defer mockGateway.Close()

	engine, err := NewEngine(hub, Options{
		DefaultGatewayURL: mockGateway.URL,
		HTTPClient:        mockGateway.Client(),
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	app := echo.New()
	app.HideBanner = true
	apiGroup := app.Group("/api")
	engine.RegisterRoutes(apiGroup)

	apiServer := httptest.NewServer(app)
	defer func() {
		apiServer.CloseClientConnections()
		apiServer.Close()
	}()
	client := apiServer.Client()

	// 1. GET /api/agent/tools
	toolsResp, err := client.Get(apiServer.URL + "/api/agent/tools")
	if err != nil || toolsResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/agent/tools failed: err=%v status=%v", err, toolsResp)
	}
	var toolsBody struct {
		Tools []ToolSpec `json:"tools"`
		Count int        `json:"count"`
	}
	_ = json.NewDecoder(toolsResp.Body).Decode(&toolsBody)
	_ = toolsResp.Body.Close()
	if toolsBody.Count != 9 {
		t.Fatalf("expected 9 tools, got %d", toolsBody.Count)
	}

	// 2. POST /api/agent/sessions
	createPayload, _ := json.Marshal(map[string]any{
		"title":        "HTTP Test Session",
		"workDir":      workDir,
		"project":      "mw-echo",
		"agentProfile": "codex",
		"mode":         "act",
	})
	createResp, err := client.Post(apiServer.URL+"/api/agent/sessions", "application/json", bytes.NewReader(createPayload))
	if err != nil || createResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/agent/sessions failed: err=%v", err)
	}
	var createdSession Session
	_ = json.NewDecoder(createResp.Body).Decode(&createdSession)
	_ = createResp.Body.Close()
	if createdSession.ID == "" || createdSession.AgentProfile != "codex" {
		t.Fatalf("unexpected created session: %+v", createdSession)
	}

	// 3. POST /api/agent/sessions/:id/prompt (sync)
	promptPayload := []byte(`{"prompt":"Hello from HTTP test","async":false}`)
	promptResp, err := client.Post(apiServer.URL+"/api/agent/sessions/"+createdSession.ID+"/prompt", "application/json", bytes.NewReader(promptPayload))
	if err != nil || promptResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /prompt failed: err=%v status=%d", err, promptResp.StatusCode)
	}
	var promptOut struct {
		Session  Session   `json:"session"`
		Messages []Message `json:"messages"`
		Reply    string    `json:"reply"`
	}
	_ = json.NewDecoder(promptResp.Body).Decode(&promptOut)
	_ = promptResp.Body.Close()
	if promptOut.Reply != "Route reply complete." || len(promptOut.Messages) != 2 {
		t.Fatalf("unexpected prompt output: reply=%q msgs=%d", promptOut.Reply, len(promptOut.Messages))
	}

	// 4. GET /api/agent/sessions/:id
	getResp, err := client.Get(apiServer.URL + "/api/agent/sessions/" + createdSession.ID)
	if err != nil || getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /sessions/:id failed: err=%v", err)
	}
	var detailOut struct {
		Session  Session   `json:"session"`
		Messages []Message `json:"messages"`
	}
	_ = json.NewDecoder(getResp.Body).Decode(&detailOut)
	_ = getResp.Body.Close()
	if detailOut.Session.ID != createdSession.ID || len(detailOut.Messages) != 2 {
		t.Fatalf("unexpected session detail: %+v msgs=%d", detailOut.Session, len(detailOut.Messages))
	}

	// 5. PATCH /api/agent/sessions/:id
	patchReq, _ := http.NewRequest(http.MethodPatch, apiServer.URL+"/api/agent/sessions/"+createdSession.ID, bytes.NewReader([]byte(`{"mode":"plan","title":"Patched Title"}`)))
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := client.Do(patchReq)
	if err != nil || patchResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH /sessions/:id failed: err=%v", err)
	}
	var patched Session
	_ = json.NewDecoder(patchResp.Body).Decode(&patched)
	_ = patchResp.Body.Close()
	if patched.Mode != ModePlan || patched.Title != "Patched Title" {
		t.Fatalf("unexpected patched session: %+v", patched)
	}

	// 6. POST /api/agent/sessions/:id/fork
	forkResp, err := client.Post(apiServer.URL+"/api/agent/sessions/"+createdSession.ID+"/fork", "application/json", bytes.NewReader([]byte(`{"title":"Forked via HTTP"}`)))
	if err != nil || forkResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /fork failed: err=%v status=%d", err, forkResp.StatusCode)
	}
	var forkedSession Session
	_ = json.NewDecoder(forkResp.Body).Decode(&forkedSession)
	_ = forkResp.Body.Close()
	if forkedSession.ID == "" || forkedSession.ID == createdSession.ID || forkedSession.Title != "Forked via HTTP" {
		t.Fatalf("unexpected forked session: %+v", forkedSession)
	}

	// 7. SSE stream + async prompt delivery
	sseCtx, cancelSSE := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelSSE()
	sseReq, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, apiServer.URL+"/api/agent/sessions/"+forkedSession.ID+"/events", nil)
	sseResp, err := client.Do(sseReq)
	if err != nil || sseResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events failed: err=%v", err)
	}
	defer sseResp.Body.Close()

	sseReader := bufio.NewReader(sseResp.Body)
	// Wait for initial ": connected" comment so subscription is active before posting async prompt.
	firstLine, err := sseReader.ReadString('\n')
	if err != nil || !strings.HasPrefix(firstLine, ": connected") {
		t.Fatalf("expected SSE connected comment, got %q err=%v", firstLine, err)
	}

	asyncResp, err := client.Post(apiServer.URL+"/api/agent/sessions/"+forkedSession.ID+"/prompt", "application/json", bytes.NewReader([]byte(`{"prompt":"Async SSE test","async":true}`)))
	if err != nil || asyncResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST async /prompt expected 202, got err=%v status=%d", err, asyncResp.StatusCode)
	}
	_ = asyncResp.Body.Close()

	receivedTurnEnd := false
	for !receivedTurnEnd {
		line, readErr := sseReader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("SSE read error before turn_end: %v", readErr)
		}
		if strings.HasPrefix(line, "data: ") {
			rawJSON := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			var ev AgentEvent
			if unmarshalErr := json.Unmarshal([]byte(rawJSON), &ev); unmarshalErr == nil && ev.Type == "turn_end" {
				receivedTurnEnd = true
			}
		}
	}

	// 8. DELETE /api/agent/sessions/:id
	delReq, _ := http.NewRequest(http.MethodDelete, apiServer.URL+"/api/agent/sessions/"+forkedSession.ID, nil)
	delResp, err := client.Do(delReq)
	if err != nil || delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /sessions/:id failed: err=%v", err)
	}
	_ = delResp.Body.Close()
}
