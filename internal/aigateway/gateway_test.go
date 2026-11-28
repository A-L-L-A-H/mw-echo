package aigateway_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/aigateway"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func setupTestGateway(t *testing.T) (*store.HubStore, *aigateway.Gateway, *echo.Echo) {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	gw, err := aigateway.NewGateway(hub)
	if err != nil {
		t.Fatalf("init ai gateway: %v", err)
	}

	app := echo.New()
	apiGroup := app.Group("/api")
	gw.RegisterRoutes(app, apiGroup)
	return hub, gw, app
}

func doJSONRequest(t *testing.T, handler http.Handler, method, target string, payload any,
	headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}
	httpReq := httptest.NewRequest(method, target, bodyReader)
	httpReq.Header.Set("Content-Type", "application/json")
	for key, val := range headers {
		httpReq.Header.Set(key, val)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httpReq)
	return recorder
}

func TestRTKCompressionSavesOver25PercentTokens(t *testing.T) {
	var logBuilder strings.Builder
	logBuilder.WriteString("\x1b[32m==================================================\x1b[0m\n")
	logBuilder.WriteString("diff --git a/internal/app.go b/internal/app.go\n")
	logBuilder.WriteString("index 1a2b3c4d..9e8f7a6b 100644\n")
	logBuilder.WriteString("old mode 100644\nnew mode 100755\n")
	for idx := 0; idx < 45; idx++ {
		logBuilder.WriteString(fmt.Sprintf("\x1b[36m=== RUN   TestWorkerCase_%02d\x1b[0m\n", idx))
		logBuilder.WriteString(fmt.Sprintf("\x1b[32m--- PASS: TestWorkerCase_%02d (0.00s)\x1b[0m\n", idx))
	}
	for idx := 0; idx < 25; idx++ {
		logBuilder.WriteString("\x1b[33m2026-11-26T12:00:01Z INFO polling worker queue...\x1b[0m\n\n\n")
	}
	logBuilder.WriteString("\x1b[31m--------------------------------------------------\x1b[0m\n")
	logBuilder.WriteString("\x1b[31mERROR: failed to bind listener on port 8080: address already in use\x1b[0m\n")

	noisyLog := logBuilder.String()
	rawTokens := len(noisyLog) / 4

	// 1. OpenAI role == "tool" message
	toolMessages := []aigateway.ChatMessage{
		{Role: "user", Content: "Inspect the test failure."},
		{Role: "tool", ToolCallID: "call_01", Content: noisyLog},
	}
	compressedMsgs, tokensSaved := aigateway.CompressMessages(toolMessages)
	if len(compressedMsgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(compressedMsgs))
	}
	compressedTool, ok := compressedMsgs[1].Content.(string)
	if !ok {
		t.Fatalf("expected string tool content")
	}
	if strings.Contains(compressedTool, "\x1b[") {
		t.Fatalf("ANSI escape codes were not stripped: %q", compressedTool)
	}
	if !strings.Contains(compressedTool, "ERROR: failed to bind listener on port 8080") {
		t.Fatalf("critical tail error line was lost during compression: %q", compressedTool)
	}
	if !strings.Contains(compressedTool, "[x25 repeated]") {
		t.Fatalf("expected consecutive repeated line deduplication marker, got: %q", compressedTool)
	}
	savingsPct := float64(tokensSaved) / float64(rawTokens) * 100.0
	if savingsPct <= 25.0 {
		t.Fatalf("expected >25%% token savings, got %.1f%% (saved=%d raw=%d)", savingsPct, tokensSaved, rawTokens)
	}

	// 2. Anthropic tool_result content block
	anthToolMessages := []aigateway.ChatMessage{
		{
			Role: "user",
			Content: []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_01",
					"content":     noisyLog,
				},
			},
		},
	}
	anthCompressed, anthSaved := aigateway.CompressMessages(anthToolMessages)
	if anthSaved <= rawTokens/4 {
		t.Fatalf("expected >25%% savings on Anthropic tool_result block, got saved=%d raw=%d", anthSaved, rawTokens)
	}
	blocks, ok := anthCompressed[0].Content.([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("expected compressed Anthropic content blocks")
	}
	blockMap := blocks[0].(map[string]any)
	blockContent, _ := blockMap["content"].(string)
	if strings.Contains(blockContent, "\x1b[") || !strings.Contains(blockContent, "ERROR: failed to bind listener") {
		t.Fatalf("Anthropic tool_result compression invalid: %q", blockContent)
	}
}

func TestFailoverOn429SetsCooldownAndRoutesToBackup(t *testing.T) {
	_, _, app := setupTestGateway(t)

	var primaryHits atomic.Int32
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer primaryServer.Close()

	var backupHits atomic.Int32
	backupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupHits.Add(1)
		if r.Header.Get("Authorization") != "Bearer backup-secret-key" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-backup-1",
			"object":"chat.completion",
			"created":1730000000,
			"model":"llama-3.3-70b-versatile",
			"choices":[{"index":0,"message":{"role":"assistant","content":"served by backup"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":18,"completion_tokens":7,"total_tokens":25}
		}`))
	}))
	defer backupServer.Close()

	// Register primary (free, priority 10) and backup (cheap, priority 20)
	primaryRec := doJSONRequest(t, app, http.MethodPost, "/api/ai/providers", map[string]any{
		"name":         "primary-free",
		"kind":         "openai_compat",
		"tier":         "free",
		"baseUrl":      primaryServer.URL + "/v1",
		"apiKey":       "primary-secret-key",
		"defaultModel": "llama-3.3-70b-versatile",
		"models":       []string{"llama-3.3-70b-versatile"},
		"priority":     10,
	}, nil)
	if primaryRec.Code != http.StatusCreated {
		t.Fatalf("create primary provider failed: %d %s", primaryRec.Code, primaryRec.Body.String())
	}

	backupRec := doJSONRequest(t, app, http.MethodPost, "/api/ai/providers", map[string]any{
		"name":         "backup-cheap",
		"kind":         "openai_compat",
		"tier":         "cheap",
		"baseUrl":      backupServer.URL + "/v1",
		"apiKey":       "backup-secret-key",
		"defaultModel": "llama-3.3-70b-versatile",
		"models":       []string{"llama-3.3-70b-versatile"},
		"priority":     20,
	}, nil)
	if backupRec.Code != http.StatusCreated {
		t.Fatalf("create backup provider failed: %d %s", backupRec.Code, backupRec.Body.String())
	}

	// Request 1: primary returns 429 -> cooldown set -> automatic failover to backup
	highTemp := 0.7
	firstRec := doJSONRequest(t, app, http.MethodPost, "/v1/chat/completions", map[string]any{
		"model":       "llama-3.3-70b-versatile",
		"temperature": highTemp,
		"messages":    []map[string]string{{"role": "user", "content": "hello failover"}},
	}, nil)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover, got %d: %s", firstRec.Code, firstRec.Body.String())
	}
	if firstRec.Header().Get("X-MW-Echo-Provider") != "backup-cheap" {
		t.Fatalf("expected X-MW-Echo-Provider=backup-cheap, got %q", firstRec.Header().Get("X-MW-Echo-Provider"))
	}
	if !strings.Contains(firstRec.Body.String(), "served by backup") {
		t.Fatalf("unexpected body: %s", firstRec.Body.String())
	}
	if primaryHits.Load() != 1 || backupHits.Load() != 1 {
		t.Fatalf("expected primary=1 backup=1 hits, got primary=%d backup=%d", primaryHits.Load(), backupHits.Load())
	}

	// Verify primary provider is marked in cooldown via GET /api/ai/providers
	listRec := doJSONRequest(t, app, http.MethodGet, "/api/ai/providers", nil, nil)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list providers failed: %d", listRec.Code)
	}
	var listEnvelope struct {
		Providers []aigateway.Provider `json:"providers"`
	}
	_ = json.Unmarshal(listRec.Body.Bytes(), &listEnvelope)
	foundCooldown := false
	for _, provider := range listEnvelope.Providers {
		if provider.Name == "primary-free" && provider.InCooldown && provider.CooldownUntil != "" {
			foundCooldown = true
		}
	}
	if !foundCooldown {
		t.Fatalf("expected primary-free provider to be marked inCooldown, got: %s", listRec.Body.String())
	}

	// Request 2 (distinct prompt): primary MUST be skipped completely because it is in 60s cooldown
	secondRec := doJSONRequest(t, app, http.MethodPost, "/v1/chat/completions", map[string]any{
		"model":       "llama-3.3-70b-versatile",
		"temperature": highTemp,
		"messages":    []map[string]string{{"role": "user", "content": "second request during cooldown"}},
	}, nil)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second request failed: %d %s", secondRec.Code, secondRec.Body.String())
	}
	if primaryHits.Load() != 1 {
		t.Fatalf("primary should have been skipped while in cooldown, got %d hits", primaryHits.Load())
	}
	if backupHits.Load() != 2 {
		t.Fatalf("expected backup hits=2, got %d", backupHits.Load())
	}
}

func TestExactPromptCacheServesSecondRequestWithZeroUpstreamCalls(t *testing.T) {
	_, _, app := setupTestGateway(t)

	var upstreamHits atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-cache-1",
			"object":"chat.completion",
			"created":1730000100,
			"model":"gpt-oss-120b",
			"choices":[{"index":0,"message":{"role":"assistant","content":"deterministic answer 42"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":30,"completion_tokens":12,"total_tokens":42}
		}`))
	}))
	defer upstreamServer.Close()

	createRec := doJSONRequest(t, app, http.MethodPost, "/api/ai/providers", map[string]any{
		"name":         "cache-test-provider",
		"kind":         "openai_compat",
		"tier":         "free",
		"baseUrl":      upstreamServer.URL + "/v1",
		"defaultModel": "gpt-oss-120b",
	}, nil)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create provider failed: %d", createRec.Code)
	}

	reqBody := map[string]any{
		"model":       "gpt-oss-120b",
		"temperature": 0.1,
		"messages":    []map[string]string{{"role": "user", "content": "What is 6 * 7?"}},
	}

	firstRec := doJSONRequest(t, app, http.MethodPost, "/v1/chat/completions", reqBody, nil)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first completion failed: %d %s", firstRec.Code, firstRec.Body.String())
	}
	if firstRec.Header().Get("X-MW-Echo-Cache") != "MISS" {
		t.Fatalf("expected X-MW-Echo-Cache=MISS on first call, got %q", firstRec.Header().Get("X-MW-Echo-Cache"))
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("expected 1 upstream call, got %d", upstreamHits.Load())
	}

	// Second identical request MUST be served from SQLite cache with 0 additional upstream calls
	secondRec := doJSONRequest(t, app, http.MethodPost, "/v1/chat/completions", reqBody, nil)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second completion failed: %d %s", secondRec.Code, secondRec.Body.String())
	}
	if secondRec.Header().Get("X-MW-Echo-Cache") != "HIT" {
		t.Fatalf("expected X-MW-Echo-Cache=HIT on second call, got %q", secondRec.Header().Get("X-MW-Echo-Cache"))
	}
	if !strings.Contains(secondRec.Body.String(), "deterministic answer 42") {
		t.Fatalf("cached body mismatch: %s", secondRec.Body.String())
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("expected 0 new upstream calls on cache HIT, total hits=%d", upstreamHits.Load())
	}

	// Verify stats reflect cache hit + cache tokens saved, and DELETE /api/ai/cache purges cache
	statsRec := doJSONRequest(t, app, http.MethodGet, "/api/ai/stats", nil, nil)
	if statsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/ai/stats failed: %d", statsRec.Code)
	}
	var stats aigateway.GatewayStats
	_ = json.Unmarshal(statsRec.Body.Bytes(), &stats)
	if stats.CacheHits != 1 || stats.CacheTokensSaved != 42 || stats.TotalRequests != 2 {
		t.Fatalf("unexpected stats: total=%d cacheHits=%d cacheTokensSaved=%d", stats.TotalRequests, stats.CacheHits,
			stats.CacheTokensSaved)
	}

	purgeRec := doJSONRequest(t, app, http.MethodDelete, "/api/ai/cache", nil, nil)
	if purgeRec.Code != http.StatusOK || !strings.Contains(purgeRec.Body.String(), `"purged":1`) {
		t.Fatalf("cache purge failed: %d %s", purgeRec.Code, purgeRec.Body.String())
	}

	thirdRec := doJSONRequest(t, app, http.MethodPost, "/v1/chat/completions", reqBody, nil)
	if thirdRec.Header().Get("X-MW-Echo-Cache") != "MISS" || upstreamHits.Load() != 2 {
		t.Fatalf("expected cache MISS and upstream hit after purge, header=%q hits=%d",
			thirdRec.Header().Get("X-MW-Echo-Cache"), upstreamHits.Load())
	}
}

func TestAnthropicMessagesTranslatesClaudeCodeViaMagpieRoute(t *testing.T) {
	_, _, app := setupTestGateway(t)

	var capturedOpenAIReq aigateway.ChatCompletionRequest
	openAIBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		rawBody, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(rawBody, &capturedOpenAIReq)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-magpie-99",
			"object":"chat.completion",
			"created":1730000200,
			"model":"qwen/qwen3-coder:free",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Refactored cleanly via Magpie!"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":44,"completion_tokens":11,"total_tokens":55}
		}`))
	}))
	defer openAIBackend.Close()

	createRec := doJSONRequest(t, app, http.MethodPost, "/api/ai/providers", map[string]any{
		"name":         "openrouter-free",
		"kind":         "openai_compat",
		"tier":         "free",
		"baseUrl":      openAIBackend.URL + "/v1",
		"apiKey":       "sk-or-test-key",
		"defaultModel": "qwen/qwen3-coder:free",
		"models":       []string{"qwen/qwen3-coder:free"},
	}, nil)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create provider failed: %d", createRec.Code)
	}

	// Configure Magpie claude-code switchboard to reroute Claude Code onto qwen/qwen3-coder:free
	routeRec := doJSONRequest(t, app, http.MethodPut, "/api/ai/agents/claude-code", map[string]any{
		"targetModel": "qwen/qwen3-coder:free",
		"tierOrder":   "free,cheap,subscription,paid",
		"rtkEnabled":  true,
	}, nil)
	if routeRec.Code != http.StatusOK {
		t.Fatalf("update claude-code route failed: %d %s", routeRec.Code, routeRec.Body.String())
	}

	// Send native Anthropic Messages request as Claude Code
	anthRec := doJSONRequest(t, app, http.MethodPost, "/v1/messages", map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"system":     "You are Claude Code.",
		"max_tokens": 512,
		"messages": []map[string]any{
			{"role": "user", "content": "Explain Magpie routing in one line."},
		},
	}, nil)
	if anthRec.Code != http.StatusOK {
		t.Fatalf("POST /v1/messages failed: %d %s", anthRec.Code, anthRec.Body.String())
	}

	// Verify backend received translated OpenAI request with Magpie target model + system prompt
	if capturedOpenAIReq.Model != "qwen/qwen3-coder:free" {
		t.Fatalf("expected Magpie claude-code route model qwen/qwen3-coder:free, got %q", capturedOpenAIReq.Model)
	}
	if len(capturedOpenAIReq.Messages) != 2 || capturedOpenAIReq.Messages[0].Role != "system" || capturedOpenAIReq.Messages[1].Role != "user" {
		t.Fatalf("unexpected translated OpenAI messages: %+v", capturedOpenAIReq.Messages)
	}

	// Verify Anthropic Messages response shape
	var anthResp aigateway.AnthropicMessageResponse
	if err := json.Unmarshal(anthRec.Body.Bytes(), &anthResp); err != nil {
		t.Fatalf("unmarshal anthropic response: %v", err)
	}
	if anthResp.Type != "message" || anthResp.Role != "assistant" {
		t.Fatalf("invalid anthropic response envelope: %+v", anthResp)
	}
	if len(anthResp.Content) != 1 || anthResp.Content[0].Type != "text" || anthResp.Content[0].Text != "Refactored cleanly via Magpie!" {
		t.Fatalf("unexpected anthropic content blocks: %+v", anthResp.Content)
	}
	if anthResp.Usage.InputTokens != 44 || anthResp.Usage.OutputTokens != 11 {
		t.Fatalf("unexpected anthropic usage: %+v", anthResp.Usage)
	}
	if anthRec.Header().Get("X-MW-Agent") != "claude-code" {
		t.Fatalf("expected X-MW-Agent=claude-code, got %q", anthRec.Header().Get("X-MW-Agent"))
	}
}

func TestFreeLLMAPISnifferDetectsNewSHAAndIndexesModels(t *testing.T) {
	_, gw, app := setupTestGateway(t)

	currentSHA := "abc111222333444555666777888999000aaabbb1"
	upstreamMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/tashfeenahmed/freellmapi/commits/main":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"sha":%q}`, currentSHA)
		case "/tashfeenahmed/freellmapi/main/server/src/providers/index.ts":
			_, _ = w.Write([]byte(`
register(new OpenAICompatProvider({
  platform: 'groq',
  name: 'Groq',
  baseUrl: 'https://api.groq.com/openai/v1',
}));
register(new OpenAICompatProvider({
  platform: 'powertokens',
  name: 'PowerTokens',
  baseUrl: 'https://api.powertokens.ai/v1',
}));
`))
		case "/tashfeenahmed/freellmapi/main/server/src/db/model-pricing.ts":
			_, _ = w.Write([]byte(`
export const MODEL_PRICING = [
  ['groq', 'llama-4-maverick-99b-sniffed', 0.10, 0.30],
  ['powertokens', 'glm-4.7-flash-sniffed', 0.06, 0.40],
];
`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamMock.Close()

	gw.GitHubAPIBase = upstreamMock.URL
	gw.RawBase = upstreamMock.URL

	// First sniff detects brand-new SHA and indexes upstream models
	firstRec := doJSONRequest(t, app, http.MethodPost, "/api/ai/catalog/sniff", nil, nil)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("POST /api/ai/catalog/sniff failed: %d %s", firstRec.Code, firstRec.Body.String())
	}
	var firstResult aigateway.SniffResult
	_ = json.Unmarshal(firstRec.Body.Bytes(), &firstResult)
	if !firstResult.Updated || firstResult.UpstreamSHA != currentSHA || firstResult.PreviousSHA != "" {
		t.Fatalf("unexpected first sniff result: %+v", firstResult)
	}
	if firstResult.ProvidersFound < 10 || firstResult.ModelsIndexed < 35 {
		t.Fatalf("expected merged seed + upstream catalog counts, got providers=%d models=%d", firstResult.ProvidersFound,
			firstResult.ModelsIndexed)
	}

	// Verify searchable catalog endpoint finds newly sniffed models
	searchRec := doJSONRequest(t, app, http.MethodGet, "/api/ai/catalog?q=glm-4.7-flash-sniffed&freeOnly=true", nil, nil)
	if searchRec.Code != http.StatusOK || !strings.Contains(searchRec.Body.String(), "https://api.powertokens.ai/v1") {
		t.Fatalf("sniffed model not indexed in catalog: %d %s", searchRec.Code, searchRec.Body.String())
	}

	// Verify GET /v1/models exposes agent aliases + sniffed catalog models
	modelsRec := doJSONRequest(t, app, http.MethodGet, "/v1/models", nil, nil)
	if modelsRec.Code != http.StatusOK || !strings.Contains(modelsRec.Body.String(), `"agent/claude-code"`) || !strings.Contains(modelsRec.Body.String(), `"glm-4.7-flash-sniffed"`) {
		t.Fatalf("GET /v1/models missing expected entries: %d %s", modelsRec.Code, modelsRec.Body.String())
	}

	// Second sniff on same SHA reports updated=false
	secondRec := doJSONRequest(t, app, http.MethodPost, "/api/ai/catalog/sniff", nil, nil)
	var secondResult aigateway.SniffResult
	_ = json.Unmarshal(secondRec.Body.Bytes(), &secondResult)
	if secondResult.Updated || secondResult.PreviousSHA != currentSHA || secondResult.UpstreamSHA != currentSHA {
		t.Fatalf("expected updated=false when SHA unchanged, got: %+v", secondResult)
	}

	// Third sniff after upstream advances SHA reports updated=true
	nextSHA := "def999888777666555444333222111000cccdde2"
	currentSHA = nextSHA
	thirdResult, err := gw.SniffUpstream(context.Background())
	if err != nil {
		t.Fatalf("third sniff failed: %v", err)
	}
	if !thirdResult.Updated || thirdResult.PreviousSHA != "abc111222333444555666777888999000aaabbb1" ||
		thirdResult.UpstreamSHA != nextSHA {
		t.Fatalf("expected updated=true on new SHA, got: %+v", thirdResult)
	}
}

func TestProviderAPIKeysEncryptedAtRestAndNeverLeaked(t *testing.T) {
	hub, _, app := setupTestGateway(t)

	secretAPIKey := "sk-live-super-secret-never-leak-987654321"
	createRec := doJSONRequest(t, app, http.MethodPost, "/api/ai/providers", map[string]any{
		"name":         "groq-secure",
		"kind":         "openai_compat",
		"tier":         "free",
		"baseUrl":      "https://api.groq.com/openai/v1",
		"apiKey":       secretAPIKey,
		"defaultModel": "llama-3.3-70b-versatile",
	}, nil)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create provider failed: %d %s", createRec.Code, createRec.Body.String())
	}
	if strings.Contains(createRec.Body.String(), secretAPIKey) {
		t.Fatalf("API key leaked in POST /api/ai/providers response!")
	}

	var created aigateway.Provider
	_ = json.Unmarshal(createRec.Body.Bytes(), &created)
	if !created.HasAPIKey {
		t.Fatalf("expected hasApiKey=true")
	}

	// Confirm GET /api/ai/providers never leaks plaintext or ciphertext
	listRec := doJSONRequest(t, app, http.MethodGet, "/api/ai/providers", nil, nil)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/ai/providers failed: %d", listRec.Code)
	}
	if strings.Contains(listRec.Body.String(), secretAPIKey) || strings.Contains(listRec.Body.String(), "api_key_enc") {
		t.Fatalf("API key or encrypted blob leaked in GET /api/ai/providers: %s", listRec.Body.String())
	}

	// Confirm SQLite row stores encrypted ciphertext, NOT plaintext
	var storedCipher string
	err := hub.DB().QueryRow(`SELECT api_key_enc FROM ai_providers WHERE id = ?`, created.ID).Scan(&storedCipher)
	if err != nil {
		t.Fatalf("query stored api_key_enc: %v", err)
	}
	if storedCipher == "" || storedCipher == secretAPIKey || strings.Contains(storedCipher, secretAPIKey) {
		t.Fatalf("API key is not encrypted at rest in SQLite: %q", storedCipher)
	}

	// Confirm settings update & provider deletion work cleanly
	settingsRec := doJSONRequest(t, app, http.MethodPut, "/api/ai/settings", map[string]any{
		"rtkEnabled":    false,
		"cacheEnabled":  true,
		"fallbackOrder": "cheap,free,paid",
	}, nil)
	if settingsRec.Code != http.StatusOK || !strings.Contains(settingsRec.Body.String(), `"fallbackOrder":"cheap,free,paid"`) {
		t.Fatalf("PUT /api/ai/settings failed: %d %s", settingsRec.Code, settingsRec.Body.String())
	}

	delRec := doJSONRequest(t, app, http.MethodDelete, "/api/ai/providers/"+created.ID, nil, nil)
	if delRec.Code != http.StatusOK || !strings.Contains(delRec.Body.String(), `"deleted":true`) {
		t.Fatalf("DELETE /api/ai/providers/:id failed: %d %s", delRec.Code, delRec.Body.String())
	}
}

func TestCavemanCompressionAndHeaderMode(t *testing.T) {
	verbosePrompt := strings.Join([]string{
		"Halo, selamat pagi! Tolong bantu saya sebenarnya pada dasarnya ini adalah sebuah permintaan yang sangat panjang sekali dong deh nih sih.",
		"Could you please kindly help me actually basically literally refactor the very verbose configuration object that is really quite simple?",
		"{",
		"    \"service\": \"mw-echo\",",
		"    \"mode\": \"caveman\",",
		"    \"retries\": 3,",
		"    \"endpoints\": [",
		"        \"/v1/chat/completions\",",
		"        \"/api/radar/extract-markdown\"",
		"    ]",
		"}",
		"Thank you very much! Terima kasih banyak ya!",
	}, "\n")

	rawTokens := len(verbosePrompt) / 4
	compressedMsgs, savedTokens := aigateway.CompressCaveman([]aigateway.ChatMessage{
		{Role: "user", Content: verbosePrompt},
	})
	if len(compressedMsgs) != 1 {
		t.Fatalf("expected 1 compressed message, got %d", len(compressedMsgs))
	}
	compressedText, ok := compressedMsgs[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content")
	}
	if savedTokens <= 0 {
		t.Fatalf("expected positive token savings from CompressCaveman, got %d", savedTokens)
	}
	savingsPct := float64(savedTokens) / float64(rawTokens) * 100.0
	if savingsPct < 30.0 {
		t.Fatalf("expected >=30%% token savings in caveman mode, got %.1f%% (saved=%d raw=%d, out=%q)", savingsPct,
			savedTokens, rawTokens, compressedText)
	}
	if !strings.Contains(compressedText, `{"service":"mw-echo"`) {
		t.Fatalf("expected compacted JSON in caveman output, got: %q", compressedText)
	}

	// Verify HTTP header X-MW-RTK-Mode: caveman and query ?rtk=caveman activate CompressCaveman.
	_, gw, app := setupTestGateway(t)
	var receivedPrompt string
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body aigateway.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && len(body.Messages) > 0 {
			if text, isStr := body.Messages[0].Content.(string); isStr {
				receivedPrompt = text
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-caveman","object":"chat.completion","model":"mock-caveman","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`))
	}))
	defer mockUpstream.Close()

	_, err := gw.AddProvider(context.Background(), aigateway.ProviderCreateInput{
		Name:         "caveman-mock",
		Kind:         aigateway.KindOpenAICompat,
		Tier:         aigateway.TierFree,
		BaseURL:      mockUpstream.URL,
		DefaultModel: "mock-caveman",
		Models:       []string{"mock-caveman"},
	})
	if err != nil {
		t.Fatalf("add provider: %v", err)
	}

	reqBody := aigateway.ChatCompletionRequest{
		Model:    "mock-caveman",
		Messages: []aigateway.ChatMessage{{Role: "user", Content: verbosePrompt}},
	}
	recHeader := doJSONRequest(t, app, http.MethodPost, "/v1/chat/completions", reqBody, map[string]string{
		"X-MW-RTK-Mode": "caveman",
	})
	if recHeader.Code != http.StatusOK {
		t.Fatalf("expected 200 for caveman header request, got %d: %s", recHeader.Code, recHeader.Body.String())
	}
	savedHeader := recHeader.Header().Get("X-MW-Echo-RTK-Saved")
	if savedHeader == "" || savedHeader == "0" {
		t.Fatalf("expected non-zero X-MW-Echo-RTK-Saved header, got %q", savedHeader)
	}
	if len(receivedPrompt) >= len(verbosePrompt) {
		t.Fatalf("expected upstream prompt to be compressed, raw=%d sent=%d", len(verbosePrompt), len(receivedPrompt))
	}

	// Query param ?rtk=caveman should also work.
	recQuery := doJSONRequest(t, app, http.MethodPost, "/v1/chat/completions?rtk=caveman", reqBody, nil)
	if recQuery.Code != http.StatusOK {
		t.Fatalf("expected 200 for ?rtk=caveman request, got %d: %s", recQuery.Code, recQuery.Body.String())
	}
	if savedQ := recQuery.Header().Get("X-MW-Echo-RTK-Saved"); savedQ == "" || savedQ == "0" {
		t.Fatalf("expected non-zero X-MW-Echo-RTK-Saved header for query mode, got %q", savedQ)
	}
}
