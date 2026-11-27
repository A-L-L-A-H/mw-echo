package meshfleet

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/storage"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

const (
	// StatusUnknown indicates a mesh node has not been probed yet.
	StatusUnknown = "unknown"
	// StatusOnline indicates both /health and /api/mesh/status succeeded.
	StatusOnline = "online"
	// StatusDegraded indicates one probe endpoint succeeded while the other failed.
	StatusDegraded = "degraded"
	// StatusOffline indicates the mesh node failed health and status probes.
	StatusOffline = "offline"

	defaultProbeTimeout = 10 * time.Second
	maxProbeBodyBytes   = 64 * 1024
)

var (
	// ErrValidation indicates malformed or missing mesh fleet inputs.
	ErrValidation = errors.New("meshfleet validation failed")
	// ErrEndpointDenied indicates an endpoint URL failed private-mesh SSRF validation.
	ErrEndpointDenied = errors.New("mesh endpoint denied")
	// ErrNotFound indicates the requested mesh node does not exist.
	ErrNotFound = errors.New("mesh node not found")

	privateMeshPrefixes = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("100.64.0.0/10"),
	}
)

// Options configures the Private Mesh Fleet Manager.
type Options struct {
	HTTPClient    *http.Client
	AllowLoopback bool
}

// RegisterNodeInput defines parameters to register or update a private mesh node.
type RegisterNodeInput struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	BaseURL    string `json:"baseUrl"`
	AdminToken string `json:"adminToken"`
}

// NodeRecord represents a registered private mesh node with redacted admin credentials.
type NodeRecord struct {
	ID              string `json:"id"`
	Label           string `json:"label"`
	BaseURL         string `json:"baseUrl"`
	TokenHint       string `json:"tokenHint"`
	TokenConfigured bool   `json:"tokenConfigured"`
	LastStatus      string `json:"lastStatus"`
	LastProbeAt     string `json:"lastProbeAt"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

// ProbeResult captures the outcome of probing a mesh node's /health and /api/mesh/status endpoints.
type ProbeResult struct {
	Node       NodeRecord `json:"node"`
	Status     string     `json:"status"`
	HealthOK   bool       `json:"healthOk"`
	HealthCode int        `json:"healthCode"`
	HealthBody any        `json:"healthBody,omitempty"`
	MeshOK     bool       `json:"meshOk"`
	MeshCode   int        `json:"meshCode"`
	MeshStatus any        `json:"meshStatus,omitempty"`
	ProbeError string     `json:"probeError,omitempty"`
	ProbedAt   string     `json:"probedAt"`
}

// Manager persists encrypted mesh node credentials and orchestrates private-mesh probes.
type Manager struct {
	hub           *store.HubStore
	db            *sql.DB
	secretBox     *storage.SecretBox
	httpClient    *http.Client
	allowLoopback bool
}

// ValidateMeshEndpoint enforces fail-closed SSRF validation for mesh node URLs.
// Only http/https URLs without userinfo or fragments targeting RFC1918 or CGNAT (100.64.0.0/10)
// IP literals are permitted; loopback IPs are allowed solely when allowLoopback is true.
func ValidateMeshEndpoint(rawURL string, allowLoopback bool) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: endpoint URL is required", ErrValidation)
	}
	if strings.Contains(trimmed, "#") {
		return nil, fmt.Errorf("%w: URL fragments are forbidden", ErrEndpointDenied)
	}

	parsedURL, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid mesh endpoint URL: %v", ErrValidation, err)
	}

	scheme := strings.ToLower(parsedURL.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q is not allowed; expected http or https", ErrEndpointDenied, parsedURL.Scheme)
	}
	parsedURL.Scheme = scheme

	if parsedURL.User != nil {
		return nil, fmt.Errorf("%w: userinfo credentials in URL are forbidden", ErrEndpointDenied)
	}
	if parsedURL.Fragment != "" || parsedURL.RawFragment != "" {
		return nil, fmt.Errorf("%w: URL fragments are forbidden", ErrEndpointDenied)
	}

	hostname := strings.TrimSpace(parsedURL.Hostname())
	if hostname == "" {
		return nil, fmt.Errorf("%w: endpoint host is required", ErrValidation)
	}

	addr, err := netip.ParseAddr(hostname)
	if err != nil {
		return nil, fmt.Errorf("%w: hostname %q rejected; IP literal required", ErrEndpointDenied, hostname)
	}
	if addr.Zone() != "" {
		return nil, fmt.Errorf("%w: IPv6 zone identifiers are forbidden", ErrEndpointDenied)
	}
	addr = addr.Unmap()

	if !isAllowedMeshAddr(addr, allowLoopback) {
		return nil, fmt.Errorf("%w: IP %q is outside RFC1918 and CGNAT 100.64.0.0/10 ranges", ErrEndpointDenied,
			addr.String())
	}

	return parsedURL, nil
}

func isAllowedMeshAddr(addr netip.Addr, allowLoopback bool) bool {
	if allowLoopback && addr.IsLoopback() {
		return true
	}
	for _, prefix := range privateMeshPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// NewManager opens the AES-256-GCM SecretBox, migrates the mesh_nodes table, and returns a Manager.
func NewManager(hub *store.HubStore, opts Options) (*Manager, error) {
	if hub == nil || hub.DB() == nil {
		return nil, errors.New("meshfleet: hub store is required")
	}

	secretBox, err := storage.OpenSecretBox(hub.DataDir())
	if err != nil {
		return nil, fmt.Errorf("meshfleet open secret box: %w", err)
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: defaultProbeTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if _, checkErr := ValidateMeshEndpoint(req.URL.String(), opts.AllowLoopback); checkErr != nil {
					return checkErr
				}
				if len(via) >= 3 {
					return errors.New("too many redirects")
				}
				return nil
			},
		}
	}

	mgr := &Manager{
		hub:           hub,
		db:            hub.DB(),
		secretBox:     secretBox,
		httpClient:    client,
		allowLoopback: opts.AllowLoopback,
	}
	if err := mgr.migrate(); err != nil {
		return nil, fmt.Errorf("meshfleet migrate: %w", err)
	}
	return mgr, nil
}

func (m *Manager) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS mesh_nodes (
  id TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  base_url TEXT NOT NULL,
  admin_token_enc TEXT NOT NULL,
  token_hint TEXT NOT NULL DEFAULT '',
  last_status TEXT NOT NULL DEFAULT 'unknown',
  last_probe_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mesh_nodes_updated_at ON mesh_nodes(updated_at DESC);
`
	_, err := m.db.Exec(schema)
	return err
}

// RegisterNode validates the private mesh endpoint, encrypts the admin token with AES-256-GCM,
// and persists the node record in SQLite without returning the raw token.
func (m *Manager) RegisterNode(ctx context.Context, input RegisterNodeInput) (NodeRecord, error) {
	label := strings.TrimSpace(input.Label)
	if label == "" {
		return NodeRecord{}, fmt.Errorf("%w: node label is required", ErrValidation)
	}

	parsedURL, err := ValidateMeshEndpoint(input.BaseURL, m.allowLoopback)
	if err != nil {
		return NodeRecord{}, err
	}
	normalizedBase := normalizeBaseURL(parsedURL)

	nodeID := strings.TrimSpace(input.ID)
	rawToken := strings.TrimSpace(input.AdminToken)

	var existingEnc string
	var existingHint string
	var existingStatus string
	var existingProbeAt string
	var existingCreatedAt string
	hasExisting := false

	if nodeID != "" {
		rowErr := m.db.QueryRowContext(ctx, `
SELECT admin_token_enc, token_hint, last_status, last_probe_at, created_at
FROM mesh_nodes WHERE id = ?`, nodeID).Scan(
			&existingEnc, &existingHint, &existingStatus, &existingProbeAt, &existingCreatedAt,
		)
		if rowErr == nil {
			hasExisting = true
		} else if !errors.Is(rowErr, sql.ErrNoRows) {
			return NodeRecord{}, rowErr
		}
	} else {
		nodeID = generateNodeID()
	}

	tokenEnc := existingEnc
	tokenHint := existingHint
	if rawToken != "" {
		encrypted, encErr := m.secretBox.Encrypt([]byte(rawToken))
		if encErr != nil {
			return NodeRecord{}, fmt.Errorf("encrypt mesh admin token: %w", encErr)
		}
		tokenEnc = encrypted
		tokenHint = maskTokenHint(rawToken)
	} else if !hasExisting || strings.TrimSpace(tokenEnc) == "" {
		return NodeRecord{}, fmt.Errorf("%w: adminToken is required for new mesh node", ErrValidation)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	createdAt := now
	lastStatus := StatusUnknown
	lastProbeAt := ""
	if hasExisting {
		createdAt = existingCreatedAt
		lastStatus = existingStatus
		lastProbeAt = existingProbeAt
	}

	_, execErr := m.db.ExecContext(ctx, `
INSERT INTO mesh_nodes (
  id, label, base_url, admin_token_enc, token_hint, last_status, last_probe_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  label = excluded.label,
  base_url = excluded.base_url,
  admin_token_enc = excluded.admin_token_enc,
  token_hint = excluded.token_hint,
  updated_at = excluded.updated_at
`, nodeID, label, normalizedBase, tokenEnc, tokenHint, lastStatus, lastProbeAt, createdAt, now)
	if execErr != nil {
		return NodeRecord{}, execErr
	}

	return NodeRecord{
		ID:              nodeID,
		Label:           label,
		BaseURL:         normalizedBase,
		TokenHint:       tokenHint,
		TokenConfigured: tokenEnc != "",
		LastStatus:      lastStatus,
		LastProbeAt:     lastProbeAt,
		CreatedAt:       createdAt,
		UpdatedAt:       now,
	}, nil
}

// ListNodes returns all registered mesh nodes with redacted token metadata (never raw tokens).
func (m *Manager) ListNodes(ctx context.Context) ([]NodeRecord, error) {
	rows, err := m.db.QueryContext(ctx, `
SELECT id, label, base_url, admin_token_enc, token_hint, last_status, last_probe_at, created_at, updated_at
FROM mesh_nodes
ORDER BY updated_at DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodesList := make([]NodeRecord, 0)
	for rows.Next() {
		var record NodeRecord
		var tokenEnc string
		if scanErr := rows.Scan(
			&record.ID,
			&record.Label,
			&record.BaseURL,
			&tokenEnc,
			&record.TokenHint,
			&record.LastStatus,
			&record.LastProbeAt,
			&record.CreatedAt,
			&record.UpdatedAt,
		); scanErr != nil {
			return nil, scanErr
		}
		record.TokenConfigured = strings.TrimSpace(tokenEnc) != ""
		nodesList = append(nodesList, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nodesList, nil
}

// DeleteNode removes a mesh node by ID.
func (m *Manager) DeleteNode(ctx context.Context, id string) error {
	nodeID := strings.TrimSpace(id)
	if nodeID == "" {
		return fmt.Errorf("%w: node id is required", ErrValidation)
	}
	sqlResult, err := m.db.ExecContext(ctx, `DELETE FROM mesh_nodes WHERE id = ?`, nodeID)
	if err != nil {
		return err
	}
	affected, err := sqlResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}
	return nil
}

// ProbeNode fetches <baseUrl>/health and <baseUrl>/api/mesh/status (with Bearer auth),
// persists last_status and last_probe_at, and returns the redacted ProbeResult.
func (m *Manager) ProbeNode(ctx context.Context, id string) (ProbeResult, error) {
	nodeID := strings.TrimSpace(id)
	if nodeID == "" {
		return ProbeResult{}, fmt.Errorf("%w: node id is required", ErrValidation)
	}

	var record NodeRecord
	var tokenEnc string
	rowErr := m.db.QueryRowContext(ctx, `
SELECT id, label, base_url, admin_token_enc, token_hint, last_status, last_probe_at, created_at, updated_at
FROM mesh_nodes WHERE id = ?`, nodeID).Scan(
		&record.ID,
		&record.Label,
		&record.BaseURL,
		&tokenEnc,
		&record.TokenHint,
		&record.LastStatus,
		&record.LastProbeAt,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	if errors.Is(rowErr, sql.ErrNoRows) {
		return ProbeResult{}, fmt.Errorf("%w: %s", ErrNotFound, nodeID)
	}
	if rowErr != nil {
		return ProbeResult{}, rowErr
	}
	record.TokenConfigured = strings.TrimSpace(tokenEnc) != ""

	parsedBase, err := ValidateMeshEndpoint(record.BaseURL, m.allowLoopback)
	if err != nil {
		return ProbeResult{}, err
	}
	baseURL := normalizeBaseURL(parsedBase)

	tokenBytes, err := m.secretBox.Decrypt(tokenEnc)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("decrypt mesh admin token: %w", err)
	}
	adminToken := strings.TrimSpace(string(tokenBytes))

	healthCode, healthBody, healthErr := m.fetchProbeJSON(ctx, baseURL+"/health", "")
	meshCode, meshBody, meshErr := m.fetchProbeJSON(ctx, baseURL+"/api/mesh/status", adminToken)

	healthOK := healthErr == nil && healthCode >= 200 && healthCode < 300
	meshOK := meshErr == nil && meshCode >= 200 && meshCode < 300

	derivedStatus := StatusOffline
	switch {
	case healthOK && meshOK:
		derivedStatus = StatusOnline
	case healthOK || meshOK:
		derivedStatus = StatusDegraded
	}

	var errParts []string
	if healthErr != nil {
		errParts = append(errParts, "health: "+redactSecretText(healthErr.Error(), adminToken))
	} else if !healthOK {
		errParts = append(errParts, fmt.Sprintf("health: HTTP %d", healthCode))
	}
	if meshErr != nil {
		errParts = append(errParts, "mesh: "+redactSecretText(meshErr.Error(), adminToken))
	} else if !meshOK {
		errParts = append(errParts, fmt.Sprintf("mesh: HTTP %d", meshCode))
	}

	probedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, updateErr := m.db.ExecContext(ctx, `
UPDATE mesh_nodes
SET last_status = ?, last_probe_at = ?, updated_at = ?
WHERE id = ?`, derivedStatus, probedAt, probedAt, record.ID); updateErr != nil {
		return ProbeResult{}, updateErr
	}

	record.LastStatus = derivedStatus
	record.LastProbeAt = probedAt
	record.UpdatedAt = probedAt

	return ProbeResult{
		Node:       record,
		Status:     derivedStatus,
		HealthOK:   healthOK,
		HealthCode: healthCode,
		HealthBody: healthBody,
		MeshOK:     meshOK,
		MeshCode:   meshCode,
		MeshStatus: meshBody,
		ProbeError: strings.Join(errParts, "; "),
		ProbedAt:   probedAt,
	}, nil
}

func (m *Manager) fetchProbeJSON(ctx context.Context, targetURL, bearerToken string) (int, any, error) {
	if _, err := ValidateMeshEndpoint(targetURL, m.allowLoopback); err != nil {
		return 0, nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, nil, err
	}
	httpReq.Header.Set("Accept", "application/json")
	if bearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	httpResp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return 0, nil, err
	}
	defer httpResp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, maxProbeBodyBytes))
	if err != nil {
		return httpResp.StatusCode, nil, err
	}

	trimmed := strings.TrimSpace(string(bodyBytes))
	if trimmed == "" {
		return httpResp.StatusCode, nil, nil
	}
	var parsed any
	if unmarshalErr := json.Unmarshal([]byte(trimmed), &parsed); unmarshalErr == nil {
		return httpResp.StatusCode, parsed, nil
	}
	return httpResp.StatusCode, redactSecretText(trimmed, bearerToken), nil
}

// BuildVPSJoinCommand constructs a shell-safe EasyTier VPS join command without embedding raw secrets.
func (m *Manager) BuildVPSJoinCommand(networkName, peerAddr, hostname string) (string, error) {
	return BuildVPSJoinCommand(networkName, peerAddr, hostname)
}

// BuildVPSJoinCommand constructs a shell-safe EasyTier VPS join command without embedding raw secrets.
func BuildVPSJoinCommand(networkName, peerAddr, hostname string) (string, error) {
	cleanNetwork, err := sanitizeJoinToken(networkName, "networkName", true)
	if err != nil {
		return "", err
	}
	cleanPeer, err := sanitizePeerAddress(peerAddr)
	if err != nil {
		return "", err
	}
	cleanHostname, err := sanitizeJoinToken(hostname, "hostname", false)
	if err != nil {
		return "", err
	}

	args := []string{
		"easytier-core",
		"--dhcp",
		"--network-name", shellSingleQuote(cleanNetwork),
		"--network-secret", shellSingleQuote("<MW_MESH_NETWORK_SECRET>"),
	}
	if cleanPeer != "" {
		args = append(args, "--peers", shellSingleQuote(cleanPeer))
	}
	if cleanHostname != "" {
		args = append(args, "--hostname", shellSingleQuote(cleanHostname))
	}
	return strings.Join(args, " "), nil
}

func sanitizeJoinToken(raw, fieldName string, required bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if required {
			return "", fmt.Errorf("%w: %s is required", ErrValidation, fieldName)
		}
		return "", nil
	}
	if strings.HasPrefix(trimmed, "-") {
		return "", fmt.Errorf("%w: %s must not start with '-'", ErrValidation, fieldName)
	}
	for _, ch := range trimmed {
		isSafe := (ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '.' || ch == '_' || ch == '-'
		if !isSafe {
			return "", fmt.Errorf("%w: %s contains disallowed character %q", ErrValidation, fieldName, ch)
		}
	}
	return trimmed, nil
}

func sanitizePeerAddress(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if strings.HasPrefix(trimmed, "-") {
		return "", fmt.Errorf("%w: peerAddr must not start with '-'", ErrValidation)
	}
	for _, ch := range trimmed {
		isSafe := (ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '.' || ch == ':' || ch == '/' || ch == '-' || ch == '_' || ch == '[' || ch == ']'
		if !isSafe {
			return "", fmt.Errorf("%w: peerAddr contains disallowed character %q", ErrValidation, ch)
		}
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "tcp://" + trimmed
	}
	return trimmed, nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func normalizeBaseURL(parsedURL *url.URL) string {
	cleanedPath := strings.TrimRight(parsedURL.Path, "/")
	return fmt.Sprintf("%s://%s%s", parsedURL.Scheme, parsedURL.Host, cleanedPath)
}

func maskTokenHint(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return "configured"
	}
	return trimmed[:4] + "..." + trimmed[len(trimmed)-4:]
}

func redactSecretText(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[REDACTED]")
}

func generateNodeID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("node_%d", time.Now().UnixNano())
	}
	return "node_" + hex.EncodeToString(buf)
}
