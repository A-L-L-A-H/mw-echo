package observatory

import (
	"fmt"
	"strings"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

// Observatory provides OTLP trace/metric/log ingest, localhost protocol auto-discovery,
// and local Git repository radar inside mw-echo.
type Observatory struct {
	hub    *store.HubStore
	triage *ErrorTriageMonitor
}

// SetErrorTriageMonitor attaches an ErrorTriageMonitor to observe incoming OTLP error spans.
func (o *Observatory) SetErrorTriageMonitor(monitor *ErrorTriageMonitor) {
	o.triage = monitor
}

// New initializes the Observatory module and ensures its SQLite table exists.
func New(hub *store.HubStore) (*Observatory, error) {
	obs := &Observatory{hub: hub}
	if err := obs.migrate(); err != nil {
		return nil, err
	}
	return obs, nil
}

func (o *Observatory) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS observatory_events (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  service TEXT NOT NULL DEFAULT 'unknown',
  agent TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'ok',
  duration_ms REAL NOT NULL DEFAULT 0,
  tokens_in INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0,
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_obs_kind_created ON observatory_events(kind, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_obs_service ON observatory_events(service);
`
	if _, err := o.hub.DB().Exec(schema); err != nil {
		return fmt.Errorf("migrate observatory schema: %w", err)
	}
	return nil
}

// RegisterRoutes attaches OTLP ingest endpoints (/v1/traces, /v1/metrics, /v1/logs)
// and authenticated Observatory management endpoints (/api/observatory/*).
func (o *Observatory) RegisterRoutes(app *echo.Echo, api *echo.Group) {
	// Standard OTLP HTTP/JSON ingest endpoints (open on local host like AI gateway /v1/*)
	app.POST("/v1/traces", o.handleOTLPTraces)
	app.POST("/v1/metrics", o.handleOTLPMetrics)
	app.POST("/v1/logs", o.handleOTLPLogs)

	obsGroup := api.Group("/observatory")
	obsGroup.GET("/events", o.handleListEvents)
	obsGroup.GET("/stats", o.handleStats)
	obsGroup.GET("/services", o.handleDiscoverServices)
	obsGroup.GET("/repos", o.handleListRepos)
	obsGroup.POST("/repos/scan", o.handleScanRepos)
}

func sanitizeKind(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "trace", "metric", "log":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}
