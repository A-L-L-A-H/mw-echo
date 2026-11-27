package server

import (
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func registerProbeRoutes(app *echo.Echo, hub *store.HubStore) {
	startedAt := time.Now().UTC()

	app.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "mw-echo",
			"dataDir": hub.DataDir(),
		})
	})

	app.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "mw-echo",
		})
	})

	app.GET("/readyz", func(c echo.Context) error {
		if err := hub.DB().PingContext(c.Request().Context()); err != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]any{
				"status":  "not_ready",
				"service": "mw-echo",
				"error":   err.Error(),
			})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"status":  "ready",
			"service": "mw-echo",
		})
	})

	app.GET("/metrics", func(c echo.Context) error {
		dbReady := 1
		if err := hub.DB().PingContext(c.Request().Context()); err != nil {
			dbReady = 0
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		uptime := time.Since(startedAt).Seconds()
		payload := fmt.Sprintf(
			"# HELP mw_echo_up Whether mw-echo is up.\n"+
				"# TYPE mw_echo_up gauge\n"+
				"mw_echo_up 1\n"+
				"# HELP mw_echo_db_ready Whether SQLite hub store is reachable.\n"+
				"# TYPE mw_echo_db_ready gauge\n"+
				"mw_echo_db_ready %d\n"+
				"# HELP mw_echo_uptime_seconds Process uptime in seconds.\n"+
				"# TYPE mw_echo_uptime_seconds gauge\n"+
				"mw_echo_uptime_seconds %.3f\n"+
				"# HELP mw_echo_goroutines Current number of goroutines.\n"+
				"# TYPE mw_echo_goroutines gauge\n"+
				"mw_echo_goroutines %d\n"+
				"# HELP mw_echo_mem_alloc_bytes Allocated heap memory in bytes.\n"+
				"# TYPE mw_echo_mem_alloc_bytes gauge\n"+
				"mw_echo_mem_alloc_bytes %d\n",
			dbReady, uptime, runtime.NumGoroutine(), mem.Alloc,
		)
		return c.Blob(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(payload))
	})
}
