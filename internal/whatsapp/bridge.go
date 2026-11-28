package whatsapp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/briefing"
)

// SendTextFunc abstracts outbound WhatsApp message dispatch for live and test bridges.
type SendTextFunc func(ctx context.Context, deviceID, phone, text string) (*SendResult, error)

// BridgeConfig configures the WhatsApp Morning Brief and pairing status bridge.
type BridgeConfig struct {
	DeviceID     string
	RecipientJID string
	SendText     SendTextFunc
}

// PairingStatusView exposes safe, credential-free WhatsApp device pairing telemetry.
type PairingStatusView struct {
	DeviceID       string `json:"device_id"`
	Status         string `json:"status"`
	Connected      bool   `json:"connected"`
	LoggedIn       bool   `json:"logged_in"`
	HasQR          bool   `json:"has_qr"`
	HasPairingCode bool   `json:"has_pairing_code"`
}

// BriefDeliveryResult records a 07:00 WIB Morning Executive Brief dispatch over WhatsApp.
type BriefDeliveryResult struct {
	DeviceID        string                  `json:"device_id"`
	Recipient       string                  `json:"recipient"`
	ScheduleTimeWIB string                  `json:"schedule_time_wib"`
	OverallStatus   string                  `json:"overall_status"`
	Delivered       bool                    `json:"delivered"`
	MessageID       string                  `json:"message_id,omitempty"`
	Brief           briefing.ExecutiveBrief `json:"brief"`
}

// Bridge connects whatsapp.Manager with the 07:00 WIB Morning Brief and redacted pairing telemetry.
type Bridge struct {
	manager *Manager
	cfg     BridgeConfig
}

// NewBridge creates a WhatsApp operational bridge backed by manager.
func NewBridge(manager *Manager, cfg BridgeConfig) (*Bridge, error) {
	if manager == nil {
		return nil, errors.New("whatsapp manager is required")
	}
	if strings.TrimSpace(cfg.DeviceID) == "" {
		cfg.DeviceID = "default"
	}
	return &Bridge{
		manager: manager,
		cfg:     cfg,
	}, nil
}

// NextScheduledWIB returns the next 07:00 WIB schedule timestamp after from.
func (b *Bridge) NextScheduledWIB(from time.Time) time.Time {
	return briefing.Next0700WIB(from)
}

// PairingStatus returns redacted pairing and connection state without exposing session credentials.
func (b *Bridge) PairingStatus(ctx context.Context) (PairingStatusView, error) {
	record, err := b.manager.GetDeviceStatus(ctx, b.cfg.DeviceID)
	if err != nil {
		return PairingStatusView{
			DeviceID: b.cfg.DeviceID,
			Status:   "unregistered",
		}, err
	}
	return PairingStatusView{
		DeviceID:       record.DeviceID,
		Status:         record.Status,
		Connected:      record.Connected,
		LoggedIn:       record.LoggedIn,
		HasQR:          strings.TrimSpace(record.QR) != "",
		HasPairingCode: strings.TrimSpace(record.PairingCode) != "",
	}, nil
}

// TriggerMorningBrief generates the 07:00 WIB Morning Brief and dispatches it to the recipient.
func (b *Bridge) TriggerMorningBrief(
	ctx context.Context,
	metrics briefing.MorningMetrics,
) (BriefDeliveryResult, error) {
	recipient := strings.TrimSpace(b.cfg.RecipientJID)
	if recipient == "" {
		return BriefDeliveryResult{}, errors.New("recipient JID or phone is required")
	}

	brief := briefing.GenerateMorningBrief(metrics)
	sender := b.cfg.SendText
	if sender == nil {
		sender = func(sCtx context.Context, devID, phone, text string) (*SendResult, error) {
			res, err := b.manager.SendText(sCtx, devID, phone, text, "", nil)
			return &res, err
		}
	}

	sendRes, err := sender(ctx, b.cfg.DeviceID, recipient, brief.Markdown)
	if err != nil {
		return BriefDeliveryResult{
			DeviceID:        b.cfg.DeviceID,
			Recipient:       recipient,
			ScheduleTimeWIB: brief.ScheduleTimeWIB,
			OverallStatus:   brief.OverallStatus,
			Delivered:       false,
			Brief:           brief,
		}, err
	}

	msgID := ""
	if sendRes != nil {
		msgID = sendRes.MessageID
	}

	return BriefDeliveryResult{
		DeviceID:        b.cfg.DeviceID,
		Recipient:       recipient,
		ScheduleTimeWIB: brief.ScheduleTimeWIB,
		OverallStatus:   brief.OverallStatus,
		Delivered:       true,
		MessageID:       msgID,
		Brief:           brief,
	}, nil
}

var sensitiveFieldMarkers = []string{
	"secret",
	"token",
	"password",
	"pass",
	"credential",
	"session",
	"private_key",
	"noise_key",
	"identity_key",
	"auth",
}

// RedactCredentialFields returns a shallow copy of fields with any sensitive credential keys masked.
func RedactCredentialFields(fields map[string]any) map[string]any {
	sanitized := make(map[string]any, len(fields))
	for k, v := range fields {
		lower := strings.ToLower(k)
		redacted := false
		for _, marker := range sensitiveFieldMarkers {
			if strings.Contains(lower, marker) {
				redacted = true
				break
			}
		}
		if redacted {
			sanitized[k] = "[REDACTED]"
		} else {
			sanitized[k] = v
		}
	}
	return sanitized
}
