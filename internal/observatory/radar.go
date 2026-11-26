package observatory

import (
	"bufio"
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// DiscoveredRepo describes a Git repository found on the local host.
type DiscoveredRepo struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Branch       string `json:"branch"`
	RemoteURL    string `json:"remoteUrl,omitempty"`
	HasDocs      bool   `json:"hasDocs"`
	HasTasksJSON bool   `json:"hasTasksJson"`
	Imported     bool   `json:"imported"`
	ModifiedAt   string `json:"modifiedAt"`
}

func DefaultRadarRoots() []string {
	candidates := []string{`Z:\repo`}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, "source"),
			filepath.Join(home, "repos"),
			filepath.Join(home, "projects"),
			filepath.Join(home, "code"),
			filepath.Join(home, "dev"),
		)
	}
	var existing []string
	seen := map[string]bool{}
	for _, dir := range candidates {
		clean := filepath.Clean(dir)
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		if info, err := os.Stat(clean); err == nil && info.IsDir() {
			seen[key] = true
			existing = append(existing, clean)
		}
	}
	return existing
}

// ScanRepos walks root directories (up to depth 4) to discover local Git repositories.
func (o *Observatory) ScanRepos(ctx context.Context, roots []string) ([]DiscoveredRepo, error) {
	if len(roots) == 0 {
		roots = DefaultRadarRoots()
	}

	importedPaths := map[string]bool{}
	importedNames := map[string]bool{}
	if o != nil && o.hub != nil && o.hub.DB() != nil {
		rows, err := o.hub.DB().QueryContext(ctx, `SELECT slug, repo_path FROM projects`)
		if err == nil {
			for rows.Next() {
				var slug, repoPath string
				if rows.Scan(&slug, &repoPath) == nil {
					importedNames[strings.ToLower(strings.TrimSpace(slug))] = true
					if strings.TrimSpace(repoPath) != "" {
						importedPaths[strings.ToLower(filepath.Clean(repoPath))] = true
					}
				}
			}
			_ = rows.Close()
		}
	}

	skipDirs := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		"target":       true,
		"dist":         true,
		"build":        true,
		".next":        true,
		".turbo":       true,
		".cache":       true,
		"blobs":        true,
	}

	seenRepos := map[string]bool{}
	repos := make([]DiscoveredRepo, 0)

	for _, root := range roots {
		cleanRoot := filepath.Clean(strings.TrimSpace(root))
		if cleanRoot == "" {
			continue
		}
		info, err := os.Stat(cleanRoot)
		if err != nil || !info.IsDir() {
			continue
		}
		baseDepth := strings.Count(cleanRoot, string(os.PathSeparator))

		_ = filepath.WalkDir(cleanRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || ctx.Err() != nil {
				return nil
			}
			if !entry.IsDir() {
				return nil
			}
			name := entry.Name()
			if skipDirs[strings.ToLower(name)] {
				return filepath.SkipDir
			}
			if name == ".git" {
				repoDir := filepath.Dir(path)
				repoKey := strings.ToLower(filepath.Clean(repoDir))
				if !seenRepos[repoKey] {
					seenRepos[repoKey] = true
					repo := inspectGitRepo(repoDir, importedPaths, importedNames)
					repos = append(repos, repo)
				}
				return filepath.SkipDir
			}
			depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - baseDepth
			if depth >= 4 {
				return filepath.SkipDir
			}
			return nil
		})
	}

	sort.Slice(repos, func(i, j int) bool {
		return strings.ToLower(repos[i].Name) < strings.ToLower(repos[j].Name)
	})
	return repos, nil
}

func inspectGitRepo(repoDir string, importedPaths, importedNames map[string]bool) DiscoveredRepo {
	name := filepath.Base(repoDir)
	branch := readGitBranch(filepath.Join(repoDir, ".git", "HEAD"))
	remote := readGitOriginURL(filepath.Join(repoDir, ".git", "config"))
	hasDocs := dirExists(filepath.Join(repoDir, "docs")) || fileExists(filepath.Join(repoDir, "README.md"))
	hasTasks := fileExists(filepath.Join(repoDir, "docs", "tasks", "tasks.json")) || fileExists(filepath.Join(repoDir, "tasks.json"))

	modTime := ""
	if info, err := os.Stat(repoDir); err == nil {
		modTime = info.ModTime().UTC().Format(time.RFC3339)
	}

	cleanKey := strings.ToLower(filepath.Clean(repoDir))
	imported := importedPaths[cleanKey] || importedNames[strings.ToLower(name)] || (name == "mw-xv" && importedNames["moonwitness"])

	return DiscoveredRepo{
		Name:         name,
		Path:         repoDir,
		Branch:       branch,
		RemoteURL:    remote,
		HasDocs:      hasDocs,
		HasTasksJSON: hasTasks,
		Imported:     imported,
		ModifiedAt:   modTime,
	}
}

func readGitBranch(headPath string) string {
	raw, err := os.ReadFile(headPath)
	if err != nil {
		return "unknown"
	}
	trimmed := strings.TrimSpace(string(raw))
	const prefix = "ref: refs/heads/"
	if strings.HasPrefix(trimmed, prefix) {
		return strings.TrimPrefix(trimmed, prefix)
	}
	if len(trimmed) > 8 {
		return trimmed[:8]
	}
	return trimmed
}

func readGitOriginURL(cfgPath string) string {
	f, err := os.Open(cfgPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	inOrigin := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.EqualFold(line, `[remote "origin"]`)
			continue
		}
		if inOrigin && strings.HasPrefix(line, "url") {
			if _, val, ok := strings.Cut(line, "="); ok {
				return strings.TrimSpace(val)
			}
		}
	}
	return ""
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (o *Observatory) handleListRepos(c echo.Context) error {
	roots := DefaultRadarRoots()
	repos, err := o.ScanRepos(c.Request().Context(), roots)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"repos":     repos,
		"count":     len(repos),
		"roots":     roots,
		"scannedAt": time.Now().UTC().Format(time.RFC3339),
	})
}

func (o *Observatory) handleScanRepos(c echo.Context) error {
	var body struct {
		Roots []string `json:"roots"`
	}
	_ = c.Bind(&body)
	roots := body.Roots
	if len(roots) == 0 {
		roots = DefaultRadarRoots()
	}
	repos, err := o.ScanRepos(c.Request().Context(), roots)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"repos":     repos,
		"count":     len(repos),
		"roots":     roots,
		"scannedAt": time.Now().UTC().Format(time.RFC3339),
	})
}
