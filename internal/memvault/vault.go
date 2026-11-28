package memvault

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

const (
	SensitivityPublic         = "public"
	SensitivityPrivate        = "private"
	SensitivityChildSensitive = "child_sensitive"
)

var (
	ErrValidation       = errors.New("memvault validation error")
	ErrNotFound         = errors.New("memvault conversation not found")
	ErrPrivacyViolation = errors.New("memvault privacy firewall violation")
)

// Manager owns the local-first SQLite memory vault and privacy firewall.
type Manager struct {
	hub *store.HubStore
	db  *sql.DB
}

// Conversation represents an imported conversation header and privacy policy.
type Conversation struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	Title         string `json:"title"`
	Sensitivity   string `json:"sensitivity"`
	AllowCloudLLM bool   `json:"allowCloudLLM"`
	SHA256        string `json:"sha256"`
	MessageCount  int    `json:"messageCount"`
	CreatedAt     string `json:"createdAt"`
	ImportedAt    string `json:"importedAt"`
}

// Message represents a single ordered turn inside a conversation.
type Message struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	Seq            int    `json:"seq"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	CreatedAt      string `json:"createdAt"`
}

// ConversationDetail bundles a conversation header with its ordered messages.
type ConversationDetail struct {
	Conversation Conversation `json:"conversation"`
	Messages     []Message    `json:"messages"`
}

// SearchHit is a single FTS5 search match annotated with conversation privacy metadata.
type SearchHit struct {
	MessageID         string `json:"messageId"`
	ConversationID    string `json:"conversationId"`
	ConversationTitle string `json:"conversationTitle"`
	Source            string `json:"source"`
	Sensitivity       string `json:"sensitivity"`
	AllowCloudLLM     bool   `json:"allowCloudLLM"`
	Role              string `json:"role"`
	Content           string `json:"content"`
	Snippet           string `json:"snippet"`
	CreatedAt         string `json:"createdAt"`
}

// ImportReport summarizes an import operation.
type ImportReport struct {
	ImportedConversations int      `json:"importedConversations"`
	SkippedDuplicates     int      `json:"skippedDuplicates"`
	ImportedMessages      int      `json:"importedMessages"`
	ChildSensitiveFlagged int      `json:"childSensitiveFlagged"`
	ConversationIDs       []string `json:"conversationIds"`
}

// VaultStats aggregates counts across the memory vault.
type VaultStats struct {
	TotalConversations int            `json:"totalConversations"`
	TotalMessages      int            `json:"totalMessages"`
	CloudSafeCount     int            `json:"cloudSafeCount"`
	BySensitivity      map[string]int `json:"bySensitivity"`
}

// NewManager initializes the memory vault tables on top of HubStore.
func NewManager(hub *store.HubStore) (*Manager, error) {
	if hub == nil || hub.DB() == nil {
		return nil, fmt.Errorf("%w: hub store is required", ErrValidation)
	}
	manager := &Manager{
		hub: hub,
		db:  hub.DB(),
	}
	if err := manager.migrate(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS mem_conversations (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  title TEXT NOT NULL,
  sensitivity TEXT NOT NULL DEFAULT 'private' CHECK(sensitivity IN ('public','private','child_sensitive')),
  allow_cloud_llm INTEGER NOT NULL DEFAULT 0,
  sha256 TEXT NOT NULL UNIQUE,
  message_count INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  imported_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS mem_messages (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY(conversation_id) REFERENCES mem_conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_mem_messages_conv_seq ON mem_messages(conversation_id, seq);

CREATE VIRTUAL TABLE IF NOT EXISTS mem_messages_fts USING fts5(
  message_id UNINDEXED,
  conversation_id UNINDEXED,
  role,
  content
);
`
	if _, err := m.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate memvault schema: %w", err)
	}
	return nil
}

type rawChatGPTConversation struct {
	ID          string                     `json:"id"`
	Title       string                     `json:"title"`
	Source      string                     `json:"source"`
	CreateTime  any                        `json:"create_time"`
	CreatedAt   string                     `json:"created_at"`
	CurrentNode string                     `json:"current_node"`
	Mapping     map[string]rawChatGPTNode  `json:"mapping"`
	Messages    []rawFlatConversationEntry `json:"messages"`
}

type rawChatGPTNode struct {
	ID       string            `json:"id"`
	Parent   *string           `json:"parent"`
	Children []string          `json:"children"`
	Message  *rawChatGPTMsgObj `json:"message"`
}

type rawChatGPTMsgObj struct {
	ID         string               `json:"id"`
	Author     rawChatGPTAuthor     `json:"author"`
	CreateTime any                  `json:"create_time"`
	Content    rawChatGPTMsgContent `json:"content"`
}

type rawChatGPTAuthor struct {
	Role string `json:"role"`
}

type rawChatGPTMsgContent struct {
	ContentType string `json:"content_type"`
	Parts       []any  `json:"parts"`
	Text        string `json:"text"`
}

type rawFlatConversationEntry struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	CreatedAt  string `json:"created_at"`
	CreateTime any    `json:"create_time"`
}

type rawConversationWrapper struct {
	Conversations []rawChatGPTConversation `json:"conversations"`
}

type parsedMessage struct {
	Role      string
	Content   string
	CreatedAt string
}

// ImportZIP extracts conversations.json from a ChatGPT export ZIP and imports it.
func (m *Manager) ImportZIP(ctx context.Context, zipBytes []byte) (ImportReport, error) {
	if len(zipBytes) == 0 {
		return ImportReport{}, fmt.Errorf("%w: empty zip payload", ErrValidation)
	}
	archiveReader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return ImportReport{}, fmt.Errorf("%w: invalid zip archive: %v", ErrValidation, err)
	}

	var conversationsFile *zip.File
	for _, entry := range archiveReader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		if strings.EqualFold(path.Base(strings.ReplaceAll(entry.Name, "\\", "/")), "conversations.json") {
			conversationsFile = entry
			break
		}
	}
	if conversationsFile == nil {
		return ImportReport{}, fmt.Errorf("%w: conversations.json not found in zip archive", ErrValidation)
	}

	fileReader, err := conversationsFile.Open()
	if err != nil {
		return ImportReport{}, fmt.Errorf("open conversations.json in zip: %w", err)
	}
	defer fileReader.Close()

	rawJSON, err := io.ReadAll(fileReader)
	if err != nil {
		return ImportReport{}, fmt.Errorf("read conversations.json in zip: %w", err)
	}
	return m.ImportChatGPTJSON(ctx, rawJSON)
}

// ImportChatGPTJSON parses ChatGPT export JSON (array, wrapper, or single object),
// deduplicates by SHA-256, classifies sensitivity, and persists into SQLite + FTS5.
func (m *Manager) ImportChatGPTJSON(ctx context.Context, rawJSON []byte) (ImportReport, error) {
	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 {
		return ImportReport{}, fmt.Errorf("%w: empty json payload", ErrValidation)
	}

	rawConversations, err := decodeChatGPTConversations(trimmed)
	if err != nil {
		return ImportReport{}, err
	}

	report := ImportReport{
		ConversationIDs: make([]string, 0, len(rawConversations)),
	}
	importedAt := time.Now().UTC().Format(time.RFC3339)

	for _, rawConv := range rawConversations {
		messages := extractConversationMessages(rawConv)
		if len(messages) == 0 {
			continue
		}
		source := strings.TrimSpace(rawConv.Source)
		if source == "" {
			source = "chatgpt"
		}
		title := strings.TrimSpace(rawConv.Title)
		if title == "" {
			title = "Untitled Conversation"
		}
		createdAt := normalizeTimestamp(rawConv.CreatedAt, rawConv.CreateTime, importedAt)
		digest := computeConversationSHA256(source, title, messages)

		var existingID string
		lookupErr := m.db.QueryRowContext(ctx, `SELECT id FROM mem_conversations WHERE sha256 = ?`, digest).Scan(&existingID)
		if lookupErr == nil && existingID != "" {
			report.SkippedDuplicates++
			continue
		}
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return report, fmt.Errorf("check conversation duplicate: %w", lookupErr)
		}

		sensitivity := classifySensitivity(title, messages)
		if sensitivity == SensitivityChildSensitive {
			report.ChildSensitiveFlagged++
		}
		convID := "conv_" + digest[:24]

		tx, err := m.db.BeginTx(ctx, nil)
		if err != nil {
			return report, fmt.Errorf("begin memvault import tx: %w", err)
		}

		// Always default allow_cloud_llm to 0 on import; child_sensitive is strictly locked to 0.
		_, err = tx.ExecContext(ctx, `
INSERT INTO mem_conversations (id, source, title, sensitivity, allow_cloud_llm, sha256, message_count, created_at,
	imported_at)
VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?)
`, convID, source, title, sensitivity, digest, len(messages), createdAt, importedAt)
		if err != nil {
			_ = tx.Rollback()
			return report, fmt.Errorf("insert mem_conversation: %w", err)
		}

		for idx, msg := range messages {
			seq := idx + 1
			msgID := makeMessageID(convID, seq, msg.Role, msg.Content)
			msgCreatedAt := msg.CreatedAt
			if msgCreatedAt == "" {
				msgCreatedAt = createdAt
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO mem_messages (id, conversation_id, seq, role, content, created_at)
VALUES (?, ?, ?, ?, ?, ?)
`, msgID, convID, seq, msg.Role, msg.Content, msgCreatedAt); err != nil {
				_ = tx.Rollback()
				return report, fmt.Errorf("insert mem_message: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO mem_messages_fts (message_id, conversation_id, role, content)
VALUES (?, ?, ?, ?)
`, msgID, convID, msg.Role, msg.Content); err != nil {
				_ = tx.Rollback()
				return report, fmt.Errorf("insert mem_messages_fts: %w", err)
			}
		}

		if err := tx.Commit(); err != nil {
			return report, fmt.Errorf("commit memvault import tx: %w", err)
		}

		report.ImportedConversations++
		report.ImportedMessages += len(messages)
		report.ConversationIDs = append(report.ConversationIDs, convID)
	}

	return report, nil
}

// Search executes an FTS5 search over mem_messages.
// When cloudSafeOnly is true, the Hard Privacy Firewall strictly restricts results
// to conversations where sensitivity = 'public' AND allow_cloud_llm = 1.
func (m *Manager) Search(ctx context.Context, query string, cloudSafeOnly bool, limit int) ([]SearchHit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	ftsExpr := sanitizeFTSQuery(query)
	if ftsExpr == "" {
		return []SearchHit{}, nil
	}

	clauses := []string{"mem_messages_fts MATCH ?"}
	args := []any{ftsExpr}
	if cloudSafeOnly {
		clauses = append(clauses, "c.sensitivity = 'public'", "c.allow_cloud_llm = 1")
	}
	args = append(args, limit)

	querySQL := fmt.Sprintf(`
SELECT m.id, m.conversation_id, c.title, c.source, c.sensitivity, c.allow_cloud_llm,
       m.role, m.content, snippet(mem_messages_fts, 3, '[', ']', '...', 24) AS snippet, m.created_at
FROM mem_messages_fts
JOIN mem_messages m ON m.id = mem_messages_fts.message_id
JOIN mem_conversations c ON c.id = m.conversation_id
WHERE %s
ORDER BY bm25(mem_messages_fts) ASC
LIMIT ?`, strings.Join(clauses, " AND "))

	rows, err := m.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("search memvault fts: %w", err)
	}
	defer rows.Close()

	hits := make([]SearchHit, 0)
	for rows.Next() {
		var hit SearchHit
		var allowInt int
		if err := rows.Scan(
			&hit.MessageID, &hit.ConversationID, &hit.ConversationTitle, &hit.Source,
			&hit.Sensitivity, &allowInt, &hit.Role, &hit.Content, &hit.Snippet, &hit.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan search hit: %w", err)
		}
		hit.AllowCloudLLM = allowInt == 1
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

// SetPrivacy updates a conversation's sensitivity and cloud LLM permission.
// Hard Privacy Firewall: refuses allowCloudLLM=true whenever sensitivity == 'child_sensitive'.
func (m *Manager) SetPrivacy(ctx context.Context, convID string, sensitivity string, allowCloudLLM bool) (Conversation,
	error) {
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return Conversation{}, fmt.Errorf("%w: conversation id is required", ErrValidation)
	}
	normalizedSensitivity := strings.TrimSpace(strings.ToLower(sensitivity))
	switch normalizedSensitivity {
	case SensitivityPublic, SensitivityPrivate, SensitivityChildSensitive:
	default:
		return Conversation{}, fmt.Errorf("%w: invalid sensitivity %q", ErrValidation, sensitivity)
	}
	if normalizedSensitivity == SensitivityChildSensitive && allowCloudLLM {
		return Conversation{}, fmt.Errorf("%w: allow_cloud_llm cannot be enabled for child_sensitive conversations",
			ErrPrivacyViolation)
	}

	allowInt := 0
	if allowCloudLLM {
		allowInt = 1
	}

	execResult, err := m.db.ExecContext(ctx, `
UPDATE mem_conversations
SET sensitivity = ?, allow_cloud_llm = ?
WHERE id = ?
`, normalizedSensitivity, allowInt, convID)
	if err != nil {
		return Conversation{}, fmt.Errorf("update conversation privacy: %w", err)
	}
	affected, _ := execResult.RowsAffected()
	if affected == 0 {
		return Conversation{}, ErrNotFound
	}
	detail, err := m.GetConversation(ctx, convID)
	if err != nil {
		return Conversation{}, err
	}
	return detail.Conversation, nil
}

// ListConversations returns stored conversations, optionally filtered by sensitivity.
func (m *Manager) ListConversations(ctx context.Context, sensitivity string, limit int) ([]Conversation, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var clauses []string
	var args []any
	trimmedSensitivity := strings.TrimSpace(strings.ToLower(sensitivity))
	if trimmedSensitivity != "" {
		switch trimmedSensitivity {
		case SensitivityPublic, SensitivityPrivate, SensitivityChildSensitive:
			clauses = append(clauses, "sensitivity = ?")
			args = append(args, trimmedSensitivity)
		default:
			return nil, fmt.Errorf("%w: invalid sensitivity filter %q", ErrValidation, sensitivity)
		}
	}
	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)

	rows, err := m.db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, source, title, sensitivity, allow_cloud_llm, sha256, message_count, created_at, imported_at
FROM mem_conversations
%s
ORDER BY imported_at DESC, created_at DESC
LIMIT ?`, whereSQL), args...)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	conversations := make([]Conversation, 0)
	for rows.Next() {
		var conv Conversation
		var allowInt int
		if err := rows.Scan(
			&conv.ID, &conv.Source, &conv.Title, &conv.Sensitivity, &allowInt,
			&conv.SHA256, &conv.MessageCount, &conv.CreatedAt, &conv.ImportedAt,
		); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		conv.AllowCloudLLM = allowInt == 1
		conversations = append(conversations, conv)
	}
	return conversations, rows.Err()
}

// GetConversation returns a single conversation along with all ordered messages.
func (m *Manager) GetConversation(ctx context.Context, convID string) (ConversationDetail, error) {
	convID = strings.TrimSpace(convID)
	if convID == "" {
		return ConversationDetail{}, fmt.Errorf("%w: conversation id is required", ErrValidation)
	}

	var conv Conversation
	var allowInt int
	err := m.db.QueryRowContext(ctx, `
SELECT id, source, title, sensitivity, allow_cloud_llm, sha256, message_count, created_at, imported_at
FROM mem_conversations
WHERE id = ?
`, convID).Scan(
		&conv.ID, &conv.Source, &conv.Title, &conv.Sensitivity, &allowInt,
		&conv.SHA256, &conv.MessageCount, &conv.CreatedAt, &conv.ImportedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ConversationDetail{}, ErrNotFound
	}
	if err != nil {
		return ConversationDetail{}, fmt.Errorf("get conversation: %w", err)
	}
	conv.AllowCloudLLM = allowInt == 1

	rows, err := m.db.QueryContext(ctx, `
SELECT id, conversation_id, seq, role, content, created_at
FROM mem_messages
WHERE conversation_id = ?
ORDER BY seq ASC
`, convID)
	if err != nil {
		return ConversationDetail{}, fmt.Errorf("list conversation messages: %w", err)
	}
	defer rows.Close()

	messages := make([]Message, 0, conv.MessageCount)
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.ConversationID, &msg.Seq, &msg.Role, &msg.Content, &msg.CreatedAt); err != nil {
			return ConversationDetail{}, fmt.Errorf("scan conversation message: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return ConversationDetail{}, err
	}

	return ConversationDetail{
		Conversation: conv,
		Messages:     messages,
	}, nil
}

// Stats returns aggregate counts across conversations, messages, and privacy tiers.
func (m *Manager) Stats(ctx context.Context) (VaultStats, error) {
	stats := VaultStats{
		BySensitivity: map[string]int{
			SensitivityPublic:         0,
			SensitivityPrivate:        0,
			SensitivityChildSensitive: 0,
		},
	}
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_conversations`).Scan(&stats.TotalConversations); err != nil {
		return stats, fmt.Errorf("count conversations: %w", err)
	}
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_messages`).Scan(&stats.TotalMessages); err != nil {
		return stats, fmt.Errorf("count messages: %w", err)
	}
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mem_conversations WHERE sensitivity = 'public' AND allow_cloud_llm = 1`).Scan(&stats.CloudSafeCount); err != nil {
		return stats, fmt.Errorf("count cloud-safe conversations: %w", err)
	}

	rows, err := m.db.QueryContext(ctx, `SELECT sensitivity, COUNT(*) FROM mem_conversations GROUP BY sensitivity`)
	if err != nil {
		return stats, fmt.Errorf("group conversations by sensitivity: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tier string
		var count int
		if err := rows.Scan(&tier, &count); err != nil {
			return stats, fmt.Errorf("scan sensitivity count: %w", err)
		}
		stats.BySensitivity[tier] = count
	}
	return stats, rows.Err()
}

func decodeChatGPTConversations(trimmed []byte) ([]rawChatGPTConversation, error) {
	if trimmed[0] == '[' {
		var convList []rawChatGPTConversation
		if err := json.Unmarshal(trimmed, &convList); err != nil {
			return nil, fmt.Errorf("%w: invalid conversations array json: %v", ErrValidation, err)
		}
		return convList, nil
	}

	var wrapper rawConversationWrapper
	if err := json.Unmarshal(trimmed, &wrapper); err == nil && len(wrapper.Conversations) > 0 {
		return wrapper.Conversations, nil
	}

	var single rawChatGPTConversation
	if err := json.Unmarshal(trimmed, &single); err != nil {
		return nil, fmt.Errorf("%w: invalid conversation json: %v", ErrValidation, err)
	}
	if len(single.Mapping) == 0 && len(single.Messages) == 0 {
		return nil, fmt.Errorf("%w: conversation payload contains no mapping or messages", ErrValidation)
	}
	return []rawChatGPTConversation{single}, nil
}

func extractConversationMessages(rawConv rawChatGPTConversation) []parsedMessage {
	if len(rawConv.Mapping) > 0 {
		return extractFromMapping(rawConv.Mapping, rawConv.CurrentNode)
	}
	messages := make([]parsedMessage, 0, len(rawConv.Messages))
	for _, entry := range rawConv.Messages {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		role := strings.TrimSpace(strings.ToLower(entry.Role))
		if role == "" {
			role = "user"
		}
		messages = append(messages, parsedMessage{
			Role:      role,
			Content:   content,
			CreatedAt: normalizeTimestamp(entry.CreatedAt, entry.CreateTime, ""),
		})
	}
	return messages
}

func extractFromMapping(mapping map[string]rawChatGPTNode, currentNode string) []parsedMessage {
	orderedNodes := make([]rawChatGPTNode, 0, len(mapping))
	if currentNode != "" {
		if _, exists := mapping[currentNode]; exists {
			visited := make(map[string]bool, len(mapping))
			cursor := currentNode
			for cursor != "" && !visited[cursor] {
				visited[cursor] = true
				node, ok := mapping[cursor]
				if !ok {
					break
				}
				orderedNodes = append(orderedNodes, node)
				if node.Parent == nil {
					break
				}
				cursor = *node.Parent
			}
			for left, right := 0, len(orderedNodes)-1; left < right; left, right = left+1, right-1 {
				orderedNodes[left], orderedNodes[right] = orderedNodes[right], orderedNodes[left]
			}
		}
	}

	if len(orderedNodes) == 0 {
		roots := make([]string, 0)
		for nodeID, node := range mapping {
			if node.Parent == nil || *node.Parent == "" {
				roots = append(roots, nodeID)
			}
		}
		sort.Strings(roots)
		visited := make(map[string]bool, len(mapping))
		var walk func(nodeID string)
		walk = func(nodeID string) {
			if visited[nodeID] {
				return
			}
			node, ok := mapping[nodeID]
			if !ok {
				return
			}
			visited[nodeID] = true
			orderedNodes = append(orderedNodes, node)
			for _, childID := range node.Children {
				walk(childID)
			}
		}
		for _, rootID := range roots {
			walk(rootID)
		}
		if len(orderedNodes) == 0 {
			keys := make([]string, 0, len(mapping))
			for nodeID := range mapping {
				keys = append(keys, nodeID)
			}
			sort.Strings(keys)
			for _, key := range keys {
				orderedNodes = append(orderedNodes, mapping[key])
			}
		}
	}

	messages := make([]parsedMessage, 0, len(orderedNodes))
	for _, node := range orderedNodes {
		if node.Message == nil {
			continue
		}
		content := extractMessageContent(node.Message.Content)
		if content == "" {
			continue
		}
		role := strings.TrimSpace(strings.ToLower(node.Message.Author.Role))
		if role == "" {
			role = "user"
		}
		messages = append(messages, parsedMessage{
			Role:      role,
			Content:   content,
			CreatedAt: normalizeTimestamp("", node.Message.CreateTime, ""),
		})
	}
	return messages
}

func extractMessageContent(content rawChatGPTMsgContent) string {
	var parts []string
	for _, rawPart := range content.Parts {
		switch typedPart := rawPart.(type) {
		case string:
			if trimmed := strings.TrimSpace(typedPart); trimmed != "" {
				parts = append(parts, trimmed)
			}
		case map[string]any:
			if textVal, ok := typedPart["text"].(string); ok && strings.TrimSpace(textVal) != "" {
				parts = append(parts, strings.TrimSpace(textVal))
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return strings.TrimSpace(content.Text)
}

func normalizeTimestamp(isoCandidate string, numericCandidate any, fallback string) string {
	if trimmed := strings.TrimSpace(isoCandidate); trimmed != "" {
		return trimmed
	}
	switch typedVal := numericCandidate.(type) {
	case float64:
		if typedVal > 0 {
			sec := int64(typedVal)
			nsec := int64((typedVal - float64(sec)) * 1e9)
			return time.Unix(sec, nsec).UTC().Format(time.RFC3339)
		}
	case int64:
		if typedVal > 0 {
			return time.Unix(typedVal, 0).UTC().Format(time.RFC3339)
		}
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typedVal), 64); err == nil && parsed > 0 {
			sec := int64(parsed)
			return time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
	}
	return fallback
}

func computeConversationSHA256(source, title string, messages []parsedMessage) string {
	hasher := sha256.New()
	hasher.Write([]byte(strings.ToLower(strings.TrimSpace(source))))
	hasher.Write([]byte{0})
	hasher.Write([]byte(strings.TrimSpace(title)))
	hasher.Write([]byte{0})
	for _, msg := range messages {
		hasher.Write([]byte(msg.Role))
		hasher.Write([]byte{0})
		hasher.Write([]byte(msg.Content))
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func makeMessageID(convID string, seq int, role, content string) string {
	hasher := sha256.New()
	hasher.Write([]byte(convID))
	hasher.Write([]byte{0})
	hasher.Write([]byte(strconv.Itoa(seq)))
	hasher.Write([]byte{0})
	hasher.Write([]byte(role))
	hasher.Write([]byte{0})
	hasher.Write([]byte(content))
	return "msg_" + hex.EncodeToString(hasher.Sum(nil))[:24]
}

var childSensitivePhrases = []string{
	"child safety",
	"child abuse",
	"elementary school",
	"middle school",
	"high school",
	"my son",
	"my daughter",
	"my child",
	"my kid",
	"my kids",
	"parental control",
	"underage",
	"minor safety",
}

var childSensitiveTokens = map[string]struct{}{
	"child":        {},
	"children":     {},
	"minor":        {},
	"minors":       {},
	"toddler":      {},
	"infant":       {},
	"pediatric":    {},
	"pediatrician": {},
	"kindergarten": {},
	"preschool":    {},
	"adolescent":   {},
	"juvenile":     {},
	"schoolchild":  {},
	"balita":       {},
	"anak":         {},
	"paud":         {},
	"csam":         {},
}

func classifySensitivity(title string, messages []parsedMessage) string {
	if containsChildSensitiveSignal(title) {
		return SensitivityChildSensitive
	}
	for _, msg := range messages {
		if containsChildSensitiveSignal(msg.Content) {
			return SensitivityChildSensitive
		}
	}
	return SensitivityPrivate
}

func containsChildSensitiveSignal(text string) bool {
	lowered := strings.ToLower(text)
	for _, phrase := range childSensitivePhrases {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	tokens := strings.FieldsFunc(lowered, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, token := range tokens {
		if _, matched := childSensitiveTokens[token]; matched {
			return true
		}
	}
	return false
}

func sanitizeFTSQuery(raw string) string {
	tokens := strings.Fields(raw)
	if len(tokens) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		clean := strings.ReplaceAll(tok, `"`, "")
		if clean != "" {
			quoted = append(quoted, `"`+clean+`"*`)
		}
	}
	return strings.Join(quoted, " AND ")
}
