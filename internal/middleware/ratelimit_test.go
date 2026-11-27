package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func TestSlidingWindowLimiter_WindowAndExpiry(t *testing.T) {
	now := time.Date(2026, 11, 27, 7, 0, 0, 0, time.UTC)
	limiter := NewSlidingWindowLimiter(RateLimitConfig{
		WindowDuration:  10 * time.Second,
		MaxRequests:     3,
		FailureWindow:   time.Minute,
		MaxFailures:     5,
		LockoutDuration: 5 * time.Minute,
		NowFunc:         func() time.Time { return now },
	})

	for i := 0; i < 3; i++ {
		allowed, reason, _ := limiter.Allow("203.0.113.10")
		if !allowed {
			t.Fatalf("request %d unexpectedly blocked: %s", i+1, reason)
		}
		now = now.Add(2 * time.Second)
	}

	// 4th request at t=6s within 10s window must be rate-limited
	allowed, reason, retry := limiter.Allow("203.0.113.10")
	if allowed || reason != "rate_limit_exceeded" {
		t.Fatalf("expected rate_limit_exceeded, got allowed=%v reason=%q", allowed, reason)
	}
	if retry != 4*time.Second {
		t.Fatalf("expected retryAfter=4s, got %v", retry)
	}

	// Advance past first request expiry (t=11s > 10s)
	now = now.Add(5 * time.Second)
	allowed, _, _ = limiter.Allow("203.0.113.10")
	if !allowed {
		t.Fatalf("expected request after sliding window expiry to succeed")
	}
}

func TestSlidingWindowLimiter_BruteForceLockoutMiddleware(t *testing.T) {
	now := time.Date(2026, 11, 27, 7, 0, 0, 0, time.UTC)
	limiter := NewSlidingWindowLimiter(RateLimitConfig{
		WindowDuration:  time.Minute,
		MaxRequests:     50,
		FailureWindow:   2 * time.Minute,
		MaxFailures:     3,
		LockoutDuration: 10 * time.Minute,
		NowFunc:         func() time.Time { return now },
	})

	app := echo.New()
	app.Use(limiter.Middleware())
	app.POST("/login", func(c echo.Context) error {
		if c.Request().Header.Get("X-Pin") != "9999" {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid pin"})
		}
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// 3 failed login attempts trigger lockout
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.Header.Set("CF-Connecting-IP", "198.51.100.42")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, rec.Code)
		}
	}

	// 4th attempt (even with valid PIN) is rejected with 429 brute_force_lockout
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.Header.Set("CF-Connecting-IP", "198.51.100.42")
	req.Header.Set("X-Pin", "9999")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 during brute-force lockout, got %d", rec.Code)
	}
	var payload map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload["error"] != "brute_force_lockout" {
		t.Fatalf("expected error=brute_force_lockout, got %v", payload["error"])
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("expected Retry-After header on 429 lockout response")
	}

	// Advance clock past lockout duration
	now = now.Add(11 * time.Minute)
	reqAfter := httptest.NewRequest(http.MethodPost, "/login", nil)
	reqAfter.Header.Set("CF-Connecting-IP", "198.51.100.42")
	reqAfter.Header.Set("X-Pin", "9999")
	recAfter := httptest.NewRecorder()
	app.ServeHTTP(recAfter, reqAfter)
	if recAfter.Code != http.StatusOK {
		t.Fatalf("expected 200 after lockout expired, got %d", recAfter.Code)
	}
}
