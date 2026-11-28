package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type HubStore struct {
	db      *sql.DB
	dataDir string
	blobDir string
	closers []func() error
}

type DocRecord struct {
	ID        string `json:"id"`
	Project   string `json:"project"`
	RelPath   string `json:"relPath"`
	Title     string `json:"title"`
	Category  string `json:"category"`
	Content   string `json:"content,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
	UpdatedAt string `json:"updatedAt"`
}

type TaskRecord struct {
	Code        string   `json:"code"`
	Project     string   `json:"project"`
	Series      string   `json:"series"`
	Milestone   string   `json:"milestone"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Priority    string   `json:"priority"`
	Status      string   `json:"status"`
	DependsOn   []string `json:"dependsOn"`
	Tags        []string `json:"tags"`
	PayloadJSON string   `json:"payloadJson,omitempty"`
	UpdatedAt   string   `json:"updatedAt"`
}

type StorageObject struct {
	ID          string `json:"id"`
	Project     string `json:"project"`
	LogicalPath string `json:"logicalPath"`
	Filename    string `json:"filename"`
	MimeType    string `json:"mimeType"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"sizeBytes"`
	CreatedAt   string `json:"createdAt"`
}

type HubStats struct {
	DocsCount         int            `json:"docsCount"`
	TasksCount        int            `json:"tasksCount"`
	StorageCount      int            `json:"storageCount"`
	StorageTotalBytes int64          `json:"storageTotalBytes"`
	TasksByStatus     map[string]int `json:"tasksByStatus"`
	DocsByProject     map[string]int `json:"docsByProject"`
	DataDir           string         `json:"dataDir"`
}

func Open(dataDir string) (*HubStore, error) {
	cleanDir := filepath.Clean(dataDir)
	blobDir := filepath.Join(cleanDir, "blobs")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dirs: %w", err)
	}
	dbPath := filepath.Join(cleanDir, "mw-echo.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
		filepath.ToSlash(dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	hub := &HubStore{db: db, dataDir: cleanDir, blobDir: blobDir}
	if err := hub.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return hub, nil
}

// RegisterCloser registers a module cleanup callback invoked when HubStore.Close runs.
func (s *HubStore) RegisterCloser(fn func() error) {
	if fn != nil {
		s.closers = append(s.closers, fn)
	}
}

func (s *HubStore) Close() error {
	var firstErr error
	for i := len(s.closers) - 1; i >= 0; i-- {
		if err := s.closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := s.db.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *HubStore) DataDir() string {
	return s.dataDir
}

func (s *HubStore) DB() *sql.DB {
	return s.db
}

func (s *HubStore) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS docs (
  id TEXT PRIMARY KEY,
  project TEXT NOT NULL,
  rel_path TEXT NOT NULL,
  title TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT 'general',
  content TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(project, rel_path)
);

CREATE VIRTUAL TABLE IF NOT EXISTS docs_fts USING fts5(
  doc_id UNINDEXED,
  project,
  rel_path,
  title,
  category,
  content
);

CREATE TABLE IF NOT EXISTS tasks (
  code TEXT PRIMARY KEY,
  project TEXT NOT NULL,
  series TEXT NOT NULL DEFAULT '',
  milestone TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  priority TEXT NOT NULL DEFAULT 'p2',
  status TEXT NOT NULL DEFAULT 'todo',
  depends_json TEXT NOT NULL DEFAULT '[]',
  tags_json TEXT NOT NULL DEFAULT '[]',
  payload_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_project_status ON tasks(project, status);

CREATE TABLE IF NOT EXISTS storage_objects (
  id TEXT PRIMARY KEY,
  project TEXT NOT NULL,
  logical_path TEXT NOT NULL,
  filename TEXT NOT NULL,
  mime_type TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(project, logical_path)
);
CREATE INDEX IF NOT EXISTS idx_storage_sha ON storage_objects(sha256);

CREATE TABLE IF NOT EXISTS projects (
  slug TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  repo_path TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  color TEXT NOT NULL DEFAULT '#6366f1',
  default_agent TEXT NOT NULL DEFAULT 'default',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate sqlite schema: %w", err)
	}
	return nil
}

func makeDeterministicID(parts ...string) string {
	hasher := sha256.New()
	for _, part := range parts {
		hasher.Write([]byte(part))
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))[:24]
}

func (s *HubStore) UpsertDoc(ctx context.Context, record DocRecord) (DocRecord, error) {
	record.Project = strings.TrimSpace(record.Project)
	if record.Project == "" {
		record.Project = "default"
	}
	record.RelPath = filepath.ToSlash(strings.TrimLeft(strings.TrimSpace(record.RelPath), "/\\"))
	if record.RelPath == "" {
		return DocRecord{}, errors.New("relPath is required")
	}
	if strings.TrimSpace(record.Title) == "" {
		record.Title = deriveTitleFromMarkdown(record.RelPath, record.Content)
	}
	if strings.TrimSpace(record.Category) == "" {
		record.Category = deriveCategoryFromPath(record.RelPath)
	}
	sum := sha256.Sum256([]byte(record.Content))
	record.SHA256 = hex.EncodeToString(sum[:])
	record.SizeBytes = int64(len(record.Content))
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if strings.TrimSpace(record.ID) == "" {
		record.ID = makeDeterministicID("doc", record.Project, record.RelPath)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DocRecord{}, err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
INSERT INTO docs (id, project, rel_path, title, category, content, sha256, size_bytes, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project, rel_path) DO UPDATE SET
  title = excluded.title,
  category = excluded.category,
  content = excluded.content,
  sha256 = excluded.sha256,
  size_bytes = excluded.size_bytes,
  updated_at = excluded.updated_at
`, record.ID, record.Project, record.RelPath, record.Title, record.Category, record.Content, record.SHA256, record.SizeBytes, record.UpdatedAt)
	if err != nil {
		return DocRecord{}, err
	}

	var storedID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM docs WHERE project = ? AND rel_path = ?`, record.Project, record.RelPath).Scan(&storedID); err != nil {
		return DocRecord{}, err
	}
	record.ID = storedID

	if _, err := tx.ExecContext(ctx, `DELETE FROM docs_fts WHERE doc_id = ?`, record.ID); err != nil {
		return DocRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO docs_fts (doc_id, project, rel_path, title, category, content)
VALUES (?, ?, ?, ?, ?, ?)
`, record.ID, record.Project, record.RelPath, record.Title, record.Category, record.Content); err != nil {
		return DocRecord{}, err
	}

	if err := tx.Commit(); err != nil {
		return DocRecord{}, err
	}
	return record, nil
}

func (s *HubStore) ListDocs(ctx context.Context, project, category, searchQuery string, limit int) ([]DocRecord,
	error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	trimmedQuery := strings.TrimSpace(searchQuery)
	if trimmedQuery != "" {
		return s.searchDocsFTS(ctx, project, category, trimmedQuery, limit)
	}

	var clauses []string
	var args []any
	if strings.TrimSpace(project) != "" {
		clauses = append(clauses, "project = ?")
		args = append(args, strings.TrimSpace(project))
	}
	if strings.TrimSpace(category) != "" {
		clauses = append(clauses, "category = ?")
		args = append(args, strings.TrimSpace(category))
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, project, rel_path, title, category, sha256, size_bytes, updated_at
FROM docs %s ORDER BY updated_at DESC, rel_path ASC LIMIT ?`, whereSQL), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]DocRecord, 0)
	for rows.Next() {
		var rec DocRecord
		if err := rows.Scan(&rec.ID, &rec.Project, &rec.RelPath, &rec.Title, &rec.Category, &rec.SHA256, &rec.SizeBytes,
			&rec.UpdatedAt); err != nil {
			return nil, err
		}
		results = append(results, rec)
	}
	return results, rows.Err()
}

func sanitizeFTSQuery(raw string) string {
	tokens := strings.Fields(raw)
	if len(tokens) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		clean := strings.ReplaceAll(tok, `"`, "")
		if clean != "" {
			quoted = append(quoted, `"`+clean+`"*`)
		}
	}
	return strings.Join(quoted, " AND ")
}

func (s *HubStore) searchDocsFTS(ctx context.Context, project, category, rawQuery string, limit int) ([]DocRecord,
	error) {
	ftsExpr := sanitizeFTSQuery(rawQuery)
	if ftsExpr == "" {
		return []DocRecord{}, nil
	}
	clauses := []string{"docs_fts MATCH ?"}
	args := []any{ftsExpr}
	if strings.TrimSpace(project) != "" {
		clauses = append(clauses, "d.project = ?")
		args = append(args, strings.TrimSpace(project))
	}
	if strings.TrimSpace(category) != "" {
		clauses = append(clauses, "d.category = ?")
		args = append(args, strings.TrimSpace(category))
	}
	args = append(args, limit)

	querySQL := fmt.Sprintf(`
SELECT d.id, d.project, d.rel_path, d.title, d.category,
       snippet(docs_fts, 5, '[', ']', '...', 24) AS snippet,
       d.sha256, d.size_bytes, d.updated_at
FROM docs_fts
JOIN docs d ON d.id = docs_fts.doc_id
WHERE %s
ORDER BY bm25(docs_fts) ASC
LIMIT ?`, strings.Join(clauses, " AND "))

	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]DocRecord, 0)
	for rows.Next() {
		var rec DocRecord
		if err := rows.Scan(&rec.ID, &rec.Project, &rec.RelPath, &rec.Title, &rec.Category, &rec.Snippet, &rec.SHA256,
			&rec.SizeBytes, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		results = append(results, rec)
	}
	return results, rows.Err()
}

func (s *HubStore) GetDoc(ctx context.Context, idOrPath string) (DocRecord, error) {
	var rec DocRecord
	err := s.db.QueryRowContext(ctx, `
SELECT id, project, rel_path, title, category, content, sha256, size_bytes, updated_at
FROM docs WHERE id = ? OR rel_path = ? LIMIT 1
`, idOrPath, filepath.ToSlash(idOrPath)).Scan(
		&rec.ID, &rec.Project, &rec.RelPath, &rec.Title, &rec.Category, &rec.Content, &rec.SHA256, &rec.SizeBytes, &rec.UpdatedAt,
	)
	return rec, err
}

func (s *HubStore) DeleteDoc(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM docs_fts WHERE doc_id = ?`, id); err != nil {
		return err
	}
	execRes, err := tx.ExecContext(ctx, `DELETE FROM docs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, _ := execRes.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *HubStore) UpsertTask(ctx context.Context, task TaskRecord) (TaskRecord, error) {
	task.Code = strings.TrimSpace(task.Code)
	if task.Code == "" {
		return TaskRecord{}, errors.New("task code is required")
	}
	if strings.TrimSpace(task.Project) == "" {
		task.Project = "moonwitness"
	}
	if strings.TrimSpace(task.Priority) == "" {
		task.Priority = "p2"
	}
	if strings.TrimSpace(task.Status) == "" {
		task.Status = "todo"
	}
	if task.DependsOn == nil {
		task.DependsOn = []string{}
	}
	if task.Tags == nil {
		task.Tags = []string{}
	}
	depsBytes, _ := json.Marshal(task.DependsOn)
	tagsBytes, _ := json.Marshal(task.Tags)
	if strings.TrimSpace(task.PayloadJSON) == "" {
		task.PayloadJSON = "{}"
	}
	task.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	_, err := s.db.ExecContext(ctx, `
INSERT INTO tasks (code, project, series, milestone, title, summary, priority, status, depends_json, tags_json,
	payload_json, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(code) DO UPDATE SET
  project = excluded.project,
  series = excluded.series,
  milestone = excluded.milestone,
  title = excluded.title,
  summary = excluded.summary,
  priority = excluded.priority,
  status = excluded.status,
  depends_json = excluded.depends_json,
  tags_json = excluded.tags_json,
  payload_json = excluded.payload_json,
  updated_at = excluded.updated_at
`, task.Code, task.Project, task.Series, task.Milestone, task.Title, task.Summary, task.Priority, task.Status, string(depsBytes), string(tagsBytes), task.PayloadJSON, task.UpdatedAt)
	return task, err
}

func (s *HubStore) UpdateTaskStatus(ctx context.Context, code, status, priority, title string) (TaskRecord, error) {
	existing, err := s.GetTask(ctx, code)
	if err != nil {
		return TaskRecord{}, err
	}
	if strings.TrimSpace(status) != "" {
		existing.Status = strings.TrimSpace(status)
	}
	if strings.TrimSpace(priority) != "" {
		existing.Priority = strings.TrimSpace(priority)
	}
	if strings.TrimSpace(title) != "" {
		existing.Title = strings.TrimSpace(title)
	}
	// Keep raw payload JSON synchronized if present.
	var payloadMap map[string]any
	if err := json.Unmarshal([]byte(existing.PayloadJSON), &payloadMap); err == nil && len(payloadMap) > 0 {
		payloadMap["status"] = existing.Status
		payloadMap["priority"] = existing.Priority
		payloadMap["title"] = existing.Title
		if rawBytes, err := json.Marshal(payloadMap); err == nil {
			existing.PayloadJSON = string(rawBytes)
		}
	}
	return s.UpsertTask(ctx, existing)
}

func (s *HubStore) GetTask(ctx context.Context, code string) (TaskRecord, error) {
	var task TaskRecord
	var depsRaw, tagsRaw string
	err := s.db.QueryRowContext(ctx, `
SELECT code, project, series, milestone, title, summary, priority, status, depends_json, tags_json, payload_json, updated_at
FROM tasks WHERE code = ?
`, code).Scan(
		&task.Code, &task.Project, &task.Series, &task.Milestone, &task.Title, &task.Summary,
		&task.Priority, &task.Status, &depsRaw, &tagsRaw, &task.PayloadJSON, &task.UpdatedAt,
	)
	if err != nil {
		return TaskRecord{}, err
	}
	_ = json.Unmarshal([]byte(depsRaw), &task.DependsOn)
	_ = json.Unmarshal([]byte(tagsRaw), &task.Tags)
	return task, nil
}

func (s *HubStore) ListTasks(ctx context.Context, project, status, priority, searchQuery string,
	limit int) ([]TaskRecord, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	var clauses []string
	var args []any
	if strings.TrimSpace(project) != "" {
		clauses = append(clauses, "project = ?")
		args = append(args, strings.TrimSpace(project))
	}
	if strings.TrimSpace(status) != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, strings.TrimSpace(status))
	}
	if strings.TrimSpace(priority) != "" {
		clauses = append(clauses, "priority = ?")
		args = append(args, strings.TrimSpace(priority))
	}
	if strings.TrimSpace(searchQuery) != "" {
		pattern := "%" + strings.ToLower(strings.TrimSpace(searchQuery)) + "%"
		clauses = append(clauses, "(LOWER(code) LIKE ? OR LOWER(title) LIKE ? OR LOWER(summary) LIKE ?)")
		args = append(args, pattern, pattern, pattern)
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT code, project, series, milestone, title, summary, priority, status, depends_json, tags_json, updated_at
FROM tasks %s
ORDER BY CASE status WHEN 'doing' THEN 0 WHEN 'todo' THEN 1 WHEN 'blocked' THEN 2 WHEN 'backlog' THEN 3 ELSE 4 END, priority ASC, code ASC
LIMIT ?`, whereSQL), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := make([]TaskRecord, 0)
	for rows.Next() {
		var task TaskRecord
		var depsRaw, tagsRaw string
		if err := rows.Scan(
			&task.Code, &task.Project, &task.Series, &task.Milestone, &task.Title, &task.Summary,
			&task.Priority, &task.Status, &depsRaw, &tagsRaw, &task.UpdatedAt,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(depsRaw), &task.DependsOn)
		_ = json.Unmarshal([]byte(tagsRaw), &task.Tags)
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *HubStore) DeleteTask(ctx context.Context, code string) error {
	execRes, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE code = ?`, code)
	if err != nil {
		return err
	}
	affected, _ := execRes.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *HubStore) ExportTaskosV2(ctx context.Context) (map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT code, project, series, milestone, title, summary, priority, status, depends_json, tags_json, payload_json FROM tasks ORDER BY code ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projectSet := map[string]bool{}
	exportedTasks := make([]any, 0)
	for rows.Next() {
		var task TaskRecord
		var depsRaw, tagsRaw string
		if err := rows.Scan(&task.Code, &task.Project, &task.Series, &task.Milestone, &task.Title, &task.Summary,
			&task.Priority, &task.Status, &depsRaw, &tagsRaw, &task.PayloadJSON); err != nil {
			return nil, err
		}
		projectSet[task.Project] = true
		var rawMap map[string]any
		if err := json.Unmarshal([]byte(task.PayloadJSON), &rawMap); err == nil && len(rawMap) > 0 {
			rawMap["status"] = task.Status
			rawMap["priority"] = task.Priority
			rawMap["title"] = task.Title
			exportedTasks = append(exportedTasks, rawMap)
			continue
		}
		_ = json.Unmarshal([]byte(depsRaw), &task.DependsOn)
		_ = json.Unmarshal([]byte(tagsRaw), &task.Tags)
		exportedTasks = append(exportedTasks, map[string]any{
			"code":      task.Code,
			"project":   task.Project,
			"series":    task.Series,
			"milestone": task.Milestone,
			"title":     task.Title,
			"summary":   task.Summary,
			"priority":  task.Priority,
			"status":    task.Status,
			"dependsOn": task.DependsOn,
			"tags":      task.Tags,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	projects := make([]map[string]any, 0, len(projectSet))
	for slug := range projectSet {
		projects = append(projects, map[string]any{"slug": slug, "name": slug})
	}
	return map[string]any{
		"schemaVersion": "2.0.0",
		"format":        "moonwitness-tasks/v2",
		"generatedAt":   time.Now().UTC().Format(time.RFC3339),
		"projects":      projects,
		"tasks":         exportedTasks,
	}, nil
}

func (s *HubStore) Stats(ctx context.Context) (HubStats, error) {
	stats := HubStats{
		TasksByStatus: map[string]int{},
		DocsByProject: map[string]int{},
		DataDir:       s.dataDir,
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM docs`).Scan(&stats.DocsCount)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&stats.TasksCount)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(size_bytes), 0) FROM storage_objects`).Scan(&stats.StorageCount, &stats.StorageTotalBytes)

	statusRows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM tasks GROUP BY status`)
	if err == nil {
		defer statusRows.Close()
		for statusRows.Next() {
			var key string
			var count int
			if statusRows.Scan(&key, &count) == nil {
				stats.TasksByStatus[key] = count
			}
		}
	}
	docRows, err := s.db.QueryContext(ctx, `SELECT project, COUNT(*) FROM docs GROUP BY project`)
	if err == nil {
		defer docRows.Close()
		for docRows.Next() {
			var key string
			var count int
			if docRows.Scan(&key, &count) == nil {
				stats.DocsByProject[key] = count
			}
		}
	}
	return stats, nil
}

func deriveTitleFromMarkdown(relPath, content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
	}
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func deriveCategoryFromPath(relPath string) string {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) > 1 && parts[0] != "" {
		return parts[0]
	}
	return "root"
}
