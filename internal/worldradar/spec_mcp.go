package worldradar

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MCPTool defines an Antigravity MCP tool descriptor and handler (Section 4.3).
type MCPTool struct {
	Name        string
	Description string
	InputSchema string
	Handler     func(ctx context.Context, args json.RawMessage) (any, error)
}

const worldradarQueryEventsSchema = `{
  "type": "object",
  "properties": {
    "instance_id": { "type": "string", "format": "uuid" },
    "domain": { "type": "string", "pattern": "^D[0-9]{2}$" },
    "lens": { "type": "string", "pattern": "^L[0-9]{2}$" },
    "confidence_min": { "type": "number", "minimum": 0, "maximum": 1 },
    "since": { "type": "string", "format": "date-time" },
    "until": { "type": "string", "format": "date-time" },
    "limit": { "type": "integer", "minimum": 1, "maximum": 1000, "default": 50 },
    "offset": { "type": "integer", "minimum": 0, "default": 0 }
  },
  "required": ["instance_id"]
}`

const worldradarGetEventSchema = `{
  "type": "object",
  "properties": {
    "event_id": { "type": "string", "format": "uuid" },
    "include_provenance": { "type": "boolean", "default": true },
    "include_claims": { "type": "boolean", "default": true }
  },
  "required": ["event_id"]
}`

const worldradarListSourcesSchema = `{
  "type": "object",
  "properties": {
    "instance_id": { "type": "string", "format": "uuid" },
    "status": { "type": "string", "enum": ["probation","active","suspended","retired"] }
  },
  "required": ["instance_id"]
}`

const worldradarRegisterSourceSchema = `{
  "type": "object",
  "properties": {
    "instance_id": { "type": "string", "format": "uuid" },
    "source_tier": { "type": "string", "enum": ["A","B","C","D","E","F","G","X"] },
    "category": { "type": "string" },
    "trust_base": { "type": "number", "minimum": 0, "maximum": 1 },
    "collection_method": { "type": "string",
    	"enum": ["pull","push","public_api","document_drop","sensor_stream","manual"] },
    "license_class": { "type": "string" },
    "language_hint": { "type": "string" },
    "geography_scope": { "type": "string", "enum": ["global","regional","local","unknown"] },
    "independence_group": { "type": "string" },
    "config_json": { "type": "string" }
  },
  "required": ["instance_id", "source_tier", "category", "trust_base", "collection_method", "license_class",
  	"geography_scope", "config_json"]
}`

const worldradarTriggerIngestSchema = `{
  "type": "object",
  "properties": {
    "source_id": { "type": "string", "format": "uuid" }
  },
  "required": ["source_id"]
}`

const worldradarGetFreshnessSchema = `{
  "type": "object",
  "properties": {
    "instance_id": { "type": "string", "format": "uuid" },
    "tier": { "type": "string", "enum": ["A","B","C","D","E","F","G","X"] }
  },
  "required": ["instance_id"]
}`

const worldradarGetTaxonomySchema = `{
  "type": "object",
  "properties": {
    "version": { "type": "string", "default": "v1.0.0" }
  }
}`

// MCPTools returns the 7 normative World Radar MCP tools wired to eng.
func MCPTools(eng *RadarEngine) []MCPTool {
	return []MCPTool{
		{
			Name:        "worldradar_query_events",
			Description: "Query world radar events with filters",
			InputSchema: worldradarQueryEventsSchema,
			Handler: func(_ context.Context, args json.RawMessage) (any, error) {
				var p struct {
					InstanceID    string  `json:"instance_id"`
					Domain        string  `json:"domain"`
					Lens          string  `json:"lens"`
					ConfidenceMin float64 `json:"confidence_min"`
					Since         string  `json:"since"`
					Until         string  `json:"until"`
					Limit         int     `json:"limit"`
					Offset        int     `json:"offset"`
				}
				if err := json.Unmarshal(args, &p); err != nil {
					return nil, err
				}
				q := EventQuery{
					InstanceID:    p.InstanceID,
					Domain:        p.Domain,
					Lens:          p.Lens,
					ConfidenceMin: p.ConfidenceMin,
					Limit:         p.Limit,
					Offset:        p.Offset,
				}
				if p.Since != "" {
					if t, err := time.Parse(time.RFC3339, p.Since); err == nil {
						q.Since = t.UTC()
					}
				}
				if p.Until != "" {
					if t, err := time.Parse(time.RFC3339, p.Until); err == nil {
						q.Until = t.UTC()
					}
				}
				return eng.QueryEvents(q)
			},
		},
		{
			Name:        "worldradar_get_event",
			Description: "Get full event detail with provenance",
			InputSchema: worldradarGetEventSchema,
			Handler: func(_ context.Context, args json.RawMessage) (any, error) {
				var p struct {
					EventID           string `json:"event_id"`
					IncludeProvenance *bool  `json:"include_provenance"`
					IncludeClaims     *bool  `json:"include_claims"`
				}
				if err := json.Unmarshal(args, &p); err != nil {
					return nil, err
				}
				detail, err := eng.GetEvent(p.EventID)
				if err != nil {
					return nil, err
				}
				if p.IncludeProvenance != nil && !*p.IncludeProvenance {
					detail.Provenance = nil
				}
				if p.IncludeClaims != nil && !*p.IncludeClaims {
					detail.Claims = nil
				}
				return detail, nil
			},
		},
		{
			Name:        "worldradar_list_sources",
			Description: "List registered sources for an instance",
			InputSchema: worldradarListSourcesSchema,
			Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
				var p struct {
					InstanceID string `json:"instance_id"`
					Status     string `json:"status"`
				}
				if err := json.Unmarshal(args, &p); err != nil {
					return nil, err
				}
				return eng.ListSources(ctx, p.InstanceID, p.Status)
			},
		},
		{
			Name:        "worldradar_register_source",
			Description: "Register a new ingestion source",
			InputSchema: worldradarRegisterSourceSchema,
			Handler: func(_ context.Context, args json.RawMessage) (any, error) {
				var cfg SourceConfig
				if err := json.Unmarshal(args, &cfg); err != nil {
					return nil, err
				}
				id, err := eng.RegisterSource(cfg)
				if err != nil {
					return nil, err
				}
				return map[string]string{"source_id": id}, nil
			},
		},
		{
			Name:        "worldradar_trigger_ingest",
			Description: "Trigger manual ingestion for a source",
			InputSchema: worldradarTriggerIngestSchema,
			Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
				var p struct {
					SourceID string `json:"source_id"`
				}
				if err := json.Unmarshal(args, &p); err != nil {
					return nil, err
				}
				if err := eng.TriggerIngestionContext(ctx, p.SourceID); err != nil {
					return nil, err
				}
				return map[string]string{"source_id": p.SourceID, "status": "completed"}, nil
			},
		},
		{
			Name:        "worldradar_get_freshness",
			Description: "Get freshness SLO report",
			InputSchema: worldradarGetFreshnessSchema,
			Handler: func(_ context.Context, args json.RawMessage) (any, error) {
				var p struct {
					InstanceID string `json:"instance_id"`
					Tier       string `json:"tier"`
				}
				if err := json.Unmarshal(args, &p); err != nil {
					return nil, err
				}
				report := eng.GetFreshnessReport()
				if strings.TrimSpace(p.Tier) != "" {
					normTier := strings.ToUpper(strings.TrimSpace(p.Tier))
					filtered := make([]FreshnessSourceStatus, 0)
					for _, s := range report.Sources {
						if s.SourceTier == normTier {
							filtered = append(filtered, s)
						}
					}
					report.Sources = filtered
					report.TotalSources = len(filtered)
				}
				return report, nil
			},
		},
		{
			Name:        "worldradar_get_taxonomy",
			Description: "Get domain/lens taxonomy v1.0.0",
			InputSchema: worldradarGetTaxonomySchema,
			Handler: func(_ context.Context, args json.RawMessage) (any, error) {
				var p struct {
					Version string `json:"version"`
				}
				if len(args) > 0 {
					_ = json.Unmarshal(args, &p)
				}
				ver := strings.TrimSpace(p.Version)
				if ver != "" && ver != "v1.0.0" && ver != "1.0.0" {
					return nil, fmt.Errorf("%w: unsupported taxonomy version %q", ErrValidation, ver)
				}
				return eng.Taxonomy, nil
			},
		},
	}
}
