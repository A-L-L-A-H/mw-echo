package memvault

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

type updatePrivacyRequest struct {
	Sensitivity   string `json:"sensitivity"`
	AllowCloudLLM bool   `json:"allowCloudLLM"`
}

// RegisterRoutes mounts the local-first memory vault REST endpoints under /memvault.
func (m *Manager) RegisterRoutes(api *echo.Group) {
	if api == nil {
		return
	}
	group := api.Group("/memvault")
	group.POST("/import", m.handleImport)
	group.GET("/search", m.handleSearch)
	group.GET("/conversations", m.handleListConversations)
	group.GET("/conversations/:id", m.handleGetConversation)
	group.PATCH("/conversations/:id/privacy", m.handleUpdatePrivacy)
	group.GET("/stats", m.handleStats)
}

func (m *Manager) handleImport(c echo.Context) error {
	contentType := strings.ToLower(c.Request().Header.Get(echo.HeaderContentType))

	var payload []byte
	var isZip bool

	if strings.HasPrefix(contentType, "multipart/form-data") {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "multipart form field 'file' is required"})
		}
		openedFile, err := fileHeader.Open()
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to open uploaded file"})
		}
		defer openedFile.Close()

		payload, err = io.ReadAll(openedFile)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to read uploaded file"})
		}
		isZip = strings.HasSuffix(strings.ToLower(fileHeader.Filename), ".zip") || isZipPayload(payload)
	} else {
		var err error
		payload, err = io.ReadAll(c.Request().Body)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
		}
		isZip = strings.Contains(contentType, "zip") || isZipPayload(payload)
	}

	var report ImportReport
	var err error
	if isZip {
		report, err = m.ImportZIP(c.Request().Context(), payload)
	} else {
		report, err = m.ImportChatGPTJSON(c.Request().Context(), payload)
	}
	if err != nil {
		return writeMemvaultError(c, err)
	}
	return c.JSON(http.StatusOK, report)
}

func (m *Manager) handleSearch(c echo.Context) error {
	query := c.QueryParam("q")
	cloudSafeOnly := parseBoolQuery(c.QueryParam("cloudSafeOnly")) || parseBoolQuery(c.QueryParam("cloud_safe_only"))
	limit := 0
	if rawLimit := c.QueryParam("limit"); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid limit query parameter"})
		}
		limit = parsedLimit
	}

	hits, err := m.Search(c.Request().Context(), query, cloudSafeOnly, limit)
	if err != nil {
		return writeMemvaultError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"hits":          hits,
		"count":         len(hits),
		"cloudSafeOnly": cloudSafeOnly,
	})
}

func (m *Manager) handleListConversations(c echo.Context) error {
	limit := 0
	if rawLimit := c.QueryParam("limit"); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid limit query parameter"})
		}
		limit = parsedLimit
	}
	conversations, err := m.ListConversations(c.Request().Context(), c.QueryParam("sensitivity"), limit)
	if err != nil {
		return writeMemvaultError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"conversations": conversations,
		"count":         len(conversations),
	})
}

func (m *Manager) handleGetConversation(c echo.Context) error {
	detail, err := m.GetConversation(c.Request().Context(), c.Param("id"))
	if err != nil {
		return writeMemvaultError(c, err)
	}
	return c.JSON(http.StatusOK, detail)
}

func (m *Manager) handleUpdatePrivacy(c echo.Context) error {
	var body updatePrivacyRequest
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid privacy update JSON payload"})
	}
	updated, err := m.SetPrivacy(c.Request().Context(), c.Param("id"), body.Sensitivity, body.AllowCloudLLM)
	if err != nil {
		return writeMemvaultError(c, err)
	}
	return c.JSON(http.StatusOK, updated)
}

func (m *Manager) handleStats(c echo.Context) error {
	stats, err := m.Stats(c.Request().Context())
	if err != nil {
		return writeMemvaultError(c, err)
	}
	return c.JSON(http.StatusOK, stats)
}

func isZipPayload(payload []byte) bool {
	return len(payload) >= 4 && bytes.Equal(payload[:4], []byte{'P', 'K', 0x03, 0x04})
}

func parseBoolQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func writeMemvaultError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, ErrPrivacyViolation):
		return c.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrValidation):
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
