package whatsapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DeviceRecord captures persisted device configuration merged with live session state.
type DeviceRecord struct {
	DeviceID         string `json:"deviceId"`
	ID               string `json:"id"`
	Label            string `json:"label"`
	JID              string `json:"jid"`
	Status           string `json:"status"`
	Connected        bool   `json:"isConnected"`
	LoggedIn         bool   `json:"isLoggedIn"`
	WebhookURL       string `json:"webhookUrl"`
	WebhookSecret    string `json:"-"`
	HasWebhookSecret bool   `json:"hasWebhookSecret"`
	AIAutoReply      bool   `json:"aiAutoReply"`
	AIAgentID        string `json:"aiAgentId"`
	ArchiveToPool    bool   `json:"archiveToPool"`
	QR               string `json:"qr,omitempty"`
	QRLink           string `json:"qrLink,omitempty"`
	QRDataURL        string `json:"qrDataUrl,omitempty"`
	PairingCode      string `json:"pairingCode,omitempty"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

// DeviceConfigPatch updates mutable configuration attributes on a device.
type DeviceConfigPatch struct {
	Label         *string `json:"label,omitempty"`
	WebhookURL    *string `json:"webhookUrl,omitempty"`
	WebhookSecret *string `json:"webhookSecret,omitempty"`
	AIAutoReply   *bool   `json:"aiAutoReply,omitempty"`
	AIAgentID     *string `json:"aiAgentId,omitempty"`
	ArchiveToPool *bool   `json:"archiveToPool,omitempty"`
}

// ChatRecord represents a WhatsApp conversation stored in wa_chats.
type ChatRecord struct {
	DeviceID        string `json:"device_id"`
	JID             string `json:"jid"`
	Name            string `json:"name"`
	Archived        bool   `json:"archived"`
	UnreadCount     int    `json:"unread_count"`
	LastMessage     string `json:"last_message"`
	LastMessageTime string `json:"last_message_time"`
}

// MessageRecord represents a persisted inbound or outbound WhatsApp message.
type MessageRecord struct {
	ID            string `json:"id"`
	DeviceID      string `json:"device_id"`
	ChatJID       string `json:"chat_jid"`
	SenderJID     string `json:"sender_jid"`
	FromMe        bool   `json:"from_me"`
	MsgType       string `json:"msg_type"`
	Body          string `json:"body"`
	MediaObjectID string `json:"media_object_id,omitempty"`
	Status        string `json:"status"`
	Timestamp     string `json:"timestamp"`
}

// Stats summarizes WhatsApp multi-device counts for the management dashboard.
type Stats struct {
	DevicesCount   int    `json:"devicesCount"`
	ConnectedCount int    `json:"connectedCount"`
	ChatsCount     int    `json:"chatsCount"`
	MessagesCount  int    `json:"messagesCount"`
	MediaCount     int    `json:"mediaCount"`
	Engine         string `json:"engine"`
}

func migrateTables(db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS wa_devices (
  device_id TEXT PRIMARY KEY,
  label TEXT NOT NULL DEFAULT '',
  jid TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'disconnected',
  webhook_url TEXT NOT NULL DEFAULT '',
  webhook_secret TEXT NOT NULL DEFAULT '',
  ai_auto_reply INTEGER NOT NULL DEFAULT 0,
  ai_agent_id TEXT NOT NULL DEFAULT 'default',
  archive_to_pool INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS wa_chats (
  device_id TEXT NOT NULL,
  jid TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  archived INTEGER NOT NULL DEFAULT 0,
  unread_count INTEGER NOT NULL DEFAULT 0,
  last_message TEXT NOT NULL DEFAULT '',
  last_message_time TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(device_id, jid)
);
CREATE INDEX IF NOT EXISTS idx_wa_chats_device_time ON wa_chats(device_id, last_message_time DESC);

CREATE TABLE IF NOT EXISTS wa_messages (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL,
  chat_jid TEXT NOT NULL,
  sender_jid TEXT NOT NULL DEFAULT '',
  from_me INTEGER NOT NULL DEFAULT 0,
  msg_type TEXT NOT NULL DEFAULT 'text',
  body TEXT NOT NULL DEFAULT '',
  media_object_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'received',
  timestamp TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wa_messages_device_chat_ts ON wa_messages(device_id, chat_jid, timestamp DESC);

CREATE TABLE IF NOT EXISTS wa_media (
  media_id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL,
  filename TEXT NOT NULL DEFAULT '',
  mime_type TEXT NOT NULL DEFAULT 'application/octet-stream',
  sha256 TEXT NOT NULL DEFAULT '',
  size_bytes INTEGER NOT NULL DEFAULT 0,
  storage_object_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wa_media_device_created ON wa_media(device_id, created_at DESC);
`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate whatsapp schema: %w", err)
	}
	return nil
}

func normalizeDeviceID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "default"
	}
	trimmed = strings.TrimSuffix(trimmed, ".png")
	trimmed = strings.ReplaceAll(trimmed, "/", "_")
	trimmed = strings.ReplaceAll(trimmed, "\\", "_")
	trimmed = strings.ReplaceAll(trimmed, "..", "_")
	if trimmed == "" {
		return "default"
	}
	return trimmed
}

func boolToSQLiteInt(flag bool) int {
	if flag {
		return 1
	}
	return 0
}

func (m *Manager) upsertDeviceRow(ctx context.Context, deviceID, label string) (DeviceRecord, error) {
	cleanID := normalizeDeviceID(deviceID)
	cleanLabel := strings.TrimSpace(label)
	if cleanLabel == "" {
		cleanLabel = cleanID
	}
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := m.db.ExecContext(ctx, `
INSERT INTO wa_devices (
  device_id, label, jid, status, webhook_url, webhook_secret,
  ai_auto_reply, ai_agent_id, archive_to_pool, created_at, updated_at
)
VALUES (?, ?, '', 'disconnected', ?, ?, 0, 'default', 0, ?, ?)
ON CONFLICT(device_id) DO UPDATE SET
  label = CASE WHEN ? != '' AND wa_devices.label = wa_devices.device_id THEN excluded.label ELSE wa_devices.label END,
  updated_at = excluded.updated_at
`, cleanID, cleanLabel, m.opts.DefaultWebhookURL, m.opts.DefaultWebhookSecret, now, now, strings.TrimSpace(label))
	if err != nil {
		return DeviceRecord{}, fmt.Errorf("upsert device %q: %w", cleanID, err)
	}
	return m.getDeviceRow(ctx, cleanID)
}

func (m *Manager) getDeviceRow(ctx context.Context, deviceID string) (DeviceRecord, error) {
	cleanID := normalizeDeviceID(deviceID)
	var record DeviceRecord
	var autoReplyInt, archiveInt int
	err := m.db.QueryRowContext(ctx, `
SELECT device_id, label, jid, status, webhook_url, webhook_secret,
       ai_auto_reply, ai_agent_id, archive_to_pool, created_at, updated_at
FROM wa_devices WHERE device_id = ?`, cleanID).Scan(
		&record.DeviceID, &record.Label, &record.JID, &record.Status,
		&record.WebhookURL, &record.WebhookSecret,
		&autoReplyInt, &record.AIAgentID, &archiveInt,
		&record.CreatedAt, &record.UpdatedAt,
	)
	if err != nil {
		return DeviceRecord{}, err
	}
	record.ID = record.DeviceID
	record.HasWebhookSecret = strings.TrimSpace(record.WebhookSecret) != ""
	record.AIAutoReply = autoReplyInt == 1
	record.ArchiveToPool = archiveInt == 1
	if strings.TrimSpace(record.AIAgentID) == "" {
		record.AIAgentID = "default"
	}
	return record, nil
}

func (m *Manager) listDeviceRows(ctx context.Context) ([]DeviceRecord, error) {
	rows, err := m.db.QueryContext(ctx, `
SELECT device_id, label, jid, status, webhook_url, webhook_secret,
       ai_auto_reply, ai_agent_id, archive_to_pool, created_at, updated_at
FROM wa_devices ORDER BY created_at ASC, device_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	devices := make([]DeviceRecord, 0)
	for rows.Next() {
		var record DeviceRecord
		var autoReplyInt, archiveInt int
		if err := rows.Scan(
			&record.DeviceID, &record.Label, &record.JID, &record.Status,
			&record.WebhookURL, &record.WebhookSecret,
			&autoReplyInt, &record.AIAgentID, &archiveInt,
			&record.CreatedAt, &record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		record.ID = record.DeviceID
		record.HasWebhookSecret = strings.TrimSpace(record.WebhookSecret) != ""
		record.AIAutoReply = autoReplyInt == 1
		record.ArchiveToPool = archiveInt == 1
		if strings.TrimSpace(record.AIAgentID) == "" {
			record.AIAgentID = "default"
		}
		devices = append(devices, record)
	}
	return devices, rows.Err()
}

func (m *Manager) updateDeviceStatusAndJID(ctx context.Context, deviceID, status, jid string, overwriteJID bool) error {
	cleanID := normalizeDeviceID(deviceID)
	now := time.Now().UTC().Format(time.RFC3339)
	if overwriteJID {
		_, err := m.db.ExecContext(ctx, `
UPDATE wa_devices SET status = ?, jid = ?, updated_at = ? WHERE device_id = ?`,
			status, jid, now, cleanID)
		return err
	}
	_, err := m.db.ExecContext(ctx, `
UPDATE wa_devices
SET status = ?,
    jid = CASE WHEN ? != '' THEN ? ELSE jid END,
    updated_at = ?
WHERE device_id = ?`, status, jid, jid, now, cleanID)
	return err
}

func (m *Manager) updateDeviceConfigRow(ctx context.Context, deviceID string, patch DeviceConfigPatch) (DeviceRecord, error) {
	existing, err := m.getDeviceRow(ctx, deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		existing, err = m.upsertDeviceRow(ctx, deviceID, "")
	}
	if err != nil {
		return DeviceRecord{}, err
	}

	if patch.Label != nil {
		trimmedLabel := strings.TrimSpace(*patch.Label)
		if trimmedLabel != "" {
			existing.Label = trimmedLabel
		}
	}
	if patch.WebhookURL != nil {
		existing.WebhookURL = strings.TrimSpace(*patch.WebhookURL)
	}
	if patch.WebhookSecret != nil {
		existing.WebhookSecret = strings.TrimSpace(*patch.WebhookSecret)
		existing.HasWebhookSecret = existing.WebhookSecret != ""
	}
	if patch.AIAutoReply != nil {
		existing.AIAutoReply = *patch.AIAutoReply
	}
	if patch.AIAgentID != nil {
		trimmedAgent := strings.TrimSpace(*patch.AIAgentID)
		if trimmedAgent == "" {
			trimmedAgent = "default"
		}
		existing.AIAgentID = trimmedAgent
	}
	if patch.ArchiveToPool != nil {
		existing.ArchiveToPool = *patch.ArchiveToPool
	}
	existing.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	_, err = m.db.ExecContext(ctx, `
UPDATE wa_devices
SET label = ?, webhook_url = ?, webhook_secret = ?,
    ai_auto_reply = ?, ai_agent_id = ?, archive_to_pool = ?, updated_at = ?
WHERE device_id = ?`,
		existing.Label, existing.WebhookURL, existing.WebhookSecret,
		boolToSQLiteInt(existing.AIAutoReply), existing.AIAgentID,
		boolToSQLiteInt(existing.ArchiveToPool), existing.UpdatedAt, existing.DeviceID,
	)
	if err != nil {
		return DeviceRecord{}, err
	}
	return existing, nil
}

func (m *Manager) deleteDeviceRows(ctx context.Context, deviceID string) error {
	cleanID := normalizeDeviceID(deviceID)
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM wa_media WHERE device_id = ?`, cleanID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wa_messages WHERE device_id = ?`, cleanID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM wa_chats WHERE device_id = ?`, cleanID); err != nil {
		return err
	}
	execResult, err := tx.ExecContext(ctx, `DELETE FROM wa_devices WHERE device_id = ?`, cleanID)
	if err != nil {
		return err
	}
	affected, _ := execResult.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (m *Manager) upsertChatRow(ctx context.Context, chat ChatRecord, incrementUnread bool) error {
	cleanDeviceID := normalizeDeviceID(chat.DeviceID)
	chat.JID = strings.TrimSpace(chat.JID)
	if chat.JID == "" {
		return errors.New("chat jid is required")
	}
	if strings.TrimSpace(chat.LastMessageTime) == "" {
		chat.LastMessageTime = time.Now().UTC().Format(time.RFC3339)
	}
	unreadDelta := 0
	if incrementUnread {
		unreadDelta = 1
	}

	_, err := m.db.ExecContext(ctx, `
INSERT INTO wa_chats (device_id, jid, name, archived, unread_count, last_message, last_message_time)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(device_id, jid) DO UPDATE SET
  name = CASE WHEN excluded.name != '' THEN excluded.name ELSE wa_chats.name END,
  unread_count = wa_chats.unread_count + ?,
  last_message = excluded.last_message,
  last_message_time = excluded.last_message_time
`, cleanDeviceID, chat.JID, strings.TrimSpace(chat.Name), boolToSQLiteInt(chat.Archived), unreadDelta, chat.LastMessage, chat.LastMessageTime, unreadDelta)
	return err
}

func (m *Manager) resetChatUnreadRow(ctx context.Context, deviceID, chatJID string) error {
	_, err := m.db.ExecContext(ctx, `
UPDATE wa_chats SET unread_count = 0 WHERE device_id = ? AND jid = ?`,
		normalizeDeviceID(deviceID), strings.TrimSpace(chatJID))
	return err
}

func (m *Manager) upsertMessageRow(ctx context.Context, record MessageRecord) error {
	cleanDeviceID := normalizeDeviceID(record.DeviceID)
	record.ID = strings.TrimSpace(record.ID)
	if record.ID == "" {
		return errors.New("message id is required")
	}
	if strings.TrimSpace(record.MsgType) == "" {
		record.MsgType = "text"
	}
	if strings.TrimSpace(record.Status) == "" {
		if record.FromMe {
			record.Status = "sent"
		} else {
			record.Status = "received"
		}
	}
	if strings.TrimSpace(record.Timestamp) == "" {
		record.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	_, err := m.db.ExecContext(ctx, `
INSERT INTO wa_messages (
  id, device_id, chat_jid, sender_jid, from_me, msg_type, body, media_object_id, status, timestamp
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  body = excluded.body,
  media_object_id = CASE WHEN excluded.media_object_id != '' THEN excluded.media_object_id ELSE wa_messages.media_object_id END,
  status = excluded.status,
  timestamp = excluded.timestamp
`, record.ID, cleanDeviceID, strings.TrimSpace(record.ChatJID), strings.TrimSpace(record.SenderJID),
		boolToSQLiteInt(record.FromMe), record.MsgType, record.Body, record.MediaObjectID, record.Status, record.Timestamp)
	return err
}

func (m *Manager) getMessageRow(ctx context.Context, messageID string) (MessageRecord, error) {
	var record MessageRecord
	var fromMeInt int
	err := m.db.QueryRowContext(ctx, `
SELECT id, device_id, chat_jid, sender_jid, from_me, msg_type, body, media_object_id, status, timestamp
FROM wa_messages WHERE id = ?`, strings.TrimSpace(messageID)).Scan(
		&record.ID, &record.DeviceID, &record.ChatJID, &record.SenderJID,
		&fromMeInt, &record.MsgType, &record.Body, &record.MediaObjectID,
		&record.Status, &record.Timestamp,
	)
	if err != nil {
		return MessageRecord{}, err
	}
	record.FromMe = fromMeInt == 1
	return record, nil
}

func (m *Manager) updateMessageRow(ctx context.Context, messageID, newBody, newStatus string, replaceBody bool) error {
	if replaceBody {
		_, err := m.db.ExecContext(ctx, `UPDATE wa_messages SET body = ?, status = ? WHERE id = ?`, newBody, newStatus, strings.TrimSpace(messageID))
		return err
	}
	_, err := m.db.ExecContext(ctx, `UPDATE wa_messages SET status = ? WHERE id = ?`, newStatus, strings.TrimSpace(messageID))
	return err
}

func (m *Manager) listChatRows(ctx context.Context, deviceID, searchQuery string, limit int) ([]ChatRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var clauses []string
	var queryArgs []any
	if trimmedDevice := strings.TrimSpace(deviceID); trimmedDevice != "" {
		clauses = append(clauses, "device_id = ?")
		queryArgs = append(queryArgs, normalizeDeviceID(trimmedDevice))
	}
	if trimmedQuery := strings.TrimSpace(searchQuery); trimmedQuery != "" {
		pattern := "%" + strings.ToLower(trimmedQuery) + "%"
		clauses = append(clauses, "(LOWER(jid) LIKE ? OR LOWER(name) LIKE ? OR LOWER(last_message) LIKE ?)")
		queryArgs = append(queryArgs, pattern, pattern, pattern)
	}
	whereClause := ""
	if len(clauses) > 0 {
		whereClause = "WHERE " + strings.Join(clauses, " AND ")
	}
	queryArgs = append(queryArgs, limit)

	rows, err := m.db.QueryContext(ctx, fmt.Sprintf(`
SELECT device_id, jid, name, archived, unread_count, last_message, last_message_time
FROM wa_chats %s
ORDER BY last_message_time DESC, jid ASC
LIMIT ?`, whereClause), queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chats := make([]ChatRecord, 0)
	for rows.Next() {
		var record ChatRecord
		var archivedInt int
		if err := rows.Scan(
			&record.DeviceID, &record.JID, &record.Name,
			&archivedInt, &record.UnreadCount, &record.LastMessage, &record.LastMessageTime,
		); err != nil {
			return nil, err
		}
		record.Archived = archivedInt == 1
		chats = append(chats, record)
	}
	return chats, rows.Err()
}

func (m *Manager) listMessageRows(ctx context.Context, deviceID, chatJID string, limit int) ([]MessageRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var clauses []string
	var queryArgs []any
	if trimmedDevice := strings.TrimSpace(deviceID); trimmedDevice != "" {
		clauses = append(clauses, "device_id = ?")
		queryArgs = append(queryArgs, normalizeDeviceID(trimmedDevice))
	}
	if trimmedChat := strings.TrimSpace(chatJID); trimmedChat != "" {
		clauses = append(clauses, "chat_jid = ?")
		queryArgs = append(queryArgs, trimmedChat)
	}
	whereClause := ""
	if len(clauses) > 0 {
		whereClause = "WHERE " + strings.Join(clauses, " AND ")
	}
	queryArgs = append(queryArgs, limit)

	rows, err := m.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, device_id, chat_jid, sender_jid, from_me, msg_type, body, media_object_id, status, timestamp
FROM wa_messages %s
ORDER BY timestamp DESC, id DESC
LIMIT ?`, whereClause), queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]MessageRecord, 0)
	for rows.Next() {
		var record MessageRecord
		var fromMeInt int
		if err := rows.Scan(
			&record.ID, &record.DeviceID, &record.ChatJID, &record.SenderJID,
			&fromMeInt, &record.MsgType, &record.Body, &record.MediaObjectID,
			&record.Status, &record.Timestamp,
		); err != nil {
			return nil, err
		}
		record.FromMe = fromMeInt == 1
		messages = append(messages, record)
	}
	return messages, rows.Err()
}

func (m *Manager) queryStats(ctx context.Context) (Stats, error) {
	stats := Stats{Engine: "whatsmeow"}
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wa_devices`).Scan(&stats.DevicesCount); err != nil {
		return Stats{}, err
	}
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wa_chats`).Scan(&stats.ChatsCount); err != nil {
		return Stats{}, err
	}
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wa_messages`).Scan(&stats.MessagesCount); err != nil {
		return Stats{}, err
	}
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wa_media`).Scan(&stats.MediaCount); err != nil {
		return Stats{}, err
	}

	m.mu.RLock()
	for _, sess := range m.sessions {
		sess.mu.RLock()
		isOnline := sess.connected || (sess.client != nil && sess.client.IsConnected())
		sess.mu.RUnlock()
		if isOnline {
			stats.ConnectedCount++
		}
	}
	m.mu.RUnlock()

	if stats.ConnectedCount == 0 {
		var persistedOnline int
		if m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wa_devices WHERE status = 'connected'`).Scan(&persistedOnline) == nil {
			stats.ConnectedCount = persistedOnline
		}
	}
	return stats, nil
}

func (m *Manager) upsertMediaMetadataRow(ctx context.Context, meta MediaMetadata) error {
	if strings.TrimSpace(meta.MediaID) == "" {
		return errors.New("media id is required")
	}
	if strings.TrimSpace(meta.CreatedAt) == "" {
		meta.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := m.db.ExecContext(ctx, `
INSERT INTO wa_media (media_id, device_id, filename, mime_type, sha256, size_bytes, storage_object_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(media_id) DO UPDATE SET
  filename = excluded.filename,
  mime_type = excluded.mime_type,
  sha256 = excluded.sha256,
  size_bytes = excluded.size_bytes,
  storage_object_id = CASE WHEN excluded.storage_object_id != '' THEN excluded.storage_object_id ELSE wa_media.storage_object_id END
`, meta.MediaID, normalizeDeviceID(meta.DeviceID), meta.Filename, meta.MimeType, meta.SHA256, meta.SizeBytes, meta.StorageObjectID, meta.CreatedAt)
	return err
}

func (m *Manager) getMediaMetadataRow(ctx context.Context, mediaID string) (MediaMetadata, error) {
	var meta MediaMetadata
	err := m.db.QueryRowContext(ctx, `
SELECT media_id, device_id, filename, mime_type, sha256, size_bytes, storage_object_id, created_at
FROM wa_media WHERE media_id = ?`, strings.TrimSpace(mediaID)).Scan(
		&meta.MediaID, &meta.DeviceID, &meta.Filename, &meta.MimeType,
		&meta.SHA256, &meta.SizeBytes, &meta.StorageObjectID, &meta.CreatedAt,
	)
	if err != nil {
		return MediaMetadata{}, err
	}
	return meta, nil
}

func (m *Manager) listInboxRows(ctx context.Context, deviceID, chatJID, searchQuery string, limit, offset int) ([]MessageRecord, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var clauses []string
	var args []any
	if trimmedDevice := strings.TrimSpace(deviceID); trimmedDevice != "" {
		clauses = append(clauses, "device_id = ?")
		args = append(args, normalizeDeviceID(trimmedDevice))
	}
	if trimmedChat := strings.TrimSpace(chatJID); trimmedChat != "" {
		clauses = append(clauses, "chat_jid = ?")
		args = append(args, trimmedChat)
	}
	if trimmedQuery := strings.TrimSpace(searchQuery); trimmedQuery != "" {
		pattern := "%" + strings.ToLower(trimmedQuery) + "%"
		clauses = append(clauses, "(LOWER(body) LIKE ? OR LOWER(sender_jid) LIKE ? OR LOWER(chat_jid) LIKE ?)")
		args = append(args, pattern, pattern, pattern)
	}
	whereClause := ""
	if len(clauses) > 0 {
		whereClause = "WHERE " + strings.Join(clauses, " AND ")
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM wa_messages %s", whereClause)
	if err := m.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := m.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, device_id, chat_jid, sender_jid, from_me, msg_type, body, media_object_id, status, timestamp
FROM wa_messages %s
ORDER BY timestamp DESC, id DESC
LIMIT ? OFFSET ?`, whereClause), queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]MessageRecord, 0)
	for rows.Next() {
		var record MessageRecord
		var fromMeInt int
		if err := rows.Scan(
			&record.ID, &record.DeviceID, &record.ChatJID, &record.SenderJID,
			&fromMeInt, &record.MsgType, &record.Body, &record.MediaObjectID,
			&record.Status, &record.Timestamp,
		); err != nil {
			return nil, 0, err
		}
		record.FromMe = fromMeInt == 1
		items = append(items, record)
	}
	return items, total, rows.Err()
}
