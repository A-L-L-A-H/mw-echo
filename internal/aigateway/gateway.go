package aigateway

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
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	headerAgentID    = "X-MW-Agent"
	headerRTKMode    = "X-MW-RTK-Mode"
	headerCacheState = "X-MW-Echo-Cache"
	headerProvider   = "X-MW-Echo-Provider"
	headerModel      = "X-MW-Echo-Model"
	headerRTKSaved   = "X-MW-Echo-RTK-Saved"
)

type routeOutcome struct {
	Response         ChatCompletionResponse
	RawBody          []byte
	ResolvedProvider string
	ResolvedModel    string
	AgentID          string
	CacheHit         bool
	RTKTokensSaved   int
	LatencyMS        int64
}

type upstreamCallError struct {
	StatusCode int
	Body       string
	Retryable  bool
	Cooldown   bool
}

func (e *upstreamCallError) Error() string {
	return fmt.Sprintf("upstream HTTP %d: %s", e.StatusCode, e.Body)
}

// RegisterRoutes mounts OpenAI/Anthropic gateway routes on root (/v1/*) and
// management control-plane routes on api (/api/ai/*).
func (g *Gateway) RegisterRoutes(root *echo.Echo, api *echo.Group) {
	if root != nil {
		root.GET("/v1/models", g.handleListOpenAIModels)
		root.POST("/v1/chat/completions", g.handleChatCompletions)
		root.POST("/v1/messages", g.handleAnthropicMessages)
	}

	if api != nil {
		api.GET("/ai/stats", g.handleGetStats)
		api.GET("/ai/providers", g.handleListProviders)
		api.POST("/ai/providers", g.handleCreateProvider)
		api.DELETE("/ai/providers/:id", g.handleDeleteProvider)
		api.GET("/ai/agents", g.handleListAgents)
		api.PUT("/ai/agents/:agentId", g.handleUpdateAgent)
		api.GET("/ai/catalog", g.handleListCatalog)
		api.POST("/ai/catalog/sniff", g.handleSniffCatalog)
		api.PUT("/ai/settings", g.handleUpdateSettings)
		api.DELETE("/ai/cache", g.handlePurgeCache)
	}
}

// --- Settings & Store Operations ---

// GetSettings loads gateway settings from ai_settings with safe defaults.
func (g *Gateway) GetSettings(ctx context.Context) GatewaySettings {
	settings := GatewaySettings{
		RTKEnabled:    true,
		CacheEnabled:  true,
		FallbackOrder: DefaultFallbackOrder,
		UpstreamRepo:  DefaultUpstreamRepo,
	}

	rows, err := g.db.QueryContext(ctx, `SELECT key, value FROM ai_settings`)
	if err != nil {
		return settings
	}
	defer rows.Close()

	for rows.Next() {
		var settingKey, settingVal string
		if rows.Scan(&settingKey, &settingVal) != nil {
			continue
		}
		switch settingKey {
		case "rtk_enabled":
			settings.RTKEnabled = settingVal != "false" && settingVal != "0"
		case "cache_enabled":
			settings.CacheEnabled = settingVal != "false" && settingVal != "0"
		case "fallback_order":
			if strings.TrimSpace(settingVal) != "" {
				settings.FallbackOrder = settingVal
			}
		case "upstream_repo":
			if strings.TrimSpace(settingVal) != "" {
				settings.UpstreamRepo = settingVal
			}
		case "upstream_sha":
			settings.UpstreamSHA = settingVal
		case "last_sniff_at":
			settings.LastSniffAt = settingVal
		}
	}
	return settings
}

// UpdateSettings updates gateway feature flags and fallback ordering.
func (g *Gateway) UpdateSettings(ctx context.Context, input SettingsUpdateInput) (GatewaySettings, error) {
	upsert := func(key, value string) error {
		_, err := g.db.ExecContext(ctx, `
INSERT INTO ai_settings (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
		return err
	}

	if input.RTKEnabled != nil {
		if err := upsert("rtk_enabled", strconv.FormatBool(*input.RTKEnabled)); err != nil {
			return GatewaySettings{}, err
		}
	}
	if input.CacheEnabled != nil {
		if err := upsert("cache_enabled", strconv.FormatBool(*input.CacheEnabled)); err != nil {
			return GatewaySettings{}, err
		}
	}
	if input.FallbackOrder != nil {
		normalized, err := normalizeTierOrder(*input.FallbackOrder)
		if err != nil {
			return GatewaySettings{}, err
		}
		if err := upsert("fallback_order", normalized); err != nil {
			return GatewaySettings{}, err
		}
	}
	if input.UpstreamRepo != nil {
		repo := strings.TrimSpace(*input.UpstreamRepo)
		if repo == "" || !strings.Contains(repo, "/") {
			return GatewaySettings{}, errors.New("upstreamRepo must be in owner/repo format")
		}
		if err := upsert("upstream_repo", repo); err != nil {
			return GatewaySettings{}, err
		}
	}
	return g.GetSettings(ctx), nil
}

// AddProvider validates, encrypts credentials with SecretBox, and stores a new AI provider.
func (g *Gateway) AddProvider(ctx context.Context, input ProviderCreateInput) (Provider, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Provider{}, errors.New("provider name is required")
	}

	kind := strings.TrimSpace(strings.ToLower(input.Kind))
	if kind == "" {
		kind = KindOpenAICompat
	}
	if !isValidProviderKind(kind) {
		return Provider{}, fmt.Errorf("kind must be one of %s, %s, %s", KindOpenAICompat, KindAnthropic, KindGemini)
	}

	tier := strings.TrimSpace(strings.ToLower(input.Tier))
	if tier == "" {
		tier = TierFree
	}
	if !isValidTier(tier) {
		return Provider{}, fmt.Errorf("tier must be one of %s, %s, %s, %s", TierFree, TierCheap, TierSubscription, TierPaid)
	}

	baseURL := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	if baseURL == "" || (!strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://")) {
		return Provider{}, errors.New("valid http(s) baseUrl is required")
	}

	apiKeyEnc := ""
	trimmedKey := strings.TrimSpace(input.APIKey)
	if trimmedKey != "" {
		encrypted, err := g.box.Encrypt([]byte(trimmedKey))
		if err != nil {
			return Provider{}, fmt.Errorf("encrypt api key: %w", err)
		}
		apiKeyEnc = encrypted
	}

	cleanModels := make([]string, 0, len(input.Models))
	seenModels := make(map[string]bool)
	for _, rawModel := range input.Models {
		trimmedModel := strings.TrimSpace(rawModel)
		if trimmedModel != "" && !seenModels[trimmedModel] {
			seenModels[trimmedModel] = true
			cleanModels = append(cleanModels, trimmedModel)
		}
	}
	defaultModel := strings.TrimSpace(input.DefaultModel)
	if defaultModel == "" && len(cleanModels) > 0 {
		defaultModel = cleanModels[0]
	}
	if defaultModel != "" && !seenModels[defaultModel] && len(cleanModels) > 0 {
		cleanModels = append([]string{defaultModel}, cleanModels...)
	}

	modelsBytes, _ := json.Marshal(cleanModels)

	priority := input.Priority
	if priority <= 0 {
		priority = 100
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	now := time.Now().UTC().Format(time.RFC3339)
	providerID := makeGatewayID("aip", kind, name, time.Now().UTC().Format(time.RFC3339Nano))

	_, err := g.db.ExecContext(ctx, `
INSERT INTO ai_providers (
  id, name, kind, tier, base_url, api_key_enc, default_model, models_json,
  priority, enabled, cooldown_until, req_count, err_count, tokens_in, tokens_out, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, 0, 0, 0, ?)`,
		providerID, name, kind, tier, baseURL, apiKeyEnc, defaultModel, string(modelsBytes),
		priority, boolToInt(enabled), now,
	)
	if err != nil {
		return Provider{}, err
	}

	return Provider{
		ID:           providerID,
		Name:         name,
		Kind:         kind,
		Tier:         tier,
		BaseURL:      baseURL,
		HasAPIKey:    apiKeyEnc != "",
		DefaultModel: defaultModel,
		Models:       cleanModels,
		Priority:     priority,
		Enabled:      enabled,
		CreatedAt:    now,
	}, nil
}

// ListProviders returns all providers with secrets stripped.
func (g *Gateway) ListProviders(ctx context.Context) ([]Provider, error) {
	storedList, err := g.loadStoredProviders(ctx, false)
	if err != nil {
		return nil, err
	}
	sanitized := make([]Provider, 0, len(storedList))
	for _, entry := range storedList {
		sanitized = append(sanitized, entry.Provider)
	}
	return sanitized, nil
}

func (g *Gateway) loadStoredProviders(ctx context.Context, enabledOnly bool) ([]storedProvider, error) {
	querySQL := `
SELECT id, name, kind, tier, base_url, api_key_enc, default_model, models_json,
       priority, enabled, cooldown_until, req_count, err_count, tokens_in, tokens_out, created_at
FROM ai_providers`
	if enabledOnly {
		querySQL += ` WHERE enabled = 1`
	}
	querySQL += ` ORDER BY priority ASC, created_at ASC`

	rows, err := g.db.QueryContext(ctx, querySQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now().UTC()
	providers := make([]storedProvider, 0)
	for rows.Next() {
		var record storedProvider
		var modelsRaw string
		var enabledInt int
		if err := rows.Scan(
			&record.ID, &record.Name, &record.Kind, &record.Tier, &record.BaseURL,
			&record.APIKeyEnc, &record.DefaultModel, &modelsRaw, &record.Priority,
			&enabledInt, &record.CooldownUntil, &record.ReqCount, &record.ErrCount,
			&record.TokensIn, &record.TokensOut, &record.CreatedAt,
		); err != nil {
			return nil, err
		}
		record.Enabled = enabledInt == 1
		record.HasAPIKey = record.APIKeyEnc != ""
		record.Models = make([]string, 0)
		_ = json.Unmarshal([]byte(modelsRaw), &record.Models)
		if record.CooldownUntil != "" {
			if untilTime, parseErr := time.Parse(time.RFC3339, record.CooldownUntil); parseErr == nil && untilTime.After(now) {
				record.InCooldown = true
			}
		}
		providers = append(providers, record)
	}
	return providers, rows.Err()
}

// DeleteProvider removes a configured provider by ID.
func (g *Gateway) DeleteProvider(ctx context.Context, providerID string) error {
	execOut, err := g.db.ExecContext(ctx, `DELETE FROM ai_providers WHERE id = ?`, providerID)
	if err != nil {
		return err
	}
	affected, _ := execOut.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListAgentRoutes returns all Magpie per-agent routing entries.
func (g *Gateway) ListAgentRoutes(ctx context.Context) ([]AgentRoute, error) {
	rows, err := g.db.QueryContext(ctx, `
SELECT agent_id, target_model, tier_order, rtk_enabled, updated_at
FROM ai_agent_routes ORDER BY CASE agent_id WHEN 'default' THEN 1 ELSE 0 END, agent_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	routes := make([]AgentRoute, 0)
	for rows.Next() {
		var route AgentRoute
		var rtkInt int
		if err := rows.Scan(&route.AgentID, &route.TargetModel, &route.TierOrder, &rtkInt, &route.UpdatedAt); err != nil {
			return nil, err
		}
		route.RTKEnabled = rtkInt == 1
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

// GetAgentRoute fetches a single agent route by ID.
func (g *Gateway) GetAgentRoute(ctx context.Context, agentID string) (AgentRoute, error) {
	var route AgentRoute
	var rtkInt int
	err := g.db.QueryRowContext(ctx, `
SELECT agent_id, target_model, tier_order, rtk_enabled, updated_at
FROM ai_agent_routes WHERE agent_id = ?`, agentID).Scan(
		&route.AgentID, &route.TargetModel, &route.TierOrder, &rtkInt, &route.UpdatedAt,
	)
	if err != nil {
		return AgentRoute{}, err
	}
	route.RTKEnabled = rtkInt == 1
	return route, nil
}

// UpdateAgentRoute creates or updates a Magpie per-agent model switchboard route.
func (g *Gateway) UpdateAgentRoute(ctx context.Context, agentID string, input AgentRouteUpdateInput) (AgentRoute,
	error) {
	cleanID := strings.TrimSpace(strings.ToLower(agentID))
	if cleanID == "" {
		return AgentRoute{}, errors.New("agentId is required")
	}

	existing, err := g.GetAgentRoute(ctx, cleanID)
	if errors.Is(err, sql.ErrNoRows) {
		existing = AgentRoute{
			AgentID:    cleanID,
			TierOrder:  DefaultFallbackOrder,
			RTKEnabled: true,
		}
	} else if err != nil {
		return AgentRoute{}, err
	}

	if input.TargetModel != nil {
		existing.TargetModel = strings.TrimSpace(*input.TargetModel)
	}
	if input.TierOrder != nil {
		normalized, tierErr := normalizeTierOrder(*input.TierOrder)
		if tierErr != nil {
			return AgentRoute{}, tierErr
		}
		existing.TierOrder = normalized
	}
	if input.RTKEnabled != nil {
		existing.RTKEnabled = *input.RTKEnabled
	}
	existing.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	_, err = g.db.ExecContext(ctx, `
INSERT INTO ai_agent_routes (agent_id, target_model, tier_order, rtk_enabled, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(agent_id) DO UPDATE SET
  target_model = excluded.target_model,
  tier_order = excluded.tier_order,
  rtk_enabled = excluded.rtk_enabled,
  updated_at = excluded.updated_at`,
		existing.AgentID, existing.TargetModel, existing.TierOrder, boolToInt(existing.RTKEnabled), existing.UpdatedAt,
	)
	return existing, err
}

// ListCatalog queries ai_catalog with optional keyword search and free-tier filter.
func (g *Gateway) ListCatalog(ctx context.Context, searchQuery string, freeOnly bool) ([]CatalogEntry, error) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 4)

	if trimmed := strings.TrimSpace(strings.ToLower(searchQuery)); trimmed != "" {
		pattern := "%" + trimmed + "%"
		clauses = append(clauses, `(LOWER(provider_slug) LIKE ? OR LOWER(provider_name) LIKE ? OR LOWER(model_id) LIKE ? OR LOWER(base_url) LIKE ?)`)
		args = append(args, pattern, pattern, pattern, pattern)
	}
	if freeOnly {
		clauses = append(clauses, `free_tier = 1`)
	}

	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = "WHERE " + strings.Join(clauses, " AND ")
	}

	rows, err := g.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, provider_slug, provider_name, base_url, model_id, context_window, free_tier, source, updated_at
FROM ai_catalog %s
ORDER BY free_tier DESC, provider_slug ASC, model_id ASC`, whereSQL), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]CatalogEntry, 0)
	for rows.Next() {
		var entry CatalogEntry
		var freeInt int
		if err := rows.Scan(
			&entry.ID, &entry.ProviderSlug, &entry.ProviderName, &entry.BaseURL,
			&entry.ModelID, &entry.ContextWindow, &freeInt, &entry.Source, &entry.UpdatedAt,
		); err != nil {
			return nil, err
		}
		entry.FreeTier = freeInt == 1
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// PurgeCache clears all cached responses from ai_response_cache.
func (g *Gateway) PurgeCache(ctx context.Context) (int64, error) {
	execOut, err := g.db.ExecContext(ctx, `DELETE FROM ai_response_cache`)
	if err != nil {
		return 0, err
	}
	affected, _ := execOut.RowsAffected()
	return affected, nil
}

// Stats aggregates gateway telemetry, token savings, and recent 25 logs.
func (g *Gateway) Stats(ctx context.Context) (GatewayStats, error) {
	stats := GatewayStats{
		Settings:   g.GetSettings(ctx),
		RecentLogs: make([]RequestLog, 0),
	}

	_ = g.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN cache_hit = 1 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(rtk_tokens_saved), 0),
       COALESCE(AVG(latency_ms), 0)
FROM ai_request_logs`).Scan(
		&stats.TotalRequests,
		&stats.CacheHits,
		&stats.TotalRTKTokensSaved,
		&stats.AvgLatencyMS,
	)

	_ = g.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(tokens_saved), 0) FROM ai_response_cache`).Scan(&stats.CacheTokensSaved)
	_ = g.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_providers`).Scan(&stats.ProvidersCount)
	_ = g.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_catalog`).Scan(&stats.CatalogCount)

	rows, err := g.db.QueryContext(ctx, `
SELECT id, agent_id, requested_model, resolved_provider, resolved_model,
       status_code, latency_ms, prompt_tokens, completion_tokens, rtk_tokens_saved, cache_hit, created_at
FROM ai_request_logs
ORDER BY created_at DESC, rowid DESC
LIMIT 25`)
	if err != nil {
		return stats, err
	}
	defer rows.Close()

	for rows.Next() {
		var logRow RequestLog
		var cacheInt int
		if err := rows.Scan(
			&logRow.ID, &logRow.AgentID, &logRow.RequestedModel, &logRow.ResolvedProvider,
			&logRow.ResolvedModel, &logRow.StatusCode, &logRow.LatencyMS, &logRow.PromptTokens,
			&logRow.CompletionTokens, &logRow.RTKTokensSaved, &cacheInt, &logRow.CreatedAt,
		); err != nil {
			return stats, err
		}
		logRow.CacheHit = cacheInt == 1
		stats.RecentLogs = append(stats.RecentLogs, logRow)
	}
	return stats, rows.Err()
}

// --- Core Routing Engine (Magpie + RTK + Exact Cache + Bifrost Failover) ---

func (g *Gateway) resolveRoutePlan(ctx context.Context, explicitAgentHeader, defaultAgentID,
	requestedModel string) (string, string, string, bool) {
	settings := g.GetSettings(ctx)

	agentID := strings.TrimSpace(strings.ToLower(explicitAgentHeader))
	viaAgentPrefix := false
	if strings.HasPrefix(requestedModel, "agent/") {
		agentID = strings.TrimSpace(strings.ToLower(strings.TrimPrefix(requestedModel, "agent/")))
		viaAgentPrefix = true
	}
	if agentID == "" {
		agentID = defaultAgentID
	}
	if agentID == "" {
		agentID = "default"
	}

	route, err := g.GetAgentRoute(ctx, agentID)
	if err != nil {
		route, err = g.GetAgentRoute(ctx, "default")
		if err != nil {
			route = AgentRoute{
				AgentID:    agentID,
				TierOrder:  settings.FallbackOrder,
				RTKEnabled: true,
			}
		}
	}

	effectiveModel := strings.TrimSpace(requestedModel)
	if viaAgentPrefix {
		effectiveModel = strings.TrimSpace(route.TargetModel)
	} else if route.TargetModel != "" {
		shouldOverride := agentID != "default" || effectiveModel == "" || strings.EqualFold(effectiveModel, "auto")
		if shouldOverride {
			effectiveModel = strings.TrimSpace(route.TargetModel)
		}
	}

	tierOrder := strings.TrimSpace(route.TierOrder)
	if tierOrder == "" {
		tierOrder = settings.FallbackOrder
	}
	rtkActive := settings.RTKEnabled && route.RTKEnabled
	return agentID, effectiveModel, tierOrder, rtkActive
}

func (g *Gateway) routeCompletion(ctx context.Context, explicitAgentHeader, defaultAgentID, rtkMode string,
	incomingReq ChatCompletionRequest) (routeOutcome, error) {
	startedAt := time.Now()
	requestedModel := strings.TrimSpace(incomingReq.Model)
	settings := g.GetSettings(ctx)

	agentID, effectiveModel, tierOrder, rtkActive := g.resolveRoutePlan(ctx, explicitAgentHeader, defaultAgentID,
		requestedModel)

	outboundReq := incomingReq
	outboundReq.Model = effectiveModel

	rtkTokensSaved := 0
	if strings.EqualFold(strings.TrimSpace(rtkMode), "caveman") {
		compressedMsgs, saved := CompressCaveman(outboundReq.Messages)
		outboundReq.Messages = compressedMsgs
		rtkTokensSaved = saved
	} else if rtkActive {
		compressedMsgs, saved := CompressMessages(outboundReq.Messages)
		outboundReq.Messages = compressedMsgs
		rtkTokensSaved = saved
	}

	// Exact SQLite response cache check for deterministic non-streaming requests.
	cacheEligible := settings.CacheEnabled && !outboundReq.Stream &&
		(outboundReq.Temperature == nil || *outboundReq.Temperature <= 0.2)
	cacheKey := ""
	if cacheEligible {
		cacheKey = computePromptCacheKey(agentID, outboundReq)
		if cachedBody, cachedResp, hit := g.lookupResponseCache(ctx, cacheKey); hit {
			latencyMS := time.Since(startedAt).Milliseconds()
			g.recordRequestLog(ctx, RequestLog{
				AgentID:          agentID,
				RequestedModel:   requestedModel,
				ResolvedProvider: "cache",
				ResolvedModel:    cachedResp.Model,
				StatusCode:       http.StatusOK,
				LatencyMS:        latencyMS,
				PromptTokens:     cachedResp.Usage.PromptTokens,
				CompletionTokens: cachedResp.Usage.CompletionTokens,
				RTKTokensSaved:   rtkTokensSaved,
				CacheHit:         true,
			})
			return routeOutcome{
				Response:         cachedResp,
				RawBody:          cachedBody,
				ResolvedProvider: "cache",
				ResolvedModel:    cachedResp.Model,
				AgentID:          agentID,
				CacheHit:         true,
				RTKTokensSaved:   rtkTokensSaved,
				LatencyMS:        latencyMS,
			}, nil
		}
	}

	candidates, err := g.selectCandidateProviders(ctx, effectiveModel, tierOrder)
	if err != nil {
		return routeOutcome{}, err
	}
	if len(candidates) == 0 {
		latencyMS := time.Since(startedAt).Milliseconds()
		g.recordRequestLog(ctx, RequestLog{
			AgentID:        agentID,
			RequestedModel: requestedModel,
			StatusCode:     http.StatusServiceUnavailable,
			LatencyMS:      latencyMS,
			RTKTokensSaved: rtkTokensSaved,
		})
		return routeOutcome{}, &upstreamCallError{
			StatusCode: http.StatusServiceUnavailable,
			Body:       "no healthy AI providers available (configure a provider or wait for cooldown expiry)",
		}
	}

	var lastCallErr error
	for _, candidate := range candidates {
		resolvedModel := pickProviderModel(candidate, effectiveModel)
		attemptReq := outboundReq
		attemptReq.Model = resolvedModel

		apiKeyPlain := ""
		if candidate.APIKeyEnc != "" {
			plainBytes, decErr := g.box.Decrypt(candidate.APIKeyEnc)
			if decErr != nil {
				lastCallErr = fmt.Errorf("decrypt key for provider %s: %w", candidate.Name, decErr)
				g.recordProviderError(ctx, candidate.ID, false)
				continue
			}
			apiKeyPlain = string(plainBytes)
		}

		rawRespBytes, parsedResp, callErr := g.dispatchProvider(ctx, candidate, apiKeyPlain, attemptReq)
		if callErr != nil {
			lastCallErr = callErr
			var upErr *upstreamCallError
			if errors.As(callErr, &upErr) {
				g.recordProviderError(ctx, candidate.ID, upErr.Cooldown)
				if upErr.Retryable {
					continue
				}
				latencyMS := time.Since(startedAt).Milliseconds()
				g.recordRequestLog(ctx, RequestLog{
					AgentID:          agentID,
					RequestedModel:   requestedModel,
					ResolvedProvider: candidate.Name,
					ResolvedModel:    resolvedModel,
					StatusCode:       upErr.StatusCode,
					LatencyMS:        latencyMS,
					RTKTokensSaved:   rtkTokensSaved,
				})
				return routeOutcome{}, upErr
			}
			g.recordProviderError(ctx, candidate.ID, false)
			continue
		}

		if parsedResp.Model == "" {
			parsedResp.Model = resolvedModel
		}
		if len(rawRespBytes) == 0 {
			rawRespBytes, _ = json.Marshal(parsedResp)
		}

		latencyMS := time.Since(startedAt).Milliseconds()
		g.recordProviderSuccess(ctx, candidate.ID, parsedResp.Usage.PromptTokens, parsedResp.Usage.CompletionTokens)
		g.recordRequestLog(ctx, RequestLog{
			AgentID:          agentID,
			RequestedModel:   requestedModel,
			ResolvedProvider: candidate.Name,
			ResolvedModel:    parsedResp.Model,
			StatusCode:       http.StatusOK,
			LatencyMS:        latencyMS,
			PromptTokens:     parsedResp.Usage.PromptTokens,
			CompletionTokens: parsedResp.Usage.CompletionTokens,
			RTKTokensSaved:   rtkTokensSaved,
			CacheHit:         false,
		})

		if cacheEligible && cacheKey != "" {
			g.storeResponseCache(ctx, cacheKey, parsedResp.Model, rawRespBytes)
		}

		return routeOutcome{
			Response:         parsedResp,
			RawBody:          rawRespBytes,
			ResolvedProvider: candidate.Name,
			ResolvedModel:    parsedResp.Model,
			AgentID:          agentID,
			CacheHit:         false,
			RTKTokensSaved:   rtkTokensSaved,
			LatencyMS:        latencyMS,
		}, nil
	}

	latencyMS := time.Since(startedAt).Milliseconds()
	statusCode := http.StatusBadGateway
	var upErr *upstreamCallError
	if errors.As(lastCallErr, &upErr) && upErr.StatusCode > 0 {
		statusCode = upErr.StatusCode
	}
	g.recordRequestLog(ctx, RequestLog{
		AgentID:        agentID,
		RequestedModel: requestedModel,
		StatusCode:     statusCode,
		LatencyMS:      latencyMS,
		RTKTokensSaved: rtkTokensSaved,
	})
	if lastCallErr == nil {
		lastCallErr = &upstreamCallError{StatusCode: http.StatusBadGateway, Body: "all upstream providers failed"}
	}
	return routeOutcome{}, lastCallErr
}

func (g *Gateway) selectCandidateProviders(ctx context.Context, effectiveModel, tierOrder string) ([]storedProvider,
	error) {
	enabledProviders, err := g.loadStoredProviders(ctx, true)
	if err != nil {
		return nil, err
	}

	tierRank := make(map[string]int)
	for idx, tierName := range strings.Split(tierOrder, ",") {
		cleanTier := strings.TrimSpace(strings.ToLower(tierName))
		if cleanTier != "" {
			tierRank[cleanTier] = idx
		}
	}
	getTierRank := func(tier string) int {
		if rank, ok := tierRank[strings.ToLower(tier)]; ok {
			return rank
		}
		return len(tierRank) + 10
	}

	healthy := make([]storedProvider, 0, len(enabledProviders))
	for _, provider := range enabledProviders {
		if provider.InCooldown {
			continue
		}
		healthy = append(healthy, provider)
	}

	modelSpecific := effectiveModel != "" && !strings.EqualFold(effectiveModel, "auto")
	sort.SliceStable(healthy, func(i, j int) bool {
		leftMatch := !modelSpecific || providerMatchesModel(healthy[i], effectiveModel)
		rightMatch := !modelSpecific || providerMatchesModel(healthy[j], effectiveModel)
		if leftMatch != rightMatch {
			return leftMatch
		}
		leftTier := getTierRank(healthy[i].Tier)
		rightTier := getTierRank(healthy[j].Tier)
		if leftTier != rightTier {
			return leftTier < rightTier
		}
		if healthy[i].Priority != healthy[j].Priority {
			return healthy[i].Priority < healthy[j].Priority
		}
		return healthy[i].CreatedAt < healthy[j].CreatedAt
	})

	return healthy, nil
}

func providerMatchesModel(provider storedProvider, targetModel string) bool {
	if targetModel == "" || strings.EqualFold(targetModel, "auto") {
		return true
	}
	if strings.EqualFold(provider.DefaultModel, targetModel) {
		return true
	}
	if len(provider.Models) == 0 {
		return true
	}
	for _, declaredModel := range provider.Models {
		if strings.EqualFold(declaredModel, targetModel) {
			return true
		}
	}
	return false
}

func pickProviderModel(provider storedProvider, requestedModel string) string {
	trimmed := strings.TrimSpace(requestedModel)
	if trimmed == "" || strings.EqualFold(trimmed, "auto") {
		if provider.DefaultModel != "" {
			return provider.DefaultModel
		}
		if len(provider.Models) > 0 {
			return provider.Models[0]
		}
		return "gpt-4o-mini"
	}
	if len(provider.Models) == 0 || providerMatchesModel(provider, trimmed) {
		return trimmed
	}
	if provider.DefaultModel != "" {
		return provider.DefaultModel
	}
	return trimmed
}

func (g *Gateway) dispatchProvider(ctx context.Context, provider storedProvider, apiKey string,
	req ChatCompletionRequest) ([]byte, ChatCompletionResponse, error) {
	switch provider.Kind {
	case KindAnthropic:
		return g.dispatchAnthropicUpstream(ctx, provider, apiKey, req)
	case KindGemini:
		if strings.Contains(strings.ToLower(provider.BaseURL), "/openai") {
			return g.dispatchOpenAICompatUpstream(ctx, provider, apiKey, req)
		}
		return g.dispatchGeminiNativeUpstream(ctx, provider, apiKey, req)
	default:
		return g.dispatchOpenAICompatUpstream(ctx, provider, apiKey, req)
	}
}

func (g *Gateway) dispatchOpenAICompatUpstream(ctx context.Context, provider storedProvider, apiKey string,
	req ChatCompletionRequest) ([]byte, ChatCompletionResponse, error) {
	endpoint := strings.TrimRight(provider.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}

	reqBodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, ChatCompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBodyBytes))
	if err != nil {
		return nil, ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	httpResp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, ChatCompletionResponse{}, &upstreamCallError{
			StatusCode: http.StatusBadGateway,
			Body:       err.Error(),
			Retryable:  true,
		}
	}
	defer httpResp.Body.Close()

	respBytes, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024*1024))
	if readErr != nil {
		return nil, ChatCompletionResponse{}, &upstreamCallError{
			StatusCode: http.StatusBadGateway,
			Body:       readErr.Error(),
			Retryable:  true,
		}
	}

	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, ChatCompletionResponse{}, &upstreamCallError{
			StatusCode: httpResp.StatusCode,
			Body:       string(respBytes),
			Retryable:  true,
			Cooldown:   true,
		}
	}
	if httpResp.StatusCode >= 500 || httpResp.StatusCode == http.StatusUnauthorized ||
		httpResp.StatusCode == http.StatusForbidden {
		return nil, ChatCompletionResponse{}, &upstreamCallError{
			StatusCode: httpResp.StatusCode,
			Body:       string(respBytes),
			Retryable:  true,
		}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, ChatCompletionResponse{}, &upstreamCallError{
			StatusCode: httpResp.StatusCode,
			Body:       string(respBytes),
			Retryable:  false,
		}
	}

	var parsed ChatCompletionResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, ChatCompletionResponse{}, &upstreamCallError{
			StatusCode: http.StatusBadGateway,
			Body:       "invalid JSON from upstream provider: " + err.Error(),
			Retryable:  true,
		}
	}
	return respBytes, parsed, nil
}

func (g *Gateway) dispatchAnthropicUpstream(ctx context.Context, provider storedProvider, apiKey string,
	req ChatCompletionRequest) ([]byte, ChatCompletionResponse, error) {
	endpoint := strings.TrimRight(provider.BaseURL, "/")
	if !strings.HasSuffix(endpoint, "/messages") {
		if strings.HasSuffix(endpoint, "/v1") {
			endpoint += "/messages"
		} else {
			endpoint += "/v1/messages"
		}
	}

	anthReq := openAIToAnthropicRequest(req)
	reqBodyBytes, err := json.Marshal(anthReq)
	if err != nil {
		return nil, ChatCompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBodyBytes))
	if err != nil {
		return nil, ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if apiKey != "" {
		httpReq.Header.Set("x-api-key", apiKey)
	}

	httpResp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: http.StatusBadGateway, Body: err.Error(),
			Retryable: true}
	}
	defer httpResp.Body.Close()

	respBytes, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024*1024))
	if readErr != nil {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: http.StatusBadGateway, Body: readErr.Error(),
			Retryable: true}
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: httpResp.StatusCode, Body: string(respBytes),
			Retryable: true, Cooldown: true}
	}
	if httpResp.StatusCode >= 500 || httpResp.StatusCode == http.StatusUnauthorized ||
		httpResp.StatusCode == http.StatusForbidden {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: httpResp.StatusCode, Body: string(respBytes),
			Retryable: true}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: httpResp.StatusCode, Body: string(respBytes),
			Retryable: false}
	}

	var anthResp AnthropicMessageResponse
	if err := json.Unmarshal(respBytes, &anthResp); err != nil {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: http.StatusBadGateway, Body: err.Error(),
			Retryable: true}
	}
	converted := anthropicToOpenAIResponse(anthResp, req.Model)
	encoded, _ := json.Marshal(converted)
	return encoded, converted, nil
}

func (g *Gateway) dispatchGeminiNativeUpstream(ctx context.Context, provider storedProvider, apiKey string,
	req ChatCompletionRequest) ([]byte, ChatCompletionResponse, error) {
	endpoint := fmt.Sprintf("%s/models/%s:generateContent", strings.TrimRight(provider.BaseURL, "/"), req.Model)
	if apiKey != "" {
		endpoint += "?key=" + apiKey
	}

	type geminiPart struct {
		Text string `json:"text"`
	}
	type geminiContent struct {
		Role  string       `json:"role"`
		Parts []geminiPart `json:"parts"`
	}
	contents := make([]geminiContent, 0, len(req.Messages))
	for _, msg := range req.Messages {
		role := "user"
		if strings.EqualFold(msg.Role, "assistant") {
			role = "model"
		}
		textVal := flattenMessageContentToText(msg.Content)
		if textVal != "" {
			contents = append(contents, geminiContent{
				Role:  role,
				Parts: []geminiPart{{Text: textVal}},
			})
		}
	}

	payloadMap := map[string]any{"contents": contents}
	reqBodyBytes, _ := json.Marshal(payloadMap)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBodyBytes))
	if err != nil {
		return nil, ChatCompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: http.StatusBadGateway, Body: err.Error(),
			Retryable: true}
	}
	defer httpResp.Body.Close()

	respBytes, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024*1024))
	if readErr != nil {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: http.StatusBadGateway, Body: readErr.Error(),
			Retryable: true}
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: httpResp.StatusCode, Body: string(respBytes),
			Retryable: true, Cooldown: true}
	}
	if httpResp.StatusCode >= 500 || httpResp.StatusCode == http.StatusUnauthorized ||
		httpResp.StatusCode == http.StatusForbidden {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: httpResp.StatusCode, Body: string(respBytes),
			Retryable: true}
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: httpResp.StatusCode, Body: string(respBytes),
			Retryable: false}
	}

	var geminiEnvelope struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(respBytes, &geminiEnvelope); err != nil {
		return nil, ChatCompletionResponse{}, &upstreamCallError{StatusCode: http.StatusBadGateway, Body: err.Error(),
			Retryable: true}
	}

	var replyBuilder strings.Builder
	if len(geminiEnvelope.Candidates) > 0 {
		for _, part := range geminiEnvelope.Candidates[0].Content.Parts {
			replyBuilder.WriteString(part.Text)
		}
	}

	converted := ChatCompletionResponse{
		ID:      makeGatewayID("chatcmpl", req.Model, time.Now().UTC().Format(time.RFC3339Nano)),
		Object:  "chat.completion",
		Created: time.Now().UTC().Unix(),
		Model:   req.Model,
		Choices: []ChatCompletionChoice{
			{
				Index:        0,
				Message:      ChatMessage{Role: "assistant", Content: replyBuilder.String()},
				FinishReason: "stop",
			},
		},
		Usage: ChatCompletionUsage{
			PromptTokens:     geminiEnvelope.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiEnvelope.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      geminiEnvelope.UsageMetadata.TotalTokenCount,
		},
	}
	encoded, _ := json.Marshal(converted)
	return encoded, converted, nil
}

// --- Exact Prompt Cache Helpers ---

func computePromptCacheKey(agentID string, req ChatCompletionRequest) string {
	canonical, _ := json.Marshal(struct {
		AgentID     string          `json:"agentId"`
		Model       string          `json:"model"`
		Messages    []ChatMessage   `json:"messages"`
		Temperature *float64        `json:"temperature,omitempty"`
		MaxTokens   *int            `json:"max_tokens,omitempty"`
		Tools       json.RawMessage `json:"tools,omitempty"`
		ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
		Stop        json.RawMessage `json:"stop,omitempty"`
	}{
		AgentID:     agentID,
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
		Stop:        req.Stop,
	})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func (g *Gateway) lookupResponseCache(ctx context.Context, cacheKey string) ([]byte, ChatCompletionResponse, bool) {
	var rawJSON string
	err := g.db.QueryRowContext(ctx, `SELECT response_json FROM ai_response_cache WHERE cache_key = ?`, cacheKey).Scan(&rawJSON)
	if err != nil || rawJSON == "" {
		return nil, ChatCompletionResponse{}, false
	}
	var parsed ChatCompletionResponse
	if unmarshalErr := json.Unmarshal([]byte(rawJSON), &parsed); unmarshalErr != nil {
		return nil, ChatCompletionResponse{}, false
	}

	savedTokens := parsed.Usage.TotalTokens
	if savedTokens <= 0 {
		savedTokens = parsed.Usage.PromptTokens + parsed.Usage.CompletionTokens
	}
	if savedTokens <= 0 {
		savedTokens = len(rawJSON) / 4
	}
	_, _ = g.db.ExecContext(ctx, `
UPDATE ai_response_cache SET hits = hits + 1, tokens_saved = tokens_saved + ? WHERE cache_key = ?`,
		savedTokens, cacheKey,
	)
	return []byte(rawJSON), parsed, true
}

func (g *Gateway) storeResponseCache(ctx context.Context, cacheKey, model string, rawResp []byte) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = g.db.ExecContext(ctx, `
INSERT INTO ai_response_cache (cache_key, model, response_json, tokens_saved, hits, created_at)
VALUES (?, ?, ?, 0, 0, ?)
ON CONFLICT(cache_key) DO UPDATE SET
  model = excluded.model,
  response_json = excluded.response_json,
  created_at = excluded.created_at`,
		cacheKey, model, string(rawResp), now,
	)
}

func (g *Gateway) recordProviderError(ctx context.Context, providerID string, setCooldown bool) {
	if setCooldown {
		until := time.Now().UTC().Add(defaultCooldownDuration).Format(time.RFC3339)
		_, _ = g.db.ExecContext(ctx, `
UPDATE ai_providers SET err_count = err_count + 1, cooldown_until = ? WHERE id = ?`, until, providerID)
		return
	}
	_, _ = g.db.ExecContext(ctx, `UPDATE ai_providers SET err_count = err_count + 1 WHERE id = ?`, providerID)
}

func (g *Gateway) recordProviderSuccess(ctx context.Context, providerID string, promptTokens, completionTokens int) {
	_, _ = g.db.ExecContext(ctx, `
UPDATE ai_providers
SET req_count = req_count + 1,
    tokens_in = tokens_in + ?,
    tokens_out = tokens_out + ?,
    cooldown_until = ''
WHERE id = ?`, promptTokens, completionTokens, providerID)
}

func (g *Gateway) recordRequestLog(ctx context.Context, entry RequestLog) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	logID := makeGatewayID("log", entry.AgentID, entry.RequestedModel, now)
	_, _ = g.db.ExecContext(ctx, `
INSERT INTO ai_request_logs (
  id, agent_id, requested_model, resolved_provider, resolved_model,
  status_code, latency_ms, prompt_tokens, completion_tokens, rtk_tokens_saved, cache_hit, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		logID, entry.AgentID, entry.RequestedModel, entry.ResolvedProvider, entry.ResolvedModel,
		entry.StatusCode, entry.LatencyMS, entry.PromptTokens, entry.CompletionTokens,
		entry.RTKTokensSaved, boolToInt(entry.CacheHit), time.Now().UTC().Format(time.RFC3339),
	)
}

// --- Anthropic <-> OpenAI Protocol Translation ---

func anthropicToOpenAIRequest(anthReq AnthropicMessageRequest) ChatCompletionRequest {
	messages := make([]ChatMessage, 0, len(anthReq.Messages)+1)

	if systemText := extractAnthropicSystemText(anthReq.System); systemText != "" {
		messages = append(messages, ChatMessage{Role: "system", Content: systemText})
	}

	for _, msg := range anthReq.Messages {
		switch contentVal := msg.Content.(type) {
		case string:
			messages = append(messages, ChatMessage{Role: msg.Role, Content: contentVal})
		case []any:
			var textParts []string
			for _, rawBlock := range contentVal {
				blockMap, ok := rawBlock.(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := blockMap["type"].(string)
				switch blockType {
				case "tool_result":
					toolUseID, _ := blockMap["tool_use_id"].(string)
					toolText := flattenMessageContentToText(blockMap["content"])
					messages = append(messages, ChatMessage{
						Role:       "tool",
						ToolCallID: toolUseID,
						Content:    toolText,
					})
				case "text":
					if textStr, isStr := blockMap["text"].(string); isStr && textStr != "" {
						textParts = append(textParts, textStr)
					}
				case "tool_use":
					toolName, _ := blockMap["name"].(string)
					inputBytes, _ := json.Marshal(blockMap["input"])
					textParts = append(textParts, fmt.Sprintf("[tool_use %s: %s]", toolName, string(inputBytes)))
				}
			}
			if len(textParts) > 0 {
				messages = append(messages, ChatMessage{
					Role:    msg.Role,
					Content: strings.Join(textParts, "\n"),
				})
			}
		default:
			if flattened := flattenMessageContentToText(contentVal); flattened != "" {
				messages = append(messages, ChatMessage{Role: msg.Role, Content: flattened})
			}
		}
	}

	openAIReq := ChatCompletionRequest{
		Model:       anthReq.Model,
		Messages:    messages,
		Temperature: anthReq.Temperature,
		TopP:        anthReq.TopP,
		Stream:      anthReq.Stream,
	}
	if anthReq.MaxTokens > 0 {
		maxTok := anthReq.MaxTokens
		openAIReq.MaxTokens = &maxTok
	}
	if len(anthReq.StopSequences) > 0 {
		stopRaw, _ := json.Marshal(anthReq.StopSequences)
		openAIReq.Stop = stopRaw
	}
	return openAIReq
}

func openAIToAnthropicRequest(openAIReq ChatCompletionRequest) AnthropicMessageRequest {
	var systemParts []string
	anthMsgs := make([]AnthropicMessage, 0, len(openAIReq.Messages))

	for _, msg := range openAIReq.Messages {
		textVal := flattenMessageContentToText(msg.Content)
		switch strings.ToLower(msg.Role) {
		case "system":
			if textVal != "" {
				systemParts = append(systemParts, textVal)
			}
		case "tool":
			anthMsgs = append(anthMsgs, AnthropicMessage{
				Role: "user",
				Content: []map[string]any{
					{
						"type":        "tool_result",
						"tool_use_id": msg.ToolCallID,
						"content":     textVal,
					},
				},
			})
		case "assistant":
			anthMsgs = append(anthMsgs, AnthropicMessage{Role: "assistant", Content: textVal})
		default:
			anthMsgs = append(anthMsgs, AnthropicMessage{Role: "user", Content: textVal})
		}
	}

	maxTok := 4096
	if openAIReq.MaxTokens != nil && *openAIReq.MaxTokens > 0 {
		maxTok = *openAIReq.MaxTokens
	}
	anthReq := AnthropicMessageRequest{
		Model:       openAIReq.Model,
		Messages:    anthMsgs,
		MaxTokens:   maxTok,
		Temperature: openAIReq.Temperature,
		TopP:        openAIReq.TopP,
	}
	if len(systemParts) > 0 {
		anthReq.System = strings.Join(systemParts, "\n")
	}
	return anthReq
}

func openAIToAnthropicResponse(openAIResp ChatCompletionResponse) AnthropicMessageResponse {
	assistantText := ""
	stopReason := "end_turn"
	if len(openAIResp.Choices) > 0 {
		choice := openAIResp.Choices[0]
		assistantText = flattenMessageContentToText(choice.Message.Content)
		switch choice.FinishReason {
		case "", "stop":
			stopReason = "end_turn"
		case "length":
			stopReason = "max_tokens"
		case "tool_calls":
			stopReason = "tool_use"
		default:
			stopReason = choice.FinishReason
		}
	}

	msgID := openAIResp.ID
	if msgID == "" {
		msgID = makeGatewayID("msg", openAIResp.Model, time.Now().UTC().Format(time.RFC3339Nano))
	} else if !strings.HasPrefix(msgID, "msg_") {
		msgID = "msg_" + strings.TrimPrefix(msgID, "chatcmpl-")
	}

	return AnthropicMessageResponse{
		ID:         msgID,
		Type:       "message",
		Role:       "assistant",
		Model:      openAIResp.Model,
		Content:    []AnthropicContentBlock{{Type: "text", Text: assistantText}},
		StopReason: stopReason,
		Usage: AnthropicUsage{
			InputTokens:  openAIResp.Usage.PromptTokens,
			OutputTokens: openAIResp.Usage.CompletionTokens,
		},
	}
}

func anthropicToOpenAIResponse(anthResp AnthropicMessageResponse, fallbackModel string) ChatCompletionResponse {
	var textParts []string
	for _, block := range anthResp.Content {
		if block.Text != "" {
			textParts = append(textParts, block.Text)
		}
	}
	modelName := anthResp.Model
	if modelName == "" {
		modelName = fallbackModel
	}
	finishReason := "stop"
	switch anthResp.StopReason {
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		finishReason = "tool_calls"
	}

	return ChatCompletionResponse{
		ID:      "chatcmpl-" + strings.TrimPrefix(anthResp.ID, "msg_"),
		Object:  "chat.completion",
		Created: time.Now().UTC().Unix(),
		Model:   modelName,
		Choices: []ChatCompletionChoice{
			{
				Index:        0,
				Message:      ChatMessage{Role: "assistant", Content: strings.Join(textParts, "\n")},
				FinishReason: finishReason,
			},
		},
		Usage: ChatCompletionUsage{
			PromptTokens:     anthResp.Usage.InputTokens,
			CompletionTokens: anthResp.Usage.OutputTokens,
			TotalTokens:      anthResp.Usage.InputTokens + anthResp.Usage.OutputTokens,
		},
	}
}

func extractAnthropicSystemText(systemField any) string {
	switch typed := systemField.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, rawBlock := range typed {
			if blockMap, ok := rawBlock.(map[string]any); ok {
				if textStr, isStr := blockMap["text"].(string); isStr && strings.TrimSpace(textStr) != "" {
					parts = append(parts, textStr)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func flattenMessageContentToText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, rawBlock := range typed {
			switch blockVal := rawBlock.(type) {
			case string:
				parts = append(parts, blockVal)
			case map[string]any:
				if textStr, ok := blockVal["text"].(string); ok && textStr != "" {
					parts = append(parts, textStr)
				} else if nestedContent, exists := blockVal["content"]; exists {
					if nestedText := flattenMessageContentToText(nestedContent); nestedText != "" {
						parts = append(parts, nestedText)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		if typed == nil {
			return ""
		}
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	}
}

// --- Echo HTTP Handlers ---

func (g *Gateway) handleListOpenAIModels(c echo.Context) error {
	ctx := c.Request().Context()
	nowUnix := time.Now().UTC().Unix()
	seen := make(map[string]bool)
	modelList := make([]OpenAIModelObject, 0)

	appendUnique := func(modelID, owner string) {
		cleanID := strings.TrimSpace(modelID)
		if cleanID == "" || seen[cleanID] {
			return
		}
		seen[cleanID] = true
		modelList = append(modelList, OpenAIModelObject{
			ID:      cleanID,
			Object:  "model",
			Created: nowUnix,
			OwnedBy: owner,
		})
	}

	if routes, err := g.ListAgentRoutes(ctx); err == nil {
		for _, route := range routes {
			appendUnique("agent/"+route.AgentID, "mw-echo-agent")
		}
	}

	if providers, err := g.ListProviders(ctx); err == nil {
		for _, provider := range providers {
			if !provider.Enabled {
				continue
			}
			owner := "provider:" + provider.Name
			if provider.DefaultModel != "" {
				appendUnique(provider.DefaultModel, owner)
			}
			for _, declared := range provider.Models {
				appendUnique(declared, owner)
			}
		}
	}

	if catalogEntries, err := g.ListCatalog(ctx, "", true); err == nil {
		for _, entry := range catalogEntries {
			appendUnique(entry.ModelID, "catalog:"+entry.ProviderSlug)
		}
	}

	return c.JSON(http.StatusOK, OpenAIModelList{
		Object: "list",
		Models: modelList,
	})
}

func resolveRTKMode(c echo.Context) string {
	if mode := strings.TrimSpace(c.Request().Header.Get(headerRTKMode)); mode != "" {
		return strings.ToLower(mode)
	}
	return strings.ToLower(strings.TrimSpace(c.QueryParam("rtk")))
}

func (g *Gateway) handleChatCompletions(c echo.Context) error {
	var completionReq ChatCompletionRequest
	if err := c.Bind(&completionReq); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "invalid chat completion JSON", "type": "invalid_request_error"},
		})
	}
	if len(completionReq.Messages) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "messages array is required", "type": "invalid_request_error"},
		})
	}

	agentHeader := c.Request().Header.Get(headerAgentID)
	rtkMode := resolveRTKMode(c)
	if completionReq.Stream {
		return g.handleStreamingChatCompletion(c, agentHeader, rtkMode, completionReq)
	}

	outcome, err := g.routeCompletion(c.Request().Context(), agentHeader, "default", rtkMode, completionReq)
	if err != nil {
		return writeGatewayError(c, err)
	}

	setGatewayResponseHeaders(c, outcome)
	return c.Blob(http.StatusOK, "application/json; charset=utf-8", outcome.RawBody)
}

func (g *Gateway) handleStreamingChatCompletion(c echo.Context, agentHeader, rtkMode string,
	incomingReq ChatCompletionRequest) error {
	ctx := c.Request().Context()
	startedAt := time.Now()
	requestedModel := strings.TrimSpace(incomingReq.Model)
	agentID, effectiveModel, tierOrder, rtkActive := g.resolveRoutePlan(ctx, agentHeader, "default", requestedModel)

	outboundReq := incomingReq
	outboundReq.Model = effectiveModel
	rtkTokensSaved := 0
	if strings.EqualFold(strings.TrimSpace(rtkMode), "caveman") {
		compressedMsgs, saved := CompressCaveman(outboundReq.Messages)
		outboundReq.Messages = compressedMsgs
		rtkTokensSaved = saved
	} else if rtkActive {
		compressedMsgs, saved := CompressMessages(outboundReq.Messages)
		outboundReq.Messages = compressedMsgs
		rtkTokensSaved = saved
	}

	candidates, err := g.selectCandidateProviders(ctx, effectiveModel, tierOrder)
	if err != nil {
		return writeGatewayError(c, err)
	}
	if len(candidates) == 0 {
		return writeGatewayError(c, &upstreamCallError{
			StatusCode: http.StatusServiceUnavailable,
			Body:       "no healthy AI providers available",
		})
	}

	var lastErr error
	for _, candidate := range candidates {
		if candidate.Kind != KindOpenAICompat && !(candidate.Kind == KindGemini &&
			strings.Contains(strings.ToLower(candidate.BaseURL), "/openai")) {
			continue
		}
		resolvedModel := pickProviderModel(candidate, effectiveModel)
		attemptReq := outboundReq
		attemptReq.Model = resolvedModel

		apiKeyPlain := ""
		if candidate.APIKeyEnc != "" {
			plainBytes, decErr := g.box.Decrypt(candidate.APIKeyEnc)
			if decErr != nil {
				lastErr = decErr
				g.recordProviderError(ctx, candidate.ID, false)
				continue
			}
			apiKeyPlain = string(plainBytes)
		}

		endpoint := strings.TrimRight(candidate.BaseURL, "/")
		if !strings.HasSuffix(endpoint, "/chat/completions") {
			endpoint += "/chat/completions"
		}
		reqBodyBytes, _ := json.Marshal(attemptReq)
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBodyBytes))
		if reqErr != nil {
			lastErr = reqErr
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if apiKeyPlain != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKeyPlain)
		}

		httpResp, doErr := g.httpClient.Do(httpReq)
		if doErr != nil {
			lastErr = &upstreamCallError{StatusCode: http.StatusBadGateway, Body: doErr.Error(), Retryable: true}
			g.recordProviderError(ctx, candidate.ID, false)
			continue
		}

		if httpResp.StatusCode == http.StatusTooManyRequests {
			bodyBytes, _ := io.ReadAll(io.LimitReader(httpResp.Body, 64*1024))
			_ = httpResp.Body.Close()
			lastErr = &upstreamCallError{StatusCode: httpResp.StatusCode, Body: string(bodyBytes), Retryable: true,
				Cooldown: true}
			g.recordProviderError(ctx, candidate.ID, true)
			continue
		}
		if httpResp.StatusCode >= 500 || httpResp.StatusCode == http.StatusUnauthorized ||
			httpResp.StatusCode == http.StatusForbidden {
			bodyBytes, _ := io.ReadAll(io.LimitReader(httpResp.Body, 64*1024))
			_ = httpResp.Body.Close()
			lastErr = &upstreamCallError{StatusCode: httpResp.StatusCode, Body: string(bodyBytes), Retryable: true}
			g.recordProviderError(ctx, candidate.ID, false)
			continue
		}
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			bodyBytes, _ := io.ReadAll(io.LimitReader(httpResp.Body, 64*1024))
			_ = httpResp.Body.Close()
			return writeGatewayError(c, &upstreamCallError{StatusCode: httpResp.StatusCode, Body: string(bodyBytes)})
		}

		defer httpResp.Body.Close()
		contentType := httpResp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "text/event-stream"
		}
		c.Response().Header().Set("Content-Type", contentType)
		c.Response().Header().Set(headerCacheState, "BYPASS")
		c.Response().Header().Set(headerProvider, candidate.Name)
		c.Response().Header().Set(headerModel, resolvedModel)
		c.Response().Header().Set(headerAgentID, agentID)
		c.Response().Header().Set(headerRTKSaved, strconv.Itoa(rtkTokensSaved))
		c.Response().WriteHeader(http.StatusOK)

		_, _ = io.Copy(c.Response().Writer, httpResp.Body)
		if flusher, ok := c.Response().Writer.(http.Flusher); ok {
			flusher.Flush()
		}

		g.recordProviderSuccess(ctx, candidate.ID, 0, 0)
		g.recordRequestLog(ctx, RequestLog{
			AgentID:          agentID,
			RequestedModel:   requestedModel,
			ResolvedProvider: candidate.Name,
			ResolvedModel:    resolvedModel,
			StatusCode:       http.StatusOK,
			LatencyMS:        time.Since(startedAt).Milliseconds(),
			RTKTokensSaved:   rtkTokensSaved,
			CacheHit:         false,
		})
		return nil
	}

	if lastErr == nil {
		lastErr = &upstreamCallError{StatusCode: http.StatusBadGateway, Body: "no streaming-capable provider succeeded"}
	}
	return writeGatewayError(c, lastErr)
}

func (g *Gateway) handleAnthropicMessages(c echo.Context) error {
	var anthReq AnthropicMessageRequest
	if err := c.Bind(&anthReq); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"type":  "error",
			"error": map[string]string{"type": "invalid_request_error", "message": "invalid anthropic messages JSON"},
		})
	}
	if len(anthReq.Messages) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"type":  "error",
			"error": map[string]string{"type": "invalid_request_error", "message": "messages array is required"},
		})
	}

	openAIReq := anthropicToOpenAIRequest(anthReq)
	agentHeader := c.Request().Header.Get(headerAgentID)
	rtkMode := resolveRTKMode(c)
	outcome, err := g.routeCompletion(c.Request().Context(), agentHeader, "claude-code", rtkMode, openAIReq)
	if err != nil {
		statusCode := http.StatusBadGateway
		var upErr *upstreamCallError
		if errors.As(err, &upErr) && upErr.StatusCode > 0 {
			statusCode = upErr.StatusCode
		}
		return c.JSON(statusCode, map[string]any{
			"type":  "error",
			"error": map[string]string{"type": "api_error", "message": err.Error()},
		})
	}

	setGatewayResponseHeaders(c, outcome)
	return c.JSON(http.StatusOK, openAIToAnthropicResponse(outcome.Response))
}

func setGatewayResponseHeaders(c echo.Context, outcome routeOutcome) {
	cacheState := "MISS"
	if outcome.CacheHit {
		cacheState = "HIT"
	}
	c.Response().Header().Set(headerCacheState, cacheState)
	c.Response().Header().Set(headerProvider, outcome.ResolvedProvider)
	c.Response().Header().Set(headerModel, outcome.ResolvedModel)
	c.Response().Header().Set(headerAgentID, outcome.AgentID)
	c.Response().Header().Set(headerRTKSaved, strconv.Itoa(outcome.RTKTokensSaved))
}

func writeGatewayError(c echo.Context, err error) error {
	statusCode := http.StatusBadGateway
	var upErr *upstreamCallError
	if errors.As(err, &upErr) && upErr.StatusCode > 0 {
		statusCode = upErr.StatusCode
	}
	return c.JSON(statusCode, map[string]any{
		"error": map[string]string{
			"message": err.Error(),
			"type":    "gateway_error",
		},
	})
}

func (g *Gateway) handleGetStats(c echo.Context) error {
	stats, err := g.Stats(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, stats)
}

func (g *Gateway) handleListProviders(c echo.Context) error {
	providers, err := g.ListProviders(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"providers": providers, "count": len(providers)})
}

func (g *Gateway) handleCreateProvider(c echo.Context) error {
	var input ProviderCreateInput
	if err := c.Bind(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid provider JSON"})
	}
	created, err := g.AddProvider(c.Request().Context(), input)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, created)
}

func (g *Gateway) handleDeleteProvider(c echo.Context) error {
	err := g.DeleteProvider(c.Request().Context(), c.Param("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "provider not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
}

func (g *Gateway) handleListAgents(c echo.Context) error {
	routes, err := g.ListAgentRoutes(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"agents": routes, "count": len(routes)})
}

func (g *Gateway) handleUpdateAgent(c echo.Context) error {
	var input AgentRouteUpdateInput
	if err := c.Bind(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid agent route JSON"})
	}
	updated, err := g.UpdateAgentRoute(c.Request().Context(), c.Param("agentId"), input)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, updated)
}

func (g *Gateway) handleListCatalog(c echo.Context) error {
	freeOnly := strings.EqualFold(c.QueryParam("freeOnly"), "true") || c.QueryParam("freeOnly") == "1"
	entries, err := g.ListCatalog(c.Request().Context(), c.QueryParam("q"), freeOnly)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}

func (g *Gateway) handleSniffCatalog(c echo.Context) error {
	sniffOut, err := g.SniffUpstream(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, sniffOut)
}

func (g *Gateway) handleUpdateSettings(c echo.Context) error {
	var input SettingsUpdateInput
	if err := c.Bind(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid settings JSON"})
	}
	updated, err := g.UpdateSettings(c.Request().Context(), input)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, updated)
}

func (g *Gateway) handlePurgeCache(c echo.Context) error {
	purged, err := g.PurgeCache(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]int64{"purged": purged})
}
