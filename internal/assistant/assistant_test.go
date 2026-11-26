package assistant_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/assistant"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestAssistantPersonaRoutingAndToolCalling(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	defer func() { _ = hub.Close() }()

	var step atomic.Int32
	var seenAgent string
	fakeGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAgent = r.Header.Get("X-MW-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-MW-Echo-Model", "free-test-model")
		current := step.Add(1)
		if current == 1 {
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

	svc, err := assistant.New(hub, assistant.Options{GatewayURL: fakeGateway.URL})
	if err != nil {
		t.Fatalf("init assistant: %v", err)
	}

	reply, err := svc.Chat(context.Background(), "tactician", "Create a smoke task SIDE-1", "")
	if err != nil {
		t.Fatalf("chat failed: %v", err)
	}
	if seenAgent != "claude-code" {
		t.Fatalf("expected tactician to route via claude-code agent profile, got %q", seenAgent)
	}
	if reply.ToolSummary != "create_task" || reply.Content != "Mission SIDE-1 armed and queued." {
		t.Fatalf("unexpected reply: %+v", reply)
	}

	task, err := hub.GetTask(context.Background(), "SIDE-1")
	if err != nil || task.Title != "Smoke Sidekick" {
		t.Fatalf("expected SIDE-1 task created in SQLite, err=%v task=%+v", err, task)
	}
}
