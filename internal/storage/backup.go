package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	_ "modernc.org/sqlite"
)

const (
	backupProject  = "system-backups"
	sqliteMimeType = "application/vnd.sqlite3"
)

// BackupRequest specifies optional parameters for a database backup run.
type BackupRequest struct {
	ProviderID string `json:"providerId,omitempty"`
}

// BackupResult describes the SQLite snapshots uploaded to the 9Drive pool.
type BackupResult struct {
	Timestamp string         `json:"timestamp"`
	Backups   []PooledObject `json:"backups"`
	Count     int            `json:"count"`
}

// RegisterBackupRoutes registers POST /storage/backup on the provided /api Echo group.
func (p *PoolManager) RegisterBackupRoutes(api *echo.Group) {
	if api == nil {
		return
	}
	api.POST("/storage/backup", func(c echo.Context) error {
		var req BackupRequest
		if c.Request().Body != nil && c.Request().ContentLength != 0 {
			if err := c.Bind(&req); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid backup request payload"})
			}
		}
		res, err := p.backupDatabases(c.Request().Context(), req.ProviderID)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, res)
	})
}

// BackupDatabases safely snapshots mw-echo.db (and whatsapp_sessions.db if present)
// via SQLite VACUUM INTO, uploads each snapshot to the 9Drive pool under system-backups,
// and always removes all temporary snapshot files before returning.
func (p *PoolManager) BackupDatabases(ctx context.Context) ([]PooledObject, error) {
	res, err := p.backupDatabases(ctx, "")
	if err != nil {
		return nil, err
	}
	return res.Backups, nil
}

func (p *PoolManager) backupDatabases(ctx context.Context, preferredProviderID string) (BackupResult, error) {
	if p == nil || p.hub == nil || p.db == nil {
		return BackupResult{}, errors.New("storage pool manager is not initialized")
	}

	tmpDir, err := os.MkdirTemp("", "mw-echo-backup-*")
	if err != nil {
		return BackupResult{}, fmt.Errorf("create backup temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	type backupTarget struct {
		dbName       string
		snapshotPath string
	}

	targets := make([]backupTarget, 0, 2)

	mainSnapPath := filepath.Join(tmpDir, "mw-echo.db")
	if err := snapshotSQLiteDB(ctx, p.db, mainSnapPath); err != nil {
		return BackupResult{}, err
	}
	targets = append(targets, backupTarget{
		dbName:       "mw-echo.db",
		snapshotPath: mainSnapPath,
	})

	waDBPath := filepath.Join(p.hub.DataDir(), "whatsapp_sessions.db")
	if info, statErr := os.Stat(waDBPath); statErr == nil && !info.IsDir() {
		waDSN := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", filepath.ToSlash(waDBPath))
		waDB, openErr := sql.Open("sqlite", waDSN)
		if openErr != nil {
			return BackupResult{}, fmt.Errorf("open whatsapp_sessions.db: %w", openErr)
		}
		waSnapPath := filepath.Join(tmpDir, "whatsapp_sessions.db")
		snapErr := snapshotSQLiteDB(ctx, waDB, waSnapPath)
		_ = waDB.Close()
		if snapErr != nil {
			return BackupResult{}, snapErr
		}
		targets = append(targets, backupTarget{
			dbName:       "whatsapp_sessions.db",
			snapshotPath: waSnapPath,
		})
	}

	ts := time.Now().UTC().Format("20060102T150405Z")
	uploaded := make([]PooledObject, 0, len(targets))
	for _, target := range targets {
		obj, uploadErr := p.uploadSnapshotFile(ctx, target.snapshotPath, target.dbName, ts, preferredProviderID)
		if uploadErr != nil {
			return BackupResult{}, uploadErr
		}
		uploaded = append(uploaded, obj)
	}

	return BackupResult{
		Timestamp: ts,
		Backups:   uploaded,
		Count:     len(uploaded),
	}, nil
}

func snapshotSQLiteDB(ctx context.Context, db *sql.DB, snapshotPath string) error {
	escapedPath := strings.ReplaceAll(filepath.ToSlash(snapshotPath), "'", "''")
	if _, err := db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", escapedPath)); err != nil {
		return fmt.Errorf("vacuum into %s: %w", filepath.Base(snapshotPath), err)
	}
	return nil
}

func (p *PoolManager) uploadSnapshotFile(ctx context.Context, snapshotPath, dbName, ts,
	preferredProviderID string) (PooledObject, error) {
	// Remove after closing the file handle (LIFO defer order ensures Close runs first on Windows).
	defer func() { _ = os.Remove(snapshotPath) }()

	f, err := os.Open(snapshotPath)
	if err != nil {
		return PooledObject{}, fmt.Errorf("open snapshot %s: %w", dbName, err)
	}
	defer f.Close()

	logicalPath := fmt.Sprintf("backups/%s/%s", ts, dbName)
	return p.Upload(ctx, backupProject, logicalPath, dbName, sqliteMimeType, preferredProviderID, f)
}
