package observatory

import (
	"context"
	"testing"
	"time"
)

func TestErrorTriageMonitorThresholdAndCooldown(t *testing.T) {
	current := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	var spikes []struct {
		service string
		sample  string
		count   int
	}

	monitor := NewErrorTriageMonitor(ErrorTriageOptions{
		Threshold: 3,
		Window:    5 * time.Minute,
		Cooldown:  5 * time.Minute,
		Now:       func() time.Time { return current },
		OnSpike: func(_ context.Context, serviceName, sampleError string, count int) error {
			spikes = append(spikes, struct {
				service string
				sample  string
				count   int
			}{service: serviceName, sample: sampleError, count: count})
			return nil
		},
	})

	ctx := context.Background()

	// Errors 1 and 2 should not trigger.
	if fired, err := monitor.RecordErrorSpan(ctx, "mw-lab", "db timeout 1"); err != nil || fired {
		t.Fatalf("expected no spike on error 1, fired=%v err=%v", fired, err)
	}
	current = current.Add(30 * time.Second)
	if fired, err := monitor.RecordErrorSpan(ctx, "mw-lab", "db timeout 2"); err != nil || fired {
		t.Fatalf("expected no spike on error 2, fired=%v err=%v", fired, err)
	}

	// Error 3 within window triggers spike.
	current = current.Add(30 * time.Second)
	if fired, err := monitor.RecordErrorSpan(ctx, "mw-lab", "db timeout 3"); err != nil || !fired {
		t.Fatalf("expected spike on error 3, fired=%v err=%v", fired, err)
	}
	if len(spikes) != 1 || spikes[0].service != "mw-lab" || spikes[0].count != 3 || spikes[0].sample != "db timeout 3" {
		t.Fatalf("unexpected spike payload: %+v", spikes)
	}

	// Error 4 within cooldown is deduplicated.
	current = current.Add(1 * time.Minute)
	if fired, err := monitor.RecordErrorSpan(ctx, "mw-lab", "db timeout 4"); err != nil || fired {
		t.Fatalf("expected cooldown dedup on error 4, fired=%v err=%v", fired, err)
	}
	if len(spikes) != 1 {
		t.Fatalf("expected still 1 spike during cooldown, got %d", len(spikes))
	}

	// Advance past cooldown & window: 3 fresh errors should trigger a second spike.
	current = current.Add(6 * time.Minute)
	_, _ = monitor.RecordErrorSpan(ctx, "mw-lab", "oom 1")
	_, _ = monitor.RecordErrorSpan(ctx, "mw-lab", "oom 2")
	fired, err := monitor.RecordErrorSpan(ctx, "mw-lab", "oom 3")
	if err != nil || !fired {
		t.Fatalf("expected second spike after cooldown, fired=%v err=%v", fired, err)
	}
	if len(spikes) != 2 || spikes[1].count != 3 || spikes[1].sample != "oom 3" {
		t.Fatalf("unexpected second spike: %+v", spikes)
	}
}
