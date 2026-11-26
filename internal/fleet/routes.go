package fleet

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

type upsertServiceRequest struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type evaluateRequest struct {
	Subject    string `json:"subject"`
	Capability string `json:"capability"`
	RiskTier   string `json:"riskTier"`
	Resource   string `json:"resource"`
	CostUnits  int    `json:"costUnits"`
}

// RegisterRoutes mounts Sovereign Fleet and ARSY Constitutional Governor endpoints under /fleet.
func (m *Manager) RegisterRoutes(api *echo.Group) {
	group := api.Group("/fleet")
	group.GET("/services", m.handleListServices)
	group.PUT("/services", m.handleUpsertService)
	group.POST("/probe", m.handleProbeFleet)

	group.GET("/arsy/grants", m.handleListGrants)
	group.POST("/arsy/grants", m.handleIssueGrant)
	group.DELETE("/arsy/grants/:id", m.handleRevokeGrant)
	group.POST("/arsy/evaluate", m.handleEvaluate)
	group.GET("/arsy/audit", m.handleListAudit)
	group.GET("/arsy/audit/verify", m.handleVerifyAudit)
}

func (m *Manager) handleListServices(c echo.Context) error {
	services, err := m.ListServices(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"services": services,
		"count":    len(services),
	})
}

func (m *Manager) handleUpsertService(c echo.Context) error {
	var body upsertServiceRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request payload"})
	}
	updated, err := m.UpsertService(c.Request().Context(), body.Key, body.Name, body.URL)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, updated)
}

func (m *Manager) handleProbeFleet(c echo.Context) error {
	report, err := m.ProbeFleet(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, report)
}

func (m *Manager) handleListGrants(c echo.Context) error {
	grants, err := m.ListGrants(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"grants": grants,
		"count":  len(grants),
	})
}

func (m *Manager) handleIssueGrant(c echo.Context) error {
	var input GrantInput
	if err := c.Bind(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid grant payload"})
	}
	grant, err := m.IssueGrant(c.Request().Context(), input)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, grant)
}

func (m *Manager) handleRevokeGrant(c echo.Context) error {
	grantID := c.Param("id")
	if err := m.RevokeGrant(c.Request().Context(), grantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "grant not found"})
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"revoked": true, "id": grantID})
}

func (m *Manager) handleEvaluate(c echo.Context) error {
	var body evaluateRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid evaluate payload"})
	}
	outcome, err := m.CheckAndConsume(
		c.Request().Context(),
		body.Subject,
		body.Capability,
		body.RiskTier,
		body.Resource,
		body.CostUnits,
	)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, outcome)
}

func (m *Manager) handleListAudit(c echo.Context) error {
	limit := 50
	if rawLimit := c.QueryParam("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	entries, err := m.ListAuditLogs(c.Request().Context(), limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"entries": entries,
		"count":   len(entries),
	})
}

func (m *Manager) handleVerifyAudit(c echo.Context) error {
	verification, err := m.VerifyAuditChain(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, verification)
}
