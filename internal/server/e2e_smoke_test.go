package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestE2ESmokeRouterAndProbes(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	app := NewEcho(hub, Config{Addr: "127.0.0.1:0"})

	t.Run("embedded UI root and headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET / status=%d body=%s", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("unexpected content-type: %q", ct)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("missing X-Content-Type-Options header: %q", got)
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
			t.Fatalf("expected embedded HTML shell, got: %s", rec.Body.String())
		}
	})

	t.Run("health, healthz, readyz probes", func(t *testing.T) {
		for _, path := range []string{"/health", "/healthz", "/readyz"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status=%d body=%s", path, rec.Code, rec.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("GET %s invalid json: %v", path, err)
			}
			if payload["service"] != "mw-echo" {
				t.Fatalf("GET %s service=%v want mw-echo", path, payload["service"])
			}
		}
	})

	t.Run("prometheus metrics endpoint", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /metrics status=%d", rec.Code)
		}
		body := rec.Body.String()
		for _, needle := range []string{"mw_echo_up 1", "mw_echo_db_ready 1", "mw_echo_goroutines"} {
			if !strings.Contains(body, needle) {
				t.Fatalf("GET /metrics missing %q in:\n%s", needle, body)
			}
		}
	})

	t.Run("core API endpoints", func(t *testing.T) {
		endpoints := []string{
			"/api/ping",
			"/api/stats",
			"/api/tasks",
			"/api/docs",
			"/api/projects",
			"/api/pocket/pair-config",
		}
		for _, path := range endpoints {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status=%d body=%s", path, rec.Code, rec.Body.String())
			}
		}
	})
}
