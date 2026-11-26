package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultLocalAITimeout   = 800 * time.Millisecond
	maxLocalAIResponseBytes = 2 << 20 // 2 MiB
)

var devModelPattern = regexp.MustCompile(`(?i)coder|code|deepseek|starcoder|codellama|devstral|qwen.*coder`)

// LocalAITarget defines a local inference provider endpoint to probe.
type LocalAITarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // "ollama", "lmstudio", "vllm"
	Endpoint string `json:"endpoint"`
}

// LocalAIModel represents a model discovered on a loopback local AI runtime.
type LocalAIModel struct {
	ID           string `json:"id"`
	ProviderID   string `json:"providerId"`
	ProviderName string `json:"providerName"`
	ProviderKind string `json:"providerKind"`
	Endpoint     string `json:"endpoint"`
	SizeBytes    int64  `json:"sizeBytes,omitempty"`
	Family       string `json:"family,omitempty"`
}

// LocalAIProviderStatus captures the probe result for a single local AI endpoint.
type LocalAIProviderStatus struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Endpoint  string         `json:"endpoint"`
	Online    bool           `json:"online"`
	LatencyMs int64          `json:"latencyMs"`
	Models    []LocalAIModel `json:"models"`
	Error     string         `json:"error,omitempty"`
}

// LocalAIDiscoveryResult summarizes all probed local AI providers and recommended models.
type LocalAIDiscoveryResult struct {
	OnlineCount               int                     `json:"onlineCount"`
	Providers                 []LocalAIProviderStatus `json:"providers"`
	Models                    []LocalAIModel          `json:"models"`
	RecommendedWorkspaceModel string                  `json:"recommendedWorkspaceModel"`
	RecommendedDevModel       string                  `json:"recommendedDevModel"`
}

// DefaultLocalAITargets returns the canonical loopback probe targets for Ollama, LM Studio, and vLLM.
func DefaultLocalAITargets() []LocalAITarget {
	return []LocalAITarget{
		{
			ID:       "ollama",
			Name:     "Ollama",
			Kind:     "ollama",
			Endpoint: "http://127.0.0.1:11434/api/tags",
		},
		{
			ID:       "lmstudio",
			Name:     "LM Studio",
			Kind:     "lmstudio",
			Endpoint: "http://127.0.0.1:1234/v1/models",
		},
		{
			ID:       "vllm",
			Name:     "vLLM / OpenAI-Compatible",
			Kind:     "vllm",
			Endpoint: "http://127.0.0.1:8000/v1/models",
		},
	}
}

// IsLoopbackHostname returns true strictly for 127.0.0.1, localhost, and ::1.
func IsLoopbackHostname(hostname string) bool {
	cleaned := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	cleaned = strings.TrimPrefix(cleaned, "[")
	cleaned = strings.TrimSuffix(cleaned, "]")
	if cleaned == "" {
		return false
	}
	switch cleaned {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	parsedIP := net.ParseIP(cleaned)
	if parsedIP == nil {
		return false
	}
	if v4 := parsedIP.To4(); v4 != nil {
		return v4.Equal(net.IPv4(127, 0, 0, 1))
	}
	return parsedIP.Equal(net.IPv6loopback)
}

// ValidateLoopbackEndpointURL strictly validates that rawURL is an http/https loopback endpoint without userinfo.
func ValidateLoopbackEndpointURL(rawURL string) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, errors.New("empty endpoint URL")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("rejected non-http(s) endpoint scheme %q", parsed.Scheme)
	}
	if parsed.User != nil {
		return nil, errors.New("rejected endpoint URL containing userinfo")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("rejected endpoint URL containing fragment")
	}
	if !IsLoopbackHostname(parsed.Hostname()) {
		return nil, fmt.Errorf("rejected non-loopback endpoint host %q", parsed.Hostname())
	}
	return parsed, nil
}

// IsLoopbackEndpointURL reports whether rawURL passes the strict loopback-only endpoint guard.
func IsLoopbackEndpointURL(rawURL string) bool {
	_, err := ValidateLoopbackEndpointURL(rawURL)
	return err == nil
}

func newLoopbackHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				return nil, fmt.Errorf("invalid target address %q: %w", addr, splitErr)
			}
			if !IsLoopbackHostname(host) {
				return nil, fmt.Errorf("blocked non-loopback dial target %q", host)
			}
			// Resolve and enforce loopback IP even for "localhost" to block DNS rebinding.
			ips, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
			if lookupErr != nil {
				return nil, lookupErr
			}
			for _, ipAddr := range ips {
				if !ipAddr.IP.IsLoopback() {
					return nil, fmt.Errorf("blocked non-loopback resolved IP %s for %q", ipAddr.IP.String(), host)
				}
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// DiscoverLocalAI concurrently probes loopback local AI targets with an 800ms timeout.
func DiscoverLocalAI(ctx context.Context, targets []LocalAITarget) LocalAIDiscoveryResult {
	return discoverLocalAIWithClient(ctx, targets, nil, defaultLocalAITimeout)
}

// DiscoverLocalAI concurrently probes loopback local AI targets using the Manager's configured HTTP client and timeout.
func (m *Manager) DiscoverLocalAI(ctx context.Context, targets []LocalAITarget) LocalAIDiscoveryResult {
	return discoverLocalAIWithClient(ctx, targets, m.httpClient, m.localAITimeout)
}

func discoverLocalAIWithClient(
	ctx context.Context,
	targets []LocalAITarget,
	customClient *http.Client,
	timeout time.Duration,
) LocalAIDiscoveryResult {
	if len(targets) == 0 {
		targets = DefaultLocalAITargets()
	}
	if timeout <= 0 {
		timeout = defaultLocalAITimeout
	}
	client := customClient
	if client == nil {
		client = newLoopbackHTTPClient(timeout)
	}

	statuses := make([]LocalAIProviderStatus, len(targets))
	var waitGroup sync.WaitGroup
	for idx, target := range targets {
		waitGroup.Add(1)
		go func(slot int, probeTarget LocalAITarget) {
			defer waitGroup.Done()
			statuses[slot] = probeLocalAITarget(ctx, probeTarget, timeout, client)
		}(idx, target)
	}
	waitGroup.Wait()

	onlineCount := 0
	allModels := make([]LocalAIModel, 0)
	for _, status := range statuses {
		if status.Online {
			onlineCount++
		}
		allModels = append(allModels, status.Models...)
	}

	workspaceModel, devModel := selectRecommendedModels(allModels)
	return LocalAIDiscoveryResult{
		OnlineCount:               onlineCount,
		Providers:                 statuses,
		Models:                    allModels,
		RecommendedWorkspaceModel: workspaceModel,
		RecommendedDevModel:       devModel,
	}
}

func probeLocalAITarget(
	ctx context.Context,
	target LocalAITarget,
	timeout time.Duration,
	client *http.Client,
) LocalAIProviderStatus {
	status := LocalAIProviderStatus{
		ID:       target.ID,
		Name:     target.Name,
		Kind:     target.Kind,
		Endpoint: target.Endpoint,
		Models:   []LocalAIModel{},
	}

	if _, guardErr := ValidateLoopbackEndpointURL(target.Endpoint); guardErr != nil {
		status.Error = guardErr.Error()
		return status
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, reqErr := http.NewRequestWithContext(probeCtx, http.MethodGet, target.Endpoint, nil)
	if reqErr != nil {
		status.Error = reqErr.Error()
		return status
	}
	req.Header.Set("Accept", "application/json")

	startedAt := time.Now()
	resp, doErr := client.Do(req)
	status.LatencyMs = time.Since(startedAt).Milliseconds()
	if doErr != nil {
		status.Error = doErr.Error()
		return status
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return status
	}

	rawBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxLocalAIResponseBytes))
	if readErr != nil {
		status.Error = readErr.Error()
		return status
	}

	var parsedModels []LocalAIModel
	if strings.EqualFold(target.Kind, "ollama") {
		parsedModels = parseOllamaModels(rawBody, target)
	} else {
		parsedModels = parseOpenAICompatModels(rawBody, target)
	}

	status.Online = true
	status.Models = parsedModels
	return status
}

func parseOllamaModels(rawBody []byte, target LocalAITarget) []LocalAIModel {
	var payload struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Size    int64  `json:"size"`
			Details struct {
				Family string `json:"family"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return []LocalAIModel{}
	}

	models := make([]LocalAIModel, 0, len(payload.Models))
	for _, entry := range payload.Models {
		modelID := strings.TrimSpace(entry.Name)
		if modelID == "" {
			modelID = strings.TrimSpace(entry.Model)
		}
		if modelID == "" {
			continue
		}
		models = append(models, LocalAIModel{
			ID:           modelID,
			ProviderID:   target.ID,
			ProviderName: target.Name,
			ProviderKind: target.Kind,
			Endpoint:     target.Endpoint,
			SizeBytes:    entry.Size,
			Family:       strings.TrimSpace(entry.Details.Family),
		})
	}
	return models
}

func parseOpenAICompatModels(rawBody []byte, target LocalAITarget) []LocalAIModel {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return []LocalAIModel{}
	}

	models := make([]LocalAIModel, 0, len(payload.Data))
	for _, entry := range payload.Data {
		modelID := strings.TrimSpace(entry.ID)
		if modelID == "" {
			continue
		}
		models = append(models, LocalAIModel{
			ID:           modelID,
			ProviderID:   target.ID,
			ProviderName: target.Name,
			ProviderKind: target.Kind,
			Endpoint:     target.Endpoint,
		})
	}
	return models
}

func selectRecommendedModels(models []LocalAIModel) (workspaceModel string, devModel string) {
	if len(models) == 0 {
		return "", ""
	}
	for _, model := range models {
		if devModel == "" && devModelPattern.MatchString(model.ID) {
			devModel = model.ID
		}
		if workspaceModel == "" && !devModelPattern.MatchString(model.ID) {
			workspaceModel = model.ID
		}
		if devModel != "" && workspaceModel != "" {
			break
		}
	}
	if devModel == "" {
		devModel = models[0].ID
	}
	if workspaceModel == "" {
		workspaceModel = models[0].ID
	}
	return workspaceModel, devModel
}
