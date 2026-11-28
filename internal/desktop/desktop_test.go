package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func openTestHub(t *testing.T) *store.HubStore {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	return hub
}

func TestEvaluateActionTiers(t *testing.T) {
	safeEval := EvaluateAction("repo.read", "docs/architecture.md", "git status", "http://127.0.0.1:8080/health")
	if safeEval.ResolvedTier != RiskTierR0 || !safeEval.Allowed || safeEval.RequiresHumanApproval {
		t.Fatalf("expected R0 allowed evaluation, got %+v", safeEval)
	}

	r1Eval := EvaluateAction("worktree.write", "internal/desktop/risk_gate.go", "", "")
	if r1Eval.ResolvedTier != RiskTierR1 || !r1Eval.Allowed {
		t.Fatalf("expected R1 allowed evaluation, got %+v", r1Eval)
	}

	sensitivePaths := []string{
		".env",
		"config/.env.production",
		"/home/user/.ssh/id_rsa",
		"C:\\Users\\dev\\.aws\\credentials",
		"certs/server.pem",
		"secrets/signing.key",
		"../../etc/passwd",
	}
	for _, sensitivePath := range sensitivePaths {
		evalResult := EvaluateAction("repo.read", sensitivePath, "", "")
		if evalResult.ResolvedTier != RiskTierR4 || evalResult.Allowed {
			t.Fatalf("expected R4 blocked for sensitive path %q, got %+v", sensitivePath, evalResult)
		}
	}

	destructiveCommands := []string{
		"rm -rf /tmp/work",
		"format D:",
		"git push --force origin main",
		"printenv",
	}
	for _, destructiveCmd := range destructiveCommands {
		evalResult := EvaluateAction("pty.exec_local", "", destructiveCmd, "")
		if evalResult.ResolvedTier != RiskTierR4 || evalResult.Allowed {
			t.Fatalf("expected R4 blocked for destructive command %q, got %+v", destructiveCmd, evalResult)
		}
	}

	egressCommands := []string{
		"curl https://api.github.com/zen",
		"wget https://example.com/install.sh",
		"ssh deploy@10.0.0.5",
		"git push origin feature/desktop",
	}
	for _, egressCmd := range egressCommands {
		evalResult := EvaluateAction("pty.exec_local", "", egressCmd, "")
		if evalResult.ResolvedTier != RiskTierR3 || !evalResult.RequiresHumanApproval || evalResult.Allowed {
			t.Fatalf("expected R3 approval-required for egress command %q, got %+v", egressCmd, evalResult)
		}
	}

	nonLoopbackEval := EvaluateAction("repo.read", "", "", "https://example.com/v1/models")
	if nonLoopbackEval.ResolvedTier != RiskTierR3 || !nonLoopbackEval.RequiresHumanApproval {
		t.Fatalf("expected R3 for non-loopback endpoint URL, got %+v", nonLoopbackEval)
	}
}

func TestRedactSensitiveText(t *testing.T) {
	samples := []struct {
		name      string
		input     string
		secrets   []string
		forbidden []string
		expected  []string
	}{
		{
			name:      "auth_headers",
			input:     "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9_secretValue and Basic dXNlcjpwYXNz and Token tok_abcdef123456",
			forbidden: []string{"eyJhbGciOiJIUzI1NiJ9_secretValue", "dXNlcjpwYXNz", "tok_abcdef123456"},
			expected:  []string{"Bearer [REDACTED]", "Basic [REDACTED]", "Token [REDACTED]"},
		},
		{
			name:  "provider_keys",
			input: "keys: sk-proj-1234567890abcd ghp_1234567890abcdef gho_abcdef1234567890 glpat-1234567890abcdef xoxb-1234567890-abcdef",
			forbidden: []string{"sk-proj-1234567890abcd", "ghp_1234567890abcdef", "gho_abcdef1234567890",
				"glpat-1234567890abcdef", "xoxb-1234567890-abcdef"},
			expected: []string{"[REDACTED_API_KEY]"},
		},
		{
			name:      "url_userinfo",
			input:     "connecting to https://admin:SuperPass99@mesh.internal:8443/api",
			forbidden: []string{"admin:SuperPass99", "SuperPass99"},
			expected:  []string{"https://[REDACTED]:[REDACTED]@mesh.internal:8443/api"},
		},
		{
			name:      "kv_and_known_secrets",
			input:     "password=myPass123 token=tokVal999 secret='secVal888' api_key=keyVal777 custom=alphaSecret99",
			secrets:   []string{"alphaSecret99"},
			forbidden: []string{"myPass123", "tokVal999", "secVal888", "keyVal777", "alphaSecret99"},
			expected: []string{"password=[REDACTED]", "token=[REDACTED]", "secret=[REDACTED]", "api_key=[REDACTED]",
				"custom=[REDACTED]"},
		},
	}

	for _, tc := range samples {
		t.Run(tc.name, func(t *testing.T) {
			redactedText, changed := RedactSensitiveText(tc.input, tc.secrets...)
			if !changed {
				t.Fatalf("expected redaction to change input %q", tc.input)
			}
			for _, forbid := range tc.forbidden {
				if strings.Contains(redactedText, forbid) {
					t.Fatalf("secret %q leaked in output %q", forbid, redactedText)
				}
			}
			for _, want := range tc.expected {
				if !strings.Contains(redactedText, want) {
					t.Fatalf("expected %q in output %q", want, redactedText)
				}
			}
		})
	}

	cleanInput := "desktop runtime initialized cleanly on port 8080"
	cleanOutput, changed := RedactSensitiveText(cleanInput)
	if changed || cleanOutput != cleanInput {
		t.Fatalf("expected clean input unchanged, got changed=%v output=%q", changed, cleanOutput)
	}
}

func TestModePersistence(t *testing.T) {
	hub := openTestHub(t)
	ctx := context.Background()

	mgr, err := NewManager(hub, Options{})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	initialState, err := mgr.GetMode(ctx)
	if err != nil {
		t.Fatalf("get initial mode: %v", err)
	}
	if initialState.Mode != ModeDeveloper || len(initialState.RestrictedCapabilities) != 0 {
		t.Fatalf("expected default developer mode, got %+v", initialState)
	}

	if _, err := mgr.SetMode(ctx, "invalid-mode"); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for invalid mode, got %v", err)
	}

	wsState, err := mgr.SetMode(ctx, ModeWorkspace)
	if err != nil {
		t.Fatalf("set workspace mode: %v", err)
	}
	if wsState.Mode != ModeWorkspace || len(wsState.RestrictedCapabilities) == 0 {
		t.Fatalf("expected workspace mode with restricted capabilities, got %+v", wsState)
	}

	// Re-open manager on the same HubStore to verify SQLite desktop_settings persistence.
	reopenedMgr, err := NewManager(hub, Options{})
	if err != nil {
		t.Fatalf("reopen manager: %v", err)
	}
	persistedState, err := reopenedMgr.GetMode(ctx)
	if err != nil {
		t.Fatalf("get persisted mode: %v", err)
	}
	if persistedState.Mode != ModeWorkspace || persistedState.UpdatedAt == "" {
		t.Fatalf("expected persisted workspace mode, got %+v", persistedState)
	}
}

func TestLogHubRedactionFilteringAndRingBuffer(t *testing.T) {
	hub := openTestHub(t)
	ctx := context.Background()

	mgr, err := NewManager(hub, Options{
		KnownSecrets: []string{"vault-master-secret-42"},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	redactedEntry, err := mgr.Log(
		ctx,
		"error",
		"sidecar",
		"auth failed with Bearer superSecretBearer99 and key sk-live1234567890",
		"vault-master-secret-42 password=topsecret",
	)
	if err != nil {
		t.Fatalf("log error entry: %v", err)
	}
	if !redactedEntry.Redacted {
		t.Fatal("expected Redacted=true for sensitive log entry")
	}
	if strings.Contains(redactedEntry.Message, "superSecretBearer99") ||
		strings.Contains(redactedEntry.Message, "sk-live1234567890") ||
		strings.Contains(redactedEntry.Detail, "vault-master-secret-42") ||
		strings.Contains(redactedEntry.Detail, "topsecret") {
		t.Fatalf("sensitive material leaked in log entry: %+v", redactedEntry)
	}

	if _, err := mgr.Log(ctx, "warn", "mesh", "peer handshake slow", "latency=145ms"); err != nil {
		t.Fatalf("log warn entry: %v", err)
	}
	if _, err := mgr.Log(ctx, "info", "sidecar", "ready on loopback", ""); err != nil {
		t.Fatalf("log info entry: %v", err)
	}

	sidecarLogs, err := mgr.ListLogs(ctx, "", "sidecar", 10)
	if err != nil {
		t.Fatalf("list sidecar logs: %v", err)
	}
	if len(sidecarLogs) != 2 {
		t.Fatalf("expected 2 sidecar logs, got %d", len(sidecarLogs))
	}

	warnMeshLogs, err := mgr.ListLogs(ctx, "warn", "mesh", 10)
	if err != nil {
		t.Fatalf("list warn mesh logs: %v", err)
	}
	if len(warnMeshLogs) != 1 || warnMeshLogs[0].Message != "peer handshake slow" {
		t.Fatalf("unexpected filtered logs: %+v", warnMeshLogs)
	}

	// Verify 500-entry ring buffer cap while preserving SQLite persistence.
	for idx := 0; idx < 510; idx++ {
		if _, err := mgr.Log(ctx, "info", "ringtest", fmt.Sprintf("ring message %03d", idx), ""); err != nil {
			t.Fatalf("insert ring log %d: %v", idx, err)
		}
	}
	ringSnapshot := mgr.RingEntries()
	if len(ringSnapshot) != 500 {
		t.Fatalf("expected ring buffer capped at 500 entries, got %d", len(ringSnapshot))
	}
	if ringSnapshot[len(ringSnapshot)-1].Message != "ring message 509" {
		t.Fatalf("expected latest ring entry to be 'ring message 509', got %q", ringSnapshot[len(ringSnapshot)-1].Message)
	}
}

func TestDiscoverLocalAIAndLoopbackGuard(t *testing.T) {
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"models": [
				{"name": "llama3.2:3b", "size": 2147483648, "details": {"family": "llama"}},
				{"name": "qwen2.5-coder:7b", "size": 4294967296, "details": {"family": "qwen2"}}
			]
		}`))
	}))
	defer ollamaSrv.Close()

	lmStudioSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": [{"id": "deepseek-coder-v2-lite"}]}`))
	}))
	defer lmStudioSrv.Close()

	targets := []LocalAITarget{
		{ID: "ollama", Name: "Ollama", Kind: "ollama", Endpoint: ollamaSrv.URL + "/api/tags"},
		{ID: "lmstudio", Name: "LM Studio", Kind: "lmstudio", Endpoint: lmStudioSrv.URL + "/v1/models"},
		{ID: "remote-ssrf", Name: "Remote SSRF", Kind: "vllm", Endpoint: "https://example.com/v1/models"},
		{ID: "imds-ssrf", Name: "IMDS", Kind: "vllm", Endpoint: "http://169.254.169.254/latest/meta-data"},
		{ID: "wildcard-ssrf", Name: "Wildcard", Kind: "vllm", Endpoint: "http://0.0.0.0:8000/v1/models"},
		{ID: "userinfo-ssrf", Name: "Userinfo", Kind: "vllm", Endpoint: "http://user:pass@127.0.0.1:8000/v1/models"},
	}

	discovery := DiscoverLocalAI(context.Background(), targets)
	if discovery.OnlineCount != 2 {
		t.Fatalf("expected 2 online loopback providers, got %d (%+v)", discovery.OnlineCount, discovery.Providers)
	}
	if len(discovery.Models) != 3 {
		t.Fatalf("expected 3 discovered models, got %d", len(discovery.Models))
	}
	if discovery.RecommendedWorkspaceModel != "llama3.2:3b" {
		t.Fatalf("expected workspace model llama3.2:3b, got %q", discovery.RecommendedWorkspaceModel)
	}
	if discovery.RecommendedDevModel != "qwen2.5-coder:7b" {
		t.Fatalf("expected dev model qwen2.5-coder:7b, got %q", discovery.RecommendedDevModel)
	}

	for _, provider := range discovery.Providers {
		if strings.HasSuffix(provider.ID, "-ssrf") {
			if provider.Online {
				t.Fatalf("non-loopback/invalid target %q must never be online", provider.ID)
			}
			if !strings.Contains(strings.ToLower(provider.Error), "rejected") {
				t.Fatalf("expected rejection error for %q, got %q", provider.ID, provider.Error)
			}
		}
	}

	if IsLoopbackEndpointURL("http://127.0.0.2:11434/api/tags") {
		t.Fatal("expected 127.0.0.2 to fail closed")
	}
	if !IsLoopbackEndpointURL("http://[::1]:11434/api/tags") {
		t.Fatal("expected ::1 to pass loopback check")
	}
	if !IsLoopbackEndpointURL("http://localhost:11434/api/tags") {
		t.Fatal("expected localhost to pass loopback check")
	}
}

func TestRunDoctorAndRunRepair(t *testing.T) {
	hub := openTestHub(t)
	ctx := context.Background()

	// Pre-create a fake easytier binary under <dataDir>/bin/ and stale *.tmp files in tmp/.
	binDir := filepath.Join(hub.DataDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	fakeBinName := "easytier-core"
	if os.PathSeparator == '\\' {
		fakeBinName = "easytier-core.exe"
	}
	fakeBinPath := filepath.Join(binDir, fakeBinName)
	if err := os.WriteFile(fakeBinPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write fake easytier binary: %v", err)
	}

	tmpDir := filepath.Join(hub.DataDir(), "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	staleTmp := filepath.Join(tmpDir, "orphan.tmp")
	keepFile := filepath.Join(tmpDir, "keep.txt")
	if err := os.WriteFile(staleTmp, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale tmp: %v", err)
	}
	if err := os.WriteFile(keepFile, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write keep file: %v", err)
	}

	mgr, err := NewManager(hub, Options{
		KnownSecrets: []string{"secret-doctor-leak"},
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	initialReport, err := mgr.RunDoctor(ctx)
	if err != nil {
		t.Fatalf("run initial doctor: %v", err)
	}
	if !initialReport.Healthy {
		t.Fatalf("expected healthy doctor report, got %+v", initialReport)
	}
	if len(initialReport.Checks) != 5 {
		t.Fatalf("expected 5 doctor checks, got %d", len(initialReport.Checks))
	}

	checkStatuses := make(map[string]string, len(initialReport.Checks))
	for _, check := range initialReport.Checks {
		checkStatuses[check.ID] = check.Status
	}
	for _, expectedID := range []string{"sqlite_integrity", "data_dir_writable", "logs_dir_writable", "easytier_binary",
		"runtime_memory"} {
		if _, ok := checkStatuses[expectedID]; !ok {
			t.Fatalf("missing expected doctor check %q", expectedID)
		}
	}
	if checkStatuses["logs_dir_writable"] != checkStatusWarn {
		t.Fatalf("expected logs_dir_writable warn before repair, got %q", checkStatuses["logs_dir_writable"])
	}
	if checkStatuses["easytier_binary"] != checkStatusOK {
		t.Fatalf("expected easytier_binary ok, got %q", checkStatuses["easytier_binary"])
	}

	repairResult, err := mgr.RunRepair(ctx)
	if err != nil {
		t.Fatalf("run repair: %v", err)
	}
	if !repairResult.Repaired || repairResult.PrunedTempFiles != 1 {
		t.Fatalf("unexpected repair result: %+v", repairResult)
	}
	if _, err := os.Stat(staleTmp); !os.IsNotExist(err) {
		t.Fatalf("expected orphan.tmp to be pruned")
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("expected keep.txt to remain: %v", err)
	}

	repairedLogsStatus := ""
	for _, check := range repairResult.Report.Checks {
		if check.ID == "logs_dir_writable" {
			repairedLogsStatus = check.Status
		}
	}
	if repairedLogsStatus != checkStatusOK {
		t.Fatalf("expected logs_dir_writable ok after repair, got %q", repairedLogsStatus)
	}

	doctorLogs, err := mgr.ListLogs(ctx, "info", "doctor", 10)
	if err != nil {
		t.Fatalf("list doctor logs: %v", err)
	}
	if len(doctorLogs) != 1 {
		t.Fatalf("expected 1 logged repair entry, got %d", len(doctorLogs))
	}
	if !strings.Contains(repairResult.Report.SupportReportMarkdown, "# MoonWitness Desktop Support Report") {
		t.Fatalf("unexpected support markdown: %s", repairResult.Report.SupportReportMarkdown)
	}
}

func TestOpenDesktopWindowCrossPlatform(t *testing.T) {
	var launchedBin string
	var launchedArgs []string
	recordStart := func(name string, args ...string) error {
		launchedBin = name
		launchedArgs = append([]string(nil), args...)
		return nil
	}

	err := openDesktopWindowWith(
		"windows",
		"http://127.0.0.1:8080/dashboard",
		func(bin string) (string, error) {
			if bin == "msedge.exe" {
				return `C:\Edge\msedge.exe`, nil
			}
			return "", errors.New("not found")
		},
		func(string) bool { return false },
		recordStart,
	)
	if err != nil {
		t.Fatalf("windows edge launch: %v", err)
	}
	if launchedBin != `C:\Edge\msedge.exe` || len(launchedArgs) != 1 || launchedArgs[0] != "--app=http://127.0.0.1:8080/dashboard" {
		t.Fatalf("unexpected windows edge launch: %q %v", launchedBin, launchedArgs)
	}

	err = openDesktopWindowWith(
		"windows",
		"http://127.0.0.1:8080",
		func(string) (string, error) { return "", errors.New("not found") },
		func(string) bool { return false },
		recordStart,
	)
	if err != nil {
		t.Fatalf("windows rundll32 fallback: %v", err)
	}
	if launchedBin != "rundll32" || len(launchedArgs) != 2 || launchedArgs[0] != "url.dll,FileProtocolHandler" {
		t.Fatalf("unexpected windows rundll32 launch: %q %v", launchedBin, launchedArgs)
	}

	if err := openDesktopWindowWith("darwin", "https://localhost:8443", nil, nil, recordStart); err != nil {
		t.Fatalf("darwin launch: %v", err)
	}
	if launchedBin != "open" || launchedArgs[0] != "https://localhost:8443" {
		t.Fatalf("unexpected darwin launch: %q %v", launchedBin, launchedArgs)
	}

	if err := openDesktopWindowWith("linux", "http://127.0.0.1:3000", nil, nil, recordStart); err != nil {
		t.Fatalf("linux launch: %v", err)
	}
	if launchedBin != "xdg-open" || launchedArgs[0] != "http://127.0.0.1:3000" {
		t.Fatalf("unexpected linux launch: %q %v", launchedBin, launchedArgs)
	}

	if err := openDesktopWindowWith("linux", "file:///etc/passwd", nil, nil, recordStart); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for non-http scheme, got %v", err)
	}
}

func TestDesktopHTTPRoutes(t *testing.T) {
	hub := openTestHub(t)
	mgr, err := NewManager(hub, Options{LocalAITimeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	app := echo.New()
	api := app.Group("/api")
	mgr.RegisterRoutes(api)

	performRequest := func(method, target string, payload any) *httptest.ResponseRecorder {
		var bodyReader *bytes.Reader
		if payload != nil {
			encoded, _ := json.Marshal(payload)
			bodyReader = bytes.NewReader(encoded)
		} else {
			bodyReader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, target, bodyReader)
		if payload != nil {
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}

	statusRec := performRequest(http.MethodGet, "/api/desktop/status", nil)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET /status expected 200, got %d: %s", statusRec.Code, statusRec.Body.String())
	}

	modeRec := performRequest(http.MethodPut, "/api/desktop/mode", map[string]string{"mode": "workspace"})
	if modeRec.Code != http.StatusOK {
		t.Fatalf("PUT /mode expected 200, got %d: %s", modeRec.Code, modeRec.Body.String())
	}
	var modeResp DesktopModeState
	if err := json.Unmarshal(modeRec.Body.Bytes(), &modeResp); err != nil {
		t.Fatalf("decode mode response: %v", err)
	}
	if modeResp.Mode != ModeWorkspace {
		t.Fatalf("expected workspace mode, got %q", modeResp.Mode)
	}

	doctorRec := performRequest(http.MethodGet, "/api/desktop/doctor", nil)
	if doctorRec.Code != http.StatusOK {
		t.Fatalf("GET /doctor expected 200, got %d", doctorRec.Code)
	}

	repairRec := performRequest(http.MethodPost, "/api/desktop/doctor/repair", nil)
	if repairRec.Code != http.StatusOK {
		t.Fatalf("POST /doctor/repair expected 200, got %d", repairRec.Code)
	}

	localAIRec := performRequest(http.MethodGet, "/api/desktop/local-ai", nil)
	if localAIRec.Code != http.StatusOK {
		t.Fatalf("GET /local-ai expected 200, got %d", localAIRec.Code)
	}

	riskRec := performRequest(http.MethodPost, "/api/desktop/risk/evaluate", map[string]string{
		"action":     "repo.read",
		"targetPath": ".ssh/id_rsa",
	})
	if riskRec.Code != http.StatusOK {
		t.Fatalf("POST /risk/evaluate expected 200, got %d", riskRec.Code)
	}
	var evalResp RiskEvaluation
	if err := json.Unmarshal(riskRec.Body.Bytes(), &evalResp); err != nil {
		t.Fatalf("decode risk response: %v", err)
	}
	if evalResp.ResolvedTier != RiskTierR4 || evalResp.Allowed {
		t.Fatalf("expected R4 denied evaluation, got %+v", evalResp)
	}

	createLogRec := performRequest(http.MethodPost, "/api/desktop/logs", map[string]string{
		"level":   "warn",
		"source":  "webview",
		"message": "token=abc123secret leaked",
	})
	if createLogRec.Code != http.StatusCreated {
		t.Fatalf("POST /logs expected 201, got %d: %s", createLogRec.Code, createLogRec.Body.String())
	}

	listLogsRec := performRequest(http.MethodGet, "/api/desktop/logs?level=warn&source=webview&limit=5", nil)
	if listLogsRec.Code != http.StatusOK {
		t.Fatalf("GET /logs expected 200, got %d", listLogsRec.Code)
	}
	var logsResp struct {
		Logs  []LogEntry `json:"logs"`
		Count int        `json:"count"`
	}
	if err := json.Unmarshal(listLogsRec.Body.Bytes(), &logsResp); err != nil {
		t.Fatalf("decode logs response: %v", err)
	}
	if logsResp.Count != 1 || !logsResp.Logs[0].Redacted {
		t.Fatalf("expected 1 redacted webview warn log, got %+v", logsResp)
	}
}
