package flows

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

// RegisterRoutes mounts the flows REST endpoints under /api/flows.
func (e *Engine) RegisterRoutes(api *echo.Group) {
	if api == nil {
		return
	}
	group := api.Group("/flows")
	group.GET("", e.handleListFlows)
	group.POST("", e.handleUpsertFlow)
	group.PATCH("/:id", e.handlePatchFlow)
	group.DELETE("/:id", e.handleDeleteFlow)
	group.POST("/:id/run", e.handleRunFlow)
	group.GET("/:id/runs", e.handleListFlowRuns)
}

func (e *Engine) handleListFlows(c echo.Context) error {
	flowsList, err := e.ListFlows(c.Request().Context())
	if err != nil {
		return writeFlowError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"flows": flowsList,
		"count": len(flowsList),
	})
}

func (e *Engine) handleUpsertFlow(c echo.Context) error {
	var body FlowRecord
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid flow JSON"})
	}
	saved, err := e.UpsertFlow(c.Request().Context(), body)
	if err != nil {
		return writeFlowError(c, err)
	}
	return c.JSON(http.StatusCreated, saved)
}

func (e *Engine) handlePatchFlow(c echo.Context) error {
	var patch FlowPatchInput
	if err := c.Bind(&patch); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid flow patch JSON"})
	}
	updated, err := e.PatchFlow(c.Request().Context(), c.Param("id"), patch)
	if err != nil {
		return writeFlowError(c, err)
	}
	return c.JSON(http.StatusOK, updated)
}

func (e *Engine) handleDeleteFlow(c echo.Context) error {
	if err := e.DeleteFlow(c.Request().Context(), c.Param("id")); err != nil {
		return writeFlowError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
}

func (e *Engine) handleRunFlow(c echo.Context) error {
	var body struct {
		TriggerPayload string `json:"triggerPayload"`
	}
	if c.Request().ContentLength != 0 {
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid run payload JSON"})
		}
	}

	runRec, err := e.RunFlowByID(c.Request().Context(), c.Param("id"), body.TriggerPayload)
	if err != nil {
		if errors.Is(err, ErrApprovalRequired) {
			return c.JSON(http.StatusForbidden, map[string]any{
				"error": err.Error(),
				"run":   runRec,
			})
		}
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "flow not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"error": err.Error(),
			"run":   runRec,
		})
	}
	return c.JSON(http.StatusOK, runRec)
}

func (e *Engine) handleListFlowRuns(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	runs, err := e.ListFlowRuns(c.Request().Context(), c.Param("id"), limit)
	if err != nil {
		return writeFlowError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"runs":  runs,
		"count": len(runs),
	})
}

func writeFlowError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return c.JSON(http.StatusNotFound, map[string]string{"error": "flow not found"})
	case errors.Is(err, ErrValidation):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrApprovalRequired):
		return c.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
