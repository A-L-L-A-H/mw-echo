package fleet

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

const (
	genesisHash        = "GENESIS"
	defaultProbeWindow = 1500 * time.Millisecond
)

// Options configures the Fleet & ARSY Manager.
type Options struct {
	ProbeTimeout time.Duration
	HTTPClient   *http.Client
	Now          func() time.Time
}

// Manager coordinates ARSY constitutional grants, Merkle audit logging, and sovereign fleet probing.
type Manager struct {
	hub        *store.HubStore
	probeLimit time.Duration
	httpClient *http.Client
	nowFn      func() time.Time
	mu         sync.Mutex
}

// Grant represents an ARSY constitutional capability grant.
type Grant struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	Capability  string `json:"capability"`
	RiskTier    string `json:"riskTier"`
	BudgetLimit int    `json:"budgetLimit"`
	BudgetUsed  int    `json:"budgetUsed"`
	Status      string `json:"status"`
	ExpiresAt   string `json:"expiresAt"`
	CreatedAt   string `json:"createdAt"`
}

// GrantInput defines parameters for issuing a new ARSY grant.
type GrantInput struct {
	Subject     string `json:"subject"`
	Capability  string `json:"capability"`
	RiskTier    string `json:"riskTier"`
	BudgetLimit int    `json:"budgetLimit"`
	ExpiresAt   string `json:"expiresAt"`
}

// AuditEntry represents a single hash-chained row in arsy_merkle_audit.
type AuditEntry struct {
	Seq       int64  `json:"seq"`
	ID        string `json:"id"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Resource  string `json:"resource"`
	RiskTier  string `json:"riskTier"`
	Decision  string `json:"decision"`
	PrevHash  string `json:"prevHash"`
	EntryHash string `json:"entryHash"`
	CreatedAt string `json:"createdAt"`
}

// EvaluationResult captures the outcome of CheckAndConsume.
type EvaluationResult struct {
	Allowed         bool       `json:"allowed"`
	Decision        string     `json:"decision"`
	Reason          string     `json:"reason"`
	GrantID         string     `json:"grantId,omitempty"`
	BudgetRemaining int        `json:"budgetRemaining"`
	AuditEntry      AuditEntry `json:"auditEntry"`
}

// ChainVerification summarizes SHA-256 Merkle audit chain integrity.
type ChainVerification struct {
	Valid     bool   `json:"valid"`
	Count     int    `json:"count"`
	HeadHash  string `json:"headHash"`
	BrokenSeq int64  `json:"brokenSeq"`
}

// NewManager initializes the Fleet & ARSY manager and migrates required SQLite tables.
func NewManager(hub *store.HubStore, opts Options) (*Manager, error) {
	if hub == nil {
		return nil, errors.New("hub store is required")
	}
	probeLimit := opts.ProbeTimeout
	if probeLimit <= 0 {
		probeLimit = defaultProbeWindow
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: probeLimit}
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}

	mgr := &Manager{
		hub:        hub,
		probeLimit: probeLimit,
		httpClient: httpClient,
		nowFn:      nowFn,
	}
	if err := mgr.migrate(); err != nil {
		return nil, err
	}
	return mgr, nil
}

func (m *Manager) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS arsy_grants (
  id TEXT PRIMARY KEY,
  subject TEXT NOT NULL,
  capability TEXT NOT NULL,
  risk_tier TEXT NOT NULL,
  budget_limit INTEGER NOT NULL,
  budget_used INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active',
  expires_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_arsy_grants_subject_cap ON arsy_grants(subject, capability, status);

CREATE TABLE IF NOT EXISTS arsy_merkle_audit (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  resource TEXT NOT NULL,
  risk_tier TEXT NOT NULL,
  decision TEXT NOT NULL,
  prev_hash TEXT NOT NULL,
  entry_hash TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS fleet_services (
  key TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  env_var TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`
	if _, err := m.hub.DB().Exec(schema); err != nil {
		return fmt.Errorf("migrate fleet & arsy schema: %w", err)
	}
	return nil
}

func normalizeRiskTier(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "R0", "R1", "R2", "R3", "R4":
		return strings.ToUpper(strings.TrimSpace(raw))
	default:
		return "R1"
	}
}

func riskTierRank(tier string) int {
	switch normalizeRiskTier(tier) {
	case "R0":
		return 0
	case "R1":
		return 1
	case "R2":
		return 2
	case "R3":
		return 3
	case "R4":
		return 4
	default:
		return 1
	}
}

func generateRandomID(prefix string) string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(raw[:]))
}

func computeAuditHash(prevHash, id, actor, action, resource, riskTier, decision, createdAt string) string {
	payload := strings.Join([]string{
		prevHash,
		id,
		actor,
		action,
		resource,
		riskTier,
		decision,
		createdAt,
	}, "|")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// IssueGrant creates a new active ARSY constitutional grant.
func (m *Manager) IssueGrant(ctx context.Context, input GrantInput) (Grant, error) {
	subject := strings.TrimSpace(input.Subject)
	capability := strings.TrimSpace(input.Capability)
	if subject == "" {
		return Grant{}, errors.New("subject is required")
	}
	if capability == "" {
		return Grant{}, errors.New("capability is required")
	}
	if input.BudgetLimit <= 0 {
		return Grant{}, errors.New("budgetLimit must be positive")
	}

	tier := normalizeRiskTier(input.RiskTier)
	expiresAt := strings.TrimSpace(input.ExpiresAt)
	if expiresAt != "" {
		if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
			return Grant{}, fmt.Errorf("invalid expiresAt RFC3339 timestamp: %w", err)
		}
	}

	grant := Grant{
		ID:          generateRandomID("grant"),
		Subject:     subject,
		Capability:  capability,
		RiskTier:    tier,
		BudgetLimit: input.BudgetLimit,
		BudgetUsed:  0,
		Status:      "active",
		ExpiresAt:   expiresAt,
		CreatedAt:   m.nowFn().Format(time.RFC3339Nano),
	}

	_, err := m.hub.DB().ExecContext(ctx, `
INSERT INTO arsy_grants (id, subject, capability, risk_tier, budget_limit, budget_used, status, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, grant.ID, grant.Subject, grant.Capability, grant.RiskTier, grant.BudgetLimit, grant.BudgetUsed, grant.Status, grant.ExpiresAt, grant.CreatedAt)
	if err != nil {
		return Grant{}, fmt.Errorf("insert arsy grant: %w", err)
	}
	return grant, nil
}

// ListGrants returns all ARSY grants ordered newest first.
func (m *Manager) ListGrants(ctx context.Context) ([]Grant, error) {
	rows, err := m.hub.DB().QueryContext(ctx, `
SELECT id, subject, capability, risk_tier, budget_limit, budget_used, status, expires_at, created_at
FROM arsy_grants
ORDER BY created_at DESC, id DESC
`)
	if err != nil {
		return nil, fmt.Errorf("list arsy grants: %w", err)
	}
	defer rows.Close()

	grants := make([]Grant, 0)
	for rows.Next() {
		var grant Grant
		if err := rows.Scan(
			&grant.ID,
			&grant.Subject,
			&grant.Capability,
			&grant.RiskTier,
			&grant.BudgetLimit,
			&grant.BudgetUsed,
			&grant.Status,
			&grant.ExpiresAt,
			&grant.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan arsy grant: %w", err)
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

// RevokeGrant marks an existing ARSY grant as revoked.
func (m *Manager) RevokeGrant(ctx context.Context, grantID string) error {
	cleanID := strings.TrimSpace(grantID)
	if cleanID == "" {
		return errors.New("grant id is required")
	}
	execResult, err := m.hub.DB().ExecContext(ctx, `
UPDATE arsy_grants SET status = 'revoked' WHERE id = ?
`, cleanID)
	if err != nil {
		return fmt.Errorf("revoke arsy grant: %w", err)
	}
	affected, _ := execResult.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CheckAndConsume evaluates an action against active ARSY grants, consumes budget on approval,
// and appends a SHA-256 hash-chained row to arsy_merkle_audit.
func (m *Manager) CheckAndConsume(
	ctx context.Context,
	subject string,
	capability string,
	riskTier string,
	resource string,
	costUnits int,
) (EvaluationResult, error) {
	cleanSubject := strings.TrimSpace(subject)
	cleanCapability := strings.TrimSpace(capability)
	cleanResource := strings.TrimSpace(resource)
	if cleanSubject == "" {
		return EvaluationResult{}, errors.New("subject is required")
	}
	if cleanCapability == "" {
		return EvaluationResult{}, errors.New("capability is required")
	}
	if costUnits < 0 {
		costUnits = 0
	}
	tier := normalizeRiskTier(riskTier)
	requestedRank := riskTierRank(tier)

	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.nowFn()
	nowRFC3339 := now.Format(time.RFC3339Nano)

	tx, err := m.hub.DB().BeginTx(ctx, nil)
	if err != nil {
		return EvaluationResult{}, fmt.Errorf("begin arsy evaluation tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
SELECT id, subject, capability, risk_tier, budget_limit, budget_used, status, expires_at, created_at
FROM arsy_grants
WHERE subject = ? AND (capability = ? OR capability = '*') AND status = 'active'
ORDER BY created_at DESC
`, cleanSubject, cleanCapability)
	if err != nil {
		return EvaluationResult{}, fmt.Errorf("query active grants: %w", err)
	}

	var matchedGrant *Grant
	var budgetExceededGrant *Grant
	var tierInsufficientGrant *Grant

	for rows.Next() {
		var candidate Grant
		if scanErr := rows.Scan(
			&candidate.ID,
			&candidate.Subject,
			&candidate.Capability,
			&candidate.RiskTier,
			&candidate.BudgetLimit,
			&candidate.BudgetUsed,
			&candidate.Status,
			&candidate.ExpiresAt,
			&candidate.CreatedAt,
		); scanErr != nil {
			_ = rows.Close()
			return EvaluationResult{}, fmt.Errorf("scan candidate grant: %w", scanErr)
		}
		if candidate.ExpiresAt != "" {
			if expTime, parseErr := time.Parse(time.RFC3339, candidate.ExpiresAt); parseErr == nil && now.After(expTime) {
				continue
			}
		}
		if riskTierRank(candidate.RiskTier) < requestedRank {
			if tierInsufficientGrant == nil {
				copied := candidate
				tierInsufficientGrant = &copied
			}
			continue
		}
		if candidate.BudgetUsed+costUnits > candidate.BudgetLimit {
			if budgetExceededGrant == nil {
				copied := candidate
				budgetExceededGrant = &copied
			}
			continue
		}
		copied := candidate
		matchedGrant = &copied
		break
	}
	if closeErr := rows.Close(); closeErr != nil {
		return EvaluationResult{}, closeErr
	}

	outcome := EvaluationResult{
		Allowed:  false,
		Decision: "deny",
	}

	if matchedGrant != nil {
		newUsed := matchedGrant.BudgetUsed + costUnits
		if _, err := tx.ExecContext(ctx, `
UPDATE arsy_grants SET budget_used = ? WHERE id = ?
`, newUsed, matchedGrant.ID); err != nil {
			return EvaluationResult{}, fmt.Errorf("consume grant budget: %w", err)
		}
		outcome.Allowed = true
		outcome.Decision = "allow"
		outcome.Reason = "approved by active ARSY grant"
		outcome.GrantID = matchedGrant.ID
		outcome.BudgetRemaining = matchedGrant.BudgetLimit - newUsed
	} else if budgetExceededGrant != nil {
		outcome.Reason = fmt.Sprintf("grant %s budget exhausted (%d/%d used, requested %d)",
			budgetExceededGrant.ID, budgetExceededGrant.BudgetUsed, budgetExceededGrant.BudgetLimit, costUnits)
		outcome.GrantID = budgetExceededGrant.ID
		outcome.BudgetRemaining = budgetExceededGrant.BudgetLimit - budgetExceededGrant.BudgetUsed
	} else if tierInsufficientGrant != nil {
		outcome.Reason = fmt.Sprintf("risk tier %s denied: grant %s only authorizes up to %s",
			tier, tierInsufficientGrant.ID, tierInsufficientGrant.RiskTier)
	} else if requestedRank >= riskTierRank("R3") {
		outcome.Reason = fmt.Sprintf("high-risk tier %s denied: explicit active ARSY grant required", tier)
	} else {
		outcome.Reason = fmt.Sprintf("no active ARSY grant for subject %s capability %s", cleanSubject, cleanCapability)
	}

	prevHash := genesisHash
	var lastHash string
	err = tx.QueryRowContext(ctx, `SELECT entry_hash FROM arsy_merkle_audit ORDER BY seq DESC LIMIT 1`).Scan(&lastHash)
	if err == nil && strings.TrimSpace(lastHash) != "" {
		prevHash = lastHash
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return EvaluationResult{}, fmt.Errorf("read merkle chain head: %w", err)
	}

	auditID := generateRandomID("audit")
	entryHash := computeAuditHash(
		prevHash,
		auditID,
		cleanSubject,
		cleanCapability,
		cleanResource,
		tier,
		outcome.Decision,
		nowRFC3339,
	)

	insertResult, err := tx.ExecContext(ctx, `
INSERT INTO arsy_merkle_audit (id, actor, action, resource, risk_tier, decision, prev_hash, entry_hash, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, auditID, cleanSubject, cleanCapability, cleanResource, tier, outcome.Decision, prevHash, entryHash, nowRFC3339)
	if err != nil {
		return EvaluationResult{}, fmt.Errorf("append arsy merkle audit: %w", err)
	}
	seq, _ := insertResult.LastInsertId()

	if err := tx.Commit(); err != nil {
		return EvaluationResult{}, fmt.Errorf("commit arsy evaluation: %w", err)
	}

	outcome.AuditEntry = AuditEntry{
		Seq:       seq,
		ID:        auditID,
		Actor:     cleanSubject,
		Action:    cleanCapability,
		Resource:  cleanResource,
		RiskTier:  tier,
		Decision:  outcome.Decision,
		PrevHash:  prevHash,
		EntryHash: entryHash,
		CreatedAt: nowRFC3339,
	}
	return outcome, nil
}

// VerifyAuditChain walks the entire arsy_merkle_audit table and verifies SHA-256 hash links.
func (m *Manager) VerifyAuditChain(ctx context.Context) (ChainVerification, error) {
	rows, err := m.hub.DB().QueryContext(ctx, `
SELECT seq, id, actor, action, resource, risk_tier, decision, prev_hash, entry_hash, created_at
FROM arsy_merkle_audit
ORDER BY seq ASC
`)
	if err != nil {
		return ChainVerification{}, fmt.Errorf("query arsy merkle audit: %w", err)
	}
	defer rows.Close()

	expectedPrev := genesisHash
	headHash := genesisHash
	count := 0

	for rows.Next() {
		var entry AuditEntry
		if err := rows.Scan(
			&entry.Seq,
			&entry.ID,
			&entry.Actor,
			&entry.Action,
			&entry.Resource,
			&entry.RiskTier,
			&entry.Decision,
			&entry.PrevHash,
			&entry.EntryHash,
			&entry.CreatedAt,
		); err != nil {
			return ChainVerification{}, fmt.Errorf("scan audit row: %w", err)
		}
		count++
		recomputed := computeAuditHash(
			entry.PrevHash,
			entry.ID,
			entry.Actor,
			entry.Action,
			entry.Resource,
			entry.RiskTier,
			entry.Decision,
			entry.CreatedAt,
		)
		if entry.PrevHash != expectedPrev || entry.EntryHash != recomputed {
			return ChainVerification{
				Valid:     false,
				Count:     count,
				HeadHash:  headHash,
				BrokenSeq: entry.Seq,
			}, nil
		}
		expectedPrev = entry.EntryHash
		headHash = entry.EntryHash
	}
	if err := rows.Err(); err != nil {
		return ChainVerification{}, err
	}
	return ChainVerification{
		Valid:     true,
		Count:     count,
		HeadHash:  headHash,
		BrokenSeq: 0,
	}, nil
}

// ListAuditLogs returns the most recent ARSY Merkle audit entries.
func (m *Manager) ListAuditLogs(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := m.hub.DB().QueryContext(ctx, `
SELECT seq, id, actor, action, resource, risk_tier, decision, prev_hash, entry_hash, created_at
FROM arsy_merkle_audit
ORDER BY seq DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, fmt.Errorf("list arsy audit logs: %w", err)
	}
	defer rows.Close()

	entries := make([]AuditEntry, 0)
	for rows.Next() {
		var entry AuditEntry
		if err := rows.Scan(
			&entry.Seq,
			&entry.ID,
			&entry.Actor,
			&entry.Action,
			&entry.Resource,
			&entry.RiskTier,
			&entry.Decision,
			&entry.PrevHash,
			&entry.EntryHash,
			&entry.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
