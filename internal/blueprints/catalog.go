package blueprints

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

const (
	defaultGitHubAPIBase = "https://api.github.com"
	defaultRawBase       = "https://raw.githubusercontent.com"
)

// ErrNotFound is returned when a blueprint cannot be located by id or slug.
var ErrNotFound = errors.New("blueprint not found")

// Options configures the self-hosted PaaS blueprint catalog and upstream GitHub sniffer.
type Options struct {
	HTTPClient    *http.Client
	GitHubAPIBase string
	RawBase       string
	GitHubToken   string
}

// Blueprint represents a self-hosted Docker Compose blueprint stored in the local catalog.
type Blueprint struct {
	ID          string   `json:"id"`
	Source      string   `json:"source"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	ComposeYAML string   `json:"composeYaml"`
	Tags        []string `json:"tags"`
	SourceURL   string   `json:"sourceUrl"`
	UpdatedAt   string   `json:"updatedAt"`
}

// SourceStatus tracks sync metadata for a blueprint catalog source.
type SourceStatus struct {
	Source      string `json:"source"`
	Repo        string `json:"repo"`
	LastSHA     string `json:"lastSha"`
	LastSniffAt string `json:"lastSniffAt"`
	ItemsCount  int    `json:"itemsCount"`
}

// SniffResult summarizes the outcome of sniffing one upstream blueprint repository.
type SniffResult struct {
	Source       string `json:"source"`
	Repo         string `json:"repo"`
	SHA          string `json:"sha"`
	PreviousSHA  string `json:"previousSha,omitempty"`
	Upserted     int    `json:"upserted"`
	Skipped      bool   `json:"skipped"`
	Status       string `json:"status"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// Catalog manages curated and upstream-sniffed Docker Compose blueprints in HubStore.
type Catalog struct {
	hub           *store.HubStore
	db            *sql.DB
	httpClient    *http.Client
	githubAPIBase string
	rawBase       string
	githubToken   string
}

// NewCatalog initializes the blueprint SQLite schema, seeds curated blueprints, and returns a Catalog.
func NewCatalog(hub *store.HubStore, opts ...Options) (*Catalog, error) {
	if hub == nil || hub.DB() == nil {
		return nil, errors.New("blueprints: hub store is required")
	}

	var cfg Options
	if len(opts) > 0 {
		cfg = opts[0]
	}

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	apiBase := strings.TrimRight(strings.TrimSpace(cfg.GitHubAPIBase), "/")
	if apiBase == "" {
		apiBase = defaultGitHubAPIBase
	}
	rawBase := strings.TrimRight(strings.TrimSpace(cfg.RawBase), "/")
	if rawBase == "" {
		rawBase = defaultRawBase
	}

	catalog := &Catalog{
		hub:           hub,
		db:            hub.DB(),
		httpClient:    client,
		githubAPIBase: apiBase,
		rawBase:       rawBase,
		githubToken:   strings.TrimSpace(cfg.GitHubToken),
	}

	if err := catalog.migrate(); err != nil {
		return nil, fmt.Errorf("blueprints migrate: %w", err)
	}
	if err := catalog.seedCurated(context.Background()); err != nil {
		return nil, fmt.Errorf("blueprints seed: %w", err)
	}
	return catalog, nil
}

func (c *Catalog) migrate() error {
	schemaSQL := `
CREATE TABLE IF NOT EXISTS paas_blueprints (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT 'general',
  description TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT 'latest',
  compose_yaml TEXT NOT NULL,
  tags_json TEXT NOT NULL DEFAULT '[]',
  source_url TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_paas_blueprints_source_cat ON paas_blueprints(source, category);
CREATE INDEX IF NOT EXISTS idx_paas_blueprints_slug ON paas_blueprints(slug);

CREATE VIRTUAL TABLE IF NOT EXISTS paas_blueprints_fts USING fts5(
  bp_id UNINDEXED,
  slug,
  name,
  category,
  description,
  tags
);

CREATE TABLE IF NOT EXISTS paas_blueprint_sources (
  source TEXT PRIMARY KEY,
  repo TEXT NOT NULL,
  last_sha TEXT NOT NULL DEFAULT '',
  last_sniff_at TEXT NOT NULL DEFAULT '',
  items_count INTEGER NOT NULL DEFAULT 0
);
`
	_, err := c.db.Exec(schemaSQL)
	return err
}

func (c *Catalog) seedCurated(ctx context.Context) error {
	seeds := curatedBlueprints()
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, blueprint := range seeds {
		blueprint.UpdatedAt = now
		if err := upsertBlueprintTx(ctx, tx, blueprint); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO paas_blueprint_sources (source, repo, last_sha, last_sniff_at, items_count)
VALUES ('curated', 'mw-echo/curated', 'builtin-v1', ?, ?)
ON CONFLICT(source) DO UPDATE SET
  repo = excluded.repo,
  last_sha = excluded.last_sha,
  last_sniff_at = excluded.last_sniff_at,
  items_count = excluded.items_count
`, now, len(seeds)); err != nil {
		return err
	}

	return tx.Commit()
}

func makeBlueprintID(source, slug string) string {
	cleanSource := strings.ToLower(strings.TrimSpace(source))
	cleanSlug := strings.ToLower(strings.TrimSpace(slug))
	return cleanSource + ":" + cleanSlug
}

func upsertBlueprintTx(ctx context.Context, tx *sql.Tx, blueprint Blueprint) error {
	blueprint.Source = strings.ToLower(strings.TrimSpace(blueprint.Source))
	blueprint.Slug = strings.ToLower(strings.TrimSpace(blueprint.Slug))
	if blueprint.Source == "" {
		blueprint.Source = "curated"
	}
	if blueprint.Slug == "" {
		return errors.New("blueprint slug is required")
	}
	if strings.TrimSpace(blueprint.ID) == "" {
		blueprint.ID = makeBlueprintID(blueprint.Source, blueprint.Slug)
	}
	if strings.TrimSpace(blueprint.Name) == "" {
		blueprint.Name = blueprint.Slug
	}
	if strings.TrimSpace(blueprint.Category) == "" {
		blueprint.Category = "general"
	}
	if strings.TrimSpace(blueprint.Version) == "" {
		blueprint.Version = "latest"
	}
	if blueprint.Tags == nil {
		blueprint.Tags = []string{}
	}
	if strings.TrimSpace(blueprint.UpdatedAt) == "" {
		blueprint.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	tagsJSON, err := json.Marshal(blueprint.Tags)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO paas_blueprints (id, source, slug, name, category, description, version, compose_yaml, tags_json,
	source_url, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  source = excluded.source,
  slug = excluded.slug,
  name = excluded.name,
  category = excluded.category,
  description = excluded.description,
  version = excluded.version,
  compose_yaml = CASE WHEN excluded.compose_yaml != '' THEN excluded.compose_yaml ELSE paas_blueprints.compose_yaml END,
  tags_json = excluded.tags_json,
  source_url = excluded.source_url,
  updated_at = excluded.updated_at
`, blueprint.ID, blueprint.Source, blueprint.Slug, blueprint.Name, blueprint.Category, blueprint.Description, blueprint.Version, blueprint.ComposeYAML, string(tagsJSON), blueprint.SourceURL, blueprint.UpdatedAt); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM paas_blueprints_fts WHERE bp_id = ?`, blueprint.ID); err != nil {
		return err
	}
	tagsJoined := strings.Join(blueprint.Tags, " ")
	if _, err := tx.ExecContext(ctx, `
INSERT INTO paas_blueprints_fts (bp_id, slug, name, category, description, tags)
VALUES (?, ?, ?, ?, ?, ?)
`, blueprint.ID, blueprint.Slug, blueprint.Name, blueprint.Category, blueprint.Description, tagsJoined); err != nil {
		return err
	}
	return nil
}

// Search queries the blueprint catalog using FTS5 when query is non-empty, with optional source and category filters.
func (c *Catalog) Search(ctx context.Context, query, source, category string, limit int) ([]Blueprint, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	trimmedQuery := strings.TrimSpace(query)
	trimmedSource := strings.ToLower(strings.TrimSpace(source))
	if trimmedSource == "all" {
		trimmedSource = ""
	}
	trimmedCategory := strings.ToLower(strings.TrimSpace(category))
	if trimmedCategory == "all" {
		trimmedCategory = ""
	}

	if trimmedQuery != "" {
		ftsExpr := buildFTSQuery(trimmedQuery)
		if ftsExpr != "" {
			return c.searchFTS(ctx, ftsExpr, trimmedSource, trimmedCategory, limit)
		}
	}

	clauses := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if trimmedSource != "" {
		clauses = append(clauses, "LOWER(source) = ?")
		args = append(args, trimmedSource)
	}
	if trimmedCategory != "" {
		clauses = append(clauses, "LOWER(category) = ?")
		args = append(args, trimmedCategory)
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)

	rows, err := c.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, source, slug, name, category, description, version, compose_yaml, tags_json, source_url, updated_at
FROM paas_blueprints
%s
ORDER BY CASE WHEN source = 'curated' THEN 0 ELSE 1 END, name ASC
LIMIT ?`, whereSQL), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanBlueprints(rows)
}

func (c *Catalog) searchFTS(ctx context.Context, ftsExpr, source, category string, limit int) ([]Blueprint, error) {
	clauses := []string{"paas_blueprints_fts MATCH ?"}
	args := []any{ftsExpr}
	if source != "" {
		clauses = append(clauses, "LOWER(b.source) = ?")
		args = append(args, source)
	}
	if category != "" {
		clauses = append(clauses, "LOWER(b.category) = ?")
		args = append(args, category)
	}
	args = append(args, limit)

	querySQL := fmt.Sprintf(`
SELECT b.id, b.source, b.slug, b.name, b.category, b.description, b.version, b.compose_yaml, b.tags_json, b.source_url, b.updated_at
FROM paas_blueprints_fts
JOIN paas_blueprints b ON b.id = paas_blueprints_fts.bp_id
WHERE %s
ORDER BY bm25(paas_blueprints_fts) ASC, b.name ASC
LIMIT ?`, strings.Join(clauses, " AND "))

	rows, err := c.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanBlueprints(rows)
}

func buildFTSQuery(raw string) string {
	tokens := strings.FieldsFunc(raw, func(ch rune) bool {
		return !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_')
	})
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token != "" {
			parts = append(parts, `"`+token+`"*`)
		}
	}
	return strings.Join(parts, " AND ")
}

func scanBlueprints(rows *sql.Rows) ([]Blueprint, error) {
	results := make([]Blueprint, 0)
	for rows.Next() {
		var blueprint Blueprint
		var tagsRaw string
		if err := rows.Scan(
			&blueprint.ID,
			&blueprint.Source,
			&blueprint.Slug,
			&blueprint.Name,
			&blueprint.Category,
			&blueprint.Description,
			&blueprint.Version,
			&blueprint.ComposeYAML,
			&tagsRaw,
			&blueprint.SourceURL,
			&blueprint.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tagsRaw), &blueprint.Tags); err != nil || blueprint.Tags == nil {
			blueprint.Tags = []string{}
		}
		results = append(results, blueprint)
	}
	return results, rows.Err()
}

// Get retrieves a single blueprint by exact ID or slug.
func (c *Catalog) Get(ctx context.Context, idOrSlug string) (Blueprint, error) {
	trimmed := strings.TrimSpace(idOrSlug)
	if trimmed == "" {
		return Blueprint{}, ErrNotFound
	}

	var blueprint Blueprint
	var tagsRaw string
	err := c.db.QueryRowContext(ctx, `
SELECT id, source, slug, name, category, description, version, compose_yaml, tags_json, source_url, updated_at
FROM paas_blueprints
WHERE id = ? OR slug = ?
ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END, updated_at DESC
LIMIT 1
`, trimmed, trimmed, trimmed).Scan(
		&blueprint.ID,
		&blueprint.Source,
		&blueprint.Slug,
		&blueprint.Name,
		&blueprint.Category,
		&blueprint.Description,
		&blueprint.Version,
		&blueprint.ComposeYAML,
		&tagsRaw,
		&blueprint.SourceURL,
		&blueprint.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Blueprint{}, ErrNotFound
	}
	if err != nil {
		return Blueprint{}, err
	}
	if err := json.Unmarshal([]byte(tagsRaw), &blueprint.Tags); err != nil || blueprint.Tags == nil {
		blueprint.Tags = []string{}
	}
	return blueprint, nil
}

// GetCompose returns the raw Docker Compose YAML and suggested filename for a blueprint.
func (c *Catalog) GetCompose(ctx context.Context, idOrSlug string) (string, string, error) {
	blueprint, err := c.Get(ctx, idOrSlug)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(blueprint.ComposeYAML) == "" && strings.HasPrefix(blueprint.SourceURL, "http") {
		fetched, fetchErr := c.fetchText(ctx, blueprint.SourceURL)
		if fetchErr == nil && strings.TrimSpace(fetched) != "" {
			blueprint.ComposeYAML = fetched
			_, _ = c.db.ExecContext(ctx, `UPDATE paas_blueprints SET compose_yaml = ? WHERE id = ?`, fetched, blueprint.ID)
		}
	}
	filename := blueprint.Slug + "-docker-compose.yml"
	return blueprint.ComposeYAML, filename, nil
}

// ListSources returns sync status rows for all catalog sources.
func (c *Catalog) ListSources(ctx context.Context) ([]SourceStatus, error) {
	rows, err := c.db.QueryContext(ctx, `
SELECT source, repo, last_sha, last_sniff_at, items_count
FROM paas_blueprint_sources
ORDER BY CASE WHEN source = 'curated' THEN 0 ELSE 1 END, source ASC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sources := make([]SourceStatus, 0)
	for rows.Next() {
		var statusRow SourceStatus
		if err := rows.Scan(&statusRow.Source, &statusRow.Repo, &statusRow.LastSHA, &statusRow.LastSniffAt,
			&statusRow.ItemsCount); err != nil {
			return nil, err
		}
		sources = append(sources, statusRow)
	}
	return sources, rows.Err()
}

// Count returns the total number of indexed blueprints across all sources.
func (c *Catalog) Count(ctx context.Context) (int, error) {
	var total int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM paas_blueprints`).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func curatedBlueprints() []Blueprint {
	return []Blueprint{
		{
			ID:          "curated:postgres-16",
			Source:      "curated",
			Slug:        "postgres-16",
			Name:        "PostgreSQL 16",
			Category:    "database",
			Description: "Production PostgreSQL 16 relational database with persistent volume, SCRAM auth, and pg_isready healthcheck.",
			Version:     "16-alpine",
			Tags:        []string{"postgres", "postgresql", "sql", "database", "relational"},
			SourceURL:   "https://hub.docker.com/_/postgres",
			ComposeYAML: `services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: ${POSTGRES_USER:-postgres}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-changeme}
      POSTGRES_DB: ${POSTGRES_DB:-app}
    ports:
      - "${POSTGRES_PORT:-5432}:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER:-postgres}"]
      interval: 10s
      timeout: 5s
      retries: 5

volumes:
  postgres_data:
`,
		},
		{
			ID:          "curated:redis-7",
			Source:      "curated",
			Slug:        "redis-7",
			Name:        "Redis 7",
			Category:    "cache",
			Description: "In-memory key-value store and message broker with AOF persistence and password protection.",
			Version:     "7-alpine",
			Tags:        []string{"redis", "cache", "queue", "pubsub", "kv"},
			SourceURL:   "https://hub.docker.com/_/redis",
			ComposeYAML: `services:
  redis:
    image: redis:7-alpine
    restart: unless-stopped
    command: ["redis-server", "--appendonly", "yes", "--requirepass", "${REDIS_PASSWORD:-changeme}"]
    ports:
      - "${REDIS_PORT:-6379}:6379"
    volumes:
      - redis_data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "-a", "${REDIS_PASSWORD:-changeme}", "ping"]
      interval: 10s
      timeout: 5s
      retries: 5

volumes:
  redis_data:
`,
		},
		{
			ID:          "curated:garage-s3",
			Source:      "curated",
			Slug:        "garage-s3",
			Name:        "Garage S3",
			Category:    "storage",
			Description: "Lightweight self-hosted distributed S3-compatible object storage engine built by Deuxfleurs.",
			Version:     "v1.0.1",
			Tags:        []string{"garage", "s3", "object-storage", "backup", "storage"},
			SourceURL:   "https://garagehq.deuxfleurs.fr/",
			ComposeYAML: `services:
  garage:
    image: dxflrs/garage:v1.0.1
    restart: unless-stopped
    ports:
      - "3900:3900"
      - "3901:3901"
      - "3902:3902"
      - "3903:3903"
    volumes:
      - ./garage.toml:/etc/garage.toml:ro
      - garage_meta:/var/lib/garage/meta
      - garage_data:/var/lib/garage/data
    healthcheck:
      test: ["CMD", "/garage", "status"]
      interval: 15s
      timeout: 5s
      retries: 5

volumes:
  garage_meta:
  garage_data:
`,
		},
		{
			ID:          "curated:minio-s3",
			Source:      "curated",
			Slug:        "minio-s3",
			Name:        "MinIO S3",
			Category:    "storage",
			Description: "High-performance S3-compatible object storage server with built-in web administration console.",
			Version:     "latest",
			Tags:        []string{"minio", "s3", "object-storage", "bucket", "storage"},
			SourceURL:   "https://min.io/",
			ComposeYAML: `services:
  minio:
    image: quay.io/minio/minio:latest
    restart: unless-stopped
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: ${MINIO_ROOT_USER:-minioadmin}
      MINIO_ROOT_PASSWORD: ${MINIO_ROOT_PASSWORD:-changeme123}
    ports:
      - "9000:9000"
      - "9001:9001"
    volumes:
      - minio_data:/data
    healthcheck:
      test: ["CMD", "mc", "ready", "local"]
      interval: 15s
      timeout: 5s
      retries: 5

volumes:
  minio_data:
`,
		},
		{
			ID:          "curated:n8n",
			Source:      "curated",
			Slug:        "n8n",
			Name:        "n8n Workflow Automation",
			Category:    "automation",
			Description: "Fair-code visual workflow automation and webhook orchestration platform with persistent SQLite/encryption state.",
			Version:     "latest",
			Tags:        []string{"n8n", "automation", "workflows", "webhooks", "ai"},
			SourceURL:   "https://n8n.io/",
			ComposeYAML: `services:
  n8n:
    image: docker.n8n.io/n8nio/n8n:latest
    restart: unless-stopped
    environment:
      N8N_HOST: ${N8N_HOST:-localhost}
      N8N_PORT: "5678"
      N8N_PROTOCOL: ${N8N_PROTOCOL:-http}
      N8N_ENCRYPTION_KEY: ${N8N_ENCRYPTION_KEY:-replace-with-32-char-secret}
      GENERIC_TIMEZONE: ${TZ:-UTC}
    ports:
      - "5678:5678"
    volumes:
      - n8n_data:/home/node/.n8n
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:5678/healthz || exit 1"]
      interval: 15s
      timeout: 5s
      retries: 5

volumes:
  n8n_data:
`,
		},
		{
			ID:          "curated:uptime-kuma",
			Source:      "curated",
			Slug:        "uptime-kuma",
			Name:        "Uptime Kuma",
			Category:    "monitoring",
			Description: "Self-hosted status page and multi-protocol uptime monitoring dashboard with alert webhooks.",
			Version:     "1",
			Tags:        []string{"uptime", "monitoring", "alerts", "status-page", "kuma"},
			SourceURL:   "https://github.com/louislam/uptime-kuma",
			ComposeYAML: `services:
  uptime-kuma:
    image: louislam/uptime-kuma:1
    restart: unless-stopped
    ports:
      - "3001:3001"
    volumes:
      - uptime_kuma_data:/app/data
    healthcheck:
      test: ["CMD-SHELL", "node extra/healthcheck.js || exit 1"]
      interval: 20s
      timeout: 10s
      retries: 5

volumes:
  uptime_kuma_data:
`,
		},
		{
			ID:          "curated:grafana",
			Source:      "curated",
			Slug:        "grafana",
			Name:        "Grafana",
			Category:    "monitoring",
			Description: "Observability analytics and dashboarding suite supporting Prometheus, Loki, and OpenTelemetry.",
			Version:     "latest",
			Tags:        []string{"grafana", "observability", "dashboards", "metrics", "monitoring"},
			SourceURL:   "https://grafana.com/",
			ComposeYAML: `services:
  grafana:
    image: grafana/grafana-oss:latest
    restart: unless-stopped
    environment:
      GF_SECURITY_ADMIN_USER: ${GF_SECURITY_ADMIN_USER:-admin}
      GF_SECURITY_ADMIN_PASSWORD: ${GF_SECURITY_ADMIN_PASSWORD:-changeme}
    ports:
      - "3000:3000"
    volumes:
      - grafana_data:/var/lib/grafana
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:3000/api/health || exit 1"]
      interval: 15s
      timeout: 5s
      retries: 5

volumes:
  grafana_data:
`,
		},
		{
			ID:          "curated:prometheus",
			Source:      "curated",
			Slug:        "prometheus",
			Name:        "Prometheus",
			Category:    "monitoring",
			Description: "Time-series metrics collector and alerting engine for cloud-native and Docker workloads.",
			Version:     "latest",
			Tags:        []string{"prometheus", "metrics", "tsdb", "observability", "monitoring"},
			SourceURL:   "https://prometheus.io/",
			ComposeYAML: `services:
  prometheus:
    image: prom/prometheus:latest
    restart: unless-stopped
    command:
      - "--config.file=/etc/prometheus/prometheus.yml"
      - "--storage.tsdb.path=/prometheus"
      - "--storage.tsdb.retention.time=30d"
    ports:
      - "9090:9090"
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml:ro
      - prometheus_data:/prometheus
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:9090/-/healthy || exit 1"]
      interval: 15s
      timeout: 5s
      retries: 5

volumes:
  prometheus_data:
`,
		},
		{
			ID:          "curated:qdrant",
			Source:      "curated",
			Slug:        "qdrant",
			Name:        "Qdrant Vector DB",
			Category:    "ai",
			Description: "High-speed Rust vector similarity search engine for RAG embeddings, agents, and semantic memory.",
			Version:     "latest",
			Tags:        []string{"qdrant", "vector", "embeddings", "rag", "ai"},
			SourceURL:   "https://qdrant.tech/",
			ComposeYAML: `services:
  qdrant:
    image: qdrant/qdrant:latest
    restart: unless-stopped
    environment:
      QDRANT__SERVICE__API_KEY: ${QDRANT_API_KEY:-}
    ports:
      - "6333:6333"
      - "6334:6334"
    volumes:
      - qdrant_data:/qdrant/storage
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:6333/healthz || exit 1"]
      interval: 15s
      timeout: 5s
      retries: 5

volumes:
  qdrant_data:
`,
		},
		{
			ID:          "curated:ollama",
			Source:      "curated",
			Slug:        "ollama",
			Name:        "Ollama Local LLM",
			Category:    "ai",
			Description: "Local open-weights LLM runtime with OpenAI-compatible HTTP inference API and persistent model store.",
			Version:     "latest",
			Tags:        []string{"ollama", "llm", "ai", "inference", "local"},
			SourceURL:   "https://ollama.com/",
			ComposeYAML: `services:
  ollama:
    image: ollama/ollama:latest
    restart: unless-stopped
    ports:
      - "11434:11434"
    volumes:
      - ollama_models:/root/.ollama
    healthcheck:
      test: ["CMD", "ollama", "list"]
      interval: 20s
      timeout: 10s
      retries: 5

volumes:
  ollama_models:
`,
		},
		{
			ID:          "curated:cloudflared",
			Source:      "curated",
			Slug:        "cloudflared",
			Name:        "Cloudflare Tunnel (cloudflared)",
			Category:    "networking",
			Description: "Zero-trust outbound Cloudflare Tunnel daemon exposing local services without opening inbound firewall ports.",
			Version:     "latest",
			Tags:        []string{"cloudflared", "cloudflare", "tunnel", "networking", "ingress"},
			SourceURL:   "https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/",
			ComposeYAML: `services:
  cloudflared:
    image: cloudflare/cloudflared:latest
    restart: unless-stopped
    command: tunnel --no-autoupdate run --token ${CLOUDFLARE_TUNNEL_TOKEN}
    environment:
      NO_AUTOUPDATE: "true"
`,
		},
		{
			ID:          "curated:mw-echo",
			Source:      "curated",
			Slug:        "mw-echo",
			Name:        "MW-Echo Control Plane",
			Category:    "platform",
			Description: "MoonWitness Echo single-binary control plane: AI Gateway, 9Drive storage pool, WhatsApp bridge, World Radar, Flows, and PaaS blueprints.",
			Version:     "latest",
			Tags:        []string{"mw-echo", "moonwitness", "ai-gateway", "whatsapp", "control-plane"},
			SourceURL:   "https://github.com/A-L-L-A-H/mw-echo",
			ComposeYAML: `services:
  mw-echo:
    image: ghcr.io/a-l-l-a-h/mw-echo:latest
    restart: unless-stopped
    ports:
      - "4310:4310"
    environment:
      MW_ECHO_ADDR: ":4310"
      MW_ECHO_DATA_DIR: "/data"
      MW_ECHO_TOKEN: ${MW_ECHO_TOKEN:-}
    volumes:
      - mw_echo_data:/data
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:4310/health || exit 1"]
      interval: 15s
      timeout: 5s
      retries: 5

volumes:
  mw_echo_data:
`,
		},
	}
}
