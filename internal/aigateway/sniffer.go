package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type upstreamProviderMeta struct {
	Slug    string
	Name    string
	BaseURL string
}

var (
	openAICompatBlockRegex = regexp.MustCompile(`(?s)new\s+OpenAICompatProvider\s*\(\s*\{(.*?)\}\s*\)`)
	platformFieldRegex     = regexp.MustCompile(`platform\s*:\s*['"]([a-zA-Z0-9_-]+)['"]`)
	nameFieldRegex         = regexp.MustCompile(`name\s*:\s*['"]([^'"]+)['"]`)
	baseURLFieldRegex      = regexp.MustCompile(`baseUrl\s*:\s*['"](https?://[^'"]+)['"]`)
	modelPricingRowRegex   = regexp.MustCompile(`\[\s*['"]([a-zA-Z0-9_-]+)['"]\s*,\s*['"]([^'"]+)['"]\s*,`)
)

func knownProviderDirectory() map[string]upstreamProviderMeta {
	return map[string]upstreamProviderMeta{
		"groq":          {Slug: "groq", Name: "Groq", BaseURL: "https://api.groq.com/openai/v1"},
		"cerebras":      {Slug: "cerebras", Name: "Cerebras", BaseURL: "https://api.cerebras.ai/v1"},
		"openrouter":    {Slug: "openrouter", Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1"},
		"google":        {Slug: "gemini", Name: "Google Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
		"gemini":        {Slug: "gemini", Name: "Google Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
		"mistral":       {Slug: "mistral", Name: "Mistral", BaseURL: "https://api.mistral.ai/v1"},
		"nvidia":        {Slug: "nvidia", Name: "NVIDIA NIM", BaseURL: "https://integrate.api.nvidia.com/v1"},
		"sambanova":     {Slug: "sambanova", Name: "SambaNova", BaseURL: "https://api.sambanova.ai/v1"},
		"github-models": {Slug: "github-models", Name: "GitHub Models", BaseURL: "https://models.inference.ai.azure.com"},
		"deepseek":      {Slug: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1"},
		"huggingface":   {Slug: "huggingface", Name: "HuggingFace Router", BaseURL: "https://router.huggingface.co/v1"},
		"cloudflare":    {Slug: "cloudflare", Name: "Cloudflare Workers AI", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1"},
		"cohere":        {Slug: "cohere", Name: "Cohere", BaseURL: "https://api.cohere.ai/compatibility/v1"},
		"zhipu":         {Slug: "zhipu", Name: "Zhipu AI", BaseURL: "https://open.bigmodel.cn/api/paas/v4"},
		"ollama":        {Slug: "ollama", Name: "Ollama Cloud", BaseURL: "https://ollama.com/v1"},
		"kilo":          {Slug: "kilo", Name: "Kilo Gateway", BaseURL: "https://api.kilo.ai/api/gateway/v1"},
		"pollinations":  {Slug: "pollinations", Name: "Pollinations", BaseURL: "https://gen.pollinations.ai/v1"},
		"reka":          {Slug: "reka", Name: "Reka", BaseURL: "https://api.reka.ai/v1"},
		"anyapi":        {Slug: "anyapi", Name: "AnyAPI", BaseURL: "https://api.anyapi.ai/v1"},
		"powertokens":   {Slug: "powertokens", Name: "PowerTokens", BaseURL: "https://api.powertokens.ai/v1"},
		"cheapllm":      {Slug: "cheapllm", Name: "cheapllm.shop", BaseURL: "https://cheapllm.shop/v1"},
		"tokenreply":    {Slug: "tokenreply", Name: "TokenReply", BaseURL: "https://api.tokenreply.com/v1"},
		"radeon":        {Slug: "radeon", Name: "AMD Radeon Cloud", BaseURL: "https://developer.amd.com.cn/radeon/api/v1"},
		"longcat":       {Slug: "longcat", Name: "LongCat", BaseURL: "https://api.longcat.chat/openai/v1"},
	}
}

func curatedFreeCatalog() []CatalogEntry {
	return []CatalogEntry{
		// Groq
		{ProviderSlug: "groq", ProviderName: "Groq", BaseURL: "https://api.groq.com/openai/v1", ModelID: "llama-3.3-70b-versatile", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "groq", ProviderName: "Groq", BaseURL: "https://api.groq.com/openai/v1", ModelID: "llama-3.1-8b-instant", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "groq", ProviderName: "Groq", BaseURL: "https://api.groq.com/openai/v1", ModelID: "meta-llama/llama-4-scout-17b-16e-instruct", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "groq", ProviderName: "Groq", BaseURL: "https://api.groq.com/openai/v1", ModelID: "openai/gpt-oss-120b", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "groq", ProviderName: "Groq", BaseURL: "https://api.groq.com/openai/v1", ModelID: "qwen/qwen3-32b", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		// Cerebras
		{ProviderSlug: "cerebras", ProviderName: "Cerebras", BaseURL: "https://api.cerebras.ai/v1", ModelID: "llama3.1-8b", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "cerebras", ProviderName: "Cerebras", BaseURL: "https://api.cerebras.ai/v1", ModelID: "gpt-oss-120b", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "cerebras", ProviderName: "Cerebras", BaseURL: "https://api.cerebras.ai/v1", ModelID: "qwen-3-235b-a22b-instruct-2507", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "cerebras", ProviderName: "Cerebras", BaseURL: "https://api.cerebras.ai/v1", ModelID: "zai-glm-4.7", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		// OpenRouter :free
		{ProviderSlug: "openrouter", ProviderName: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", ModelID: "meta-llama/llama-3.3-70b-instruct:free", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "openrouter", ProviderName: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", ModelID: "qwen/qwen3-coder:free", ContextWindow: 262144, FreeTier: true, Source: "seed"},
		{ProviderSlug: "openrouter", ProviderName: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", ModelID: "google/gemma-4-31b-it:free", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "openrouter", ProviderName: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", ModelID: "openai/gpt-oss-120b:free", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "openrouter", ProviderName: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", ModelID: "moonshotai/kimi-k2.6:free", ContextWindow: 262144, FreeTier: true, Source: "seed"},
		{ProviderSlug: "openrouter", ProviderName: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", ModelID: "nvidia/nemotron-3-super-120b-a12b:free", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		// Google Gemini OpenAI-compat
		{ProviderSlug: "gemini", ProviderName: "Google Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", ModelID: "gemini-2.5-flash", ContextWindow: 1048576, FreeTier: true, Source: "seed"},
		{ProviderSlug: "gemini", ProviderName: "Google Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", ModelID: "gemini-2.5-flash-lite", ContextWindow: 1048576, FreeTier: true, Source: "seed"},
		{ProviderSlug: "gemini", ProviderName: "Google Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", ModelID: "gemini-2.5-pro", ContextWindow: 1048576, FreeTier: true, Source: "seed"},
		{ProviderSlug: "gemini", ProviderName: "Google Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", ModelID: "gemma-4-31b-it", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		// Mistral
		{ProviderSlug: "mistral", ProviderName: "Mistral", BaseURL: "https://api.mistral.ai/v1", ModelID: "mistral-small-latest", ContextWindow: 128000, FreeTier: true, Source: "seed"},
		{ProviderSlug: "mistral", ProviderName: "Mistral", BaseURL: "https://api.mistral.ai/v1", ModelID: "codestral-latest", ContextWindow: 256000, FreeTier: true, Source: "seed"},
		{ProviderSlug: "mistral", ProviderName: "Mistral", BaseURL: "https://api.mistral.ai/v1", ModelID: "open-mistral-nemo", ContextWindow: 128000, FreeTier: true, Source: "seed"},
		// NVIDIA NIM
		{ProviderSlug: "nvidia", ProviderName: "NVIDIA NIM", BaseURL: "https://integrate.api.nvidia.com/v1", ModelID: "meta/llama-3.3-70b-instruct", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "nvidia", ProviderName: "NVIDIA NIM", BaseURL: "https://integrate.api.nvidia.com/v1", ModelID: "deepseek-ai/deepseek-r1", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "nvidia", ProviderName: "NVIDIA NIM", BaseURL: "https://integrate.api.nvidia.com/v1", ModelID: "qwen/qwen2.5-coder-32b-instruct", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		// SambaNova
		{ProviderSlug: "sambanova", ProviderName: "SambaNova", BaseURL: "https://api.sambanova.ai/v1", ModelID: "Meta-Llama-3.3-70B-Instruct", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "sambanova", ProviderName: "SambaNova", BaseURL: "https://api.sambanova.ai/v1", ModelID: "DeepSeek-R1-Distill-Llama-70B", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "sambanova", ProviderName: "SambaNova", BaseURL: "https://api.sambanova.ai/v1", ModelID: "Qwen2.5-Coder-32B-Instruct", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		// GitHub Models
		{ProviderSlug: "github-models", ProviderName: "GitHub Models", BaseURL: "https://models.inference.ai.azure.com", ModelID: "gpt-4o-mini", ContextWindow: 128000, FreeTier: true, Source: "seed"},
		{ProviderSlug: "github-models", ProviderName: "GitHub Models", BaseURL: "https://models.inference.ai.azure.com", ModelID: "Llama-3.3-70B-Instruct", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "github-models", ProviderName: "GitHub Models", BaseURL: "https://models.inference.ai.azure.com", ModelID: "DeepSeek-R1", ContextWindow: 128000, FreeTier: true, Source: "seed"},
		// DeepSeek
		{ProviderSlug: "deepseek", ProviderName: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", ModelID: "deepseek-chat", ContextWindow: 128000, FreeTier: false, Source: "seed"},
		{ProviderSlug: "deepseek", ProviderName: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", ModelID: "deepseek-reasoner", ContextWindow: 128000, FreeTier: false, Source: "seed"},
		// HuggingFace Router
		{ProviderSlug: "huggingface", ProviderName: "HuggingFace Router", BaseURL: "https://router.huggingface.co/v1", ModelID: "Qwen/Qwen3-Coder-Next", ContextWindow: 262144, FreeTier: true, Source: "seed"},
		{ProviderSlug: "huggingface", ProviderName: "HuggingFace Router", BaseURL: "https://router.huggingface.co/v1", ModelID: "deepseek-ai/DeepSeek-V4-Flash", ContextWindow: 131072, FreeTier: true, Source: "seed"},
		{ProviderSlug: "huggingface", ProviderName: "HuggingFace Router", BaseURL: "https://router.huggingface.co/v1", ModelID: "moonshotai/Kimi-K2.6", ContextWindow: 262144, FreeTier: true, Source: "seed"},
	}
}

func (g *Gateway) seedInitialCatalog(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, entry := range curatedFreeCatalog() {
		entryID := makeGatewayID("cat", entry.ProviderSlug, entry.ModelID)
		_, execErr := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO ai_catalog (id, provider_slug, provider_name, base_url, model_id, context_window, free_tier, source, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			entryID, entry.ProviderSlug, entry.ProviderName, entry.BaseURL, entry.ModelID,
			entry.ContextWindow, boolToInt(entry.FreeTier), entry.Source, now,
		)
		if execErr != nil {
			return execErr
		}
	}
	return tx.Commit()
}

// SniffUpstream checks the configured upstream FreeLLMAPI repository commit SHA,
// fetches live provider definitions (server/src/providers/index.ts) and model
// catalog entries (server/src/db/model-pricing.ts, with JSON fallback), merges
// with the curated free-tier seed catalog, and upserts all rows into ai_catalog.
func (g *Gateway) SniffUpstream(ctx context.Context) (SniffResult, error) {
	settings := g.GetSettings(ctx)
	upstreamRepo := strings.TrimSpace(settings.UpstreamRepo)
	if upstreamRepo == "" {
		upstreamRepo = DefaultUpstreamRepo
	}
	previousSHA := settings.UpstreamSHA

	latestSHA, shaErr := g.fetchUpstreamCommitSHA(ctx, upstreamRepo)

	providerDir := knownProviderDirectory()
	discoveredProviders := make(map[string]upstreamProviderMeta)
	upstreamEntries := make([]CatalogEntry, 0)

	providersBody, providersErr := g.fetchUpstreamRaw(ctx, upstreamRepo, "server/src/providers/index.ts")
	if providersErr == nil && len(providersBody) > 0 {
		for _, parsed := range parseProvidersTS(string(providersBody)) {
			providerDir[parsed.Slug] = parsed
			discoveredProviders[parsed.Slug] = parsed
		}
	}

	pricingBody, pricingErr := g.fetchUpstreamRaw(ctx, upstreamRepo, "server/src/db/model-pricing.ts")
	if pricingErr == nil && len(pricingBody) > 0 {
		parsedEntries := parseModelPricingTS(string(pricingBody), providerDir)
		upstreamEntries = append(upstreamEntries, parsedEntries...)
	}

	// Support structured JSON catalog if upstream exposes one (or in custom repos).
	if len(upstreamEntries) == 0 && len(discoveredProviders) == 0 {
		jsonBody, jsonErr := g.fetchUpstreamRaw(ctx, upstreamRepo, "catalog.json")
		if jsonErr == nil && len(jsonBody) > 0 {
			upstreamEntries = append(upstreamEntries, parseCatalogJSON(jsonBody, providerDir)...)
		}
	}

	if shaErr != nil && len(upstreamEntries) == 0 && len(discoveredProviders) == 0 {
		return SniffResult{
			Updated:     false,
			PreviousSHA: previousSHA,
		}, fmt.Errorf("sniff upstream %s failed: %w", upstreamRepo, shaErr)
	}

	// Merge curated seed + upstream discovered models (upstream wins on key collisions).
	mergedByKey := make(map[string]CatalogEntry)
	for _, seedEntry := range curatedFreeCatalog() {
		key := seedEntry.ProviderSlug + ":" + seedEntry.ModelID
		mergedByKey[key] = seedEntry
	}

	providersWithModels := make(map[string]bool)
	for _, upstreamEntry := range upstreamEntries {
		key := upstreamEntry.ProviderSlug + ":" + upstreamEntry.ModelID
		mergedByKey[key] = upstreamEntry
		providersWithModels[upstreamEntry.ProviderSlug] = true
	}

	// Ensure newly discovered upstream providers without explicit model rows are still indexed.
	for slug, meta := range discoveredProviders {
		if providersWithModels[slug] {
			continue
		}
		hasSeed := false
		for _, existing := range mergedByKey {
			if existing.ProviderSlug == slug {
				hasSeed = true
				break
			}
		}
		if !hasSeed && meta.BaseURL != "" {
			autoEntry := CatalogEntry{
				ProviderSlug:  meta.Slug,
				ProviderName:  meta.Name,
				BaseURL:       meta.BaseURL,
				ModelID:       "auto",
				ContextWindow: 128000,
				FreeTier:      true,
				Source:        "freellmapi",
			}
			mergedByKey[meta.Slug+":auto"] = autoEntry
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	uniqueProviders := make(map[string]bool)

	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return SniffResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, entry := range mergedByKey {
		entryID := makeGatewayID("cat", entry.ProviderSlug, entry.ModelID)
		if entry.ContextWindow <= 0 {
			entry.ContextWindow = inferContextWindow(entry.ModelID)
		}
		uniqueProviders[entry.ProviderSlug] = true

		_, execErr := tx.ExecContext(ctx, `
INSERT INTO ai_catalog (id, provider_slug, provider_name, base_url, model_id, context_window, free_tier, source, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  provider_slug = excluded.provider_slug,
  provider_name = excluded.provider_name,
  base_url = excluded.base_url,
  model_id = excluded.model_id,
  context_window = excluded.context_window,
  free_tier = excluded.free_tier,
  source = excluded.source,
  updated_at = excluded.updated_at`,
			entryID, entry.ProviderSlug, entry.ProviderName, entry.BaseURL, entry.ModelID,
			entry.ContextWindow, boolToInt(entry.FreeTier), entry.Source, now,
		)
		if execErr != nil {
			return SniffResult{}, execErr
		}
	}

	if latestSHA != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_settings (key, value) VALUES ('upstream_sha', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, latestSHA); err != nil {
			return SniffResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_settings (key, value) VALUES ('last_sniff_at', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, now); err != nil {
		return SniffResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return SniffResult{}, err
	}

	updated := (latestSHA != "" && latestSHA != previousSHA) || (previousSHA == "" && len(upstreamEntries) > 0)
	return SniffResult{
		Updated:        updated,
		UpstreamSHA:    latestSHA,
		PreviousSHA:    previousSHA,
		ProvidersFound: len(uniqueProviders),
		ModelsIndexed:  len(mergedByKey),
		LastSniffAt:    now,
	}, nil
}

func (g *Gateway) fetchUpstreamCommitSHA(ctx context.Context, repo string) (string, error) {
	apiBase := strings.TrimRight(g.GitHubAPIBase, "/")
	if apiBase == "" {
		apiBase = DefaultGitHubAPIBase
	}

	candidatePaths := []string{
		fmt.Sprintf("%s/repos/%s/commits/main", apiBase, repo),
		fmt.Sprintf("%s/repos/%s/commits/HEAD", apiBase, repo),
	}

	var lastErr error
	for _, endpoint := range candidatePaths {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		httpReq.Header.Set("Accept", "application/vnd.github+json")
		httpReq.Header.Set("User-Agent", "mw-echo-aigateway")

		httpResp, err := g.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		bodyBytes, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 512*1024))
		_ = httpResp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			lastErr = fmt.Errorf("github api %s returned %d", endpoint, httpResp.StatusCode)
			continue
		}

		var commitEnvelope struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(bodyBytes, &commitEnvelope); err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(commitEnvelope.SHA) != "" {
			return strings.TrimSpace(commitEnvelope.SHA), nil
		}
		lastErr = errors.New("empty commit sha in github response")
	}
	return "", lastErr
}

func (g *Gateway) fetchUpstreamRaw(ctx context.Context, repo, relPath string) ([]byte, error) {
	rawBase := strings.TrimRight(g.RawBase, "/")
	if rawBase == "" {
		rawBase = DefaultRawBase
	}
	targetURL := fmt.Sprintf("%s/%s/main/%s", rawBase, repo, strings.TrimLeft(relPath, "/"))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", "mw-echo-aigateway")

	httpResp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("raw fetch %s returned %d", targetURL, httpResp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(httpResp.Body, 4*1024*1024))
}

func parseProvidersTS(sourceText string) []upstreamProviderMeta {
	matches := openAICompatBlockRegex.FindAllStringSubmatch(sourceText, -1)
	providers := make([]upstreamProviderMeta, 0, len(matches))
	seen := make(map[string]bool)

	for _, blockMatch := range matches {
		if len(blockMatch) < 2 {
			continue
		}
		body := blockMatch[1]
		platformMatch := platformFieldRegex.FindStringSubmatch(body)
		urlMatch := baseURLFieldRegex.FindStringSubmatch(body)
		if len(platformMatch) < 2 || len(urlMatch) < 2 {
			continue
		}
		slug := strings.ToLower(strings.TrimSpace(platformMatch[1]))
		if slug == "" || slug == "custom" || seen[slug] {
			continue
		}
		seen[slug] = true

		displayName := slug
		if nameMatch := nameFieldRegex.FindStringSubmatch(body); len(nameMatch) >= 2 {
			displayName = strings.TrimSpace(nameMatch[1])
		}
		providers = append(providers, upstreamProviderMeta{
			Slug:    slug,
			Name:    displayName,
			BaseURL: strings.TrimSpace(urlMatch[1]),
		})
	}
	return providers
}

func parseModelPricingTS(sourceText string, providerDir map[string]upstreamProviderMeta) []CatalogEntry {
	matches := modelPricingRowRegex.FindAllStringSubmatch(sourceText, -1)
	entries := make([]CatalogEntry, 0, len(matches))
	seen := make(map[string]bool)

	for _, rowMatch := range matches {
		if len(rowMatch) < 3 {
			continue
		}
		rawPlatform := strings.ToLower(strings.TrimSpace(rowMatch[1]))
		modelID := strings.TrimSpace(rowMatch[2])
		if rawPlatform == "" || modelID == "" {
			continue
		}

		meta, ok := providerDir[rawPlatform]
		if !ok {
			meta = upstreamProviderMeta{
				Slug:    rawPlatform,
				Name:    strings.ToUpper(rawPlatform[:1]) + rawPlatform[1:],
				BaseURL: "",
			}
		}

		dedupKey := meta.Slug + ":" + modelID
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true

		entries = append(entries, CatalogEntry{
			ProviderSlug:  meta.Slug,
			ProviderName:  meta.Name,
			BaseURL:       meta.BaseURL,
			ModelID:       modelID,
			ContextWindow: inferContextWindow(modelID),
			FreeTier:      true,
			Source:        "freellmapi",
		})
	}
	return entries
}

func parseCatalogJSON(rawJSON []byte, providerDir map[string]upstreamProviderMeta) []CatalogEntry {
	type jsonCatalogRow struct {
		ProviderSlug  string `json:"providerSlug"`
		Platform      string `json:"platform"`
		ProviderName  string `json:"providerName"`
		BaseURL       string `json:"baseUrl"`
		ModelID       string `json:"modelId"`
		ContextWindow int    `json:"contextWindow"`
		FreeTier      *bool  `json:"freeTier"`
	}

	var rows []jsonCatalogRow
	if err := json.Unmarshal(rawJSON, &rows); err != nil {
		var wrapped struct {
			Models  []jsonCatalogRow `json:"models"`
			Entries []jsonCatalogRow `json:"entries"`
		}
		if wrapErr := json.Unmarshal(rawJSON, &wrapped); wrapErr != nil {
			return nil
		}
		rows = append(rows, wrapped.Models...)
		rows = append(rows, wrapped.Entries...)
	}

	entries := make([]CatalogEntry, 0, len(rows))
	for _, row := range rows {
		slug := strings.ToLower(strings.TrimSpace(row.ProviderSlug))
		if slug == "" {
			slug = strings.ToLower(strings.TrimSpace(row.Platform))
		}
		modelID := strings.TrimSpace(row.ModelID)
		if slug == "" || modelID == "" {
			continue
		}
		meta := providerDir[slug]
		name := strings.TrimSpace(row.ProviderName)
		if name == "" {
			name = meta.Name
			if name == "" {
				name = slug
			}
		}
		baseURL := strings.TrimSpace(row.BaseURL)
		if baseURL == "" {
			baseURL = meta.BaseURL
		}
		ctxWindow := row.ContextWindow
		if ctxWindow <= 0 {
			ctxWindow = inferContextWindow(modelID)
		}
		freeTier := true
		if row.FreeTier != nil {
			freeTier = *row.FreeTier
		}
		entries = append(entries, CatalogEntry{
			ProviderSlug:  slug,
			ProviderName:  name,
			BaseURL:       baseURL,
			ModelID:       modelID,
			ContextWindow: ctxWindow,
			FreeTier:      freeTier,
			Source:        "freellmapi",
		})
	}
	return entries
}

func inferContextWindow(modelID string) int {
	lower := strings.ToLower(modelID)
	switch {
	case strings.Contains(lower, "gemini-2.5"), strings.Contains(lower, "gemini-3"):
		return 1048576
	case strings.Contains(lower, "qwen3-coder"), strings.Contains(lower, "kimi-k2"), strings.Contains(lower, "codestral"):
		return 262144
	case strings.Contains(lower, "llama-3.3"), strings.Contains(lower, "llama-4"), strings.Contains(lower, "gpt-oss"), strings.Contains(lower, "deepseek"), strings.Contains(lower, "gemma-4"):
		return 131072
	default:
		return 128000
	}
}
