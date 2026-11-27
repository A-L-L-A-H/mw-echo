package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

const (
	PolicyMostFree   = "most_free"
	PolicyRoundRobin = "round_robin"
	PolicyPriority   = "priority"
)

type ProviderRecord struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"` // gdrive | s3 | local
	AccountRef string `json:"accountRef,omitempty"`
	QuotaBytes int64  `json:"quotaBytes"`
	UsedBytes  int64  `json:"usedBytes"`
	FreeBytes  int64  `json:"freeBytes"`
	Priority   int    `json:"priority"`
	Enabled    bool   `json:"enabled"`
	LastSyncAt string `json:"lastSyncAt,omitempty"`
	CreatedAt  string `json:"createdAt"`
}

type ProviderCreateInput struct {
	Name       string        `json:"name"`
	Kind       string        `json:"kind"` // gdrive | s3 | local
	QuotaBytes int64         `json:"quotaBytes"`
	Priority   int           `json:"priority"`
	GDrive     *GDriveConfig `json:"gdrive,omitempty"`
	S3         *S3Config     `json:"s3,omitempty"`
}

type PoolSummary struct {
	RoutingPolicy   string           `json:"routingPolicy"`
	TotalQuotaBytes int64            `json:"totalQuotaBytes"`
	TotalUsedBytes  int64            `json:"totalUsedBytes"`
	TotalFreeBytes  int64            `json:"totalFreeBytes"`
	Providers       []ProviderRecord `json:"providers"`
}

type PooledObject struct {
	store.StorageObject
	ProviderID   string `json:"providerId"`
	ProviderKind string `json:"providerKind"`
	ProviderName string `json:"providerName"`
	RemoteID     string `json:"remoteId,omitempty"`
}

type PoolManager struct {
	hub *store.HubStore
	db  *sql.DB
	box *SecretBox

	mu            sync.Mutex
	rrIndex       int
	gdriveClients map[string]*GDriveClient
	oauthStates   map[string]pendingOAuth
}

type pendingOAuth struct {
	Name         string
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Priority     int
	CreatedAt    time.Time
}

func NewPoolManager(hub *store.HubStore) (*PoolManager, error) {
	box, err := OpenSecretBox(hub.DataDir())
	if err != nil {
		return nil, err
	}
	pm := &PoolManager{
		hub:           hub,
		db:            hub.DB(),
		box:           box,
		gdriveClients: map[string]*GDriveClient{},
		oauthStates:   map[string]pendingOAuth{},
	}
	if err := pm.migrate(); err != nil {
		return nil, err
	}
	return pm, nil
}

func (p *PoolManager) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS storage_providers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  account_ref TEXT NOT NULL DEFAULT '',
  config_enc TEXT NOT NULL DEFAULT '',
  quota_bytes INTEGER NOT NULL DEFAULT 16106127360,
  used_bytes INTEGER NOT NULL DEFAULT 0,
  priority INTEGER NOT NULL DEFAULT 100,
  enabled INTEGER NOT NULL DEFAULT 1,
  last_sync_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS hub_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS object_locations (
  object_id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL,
  provider_kind TEXT NOT NULL,
  remote_id TEXT NOT NULL DEFAULT ''
);
`
	if _, err := p.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate 9drive pool tables: %w", err)
	}
	_, _ = p.db.Exec(`INSERT OR IGNORE INTO hub_settings (key, value) VALUES ('routing_policy', ?)`, PolicyMostFree)
	return nil
}

func (p *PoolManager) GetRoutingPolicy(ctx context.Context) string {
	var policy string
	if err := p.db.QueryRowContext(ctx, `SELECT value FROM hub_settings WHERE key = 'routing_policy'`).Scan(&policy); err != nil {
		return PolicyMostFree
	}
	switch policy {
	case PolicyMostFree, PolicyRoundRobin, PolicyPriority:
		return policy
	default:
		return PolicyMostFree
	}
}

func (p *PoolManager) SetRoutingPolicy(ctx context.Context, policy string) error {
	policy = strings.TrimSpace(strings.ToLower(policy))
	if policy != PolicyMostFree && policy != PolicyRoundRobin && policy != PolicyPriority {
		return fmt.Errorf("routing policy must be one of: %s, %s, %s", PolicyMostFree, PolicyRoundRobin, PolicyPriority)
	}
	_, err := p.db.ExecContext(ctx, `INSERT INTO hub_settings (key, value) VALUES ('routing_policy', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, policy)
	return err
}

func (p *PoolManager) AddProvider(ctx context.Context, input ProviderCreateInput) (ProviderRecord, error) {
	kind := strings.TrimSpace(strings.ToLower(input.Kind))
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ProviderRecord{}, errors.New("provider name is required")
	}
	if kind != "gdrive" && kind != "s3" && kind != "local" {
		return ProviderRecord{}, errors.New("provider kind must be gdrive, s3, or local")
	}

	var rawConfig []byte
	var accountRef string
	quotaBytes := input.QuotaBytes
	if quotaBytes <= 0 {
		if kind == "gdrive" {
			quotaBytes = 15 * 1024 * 1024 * 1024 // 15 GiB free Drive default
		} else {
			quotaBytes = 10 * 1024 * 1024 * 1024 // 10 GiB R2 free tier default
		}
	}

	switch kind {
	case "gdrive":
		if input.GDrive == nil || strings.TrimSpace(input.GDrive.ClientID) == "" ||
			strings.TrimSpace(input.GDrive.ClientSecret) == "" || strings.TrimSpace(input.GDrive.RefreshToken) == "" {
			return ProviderRecord{}, errors.New("gdrive requires clientId, clientSecret, and refreshToken")
		}
		accountRef = input.GDrive.RootFolder
		if accountRef == "" {
			accountRef = "mw-echo"
		}
		rawConfig, _ = json.Marshal(input.GDrive)
	case "s3":
		if input.S3 == nil {
			return ProviderRecord{}, errors.New("s3 config is required")
		}
		if _, err := NewS3Client(*input.S3); err != nil {
			return ProviderRecord{}, err
		}
		accountRef = strings.TrimRight(input.S3.Endpoint, "/") + "/" + input.S3.Bucket
		rawConfig, _ = json.Marshal(input.S3)
	case "local":
		accountRef = p.hub.DataDir()
		rawConfig = []byte("{}")
	}

	encConfig, err := p.box.Encrypt(rawConfig)
	if err != nil {
		return ProviderRecord{}, err
	}

	priority := input.Priority
	if priority <= 0 {
		priority = 100
	}
	sum := sha256.Sum256([]byte(kind + ":" + name + ":" + time.Now().UTC().Format(time.RFC3339Nano)))
	id := "prv_" + hex.EncodeToString(sum[:8])
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = p.db.ExecContext(ctx, `
INSERT INTO storage_providers (id, name, kind, account_ref, config_enc, quota_bytes, used_bytes, priority, enabled,
	last_sync_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, 0, ?, 1, '', ?)
`, id, name, kind, accountRef, encConfig, quotaBytes, priority, now)
	if err != nil {
		return ProviderRecord{}, err
	}

	rec := ProviderRecord{
		ID:         id,
		Name:       name,
		Kind:       kind,
		AccountRef: accountRef,
		QuotaBytes: quotaBytes,
		UsedBytes:  0,
		FreeBytes:  quotaBytes,
		Priority:   priority,
		Enabled:    true,
		CreatedAt:  now,
	}
	return rec, nil
}

func (p *PoolManager) ListProviders(ctx context.Context) ([]ProviderRecord, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, name, kind, account_ref, quota_bytes, used_bytes, priority, enabled, last_sync_at, created_at
FROM storage_providers ORDER BY priority ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	providers := make([]ProviderRecord, 0)
	for rows.Next() {
		var rec ProviderRecord
		var enabledInt int
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Kind, &rec.AccountRef, &rec.QuotaBytes, &rec.UsedBytes, &rec.Priority,
			&enabledInt, &rec.LastSyncAt, &rec.CreatedAt); err != nil {
			return nil, err
		}
		rec.Enabled = enabledInt == 1
		rec.FreeBytes = maxInt64(0, rec.QuotaBytes-rec.UsedBytes)
		providers = append(providers, rec)
	}
	return providers, rows.Err()
}

func (p *PoolManager) PoolSummary(ctx context.Context) (PoolSummary, error) {
	providers, err := p.ListProviders(ctx)
	if err != nil {
		return PoolSummary{}, err
	}
	summary := PoolSummary{
		RoutingPolicy: p.GetRoutingPolicy(ctx),
		Providers:     providers,
	}
	for _, prv := range providers {
		if !prv.Enabled {
			continue
		}
		summary.TotalQuotaBytes += prv.QuotaBytes
		summary.TotalUsedBytes += prv.UsedBytes
		summary.TotalFreeBytes += prv.FreeBytes
	}
	return summary, nil
}

func (p *PoolManager) DeleteProvider(ctx context.Context, id string) error {
	execRes, err := p.db.ExecContext(ctx, `DELETE FROM storage_providers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, _ := execRes.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	p.mu.Lock()
	delete(p.gdriveClients, id)
	p.mu.Unlock()
	return nil
}

func (p *PoolManager) loadProviderSecret(ctx context.Context, id string) (ProviderRecord, []byte, error) {
	var rec ProviderRecord
	var encConfig string
	var enabledInt int
	err := p.db.QueryRowContext(ctx, `
SELECT id, name, kind, account_ref, config_enc, quota_bytes, used_bytes, priority, enabled, last_sync_at, created_at
FROM storage_providers WHERE id = ?`, id).Scan(
		&rec.ID, &rec.Name, &rec.Kind, &rec.AccountRef, &encConfig, &rec.QuotaBytes, &rec.UsedBytes, &rec.Priority, &enabledInt, &rec.LastSyncAt, &rec.CreatedAt,
	)
	if err != nil {
		return ProviderRecord{}, nil, err
	}
	rec.Enabled = enabledInt == 1
	rec.FreeBytes = maxInt64(0, rec.QuotaBytes-rec.UsedBytes)
	plain, err := p.box.Decrypt(encConfig)
	if err != nil {
		return ProviderRecord{}, nil, fmt.Errorf("decrypt provider config: %w", err)
	}
	return rec, plain, nil
}

func (p *PoolManager) getGDriveClient(id string, plainCfg []byte) (*GDriveClient, error) {
	p.mu.Lock()
	if existing, ok := p.gdriveClients[id]; ok {
		p.mu.Unlock()
		return existing, nil
	}
	p.mu.Unlock()

	var cfg GDriveConfig
	if err := json.Unmarshal(plainCfg, &cfg); err != nil {
		return nil, err
	}
	client := NewGDriveClient(cfg)
	p.mu.Lock()
	p.gdriveClients[id] = client
	p.mu.Unlock()
	return client, nil
}

func (p *PoolManager) SyncQuota(ctx context.Context, id string) (ProviderRecord, error) {
	rec, plain, err := p.loadProviderSecret(ctx, id)
	if err != nil {
		return ProviderRecord{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)

	switch rec.Kind {
	case "gdrive":
		client, err := p.getGDriveClient(id, plain)
		if err != nil {
			return ProviderRecord{}, err
		}
		quota, err := client.FetchQuota(ctx)
		if err != nil {
			return ProviderRecord{}, err
		}
		accountRef := rec.AccountRef
		if quota.Email != "" {
			accountRef = quota.Email
		}
		_, err = p.db.ExecContext(ctx, `
UPDATE storage_providers SET quota_bytes = ?, used_bytes = ?, account_ref = ?, last_sync_at = ? WHERE id = ?
`, quota.LimitBytes, quota.UsedBytes, accountRef, now, id)
		if err != nil {
			return ProviderRecord{}, err
		}
		rec.QuotaBytes = quota.LimitBytes
		rec.UsedBytes = quota.UsedBytes
		rec.FreeBytes = maxInt64(0, quota.LimitBytes-quota.UsedBytes)
		rec.AccountRef = accountRef
		rec.LastSyncAt = now
		return rec, nil
	case "s3":
		var cfg S3Config
		if err := json.Unmarshal(plain, &cfg); err != nil {
			return ProviderRecord{}, err
		}
		client, err := NewS3Client(cfg)
		if err != nil {
			return ProviderRecord{}, err
		}
		if err := client.HeadBucket(ctx); err != nil {
			return ProviderRecord{}, err
		}
		var trackedBytes int64
		_ = p.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(o.size_bytes), 0)
FROM storage_objects o
JOIN object_locations l ON l.object_id = o.id
WHERE l.provider_id = ?`, id).Scan(&trackedBytes)
		_, _ = p.db.ExecContext(ctx, `UPDATE storage_providers SET used_bytes = ?, last_sync_at = ? WHERE id = ?`, trackedBytes, now, id)
		rec.UsedBytes = trackedBytes
		rec.FreeBytes = maxInt64(0, rec.QuotaBytes-trackedBytes)
		rec.LastSyncAt = now
		return rec, nil
	default:
		return rec, nil
	}
}

func (p *PoolManager) SelectProvider(ctx context.Context, fileSize int64, preferredProviderID string) (*ProviderRecord,
	error) {
	providers, err := p.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(preferredProviderID) != "" {
		for i := range providers {
			if providers[i].ID == preferredProviderID && providers[i].Enabled {
				return &providers[i], nil
			}
		}
		return nil, fmt.Errorf("requested provider %q not found or disabled", preferredProviderID)
	}

	eligible := make([]ProviderRecord, 0)
	for _, prv := range providers {
		if !prv.Enabled {
			continue
		}
		if prv.FreeBytes >= fileSize {
			eligible = append(eligible, prv)
		}
	}
	if len(eligible) == 0 {
		// No remote provider configured or has space: fall back cleanly to built-in local blob store.
		return nil, nil
	}

	// Prefer remote cloud accounts (gdrive / s3) over local disk entries so Windows disk stays clean.
	remoteEligible := make([]ProviderRecord, 0, len(eligible))
	for _, prv := range eligible {
		if prv.Kind != "local" {
			remoteEligible = append(remoteEligible, prv)
		}
	}
	candidates := eligible
	if len(remoteEligible) > 0 {
		candidates = remoteEligible
	}

	policy := p.GetRoutingPolicy(ctx)
	switch policy {
	case PolicyPriority:
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].Priority != candidates[j].Priority {
				return candidates[i].Priority < candidates[j].Priority
			}
			return candidates[i].FreeBytes > candidates[j].FreeBytes
		})
		selected := candidates[0]
		return &selected, nil
	case PolicyRoundRobin:
		p.mu.Lock()
		idx := p.rrIndex % len(candidates)
		p.rrIndex++
		p.mu.Unlock()
		selected := candidates[idx]
		return &selected, nil
	default: // PolicyMostFree
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].FreeBytes != candidates[j].FreeBytes {
				return candidates[i].FreeBytes > candidates[j].FreeBytes
			}
			return candidates[i].Priority < candidates[j].Priority
		})
		selected := candidates[0]
		return &selected, nil
	}
}

func (p *PoolManager) Upload(ctx context.Context, project, logicalPath, filename, mimeType, preferredProviderID string,
	reader io.Reader) (PooledObject, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		project = "default"
	}
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." || filename == "/" {
		return PooledObject{}, errors.New("valid filename is required")
	}
	logicalPath = filepath.ToSlash(strings.TrimLeft(strings.TrimSpace(logicalPath), "/\\"))
	if logicalPath == "" {
		logicalPath = filename
	}
	if strings.Contains(logicalPath, "..") {
		return PooledObject{}, errors.New("logicalPath cannot contain traversal")
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
	}

	payload, err := io.ReadAll(io.LimitReader(reader, 256*1024*1024))
	if err != nil {
		return PooledObject{}, err
	}
	sizeBytes := int64(len(payload))
	selected, err := p.SelectProvider(ctx, sizeBytes, preferredProviderID)
	if err != nil {
		return PooledObject{}, err
	}

	// Built-in local fallback when no remote Google Drive / S3 account is connected.
	if selected == nil || selected.Kind == "local" {
		obj, err := p.hub.PutBlob(ctx, project, logicalPath, filename, mimeType, bytes.NewReader(payload))
		if err != nil {
			return PooledObject{}, err
		}
		provID := "local"
		provName := "Local Disk"
		if selected != nil {
			provID = selected.ID
			provName = selected.Name
			_, _ = p.db.ExecContext(ctx, `UPDATE storage_providers SET used_bytes = used_bytes + ? WHERE id = ?`, sizeBytes, selected.ID)
		}
		_, _ = p.db.ExecContext(ctx, `
INSERT INTO object_locations (object_id, provider_id, provider_kind, remote_id)
VALUES (?, ?, 'local', '')
ON CONFLICT(object_id) DO UPDATE SET provider_id = excluded.provider_id, provider_kind = 'local', remote_id = ''
`, obj.ID, provID)
		return PooledObject{
			StorageObject: obj,
			ProviderID:    provID,
			ProviderKind:  "local",
			ProviderName:  provName,
		}, nil
	}

	// Zero-local-disk upload: stream straight to remote Google Drive or S3!
	rec, plainCfg, err := p.loadProviderSecret(ctx, selected.ID)
	if err != nil {
		return PooledObject{}, err
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	var remoteID string
	switch rec.Kind {
	case "gdrive":
		client, err := p.getGDriveClient(rec.ID, plainCfg)
		if err != nil {
			return PooledObject{}, err
		}
		remoteName := fmt.Sprintf("%s__%s", project, strings.ReplaceAll(logicalPath, "/", "__"))
		remoteID, err = client.Upload(ctx, remoteName, mimeType, payload)
		if err != nil {
			return PooledObject{}, err
		}
	case "s3":
		var cfg S3Config
		if err := json.Unmarshal(plainCfg, &cfg); err != nil {
			return PooledObject{}, err
		}
		client, err := NewS3Client(cfg)
		if err != nil {
			return PooledObject{}, err
		}
		remoteID = fmt.Sprintf("%s/%s", project, logicalPath)
		if err := client.PutObject(ctx, remoteID, mimeType, payload); err != nil {
			return PooledObject{}, err
		}
	default:
		return PooledObject{}, fmt.Errorf("unsupported provider kind %q", rec.Kind)
	}

	obj, err := p.hub.RecordRemoteBlob(ctx, project, logicalPath, filename, mimeType, digest, sizeBytes)
	if err != nil {
		return PooledObject{}, err
	}
	_, err = p.db.ExecContext(ctx, `
INSERT INTO object_locations (object_id, provider_id, provider_kind, remote_id)
VALUES (?, ?, ?, ?)
ON CONFLICT(object_id) DO UPDATE SET provider_id = excluded.provider_id, provider_kind = excluded.provider_kind, remote_id = excluded.remote_id
`, obj.ID, rec.ID, rec.Kind, remoteID)
	if err != nil {
		return PooledObject{}, err
	}
	_, _ = p.db.ExecContext(ctx, `UPDATE storage_providers SET used_bytes = used_bytes + ? WHERE id = ?`, sizeBytes, rec.ID)

	return PooledObject{
		StorageObject: obj,
		ProviderID:    rec.ID,
		ProviderKind:  rec.Kind,
		ProviderName:  rec.Name,
		RemoteID:      remoteID,
	}, nil
}

func (p *PoolManager) ListObjects(ctx context.Context, project, searchQuery string, limit int) ([]PooledObject, error) {
	baseObjects, err := p.hub.ListBlobs(ctx, project, searchQuery, limit)
	if err != nil {
		return nil, err
	}
	results := make([]PooledObject, 0, len(baseObjects))
	for _, obj := range baseObjects {
		pooled := PooledObject{
			StorageObject: obj,
			ProviderID:    "local",
			ProviderKind:  "local",
			ProviderName:  "Local Disk",
		}
		var provID, provKind, remoteID, provName string
		err := p.db.QueryRowContext(ctx, `
SELECT l.provider_id, l.provider_kind, l.remote_id, COALESCE(pr.name, 'Local Disk')
FROM object_locations l
LEFT JOIN storage_providers pr ON pr.id = l.provider_id
WHERE l.object_id = ?`, obj.ID).Scan(&provID, &provKind, &remoteID, &provName)
		if err == nil {
			pooled.ProviderID = provID
			pooled.ProviderKind = provKind
			pooled.RemoteID = remoteID
			pooled.ProviderName = provName
		}
		results = append(results, pooled)
	}
	return results, nil
}

func (p *PoolManager) OpenDownload(ctx context.Context, id string) (PooledObject, io.ReadCloser, error) {
	obj, diskPath, err := p.hub.GetBlob(ctx, id)
	if err != nil {
		return PooledObject{}, nil, err
	}
	var provID, provKind, remoteID string
	locErr := p.db.QueryRowContext(ctx, `SELECT provider_id, provider_kind, remote_id FROM object_locations WHERE object_id = ?`, id).Scan(&provID, &provKind, &remoteID)
	if locErr != nil || provKind == "local" || remoteID == "" {
		f, openErr := os.Open(diskPath)
		if openErr != nil {
			return PooledObject{}, nil, openErr
		}
		return PooledObject{StorageObject: obj, ProviderID: "local", ProviderKind: "local",
			ProviderName: "Local Disk"}, f, nil
	}

	rec, plainCfg, err := p.loadProviderSecret(ctx, provID)
	if err != nil {
		return PooledObject{}, nil, err
	}
	var stream io.ReadCloser
	switch rec.Kind {
	case "gdrive":
		client, err := p.getGDriveClient(rec.ID, plainCfg)
		if err != nil {
			return PooledObject{}, nil, err
		}
		stream, _, err = client.Download(ctx, remoteID)
		if err != nil {
			return PooledObject{}, nil, err
		}
	case "s3":
		var cfg S3Config
		if err := json.Unmarshal(plainCfg, &cfg); err != nil {
			return PooledObject{}, nil, err
		}
		client, err := NewS3Client(cfg)
		if err != nil {
			return PooledObject{}, nil, err
		}
		stream, err = client.GetObject(ctx, remoteID)
		if err != nil {
			return PooledObject{}, nil, err
		}
	default:
		return PooledObject{}, nil, fmt.Errorf("unsupported remote provider %q", rec.Kind)
	}
	return PooledObject{
		StorageObject: obj,
		ProviderID:    rec.ID,
		ProviderKind:  rec.Kind,
		ProviderName:  rec.Name,
		RemoteID:      remoteID,
	}, stream, nil
}

func (p *PoolManager) DeleteObject(ctx context.Context, id string) error {
	obj, _, err := p.hub.GetBlob(ctx, id)
	if err != nil {
		return err
	}
	var provID, provKind, remoteID string
	locErr := p.db.QueryRowContext(ctx, `SELECT provider_id, provider_kind, remote_id FROM object_locations WHERE object_id = ?`, id).Scan(&provID, &provKind, &remoteID)
	if locErr == nil && provKind != "local" && remoteID != "" {
		if rec, plainCfg, loadErr := p.loadProviderSecret(ctx, provID); loadErr == nil {
			switch rec.Kind {
			case "gdrive":
				if client, cErr := p.getGDriveClient(rec.ID, plainCfg); cErr == nil {
					_ = client.Delete(ctx, remoteID)
				}
			case "s3":
				var cfg S3Config
				if json.Unmarshal(plainCfg, &cfg) == nil {
					if client, cErr := NewS3Client(cfg); cErr == nil {
						_ = client.DeleteObject(ctx, remoteID)
					}
				}
			}
		}
		_, _ = p.db.ExecContext(ctx, `UPDATE storage_providers SET used_bytes = MAX(0, used_bytes - ?) WHERE id = ?`, obj.SizeBytes, provID)
	}
	_, _ = p.db.ExecContext(ctx, `DELETE FROM object_locations WHERE object_id = ?`, id)
	return p.hub.DeleteBlob(ctx, id)
}

func (p *PoolManager) StartGoogleOAuth(name, clientID, clientSecret, redirectURI string, priority int) (string, string,
	error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return "", "", errors.New("name, clientId, and clientSecret are required")
	}
	sum := sha256.Sum256([]byte(name + ":" + clientID + ":" + time.Now().UTC().Format(time.RFC3339Nano)))
	state := hex.EncodeToString(sum[:16])
	p.mu.Lock()
	p.oauthStates[state] = pendingOAuth{
		Name:         name,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  redirectURI,
		Priority:     priority,
		CreatedAt:    time.Now(),
	}
	p.mu.Unlock()
	return BuildGoogleAuthURL(clientID, redirectURI, state), state, nil
}

func (p *PoolManager) CompleteGoogleOAuth(ctx context.Context, state, code string) (ProviderRecord, error) {
	p.mu.Lock()
	pending, ok := p.oauthStates[state]
	if ok {
		delete(p.oauthStates, state)
	}
	p.mu.Unlock()
	if !ok || time.Since(pending.CreatedAt) > 15*time.Minute {
		return ProviderRecord{}, errors.New("invalid or expired oauth state")
	}
	refreshToken, err := ExchangeGoogleAuthCode(ctx, pending.ClientID, pending.ClientSecret, pending.RedirectURI, code, "")
	if err != nil {
		return ProviderRecord{}, err
	}
	rec, err := p.AddProvider(ctx, ProviderCreateInput{
		Name:     pending.Name,
		Kind:     "gdrive",
		Priority: pending.Priority,
		GDrive: &GDriveConfig{
			ClientID:     pending.ClientID,
			ClientSecret: pending.ClientSecret,
			RefreshToken: refreshToken,
			RootFolder:   "mw-echo",
		},
	})
	if err != nil {
		return ProviderRecord{}, err
	}
	if synced, syncErr := p.SyncQuota(ctx, rec.ID); syncErr == nil {
		return synced, nil
	}
	return rec, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
