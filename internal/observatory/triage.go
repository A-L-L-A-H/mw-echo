package observatory

import (
	"context"
	"strings"
	"sync"
	"time"
)

// ErrorTriageOptions configures rolling error spike detection and cooldown deduplication.
type ErrorTriageOptions struct {
	Threshold int
	Window    time.Duration
	Cooldown  time.Duration
	Now       func() time.Time
	OnSpike   func(ctx context.Context, serviceName, sampleError string, count int) error
}

// ErrorTriageMonitor tracks rolling error counts per service and fires OnSpike when a service
// breaches the configured threshold within the sliding window, subject to cooldown deduplication.
type ErrorTriageMonitor struct {
	threshold int
	window    time.Duration
	cooldown  time.Duration
	nowFn     func() time.Time
	onSpike   func(ctx context.Context, serviceName, sampleError string, count int) error

	mu         sync.Mutex
	timestamps map[string][]time.Time
	lastFired  map[string]time.Time
}

// NewErrorTriageMonitor constructs an ErrorTriageMonitor with sane defaults (threshold=3, window=5m, cooldown=5m).
func NewErrorTriageMonitor(opts ErrorTriageOptions) *ErrorTriageMonitor {
	threshold := opts.Threshold
	if threshold <= 0 {
		threshold = 3
	}
	window := opts.Window
	if window <= 0 {
		window = 5 * time.Minute
	}
	cooldown := opts.Cooldown
	if cooldown <= 0 {
		cooldown = window
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	return &ErrorTriageMonitor{
		threshold:  threshold,
		window:     window,
		cooldown:   cooldown,
		nowFn:      nowFn,
		onSpike:    opts.OnSpike,
		timestamps: make(map[string][]time.Time),
		lastFired:  make(map[string]time.Time),
	}
}

// RecordErrorSpan records an error span for serviceName and returns true if OnSpike was triggered.
func (m *ErrorTriageMonitor) RecordErrorSpan(ctx context.Context, serviceName, errorMessage string) (bool, error) {
	service := strings.TrimSpace(serviceName)
	if service == "" {
		service = "unknown"
	}
	sample := strings.TrimSpace(errorMessage)

	m.mu.Lock()
	now := m.nowFn()
	cutoff := now.Add(-m.window)

	existing := m.timestamps[service]
	pruned := existing[:0]
	for _, ts := range existing {
		if !ts.Before(cutoff) {
			pruned = append(pruned, ts)
		}
	}
	pruned = append(pruned, now)
	m.timestamps[service] = pruned

	count := len(pruned)
	shouldFire := false
	if count >= m.threshold {
		prevFired, hasFired := m.lastFired[service]
		if !hasFired || now.Sub(prevFired) >= m.cooldown {
			m.lastFired[service] = now
			shouldFire = true
		}
	}
	callback := m.onSpike
	m.mu.Unlock()

	if !shouldFire {
		return false, nil
	}
	if callback != nil {
		if err := callback(ctx, service, sample, count); err != nil {
			return true, err
		}
	}
	return true, nil
}
