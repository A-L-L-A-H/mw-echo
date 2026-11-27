package assistant_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/assistant"
	"github.com/A-L-L-A-H/mw-echo/internal/gamification"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func TestAssistantPersonasSpec(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	defer func() { _ = hub.Close() }()

	svc, err := assistant.New(hub, assistant.Options{})
	if err != nil {
		t.Fatalf("init assistant: %v", err)
	}

	personas := svc.Personas()
	if len(personas) != 4 {
		t.Fatalf("expected 4 personas, got %d", len(personas))
	}

	want := map[string]struct {
		name    string
		accent  string
		profile string
	}{
		"tactician":  {name: "TACTICIAN", accent: "#3b82f6", profile: "claude-code"},
		"hacker":     {name: "HACKER", accent: "#10b981", profile: "opencode"},
		"oracle":     {name: "ORACLE", accent: "#8b5cf6", profile: "gemini-cli"},
		"dispatcher": {name: "DISPATCHER", accent: "#f59e0b", profile: "default"},
	}

	for _, p := range personas {
		exp, ok := want[p.ID]
		if !ok {
			t.Fatalf("unexpected persona id %q", p.ID)
		}
		if p.Name != exp.name || p.Accent != exp.accent || p.AgentProfile != exp.profile {
			t.Fatalf("persona %q mismatch: got %+v want %+v", p.ID, p, exp)
		}
	}
}

func TestAssistantLiveContextInjectionAndToolCalling(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	defer func() { _ = hub.Close() }()

	ctx := context.Background()
	_, _ = hub.UpsertDoc(ctx, store.DocRecord{Project: "riot", RelPath: "arch.md", Title: "Architecture", Content: "# Arch\nLive spec"})
	_, _ = hub.UpsertTask(ctx, store.TaskRecord{Code: "P0-1", Project: "riot", Milestone: "Act 1", Title: "Fix prod race", Priority: "p0", Status: "todo"})
	_, _ = hub.UpsertTask(ctx, store.TaskRecord{Code: "P0-2", Project: "riot", Milestone: "Act 1", Title: "Done P0", Priority: "p0", Status: "done"})

	// Initialize gamification schema and award XP to populate Rank/XP/Combo in SQLite.
	eng, err := gamification.NewEngine(hub, gamification.TelemetryHook{})
	if err != nil {
		t.Fatalf("init gamification: %v", err)
	}
	_, _ = eng.Award(ctx, "task_done", "Cleared P0-2", 100, "P0-2", true)
	_, _ = eng.Award(ctx, "task_done", "Combo action", 100, "P0-X", true)

	var step atomic.Int32
	var seenAgent string
	var seenSystemPrompt string
	fakeGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAgent = r.Header.Get("X-MW-Agent")
		bodyBytes, _ := io.ReadAll(r.Body)
		var reqPayload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(bodyBytes, &reqPayload)
		if len(reqPayload.Messages) > 0 && reqPayload.Messages[0].Role == "system" {
			seenSystemPrompt = reqPayload.Messages[0].Content
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-MW-Echo-Model", "free-test-model")
		if step.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model": "free-test-model",
				"choices": []map[string]any{
					{
						"message": map[string]any{
							"role":    "assistant",
							"content": "",
							"tool_calls": []map[string]any{
								{
									"id":   "call_1",
									"type": "function",
									"function": map[string]any{
										"name":      "create_task",
										"arguments": `{"code":"SIDE-1","project":"riot","title":"Smoke Sidekick"}`,
									},
								},
							},
						},
					},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "free-test-model",
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "Mission SIDE-1 armed and queued."}},
			},
		})
	}))
	defer fakeGateway.Close()

	var awardedRef string
	svc, err := assistant.New(hub, assistant.Options{
		GatewayURL: fakeGateway.URL,
		Hooks: assistant.Hooks{
			GetRouterSavings: func(context.Context) (int64, float64) { return 4200, 0.0126 },
			AwardXP: func(_ context.Context, _, _ string, _ int, refID string) {
				awardedRef = refID
			},
		},
	})
	if err != nil {
		t.Fatalf("init assistant: %v", err)
	}

	live := svc.BuildLiveContext(ctx)
	if live.P0TasksPending != 1 || live.DocsCount != 1 || live.RouterTokensSaved != 4200 || live.ComboMult != 2.0 {
		t.Fatalf("unexpected live context: %+v", live)
	}
	if !strings.HasPrefix(live.Header, "[RANK: ") || !strings.Contains(live.Header, "COMBO: x2") || !strings.Contains(live.Header, "BOSS: 50% HP]") {
		t.Fatalf("unexpected live context header: %q", live.Header)
	}

	reply, err := svc.Chat(ctx, "tactician", "Create a smoke task SIDE-1", "")
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if seenAgent != "claude-code" {
		t.Fatalf("expected tactician to route via claude-code agent profile, got %q", seenAgent)
	}
	if !strings.Contains(seenSystemPrompt, live.Header) ||
		!strings.Contains(seenSystemPrompt, "p0_pending=1") ||
		!strings.Contains(seenSystemPrompt, "docs_count=1") ||
		!strings.Contains(seenSystemPrompt, "9router_tokens_saved=4200") {
		t.Fatalf("system prompt missing live context injection: %s", seenSystemPrompt)
	}
	if reply.ToolSummary != "create_task" || reply.Content != "Mission SIDE-1 armed and queued." {
		t.Fatalf("unexpected reply: %+v", reply)
	}

	task, err := hub.GetTask(ctx, "SIDE-1")
	if err != nil || task.Title != "Smoke Sidekick" {
		t.Fatalf("expected SIDE-1 task created in SQLite, err=%v task=%+v", err, task)
	}
	_ = awardedRef
}

func TestAssistantRoutesAndContract(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	defer func() { _ = hub.Close() }()

	fakeGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "mock-brief",
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "- Clear P0\n- Defeat Boss\n- Keep Streak"}},
			},
		})
	}))
	defer fakeGateway.Close()

	svc, err := assistant.New(hub, assistant.Options{GatewayURL: fakeGateway.URL})
	if err != nil {
		t.Fatalf("init assistant: %v", err)
	}

	e := echo.New()
	svc.RegisterRoutes(e.Group("/api"))

	contractRec := httptest.NewRecorder()
	e.ServeHTTP(contractRec, httptest.NewRequest(http.MethodGet, "/api/assistant/contract", nil))
	if contractRec.Code != http.StatusOK {
		t.Fatalf("GET /api/assistant/contract failed: %d %s", contractRec.Code, contractRec.Body.String())
	}
	var contract assistant.UIContract
	if err := json.Unmarshal(contractRec.Body.Bytes(), &contract); err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	if contract.Shortcut != "Ctrl+K" || contract.DrawerStyle != "comic-speech-bubble" || !contract.DailyMissionBriefWidget {
		t.Fatalf("unexpected UI contract: %+v", contract)
	}

	ctxRec := httptest.NewRecorder()
	e.ServeHTTP(ctxRec, httptest.NewRequest(http.MethodGet, "/api/assistant/context", nil))
	if ctxRec.Code != http.StatusOK || !strings.Contains(ctxRec.Body.String(), "[RANK:") {
		t.Fatalf("GET /api/assistant/context failed: %d %s", ctxRec.Code, ctxRec.Body.String())
	}

	briefReq := httptest.NewRequest(http.MethodPost, "/api/assistant/brief", bytes.NewBufferString(`{"persona":"oracle"}`))
	briefReq.Header.Set("Content-Type", "application/json")
	briefRec := httptest.NewRecorder()
	e.ServeHTTP(briefRec, briefReq)
	if briefRec.Code != http.StatusOK || !strings.Contains(briefRec.Body.String(), "Clear P0") {
		t.Fatalf("POST /api/assistant/brief failed: %d %s", briefRec.Code, briefRec.Body.String())
	}
}
