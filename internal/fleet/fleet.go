package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type fleetServiceSpec struct {
	Key       string
	Name      string
	EnvVar    string
	DefaultURL string
}

var defaultFleetSpecs = []fleetServiceSpec{
	{Key: "mw-lab", Name: "MW Lab", EnvVar: "MW_LAB_URL"},
	{Key: "mw-mission", Name: "MW Mission", EnvVar: "MW_MISSION_URL"},
	{Key: "mw-dock", Name: "MW Dock", EnvVar: "MW_DOCK_URL"},
	{Key: "mw-design", Name: "MW Design", EnvVar: "MW_DESIGN_URL"},
	{Key: "mw-isp", Name: "MW ISP", EnvVar: "MW_ISP_URL"},
	{Key: "mw-cp", Name: "MW Control Plane", EnvVar: "MW_CP_URL"},
	{Key: "mw-corpus", Name: "MW Corpus", EnvVar: "MW_CORPUS_URL"},
	{Key: "mw-nexus", Name: "MW Nexus", EnvVar: "MW_NEXUS_URL"},
	{Key: "mw-lore", Name: "MW Lore", EnvVar: "MW_LORE_URL"},
	{Key: "mw-mylove", Name: "MW MyLove", EnvVar: "MW_MYLOVE_URL"},
	{Key: "mw-studio", Name: "MW Studio", EnvVar: "MW_STUDIO_URL"},
	{Key: "mw-rid", Name: "MW RID (R!OTS Sovereign Identity Provider)", EnvVar: "MW_RID_URL"},
}

// ServiceConfig describes a configured sovereign fleet service endpoint.
type ServiceConfig struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	EnvVar    string `json:"envVar"`
	URL       string `json:"url"`
	Source    string `json:"source"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// ServiceProbeStatus captures a single /health probe outcome.
type ServiceProbeStatus struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	EnvVar     string `json:"envVar"`
	URL        string `json:"url"`
	Online     bool   `json:"online"`
	StatusCode int    `json:"statusCode"`
	LatencyMs  int64  `json:"latencyMs"`
	Error      string `json:"error,omitempty"`
	CheckedAt  string `json:"checkedAt"`
}

// FleetProbeReport summarizes concurrent health checks across the fleet.
type FleetProbeReport struct {
	Services    []ServiceProbeStatus `json:"services"`
	OnlineCount int                  `json:"onlineCount"`
	TotalCount  int                  `json:"totalCount"`
	CheckedAt   string               `json:"checkedAt"`
}

// ListServices returns the merged view of environment-configured and SQLite-overridden fleet services.
func (m *Manager) ListServices(ctx context.Context) ([]ServiceConfig, error) {
	byKey := make(map[string]ServiceConfig, len(defaultFleetSpecs))
	for _, spec := range defaultFleetSpecs {
		envURL := strings.TrimRight(strings.TrimSpace(os.Getenv(spec.EnvVar)), "/")
		source := "default"
		if envURL != "" {
			source = "env"
		}
		byKey[spec.Key] = ServiceConfig{
			Key:    spec.Key,
			Name:   spec.Name,
			EnvVar: spec.EnvVar,
			URL:    envURL,
			Source: source,
		}
	}

	rows, err := m.hub.DB().QueryContext(ctx, `
SELECT key, name, env_var, url, updated_at
FROM fleet_services
ORDER BY key ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list fleet service overrides: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key, name, envVar, rawURL, updatedAt string
		if err := rows.Scan(&key, &name, &envVar, &rawURL, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan fleet service override: %w", err)
		}
		existing, exists := byKey[key]
		if exists {
			if strings.TrimSpace(name) != "" {
				existing.Name = name
			}
			if strings.TrimSpace(envVar) != "" {
				existing.EnvVar = envVar
			}
			existing.URL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
			existing.Source = "sqlite"
			existing.UpdatedAt = updatedAt
			byKey[key] = existing
		} else {
			byKey[key] = ServiceConfig{
				Key:       key,
				Name:      name,
				EnvVar:    envVar,
				URL:       strings.TrimRight(strings.TrimSpace(rawURL), "/"),
				Source:    "sqlite",
				UpdatedAt: updatedAt,
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ordered := make([]ServiceConfig, 0, len(byKey))
	for _, cfg := range byKey {
		ordered = append(ordered, cfg)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Key < ordered[j].Key
	})
	return ordered, nil
}

// UpsertService persists a SQLite URL override for a fleet service.
func (m *Manager) UpsertService(ctx context.Context, key, name, rawURL string) (ServiceConfig, error) {
	cleanKey := strings.ToLower(strings.TrimSpace(key))
	if cleanKey == "" {
		return ServiceConfig{}, errors.New("service key is required")
	}
	cleanName := strings.TrimSpace(name)
	envVar := ""
	for _, spec := range defaultFleetSpecs {
		if spec.Key == cleanKey {
			if cleanName == "" {
				cleanName = spec.Name
			}
			envVar = spec.EnvVar
			break
		}
	}
	if cleanName == "" {
		cleanName = cleanKey
	}
	cleanURL := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	updatedAt := m.nowFn().Format(time.RFC3339)

	_, err := m.hub.DB().ExecContext(ctx, `
INSERT INTO fleet_services (key, name, env_var, url, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
  name = excluded.name,
  env_var = CASE WHEN excluded.env_var != '' THEN excluded.env_var ELSE fleet_services.env_var END,
  url = excluded.url,
  updated_at = excluded.updated_at
`, cleanKey, cleanName, envVar, cleanURL, updatedAt)
	if err != nil {
		return ServiceConfig{}, fmt.Errorf("upsert fleet service: %w", err)
	}

	return ServiceConfig{
		Key:       cleanKey,
		Name:      cleanName,
		EnvVar:    envVar,
		URL:       cleanURL,
		Source:    "sqlite",
		UpdatedAt: updatedAt,
	}, nil
}

// ProbeFleet concurrently probes <url>/health for every registered fleet service with a 1500ms timeout.
func (m *Manager) ProbeFleet(ctx context.Context) (FleetProbeReport, error) {
	services, err := m.ListServices(ctx)
	if err != nil {
		return FleetProbeReport{}, err
	}

	statuses := make([]ServiceProbeStatus, len(services))
	var waitGroup sync.WaitGroup

	for idx, svc := range services {
		waitGroup.Add(1)
		go func(index int, target ServiceConfig) {
			defer waitGroup.Done()
			statuses[index] = m.probeSingleService(ctx, target)
		}(idx, svc)
	}
	waitGroup.Wait()

	onlineCount := 0
	for _, status := range statuses {
		if status.Online {
			onlineCount++
		}
	}

	return FleetProbeReport{
		Services:    statuses,
		OnlineCount: onlineCount,
		TotalCount:  len(statuses),
		CheckedAt:   m.nowFn().Format(time.RFC3339),
	}, nil
}

func (m *Manager) probeSingleService(ctx context.Context, target ServiceConfig) ServiceProbeStatus {
	checkedAt := m.nowFn().Format(time.RFC3339)
	status := ServiceProbeStatus{
		Key:       target.Key,
		Name:      target.Name,
		EnvVar:    target.EnvVar,
		URL:       target.URL,
		Online:    false,
		CheckedAt: checkedAt,
	}
	if strings.TrimSpace(target.URL) == "" {
		status.Error = "url not configured"
		return status
	}

	probeCtx, cancel := context.WithTimeout(ctx, m.probeLimit)
	defer cancel()

	healthURL := strings.TrimRight(target.URL, "/") + "/health"
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, healthURL, nil)
	if err != nil {
		status.Error = err.Error()
		return status
	}

	started := time.Now()
	resp, err := m.httpClient.Do(req)
	status.LatencyMs = time.Since(started).Milliseconds()
	if err != nil {
		status.Error = err.Error()
		return status
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	status.StatusCode = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		status.Online = true
	} else {
		status.Error = fmt.Sprintf("http %d", resp.StatusCode)
	}
	return status
}
