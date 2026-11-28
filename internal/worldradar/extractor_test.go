package worldradar

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func TestExtractMarkdownFromHTML(t *testing.T) {
	sampleHTML := `<!doctype html>
<html>
<head>
  <title>Radar Signal Brief</title>
  <style>body { color: red; }</style>
  <script>console.log("secret-script-noise");</script>
</head>
<body>
  <nav><a href="/home">Nav Home</a></nav>
  <main>
    <h1>Global Grid Update</h1>
    <p>Read the <a href="/reports/2026">full report</a> for details.</p>
    <h2>Key Takeaways</h2>
    <ul>
      <li>Latency dropped by 40%</li>
      <li>See <a href="https://example.org/spec">external spec</a></li>
    </ul>
    <h3>Next Steps</h3>
    <p>Deploy across all nodes.</p>
  </main>
  <footer>Copyright 2026 Noise Footer</footer>
</body>
</html>`

	md := ExtractMarkdownFromHTML(sampleHTML, "https://radar.example.com/news/today")
	if strings.Contains(md, "secret-script-noise") || strings.Contains(md, "color: red") || strings.Contains(md,
		"Nav Home") || strings.Contains(md, "Noise Footer") {
		t.Fatalf("non-content chrome was not stripped: %q", md)
	}
	expectedSnippets := []string{
		"# Global Grid Update",
		"[full report](https://radar.example.com/reports/2026)",
		"## Key Takeaways",
		"- Latency dropped by 40%",
		"- See [external spec](https://example.org/spec)",
		"### Next Steps",
		"Deploy across all nodes.",
	}
	for _, snippet := range expectedSnippets {
		if !strings.Contains(md, snippet) {
			t.Fatalf("expected markdown to contain %q, got:\n%s", snippet, md)
		}
	}
}

func TestExtractMarkdownRouteAndSSRF(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	eng, err := NewEngine(hub, Options{})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	app := echo.New()
	eng.RegisterRoutes(app.Group("/api"))

	// 1. Direct HTML conversion
	payload, _ := json.Marshal(map[string]string{
		"url":  "https://example.com/post",
		"html": "<html><head><title>Direct Page</title></head><body><h1>Hello</h1><p>World</p></body></html>",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/radar/extract-markdown", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result ExtractMarkdownResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Title != "Direct Page" || !strings.Contains(result.Markdown,
		"# Hello") || result.CharCount != len(result.Markdown) {
		t.Fatalf("unexpected extract result: %+v", result)
	}

	// 2. SSRF block on loopback and RFC1918
	for _, blockedURL := range []string{"http://127.0.0.1:8080/secret", "http://192.168.1.1/admin",
		"http://localhost:9000/x"} {
		ssrfPayload, _ := json.Marshal(map[string]string{"url": blockedURL})
		ssrfReq := httptest.NewRequest(http.MethodPost, "/api/radar/extract-markdown", bytes.NewReader(ssrfPayload))
		ssrfReq.Header.Set("Content-Type", "application/json")
		ssrfRec := httptest.NewRecorder()
		app.ServeHTTP(ssrfRec, ssrfReq)
		if ssrfRec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 SSRF block for %s, got %d: %s", blockedURL, ssrfRec.Code, ssrfRec.Body.String())
		}
	}
}
