package vaultenv

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/storage"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

const (
	EnvDev     = "dev"
	EnvStaging = "staging"
	EnvProd    = "prod"
	EnvShared  = "shared"

	defaultProject    = "default"
	defaultAuditLimit = 50
	maxAuditLimit     = 500
	backupMimeType    = "application/octet-stream"
)

var (
	// ErrValidation indicates invalid project, environment, key name, or .env syntax.
	ErrValidation = errors.New("vaultenv validation failed")
	// ErrNotFound indicates the requested secret key does not exist.
	ErrNotFound = errors.New("secret not found")
	// ErrBackupUnavailable indicates no backup uploader callback was configured.
	ErrBackupUnavailable = errors.New("backup uploader is not configured")

	keyNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// UploadBackupFunc uploads an encrypted .env snapshot to external or pooled storage and returns its object ID.
type UploadBackupFunc func(ctx context.Context, project, logicalPath, filename, mimeType string, content []byte) (string, error)

// Options configures optional integrations for the secret vault.
type Options struct {
	UploadBackup UploadBackupFunc
}

// SecretKeyMeta represents redacted secret metadata returned by list and write operations.
type SecretKeyMeta struct {
	ID            string `json:"id"`
	Project       string `json:"project"`
	EnvName       string `json:"envName"`
	KeyName       string `json:"keyName"`
	MaskedPreview string `json:"maskedPreview"`
	Note          string `json:"note"`
	UpdatedAt     string `json:"updatedAt"`
}

// SetSecretInput defines parameters for creating or updating a single encrypted secret.
type SetSecretInput struct {
	Project string `json:"project"`
	EnvName string `json:"envName"`
	KeyName string `json:"keyName"`
	Value   string `json:"value"`
	Note    string `json:"note"`
}

// BulkImportInput defines parameters for importing a raw .env document into an environment.
type BulkImportInput struct {
	Project   string `json:"project"`
	EnvName   string `json:"envName"`
	RawDotEnv string `json:"rawDotEnv"`
}

// BulkImportResult summarizes keys imported from a .env payload.
type BulkImportResult struct {
	Project  string          `json:"project"`
	EnvName  string          `json:"envName"`
	Imported int             `json:"imported"`
	Skipped  int             `json:"skipped"`
	Keys     []SecretKeyMeta `json:"keys"`
}

// RevealResult contains a single decrypted secret value returned by RevealSecret.
type RevealResult struct {
	ID        string `json:"id"`
	Project   string `json:"project"`
	EnvName   string `json:"envName"`
	KeyName   string `json:"keyName"`
	Value     string `json:"value"`
	Note      string `json:"note"`
	UpdatedAt string `json:"updatedAt"`
}

// AuditLogRecord captures a persisted vault mutation, reveal, export, or backup event.
type AuditLogRecord struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	Project   string `json:"project"`
	EnvName   string `json:"envName"`
	KeyName   string `json:"keyName"`
	Detail    string `json:"detail"`
	CreatedAt string `json:"createdAt"`
}

// BackupSnapshotResult describes an encrypted .env backup uploaded via Options.UploadBackup.
type BackupSnapshotResult struct {
	Project        string `json:"project"`
	EnvName        string `json:"envName"`
	ObjectID       string `json:"objectId"`
	LogicalPath    string `json:"logicalPath"`
	Filename       string `json:"filename"`
	KeyCount       int    `json:"keyCount"`
	EncryptedBytes int    `json:"encryptedBytes"`
	CreatedAt      string `json:"createdAt"`
}

type dotEnvEntry struct {
	keyName string
	value   string
}

// Vault provides AES-256-GCM encrypted .env secret storage, audit trails, and encrypted pool backups.
type Vault struct {
	hub          *store.HubStore
	db           *sql.DB
	box          *storage.SecretBox
	uploadBackup UploadBackupFunc
}

// NewVault opens the HubStore SecretBox, migrates vault tables, and returns a configured Vault.
func NewVault(hub *store.HubStore, opts Options) (*Vault, error) {
	if hub == nil || hub.DB() == nil {
		return nil, errors.New("vaultenv: hub store is required")
	}
	box, err := storage.OpenSecretBox(hub.DataDir())
	if err != nil {
		return nil, fmt.Errorf("vaultenv: open secret box: %w", err)
	}

	vault := &Vault{
		hub:          hub,
		db:           hub.DB(),
		box:          box,
		uploadBackup: opts.UploadBackup,
	}
	if err := vault.migrate(); err != nil {
		return nil, fmt.Errorf("vaultenv: migrate: %w", err)
	}
	return vault, nil
}

func (v *Vault) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS env_secrets (
  id TEXT PRIMARY KEY,
  project TEXT NOT NULL,
  env_name TEXT NOT NULL,
  key_name TEXT NOT NULL,
  value_enc TEXT NOT NULL,
  masked_preview TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  UNIQUE(project, env_name, key_name)
);

CREATE INDEX IF NOT EXISTS idx_env_secrets_scope
  ON env_secrets(project, env_name, key_name);

CREATE TABLE IF NOT EXISTS env_audit_logs (
  id TEXT PRIMARY KEY,
  action TEXT NOT NULL,
  project TEXT NOT NULL,
  env_name TEXT NOT NULL,
  key_name TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_env_audit_logs_project_time
  ON env_audit_logs(project, created_at DESC);
`
	_, err := v.db.Exec(schema)
	return err
}

// SetSecret encrypts and stores a secret key-value pair for a project environment.
func (v *Vault) SetSecret(ctx context.Context, input SetSecretInput) (SecretKeyMeta, error) {
	project, err := normalizeProject(input.Project)
	if err != nil {
		return SecretKeyMeta{}, err
	}
	envName := normalizeEnvName(input.EnvName)
	keyName, err := validateKeyName(input.KeyName)
	if err != nil {
		return SecretKeyMeta{}, err
	}

	encryptedValue, err := v.box.Encrypt([]byte(input.Value))
	if err != nil {
		return SecretKeyMeta{}, fmt.Errorf("encrypt secret %s: %w", keyName, err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := SecretKeyMeta{
		ID:            deriveSecretID(project, envName, keyName),
		Project:       project,
		EnvName:       envName,
		KeyName:       keyName,
		MaskedPreview: maskSecretValue(input.Value),
		Note:          strings.TrimSpace(input.Note),
		UpdatedAt:     now,
	}

	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretKeyMeta{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := upsertSecretTx(ctx, tx, meta, encryptedValue); err != nil {
		return SecretKeyMeta{}, err
	}
	if err := insertAuditTx(ctx, tx, "set", project, envName, keyName, "encrypted secret upserted", now); err != nil {
		return SecretKeyMeta{}, err
	}
	if err := tx.Commit(); err != nil {
		return SecretKeyMeta{}, err
	}
	return meta, nil
}

// BulkImportDotEnv parses a raw .env payload, encrypts each entry, and upserts all keys atomically.
func (v *Vault) BulkImportDotEnv(ctx context.Context, project, envName, rawDotEnv string) (BulkImportResult, error) {
	cleanProject, err := normalizeProject(project)
	if err != nil {
		return BulkImportResult{}, err
	}
	cleanEnv := normalizeEnvName(envName)

	entries, skippedLines, err := parseDotEnv(rawDotEnv)
	if err != nil {
		return BulkImportResult{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	importedMetas := make([]SecretKeyMeta, 0, len(entries))

	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return BulkImportResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, entry := range entries {
		encryptedValue, encErr := v.box.Encrypt([]byte(entry.value))
		if encErr != nil {
			return BulkImportResult{}, fmt.Errorf("encrypt secret %s: %w", entry.keyName, encErr)
		}
		meta := SecretKeyMeta{
			ID:            deriveSecretID(cleanProject, cleanEnv, entry.keyName),
			Project:       cleanProject,
			EnvName:       cleanEnv,
			KeyName:       entry.keyName,
			MaskedPreview: maskSecretValue(entry.value),
			Note:          "",
			UpdatedAt:     now,
		}
		if err := upsertSecretTx(ctx, tx, meta, encryptedValue); err != nil {
			return BulkImportResult{}, err
		}
		importedMetas = append(importedMetas, meta)
	}

	detail := fmt.Sprintf("imported %d key(s), skipped %d blank/comment line(s)", len(importedMetas), skippedLines)
	if err := insertAuditTx(ctx, tx, "import", cleanProject, cleanEnv, "", detail, now); err != nil {
		return BulkImportResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return BulkImportResult{}, err
	}

	return BulkImportResult{
		Project:  cleanProject,
		EnvName:  cleanEnv,
		Imported: len(importedMetas),
		Skipped:  skippedLines,
		Keys:     importedMetas,
	}, nil
}

// ListKeys returns redacted secret metadata for a project environment without decrypting any ciphertexts.
func (v *Vault) ListKeys(ctx context.Context, project, envName string) ([]SecretKeyMeta, error) {
	cleanProject, err := normalizeProject(project)
	if err != nil {
		return nil, err
	}
	cleanEnv := normalizeEnvName(envName)

	rows, err := v.db.QueryContext(ctx, `
SELECT id, project, env_name, key_name, masked_preview, note, updated_at
FROM env_secrets
WHERE project = ? AND env_name = ?
ORDER BY key_name ASC`, cleanProject, cleanEnv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := make([]SecretKeyMeta, 0)
	for rows.Next() {
		var meta SecretKeyMeta
		if scanErr := rows.Scan(
			&meta.ID,
			&meta.Project,
			&meta.EnvName,
			&meta.KeyName,
			&meta.MaskedPreview,
			&meta.Note,
			&meta.UpdatedAt,
		); scanErr != nil {
			return nil, scanErr
		}
		keys = append(keys, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

// RevealSecret decrypts a single secret value and records a reveal audit log entry.
func (v *Vault) RevealSecret(ctx context.Context, project, envName, keyName string) (RevealResult, error) {
	cleanProject, err := normalizeProject(project)
	if err != nil {
		return RevealResult{}, err
	}
	cleanEnv := normalizeEnvName(envName)
	cleanKey, err := validateKeyName(keyName)
	if err != nil {
		return RevealResult{}, err
	}

	var result RevealResult
	var encryptedValue string
	err = v.db.QueryRowContext(ctx, `
SELECT id, project, env_name, key_name, value_enc, note, updated_at
FROM env_secrets
WHERE project = ? AND env_name = ? AND key_name = ?`,
		cleanProject, cleanEnv, cleanKey,
	).Scan(
		&result.ID,
		&result.Project,
		&result.EnvName,
		&result.KeyName,
		&encryptedValue,
		&result.Note,
		&result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RevealResult{}, fmt.Errorf("%w: %s", ErrNotFound, cleanKey)
	}
	if err != nil {
		return RevealResult{}, err
	}

	plaintextBytes, err := v.box.Decrypt(encryptedValue)
	if err != nil {
		return RevealResult{}, fmt.Errorf("decrypt secret %s: %w", cleanKey, err)
	}
	result.Value = string(plaintextBytes)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := v.recordAudit(ctx, "reveal", cleanProject, cleanEnv, cleanKey, "secret value revealed", now); err != nil {
		return RevealResult{}, err
	}
	return result, nil
}

// ExportDotEnv decrypts all keys in a project environment into a sorted .env string and logs an export audit entry.
func (v *Vault) ExportDotEnv(ctx context.Context, project, envName string) (string, error) {
	cleanProject, err := normalizeProject(project)
	if err != nil {
		return "", err
	}
	cleanEnv := normalizeEnvName(envName)

	dotenvContent, keyCount, err := v.buildDotEnvBundle(ctx, cleanProject, cleanEnv)
	if err != nil {
		return "", err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	detail := fmt.Sprintf("exported %d key(s)", keyCount)
	if err := v.recordAudit(ctx, "export", cleanProject, cleanEnv, "", detail, now); err != nil {
		return "", err
	}
	return dotenvContent, nil
}

func (v *Vault) exportDotEnvWithCount(ctx context.Context, project, envName string) (string, string, string, int, error) {
	cleanProject, err := normalizeProject(project)
	if err != nil {
		return "", "", "", 0, err
	}
	cleanEnv := normalizeEnvName(envName)

	dotenvContent, keyCount, err := v.buildDotEnvBundle(ctx, cleanProject, cleanEnv)
	if err != nil {
		return "", "", "", 0, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	detail := fmt.Sprintf("exported %d key(s)", keyCount)
	if err := v.recordAudit(ctx, "export", cleanProject, cleanEnv, "", detail, now); err != nil {
		return "", "", "", 0, err
	}
	return cleanProject, cleanEnv, dotenvContent, keyCount, nil
}

func (v *Vault) buildDotEnvBundle(ctx context.Context, project, envName string) (string, int, error) {
	rows, err := v.db.QueryContext(ctx, `
SELECT key_name, value_enc
FROM env_secrets
WHERE project = ? AND env_name = ?
ORDER BY key_name ASC`, project, envName)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()

	var builder strings.Builder
	keyCount := 0
	for rows.Next() {
		var keyName string
		var encryptedValue string
		if scanErr := rows.Scan(&keyName, &encryptedValue); scanErr != nil {
			return "", 0, scanErr
		}
		plaintextBytes, decErr := v.box.Decrypt(encryptedValue)
		if decErr != nil {
			return "", 0, fmt.Errorf("decrypt secret %s: %w", keyName, decErr)
		}
		builder.WriteString(keyName)
		builder.WriteByte('=')
		builder.WriteString(formatDotEnvValue(string(plaintextBytes)))
		builder.WriteByte('\n')
		keyCount++
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	return builder.String(), keyCount, nil
}

// DeleteSecret removes a secret from a project environment and records a delete audit log entry.
func (v *Vault) DeleteSecret(ctx context.Context, project, envName, keyName string) error {
	cleanProject, err := normalizeProject(project)
	if err != nil {
		return err
	}
	cleanEnv := normalizeEnvName(envName)
	cleanKey, err := validateKeyName(keyName)
	if err != nil {
		return err
	}

	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	execResult, err := tx.ExecContext(ctx, `
DELETE FROM env_secrets
WHERE project = ? AND env_name = ? AND key_name = ?`,
		cleanProject, cleanEnv, cleanKey)
	if err != nil {
		return err
	}
	affected, err := execResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, cleanKey)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := insertAuditTx(ctx, tx, "delete", cleanProject, cleanEnv, cleanKey, "secret deleted", now); err != nil {
		return err
	}
	return tx.Commit()
}

// ListAuditLogs returns recent audit log entries ordered newest first.
func (v *Vault) ListAuditLogs(ctx context.Context, project string, limit int) ([]AuditLogRecord, error) {
	resolvedLimit := limit
	if resolvedLimit <= 0 {
		resolvedLimit = defaultAuditLimit
	}
	if resolvedLimit > maxAuditLimit {
		resolvedLimit = maxAuditLimit
	}

	trimmedProject := strings.TrimSpace(project)
	var rows *sql.Rows
	var err error
	if trimmedProject != "" {
		rows, err = v.db.QueryContext(ctx, `
SELECT id, action, project, env_name, key_name, detail, created_at
FROM env_audit_logs
WHERE project = ?
ORDER BY created_at DESC, rowid DESC
LIMIT ?`, trimmedProject, resolvedLimit)
	} else {
		rows, err = v.db.QueryContext(ctx, `
SELECT id, action, project, env_name, key_name, detail, created_at
FROM env_audit_logs
ORDER BY created_at DESC, rowid DESC
LIMIT ?`, resolvedLimit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	logs := make([]AuditLogRecord, 0)
	for rows.Next() {
		var entry AuditLogRecord
		if scanErr := rows.Scan(
			&entry.ID,
			&entry.Action,
			&entry.Project,
			&entry.EnvName,
			&entry.KeyName,
			&entry.Detail,
			&entry.CreatedAt,
		); scanErr != nil {
			return nil, scanErr
		}
		logs = append(logs, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return logs, nil
}

// BackupEncryptedToPool encrypts the full environment .env bundle with SecretBox and uploads it via Options.UploadBackup.
func (v *Vault) BackupEncryptedToPool(ctx context.Context, project, envName string) (BackupSnapshotResult, error) {
	if v.uploadBackup == nil {
		return BackupSnapshotResult{}, ErrBackupUnavailable
	}
	cleanProject, err := normalizeProject(project)
	if err != nil {
		return BackupSnapshotResult{}, err
	}
	cleanEnv := normalizeEnvName(envName)

	bundleText, keyCount, err := v.buildDotEnvBundle(ctx, cleanProject, cleanEnv)
	if err != nil {
		return BackupSnapshotResult{}, err
	}

	encryptedBundle, err := v.box.Encrypt([]byte(bundleText))
	if err != nil {
		return BackupSnapshotResult{}, fmt.Errorf("encrypt backup bundle: %w", err)
	}
	encryptedPayload := []byte(encryptedBundle)

	nowTime := time.Now().UTC()
	tsSlug := nowTime.Format("20060102T150405Z")
	safeProject := sanitizeFilenameSegment(cleanProject)
	filename := fmt.Sprintf("%s-%s-%s.env.enc", safeProject, cleanEnv, tsSlug)
	logicalPath := fmt.Sprintf("vaultenv/%s/%s/%s", safeProject, cleanEnv, filename)

	objectID, err := v.uploadBackup(ctx, cleanProject, logicalPath, filename, backupMimeType, encryptedPayload)
	if err != nil {
		return BackupSnapshotResult{}, fmt.Errorf("upload encrypted backup: %w", err)
	}

	createdAt := nowTime.Format(time.RFC3339Nano)
	detail := fmt.Sprintf("objectId=%s keys=%d bytes=%d", objectID, keyCount, len(encryptedPayload))
	if err := v.recordAudit(ctx, "backup", cleanProject, cleanEnv, "", detail, createdAt); err != nil {
		return BackupSnapshotResult{}, err
	}

	return BackupSnapshotResult{
		Project:        cleanProject,
		EnvName:        cleanEnv,
		ObjectID:       objectID,
		LogicalPath:    logicalPath,
		Filename:       filename,
		KeyCount:       keyCount,
		EncryptedBytes: len(encryptedPayload),
		CreatedAt:      createdAt,
	}, nil
}

func upsertSecretTx(ctx context.Context, tx *sql.Tx, meta SecretKeyMeta, encryptedValue string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO env_secrets (id, project, env_name, key_name, value_enc, masked_preview, note, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project, env_name, key_name) DO UPDATE SET
  value_enc = excluded.value_enc,
  masked_preview = excluded.masked_preview,
  note = CASE WHEN excluded.note <> '' THEN excluded.note ELSE env_secrets.note END,
  updated_at = excluded.updated_at`,
		meta.ID,
		meta.Project,
		meta.EnvName,
		meta.KeyName,
		encryptedValue,
		meta.MaskedPreview,
		meta.Note,
		meta.UpdatedAt,
	)
	return err
}

func (v *Vault) recordAudit(ctx context.Context, action, project, envName, keyName, detail, createdAt string) error {
	_, err := v.db.ExecContext(ctx, `
INSERT INTO env_audit_logs (id, action, project, env_name, key_name, detail, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newAuditID(), action, project, envName, keyName, detail, createdAt)
	return err
}

func insertAuditTx(ctx context.Context, tx *sql.Tx, action, project, envName, keyName, detail, createdAt string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO env_audit_logs (id, action, project, env_name, key_name, detail, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newAuditID(), action, project, envName, keyName, detail, createdAt)
	return err
}

func normalizeProject(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultProject, nil
	}
	if strings.Contains(trimmed, "..") {
		return "", fmt.Errorf("%w: invalid project %q", ErrValidation, raw)
	}
	return trimmed, nil
}

func normalizeEnvName(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "staging", "stage":
		return EnvStaging
	case "prod", "production":
		return EnvProd
	case "shared", "common", "global":
		return EnvShared
	case "dev", "development", "":
		return EnvDev
	default:
		return EnvDev
	}
}

func validateKeyName(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if !keyNamePattern.MatchString(trimmed) {
		return "", fmt.Errorf("%w: invalid key name %q", ErrValidation, raw)
	}
	return trimmed, nil
}

func deriveSecretID(project, envName, keyName string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + envName + "\x00" + keyName))
	return "sec_" + hex.EncodeToString(sum[:12])
}

func newAuditID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("aud_%d", time.Now().UTC().UnixNano())
	}
	return "aud_" + hex.EncodeToString(buf)
}

func maskSecretValue(plaintext string) string {
	runes := []rune(plaintext)
	if len(runes) == 0 {
		return "(empty)"
	}
	if len(runes) <= 8 {
		return "****"
	}
	return string(runes[:2]) + "****" + string(runes[len(runes)-2:])
}

func sanitizeFilenameSegment(raw string) string {
	var builder strings.Builder
	for _, ch := range raw {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
			builder.WriteRune(ch)
		} else {
			builder.WriteByte('-')
		}
	}
	cleaned := strings.Trim(builder.String(), "-")
	if cleaned == "" {
		return defaultProject
	}
	return cleaned
}

func parseDotEnv(rawDotEnv string) ([]dotEnvEntry, int, error) {
	normalized := strings.ReplaceAll(rawDotEnv, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")

	entries := make([]dotEnvEntry, 0, len(lines))
	keyIndex := make(map[string]int, len(lines))
	skippedLines := 0

	for lineIdx, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			skippedLines++
			continue
		}

		if strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "export\t") {
			trimmed = strings.TrimSpace(trimmed[len("export"):])
		}

		eqIdx := strings.IndexByte(trimmed, '=')
		if eqIdx <= 0 {
			return nil, 0, fmt.Errorf("%w: line %d missing key=value assignment", ErrValidation, lineIdx+1)
		}

		rawKey := strings.TrimSpace(trimmed[:eqIdx])
		keyName, err := validateKeyName(rawKey)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: line %d: %v", ErrValidation, lineIdx+1, err)
		}

		rawVal := strings.TrimSpace(trimmed[eqIdx+1:])
		parsedVal := stripSurroundingQuotes(rawVal)

		if existingPos, exists := keyIndex[keyName]; exists {
			entries[existingPos].value = parsedVal
			continue
		}
		keyIndex[keyName] = len(entries)
		entries = append(entries, dotEnvEntry{
			keyName: keyName,
			value:   parsedVal,
		})
	}
	return entries, skippedLines, nil
}

func stripSurroundingQuotes(rawVal string) string {
	if len(rawVal) < 2 {
		return rawVal
	}
	first := rawVal[0]
	last := rawVal[len(rawVal)-1]
	if first == '"' && last == '"' {
		inner := rawVal[1 : len(rawVal)-1]
		return unescapeDoubleQuoted(inner)
	}
	if first == '\'' && last == '\'' {
		return rawVal[1 : len(rawVal)-1]
	}
	return rawVal
}

func unescapeDoubleQuoted(inner string) string {
	if !strings.ContainsRune(inner, '\\') {
		return inner
	}
	var builder strings.Builder
	builder.Grow(len(inner))
	for idx := 0; idx < len(inner); idx++ {
		ch := inner[idx]
		if ch != '\\' || idx+1 >= len(inner) {
			builder.WriteByte(ch)
			continue
		}
		next := inner[idx+1]
		switch next {
		case 'n':
			builder.WriteByte('\n')
			idx++
		case 'r':
			builder.WriteByte('\r')
			idx++
		case 't':
			builder.WriteByte('\t')
			idx++
		case '"':
			builder.WriteByte('"')
			idx++
		case '\\':
			builder.WriteByte('\\')
			idx++
		default:
			builder.WriteByte(ch)
		}
	}
	return builder.String()
}

func formatDotEnvValue(rawVal string) string {
	needsQuotes := strings.ContainsAny(rawVal, " \t\r\n#\"'\\") || rawVal != strings.TrimSpace(rawVal)
	if !needsQuotes {
		return rawVal
	}
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return `"` + replacer.Replace(rawVal) + `"`
}
