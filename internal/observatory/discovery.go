package observatory

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// ServiceProbe describes a localhost port + protocol probe signature.
type ServiceProbe struct {
	Port      int    `json:"port"`
	Name      string `json:"name"`
	Category  string `json:"category"`
	Protocol  string `json:"protocol"`
	HealthURI string `json:"healthUri,omitempty"`
}

// DiscoveredService describes a live service found listening on the local host.
type DiscoveredService struct {
	Port       int    `json:"port"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Protocol   string `json:"protocol"`
	Endpoint   string `json:"endpoint"`
	Status     string `json:"status"`
	LatencyMs  int64  `json:"latencyMs"`
	Detail     string `json:"detail,omitempty"`
	DetectedAt string `json:"detectedAt"`
}

var defaultProbes = []ServiceProbe{
	// AI Runtimes & Gateways
	{Port: 11434, Name: "Ollama", Category: "ai", Protocol: "http+openai", HealthURI: "/api/tags"},
	{Port: 1234, Name: "LM Studio", Category: "ai", Protocol: "http+openai", HealthURI: "/v1/models"},
	{Port: 8000, Name: "vLLM / FastAPI AI", Category: "ai", Protocol: "http+openai", HealthURI: "/v1/models"},
	{Port: 8080, Name: "llama.cpp / LocalAI", Category: "ai", Protocol: "http+openai", HealthURI: "/health"},
	{Port: 4000, Name: "LiteLLM / Bifrost", Category: "ai", Protocol: "http+openai", HealthURI: "/health"},
	{Port: 4310, Name: "mw-echo Hub", Category: "ai", Protocol: "http+echo+mcp+otlp", HealthURI: "/health"},

	// Observability
	{Port: 4317, Name: "OTLP gRPC Collector", Category: "observability", Protocol: "otlp-grpc"},
	{Port: 4318, Name: "OTLP HTTP Collector", Category: "observability", Protocol: "otlp-http", HealthURI: "/"},
	{Port: 9090, Name: "Prometheus", Category: "observability", Protocol: "http+promql", HealthURI: "/-/healthy"},
	{Port: 3000, Name: "Grafana / Web App", Category: "observability", Protocol: "http", HealthURI: "/api/health"},
	{Port: 16686, Name: "Jaeger UI", Category: "observability", Protocol: "http+otlp", HealthURI: "/"},

	// Data & Storage
	{Port: 5432, Name: "PostgreSQL", Category: "storage", Protocol: "tcp+postgres"},
	{Port: 6379, Name: "Redis / Valkey", Category: "storage", Protocol: "tcp+resp"},
	{Port: 9000, Name: "MinIO S3", Category: "storage", Protocol: "http+s3", HealthURI: "/minio/health/live"},
	{Port: 3900, Name: "Garage S3", Category: "storage", Protocol: "http+s3", HealthURI: "/health"},
	{Port: 8123, Name: "ClickHouse HTTP", Category: "storage", Protocol: "http", HealthURI: "/ping"},
	{Port: 6333, Name: "Qdrant Vector DB", Category: "storage", Protocol: "http+qdrant", HealthURI: "/healthz"},
	{Port: 7700, Name: "Meilisearch", Category: "storage", Protocol: "http", HealthURI: "/health"},

	// Local Dev Servers
	{Port: 5173, Name: "Vite Dev Server", Category: "dev", Protocol: "http+vite", HealthURI: "/"},
	{Port: 3001, Name: "Dev Web Server (3001)", Category: "dev", Protocol: "http", HealthURI: "/"},
	{Port: 3002, Name: "Dev Web Server (3002)", Category: "dev", Protocol: "http", HealthURI: "/"},
	{Port: 8787, Name: "Cloudflare Wrangler", Category: "dev", Protocol: "http+workers", HealthURI: "/"},
}

// DiscoverServices probes the local host (127.0.0.1) concurrently against a set of known service signatures.
func DiscoverServices(ctx context.Context, probes []ServiceProbe) []DiscoveredService {
	if len(probes) == 0 {
		probes = defaultProbes
	}
	client := &http.Client{Timeout: 300 * time.Millisecond}
	now := time.Now().UTC().Format(time.RFC3339)

	var mu sync.Mutex
	var wg sync.WaitGroup
	results := make([]DiscoveredService, 0)

	for _, probe := range probes {
		probe := probe
		wg.Add(1)
		go func() {
			defer wg.Done()
			addr := fmt.Sprintf("127.0.0.1:%d", probe.Port)
			start := time.Now()
			dialer := &net.Dialer{Timeout: 180 * time.Millisecond}
			conn, err := dialer.DialContext(ctx, "tcp", addr)
			if err != nil {
				return
			}
			_ = conn.Close()
			latency := time.Since(start).Milliseconds()

			endpoint := addr
			detail := "port open"
			status := "online"
			if probe.HealthURI != "" && (strings.HasPrefix(probe.Protocol, "http") || strings.HasPrefix(probe.Protocol, "otlp-http")) {
				endpoint = fmt.Sprintf("http://%s", addr)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+probe.HealthURI, nil)
				if err == nil {
					resp, err := client.Do(req)
					if err == nil {
						bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
						_ = resp.Body.Close()
						snippet := strings.TrimSpace(string(bodyBytes))
						snippet = strings.ReplaceAll(snippet, "\n", " ")
						if len(snippet) > 90 {
							snippet = snippet[:90] + "..."
						}
						detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
						if snippet != "" && !strings.HasPrefix(snippet, "<") {
							detail += " · " + snippet
						}
					}
				}
			}

			mu.Lock()
			results = append(results, DiscoveredService{
				Port:       probe.Port,
				Name:       probe.Name,
				Category:   probe.Category,
				Protocol:   probe.Protocol,
				Endpoint:   endpoint,
				Status:     status,
				LatencyMs:  latency,
				Detail:     detail,
				DetectedAt: now,
			})
			mu.Unlock()
		}()
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		if results[i].Category != results[j].Category {
			return results[i].Category < results[j].Category
		}
		return results[i].Port < results[j].Port
	})
	return results
}

func (o *Observatory) handleDiscoverServices(c echo.Context) error {
	services := DiscoverServices(c.Request().Context(), defaultProbes)
	return c.JSON(http.StatusOK, map[string]any{
		"services":  services,
		"count":     len(services),
		"probesRun": len(defaultProbes),
		"scannedAt": time.Now().UTC().Format(time.RFC3339),
	})
}
