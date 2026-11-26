package diskdoctor

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

type scanRequest struct {
	Path       string `json:"path"`
	MaxDepth   int    `json:"maxDepth"`
	MaxEntries int    `json:"maxEntries"`
}

type cleanRequest struct {
	Paths  []string `json:"paths"`
	DryRun bool     `json:"dryRun"`
}

type insightRequest struct {
	Path   string      `json:"path"`
	Report *ScanReport `json:"report"`
}

// RegisterRoutes mounts the Disk Doctor HTTP endpoints under /disk.
func (m *Manager) RegisterRoutes(api *echo.Group) {
	diskGroup := api.Group("/disk")

	diskGroup.GET("/drives", func(c echo.Context) error {
		drives, err := ListDrives()
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"drives": drives,
			"count":  len(drives),
		})
	})

	diskGroup.GET("/quick-audit", func(c echo.Context) error {
		report, err := QuickAudit(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, report)
	})

	diskGroup.POST("/scan", func(c echo.Context) error {
		var req scanRequest
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid scan payload"})
		}
		if strings.TrimSpace(req.Path) == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "path is required"})
		}
		report, err := ScanDirectory(c.Request().Context(), req.Path, req.MaxDepth, req.MaxEntries)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, report)
	})

	diskGroup.POST("/clean", func(c echo.Context) error {
		var req cleanRequest
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid clean payload"})
		}
		result, err := m.CleanPaths(c.Request().Context(), req.Paths, req.DryRun)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, result)
	})

	diskGroup.POST("/insight", func(c echo.Context) error {
		var req insightRequest
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid insight payload"})
		}

		drives, err := ListDrives()
		if err != nil {
			drives = []DriveInfo{}
		}

		var report ScanReport
		if req.Report != nil && (req.Report.RootPath != "" || len(req.Report.TopDirectories) > 0 || req.Report.ScannedBytes > 0) {
			report = *req.Report
		} else if strings.TrimSpace(req.Path) != "" {
			scanned, scanErr := ScanDirectory(c.Request().Context(), req.Path, defaultMaxDepth, defaultMaxEntries)
			if scanErr != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": scanErr.Error()})
			}
			report = scanned
		} else {
			audited, auditErr := QuickAudit(c.Request().Context())
			if auditErr != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": auditErr.Error()})
			}
			report = audited
		}

		insight, err := m.AnalyzeWithAI(c.Request().Context(), drives, report)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, insight)
	})

	diskGroup.GET("/history", func(c echo.Context) error {
		limit := 25
		if rawLimit := c.QueryParam("limit"); rawLimit != "" {
			if parsed, parseErr := strconv.Atoi(rawLimit); parseErr == nil && parsed > 0 {
				limit = parsed
			}
		}
		history, err := m.ListHistory(c.Request().Context(), limit)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"history": history,
			"count":   len(history),
		})
	})
}
