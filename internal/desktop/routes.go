package desktop

import (
	"errors"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

type modeUpdateRequest struct {
	Mode string `json:"mode"`
}

type riskEvaluateRequest struct {
	Action            string `json:"action"`
	TargetPath        string `json:"targetPath"`
	TargetCommand     string `json:"targetCommand"`
	TargetEndpointURL string `json:"targetEndpointUrl"`
}

type logCreateRequest struct {
	Level   string `json:"level"`
	Source  string `json:"source"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

// RegisterRoutes attaches the desktop runtime endpoints under /api/desktop.
func (m *Manager) RegisterRoutes(api *echo.Group) {
	if api == nil {
		return
	}

	grp := api.Group("/desktop")
	grp.GET("/status", m.handleGetStatus)
	grp.PUT("/mode", m.handlePutMode)
	grp.GET("/doctor", m.handleGetDoctor)
	grp.POST("/doctor/repair", m.handlePostDoctorRepair)
	grp.GET("/local-ai", m.handleGetLocalAI)
	grp.POST("/risk/evaluate", m.handlePostRiskEvaluate)
	grp.GET("/logs", m.handleGetLogs)
	grp.POST("/logs", m.handlePostLogs)
}

func (m *Manager) handleGetStatus(c echo.Context) error {
	modeState, err := m.GetMode(c.Request().Context())
	if err != nil {
		return writeDesktopError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"mode":                   modeState.Mode,
		"restrictedCapabilities": modeState.RestrictedCapabilities,
		"modeUpdatedAt":          modeState.UpdatedAt,
		"platform":               runtime.GOOS,
		"arch":                   runtime.GOARCH,
		"goVersion":              runtime.Version(),
		"uptimeSeconds":          int64(time.Since(m.startedAt).Seconds()),
		"startedAt":              m.startedAt.Format(time.RFC3339),
		"dataDir":                m.dataDir,
		"ringLogCount":           len(m.RingEntries()),
	})
}

func (m *Manager) handlePutMode(c echo.Context) error {
	var body modeUpdateRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid mode JSON payload"})
	}

	modeState, err := m.SetMode(c.Request().Context(), body.Mode)
	if err != nil {
		return writeDesktopError(c, err)
	}
	return c.JSON(http.StatusOK, modeState)
}

func (m *Manager) handleGetDoctor(c echo.Context) error {
	doctorReport, err := m.RunDoctor(c.Request().Context())
	if err != nil {
		return writeDesktopError(c, err)
	}
	return c.JSON(http.StatusOK, doctorReport)
}

func (m *Manager) handlePostDoctorRepair(c echo.Context) error {
	repairResult, err := m.RunRepair(c.Request().Context())
	if err != nil {
		return writeDesktopError(c, err)
	}
	return c.JSON(http.StatusOK, repairResult)
}

func (m *Manager) handleGetLocalAI(c echo.Context) error {
	discovery := m.DiscoverLocalAI(c.Request().Context(), nil)
	return c.JSON(http.StatusOK, discovery)
}

func (m *Manager) handlePostRiskEvaluate(c echo.Context) error {
	var body riskEvaluateRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid risk evaluation JSON payload"})
	}

	evalResult := EvaluateAction(body.Action, body.TargetPath, body.TargetCommand, body.TargetEndpointURL)
	return c.JSON(http.StatusOK, evalResult)
}

func (m *Manager) handleGetLogs(c echo.Context) error {
	limit := 0
	if rawLimit := c.QueryParam("limit"); rawLimit != "" {
		parsedLimit, convErr := strconv.Atoi(rawLimit)
		if convErr != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid limit query parameter"})
		}
		limit = parsedLimit
	}

	entries, err := m.ListLogs(
		c.Request().Context(),
		c.QueryParam("level"),
		c.QueryParam("source"),
		limit,
	)
	if err != nil {
		return writeDesktopError(c, err)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"logs":  entries,
		"count": len(entries),
	})
}

func (m *Manager) handlePostLogs(c echo.Context) error {
	var body logCreateRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid log JSON payload"})
	}

	entry, err := m.Log(c.Request().Context(), body.Level, body.Source, body.Message, body.Detail)
	if err != nil {
		return writeDesktopError(c, err)
	}
	return c.JSON(http.StatusCreated, entry)
}

func writeDesktopError(c echo.Context, err error) error {
	if errors.Is(err, ErrValidation) {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
}
