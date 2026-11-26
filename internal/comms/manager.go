package comms

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/storage"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

const (
	// KindTelegram identifies a Telegram Bot API channel.
	KindTelegram = "telegram"
	// KindDiscord identifies a Discord Webhook channel.
	KindDiscord = "discord"

	// DeliveryStatusSent indicates an outbound message was accepted by the provider.
	DeliveryStatusSent = "sent"
	// DeliveryStatusFailed indicates an outbound message failed delivery.
	DeliveryStatusFailed = "failed"

	defaultTelegramAPIBase = "https://api.telegram.org"
	defaultHTTPTimeout     = 15 * time.Second
	defaultDeliveryLimit   = 50
	maxDeliveryLimit       = 500

	discordUsername     = "MW-ECHO // R!OTS"
	discordColorP0      = 0xFF0055
	discordColorP1      = 0xFF9900
	discordColorP2      = 0xFFE600
	discordColorDefault = 0x00F5FF
)

var (
	// ErrNotFound is returned when a requested channel or matching active channel does not exist.
	ErrNotFound = errors.New("comms channel not found")
	// ErrValidation is returned when channel or message input fails validation.
	ErrValidation = errors.New("comms validation failed")
)

// Options configures the Omnichannel Comms Hub manager.
type Options struct {
	HTTPClient      *http.Client
	TelegramAPIBase string
}

// ChannelRecord represents a configured outbound communication channel with redacted secrets.
type ChannelRecord struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	SecretHint       string `json:"secretHint"`
	SecretConfigured bool   `json:"secretConfigured"`
	TargetRef        string `json:"targetRef"`
	Enabled          bool   `json:"enabled"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

// ChannelUpsertInput carries fields to create or update a communication channel.
type ChannelUpsertInput struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Secret    string `json:"secret"`
	TargetRef string `json:"targetRef"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

// SendInput carries parameters for sending an alert or message through a configured channel.
type SendInput struct {
	ChannelID    string `json:"channelId"`
	Kind         string `json:"kind"`
	TargetRef    string `json:"targetRef"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	PriorityTier string `json:"priorityTier"`
}

// DeliveryRecord represents a persisted outbound delivery attempt in comms_deliveries.
type DeliveryRecord struct {
	ID        string `json:"id"`
	ChannelID string `json:"channelId"`
	Kind      string `json:"kind"`
	TargetRef string `json:"targetRef"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	ErrorMsg  string `json:"errorMsg"`
	CreatedAt string `json:"createdAt"`
}

// Manager manages encrypted comms channels and outbound deliveries for Telegram and Discord.
type Manager struct {
	hub             *store.HubStore
	db              *sql.DB
	secretBox       *storage.SecretBox
	httpClient      *http.Client
	telegramAPIBase string
}

type channelWithSecret struct {
	ChannelRecord
	SecretEnc string
}

// NewManager opens the AES-256-GCM SecretBox, migrates comms SQLite tables, and returns a Manager.
func NewManager(hub *store.HubStore, opts Options) (*Manager, error) {
	if hub == nil || hub.DB() == nil {
		return nil, errors.New("comms: hub store is required")
	}

	secretBox, err := storage.OpenSecretBox(hub.DataDir())
	if err != nil {
		return nil, fmt.Errorf("comms open secret box: %w", err)
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}

	tgBase := strings.TrimRight(strings.TrimSpace(opts.TelegramAPIBase), "/")
	if tgBase == "" {
		tgBase = defaultTelegramAPIBase
	}

	mgr := &Manager{
		hub:             hub,
		db:              hub.DB(),
		secretBox:       secretBox,
		httpClient:      client,
		telegramAPIBase: tgBase,
	}

	if err := mgr.migrate(); err != nil {
		return nil, fmt.Errorf("comms migrate: %w", err)
	}
	return mgr, nil
}

func (m *Manager) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS comms_channels (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('telegram','discord')),
  name TEXT NOT NULL,
  secret_enc TEXT NOT NULL,
  secret_hint TEXT NOT NULL DEFAULT '',
  target_ref TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_comms_channels_kind_enabled ON comms_channels(kind, enabled);

CREATE TABLE IF NOT EXISTS comms_deliveries (
  id TEXT PRIMARY KEY,
  channel_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  target_ref TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  status TEXT NOT NULL,
  error_msg TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_comms_deliveries_created_at ON comms_deliveries(created_at DESC);
`
	_, err := m.db.Exec(schema)
	return err
}

// UpsertChannel creates or updates an encrypted communication channel.
// Empty Secret on an existing channel preserves its stored secret_enc and secret_hint.
func (m *Manager) UpsertChannel(ctx context.Context, input ChannelUpsertInput) (ChannelRecord, error) {
	kind, err := normalizeKind(input.Kind)
	if err != nil {
		return ChannelRecord{}, err
	}

	channelName := strings.TrimSpace(input.Name)
	if channelName == "" {
		return ChannelRecord{}, fmt.Errorf("%w: channel name is required", ErrValidation)
	}

	channelID := strings.TrimSpace(input.ID)
	rawSecret := strings.TrimSpace(input.Secret)
	targetRef := strings.TrimSpace(input.TargetRef)

	var existingSecretEnc string
	var existingSecretHint string
	var existingEnabledInt int
	var existingCreatedAt string
	hasExisting := false

	if channelID != "" {
		err := m.db.QueryRowContext(ctx, `
SELECT secret_enc, secret_hint, enabled, created_at
FROM comms_channels WHERE id = ?`, channelID).Scan(
			&existingSecretEnc, &existingSecretHint, &existingEnabledInt, &existingCreatedAt,
		)
		if err == nil {
			hasExisting = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return ChannelRecord{}, err
		}
	} else {
		channelID = generateID("comms")
	}

	secretEnc := existingSecretEnc
	secretHint := existingSecretHint
	if rawSecret != "" {
		if kind == KindDiscord {
			if err := validateHTTPURL(rawSecret); err != nil {
				return ChannelRecord{}, fmt.Errorf("%w: invalid discord webhook url: %v", ErrValidation, err)
			}
		}
		encrypted, err := m.secretBox.Encrypt([]byte(rawSecret))
		if err != nil {
			return ChannelRecord{}, fmt.Errorf("encrypt channel secret: %w", err)
		}
		secretEnc = encrypted
		secretHint = maskSecretHint(rawSecret)
	} else if !hasExisting || strings.TrimSpace(secretEnc) == "" {
		return ChannelRecord{}, fmt.Errorf("%w: secret is required for new %s channel", ErrValidation, kind)
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	} else if hasExisting {
		enabled = existingEnabledInt == 1
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	createdAt := now
	if hasExisting && existingCreatedAt != "" {
		createdAt = existingCreatedAt
	}

	_, err = m.db.ExecContext(ctx, `
INSERT INTO comms_channels (id, kind, name, secret_enc, secret_hint, target_ref, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  kind = excluded.kind,
  name = excluded.name,
  secret_enc = excluded.secret_enc,
  secret_hint = excluded.secret_hint,
  target_ref = excluded.target_ref,
  enabled = excluded.enabled,
  updated_at = excluded.updated_at
`, channelID, kind, channelName, secretEnc, secretHint, targetRef, boolToInt(enabled), createdAt, now)
	if err != nil {
		return ChannelRecord{}, err
	}

	return ChannelRecord{
		ID:               channelID,
		Kind:             kind,
		Name:             channelName,
		SecretHint:       secretHint,
		SecretConfigured: secretEnc != "",
		TargetRef:        targetRef,
		Enabled:          enabled,
		CreatedAt:        createdAt,
		UpdatedAt:        now,
	}, nil
}

// ListChannels returns all configured channels with redacted secret hints.
func (m *Manager) ListChannels(ctx context.Context) ([]ChannelRecord, error) {
	rowsResult, err := m.db.QueryContext(ctx, `
SELECT id, kind, name, secret_enc, secret_hint, target_ref, enabled, created_at, updated_at
FROM comms_channels
ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rowsResult.Close()

	channelsList := make([]ChannelRecord, 0)
	for rowsResult.Next() {
		var rec ChannelRecord
		var secretEnc string
		var enabledInt int
		if err := rowsResult.Scan(
			&rec.ID, &rec.Kind, &rec.Name, &secretEnc, &rec.SecretHint, &rec.TargetRef, &enabledInt, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, err
		}
		rec.SecretConfigured = strings.TrimSpace(secretEnc) != ""
		rec.Enabled = enabledInt == 1
		channelsList = append(channelsList, rec)
	}
	return channelsList, rowsResult.Err()
}

// GetChannel retrieves a single channel by ID with its secret redacted.
func (m *Manager) GetChannel(ctx context.Context, id string) (ChannelRecord, error) {
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return ChannelRecord{}, fmt.Errorf("%w: channel id is required", ErrValidation)
	}

	var rec ChannelRecord
	var secretEnc string
	var enabledInt int
	err := m.db.QueryRowContext(ctx, `
SELECT id, kind, name, secret_enc, secret_hint, target_ref, enabled, created_at, updated_at
FROM comms_channels
WHERE id = ?`, trimmedID).Scan(
		&rec.ID, &rec.Kind, &rec.Name, &secretEnc, &rec.SecretHint, &rec.TargetRef, &enabledInt, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ChannelRecord{}, ErrNotFound
		}
		return ChannelRecord{}, err
	}
	rec.SecretConfigured = strings.TrimSpace(secretEnc) != ""
	rec.Enabled = enabledInt == 1
	return rec, nil
}

// DeleteChannel deletes a configured channel by ID.
func (m *Manager) DeleteChannel(ctx context.Context, id string) error {
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return fmt.Errorf("%w: channel id is required", ErrValidation)
	}

	execResult, err := m.db.ExecContext(ctx, `DELETE FROM comms_channels WHERE id = ?`, trimmedID)
	if err != nil {
		return err
	}
	affected, _ := execResult.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Send dispatches a message to the specified channel (or first enabled channel matching Kind)
// and always persists a delivery ledger entry in comms_deliveries.
func (m *Manager) Send(ctx context.Context, input SendInput) (DeliveryRecord, error) {
	title := strings.TrimSpace(input.Title)
	body := strings.TrimSpace(input.Body)
	if title == "" && body == "" {
		return DeliveryRecord{}, fmt.Errorf("%w: title or body is required", ErrValidation)
	}

	channel, err := m.resolveChannelForSend(ctx, input.ChannelID, input.Kind)
	if err != nil {
		return DeliveryRecord{}, err
	}

	targetRef := strings.TrimSpace(input.TargetRef)
	if targetRef == "" {
		targetRef = channel.TargetRef
	}

	delivery := DeliveryRecord{
		ID:        generateID("deliv"),
		ChannelID: channel.ID,
		Kind:      channel.Kind,
		TargetRef: targetRef,
		Title:     title,
		Body:      body,
		Status:    DeliveryStatusSent,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}

	secretBytes, decryptErr := m.secretBox.Decrypt(channel.SecretEnc)
	if decryptErr != nil {
		delivery.Status = DeliveryStatusFailed
		delivery.ErrorMsg = fmt.Sprintf("decrypt channel secret: %v", decryptErr)
		if persistErr := m.insertDelivery(ctx, delivery); persistErr != nil {
			return delivery, persistErr
		}
		return delivery, fmt.Errorf("decrypt channel secret: %w", decryptErr)
	}
	plainSecret := strings.TrimSpace(string(secretBytes))

	var sendErr error
	switch channel.Kind {
	case KindTelegram:
		sendErr = m.sendTelegram(ctx, plainSecret, targetRef, title, body)
	case KindDiscord:
		sendErr = m.sendDiscord(ctx, plainSecret, title, body, input.PriorityTier)
	default:
		sendErr = fmt.Errorf("%w: unsupported channel kind %q", ErrValidation, channel.Kind)
	}

	if sendErr != nil {
		delivery.Status = DeliveryStatusFailed
		delivery.ErrorMsg = sendErr.Error()
	}

	if persistErr := m.insertDelivery(ctx, delivery); persistErr != nil {
		return delivery, persistErr
	}
	return delivery, sendErr
}

// Broadcast fans out a message across all enabled channels and returns every persisted delivery record.
func (m *Manager) Broadcast(ctx context.Context, title, body, priorityTier string) ([]DeliveryRecord, error) {
	trimmedTitle := strings.TrimSpace(title)
	trimmedBody := strings.TrimSpace(body)
	if trimmedTitle == "" && trimmedBody == "" {
		return nil, fmt.Errorf("%w: title or body is required", ErrValidation)
	}

	rowsResult, err := m.db.QueryContext(ctx, `
SELECT id, kind, target_ref
FROM comms_channels
WHERE enabled = 1
ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}

	type broadcastTarget struct {
		ID        string
		Kind      string
		TargetRef string
	}
	targets := make([]broadcastTarget, 0)
	for rowsResult.Next() {
		var tgt broadcastTarget
		if err := rowsResult.Scan(&tgt.ID, &tgt.Kind, &tgt.TargetRef); err != nil {
			_ = rowsResult.Close()
			return nil, err
		}
		targets = append(targets, tgt)
	}
	if err := rowsResult.Err(); err != nil {
		_ = rowsResult.Close()
		return nil, err
	}
	_ = rowsResult.Close()

	deliveriesList := make([]DeliveryRecord, 0, len(targets))
	for _, tgt := range targets {
		deliveryRec, _ := m.Send(ctx, SendInput{
			ChannelID:    tgt.ID,
			Kind:         tgt.Kind,
			TargetRef:    tgt.TargetRef,
			Title:        trimmedTitle,
			Body:         trimmedBody,
			PriorityTier: priorityTier,
		})
		if deliveryRec.ID != "" {
			deliveriesList = append(deliveriesList, deliveryRec)
		}
	}
	return deliveriesList, nil
}

// ListDeliveries returns recent outbound delivery ledger records ordered newest-first.
func (m *Manager) ListDeliveries(ctx context.Context, limit int) ([]DeliveryRecord, error) {
	if limit <= 0 {
		limit = defaultDeliveryLimit
	}
	if limit > maxDeliveryLimit {
		limit = maxDeliveryLimit
	}

	rowsResult, err := m.db.QueryContext(ctx, `
SELECT id, channel_id, kind, target_ref, title, body, status, error_msg, created_at
FROM comms_deliveries
ORDER BY created_at DESC, rowid DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rowsResult.Close()

	deliveriesList := make([]DeliveryRecord, 0)
	for rowsResult.Next() {
		var rec DeliveryRecord
		if err := rowsResult.Scan(
			&rec.ID, &rec.ChannelID, &rec.Kind, &rec.TargetRef, &rec.Title, &rec.Body, &rec.Status, &rec.ErrorMsg, &rec.CreatedAt,
		); err != nil {
			return nil, err
		}
		deliveriesList = append(deliveriesList, rec)
	}
	return deliveriesList, rowsResult.Err()
}

func (m *Manager) resolveChannelForSend(ctx context.Context, channelID, rawKind string) (channelWithSecret, error) {
	trimmedID := strings.TrimSpace(channelID)
	var querySQL string
	var queryArg string

	if trimmedID != "" {
		querySQL = `
SELECT id, kind, name, secret_enc, secret_hint, target_ref, enabled, created_at, updated_at
FROM comms_channels
WHERE id = ?
LIMIT 1`
		queryArg = trimmedID
	} else {
		kind, err := normalizeKind(rawKind)
		if err != nil {
			return channelWithSecret{}, fmt.Errorf("%w: channelId or valid kind is required", ErrValidation)
		}
		querySQL = `
SELECT id, kind, name, secret_enc, secret_hint, target_ref, enabled, created_at, updated_at
FROM comms_channels
WHERE kind = ? AND enabled = 1
ORDER BY created_at ASC, id ASC
LIMIT 1`
		queryArg = kind
	}

	var ch channelWithSecret
	var enabledInt int
	err := m.db.QueryRowContext(ctx, querySQL, queryArg).Scan(
		&ch.ID, &ch.Kind, &ch.Name, &ch.SecretEnc, &ch.SecretHint, &ch.TargetRef, &enabledInt, &ch.CreatedAt, &ch.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return channelWithSecret{}, ErrNotFound
		}
		return channelWithSecret{}, err
	}
	ch.SecretConfigured = strings.TrimSpace(ch.SecretEnc) != ""
	ch.Enabled = enabledInt == 1

	if trimmedID != "" && !ch.Enabled {
		return channelWithSecret{}, fmt.Errorf("%w: channel %q is disabled", ErrValidation, ch.ID)
	}
	return ch, nil
}

func (m *Manager) insertDelivery(ctx context.Context, delivery DeliveryRecord) error {
	_, err := m.db.ExecContext(ctx, `
INSERT INTO comms_deliveries (id, channel_id, kind, target_ref, title, body, status, error_msg, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		delivery.ID, delivery.ChannelID, delivery.Kind, delivery.TargetRef, delivery.Title, delivery.Body, delivery.Status, delivery.ErrorMsg, delivery.CreatedAt,
	)
	return err
}

func (m *Manager) sendTelegram(ctx context.Context, botToken, chatID, title, body string) error {
	if botToken == "" {
		return errors.New("telegram bot token is empty")
	}
	if strings.TrimSpace(chatID) == "" {
		return errors.New("telegram target_ref (chat_id) is required")
	}

	payload := struct {
		ChatID    string `json:"chat_id"`
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode"`
	}{
		ChatID:    chatID,
		Text:      formatTelegramHTML(title, body),
		ParseMode: "HTML",
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", m.telegramAPIBase, url.PathEscape(botToken))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer httpResp.Body.Close()

	responseBytes, _ := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024))
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("telegram HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(string(responseBytes)))
	}

	var tgReply struct {
		OK          *bool  `json:"ok"`
		Description string `json:"description"`
	}
	if len(responseBytes) > 0 && json.Unmarshal(responseBytes, &tgReply) == nil && tgReply.OK != nil && !*tgReply.OK {
		return fmt.Errorf("telegram API error: %s", strings.TrimSpace(tgReply.Description))
	}
	return nil
}

func (m *Manager) sendDiscord(ctx context.Context, webhookURL, title, body, priorityTier string) error {
	if err := validateHTTPURL(webhookURL); err != nil {
		return fmt.Errorf("invalid discord webhook url: %w", err)
	}

	embedTitle := title
	if embedTitle == "" {
		embedTitle = "MW-ECHO Signal"
	}
	footerLabel := discordUsername
	if normalizedTier := strings.ToUpper(strings.TrimSpace(priorityTier)); normalizedTier != "" {
		footerLabel = fmt.Sprintf("%s // %s", discordUsername, normalizedTier)
	}

	type discordFooter struct {
		Text string `json:"text"`
	}
	type discordEmbed struct {
		Title       string        `json:"title"`
		Description string        `json:"description"`
		Color       int           `json:"color"`
		Footer      discordFooter `json:"footer"`
	}
	payload := struct {
		Username string         `json:"username"`
		Embeds   []discordEmbed `json:"embeds"`
	}{
		Username: discordUsername,
		Embeds: []discordEmbed{
			{
				Title:       embedTitle,
				Description: body,
				Color:       discordColorForPriority(priorityTier),
				Footer:      discordFooter{Text: footerLabel},
			},
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("discord webhook request failed: %w", err)
	}
	defer httpResp.Body.Close()

	responseBytes, _ := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024))
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(string(responseBytes)))
	}
	return nil
}

func normalizeKind(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case KindTelegram:
		return KindTelegram, nil
	case KindDiscord:
		return KindDiscord, nil
	default:
		return "", fmt.Errorf("%w: kind must be 'telegram' or 'discord'", ErrValidation)
	}
}

func validateHTTPURL(raw string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("url must use http or https with a host")
	}
	return nil
}

func maskSecretHint(secret string) string {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return "configured"
	}
	return trimmed[:4] + "..." + trimmed[len(trimmed)-4:]
}

func discordColorForPriority(priorityTier string) int {
	switch strings.ToUpper(strings.TrimSpace(priorityTier)) {
	case "P0":
		return discordColorP0
	case "P1":
		return discordColorP1
	case "P2":
		return discordColorP2
	default:
		return discordColorDefault
	}
}

func formatTelegramHTML(title, body string) string {
	escapedTitle := html.EscapeString(title)
	escapedBody := html.EscapeString(body)
	switch {
	case escapedTitle != "" && escapedBody != "":
		return fmt.Sprintf("<b>%s</b>\n%s", escapedTitle, escapedBody)
	case escapedTitle != "":
		return fmt.Sprintf("<b>%s</b>", escapedTitle)
	default:
		return escapedBody
	}
}

func generateID(prefix string) string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

func boolToInt(val bool) int {
	if val {
		return 1
	}
	return 0
}
