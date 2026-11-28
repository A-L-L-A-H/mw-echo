package worldradar

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/worldradar/connectors"
	"github.com/labstack/echo/v4"
)

// RegisterV1Routes mounts the OpenAPI 3.1 World Radar API routes under api (e.g. /api/v1).
func (e *RadarEngine) RegisterV1Routes(api *echo.Group) {
	api.GET("/instances", e.v1ListInstances)
	api.POST("/instances", e.v1CreateInstance)
	api.GET("/instances/:instanceId", e.v1GetInstance)
	api.GET("/instances/:instanceId/sources", e.v1ListSources)
	api.POST("/instances/:instanceId/sources", e.v1RegisterSource)
	api.POST("/sources/:sourceId/ingest", e.v1TriggerIngestion)
	api.GET("/sources/:sourceId/checkpoint", e.v1GetCheckpoint)
	api.PUT("/sources/:sourceId/checkpoint", e.v1PutCheckpoint)
	api.GET("/events", e.v1QueryEvents)
	api.GET("/events/:eventId", e.v1GetEvent)
	api.GET("/events/:eventId/provenance", e.v1GetEventProvenance)
	api.GET("/health", e.v1Health)
}

func (e *RadarEngine) v1ListInstances(c echo.Context) error {
	instances, err := e.ListInstances(c.Request().Context())
	if err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusOK, instances)
}

func (e *RadarEngine) v1CreateInstance(c echo.Context) error {
	var body InstanceCreateRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	inst, err := e.CreateInstance(c.Request().Context(), body)
	if err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusCreated, inst)
}

func (e *RadarEngine) v1GetInstance(c echo.Context) error {
	inst, err := e.GetInstance(c.Request().Context(), c.Param("instanceId"))
	if err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusOK, inst)
}

func (e *RadarEngine) v1ListSources(c echo.Context) error {
	sources, err := e.ListSources(c.Request().Context(), c.Param("instanceId"), c.QueryParam("status"))
	if err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusOK, sources)
}

func (e *RadarEngine) v1RegisterSource(c echo.Context) error {
	var body SourceConfig
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	body.InstanceID = c.Param("instanceId")
	sourceID, err := e.RegisterSource(body)
	if err != nil {
		return writeSpecError(c, err)
	}
	src, err := e.getSource(c.Request().Context(), sourceID)
	if err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusCreated, src)
}

func (e *RadarEngine) v1TriggerIngestion(c echo.Context) error {
	sourceID := c.Param("sourceId")
	if err := e.TriggerIngestionContext(c.Request().Context(), sourceID); err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]string{
		"job_id": newUUIDv7(),
		"status": "queued",
	})
}

func (e *RadarEngine) v1QueryEvents(c echo.Context) error {
	q := EventQuery{
		Domain: c.QueryParam("domain"),
		Lens:   c.QueryParam("lens"),
	}
	if raw := strings.TrimSpace(c.QueryParam("confidence_min")); raw != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			q.ConfidenceMin = f
		}
	}
	if raw := strings.TrimSpace(c.QueryParam("since")); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			q.Since = t.UTC()
		}
	}
	if raw := strings.TrimSpace(c.QueryParam("until")); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			q.Until = t.UTC()
		}
	}
	if raw := strings.TrimSpace(c.QueryParam("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			q.Limit = n
		}
	}
	if raw := strings.TrimSpace(c.QueryParam("offset")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			q.Offset = n
		}
	}
	page, err := e.QueryEvents(q)
	if err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusOK, page)
}

func (e *RadarEngine) v1GetEvent(c echo.Context) error {
	detail, err := e.GetEvent(c.Param("eventId"))
	if err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusOK, detail)
}

func (e *RadarEngine) v1GetEventProvenance(c echo.Context) error {
	prov, err := e.GetProvenance(c.Param("eventId"))
	if err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusOK, prov)
}

func (e *RadarEngine) v1GetCheckpoint(c echo.Context) error {
	cp, err := e.GetCheckpoint(c.Request().Context(), c.Param("sourceId"))
	if err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusOK, cp)
}

func (e *RadarEngine) v1PutCheckpoint(c echo.Context) error {
	var body connectors.Checkpoint
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	sourceID := c.Param("sourceId")
	if err := e.SetCheckpoint(c.Request().Context(), sourceID, body); err != nil {
		return writeSpecError(c, err)
	}
	cp, err := e.GetCheckpoint(c.Request().Context(), sourceID)
	if err != nil {
		return writeSpecError(c, err)
	}
	return c.JSON(http.StatusOK, cp)
}

func (e *RadarEngine) v1Health(c echo.Context) error {
	status := "healthy"
	dbOK := e.DB != nil && e.DB.PingContext(c.Request().Context()) == nil
	if !dbOK {
		status = "unhealthy"
	}
	freshness := e.GetFreshnessReport()
	if status == "healthy" && freshness.SLOFailed > 0 {
		status = "degraded"
	}
	return c.JSON(http.StatusOK, map[string]any{
		"status": status,
		"checks": map[string]any{
			"database":  dbOK,
			"freshness": freshness,
		},
	})
}

func writeSpecError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrValidation):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
