package observatory_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/observatory"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func TestOTLPIngestStatsDiscoveryAndRepoRadar(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	defer func() { _ = hub.Close() }()

	obs, err := observatory.New(hub)
	if err != nil {
		t.Fatalf("init observatory: %v", err)
	}

	app := echo.New()
	api := app.Group("/api")
	obs.RegisterRoutes(app, api)

	// 1. Ingest OTLP trace span with token telemetry
	tracePayload := `{
		"resourceSpans": [{
			"resource": {"attributes": [{"key":"service.name","value":{"stringValue":"claude-code-agent"}}]},
			"scopeSpans": [{
				"spans": [{
					"traceId": "abc123",
					"spanId": "span001",
					"name": "llm.chat.completions",
					"startTimeUnixNano": "1700000000000000000",
					"endTimeUnixNano": "1700000000250000000",
					"attributes": [
						{"key":"gen_ai.agent.name","value":{"stringValue":"claude-code"}},
						{"key":"gen_ai.usage.input_tokens","value":{"intValue":"120"}},
						{"key":"gen_ai.usage.output_tokens","value":{"intValue":"45"}}
					]
				}]
			}]
		}]
	}`
	traceReq := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(tracePayload))
	traceReq.Header.Set("Content-Type", "application/json")
	traceRec := httptest.NewRecorder()
	app.ServeHTTP(traceRec, traceReq)
	if traceRec.Code != http.StatusOK || !strings.Contains(traceRec.Body.String(), `"accepted":1`) {
		t.Fatalf("POST /v1/traces failed: %d %s", traceRec.Code, traceRec.Body.String())
	}

	// Verify /api/observatory/stats and /api/observatory/events
	statsRec := httptest.NewRecorder()
	app.ServeHTTP(statsRec, httptest.NewRequest(http.MethodGet, "/api/observatory/stats", nil))
	if statsRec.Code != http.StatusOK || !strings.Contains(statsRec.Body.String(), `"totalTokensIn":120`) {
		t.Fatalf("GET /api/observatory/stats failed: %d %s", statsRec.Code, statsRec.Body.String())
	}

	eventsRec := httptest.NewRecorder()
	app.ServeHTTP(eventsRec, httptest.NewRequest(http.MethodGet, "/api/observatory/events?kind=trace", nil))
	if eventsRec.Code != http.StatusOK || !strings.Contains(eventsRec.Body.String(), `"claude-code-agent"`) {
		t.Fatalf("GET /api/observatory/events failed: %d %s", eventsRec.Code, eventsRec.Body.String())
	}

	// 2. Localhost service probe discovery against ephemeral HTTP test server
	fakeSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer fakeSvc.Close()

	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(fakeSvc.URL, "http://"))
	port, _ := strconv.Atoi(portStr)
	found := observatory.DiscoverServices(context.Background(), []observatory.ServiceProbe{
		{Port: port, Name: "Test Agent Endpoint", Category: "ai", Protocol: "http+openai", HealthURI: "/health"},
	})
	if len(found) != 1 || found[0].Name != "Test Agent Endpoint" || !strings.Contains(found[0].Detail, "HTTP 200") {
		t.Fatalf("service discovery failed: %+v", found)
	}

	// 3. Repo Radar scan on synthetic root with two git repos
	rootDir := t.TempDir()
	repoDir := filepath.Join(rootDir, "sample-project")
	_ = os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	_ = os.MkdirAll(filepath.Join(repoDir, "docs", "tasks"), 0o755)
	_ = os.WriteFile(filepath.Join(repoDir, ".git", "HEAD"), []byte("ref: refs/heads/dev\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repoDir, ".git", "config"),
		[]byte("[remote \"origin\"]\n\turl = https://github.com/example/sample-project.git\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repoDir, "docs", "tasks", "tasks.json"), []byte(`{}`), 0o644)

	repos, err := obs.ScanRepos(context.Background(), []string{rootDir})
	if err != nil {
		t.Fatalf("scan repos: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "sample-project" || repos[0].Branch != "dev" || !repos[0].HasTasksJSON ||
		!repos[0].HasDocs {
		t.Fatalf("unexpected repo radar output: %+v", repos)
	}
}
