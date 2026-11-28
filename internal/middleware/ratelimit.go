package middleware

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

// RateLimitConfig configures the sliding-window IP rate limiter and brute-force lockout guard.
type RateLimitConfig struct {
	// WindowDuration is the sliding window length for general request rate limiting.
	WindowDuration time.Duration
	// MaxRequests is the maximum allowed requests per IP within WindowDuration.
	MaxRequests int
	// FailureWindow is the observation window for failed authentication/action attempts.
	FailureWindow time.Duration
	// MaxFailures is the number of failed attempts (HTTP 401/403) before locking out the IP.
	MaxFailures int
	// LockoutDuration is how long an IP remains locked out once MaxFailures is reached.
	LockoutDuration time.Duration
	// NowFunc allows deterministic clock injection in tests.
	NowFunc func() time.Time
}

type ipBucket struct {
	requests     []time.Time
	failures     []time.Time
	lockedUntil  time.Time
	lastActivity time.Time
}

// SlidingWindowLimiter enforces per-IP sliding-window request rate limits and brute-force lockouts.
type SlidingWindowLimiter struct {
	mu      sync.Mutex
	cfg     RateLimitConfig
	buckets map[string]*ipBucket
}

// NewSlidingWindowLimiter creates a configured SlidingWindowLimiter with safe defaults.
func NewSlidingWindowLimiter(cfg RateLimitConfig) *SlidingWindowLimiter {
	if cfg.WindowDuration <= 0 {
		cfg.WindowDuration = time.Minute
	}
	if cfg.MaxRequests <= 0 {
		cfg.MaxRequests = 120
	}
	if cfg.FailureWindow <= 0 {
		cfg.FailureWindow = 5 * time.Minute
	}
	if cfg.MaxFailures <= 0 {
		cfg.MaxFailures = 5
	}
	if cfg.LockoutDuration <= 0 {
		cfg.LockoutDuration = 15 * time.Minute
	}
	if cfg.NowFunc == nil {
		cfg.NowFunc = time.Now
	}
	return &SlidingWindowLimiter{
		cfg:     cfg,
		buckets: make(map[string]*ipBucket),
	}
}

// Allow checks whether clientIP is permitted to proceed at the current timestamp.
// It returns allowed=false with retryAfter > 0 if the IP is locked out or over the sliding-window quota.
func (l *SlidingWindowLimiter) Allow(clientIP string) (allowed bool, reason string, retryAfter time.Duration) {
	ip := normalizeIP(clientIP)
	now := l.cfg.NowFunc()

	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.getOrCreateBucket(ip, now)
	if now.Before(b.lockedUntil) {
		return false, "brute_force_lockout", b.lockedUntil.Sub(now)
	}

	cutoff := now.Add(-l.cfg.WindowDuration)
	b.requests = pruneTimestamps(b.requests, cutoff)
	if len(b.requests) >= l.cfg.MaxRequests {
		oldest := b.requests[0]
		wait := oldest.Add(l.cfg.WindowDuration).Sub(now)
		if wait <= 0 {
			wait = time.Second
		}
		return false, "rate_limit_exceeded", wait
	}

	b.requests = append(b.requests, now)
	b.lastActivity = now
	return true, "", 0
}

// RecordFailure logs a failed authentication or sensitive operation attempt for clientIP,
// triggering a brute-force lockout when failures within FailureWindow reach MaxFailures.
func (l *SlidingWindowLimiter) RecordFailure(clientIP string) (locked bool, lockedUntil time.Time) {
	ip := normalizeIP(clientIP)
	now := l.cfg.NowFunc()

	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.getOrCreateBucket(ip, now)
	failCutoff := now.Add(-l.cfg.FailureWindow)
	b.failures = pruneTimestamps(b.failures, failCutoff)
	b.failures = append(b.failures, now)
	b.lastActivity = now

	if len(b.failures) >= l.cfg.MaxFailures {
		b.lockedUntil = now.Add(l.cfg.LockoutDuration)
		return true, b.lockedUntil
	}
	return false, b.lockedUntil
}

// RecordSuccess clears accumulated brute-force failure counters for clientIP after a valid login/OTP.
func (l *SlidingWindowLimiter) RecordSuccess(clientIP string) {
	ip := normalizeIP(clientIP)
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[ip]; ok {
		b.failures = nil
	}
}

// Middleware returns an Echo middleware enforcing sliding-window rate limits and brute-force lockouts.
// Responses with HTTP 401 or 403 automatically count toward the brute-force failure threshold.
func (l *SlidingWindowLimiter) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ip := extractClientIP(c)
			allowed, reason, retryAfter := l.Allow(ip)
			if !allowed {
				retrySecs := int(math.Ceil(retryAfter.Seconds()))
				if retrySecs < 1 {
					retrySecs = 1
				}
				c.Response().Header().Set("Retry-After", fmt.Sprintf("%d", retrySecs))
				return c.JSON(http.StatusTooManyRequests, map[string]any{
					"error":             reason,
					"retryAfterSeconds": retrySecs,
				})
			}

			err := next(c)
			status := c.Response().Status
			if err != nil {
				if he, ok := err.(*echo.HTTPError); ok {
					status = he.Code
				}
			}
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				l.RecordFailure(ip)
			}
			return err
		}
	}
}

func (l *SlidingWindowLimiter) getOrCreateBucket(ip string, now time.Time) *ipBucket {
	b, exists := l.buckets[ip]
	if !exists {
		b = &ipBucket{lastActivity: now}
		l.buckets[ip] = b
	}
	return b
}

func pruneTimestamps(ts []time.Time, cutoff time.Time) []time.Time {
	idx := 0
	for idx < len(ts) && !ts[idx].After(cutoff) {
		idx++
	}
	if idx == 0 {
		return ts
	}
	remaining := make([]time.Time, len(ts)-idx)
	copy(remaining, ts[idx:])
	return remaining
}

func normalizeIP(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "0.0.0.0"
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil && host != "" {
		return host
	}
	return trimmed
}

func extractClientIP(c echo.Context) string {
	if cfIP := strings.TrimSpace(c.Request().Header.Get("CF-Connecting-IP")); cfIP != "" {
		return normalizeIP(cfIP)
	}
	return normalizeIP(c.RealIP())
}
