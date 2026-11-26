package meshfleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/storage"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func openTestManager(t *testing.T, allowLoopback bool) (*Manager, *store.HubStore) {
	t.Helper()
	dataDir := t.TempDir()
	hub, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() {
		_ = hub.Close()
	})

	mgr, err := NewManager(hub, Options{AllowLoopback: allowLoopback})
	if err != nil {
		t.Fatalf("new meshfleet manager: %v", err)
	}
	return mgr, hub
}

func TestValidateMeshEndpoint_SSRF(t *testing.T) {
	allowedCases := []struct {
		rawURL        string
		allowLoopback bool
	}{
		{rawURL: "http://10.144.0.1:3003", allowLoopback: false},
		{rawURL: "https://172.16.10.20:8443/control", allowLoopback: false},
		{rawURL: "http://192.168.1.50:3000", allowLoopback: false},
		{rawURL: "http://100.64.0.42:9000", allowLoopback: false},
		{rawURL: "http://100.127.255.254:9000", allowLoopback: false},
		{rawURL: "http://127.0.0.1:4174", allowLoopback: true},
		{rawURL: "http://[::1]:4174", allowLoopback: true},
	}

	for _, testCase := range allowedCases {
		parsedURL, err := ValidateMeshEndpoint(testCase.rawURL, testCase.allowLoopback)
		if err != nil {
			t.Fatalf("expected %q (allowLoopback=%v) to pass, got error: %v", testCase.rawURL, testCase.allowLoopback, err)
		}
		if parsedURL == nil || parsedURL.Host == "" {
			t.Fatalf("expected non-nil parsed URL for %q", testCase.rawURL)
		}
	}

	rejectedCases := []struct {
		name          string
		rawURL        string
		allowLoopback bool
	}{
		{name: "public IPv4", rawURL: "https://8.8.8.8:443", allowLoopback: false},
		{name: "public IPv4 near CGNAT", rawURL: "http://100.128.0.1:3000", allowLoopback: false},
		{name: "public IPv4 near 172.16", rawURL: "http://172.32.0.1:3000", allowLoopback: false},
		{name: "arbitrary hostname", rawURL: "https://evil.example.com/api", allowLoopback: false},
		{name: "localhost hostname even with loopback", rawURL: "http://localhost:3000", allowLoopback: true},
		{name: "loopback rejected in prod mode", rawURL: "http://127.0.0.1:3000", allowLoopback: false},
		{name: "cloud metadata link-local", rawURL: "http://169.254.169.254/latest/meta-data", allowLoopback: true},
		{name: "userinfo credentials", rawURL: "http://admin:secret@10.144.0.1:3000", allowLoopback: false},
		{name: "URL fragment", rawURL: "http://10.144.0.1:3000/status#frag", allowLoopback: false},
		{name: "empty trailing fragment", rawURL: "http://10.144.0.1:3000/#", allowLoopback: false},
		{name: "ftp scheme", rawURL: "ftp://10.144.0.1/file", allowLoopback: false},
		{name: "file scheme", rawURL: "file:///etc/passwd", allowLoopback: true},
		{name: "mapped IPv6 public", rawURL: "http://[::ffff:8.8.8.8]:8080", allowLoopback: true},
	}

	for _, testCase := range rejectedCases {
		if _, err := ValidateMeshEndpoint(testCase.rawURL, testCase.allowLoopback); err == nil {
			t.Fatalf("expected SSRF rejection for %s (%q), got nil", testCase.name, testCase.rawURL)
		}
	}
}

func TestEncryptionAndTokenRedaction(t *testing.T) {
	ctx := context.Background()
	mgr, hub := openTestManager(t, false)

	rawAdminToken := "mw_admin_super_secret_token_987654321"
	registered, err := mgr.RegisterNode(ctx, RegisterNodeInput{
		ID:         "vps-sgp-01",
		Label:      "Singapore Mesh VPS",
		BaseURL:    "http://100.64.0.12:3003/",
		AdminToken: rawAdminToken,
	})
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	if !registered.TokenConfigured {
		t.Fatal("expected TokenConfigured=true")
	}
	if registered.TokenHint != "mw_a...4321" {
		t.Fatalf("unexpected token hint %q", registered.TokenHint)
	}
	if registered.BaseURL != "http://100.64.0.12:3003" {
		t.Fatalf("expected normalized baseUrl without trailing slash, got %q", registered.BaseURL)
	}

	var storedCiphertext string
	if scanErr := hub.DB().QueryRow(`SELECT admin_token_enc FROM mesh_nodes WHERE id = ?`, "vps-sgp-01").Scan(&storedCiphertext); scanErr != nil {
		t.Fatalf("query stored ciphertext: %v", scanErr)
	}
	if storedCiphertext == "" || strings.Contains(storedCiphertext, rawAdminToken) {
		t.Fatal("expected admin_token_enc to be AES-256-GCM ciphertext, not plaintext")
	}

	box, err := storage.OpenSecretBox(hub.DataDir())
	if err != nil {
		t.Fatalf("open secret box: %v", err)
	}
	decrypted, err := box.Decrypt(storedCiphertext)
	if err != nil {
		t.Fatalf("decrypt stored token: %v", err)
	}
	if string(decrypted) != rawAdminToken {
		t.Fatalf("decrypted token mismatch: got %q", string(decrypted))
	}

	listedNodes, err := mgr.ListNodes(ctx)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(listedNodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(listedNodes))
	}
	serialized, err := json.Marshal(listedNodes)
	if err != nil {
		t.Fatalf("marshal listed nodes: %v", err)
	}
	if strings.Contains(string(serialized), rawAdminToken) {
		t.Fatal("raw adminToken leaked in ListNodes JSON output")
	}
	if !listedNodes[0].TokenConfigured || listedNodes[0].TokenHint != "mw_a...4321" {
		t.Fatalf("unexpected listed node metadata: %+v", listedNodes[0])
	}

	if err := mgr.DeleteNode(ctx, "vps-sgp-01"); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	if err := mgr.DeleteNode(ctx, "vps-sgp-01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestProbeNode_AgainstTestServer(t *testing.T) {
	ctx := context.Background()
	mgr, _ := openTestManager(t, true)

	expectedToken := "mesh_bearer_token_abcdef123456"
	var sawAuthHeader string
	var hitHealth bool
	var hitMeshStatus bool

	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			hitHealth = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","database":{"status":"healthy"}}`))
		case "/api/mesh/status":
			hitMeshStatus = true
			sawAuthHeader = r.Header.Get("Authorization")
			if sawAuthHeader != "Bearer "+expectedToken {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"enabled":true,"backend":"easytier","status":"connected","ipv4":"10.144.0.2"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer probeServer.Close()

	nodeRecord, err := mgr.RegisterNode(ctx, RegisterNodeInput{
		ID:         "loopback-probe-node",
		Label:      "Loopback Probe Node",
		BaseURL:    probeServer.URL,
		AdminToken: expectedToken,
	})
	if err != nil {
		t.Fatalf("register loopback node: %v", err)
	}

	probeResult, err := mgr.ProbeNode(ctx, nodeRecord.ID)
	if err != nil {
		t.Fatalf("probe node: %v", err)
	}
	if !hitHealth || !hitMeshStatus {
		t.Fatalf("expected both /health and /api/mesh/status to be queried (health=%v, mesh=%v)", hitHealth, hitMeshStatus)
	}
	if sawAuthHeader != "Bearer "+expectedToken {
		t.Fatalf("expected Bearer auth header on /api/mesh/status, got %q", sawAuthHeader)
	}
	if probeResult.Status != StatusOnline || !probeResult.HealthOK || !probeResult.MeshOK {
		t.Fatalf("expected online probe result, got %+v", probeResult)
	}
	if probeResult.Node.LastStatus != StatusOnline || probeResult.Node.LastProbeAt == "" {
		t.Fatalf("expected node last_status and last_probe_at to update, got %+v", probeResult.Node)
	}

	listed, err := mgr.ListNodes(ctx)
	if err != nil {
		t.Fatalf("list nodes after probe: %v", err)
	}
	if len(listed) != 1 || listed[0].LastStatus != StatusOnline || listed[0].LastProbeAt == "" {
		t.Fatalf("expected persisted probe state in SQLite, got %+v", listed)
	}
}

func TestBuildVPSJoinCommand(t *testing.T) {
	joinCmd, err := BuildVPSJoinCommand("mw-fleet-alpha", "10.144.0.1:11010", "vps-jakarta-01")
	if err != nil {
		t.Fatalf("build join command: %v", err)
	}
	for _, expectedPart := range []string{
		"easytier-core",
		"--network-name 'mw-fleet-alpha'",
		"--network-secret '<MW_MESH_NETWORK_SECRET>'",
		"--peers 'tcp://10.144.0.1:11010'",
		"--hostname 'vps-jakarta-01'",
	} {
		if !strings.Contains(joinCmd, expectedPart) {
			t.Fatalf("expected %q in join command %q", expectedPart, joinCmd)
		}
	}

	if _, err := BuildVPSJoinCommand("mw; rm -rf /", "10.144.0.1:11010", "vps-01"); err == nil {
		t.Fatal("expected shell metacharacter rejection in networkName")
	}
	if _, err := BuildVPSJoinCommand("mw-ok", "10.144.0.1:11010", "-bad-flag"); err == nil {
		t.Fatal("expected leading dash rejection in hostname")
	}
}

func TestWorktreeSandbox_RealGitRepo(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
		}
	}

	runGit("init")
	runGit("config", "user.name", "MeshFleet Test")
	runGit("config", "user.email", "meshfleet@example.local")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("meshfleet test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial commit")

	initialWorktrees, err := ListWorktrees(ctx, repoDir)
	if err != nil {
		t.Fatalf("list initial worktrees: %v", err)
	}
	if len(initialWorktrees) != 1 {
		t.Fatalf("expected 1 primary worktree, got %d: %+v", len(initialWorktrees), initialWorktrees)
	}

	if _, escapeErr := CreateWorktree(ctx, repoDir, "feat/escape", "../outside-escape"); !errors.Is(escapeErr, ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape for .. traversal, got %v", escapeErr)
	}
	outsideDir := filepath.Join(filepath.Dir(repoDir), "sibling-outside")
	if _, outsideErr := CreateWorktree(ctx, repoDir, "feat/outside", outsideDir); !errors.Is(outsideErr, ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape for outside absolute path, got %v", outsideErr)
	}

	created, err := CreateWorktree(ctx, repoDir, "feat/sandbox-01", ".mw-worktrees/sandbox-01")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if created.Branch != "feat/sandbox-01" || created.Head == "" {
		t.Fatalf("unexpected created worktree info: %+v", created)
	}

	afterCreate, err := ListWorktrees(ctx, repoDir)
	if err != nil {
		t.Fatalf("list worktrees after create: %v", err)
	}
	if len(afterCreate) != 2 {
		t.Fatalf("expected 2 worktrees after create, got %d", len(afterCreate))
	}

	if err := RemoveWorktree(ctx, repoDir, "../outside-escape"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("expected ErrPathEscape on RemoveWorktree traversal, got %v", err)
	}
	if err := RemoveWorktree(ctx, repoDir, ".mw-worktrees/sandbox-01"); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	afterRemove, err := ListWorktrees(ctx, repoDir)
	if err != nil {
		t.Fatalf("list worktrees after remove: %v", err)
	}
	if len(afterRemove) != 1 {
		t.Fatalf("expected 1 worktree after remove, got %d", len(afterRemove))
	}
}

func TestMeshRoutes(t *testing.T) {
	mgr, _ := openTestManager(t, false)
	echoApp := echo.New()
	apiGroup := echoApp.Group("/api")
	mgr.RegisterRoutes(apiGroup)

	// Verify SSRF rejection over HTTP route returns 403
	badBody := []byte(`{"label":"Public","baseUrl":"https://1.1.1.1","adminToken":"tok_123456789"}`)
	badReq := httptest.NewRequest(http.MethodPost, "/api/mesh/nodes", bytes.NewReader(badBody))
	badReq.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	echoApp.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for public IP registration, got %d: %s", badRec.Code, badRec.Body.String())
	}

	// Register valid private node
	goodBody := []byte(`{"id":"node-mesh-1","label":"Private Mesh","baseUrl":"http://10.144.0.9:3003","adminToken":"tok_secret_abcdef123"}`)
	goodReq := httptest.NewRequest(http.MethodPost, "/api/mesh/nodes", bytes.NewReader(goodBody))
	goodReq.Header.Set("Content-Type", "application/json")
	goodRec := httptest.NewRecorder()
	echoApp.ServeHTTP(goodRec, goodReq)
	if goodRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for valid mesh node, got %d: %s", goodRec.Code, goodRec.Body.String())
	}

	// Join command route
	joinReq := httptest.NewRequest(http.MethodPost, "/api/mesh/join-command", bytes.NewReader([]byte(`{"networkName":"mw-net","peerAddr":"10.144.0.1:11010","hostname":"vps-1"}`)))
	joinReq.Header.Set("Content-Type", "application/json")
	joinRec := httptest.NewRecorder()
	echoApp.ServeHTTP(joinRec, joinReq)
	if joinRec.Code != http.StatusOK || !strings.Contains(joinRec.Body.String(), "mw-net") {
		t.Fatalf("unexpected join-command response (%d): %s", joinRec.Code, joinRec.Body.String())
	}
}
