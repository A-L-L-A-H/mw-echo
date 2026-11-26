package codingagent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// RegisterRoutes mounts the coding agent REST + SSE control plane under /api/agent.
func (e *Engine) RegisterRoutes(api *echo.Group) {
	if api == nil {
		return
	}
	group := api.Group("/agent")
	group.GET("/sessions", e.handleListSessions)
	group.POST("/sessions", e.handleCreateSession)
	group.GET("/sessions/:id", e.handleGetSession)
	group.PATCH("/sessions/:id", e.handlePatchSession)
	group.DELETE("/sessions/:id", e.handleDeleteSession)
	group.POST("/sessions/:id/prompt", e.handlePromptSession)
	group.POST("/sessions/:id/abort", e.handleAbortSession)
	group.POST("/sessions/:id/fork", e.handleForkSession)
	group.POST("/sessions/:id/compact", e.handleCompactSession)
	group.GET("/sessions/:id/events", e.handleSessionEvents)
	group.GET("/tools", e.handleListTools)
}

func (e *Engine) handleListSessions(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	sessions, err := e.ListSessions(c.Request().Context(), limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"sessions": sessions,
		"count":    len(sessions),
	})
}

func (e *Engine) handleCreateSession(c echo.Context) error {
	var input SessionCreateInput
	if err := c.Bind(&input); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid session JSON"})
	}
	created, err := e.CreateSession(c.Request().Context(), input)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, created)
}

func (e *Engine) handleGetSession(c echo.Context) error {
	session, messages, err := e.GetSessionWithMessages(c.Request().Context(), c.Param("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "session not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"session":  session,
		"messages": messages,
	})
}

func (e *Engine) handlePatchSession(c echo.Context) error {
	var patch SessionPatchInput
	if err := c.Bind(&patch); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid session patch JSON"})
	}
	updated, err := e.UpdateSession(c.Request().Context(), c.Param("id"), patch)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "session not found"})
	}
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, updated)
}

func (e *Engine) handleDeleteSession(c echo.Context) error {
	err := e.DeleteSession(c.Request().Context(), c.Param("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "session not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
}

func (e *Engine) handlePromptSession(c echo.Context) error {
	var body struct {
		Prompt string `json:"prompt"`
		Async  bool   `json:"async"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid prompt JSON"})
	}
	if strings.TrimSpace(body.Prompt) == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "prompt is required"})
	}

	sessionID := c.Param("id")
	if body.Async {
		err := e.StartPromptAsync(sessionID, body.Prompt)
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "session not found"})
		}
		if errors.Is(err, ErrSessionBusy) {
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		}
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusAccepted, map[string]any{
			"accepted":  true,
			"sessionId": sessionID,
			"status":    StatusRunning,
		})
	}

	outcome, err := e.RunPrompt(c.Request().Context(), sessionID, body.Prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "session not found"})
	}
	if errors.Is(err, ErrSessionBusy) {
		return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
	}
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]any{
			"error":    err.Error(),
			"session":  outcome.Session,
			"messages": outcome.Messages,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"session":  outcome.Session,
		"messages": outcome.Messages,
		"reply":    outcome.Reply,
	})
}

func (e *Engine) handleAbortSession(c echo.Context) error {
	aborted := e.AbortSession(c.Param("id"))
	return c.JSON(http.StatusOK, map[string]bool{"aborted": aborted})
}

func (e *Engine) handleForkSession(c echo.Context) error {
	var body struct {
		Title   string `json:"title"`
		UpToSeq int    `json:"upToSeq"`
	}
	if c.Request().ContentLength > 0 {
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid fork JSON"})
		}
	}

	var forked Session
	var err error
	if body.UpToSeq > 0 {
		forked, err = e.ForkSession(c.Request().Context(), c.Param("id"), body.Title, body.UpToSeq)
	} else {
		forked, err = e.ForkSession(c.Request().Context(), c.Param("id"), body.Title)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "session not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, forked)
}

func (e *Engine) handleCompactSession(c echo.Context) error {
	outcome, err := e.CompactSession(c.Request().Context(), c.Param("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "session not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, outcome)
}

func (e *Engine) handleSessionEvents(c echo.Context) error {
	sessionID := c.Param("id")
	if _, err := e.GetSession(c.Request().Context(), sessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "session not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	resp := c.Response()
	resp.Header().Set(echo.HeaderContentType, "text/event-stream")
	resp.Header().Set(echo.HeaderCacheControl, "no-cache")
	resp.Header().Set("Connection", "keep-alive")
	resp.Header().Set("X-Accel-Buffering", "no")
	resp.WriteHeader(http.StatusOK)

	flusher, canFlush := resp.Writer.(http.Flusher)
	_, _ = fmt.Fprint(resp.Writer, ": connected\n\n")
	if canFlush {
		flusher.Flush()
	}

	eventCh, unsubscribe := e.Subscribe(sessionID)
	defer unsubscribe()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	reqCtx := c.Request().Context()
	for {
		select {
		case <-reqCtx.Done():
			return nil
		case event := <-eventCh:
			encoded, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if _, writeErr := fmt.Fprintf(resp.Writer, "data: %s\n\n", encoded); writeErr != nil {
				return nil
			}
			if canFlush {
				flusher.Flush()
			}
		case <-keepalive.C:
			if _, writeErr := fmt.Fprint(resp.Writer, ": ping\n\n"); writeErr != nil {
				return nil
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}
}

func (e *Engine) handleListTools(c echo.Context) error {
	specs := e.ToolSpecs()
	return c.JSON(http.StatusOK, map[string]any{
		"tools": specs,
		"count": len(specs),
	})
}
