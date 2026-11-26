package aigateway

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
)

// RocksoulGatewayEnv names the env var holding the mw-rocksoul-gateway base URL.
const RocksoulGatewayEnv = "ROCKSOUL_GATEWAY_URL"

// IsRocksoulProxyPath reports whether a request path is delegated to rocksoul.
func IsRocksoulProxyPath(requestPath string) bool {
	switch requestPath {
	case "/v1/chat/completions", "/v1/messages", "/v1/models":
		return true
	}
	return strings.HasPrefix(requestPath, "/api/ai/")
}

// RocksoulProxyMiddleware returns an echo Pre-middleware that strangles the
// embedded gateway: when ROCKSOUL_GATEWAY_URL resolves to a valid URL, matching
// routes are reverse-proxied (Authorization and X-MW-Data-Classification are
// forwarded). Otherwise it is a pass-through and the embedded gateway serves.
func RocksoulProxyMiddleware(lookupEnv func(string) string) echo.MiddlewareFunc {
	raw := strings.TrimSpace(lookupEnv(RocksoulGatewayEnv))
	var target *url.URL
	if raw != "" {
		parsed, err := url.Parse(raw)
		if err == nil && parsed.Scheme != "" && parsed.Host != "" {
			target = parsed
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		if target == nil {
			return next
		}
		proxy := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.Host = target.Host
				for _, header := range []string{"Authorization", "X-MW-Data-Classification"} {
					if value := pr.In.Header.Get(header); value != "" {
						pr.Out.Header.Set(header, value)
					}
				}
			},
			FlushInterval: -1,
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(w, `{"error":"rocksoul gateway unreachable"}`, http.StatusBadGateway)
			},
		}
		return func(c echo.Context) error {
			if !IsRocksoulProxyPath(c.Request().URL.Path) {
				return next(c)
			}
			proxy.ServeHTTP(c.Response(), c.Request())
			return nil
		}
	}
}
