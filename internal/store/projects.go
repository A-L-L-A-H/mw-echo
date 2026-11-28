package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

// ProjectRecord stores project-level metadata for mw-echo project management.
type ProjectRecord struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	RepoPath     string `json:"repoPath"`
	Status       string `json:"status"`
	Color        string `json:"color"`
	DefaultAgent string `json:"defaultAgent"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

// MilestoneSummary aggregates task progress inside one milestone of a project.
type MilestoneSummary struct {
	Name            string  `json:"name"`
	TotalTasks      int     `json:"totalTasks"`
	DoneTasks       int     `json:"doneTasks"`
	InProgressTasks int     `json:"inProgressTasks"`
	TodoTasks       int     `json:"todoTasks"`
	BlockedTasks    int     `json:"blockedTasks"`
	ProgressPct     float64 `json:"progressPct"`
}

// ProjectSummary combines ProjectRecord metadata with live task, doc, storage, and milestone rollups.
type ProjectSummary struct {
	ProjectRecord
	TotalTasks      int                `json:"totalTasks"`
	DoneTasks       int                `json:"doneTasks"`
	InProgressTasks int                `json:"inProgressTasks"`
	TodoTasks       int                `json:"todoTasks"`
	BlockedTasks    int                `json:"blockedTasks"`
	DocsCount       int                `json:"docsCount"`
	StorageCount    int                `json:"storageCount"`
	StorageBytes    int64              `json:"storageBytes"`
	ProgressPct     float64            `json:"progressPct"`
	Milestones      []MilestoneSummary `json:"milestones"`
}

var defaultProjectColors = []string{
	"#6366f1",
	"#10b981",
	"#0ea5e9",
	"#f59e0b",
	"#ec4899",
	"#8b5cf6",
}

// EnsureProjectsSchema creates the projects table idempotently.
func (s *HubStore) EnsureProjectsSchema() error {
	_, err := s.db.Exec(`
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
);`)
	return err
}

func normalizeProjectSlug(raw string) string {
	cleaned := strings.ToLower(strings.TrimSpace(raw))
	if cleaned == "" {
		return ""
	}
	var out strings.Builder
	lastDash := false
	for _, r := range cleaned {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			out.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !lastDash && out.Len() > 0 {
				out.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(out.String(), "-")
}

func humanizeSlug(slug string) string {
	parts := strings.FieldsFunc(slug, func(r rune) bool {
		return r == '-' || r == '_'
	})
	for i, part := range parts {
		if len(part) > 0 {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	if len(parts) == 0 {
		return slug
	}
	return strings.Join(parts, " ")
}

func pickProjectColor(slug string) string {
	if len(defaultProjectColors) == 0 {
		return "#6366f1"
	}
	sum := 0
	for i := 0; i < len(slug); i++ {
		sum += int(slug[i])
	}
	return defaultProjectColors[sum%len(defaultProjectColors)]
}

func (s *HubStore) syncDiscoveredProjects(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT project FROM (
  SELECT project FROM tasks WHERE TRIM(project) <> ''
  UNION
  SELECT project FROM docs WHERE TRIM(project) <> ''
  UNION
  SELECT project FROM storage_objects WHERE TRIM(project) <> ''
)`)
	if err != nil {
		return err
	}
	var slugs []string
	for rows.Next() {
		var slug string
		if rows.Scan(&slug) == nil && strings.TrimSpace(slug) != "" {
			slugs = append(slugs, strings.TrimSpace(slug))
		}
	}
	_ = rows.Close()

	for _, slug := range slugs {
		_, err := s.db.ExecContext(ctx, `
INSERT INTO projects (slug, name, description, repo_path, status, color, default_agent, created_at, updated_at)
VALUES (?, ?, '', '', 'active', ?, 'default', ?, ?)
ON CONFLICT(slug) DO NOTHING`,
			slug, humanizeSlug(slug), pickProjectColor(slug), now, now,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// UpsertProject inserts or updates a project record.
func (s *HubStore) UpsertProject(ctx context.Context, record ProjectRecord) (ProjectRecord, error) {
	if err := s.EnsureProjectsSchema(); err != nil {
		return ProjectRecord{}, err
	}
	slug := normalizeProjectSlug(record.Slug)
	if slug == "" && strings.TrimSpace(record.Name) != "" {
		slug = normalizeProjectSlug(record.Name)
	}
	if slug == "" {
		return ProjectRecord{}, errors.New("project slug or name is required")
	}
	record.Slug = slug
	if strings.TrimSpace(record.Name) == "" {
		record.Name = humanizeSlug(slug)
	} else {
		record.Name = strings.TrimSpace(record.Name)
	}
	record.Description = strings.TrimSpace(record.Description)
	record.RepoPath = strings.TrimSpace(record.RepoPath)
	record.Status = strings.ToLower(strings.TrimSpace(record.Status))
	if record.Status == "" {
		record.Status = "active"
	}
	record.Color = strings.TrimSpace(record.Color)
	if record.Color == "" {
		record.Color = pickProjectColor(slug)
	}
	record.DefaultAgent = strings.TrimSpace(record.DefaultAgent)
	if record.DefaultAgent == "" {
		record.DefaultAgent = "default"
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var existingCreatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT created_at FROM projects WHERE slug = ?`, record.Slug).Scan(&existingCreatedAt)
	if err == nil && existingCreatedAt != "" {
		record.CreatedAt = existingCreatedAt
	} else {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	_, err = s.db.ExecContext(ctx, `
INSERT INTO projects (slug, name, description, repo_path, status, color, default_agent, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(slug) DO UPDATE SET
  name = excluded.name,
  description = excluded.description,
  repo_path = excluded.repo_path,
  status = excluded.status,
  color = excluded.color,
  default_agent = excluded.default_agent,
  updated_at = excluded.updated_at
`, record.Slug, record.Name, record.Description, record.RepoPath, record.Status, record.Color, record.DefaultAgent, record.CreatedAt, record.UpdatedAt)
	if err != nil {
		return ProjectRecord{}, err
	}
	return record, nil
}

// ListProjects returns all projects (including auto-discovered imported projects) with live progress rollups.
func (s *HubStore) ListProjects(ctx context.Context) ([]ProjectSummary, error) {
	if err := s.EnsureProjectsSchema(); err != nil {
		return nil, err
	}
	if err := s.syncDiscoveredProjects(ctx); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT slug, name, description, repo_path, status, color, default_agent, created_at, updated_at
FROM projects
ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'paused' THEN 1 ELSE 2 END, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	summaries := make([]*ProjectSummary, 0)
	bySlug := make(map[string]*ProjectSummary)
	for rows.Next() {
		var rec ProjectRecord
		if err := rows.Scan(&rec.Slug, &rec.Name, &rec.Description, &rec.RepoPath, &rec.Status, &rec.Color, &rec.DefaultAgent,
			&rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		item := &ProjectSummary{
			ProjectRecord: rec,
			Milestones:    []MilestoneSummary{},
		}
		summaries = append(summaries, item)
		bySlug[rec.Slug] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	milestoneMap := make(map[string]map[string]*MilestoneSummary)
	taskRows, err := s.db.QueryContext(ctx, `
SELECT project, milestone, status, COUNT(*)
FROM tasks
GROUP BY project, milestone, status`)
	if err != nil {
		return nil, err
	}
	for taskRows.Next() {
		var projectSlug, milestoneName, taskStatus string
		var cnt int
		if err := taskRows.Scan(&projectSlug, &milestoneName, &taskStatus, &cnt); err != nil {
			_ = taskRows.Close()
			return nil, err
		}
		proj, ok := bySlug[projectSlug]
		if !ok {
			continue
		}
		mName := strings.TrimSpace(milestoneName)
		if mName == "" {
			mName = "General"
		}
		if milestoneMap[projectSlug] == nil {
			milestoneMap[projectSlug] = map[string]*MilestoneSummary{}
		}
		ms := milestoneMap[projectSlug][mName]
		if ms == nil {
			ms = &MilestoneSummary{Name: mName}
			milestoneMap[projectSlug][mName] = ms
		}

		proj.TotalTasks += cnt
		ms.TotalTasks += cnt
		switch normalizeTaskBucket(taskStatus) {
		case "done":
			proj.DoneTasks += cnt
			ms.DoneTasks += cnt
		case "in_progress":
			proj.InProgressTasks += cnt
			ms.InProgressTasks += cnt
		case "blocked":
			proj.BlockedTasks += cnt
			ms.BlockedTasks += cnt
		default:
			proj.TodoTasks += cnt
			ms.TodoTasks += cnt
		}
	}
	_ = taskRows.Close()

	docRows, err := s.db.QueryContext(ctx, `SELECT project, COUNT(*) FROM docs GROUP BY project`)
	if err == nil {
		for docRows.Next() {
			var projectSlug string
			var cnt int
			if docRows.Scan(&projectSlug, &cnt) == nil {
				if proj, ok := bySlug[projectSlug]; ok {
					proj.DocsCount = cnt
				}
			}
		}
		_ = docRows.Close()
	}

	storageRows, err := s.db.QueryContext(ctx, `SELECT project, COUNT(*), COALESCE(SUM(size_bytes), 0) FROM storage_objects GROUP BY project`)
	if err == nil {
		for storageRows.Next() {
			var projectSlug string
			var cnt int
			var totalBytes int64
			if storageRows.Scan(&projectSlug, &cnt, &totalBytes) == nil {
				if proj, ok := bySlug[projectSlug]; ok {
					proj.StorageCount = cnt
					proj.StorageBytes = totalBytes
				}
			}
		}
		_ = storageRows.Close()
	}

	out := make([]ProjectSummary, 0, len(summaries))
	for _, proj := range summaries {
		if proj.TotalTasks > 0 {
			proj.ProgressPct = math.Round((float64(proj.DoneTasks)/float64(proj.TotalTasks))*1000) / 10
		}
		if msByName := milestoneMap[proj.Slug]; len(msByName) > 0 {
			list := make([]MilestoneSummary, 0, len(msByName))
			for _, ms := range msByName {
				if ms.TotalTasks > 0 {
					ms.ProgressPct = math.Round((float64(ms.DoneTasks)/float64(ms.TotalTasks))*1000) / 10
				}
				list = append(list, *ms)
			}
			sort.Slice(list, func(i, j int) bool {
				return list[i].Name < list[j].Name
			})
			proj.Milestones = list
		}
		out = append(out, *proj)
	}
	return out, nil
}

// GetProject returns a single project with full rollup metrics.
func (s *HubStore) GetProject(ctx context.Context, slug string) (ProjectSummary, error) {
	cleanSlug := strings.TrimSpace(slug)
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return ProjectSummary{}, err
	}
	for _, proj := range projects {
		if proj.Slug == cleanSlug {
			return proj, nil
		}
	}
	return ProjectSummary{}, sql.ErrNoRows
}

// DeleteProject removes project metadata row by slug (tasks/docs remain intact unless explicitly deleted).
func (s *HubStore) DeleteProject(ctx context.Context, slug string) error {
	if err := s.EnsureProjectsSchema(); err != nil {
		return err
	}
	execRes, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE slug = ?`, strings.TrimSpace(slug))
	if err != nil {
		return err
	}
	affected, _ := execRes.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func normalizeTaskBucket(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "done", "completed", "closed":
		return "done"
	case "doing", "in_progress", "in-progress", "active", "review":
		return "in_progress"
	case "blocked", "on_hold":
		return "blocked"
	default:
		return "todo"
	}
}
