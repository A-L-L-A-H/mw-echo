package storage_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/storage"
)

func TestVerifySQLiteSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	snapshotPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite", snapshotPath)
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS tasks (id INTEGER PRIMARY KEY, title TEXT)`,
		`CREATE TABLE IF NOT EXISTS radar_events (id INTEGER PRIMARY KEY, event_type TEXT)`,
		`CREATE TABLE IF NOT EXISTS vault_secrets (id INTEGER PRIMARY KEY, secret TEXT)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
	}

	if _, err := db.Exec(`INSERT INTO tasks (title) VALUES ('test task 1'), ('test task 2')`); err != nil {
		t.Fatalf("failed to insert tasks: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO radar_events (event_type) VALUES ('scan_complete')`); err != nil {
		t.Fatalf("failed to insert radar_events: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO vault_secrets (secret) VALUES ('secret-123')`); err != nil {
		t.Fatalf("failed to insert vault_secrets: %v", err)
	}
	_ = db.Close()

	report, err := storage.VerifySQLiteSnapshot(context.Background(), snapshotPath)
	if err != nil {
		t.Fatalf("VerifySQLiteSnapshot failed: %v", err)
	}

	if report.SHA256 == "" {
		t.Error("expected non-empty SHA256 checksum")
	}
	if report.IntegrityCheck != "ok" {
		t.Errorf("expected integrity_check result 'ok', got '%s'", report.IntegrityCheck)
	}
	if !report.IntegrityOK {
		t.Error("expected IntegrityOK to be true")
	}
	if report.TableCounts["tasks"] != 2 {
		t.Errorf("expected 2 tasks, got %d", report.TableCounts["tasks"])
	}
	if report.TableCounts["radar_events"] != 1 {
		t.Errorf("expected 1 radar_event, got %d", report.TableCounts["radar_events"])
	}
	if report.TableCounts["vault_secrets"] != 1 {
		t.Errorf("expected 1 vault_secret, got %d", report.TableCounts["vault_secrets"])
	}
	if report.ObjectKey == "" {
		t.Error("expected non-empty ObjectKey")
	}
	if report.SizeBytes <= 0 {
		t.Error("expected positive SizeBytes")
	}
	if report.DurationMs <= 0 {
		t.Error("expected positive DurationMs")
	}
	if report.VerifiedAt == "" {
		t.Error("expected non-empty VerifiedAt")
	}
	t.Logf("VerifySQLiteSnapshot test passed! Report: %+v", report)
}
