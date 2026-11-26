package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
	_ "modernc.org/sqlite"
)

func newMockS3Server(t *testing.T) (*httptest.Server, map[string][]byte, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKBACKUP/") {
			http.Error(w, "missing sigv4", http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = raw
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			raw, ok := objects[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(raw)
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, objects, &mu
}

func assertValidSQLiteSnapshot(t *testing.T, payload []byte, query string, expected string) {
	t.Helper()
	if !bytes.HasPrefix(payload, []byte("SQLite format 3\x00")) {
		t.Fatalf("uploaded backup is not a valid SQLite header")
	}
	verifyPath := filepath.Join(t.TempDir(), "verify.db")
	if err := os.WriteFile(verifyPath, payload, 0o600); err != nil {
		t.Fatalf("write verify snapshot: %v", err)
	}
	dsn := fmt.Sprintf("file:%s?mode=ro", filepath.ToSlash(verifyPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open snapshot sqlite: %v", err)
	}
	defer db.Close()

	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatalf("integrity_check failed: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("unexpected integrity_check result: %q", integrity)
	}

	var got string
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("query snapshot failed: %v", err)
	}
	if got != expected {
		t.Fatalf("expected %q from snapshot query, got %q", expected, got)
	}
}

func listBackupTempDirs(t *testing.T) map[string]struct{} {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "mw-echo-backup-*"))
	if err != nil {
		t.Fatalf("glob temp dirs: %v", err)
	}
	out := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		out[m] = struct{}{}
	}
	return out
}

func TestBackupDatabasesToS3AndCleanup(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	hub, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	if _, err := hub.UpsertDoc(ctx, store.DocRecord{
		Project: "alpha",
		RelPath: "notes/backup-check.md",
		Title:   "Backup Check",
		Content: "# Backup Check\n\nVerified snapshot content.",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	// Seed an optional whatsapp_sessions.db in hub.DataDir().
	waPath := filepath.Join(hub.DataDir(), "whatsapp_sessions.db")
	waDB, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)", filepath.ToSlash(waPath)))
	if err != nil {
		t.Fatalf("create whatsapp_sessions.db: %v", err)
	}
	if _, err := waDB.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, label TEXT); INSERT INTO sessions (id, label) VALUES ('wa-1', 'primary-session');`); err != nil {
		_ = waDB.Close()
		t.Fatalf("seed whatsapp_sessions.db: %v", err)
	}
	_ = waDB.Close()

	fakeS3, s3Objects, mu := newMockS3Server(t)

	pool, err := NewPoolManager(hub)
	if err != nil {
		t.Fatalf("new pool manager: %v", err)
	}
	if _, err := pool.AddProvider(ctx, ProviderCreateInput{
		Name:       "backup-s3",
		Kind:       "s3",
		QuotaBytes: 5 * 1024 * 1024 * 1024,
		Priority:   1,
		S3: &S3Config{
			Endpoint:       fakeS3.URL,
			Region:         "auto",
			Bucket:         "mw-backups",
			AccessKey:      "AKBACKUP",
			SecretKey:      "SKBACKUP",
			ForcePathStyle: true,
		},
	}); err != nil {
		t.Fatalf("add s3 provider: %v", err)
	}

	beforeTemp := listBackupTempDirs(t)

	backups, err := pool.BackupDatabases(ctx)
	if err != nil {
		t.Fatalf("BackupDatabases failed: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 backups (mw-echo.db + whatsapp_sessions.db), got %d", len(backups))
	}

	afterTemp := listBackupTempDirs(t)
	for path := range afterTemp {
		if _, existed := beforeTemp[path]; !existed {
			t.Fatalf("temporary VACUUM INTO directory/file was not cleaned up: %s", path)
		}
	}

	// Confirm zero local blob files were written when backed by remote S3.
	localBlobs, err := os.ReadDir(filepath.Join(hub.DataDir(), "blobs"))
	if err != nil {
		t.Fatalf("read blobs dir: %v", err)
	}
	if len(localBlobs) != 0 {
		t.Fatalf("expected 0 local blob files, found %d", len(localBlobs))
	}

	byName := map[string]PooledObject{}
	for _, b := range backups {
		if b.Project != "system-backups" {
			t.Fatalf("expected project system-backups, got %q", b.Project)
		}
		if b.MimeType != "application/vnd.sqlite3" {
			t.Fatalf("expected mime application/vnd.sqlite3, got %q", b.MimeType)
		}
		if b.ProviderKind != "s3" {
			t.Fatalf("expected s3 provider kind, got %q", b.ProviderKind)
		}
		if !strings.HasPrefix(b.LogicalPath, "backups/") || !strings.HasSuffix(b.LogicalPath, "/"+b.Filename) {
			t.Fatalf("unexpected logical path %q for %q", b.LogicalPath, b.Filename)
		}
		byName[b.Filename] = b
	}

	mainBackup, ok := byName["mw-echo.db"]
	if !ok {
		t.Fatalf("missing mw-echo.db in backup results")
	}
	waBackup, ok := byName["whatsapp_sessions.db"]
	if !ok {
		t.Fatalf("missing whatsapp_sessions.db in backup results")
	}

	mu.Lock()
	mainKey := "/mw-backups/" + mainBackup.RemoteID
	waKey := "/mw-backups/" + waBackup.RemoteID
	mainBytes := append([]byte(nil), s3Objects[mainKey]...)
	waBytes := append([]byte(nil), s3Objects[waKey]...)
	mu.Unlock()

	if len(mainBytes) == 0 {
		t.Fatalf("expected uploaded mw-echo.db bytes in mock S3 at %s", mainKey)
	}
	if len(waBytes) == 0 {
		t.Fatalf("expected uploaded whatsapp_sessions.db bytes in mock S3 at %s", waKey)
	}

	assertValidSQLiteSnapshot(t, mainBytes, `SELECT title FROM docs WHERE rel_path = 'notes/backup-check.md'`, "Backup Check")
	assertValidSQLiteSnapshot(t, waBytes, `SELECT label FROM sessions WHERE id = 'wa-1'`, "primary-session")
}

func TestBackupRouteLocalPoolFallback(t *testing.T) {
	dataDir := t.TempDir()
	hub, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	if _, err := hub.UpsertDoc(context.Background(), store.DocRecord{
		Project: "ops",
		RelPath: "runbook.md",
		Title:   "Runbook",
		Content: "# Runbook",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	pool, err := NewPoolManager(hub)
	if err != nil {
		t.Fatalf("new pool manager: %v", err)
	}

	e := echo.New()
	api := e.Group("/api")
	pool.RegisterBackupRoutes(api)

	req := httptest.NewRequest(http.MethodPost, "/api/storage/backup", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/storage/backup returned %d: %s", rec.Code, rec.Body.String())
	}

	var res BackupResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode backup result: %v", err)
	}
	if res.Count != 1 || len(res.Backups) != 1 {
		t.Fatalf("expected 1 backup without whatsapp_sessions.db, got count=%d len=%d", res.Count, len(res.Backups))
	}
	if res.Backups[0].Filename != "mw-echo.db" || res.Backups[0].Project != "system-backups" {
		t.Fatalf("unexpected backup metadata: %+v", res.Backups[0])
	}

	_, stream, err := pool.OpenDownload(context.Background(), res.Backups[0].ID)
	if err != nil {
		t.Fatalf("download local backup: %v", err)
	}
	defer stream.Close()
	raw, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read downloaded backup: %v", err)
	}
	assertValidSQLiteSnapshot(t, raw, `SELECT title FROM docs WHERE rel_path = 'runbook.md'`, "Runbook")
}
