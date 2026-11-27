package chatops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CloudflareInboundEmail represents the payload forwarded by a Cloudflare Email Worker.
type CloudflareInboundEmail struct {
	MessageID string            `json:"messageId"`
	From      string            `json:"from"`
	To        string            `json:"to"`
	Subject   string            `json:"subject"`
	Text      string            `json:"text"`
	HTML      string            `json:"html,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Timestamp string            `json:"timestamp,omitempty"`
}

// MWXVTicketPayload is the normalized ticket creation payload dispatched to mw-xv.
type MWXVTicketPayload struct {
	TicketRef      string      `json:"ticketRef"`
	Source         string      `json:"source"`
	SenderEmail    string      `json:"senderEmail"`
	RecipientEmail string      `json:"recipientEmail"`
	Subject        string      `json:"subject"`
	Description    string      `json:"description"`
	Category       RouteDomain `json:"category"`
	Priority       string      `json:"priority"`
	Language       string      `json:"language"`
	ReceivedAt     string      `json:"receivedAt"`
}

// EmailTicketBridgeConfig configures the Cloudflare Email Worker -> mw-xv ticket + Resend ACK bridge.
type EmailTicketBridgeConfig struct {
	MWXVTicketEndpoint string
	MWXVAuthToken      string
	ResendAPIBase      string
	ResendAPIKey       string
	ResendFrom         string
	HTTPClient         *http.Client
}

// EmailBridgeOutcome summarizes the created mw-xv ticket and Resend acknowledgment delivery.
type EmailBridgeOutcome struct {
	Ticket        MWXVTicketPayload `json:"ticket"`
	MWXVTicketID  string            `json:"mwxvTicketId,omitempty"`
	ACKSent       bool              `json:"ackSent"`
	ResendEmailID string            `json:"resendEmailId,omitempty"`
}

// EmailTicketBridge converts Cloudflare Email Worker inbound payloads into mw-xv tickets and dispatches a Resend ACK.
type EmailTicketBridge struct {
	mwxvEndpoint  string
	mwxvAuthToken string
	resendAPIBase string
	resendAPIKey  string
	resendFrom    string
	httpClient    *http.Client
}

// NewEmailTicketBridge creates a configured EmailTicketBridge.
func NewEmailTicketBridge(cfg EmailTicketBridgeConfig) *EmailTicketBridge {
	resendBase := strings.TrimRight(strings.TrimSpace(cfg.ResendAPIBase), "/")
	if resendBase == "" {
		resendBase = "https://api.resend.com"
	}
	from := strings.TrimSpace(cfg.ResendFrom)
	if from == "" {
		from = "support@moonwitness.id"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &EmailTicketBridge{
		mwxvEndpoint:  strings.TrimSpace(cfg.MWXVTicketEndpoint),
		mwxvAuthToken: strings.TrimSpace(cfg.MWXVAuthToken),
		resendAPIBase: resendBase,
		resendAPIKey:  strings.TrimSpace(cfg.ResendAPIKey),
		resendFrom:    from,
		httpClient:    client,
	}
}

// TransformToMWXVTicket converts a Cloudflare Email Worker inbound email into a structured mw-xv ticket payload.
func TransformToMWXVTicket(in CloudflareInboundEmail, now time.Time) (MWXVTicketPayload, error) {
	sender := strings.TrimSpace(in.From)
	if sender == "" || !strings.Contains(sender, "@") {
		return MWXVTicketPayload{}, errors.New("valid sender email (from) is required")
	}
	subject := strings.TrimSpace(in.Subject)
	body := strings.TrimSpace(in.Text)
	if subject == "" && body == "" {
		return MWXVTicketPayload{}, errors.New("email subject or text body is required")
	}
	if subject == "" {
		subject = "(Tanpa Subjek)"
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	intent := ClassifyIntent(subject + " " + body)
	priority := "P2"
	lowerCombined := strings.ToLower(subject + " " + body)
	if strings.Contains(lowerCombined, "putus") ||
		strings.Contains(lowerCombined, "los") ||
		strings.Contains(lowerCombined, "outage") ||
		strings.Contains(lowerCombined, "down") ||
		strings.Contains(lowerCombined, "urgent") ||
		strings.Contains(lowerCombined, "darurat") {
		priority = "P1"
	}

	seed := fmt.Sprintf("%s|%s|%s|%s", sender, subject, in.MessageID, now.Format(time.RFC3339))
	hash := sha256.Sum256([]byte(seed))
	ref := "TKT-" + strings.ToUpper(hex.EncodeToString(hash[:])[:10])

	return MWXVTicketPayload{
		TicketRef:      ref,
		Source:         "cloudflare-email-worker",
		SenderEmail:    sender,
		RecipientEmail: strings.TrimSpace(in.To),
		Subject:        subject,
		Description:    body,
		Category:       intent.Domain,
		Priority:       priority,
		Language:       intent.Language,
		ReceivedAt:     now.Format(time.RFC3339),
	}, nil
}

// ProcessInbound transforms the Cloudflare email into an mw-xv ticket, posts it to mw-xv, and sends a Resend ACK email.
func (b *EmailTicketBridge) ProcessInbound(ctx context.Context, in CloudflareInboundEmail) (EmailBridgeOutcome, error) {
	ticket, err := TransformToMWXVTicket(in, time.Now().UTC())
	if err != nil {
		return EmailBridgeOutcome{}, err
	}
	outcome := EmailBridgeOutcome{Ticket: ticket}

	if b.mwxvEndpoint != "" {
		remoteID, postErr := b.postTicketToMWXV(ctx, ticket)
		if postErr != nil {
			return outcome, fmt.Errorf("mw-xv ticket dispatch: %w", postErr)
		}
		outcome.MWXVTicketID = remoteID
	}

	if b.resendAPIKey != "" {
		emailID, ackErr := b.sendResendACK(ctx, ticket)
		if ackErr != nil {
			return outcome, fmt.Errorf("resend ACK dispatch: %w", ackErr)
		}
		outcome.ACKSent = true
		outcome.ResendEmailID = emailID
	}

	return outcome, nil
}

func (b *EmailTicketBridge) postTicketToMWXV(ctx context.Context, ticket MWXVTicketPayload) (string, error) {
	payload, err := json.Marshal(ticket)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.mwxvEndpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.mwxvAuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+b.mwxvAuthToken)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("mw-xv HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		ID        string `json:"id"`
		TicketRef string `json:"ticketRef"`
	}
	_ = json.Unmarshal(raw, &parsed)
	if parsed.ID != "" {
		return parsed.ID, nil
	}
	if parsed.TicketRef != "" {
		return parsed.TicketRef, nil
	}
	return ticket.TicketRef, nil
}

func (b *EmailTicketBridge) sendResendACK(ctx context.Context, ticket MWXVTicketPayload) (string, error) {
	ackSubject := fmt.Sprintf("[%s] Tiket Diterima / Ticket Received: %s", ticket.TicketRef, ticket.Subject)
	ackBody := fmt.Sprintf(
		"Halo,\n\nTiket bantuan Anda telah kami terima dengan nomor referensi %s (Kategori: %s, Prioritas: %s).\nTim MoonWitness akan segera menindaklanjuti laporan Anda.\n\n---\nHello,\n\nYour support request has been logged under reference %s (Category: %s, Priority: %s).\nOur team will respond shortly.",
		ticket.TicketRef, ticket.Category, ticket.Priority,
		ticket.TicketRef, ticket.Category, ticket.Priority,
	)

	payload, err := json.Marshal(map[string]any{
		"from":    b.resendFrom,
		"to":      []string{ticket.SenderEmail},
		"subject": ackSubject,
		"text":    ackBody,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.resendAPIBase+"/emails", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.resendAPIKey)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("resend HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &parsed)
	return parsed.ID, nil
}
