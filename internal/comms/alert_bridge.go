package comms

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultAlertCooldown = 15 * time.Minute

// AlertIncident describes a high-severity incident from Disk Doctor or World Radar.
type AlertIncident struct {
	Source    string            `json:"source"`
	Severity  string            `json:"severity"`
	DedupeKey string            `json:"dedupeKey,omitempty"`
	Title     string            `json:"title"`
	Summary   string            `json:"summary"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// AlertDispatchResult describes the outcome of routing an AlertIncident through AlertBridge.
type AlertDispatchResult struct {
	Dispatched        bool             `json:"dispatched"`
	Suppressed        bool             `json:"suppressed"`
	SuppressionReason string           `json:"suppressionReason,omitempty"`
	DedupeKey         string           `json:"dedupeKey"`
	Severity          string           `json:"severity"`
	ChannelCount      int              `json:"channelCount"`
	Deliveries        []DeliveryRecord `json:"deliveries"`
	DispatchedAt      string           `json:"dispatchedAt"`
}

// AlertBridge deduplicates and routes P0/CRITICAL incidents to configured Telegram/Discord channels.
type AlertBridge struct {
	manager  *Manager
	cooldown time.Duration
	nowFunc  func() time.Time

	mu       sync.Mutex
	lastSent map[string]time.Time
}

// NewAlertBridge constructs a cooldown-deduplicated alert router backed by Manager.
func NewAlertBridge(manager *Manager, cooldown time.Duration) *AlertBridge {
	effectiveCooldown := cooldown
	if effectiveCooldown <= 0 {
		effectiveCooldown = defaultAlertCooldown
	}
	return &AlertBridge{
		manager:  manager,
		cooldown: effectiveCooldown,
		nowFunc:  time.Now,
		lastSent: make(map[string]time.Time),
	}
}

// Dispatch evaluates severity and cooldown deduplication, then broadcasts P0/CRITICAL alerts.
func (b *AlertBridge) Dispatch(ctx context.Context, incident AlertIncident) (AlertDispatchResult, error) {
	if b == nil || b.manager == nil {
		return AlertDispatchResult{}, errors.New("alert bridge manager is not initialized")
	}

	source := strings.TrimSpace(incident.Source)
	if source == "" {
		source = "system"
	}
	title := strings.TrimSpace(incident.Title)
	summary := strings.TrimSpace(incident.Summary)
	now := b.nowFunc().UTC()
	if title == "" && summary == "" {
		return AlertDispatchResult{
			Dispatched:        false,
			Suppressed:        true,
			SuppressionReason: "invalid_alert",
			DedupeKey:         strings.ToLower(source + ":" + title),
			Severity:          "INFO",
			Deliveries:        []DeliveryRecord{},
			DispatchedAt:      now.Format(time.RFC3339Nano),
		}, nil
	}

	normalizedSeverity := normalizeAlertSeverity(incident.Severity)
	dedupeKey := strings.TrimSpace(incident.DedupeKey)
	if dedupeKey == "" {
		dedupeKey = strings.ToLower(source + ":" + title)
	}
	if !isCriticalSeverity(normalizedSeverity) {
		return AlertDispatchResult{
			Dispatched:        false,
			Suppressed:        true,
			SuppressionReason: "below_severity_threshold",
			DedupeKey:         dedupeKey,
			Severity:          normalizedSeverity,
			Deliveries:        []DeliveryRecord{},
			DispatchedAt:      now.Format(time.RFC3339Nano),
		}, nil
	}

	b.mu.Lock()
	if previousTime, exists := b.lastSent[dedupeKey]; exists && now.Sub(previousTime) < b.cooldown {
		b.mu.Unlock()
		return AlertDispatchResult{
			Dispatched:        false,
			Suppressed:        true,
			SuppressionReason: "cooldown_active",
			DedupeKey:         dedupeKey,
			Severity:          normalizedSeverity,
			Deliveries:        []DeliveryRecord{},
			DispatchedAt:      now.Format(time.RFC3339Nano),
		}, nil
	}
	b.lastSent[dedupeKey] = now
	b.mu.Unlock()

	formattedTitle := fmt.Sprintf("[%s // %s] %s", normalizedSeverity, strings.ToUpper(source), title)
	formattedBody := formatAlertBody(source, normalizedSeverity, summary, incident.Metadata)

	deliveries, err := b.manager.Broadcast(ctx, formattedTitle, formattedBody, "P0")
	if err != nil {
		return AlertDispatchResult{
			Dispatched:   false,
			DedupeKey:    dedupeKey,
			Severity:     normalizedSeverity,
			Deliveries:   deliveries,
			DispatchedAt: now.Format(time.RFC3339Nano),
		}, err
	}

	return AlertDispatchResult{
		Dispatched:   len(deliveries) > 0,
		DedupeKey:    dedupeKey,
		Severity:     normalizedSeverity,
		ChannelCount: len(deliveries),
		Deliveries:   deliveries,
		DispatchedAt: now.Format(time.RFC3339Nano),
	}, nil
}

func normalizeAlertSeverity(raw string) string {
	upper := strings.ToUpper(strings.TrimSpace(raw))
	if upper == "" {
		return "INFO"
	}
	return upper
}

func isCriticalSeverity(normalized string) bool {
	return normalized == "P0" || normalized == "CRITICAL"
}

func formatAlertBody(source, severity, summary string, metadata map[string]string) string {
	var builder strings.Builder
	if summary != "" {
		builder.WriteString(summary)
	} else {
		fmt.Fprintf(&builder, "Critical %s alert triggered (%s).", source, severity)
	}
	if len(metadata) > 0 {
		keys := make([]string, 0, len(metadata))
		for k := range metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		builder.WriteString("\n")
		for _, k := range keys {
			fmt.Fprintf(&builder, "\n- %s: %s", k, metadata[k])
		}
	}
	return builder.String()
}
