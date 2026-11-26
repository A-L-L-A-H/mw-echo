package desktop

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

const (
	defaultMaxLogEntries   = 500
	defaultLogListLimit    = 100
	maxLogListLimit        = 500
	maxPersistedTextLength = 16384
)

// Options configures the desktop runtime manager, ring buffer, and LocalAI discovery client.
type Options struct {
	MaxLogEntries   int
	KnownSecrets    []string
	DefaultMode     string
	HTTPClient      *http.Client
	LocalAITimeout  time.Duration
	EasyTierBinPath string
	CommandStarter  func(name string, args ...string) error
}

// LogEntry represents a redacted desktop runtime log record stored in memory and SQLite.
type LogEntry struct {
	ID        string `json:"id"`
	Level     string `json:"level"`
	Source    string `json:"source"`
	Message   string `json:"message"`
	Detail    string `json:"detail"`
	Redacted  bool   `json:"redacted"`
	CreatedAt string `json:"createdAt"`
}

// Manager coordinates desktop settings, redacted log hub, diagnostics, and local AI discovery.
type Manager struct {
	hub             *store.HubStore
	db              *sql.DB
	dataDir         string
	maxEntries      int
	knownSecrets    []string
	defaultMode     string
	httpClient      *http.Client
	localAITimeout  time.Duration
	easytierBinPath string
	commandStarter  func(name string, args ...string) error
	startedAt       time.Time

	mu   sync.RWMutex
	ring []LogEntry
	seq  uint64
}

// NewManager migrates desktop_settings and desktop_logs, loads recent log entries into the ring buffer, and returns a Manager.
func NewManager(hub *store.HubStore, opts Options) (*Manager, error) {
	if hub == nil || hub.DB() == nil {
		return nil, errors.New("desktop: hub store is required")
	}

	maxEntries := opts.MaxLogEntries
	if maxEntries <= 0 {
		maxEntries = defaultMaxLogEntries
	}

	defaultMode := ModeDeveloper
	if normalized, ok := normalizeDesktopMode(opts.DefaultMode); ok {
		defaultMode = normalized
	}

	localAITimeout := opts.LocalAITimeout
	if localAITimeout <= 0 {
		localAITimeout = defaultLocalAITimeout
	}

	knownSecrets := make([]string, 0, len(opts.KnownSecrets))
	for _, secret := range opts.KnownSecrets {
		if trimmed := strings.TrimSpace(secret); trimmed != "" {
			knownSecrets = append(knownSecrets, trimmed)
		}
	}

	mgr := &Manager{
		hub:             hub,
		db:              hub.DB(),
		dataDir:         hub.DataDir(),
		maxEntries:      maxEntries,
		knownSecrets:    knownSecrets,
		defaultMode:     defaultMode,
		httpClient:      opts.HTTPClient,
		localAITimeout:  localAITimeout,
		easytierBinPath: strings.TrimSpace(opts.EasyTierBinPath),
		commandStarter:  opts.CommandStarter,
		startedAt:       time.Now().UTC(),
		ring:            make([]LogEntry, 0, maxEntries),
	}

	if err := mgr.migrate(); err != nil {
		return nil, fmt.Errorf("desktop migrate: %w", err)
	}
	if err := mgr.warmRingBuffer(context.Background()); err != nil {
		return nil, fmt.Errorf("desktop warm log buffer: %w", err)
	}
	return mgr, nil
}

func (m *Manager) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS desktop_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS desktop_logs (
  id TEXT PRIMARY KEY,
  level TEXT NOT NULL,
  source TEXT NOT NULL,
  message TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  redacted INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_desktop_logs_created_at ON desktop_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_desktop_logs_level_source ON desktop_logs(level, source);
`
	_, err := m.db.Exec(schema)
	return err
}

func (m *Manager) warmRingBuffer(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `
SELECT id, level, source, message, detail, redacted, created_at
FROM desktop_logs
ORDER BY created_at DESC, rowid DESC
LIMIT ?
`, m.maxEntries)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	loadedDesc := make([]LogEntry, 0, m.maxEntries)
	for rows.Next() {
		var entry LogEntry
		var redactedInt int
		if scanErr := rows.Scan(
			&entry.ID,
			&entry.Level,
			&entry.Source,
			&entry.Message,
			&entry.Detail,
			&redactedInt,
			&entry.CreatedAt,
		); scanErr != nil {
			return scanErr
		}
		entry.Redacted = redactedInt != 0
		loadedDesc = append(loadedDesc, entry)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.ring = make([]LogEntry, 0, m.maxEntries)
	for idx := len(loadedDesc) - 1; idx >= 0; idx-- {
		m.ring = append(m.ring, loadedDesc[idx])
	}
	return nil
}

func normalizeLogLevel(raw string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "", "info":
		return "info", nil
	case "warn", "warning":
		return "warn", nil
	case "error", "err":
		return "error", nil
	case "debug":
		return "debug", nil
	default:
		return "", fmt.Errorf("%w: unsupported log level %q", ErrValidation, raw)
	}
}

func normalizeLogSource(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return "desktop"
	}
	return normalized
}

func sanitizeLogText(input string) string {
	var builder strings.Builder
	builder.Grow(len(input))
	for _, ch := range input {
		if ch == '\n' || ch == '\r' || ch == '\t' || (!unicode.IsControl(ch) && ch != 0x7f) {
			builder.WriteRune(ch)
		}
	}
	cleaned := builder.String()
	runes := []rune(cleaned)
	if len(runes) > maxPersistedTextLength {
		return string(runes[:maxPersistedTextLength]) + "...[truncated]"
	}
	return cleaned
}

func (m *Manager) nextLogID(now time.Time) string {
	m.mu.Lock()
	m.seq++
	seq := m.seq
	m.mu.Unlock()

	var randSuffix [4]byte
	_, _ = rand.Read(randSuffix[:])
	return fmt.Sprintf("dlog-%d-%d-%s", now.UnixMilli(), seq, hex.EncodeToString(randSuffix[:]))
}

// Log sanitizes, redacts sensitive tokens, persists to SQLite desktop_logs, and appends to the in-memory ring buffer.
func (m *Manager) Log(ctx context.Context, level, source, message, detail string) (LogEntry, error) {
	normLevel, levelErr := normalizeLogLevel(level)
	if levelErr != nil {
		return LogEntry{}, levelErr
	}
	normSource := normalizeLogSource(source)

	cleanedMsg := strings.TrimSpace(sanitizeLogText(message))
	if cleanedMsg == "" {
		return LogEntry{}, fmt.Errorf("%w: log message is required", ErrValidation)
	}
	cleanedDetail := sanitizeLogText(detail)

	redactedMsg, msgChanged := RedactSensitiveText(cleanedMsg, m.knownSecrets...)
	redactedDetail, detailChanged := RedactSensitiveText(cleanedDetail, m.knownSecrets...)
	wasRedacted := msgChanged || detailChanged

	now := time.Now().UTC()
	entry := LogEntry{
		ID:        m.nextLogID(now),
		Level:     normLevel,
		Source:    normSource,
		Message:   redactedMsg,
		Detail:    redactedDetail,
		Redacted:  wasRedacted,
		CreatedAt: now.Format(time.RFC3339Nano),
	}

	redactedInt := 0
	if wasRedacted {
		redactedInt = 1
	}

	if _, execErr := m.db.ExecContext(ctx, `
INSERT INTO desktop_logs (id, level, source, message, detail, redacted, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, entry.ID, entry.Level, entry.Source, entry.Message, entry.Detail, redactedInt, entry.CreatedAt); execErr != nil {
		return LogEntry{}, fmt.Errorf("insert desktop log: %w", execErr)
	}

	m.mu.Lock()
	m.ring = append(m.ring, entry)
	if len(m.ring) > m.maxEntries {
		excess := len(m.ring) - m.maxEntries
		m.ring = append([]LogEntry(nil), m.ring[excess:]...)
	}
	m.mu.Unlock()

	return entry, nil
}

// ListLogs queries persisted desktop logs filtered by optional level and source, ordered newest first.
func (m *Manager) ListLogs(ctx context.Context, level, source string, limit int) ([]LogEntry, error) {
	if limit <= 0 {
		limit = defaultLogListLimit
	}
	if limit > maxLogListLimit {
		limit = maxLogListLimit
	}

	var clauses []string
	var queryArgs []any

	trimmedLevel := strings.ToLower(strings.TrimSpace(level))
	if trimmedLevel != "" && trimmedLevel != "all" {
		normLevel, err := normalizeLogLevel(trimmedLevel)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, "level = ?")
		queryArgs = append(queryArgs, normLevel)
	}

	trimmedSource := strings.ToLower(strings.TrimSpace(source))
	if trimmedSource != "" && trimmedSource != "all" {
		clauses = append(clauses, "source = ?")
		queryArgs = append(queryArgs, trimmedSource)
	}

	var queryBuilder strings.Builder
	queryBuilder.WriteString(`SELECT id, level, source, message, detail, redacted, created_at FROM desktop_logs`)
	if len(clauses) > 0 {
		queryBuilder.WriteString(" WHERE ")
		queryBuilder.WriteString(strings.Join(clauses, " AND "))
	}
	queryBuilder.WriteString(" ORDER BY created_at DESC, rowid DESC LIMIT ?")
	queryArgs = append(queryArgs, limit)

	rows, err := m.db.QueryContext(ctx, queryBuilder.String(), queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("query desktop logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	entries := make([]LogEntry, 0)
	for rows.Next() {
		var entry LogEntry
		var redactedInt int
		if scanErr := rows.Scan(
			&entry.ID,
			&entry.Level,
			&entry.Source,
			&entry.Message,
			&entry.Detail,
			&redactedInt,
			&entry.CreatedAt,
		); scanErr != nil {
			return nil, fmt.Errorf("scan desktop log: %w", scanErr)
		}
		entry.Redacted = redactedInt != 0
		entries = append(entries, entry)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return entries, nil
}

// RingEntries returns a copy of the in-memory ring buffer entries from oldest to newest.
func (m *Manager) RingEntries() []LogEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	copied := make([]LogEntry, len(m.ring))
	copy(copied, m.ring)
	return copied
}
