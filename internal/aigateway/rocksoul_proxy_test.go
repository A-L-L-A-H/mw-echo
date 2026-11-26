package aigateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func newProxyTestApp(upstreamURL string) *echo.Echo {
	app := echo.New()
	app.Pre(RocksoulProxyMiddleware(func(string) string { return upstreamURL }))
	app.POST("/v1/chat/completions", func(c echo.Context) error { return c.String(http.StatusOK, "embedded") })
	app.GET("/other", func(c echo.Context) error { return c.String(http.StatusOK, "embedded") })
	return app
}

func TestRocksoulProxyForwardsHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("X-MW-Data-Classification") != "secret" {
			http.Error(w, "headers", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("rocksoul:" + r.URL.Path))
	}))
	defer upstream.Close()
	app := newProxyTestApp(upstream.URL)

	for _, path := range []string{"/v1/chat/completions", "/api/ai/agents"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer k")
		req.Header.Set("X-MW-Data-Classification", "secret")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "rocksoul:"+path {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rec.Body.String() != "embedded" {
		t.Fatalf("non-matching path proxied: %s", rec.Body.String())
	}
}

func TestRocksoulProxyUnsetFallsBackToEmbedded(t *testing.T) {
	app := newProxyTestApp("")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")))
	if rec.Body.String() != "embedded" {
		t.Fatalf("expected embedded, got %s", rec.Body.String())
	}
}
