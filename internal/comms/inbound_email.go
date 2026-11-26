package comms

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	inboundEmailSecretEnv   = "MW_ECHO_INBOUND_EMAIL_SECRET"
	inboundEmailMaxBody     = 1 << 20
	inboundEmailSignatureHd = "X-Webhook-Signature"
	inboundEmailSecretHd    = "X-Webhook-Secret"
	supportTicketPriority   = "P1"
)

// InboundEmail is the Cloudflare Email Routing worker payload.
type InboundEmail struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
}

// SupportTicket is the structured record created from an inbound email.
type SupportTicket struct {
	ID         string `json:"id"`
	Sender     string `json:"from"`
	Recipient  string `json:"to"`
	Subject    string `json:"subject"`
	Body       string `json:"text"`
	Priority   string `json:"priority"`
	Status     string `json:"status"`
	ReceivedAt string `json:"receivedAt"`
}

var inboundSecretState struct {
	mu    sync.RWMutex
	byMgr map[*Manager]string
}

// SetInboundEmailSecret configures the shared secret that authenticates inbound email webhooks.
func (m *Manager) SetInboundEmailSecret(secret string) {
	inboundSecretState.mu.Lock()
	defer inboundSecretState.mu.Unlock()
	if inboundSecretState.byMgr == nil {
		inboundSecretState.byMgr = make(map[*Manager]string)
	}
	inboundSecretState.byMgr[m] = strings.TrimSpace(secret)
}

func (m *Manager) inboundEmailSecret() string {
	inboundSecretState.mu.RLock()
	configured := inboundSecretState.byMgr[m]
	inboundSecretState.mu.RUnlock()
	if configured != "" {
		return configured
	}
	return strings.TrimSpace(os.Getenv(inboundEmailSecretEnv))
}

func verifyInboundEmailAuth(secret string, header http.Header, rawBody []byte) bool {
	if secret == "" {
		return false
	}
	if provided := header.Get(inboundEmailSecretHd); provided != "" {
		return subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) == 1
	}
	signature := strings.TrimPrefix(strings.TrimSpace(header.Get(inboundEmailSignatureHd)), "sha256=")
	if signature == "" {
		return false
	}
	decoded, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawBody)
	return hmac.Equal(decoded, mac.Sum(nil))
}

func (m *Manager) ensureTicketTable(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS comms_support_tickets (
		id TEXT PRIMARY KEY,
		sender TEXT NOT NULL,
		recipient TEXT NOT NULL,
		subject TEXT NOT NULL,
		body TEXT NOT NULL,
		priority TEXT NOT NULL,
		status TEXT NOT NULL,
		received_at TEXT NOT NULL
	)`)
	return err
}

// CreateSupportTicket persists a ticket and dispatches a P1 broadcast alert.
func (m *Manager) CreateSupportTicket(ctx context.Context, mail InboundEmail) (SupportTicket, error) {
	sender := strings.TrimSpace(mail.From)
	subject := strings.TrimSpace(mail.Subject)
	body := strings.TrimSpace(mail.Text)
	if sender == "" || (subject == "" && body == "") {
		return SupportTicket{}, errors.New("from and subject or text are required")
	}
	if subject == "" {
		subject = "(no subject)"
	}
	if err := m.ensureTicketTable(ctx); err != nil {
		return SupportTicket{}, err
	}
	ticket := SupportTicket{
		ID:         generateID("tkt"),
		Sender:     sender,
		Recipient:  strings.TrimSpace(mail.To),
		Subject:    subject,
		Body:       body,
		Priority:   supportTicketPriority,
		Status:     "open",
		ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := m.db.ExecContext(ctx, `INSERT INTO comms_support_tickets
		(id, sender, recipient, subject, body, priority, status, received_at) VALUES (?,?,?,?,?,?,?,?)`,
		ticket.ID, ticket.Sender, ticket.Recipient, ticket.Subject, ticket.Body, ticket.Priority, ticket.Status, ticket.ReceivedAt); err != nil {
		return SupportTicket{}, err
	}
	alertTitle := fmt.Sprintf("Support ticket %s: %s", ticket.ID, ticket.Subject)
	alertBody := fmt.Sprintf("From: %s\nTo: %s\n\n%s", ticket.Sender, ticket.Recipient, ticket.Body)
	// Alert failure (e.g. no channels configured) must not lose the persisted ticket.
	_, _ = m.Broadcast(ctx, alertTitle, alertBody, supportTicketPriority)
	return ticket, nil
}

func (m *Manager) handleInboundEmail(c echo.Context) error {
	rawBody, err := io.ReadAll(io.LimitReader(c.Request().Body, inboundEmailMaxBody))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "unreadable body"})
	}
	if !verifyInboundEmailAuth(m.inboundEmailSecret(), c.Request().Header, rawBody) {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	var mail InboundEmail
	if err := json.Unmarshal(rawBody, &mail); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid inbound email JSON"})
	}
	ticket, err := m.CreateSupportTicket(c.Request().Context(), mail)
	if err != nil {
		return c.JSON(http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, ticket)
}
