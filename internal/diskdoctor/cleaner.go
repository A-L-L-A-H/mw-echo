package diskdoctor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

// Options configures optional capabilities for the Disk Doctor manager.
type Options struct {
	AIAnalyze func(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// Manager coordinates disk scanning, safe cleanup, history persistence, and AI insights.
type Manager struct {
	hub  *store.HubStore
	opts Options
	mu   sync.Mutex
}

// CleanResult summarizes a dry-run or live cleanup execution.
type CleanResult struct {
	Paths         []string `json:"paths"`
	DryRun        bool     `json:"dryRun"`
	FreedBytes    int64    `json:"freedBytes"`
	DeletedFiles  int64    `json:"deletedFiles"`
	SkippedLocked int64    `json:"skippedLocked"`
	Errors        []string `json:"errors,omitempty"`
	HistoryID     string   `json:"historyId,omitempty"`
}

// CleanHistoryRecord represents a persisted cleanup audit log entry.
type CleanHistoryRecord struct {
	ID            string   `json:"id"`
	Paths         []string `json:"paths"`
	DryRun        bool     `json:"dryRun"`
	FreedBytes    int64    `json:"freedBytes"`
	DeletedFiles  int64    `json:"deletedFiles"`
	SkippedLocked int64    `json:"skippedLocked"`
	CreatedAt     string   `json:"createdAt"`
}

// NewManager initializes the Disk Doctor manager and migrates its SQLite audit table.
func NewManager(hub *store.HubStore, opts Options) (*Manager, error) {
	mgr := &Manager{
		hub:  hub,
		opts: opts,
	}
	if hub != nil && hub.DB() != nil {
		schema := `
CREATE TABLE IF NOT EXISTS disk_clean_history (
  id TEXT PRIMARY KEY,
  paths_json TEXT NOT NULL,
  dry_run INTEGER NOT NULL,
  freed_bytes INTEGER NOT NULL,
  deleted_files INTEGER NOT NULL,
  skipped_locked INTEGER NOT NULL,
  created_at TEXT NOT NULL
);`
		if _, err := hub.DB().Exec(schema); err != nil {
			return nil, fmt.Errorf("migrate disk_clean_history: %w", err)
		}
	}
	return mgr, nil
}

// IsProtectedPath returns true and a reason if targetPath must never be cleaned.
func IsProtectedPath(targetPath string) (bool, string) {
	trimmed := strings.TrimSpace(targetPath)
	if trimmed == "" {
		return true, "empty path"
	}

	cleaned := filepath.Clean(trimmed)
	absPath, err := filepath.Abs(cleaned)
	if err == nil {
		cleaned = absPath
	}

	// Reject drive roots (e.g. C:\, Z:\, /).
	vol := filepath.VolumeName(cleaned)
	if cleaned == "/" || cleaned == `\` || (vol != "" && (cleaned == vol || cleaned == vol+`\` || cleaned == vol+`/`)) {
		return true, "drive root is protected"
	}
	if filepath.Dir(cleaned) == cleaned {
		return true, "filesystem root is protected"
	}

	// Reject user home root itself.
	if homeDir, homeErr := os.UserHomeDir(); homeErr == nil && homeDir != "" {
		if strings.EqualFold(filepath.Clean(homeDir), cleaned) {
			return true, "user home root is protected"
		}
	}

	slashLower := strings.ToLower(filepath.ToSlash(cleaned))
	if hasPathSegment(slashLower, ".git") {
		return true, ".git metadata is protected"
	}

	_, safety := ClassifyPath(cleaned)
	if safety == SafetyProtected {
		return true, "path classified as protected system/app directory"
	}

	return false, ""
}

// CleanPaths validates target paths against hard safety guards and either simulates (dryRun=true)
// or deletes contents inside each target path (dryRun=false), counting locked files gracefully.
func (m *Manager) CleanPaths(ctx context.Context, paths []string, dryRun bool) (CleanResult, error) {
	if len(paths) == 0 {
		return CleanResult{}, fmt.Errorf("at least one path is required")
	}

	cleanedPaths := make([]string, 0, len(paths))
	for _, rawPath := range paths {
		if protected, reason := IsProtectedPath(rawPath); protected {
			return CleanResult{}, fmt.Errorf("refusing to clean protected path %q: %s", rawPath, reason)
		}
		cleanedPaths = append(cleanedPaths, filepath.Clean(rawPath))
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	outcome := CleanResult{
		Paths:  cleanedPaths,
		DryRun: dryRun,
	}

	for _, targetPath := range cleanedPaths {
		if ctx.Err() != nil {
			outcome.Errors = append(outcome.Errors, ctx.Err().Error())
			break
		}

		info, err := os.Lstat(targetPath)
		if err != nil {
			outcome.Errors = append(outcome.Errors, fmt.Sprintf("%s: %v", targetPath, err))
			continue
		}

		if !info.IsDir() {
			fileSize := info.Size()
			if dryRun {
				outcome.FreedBytes += fileSize
				outcome.DeletedFiles++
				continue
			}
			if removeErr := os.Remove(targetPath); removeErr != nil {
				outcome.SkippedLocked++
			} else {
				outcome.FreedBytes += fileSize
				outcome.DeletedFiles++
			}
			continue
		}

		cleanDirContents(ctx, targetPath, dryRun, &outcome)
	}

	if !dryRun && m.hub != nil && m.hub.DB() != nil {
		historyID := generateCleanID()
		if err := m.recordHistory(ctx, historyID, outcome); err != nil {
			outcome.Errors = append(outcome.Errors, fmt.Sprintf("record history: %v", err))
		} else {
			outcome.HistoryID = historyID
		}
	}

	return outcome, nil
}

func cleanDirContents(ctx context.Context, dirPath string, dryRun bool, outcome *CleanResult) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		outcome.SkippedLocked++
		return
	}

	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		entryType := entry.Type()
		if entryType&os.ModeSymlink != 0 {
			continue
		}
		childPath := filepath.Join(dirPath, entry.Name())
		if entry.IsDir() {
			cleanDirContents(ctx, childPath, dryRun, outcome)
			if !dryRun {
				_ = os.Remove(childPath)
			}
			continue
		}

		fileInfo, infoErr := entry.Info()
		var fileSize int64
		if infoErr == nil {
			fileSize = fileInfo.Size()
		}

		if dryRun {
			outcome.FreedBytes += fileSize
			outcome.DeletedFiles++
			continue
		}

		if removeErr := os.Remove(childPath); removeErr != nil {
			outcome.SkippedLocked++
			continue
		}
		outcome.FreedBytes += fileSize
		outcome.DeletedFiles++
	}
}

func (m *Manager) recordHistory(ctx context.Context, historyID string, outcome CleanResult) error {
	pathsPayload, err := json.Marshal(outcome.Paths)
	if err != nil {
		return err
	}
	dryRunInt := 0
	if outcome.DryRun {
		dryRunInt = 1
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)

	_, err = m.hub.DB().ExecContext(
		ctx,
		`INSERT INTO disk_clean_history (id, paths_json, dry_run, freed_bytes, deleted_files, skipped_locked, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		historyID,
		string(pathsPayload),
		dryRunInt,
		outcome.FreedBytes,
		outcome.DeletedFiles,
		outcome.SkippedLocked,
		createdAt,
	)
	return err
}

// ListHistory returns the most recent cleanup audit records from SQLite.
func (m *Manager) ListHistory(ctx context.Context, limit int) ([]CleanHistoryRecord, error) {
	if limit <= 0 {
		limit = 25
	}
	if m.hub == nil || m.hub.DB() == nil {
		return []CleanHistoryRecord{}, nil
	}

	rows, err := m.hub.DB().QueryContext(
		ctx,
		`SELECT id, paths_json, dry_run, freed_bytes, deleted_files, skipped_locked, created_at
		 FROM disk_clean_history
		 ORDER BY created_at DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query disk_clean_history: %w", err)
	}
	defer rows.Close()

	records := make([]CleanHistoryRecord, 0, limit)
	for rows.Next() {
		var rec CleanHistoryRecord
		var pathsJSON string
		var dryRunInt int
		if scanErr := rows.Scan(
			&rec.ID,
			&pathsJSON,
			&dryRunInt,
			&rec.FreedBytes,
			&rec.DeletedFiles,
			&rec.SkippedLocked,
			&rec.CreatedAt,
		); scanErr != nil {
			return nil, fmt.Errorf("scan disk_clean_history: %w", scanErr)
		}
		rec.DryRun = dryRunInt != 0
		_ = json.Unmarshal([]byte(pathsJSON), &rec.Paths)
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func generateCleanID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return "clean-" + hex.EncodeToString(raw[:])
}
