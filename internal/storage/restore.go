package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var coreRestoreAuditTables = []string{
	"tasks",
	"radar_events",
	"vault_secrets",
}

// RestoreSnapshotProvider downloads a stored SQLite backup snapshot by object ID or key.
type RestoreSnapshotProvider interface {
	OpenDownload(ctx context.Context, objectKey string) (PooledObject, io.ReadCloser, error)
}

// RestoreDrillReport summarizes the cryptographic and SQLite integrity verification of a backup snapshot.
type RestoreDrillReport struct {
	ObjectID       string           `json:"objectId,omitempty"`
	ObjectKey      string           `json:"objectKey,omitempty"`
	ProviderID     string           `json:"providerId,omitempty"`
	ProviderKind   string           `json:"providerKind,omitempty"`
	SizeBytes      int64            `json:"sizeBytes"`
	SHA256         string           `json:"sha256"`
	IntegrityCheck string           `json:"integrityCheck"`
	IntegrityOK    bool             `json:"integrityOk"`
	TableCounts    map[string]int64 `json:"tableCounts"`
	DurationMs     int64            `json:"durationMs"`
	VerifiedAt     string           `json:"verifiedAt"`
	CleanedUp      bool             `json:"cleanedUp"`
}

// VerifySQLiteSnapshot computes the SHA-256 checksum of a local SQLite snapshot file,
// executes PRAGMA integrity_check via modernc.org/sqlite, and counts rows in core tables.
func VerifySQLiteSnapshot(ctx context.Context, snapshotPath string) (RestoreDrillReport, error) {
	startedAt := time.Now().UTC()
	cleanedPath := strings.TrimSpace(snapshotPath)
	if cleanedPath == "" {
		return RestoreDrillReport{}, errors.New("snapshot path is required")
	}

	checksumHex, sizeBytes, err := hashFileSHA256(cleanedPath)
	if err != nil {
		return RestoreDrillReport{}, fmt.Errorf("hash snapshot: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", filepath.ToSlash(cleanedPath))
	snapshotDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return RestoreDrillReport{}, fmt.Errorf("open sqlite snapshot: %w", err)
	}
	defer func() { _ = snapshotDB.Close() }()

	integrityResult, err := runIntegrityCheck(ctx, snapshotDB)
	if err != nil {
		return RestoreDrillReport{}, err
	}

	tableCounts := make(map[string]int64, len(coreRestoreAuditTables))
	for _, tableName := range coreRestoreAuditTables {
		rowCount, countErr := countTableRowsIfExists(ctx, snapshotDB, tableName)
		if countErr != nil {
			return RestoreDrillReport{}, fmt.Errorf("count rows in %s: %w", tableName, countErr)
		}
		tableCounts[tableName] = rowCount
	}

	if closeErr := snapshotDB.Close(); closeErr != nil {
		return RestoreDrillReport{}, fmt.Errorf("close sqlite snapshot: %w", closeErr)
	}

	elapsedMs := time.Since(startedAt).Milliseconds()
	return RestoreDrillReport{
		ObjectKey:      filepath.Base(cleanedPath),
		SizeBytes:      sizeBytes,
		SHA256:         checksumHex,
		IntegrityCheck: integrityResult,
		IntegrityOK:    strings.EqualFold(strings.TrimSpace(integrityResult), "ok"),
		TableCounts:    tableCounts,
		DurationMs:     elapsedMs,
		VerifiedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// RunRestoreDrill downloads a snapshot from provider into tempDir, verifies its SHA-256 checksum
// and SQLite integrity, removes the temporary file, and returns the structured report.
func RunRestoreDrill(
	ctx context.Context,
	provider RestoreSnapshotProvider,
	objectKey string,
	tempDir string,
) (RestoreDrillReport, error) {
	if provider == nil {
		return RestoreDrillReport{}, errors.New("restore snapshot provider is required")
	}
	trimmedKey := strings.TrimSpace(objectKey)
	if trimmedKey == "" {
		return RestoreDrillReport{}, errors.New("object key is required")
	}

	startedAt := time.Now().UTC()
	objMeta, stream, err := provider.OpenDownload(ctx, trimmedKey)
	if err != nil {
		return RestoreDrillReport{}, fmt.Errorf("download snapshot %q: %w", trimmedKey, err)
	}

	workDir := strings.TrimSpace(tempDir)
	createdDir := false
	if workDir == "" {
		workDir, err = os.MkdirTemp("", "mw-echo-restore-drill-*")
		if err != nil {
			_ = stream.Close()
			return RestoreDrillReport{}, fmt.Errorf("create restore drill temp dir: %w", err)
		}
		createdDir = true
	} else {
		if mkErr := os.MkdirAll(workDir, 0o755); mkErr != nil {
			_ = stream.Close()
			return RestoreDrillReport{}, fmt.Errorf("ensure restore drill temp dir: %w", mkErr)
		}
	}

	tempFile, err := os.CreateTemp(workDir, "restore-drill-*.db")
	if err != nil {
		_ = stream.Close()
		if createdDir {
			_ = os.RemoveAll(workDir)
		}
		return RestoreDrillReport{}, fmt.Errorf("create restore drill temp file: %w", err)
	}
	tempPath := tempFile.Name()

	_, copyErr := io.Copy(tempFile, stream)
	closeStreamErr := stream.Close()
	closeFileErr := tempFile.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		if createdDir {
			_ = os.RemoveAll(workDir)
		}
		return RestoreDrillReport{}, fmt.Errorf("copy snapshot to temp file: %w", copyErr)
	}
	if closeStreamErr != nil {
		_ = os.Remove(tempPath)
		if createdDir {
			_ = os.RemoveAll(workDir)
		}
		return RestoreDrillReport{}, fmt.Errorf("close snapshot stream: %w", closeStreamErr)
	}
	if closeFileErr != nil {
		_ = os.Remove(tempPath)
		if createdDir {
			_ = os.RemoveAll(workDir)
		}
		return RestoreDrillReport{}, fmt.Errorf("close snapshot temp file: %w", closeFileErr)
	}

	report, verifyErr := VerifySQLiteSnapshot(ctx, tempPath)
	removeErr := os.Remove(tempPath)
	if createdDir {
		_ = os.RemoveAll(workDir)
	}
	if verifyErr != nil {
		return RestoreDrillReport{}, verifyErr
	}

	report.ObjectID = objMeta.ID
	if objMeta.LogicalPath != "" {
		report.ObjectKey = objMeta.LogicalPath
	} else {
		report.ObjectKey = trimmedKey
	}
	report.ProviderID = objMeta.ProviderID
	report.ProviderKind = objMeta.ProviderKind
	report.DurationMs = time.Since(startedAt).Milliseconds()
	report.CleanedUp = removeErr == nil
	return report, nil
}

// VerifyLatestBackupSnapshot runs a restore verification drill against the requested object ID,
// or automatically selects the newest system-backups mw-echo.db snapshot when objectID is empty.
func (p *PoolManager) VerifyLatestBackupSnapshot(ctx context.Context, objectID string) (RestoreDrillReport, error) {
	if p == nil {
		return RestoreDrillReport{}, errors.New("storage pool manager is not initialized")
	}
	targetID := strings.TrimSpace(objectID)
	if targetID == "" {
		candidates, err := p.ListObjects(ctx, backupProject, "mw-echo.db", 1)
		if err != nil {
			return RestoreDrillReport{}, fmt.Errorf("locate latest backup snapshot: %w", err)
		}
		if len(candidates) == 0 {
			return RestoreDrillReport{}, errors.New("no backup snapshots found in system-backups")
		}
		targetID = candidates[0].ID
	}
	return RunRestoreDrill(ctx, p, targetID, "")
}

func hashFileSHA256(filePath string) (string, int64, error) {
	fileHandle, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer fileHandle.Close()

	hasher := sha256.New()
	written, err := io.Copy(hasher, fileHandle)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), written, nil
}

func runIntegrityCheck(ctx context.Context, snapshotDB *sql.DB) (string, error) {
	rows, err := snapshotDB.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return "", fmt.Errorf("run pragma integrity_check: %w", err)
	}
	defer rows.Close()

	messages := make([]string, 0, 1)
	for rows.Next() {
		var line string
		if scanErr := rows.Scan(&line); scanErr != nil {
			return "", fmt.Errorf("scan integrity_check row: %w", scanErr)
		}
		messages = append(messages, line)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate integrity_check rows: %w", err)
	}
	if len(messages) == 0 {
		return "empty", nil
	}
	return strings.Join(messages, "; "), nil
}

func countTableRowsIfExists(ctx context.Context, snapshotDB *sql.DB, tableName string) (int64, error) {
	var existingName string
	err := snapshotDB.QueryRowContext(
		ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`,
		tableName,
	).Scan(&existingName)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	var rowCount int64
	if err := snapshotDB.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, tableName)).Scan(&rowCount); err != nil {
		return 0, err
	}
	return rowCount, nil
}
