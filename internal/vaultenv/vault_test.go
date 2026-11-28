package vaultenv_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/storage"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/A-L-L-A-H/mw-echo/internal/vaultenv"
	"github.com/labstack/echo/v4"
)

type capturedBackupUpload struct {
	project     string
	logicalPath string
	filename    string
	mimeType    string
	content     []byte
}

func openTestVault(t *testing.T, opts vaultenv.Options) (*vaultenv.Vault, *store.HubStore) {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	vault, err := vaultenv.NewVault(hub, opts)
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}
	return vault, hub
}

func TestAES256GCMEncryptionAndListRedaction(t *testing.T) {
	ctx := context.Background()
	vault, hub := openTestVault(t, vaultenv.Options{})

	secretPlaintext := "sk_live_ultra_sensitive_9988776655"
	meta, err := vault.SetSecret(ctx, vaultenv.SetSecretInput{
		Project: "billing-api",
		EnvName: "PRODUCTION",
		KeyName: "STRIPE_SECRET_KEY",
		Value:   secretPlaintext,
		Note:    "primary payment processor key",
	})
	if err != nil {
		t.Fatalf("SetSecret failed: %v", err)
	}
	if meta.EnvName != vaultenv.EnvProd {
		t.Fatalf("expected normalized env prod, got %q", meta.EnvName)
	}
	if strings.Contains(meta.MaskedPreview, secretPlaintext) {
		t.Fatalf("masked preview leaked plaintext: %q", meta.MaskedPreview)
	}

	var storedCiphertext string
	err = hub.DB().QueryRowContext(ctx, `
SELECT value_enc FROM env_secrets
WHERE project = ? AND env_name = ? AND key_name = ?`,
		"billing-api", "prod", "STRIPE_SECRET_KEY",
	).Scan(&storedCiphertext)
	if err != nil {
		t.Fatalf("query stored ciphertext: %v", err)
	}
	if storedCiphertext == secretPlaintext || strings.Contains(storedCiphertext, secretPlaintext) {
		t.Fatalf("expected AES-256-GCM ciphertext in SQLite, got plaintext substring")
	}

	box, err := storage.OpenSecretBox(hub.DataDir())
	if err != nil {
		t.Fatalf("open secretbox: %v", err)
	}
	decryptedBytes, err := box.Decrypt(storedCiphertext)
	if err != nil {
		t.Fatalf("decrypt stored ciphertext: %v", err)
	}
	if string(decryptedBytes) != secretPlaintext {
		t.Fatalf("decrypted mismatch: got %q, want %q", string(decryptedBytes), secretPlaintext)
	}

	listed, err := vault.ListKeys(ctx, "billing-api", "prod")
	if err != nil {
		t.Fatalf("ListKeys failed: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 key, got %d", len(listed))
	}
	listedJSON, err := json.Marshal(listed)
	if err != nil {
		t.Fatalf("marshal listed keys: %v", err)
	}
	if strings.Contains(string(listedJSON), secretPlaintext) {
		t.Fatalf("ListKeys JSON leaked plaintext secret: %s", string(listedJSON))
	}

	if _, err := vault.SetSecret(ctx, vaultenv.SetSecretInput{
		Project: "billing-api",
		EnvName: "dev",
		KeyName: "123_INVALID_KEY",
		Value:   "bad",
	}); !errors.Is(err, vaultenv.ErrValidation) {
		t.Fatalf("expected ErrValidation for invalid key name, got %v", err)
	}
}

func TestBulkImportAndExportDotEnv(t *testing.T) {
	ctx := context.Background()
	vault, _ := openTestVault(t, vaultenv.Options{})

	rawDotEnv := strings.Join([]string{
		"# Top comment should be skipped",
		"",
		"   # Indented comment",
		`export DATABASE_URL="postgres://user:p@ss=word@db.internal:5432/app?sslmode=require"`,
		`SINGLE_QUOTED='value with spaces and # hash'`,
		`EQUALS_INSIDE=token=part1=part2`,
		`QUOTED_DOUBLE="say \"hello\" # world"`,
		`SIMPLE_FLAG=true`,
	}, "\n")

	imported, err := vault.BulkImportDotEnv(ctx, "core-svc", "", rawDotEnv)
	if err != nil {
		t.Fatalf("BulkImportDotEnv failed: %v", err)
	}
	if imported.EnvName != vaultenv.EnvDev {
		t.Fatalf("expected default env dev, got %q", imported.EnvName)
	}
	if imported.Imported != 5 {
		t.Fatalf("expected 5 imported keys, got %d", imported.Imported)
	}
	if imported.Skipped != 3 {
		t.Fatalf("expected 3 skipped lines, got %d", imported.Skipped)
	}

	revealedEquals, err := vault.RevealSecret(ctx, "core-svc", "dev", "EQUALS_INSIDE")
	if err != nil {
		t.Fatalf("RevealSecret EQUALS_INSIDE failed: %v", err)
	}
	if revealedEquals.Value != "token=part1=part2" {
		t.Fatalf("expected EQUALS_INSIDE=token=part1=part2, got %q", revealedEquals.Value)
	}

	revealedDouble, err := vault.RevealSecret(ctx, "core-svc", "dev", "QUOTED_DOUBLE")
	if err != nil {
		t.Fatalf("RevealSecret QUOTED_DOUBLE failed: %v", err)
	}
	if revealedDouble.Value != `say "hello" # world` {
		t.Fatalf("expected unescaped double-quoted value, got %q", revealedDouble.Value)
	}

	exported, err := vault.ExportDotEnv(ctx, "core-svc", "dev")
	if err != nil {
		t.Fatalf("ExportDotEnv failed: %v", err)
	}

	expectedLines := []string{
		`DATABASE_URL=postgres://user:p@ss=word@db.internal:5432/app?sslmode=require`,
		`EQUALS_INSIDE=token=part1=part2`,
		`QUOTED_DOUBLE="say \"hello\" # world"`,
		`SIMPLE_FLAG=true`,
		`SINGLE_QUOTED="value with spaces and # hash"`,
	}
	gotLines := strings.Split(strings.TrimSuffix(exported, "\n"), "\n")
	if len(gotLines) != len(expectedLines) {
		t.Fatalf("exported line count mismatch: got %d (%q), want %d", len(gotLines), exported, len(expectedLines))
	}
	for idx := range expectedLines {
		if gotLines[idx] != expectedLines[idx] {
			t.Fatalf("exported line %d mismatch: got %q, want %q", idx, gotLines[idx], expectedLines[idx])
		}
	}

	// Re-import exported bundle into staging and verify lossless round-trip.
	reimported, err := vault.BulkImportDotEnv(ctx, "core-svc", "staging", exported)
	if err != nil {
		t.Fatalf("round-trip BulkImportDotEnv failed: %v", err)
	}
	if reimported.Imported != 5 {
		t.Fatalf("expected 5 round-tripped keys, got %d", reimported.Imported)
	}
	roundTripSingle, err := vault.RevealSecret(ctx, "core-svc", "staging", "SINGLE_QUOTED")
	if err != nil {
		t.Fatalf("RevealSecret round-trip SINGLE_QUOTED failed: %v", err)
	}
	if roundTripSingle.Value != "value with spaces and # hash" {
		t.Fatalf("round-trip mismatch: got %q", roundTripSingle.Value)
	}
}

func TestRevealAuditTrailAndEncryptedBackup(t *testing.T) {
	ctx := context.Background()
	var uploads []capturedBackupUpload

	vault, hub := openTestVault(t, vaultenv.Options{
		UploadBackup: func(_ context.Context, project, logicalPath, filename, mimeType string, content []byte) (string,
			error) {
			copied := make([]byte, len(content))
			copy(copied, content)
			uploads = append(uploads, capturedBackupUpload{
				project:     project,
				logicalPath: logicalPath,
				filename:    filename,
				mimeType:    mimeType,
				content:     copied,
			})
			return "obj-vault-backup-001", nil
		},
	})

	secretAlpha := "alpha_secret_token_ABC123"
	secretBeta := "beta_webhook_signing_XYZ789"
	if _, err := vault.SetSecret(ctx, vaultenv.SetSecretInput{
		Project: "echo-hub",
		EnvName: "prod",
		KeyName: "ALPHA_TOKEN",
		Value:   secretAlpha,
	}); err != nil {
		t.Fatalf("SetSecret ALPHA_TOKEN failed: %v", err)
	}
	if _, err := vault.SetSecret(ctx, vaultenv.SetSecretInput{
		Project: "echo-hub",
		EnvName: "prod",
		KeyName: "BETA_SIGNING_KEY",
		Value:   secretBeta,
	}); err != nil {
		t.Fatalf("SetSecret BETA_SIGNING_KEY failed: %v", err)
	}

	revealed, err := vault.RevealSecret(ctx, "echo-hub", "prod", "ALPHA_TOKEN")
	if err != nil {
		t.Fatalf("RevealSecret failed: %v", err)
	}
	if revealed.Value != secretAlpha {
		t.Fatalf("revealed value mismatch: got %q, want %q", revealed.Value, secretAlpha)
	}

	snapshot, err := vault.BackupEncryptedToPool(ctx, "echo-hub", "prod")
	if err != nil {
		t.Fatalf("BackupEncryptedToPool failed: %v", err)
	}
	if snapshot.ObjectID != "obj-vault-backup-001" {
		t.Fatalf("unexpected ObjectID: %q", snapshot.ObjectID)
	}
	if snapshot.KeyCount != 2 {
		t.Fatalf("expected KeyCount=2, got %d", snapshot.KeyCount)
	}
	if snapshot.EncryptedBytes <= 0 {
		t.Fatalf("expected positive EncryptedBytes, got %d", snapshot.EncryptedBytes)
	}
	if len(uploads) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(uploads))
	}

	upload := uploads[0]
	if upload.mimeType != "application/octet-stream" {
		t.Fatalf("expected application/octet-stream mimeType, got %q", upload.mimeType)
	}
	if !strings.HasPrefix(upload.filename, "echo-hub-prod-") || !strings.HasSuffix(upload.filename, ".env.enc") {
		t.Fatalf("unexpected backup filename: %q", upload.filename)
	}
	if bytes.Contains(upload.content, []byte(secretAlpha)) || bytes.Contains(upload.content, []byte(secretBeta)) {
		t.Fatalf("encrypted backup bytes leaked plaintext secret substrings")
	}
	if bytes.Contains(upload.content, []byte("ALPHA_TOKEN")) || bytes.Contains(upload.content,
		[]byte("BETA_SIGNING_KEY")) {
		t.Fatalf("encrypted backup bytes leaked plaintext key names")
	}

	box, err := storage.OpenSecretBox(hub.DataDir())
	if err != nil {
		t.Fatalf("open secretbox: %v", err)
	}
	decryptedBundle, err := box.Decrypt(string(upload.content))
	if err != nil {
		t.Fatalf("decrypt backup bundle: %v", err)
	}
	if !strings.Contains(string(decryptedBundle), "ALPHA_TOKEN="+secretAlpha) ||
		!strings.Contains(string(decryptedBundle), "BETA_SIGNING_KEY="+secretBeta) {
		t.Fatalf("decrypted backup bundle missing expected entries: %q", string(decryptedBundle))
	}

	logs, err := vault.ListAuditLogs(ctx, "echo-hub", 20)
	if err != nil {
		t.Fatalf("ListAuditLogs failed: %v", err)
	}
	if len(logs) != 4 {
		t.Fatalf("expected 4 audit logs (2 set, 1 reveal, 1 backup), got %d", len(logs))
	}

	seenReveal := false
	seenBackup := false
	for _, entry := range logs {
		if strings.Contains(entry.Detail, secretAlpha) || strings.Contains(entry.Detail, secretBeta) {
			t.Fatalf("audit detail leaked plaintext secret: %+v", entry)
		}
		if entry.Action == "reveal" && entry.KeyName == "ALPHA_TOKEN" && entry.EnvName == "prod" {
			seenReveal = true
		}
		if entry.Action == "backup" && entry.EnvName == "prod" && strings.Contains(entry.Detail, "obj-vault-backup-001") {
			seenBackup = true
		}
	}
	if !seenReveal || !seenBackup {
		t.Fatalf("missing expected audit entries: seenReveal=%v seenBackup=%v logs=%+v", seenReveal, seenBackup, logs)
	}
}

func TestVaultEnvHTTPRoutes(t *testing.T) {
	vault, _ := openTestVault(t, vaultenv.Options{
		UploadBackup: func(_ context.Context, project, logicalPath, filename, mimeType string, content []byte) (string,
			error) {
			return "obj-route-backup", nil
		},
	})

	app := echo.New()
	api := app.Group("/api")
	vault.RegisterRoutes(api)

	// PUT /api/vaultenv/secret
	putReq := httptest.NewRequest(http.MethodPut, "/api/vaultenv/secret", strings.NewReader(`{
		"project":"web-ui",
		"envName":"shared",
		"keyName":"JWT_SECRET",
		"value":"super_jwt_signing_value_1234",
		"note":"shared auth"
	}`))
	putReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	putRec := httptest.NewRecorder()
	app.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT /secret expected 200, got %d: %s", putRec.Code, putRec.Body.String())
	}

	// POST /api/vaultenv/import
	importReq := httptest.NewRequest(http.MethodPost, "/api/vaultenv/import", strings.NewReader(`{
		"project":"web-ui",
		"envName":"shared",
		"rawDotEnv":"export REDIS_URL=redis://localhost:6379/0\nAPI_TAG=\"hello world\"\n"
	}`))
	importReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	importRec := httptest.NewRecorder()
	app.ServeHTTP(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("POST /import expected 200, got %d: %s", importRec.Code, importRec.Body.String())
	}

	// GET /api/vaultenv/keys
	keysReq := httptest.NewRequest(http.MethodGet, "/api/vaultenv/keys?project=web-ui&env=shared", nil)
	keysRec := httptest.NewRecorder()
	app.ServeHTTP(keysRec, keysReq)
	if keysRec.Code != http.StatusOK {
		t.Fatalf("GET /keys expected 200, got %d: %s", keysRec.Code, keysRec.Body.String())
	}
	if strings.Contains(keysRec.Body.String(), "super_jwt_signing_value_1234") {
		t.Fatalf("GET /keys leaked plaintext secret")
	}

	// POST /api/vaultenv/reveal
	revealReq := httptest.NewRequest(http.MethodPost, "/api/vaultenv/reveal", strings.NewReader(`{
		"project":"web-ui",
		"envName":"shared",
		"keyName":"JWT_SECRET"
	}`))
	revealReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	revealRec := httptest.NewRecorder()
	app.ServeHTTP(revealRec, revealReq)
	if revealRec.Code != http.StatusOK {
		t.Fatalf("POST /reveal expected 200, got %d: %s", revealRec.Code, revealRec.Body.String())
	}
	var revealed vaultenv.RevealResult
	if err := json.Unmarshal(revealRec.Body.Bytes(), &revealed); err != nil {
		t.Fatalf("decode reveal response: %v", err)
	}
	if revealed.Value != "super_jwt_signing_value_1234" {
		t.Fatalf("unexpected revealed value: %q", revealed.Value)
	}

	// GET /api/vaultenv/export
	exportReq := httptest.NewRequest(http.MethodGet, "/api/vaultenv/export?project=web-ui&env=shared", nil)
	exportRec := httptest.NewRecorder()
	app.ServeHTTP(exportRec, exportReq)
	if exportRec.Code != http.StatusOK {
		t.Fatalf("GET /export expected 200, got %d: %s", exportRec.Code, exportRec.Body.String())
	}
	var exportPayload struct {
		Project  string `json:"project"`
		EnvName  string `json:"envName"`
		DotEnv   string `json:"dotenv"`
		KeyCount int    `json:"keyCount"`
	}
	if err := json.Unmarshal(exportRec.Body.Bytes(), &exportPayload); err != nil {
		t.Fatalf("decode export response: %v", err)
	}
	if exportPayload.KeyCount != 3 || !strings.Contains(exportPayload.DotEnv, `API_TAG="hello world"`) {
		t.Fatalf("unexpected export payload: %+v", exportPayload)
	}

	// POST /api/vaultenv/backup
	backupReq := httptest.NewRequest(http.MethodPost, "/api/vaultenv/backup", strings.NewReader(`{"project":"web-ui","envName":"shared"}`))
	backupReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	backupRec := httptest.NewRecorder()
	app.ServeHTTP(backupRec, backupReq)
	if backupRec.Code != http.StatusOK {
		t.Fatalf("POST /backup expected 200, got %d: %s", backupRec.Code, backupRec.Body.String())
	}

	// DELETE /api/vaultenv/secret
	delReq := httptest.NewRequest(http.MethodDelete, "/api/vaultenv/secret?project=web-ui&env=shared&key=REDIS_URL", nil)
	delRec := httptest.NewRecorder()
	app.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("DELETE /secret expected 200, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// GET /api/vaultenv/audit
	auditReq := httptest.NewRequest(http.MethodGet, "/api/vaultenv/audit?project=web-ui&limit=10", nil)
	auditRec := httptest.NewRecorder()
	app.ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK {
		t.Fatalf("GET /audit expected 200, got %d: %s", auditRec.Code, auditRec.Body.String())
	}
	var auditPayload struct {
		Logs  []vaultenv.AuditLogRecord `json:"logs"`
		Count int                       `json:"count"`
	}
	if err := json.Unmarshal(auditRec.Body.Bytes(), &auditPayload); err != nil {
		t.Fatalf("decode audit response: %v", err)
	}
	if auditPayload.Count != 6 {
		t.Fatalf("expected 6 audit records (set, import, reveal, export, backup, delete), got %d", auditPayload.Count)
	}
}
