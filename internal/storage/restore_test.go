package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/storage"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestVerifySQLiteSnapshotAndRunRestoreDrill(t *testing.T) {
	ctx := context.Background()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	if _, err := hub.UpsertTask(ctx, store.TaskRecord{
		Code:     "DRILL-001",
		Project:  "mw-echo",
		Title:    "Verify 9Drive restore drill",
		Priority: "P0",
		Status:   "todo",
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	pool, err := storage.NewPoolManager(hub)
	if err != nil {
		t.Fatalf("new pool manager: %v", err)
	}

	backups, err := pool.BackupDatabases(ctx)
	if err != nil || len(backups) == 0 {
		t.Fatalf("backup databases failed: %v (count=%d)", err, len(backups))
	}

	tempDrillDir := t.TempDir()
	report, err := storage.RunRestoreDrill(ctx, pool, backups[0].ID, tempDrillDir)
	if err != nil {
		t.Fatalf("run restore drill failed: %v", err)
	}
	if !report.IntegrityOK || report.IntegrityCheck != "ok" {
		t.Fatalf("expected integrity ok, got %+v", report)
	}
	if report.SHA256 == "" || report.SizeBytes <= 0 {
		t.Fatalf("expected valid sha256 and size, got %+v", report)
	}
	if report.TableCounts["tasks"] != 1 {
		t.Fatalf("expected 1 task in snapshot, got %+v", report.TableCounts)
	}
	if !report.CleanedUp {
		t.Fatalf("expected temp snapshot file to be cleaned up")
	}

	remainingEntries, err := os.ReadDir(tempDrillDir)
	if err != nil {
		t.Fatalf("read temp drill dir: %v", err)
	}
	if len(remainingEntries) != 0 {
		t.Fatalf("expected 0 leftover files in temp drill dir, found %d", len(remainingEntries))
	}

	// Corrupt file fails integrity check
	corruptPath := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(corruptPath, []byte("not-a-valid-sqlite-database"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	if _, err := storage.VerifySQLiteSnapshot(ctx, corruptPath); err == nil {
		t.Fatalf("expected corrupt snapshot to return error")
	}
}
