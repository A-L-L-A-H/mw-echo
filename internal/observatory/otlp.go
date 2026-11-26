package observatory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// EventRecord represents a normalized OTLP trace span, metric datapoint, or log entry.
type EventRecord struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Service     string  `json:"service"`
	Agent       string  `json:"agent,omitempty"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	DurationMs  float64 `json:"durationMs"`
	TokensIn    int64   `json:"tokensIn"`
	TokensOut   int64   `json:"tokensOut"`
	CostUSD     float64 `json:"costUsd"`
	PayloadJSON string  `json:"payloadJson,omitempty"`
	CreatedAt   string  `json:"createdAt"`
}

// ObservatoryStats summarizes ingested OTLP telemetry.
type ObservatoryStats struct {
	TotalEvents    int            `json:"totalEvents"`
	CountsByKind   map[string]int `json:"countsByKind"`
	CountsBySvc    map[string]int `json:"countsByService"`
	TotalTokensIn  int64          `json:"totalTokensIn"`
	TotalTokensOut int64          `json:"totalTokensOut"`
	AvgDurationMs  float64        `json:"avgDurationMs"`
}

type otlpKeyValue struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string  `json:"stringValue"`
		IntValue    any     `json:"intValue"`
		DoubleValue float64 `json:"doubleValue"`
		BoolValue   *bool   `json:"boolValue"`
	} `json:"value"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

func attrMap(attrs []otlpKeyValue) map[string]any {
	out := make(map[string]any, len(attrs))
	for _, a := range attrs {
		switch {
		case a.Value.StringValue != "":
			out[a.Key] = a.Value.StringValue
		case a.Value.IntValue != nil:
			switch v := a.Value.IntValue.(type) {
			case float64:
				out[a.Key] = int64(v)
			case string:
				if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
					out[a.Key] = parsed
				}
			}
		case a.Value.DoubleValue != 0:
			out[a.Key] = a.Value.DoubleValue
		case a.Value.BoolValue != nil:
			out[a.Key] = *a.Value.BoolValue
		}
	}
	return out
}

func firstStringAttr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstInt64Attr(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case int64:
			return v
		case float64:
			return int64(v)
		case string:
			if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func firstFloatAttr(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return v
		case int64:
			return float64(v)
		}
	}
	return 0
}

func parseNano(raw any) int64 {
	switch v := raw.(type) {
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	case float64:
		return int64(v)
	case int64:
		return v
	default:
		return 0
	}
}

func makeEventID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	h.Write([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	return hex.EncodeToString(h.Sum(nil))[:24]
}

func (o *Observatory) insertEvents(ctx context.Context, events []EventRecord) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := o.hub.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO observatory_events (id, kind, service, agent, name, status, duration_ms, tokens_in, tokens_out, cost_usd, payload_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, ev := range events {
		if ev.ID == "" {
			ev.ID = makeEventID(ev.Kind, ev.Service, ev.Name)
		}
		if ev.CreatedAt == "" {
			ev.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if ev.PayloadJSON == "" {
			ev.PayloadJSON = "{}"
		}
		if _, err := stmt.ExecContext(ctx, ev.ID, ev.Kind, ev.Service, ev.Agent, ev.Name, ev.Status, ev.DurationMs, ev.TokensIn, ev.TokensOut, ev.CostUSD, ev.PayloadJSON, ev.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (o *Observatory) handleOTLPTraces(c echo.Context) error {
	var body struct {
		ResourceSpans []struct {
			Resource   otlpResource `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					TraceID           string         `json:"traceId"`
					SpanID            string         `json:"spanId"`
					Name              string         `json:"name"`
					StartTimeUnixNano any            `json:"startTimeUnixNano"`
					EndTimeUnixNano   any            `json:"endTimeUnixNano"`
					Attributes        []otlpKeyValue `json:"attributes"`
					Status            struct {
						Code    any    `json:"code"`
						Message string `json:"message"`
					} `json:"status"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid OTLP trace JSON"})
	}

	var events []EventRecord
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, rs := range body.ResourceSpans {
		resAttrs := attrMap(rs.Resource.Attributes)
		service := firstStringAttr(resAttrs, "service.name", "telemetry.sdk.name")
		if service == "" {
			service = "unknown"
		}
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				spAttrs := attrMap(sp.Attributes)
				startNs := parseNano(sp.StartTimeUnixNano)
				endNs := parseNano(sp.EndTimeUnixNano)
				durationMs := 0.0
				if endNs > startNs && startNs > 0 {
					durationMs = float64(endNs-startNs) / 1e6
				}
				status := "ok"
				if sp.Status.Code == float64(2) || sp.Status.Code == "STATUS_CODE_ERROR" || sp.Status.Message != "" {
					status = "error"
				}
				agent := firstStringAttr(spAttrs, "gen_ai.agent.name", "mw.agent", "agent.name")
				if agent == "" {
					agent = firstStringAttr(resAttrs, "gen_ai.agent.name", "mw.agent")
				}
				tokensIn := firstInt64Attr(spAttrs, "gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens", "llm.token_count.prompt")
				tokensOut := firstInt64Attr(spAttrs, "gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens", "llm.token_count.completion")
				costUSD := firstFloatAttr(spAttrs, "gen_ai.usage.cost", "llm.cost_usd")

				payloadBytes, _ := json.Marshal(map[string]any{
					"traceId":    sp.TraceID,
					"spanId":     sp.SpanID,
					"attributes": spAttrs,
					"resource":   resAttrs,
				})
				events = append(events, EventRecord{
					ID:          makeEventID("trace", service, sp.SpanID, sp.Name),
					Kind:        "trace",
					Service:     service,
					Agent:       agent,
					Name:        sp.Name,
					Status:      status,
					DurationMs:  durationMs,
					TokensIn:    tokensIn,
					TokensOut:   tokensOut,
					CostUSD:     costUSD,
					PayloadJSON: string(payloadBytes),
					CreatedAt:   now,
				})
			}
		}
	}
	if err := o.insertEvents(c.Request().Context(), events); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if o.triage != nil {
		for _, ev := range events {
			if ev.Status == "error" {
				_, _ = o.triage.RecordErrorSpan(c.Request().Context(), ev.Service, ev.Name)
			}
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"partialSuccess": map[string]any{}, "accepted": len(events)})
}

func (o *Observatory) handleOTLPMetrics(c echo.Context) error {
	var body struct {
		ResourceMetrics []struct {
			Resource     otlpResource `json:"resource"`
			ScopeMetrics []struct {
				Metrics []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
					Unit        string `json:"unit"`
				} `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid OTLP metrics JSON"})
	}

	var events []EventRecord
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, rm := range body.ResourceMetrics {
		resAttrs := attrMap(rm.Resource.Attributes)
		service := firstStringAttr(resAttrs, "service.name")
		if service == "" {
			service = "unknown"
		}
		agent := firstStringAttr(resAttrs, "gen_ai.agent.name", "mw.agent")
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				payloadBytes, _ := json.Marshal(map[string]any{
					"description": m.Description,
					"unit":        m.Unit,
					"resource":    resAttrs,
				})
				events = append(events, EventRecord{
					ID:          makeEventID("metric", service, m.Name),
					Kind:        "metric",
					Service:     service,
					Agent:       agent,
					Name:        m.Name,
					Status:      "ok",
					PayloadJSON: string(payloadBytes),
					CreatedAt:   now,
				})
			}
		}
	}
	if err := o.insertEvents(c.Request().Context(), events); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if o.triage != nil {
		for _, ev := range events {
			if ev.Status == "error" {
				_, _ = o.triage.RecordErrorSpan(c.Request().Context(), ev.Service, ev.Name)
			}
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"partialSuccess": map[string]any{}, "accepted": len(events)})
}

func (o *Observatory) handleOTLPLogs(c echo.Context) error {
	var body struct {
		ResourceLogs []struct {
			Resource  otlpResource `json:"resource"`
			ScopeLogs []struct {
				LogRecords []struct {
					SeverityText string         `json:"severityText"`
					Attributes   []otlpKeyValue `json:"attributes"`
					Body         struct {
						StringValue string `json:"stringValue"`
					} `json:"body"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid OTLP logs JSON"})
	}

	var events []EventRecord
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, rl := range body.ResourceLogs {
		resAttrs := attrMap(rl.Resource.Attributes)
		service := firstStringAttr(resAttrs, "service.name")
		if service == "" {
			service = "unknown"
		}
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				lrAttrs := attrMap(lr.Attributes)
				name := strings.TrimSpace(lr.Body.StringValue)
				if name == "" {
					name = "log.entry"
				}
				if len(name) > 140 {
					name = name[:140]
				}
				status := strings.ToLower(strings.TrimSpace(lr.SeverityText))
				if status == "" {
					status = "info"
				}
				payloadBytes, _ := json.Marshal(map[string]any{
					"body":       lr.Body.StringValue,
					"attributes": lrAttrs,
					"resource":   resAttrs,
				})
				events = append(events, EventRecord{
					ID:          makeEventID("log", service, name),
					Kind:        "log",
					Service:     service,
					Agent:       firstStringAttr(lrAttrs, "gen_ai.agent.name", "mw.agent"),
					Name:        name,
					Status:      status,
					PayloadJSON: string(payloadBytes),
					CreatedAt:   now,
				})
			}
		}
	}
	if err := o.insertEvents(c.Request().Context(), events); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if o.triage != nil {
		for _, ev := range events {
			if ev.Status == "error" {
				_, _ = o.triage.RecordErrorSpan(c.Request().Context(), ev.Service, ev.Name)
			}
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"partialSuccess": map[string]any{}, "accepted": len(events)})
}

func (o *Observatory) handleListEvents(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	kind := sanitizeKind(c.QueryParam("kind"))
	service := strings.TrimSpace(c.QueryParam("service"))

	var clauses []string
	var args []any
	if kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, kind)
	}
	if service != "" {
		clauses = append(clauses, "service = ?")
		args = append(args, service)
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)

	rows, err := o.hub.DB().QueryContext(c.Request().Context(), fmt.Sprintf(`
SELECT id, kind, service, agent, name, status, duration_ms, tokens_in, tokens_out, cost_usd, payload_json, created_at
FROM observatory_events %s
ORDER BY created_at DESC LIMIT ?`, whereSQL), args...)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	defer rows.Close()

	events := make([]EventRecord, 0)
	for rows.Next() {
		var ev EventRecord
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.Service, &ev.Agent, &ev.Name, &ev.Status, &ev.DurationMs, &ev.TokensIn, &ev.TokensOut, &ev.CostUSD, &ev.PayloadJSON, &ev.CreatedAt); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		events = append(events, ev)
	}
	return c.JSON(http.StatusOK, map[string]any{"events": events, "count": len(events)})
}

func (o *Observatory) handleStats(c echo.Context) error {
	stats := ObservatoryStats{
		CountsByKind: map[string]int{},
		CountsBySvc:  map[string]int{},
	}
	_ = o.hub.DB().QueryRowContext(c.Request().Context(), `
SELECT COUNT(*), COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0), COALESCE(AVG(duration_ms), 0)
FROM observatory_events`).Scan(&stats.TotalEvents, &stats.TotalTokensIn, &stats.TotalTokensOut, &stats.AvgDurationMs)

	kindRows, err := o.hub.DB().QueryContext(c.Request().Context(), `SELECT kind, COUNT(*) FROM observatory_events GROUP BY kind`)
	if err == nil {
		defer kindRows.Close()
		for kindRows.Next() {
			var k string
			var cnt int
			if kindRows.Scan(&k, &cnt) == nil {
				stats.CountsByKind[k] = cnt
			}
		}
	}
	svcRows, err := o.hub.DB().QueryContext(c.Request().Context(), `SELECT service, COUNT(*) FROM observatory_events GROUP BY service ORDER BY COUNT(*) DESC LIMIT 20`)
	if err == nil {
		defer svcRows.Close()
		for svcRows.Next() {
			var s string
			var cnt int
			if svcRows.Scan(&s, &cnt) == nil {
				stats.CountsBySvc[s] = cnt
			}
		}
	}
	return c.JSON(http.StatusOK, stats)
}
