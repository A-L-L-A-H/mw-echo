package blueprints

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxUpstreamComposeFetch = 100
	snifferConcurrency      = 6
)

type upstreamSourceSpec struct {
	Source string
	Repo   string
}

func upstreamSources() []upstreamSourceSpec {
	return []upstreamSourceSpec{
		{Source: "dokploy", Repo: "Dokploy/templates"},
		{Source: "coolify", Repo: "coollabsio/coolify"},
		{Source: "bigbear", Repo: "bigbeartechworld/big-bear-casaos"},
	}
}

// SniffAll queries all configured upstream blueprint repositories, upserts new or updated templates, and returns per-source results.
func (c *Catalog) SniffAll(ctx context.Context) ([]SniffResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	specs := upstreamSources()
	results := make([]SniffResult, len(specs))
	for idx, spec := range specs {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		results[idx] = c.sniffSource(ctx, spec)
	}
	return results, nil
}

func (c *Catalog) sniffSource(ctx context.Context, spec upstreamSourceSpec) SniffResult {
	prevSHA, prevCount := c.loadSourceState(ctx, spec.Source)
	result := SniffResult{
		Source:      spec.Source,
		Repo:        spec.Repo,
		PreviousSHA: prevSHA,
	}

	headSHA, err := c.fetchLatestCommitSHA(ctx, spec.Repo)
	if err != nil {
		result.Status = "error"
		result.ErrorMessage = err.Error()
		return result
	}
	result.SHA = headSHA

	if headSHA != "" && headSHA == prevSHA && prevCount > 0 {
		result.Skipped = true
		result.Status = "unchanged"
		c.recordSourceState(ctx, spec.Source, spec.Repo, headSHA, prevCount)
		return result
	}

	var parsed []Blueprint
	switch spec.Source {
	case "dokploy":
		parsed, err = c.sniffDokploy(ctx, spec.Repo, headSHA)
	case "coolify":
		parsed, err = c.sniffCoolify(ctx, spec.Repo, headSHA)
	case "bigbear":
		parsed, err = c.sniffBigBear(ctx, spec.Repo, headSHA)
	default:
		err = fmt.Errorf("unknown source %q", spec.Source)
	}

	if err != nil {
		result.Status = "error"
		result.ErrorMessage = err.Error()
		return result
	}

	upserted, err := c.upsertBatch(ctx, parsed)
	if err != nil {
		result.Status = "error"
		result.ErrorMessage = err.Error()
		return result
	}

	totalForSource := c.countBySource(ctx, spec.Source)
	c.recordSourceState(ctx, spec.Source, spec.Repo, headSHA, totalForSource)

	result.Upserted = upserted
	result.Status = "ok"
	return result
}

func (c *Catalog) loadSourceState(ctx context.Context, source string) (string, int) {
	var sha string
	var count int
	err := c.db.QueryRowContext(ctx, `SELECT last_sha, items_count FROM paas_blueprint_sources WHERE source = ?`, source).Scan(&sha, &count)
	if err != nil {
		return "", 0
	}
	return sha, count
}

func (c *Catalog) countBySource(ctx context.Context, source string) int {
	var total int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM paas_blueprints WHERE source = ?`, source).Scan(&total); err != nil {
		return 0
	}
	return total
}

func (c *Catalog) recordSourceState(ctx context.Context, source, repo, sha string, itemsCount int) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = c.db.ExecContext(ctx, `
INSERT INTO paas_blueprint_sources (source, repo, last_sha, last_sniff_at, items_count)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(source) DO UPDATE SET
  repo = excluded.repo,
  last_sha = excluded.last_sha,
  last_sniff_at = excluded.last_sniff_at,
  items_count = excluded.items_count
`, source, repo, sha, now, itemsCount)
}

func (c *Catalog) upsertBatch(ctx context.Context, list []Blueprint) (int, error) {
	if len(list) == 0 {
		return 0, nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339)
	count := 0
	for _, blueprint := range list {
		if strings.TrimSpace(blueprint.Slug) == "" {
			continue
		}
		blueprint.UpdatedAt = now
		if err := upsertBlueprintTx(ctx, tx, blueprint); err != nil {
			return 0, err
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func (c *Catalog) fetchLatestCommitSHA(ctx context.Context, repo string) (string, error) {
	listURL := fmt.Sprintf("%s/repos/%s/commits?per_page=1", c.githubAPIBase, repo)
	body, status, err := c.httpGet(ctx, listURL, true)
	if err == nil && status == http.StatusOK {
		var commits []struct {
			SHA string `json:"sha"`
		}
		if json.Unmarshal(body, &commits) == nil && len(commits) > 0 && strings.TrimSpace(commits[0].SHA) != "" {
			return strings.TrimSpace(commits[0].SHA), nil
		}
		var single struct {
			SHA string `json:"sha"`
		}
		if json.Unmarshal(body, &single) == nil && strings.TrimSpace(single.SHA) != "" {
			return strings.TrimSpace(single.SHA), nil
		}
	}

	for _, ref := range []string{"main", "master"} {
		refURL := fmt.Sprintf("%s/repos/%s/commits/%s", c.githubAPIBase, repo, ref)
		refBody, refStatus, refErr := c.httpGet(ctx, refURL, true)
		if refErr != nil {
			err = refErr
			continue
		}
		if refStatus != http.StatusOK {
			err = fmt.Errorf("github commits HTTP %d for %s", refStatus, repo)
			continue
		}
		var commit struct {
			SHA string `json:"sha"`
		}
		if jsonErr := json.Unmarshal(refBody, &commit); jsonErr == nil && strings.TrimSpace(commit.SHA) != "" {
			return strings.TrimSpace(commit.SHA), nil
		}
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("unable to resolve latest commit SHA for %s", repo)
}

type gitTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

func (c *Catalog) fetchRepoTree(ctx context.Context, repo, ref string) ([]gitTreeEntry, error) {
	if strings.TrimSpace(ref) == "" {
		ref = "main"
	}
	treeURL := fmt.Sprintf("%s/repos/%s/git/trees/%s?recursive=1", c.githubAPIBase, repo, ref)
	body, status, err := c.httpGet(ctx, treeURL, true)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("github tree HTTP %d for %s", status, repo)
	}
	var payload struct {
		Tree []gitTreeEntry `json:"tree"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode git tree for %s: %w", repo, err)
	}
	return payload.Tree, nil
}

type dokployMetaEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Category    string   `json:"category"`
}

func (c *Catalog) sniffDokploy(ctx context.Context, repo, sha string) ([]Blueprint, error) {
	treeEntries, err := c.fetchRepoTree(ctx, repo, sha)
	if err != nil {
		return nil, err
	}

	composePaths := make(map[string]string)
	hasMetaJSON := false
	for _, entry := range treeEntries {
		if entry.Type != "blob" {
			continue
		}
		if entry.Path == "meta.json" {
			hasMetaJSON = true
			continue
		}
		if strings.HasPrefix(entry.Path, "blueprints/") {
			base := path.Base(entry.Path)
			if base == "docker-compose.yml" || base == "docker-compose.yaml" || base == "compose.yml" {
				slug := path.Base(path.Dir(entry.Path))
				if slug != "" && slug != "blueprints" && slug != "." {
					composePaths[slug] = entry.Path
				}
			}
		}
	}

	metaBySlug := make(map[string]dokployMetaEntry)
	if hasMetaJSON {
		metaURL := fmt.Sprintf("%s/%s/%s/meta.json", c.rawBase, repo, sha)
		if metaText, fetchErr := c.fetchText(ctx, metaURL); fetchErr == nil {
			var entries []dokployMetaEntry
			if json.Unmarshal([]byte(metaText), &entries) == nil {
				for _, meta := range entries {
					key := strings.ToLower(strings.TrimSpace(meta.ID))
					if key == "" {
						key = slugify(meta.Name)
					}
					if key != "" {
						metaBySlug[key] = meta
					}
				}
			}
		}
	}

	slugs := make([]string, 0, len(composePaths))
	seen := make(map[string]struct{}, len(composePaths))
	for slug := range composePaths {
		lowerSlug := strings.ToLower(slug)
		seen[lowerSlug] = struct{}{}
		slugs = append(slugs, lowerSlug)
	}
	for slug := range metaBySlug {
		if _, ok := seen[slug]; !ok {
			slugs = append(slugs, slug)
		}
	}
	sort.Strings(slugs)

	fetchTargets := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		if relPath, ok := composePaths[slug]; ok && len(fetchTargets) < maxUpstreamComposeFetch {
			fetchTargets = append(fetchTargets, relPath)
		}
	}
	composeByPath := c.fetchRawBatch(ctx, repo, sha, fetchTargets)

	blueprints := make([]Blueprint, 0, len(slugs))
	for _, slug := range slugs {
		meta := metaBySlug[slug]
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			name = humanizeSlug(slug)
		}
		category := strings.ToLower(strings.TrimSpace(meta.Category))
		if category == "" {
			category = inferCategory(slug, meta.Tags)
		}
		version := strings.TrimSpace(meta.Version)
		if version == "" {
			version = "latest"
		}
		tags := cleanTags(meta.Tags, "dokploy", slug)

		relPath := composePaths[slug]
		composeYAML := composeByPath[relPath]
		sourceURL := fmt.Sprintf("https://github.com/%s", repo)
		if relPath != "" {
			sourceURL = fmt.Sprintf("%s/%s/%s/%s", c.rawBase, repo, sha, relPath)
		}
		if composeYAML == "" && relPath == "" {
			continue
		}

		blueprints = append(blueprints, Blueprint{
			ID:          makeBlueprintID("dokploy", slug),
			Source:      "dokploy",
			Slug:        slug,
			Name:        name,
			Category:    category,
			Description: strings.TrimSpace(meta.Description),
			Version:     version,
			ComposeYAML: composeYAML,
			Tags:        tags,
			SourceURL:   sourceURL,
		})
	}
	return blueprints, nil
}

type coolifyTemplate struct {
	Name          string   `json:"name"`
	Slogan        string   `json:"slogan"`
	Description   string   `json:"description"`
	Compose       string   `json:"compose"`
	Tags          []string `json:"tags"`
	Category      string   `json:"category"`
	Version       string   `json:"version"`
	Documentation string   `json:"documentation"`
}

func (c *Catalog) sniffCoolify(ctx context.Context, repo, sha string) ([]Blueprint, error) {
	templateURL := fmt.Sprintf("%s/%s/%s/templates/service-templates-latest.json", c.rawBase, repo, sha)
	rawBody, status, err := c.httpGet(ctx, templateURL, false)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("coolify templates HTTP %d", status)
	}

	templateMap := make(map[string]coolifyTemplate)
	if unmarshalErr := json.Unmarshal(rawBody, &templateMap); unmarshalErr != nil {
		var wrapped struct {
			Services map[string]coolifyTemplate `json:"services"`
		}
		if wrapErr := json.Unmarshal(rawBody, &wrapped); wrapErr == nil && len(wrapped.Services) > 0 {
			templateMap = wrapped.Services
		} else {
			return nil, fmt.Errorf("decode coolify templates: %w", unmarshalErr)
		}
	}

	keys := make([]string, 0, len(templateMap))
	for key := range templateMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	blueprints := make([]Blueprint, 0, len(keys))
	for _, rawKey := range keys {
		tmpl := templateMap[rawKey]
		slug := slugify(rawKey)
		if slug == "" {
			continue
		}
		composeYAML := decodeMaybeBase64YAML(tmpl.Compose)
		if strings.TrimSpace(composeYAML) == "" {
			continue
		}
		name := strings.TrimSpace(tmpl.Name)
		if name == "" {
			name = humanizeSlug(slug)
		}
		desc := strings.TrimSpace(tmpl.Slogan)
		if desc == "" {
			desc = strings.TrimSpace(tmpl.Description)
		}
		category := strings.ToLower(strings.TrimSpace(tmpl.Category))
		if category == "" {
			category = inferCategory(slug, tmpl.Tags)
		}
		version := strings.TrimSpace(tmpl.Version)
		if version == "" {
			version = "latest"
		}
		sourceURL := strings.TrimSpace(tmpl.Documentation)
		if sourceURL == "" {
			sourceURL = templateURL
		}

		blueprints = append(blueprints, Blueprint{
			ID:          makeBlueprintID("coolify", slug),
			Source:      "coolify",
			Slug:        slug,
			Name:        name,
			Category:    category,
			Description: desc,
			Version:     version,
			ComposeYAML: composeYAML,
			Tags:        cleanTags(tmpl.Tags, "coolify", slug),
			SourceURL:   sourceURL,
		})
	}
	return blueprints, nil
}

type bigBearManifestEntry struct {
	ID          string   `json:"id"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Tagline     string   `json:"tagline"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Version     string   `json:"version"`
	Compose     string   `json:"compose"`
	ComposeYAML string   `json:"composeYaml"`
	ComposePath string   `json:"composePath"`
	Tags        []string `json:"tags"`
}

func (c *Catalog) sniffBigBear(ctx context.Context, repo, sha string) ([]Blueprint, error) {
	// Prefer JSON manifest if present, and also inspect git tree for Apps/<slug>/docker-compose.yml.
	manifestCandidates := []string{"apps-list.json", "app-list.json", "apps.json"}
	var manifestEntries []bigBearManifestEntry

	treeEntries, treeErr := c.fetchRepoTree(ctx, repo, sha)
	composePaths := make(map[string]string)
	if treeErr == nil {
		for _, entry := range treeEntries {
			if entry.Type != "blob" {
				continue
			}
			for _, candidate := range manifestCandidates {
				if strings.EqualFold(entry.Path, candidate) {
					manifestText, fetchErr := c.fetchText(ctx, fmt.Sprintf("%s/%s/%s/%s", c.rawBase, repo, sha, entry.Path))
					if fetchErr == nil {
						manifestEntries = parseBigBearManifest([]byte(manifestText))
					}
				}
			}
			lowerPath := strings.ToLower(entry.Path)
			if strings.HasPrefix(lowerPath, "apps/") && (strings.HasSuffix(lowerPath, "/docker-compose.yml") || strings.HasSuffix(lowerPath, "/docker-compose.yaml")) {
				slug := slugify(path.Base(path.Dir(entry.Path)))
				if slug != "" {
					composePaths[slug] = entry.Path
				}
			}
		}
	}

	if len(manifestEntries) == 0 && len(composePaths) == 0 {
		// Direct fallback to apps-list.json if the server only serves raw manifests without git tree.
		for _, candidate := range manifestCandidates {
			rawURL := fmt.Sprintf("%s/%s/%s/%s", c.rawBase, repo, sha, candidate)
			rawBody, status, httpErr := c.httpGet(ctx, rawURL, false)
			if httpErr == nil && status == http.StatusOK {
				manifestEntries = parseBigBearManifest(rawBody)
				if len(manifestEntries) > 0 {
					break
				}
			}
		}
		if len(manifestEntries) == 0 {
			if treeErr != nil {
				return nil, treeErr
			}
			return nil, fmt.Errorf("no blueprints found in %s", repo)
		}
	}

	manifestBySlug := make(map[string]bigBearManifestEntry, len(manifestEntries))
	for _, entry := range manifestEntries {
		slug := slugify(entry.Slug)
		if slug == "" {
			slug = slugify(entry.ID)
		}
		if slug == "" {
			slug = slugify(entry.Name)
		}
		if slug == "" {
			slug = slugify(entry.Title)
		}
		if slug != "" {
			manifestBySlug[slug] = entry
			if strings.TrimSpace(entry.ComposePath) != "" {
				composePaths[slug] = strings.TrimLeft(strings.TrimSpace(entry.ComposePath), "/")
			}
		}
	}

	slugSet := make(map[string]struct{})
	for slug := range composePaths {
		slugSet[slug] = struct{}{}
	}
	for slug := range manifestBySlug {
		slugSet[slug] = struct{}{}
	}

	slugs := make([]string, 0, len(slugSet))
	for slug := range slugSet {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	fetchTargets := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		entry := manifestBySlug[slug]
		inlineCompose := firstNonEmpty(entry.ComposeYAML, entry.Compose)
		if strings.TrimSpace(inlineCompose) != "" {
			continue
		}
		if relPath, ok := composePaths[slug]; ok && len(fetchTargets) < maxUpstreamComposeFetch {
			fetchTargets = append(fetchTargets, relPath)
		}
	}
	composeByPath := c.fetchRawBatch(ctx, repo, sha, fetchTargets)

	blueprints := make([]Blueprint, 0, len(slugs))
	for _, slug := range slugs {
		entry := manifestBySlug[slug]
		composeYAML := decodeMaybeBase64YAML(firstNonEmpty(entry.ComposeYAML, entry.Compose))
		relPath := composePaths[slug]
		if strings.TrimSpace(composeYAML) == "" && relPath != "" {
			composeYAML = composeByPath[relPath]
		}
		if strings.TrimSpace(composeYAML) == "" && relPath == "" {
			continue
		}

		name := firstNonEmpty(entry.Title, entry.Name, humanizeSlug(slug))
		desc := firstNonEmpty(entry.Tagline, entry.Description)
		category := strings.ToLower(strings.TrimSpace(entry.Category))
		if category == "" {
			category = inferCategory(slug, entry.Tags)
		}
		version := strings.TrimSpace(entry.Version)
		if version == "" {
			version = "latest"
		}
		sourceURL := fmt.Sprintf("https://github.com/%s", repo)
		if relPath != "" {
			sourceURL = fmt.Sprintf("%s/%s/%s/%s", c.rawBase, repo, sha, relPath)
		}

		blueprints = append(blueprints, Blueprint{
			ID:          makeBlueprintID("bigbear", slug),
			Source:      "bigbear",
			Slug:        slug,
			Name:        name,
			Category:    category,
			Description: desc,
			Version:     version,
			ComposeYAML: composeYAML,
			Tags:        cleanTags(entry.Tags, "bigbear", "casaos", slug),
			SourceURL:   sourceURL,
		})
	}
	return blueprints, nil
}

func parseBigBearManifest(rawBody []byte) []bigBearManifestEntry {
	var list []bigBearManifestEntry
	if json.Unmarshal(rawBody, &list) == nil && len(list) > 0 {
		return list
	}
	var wrapped struct {
		Apps []bigBearManifestEntry `json:"apps"`
	}
	if json.Unmarshal(rawBody, &wrapped) == nil && len(wrapped.Apps) > 0 {
		return wrapped.Apps
	}
	var mapped map[string]bigBearManifestEntry
	if json.Unmarshal(rawBody, &mapped) == nil && len(mapped) > 0 {
		out := make([]bigBearManifestEntry, 0, len(mapped))
		for key, entry := range mapped {
			if entry.Slug == "" {
				entry.Slug = key
			}
			out = append(out, entry)
		}
		return out
	}
	return nil
}

func (c *Catalog) fetchRawBatch(ctx context.Context, repo, sha string, relPaths []string) map[string]string {
	fetched := make(map[string]string, len(relPaths))
	if len(relPaths) == 0 {
		return fetched
	}

	var mu sync.Mutex
	sem := make(chan struct{}, snifferConcurrency)
	var wg sync.WaitGroup

	for _, relPath := range relPaths {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(targetPath string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			rawURL := fmt.Sprintf("%s/%s/%s/%s", c.rawBase, repo, sha, targetPath)
			content, err := c.fetchText(ctx, rawURL)
			if err == nil && strings.TrimSpace(content) != "" {
				mu.Lock()
				fetched[targetPath] = content
				mu.Unlock()
			}
		}(relPath)
	}
	wg.Wait()
	return fetched
}

func (c *Catalog) fetchText(ctx context.Context, targetURL string) (string, error) {
	body, status, err := c.httpGet(ctx, targetURL, false)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("HTTP %d fetching %s", status, targetURL)
	}
	return string(body), nil
}

func (c *Catalog) httpGet(ctx context.Context, targetURL string, isGitHubAPI bool) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "mw-echo-blueprints-sniffer/1.0")
	if isGitHubAPI {
		req.Header.Set("Accept", "application/vnd.github+json")
		if c.githubToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.githubToken)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, 16<<20)
	body, readErr := io.ReadAll(limited)
	if readErr != nil {
		return nil, resp.StatusCode, readErr
	}
	return body, resp.StatusCode, nil
}

func decodeMaybeBase64YAML(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "\n") || strings.Contains(trimmed, "services:") {
		return raw
	}
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(trimmed)
		if err != nil {
			return raw
		}
	}
	text := string(decoded)
	if strings.Contains(text, "services:") || strings.Contains(text, "image:") || strings.Contains(text, "\n") {
		return text
	}
	return raw
}

func slugify(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return ""
	}
	var builder strings.Builder
	prevDash := false
	for _, ch := range lower {
		isAlnum := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
		if isAlnum {
			builder.WriteRune(ch)
			prevDash = false
		} else if !prevDash && builder.Len() > 0 {
			builder.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func humanizeSlug(slug string) string {
	parts := strings.Split(slug, "-")
	for idx, part := range parts {
		if len(part) > 0 {
			parts[idx] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func cleanTags(existing []string, extra ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(extra))
	tags := make([]string, 0, len(existing)+len(extra))
	for _, list := range [][]string{existing, extra} {
		for _, tag := range list {
			clean := strings.ToLower(strings.TrimSpace(tag))
			if clean == "" {
				continue
			}
			if _, ok := seen[clean]; !ok {
				seen[clean] = struct{}{}
				tags = append(tags, clean)
			}
		}
	}
	return tags
}

func inferCategory(slug string, tags []string) string {
	combined := strings.ToLower(slug + " " + strings.Join(tags, " "))
	switch {
	case strings.Contains(combined, "postgres") || strings.Contains(combined, "mysql") || strings.Contains(combined, "mongo") || strings.Contains(combined, "mariadb"):
		return "database"
	case strings.Contains(combined, "redis") || strings.Contains(combined, "valkey") || strings.Contains(combined, "memcached"):
		return "cache"
	case strings.Contains(combined, "s3") || strings.Contains(combined, "minio") || strings.Contains(combined, "garage") || strings.Contains(combined, "storage"):
		return "storage"
	case strings.Contains(combined, "grafana") || strings.Contains(combined, "prometheus") || strings.Contains(combined, "uptime") || strings.Contains(combined, "monitor"):
		return "monitoring"
	case strings.Contains(combined, "ollama") || strings.Contains(combined, "qdrant") || strings.Contains(combined, "llm") || strings.Contains(combined, "ai"):
		return "ai"
	case strings.Contains(combined, "n8n") || strings.Contains(combined, "automation") || strings.Contains(combined, "workflow"):
		return "automation"
	default:
		return "apps"
	}
}

func firstNonEmpty(candidates ...string) string {
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
