package codingagent

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

const (
	DefaultGatewayURL   = "http://127.0.0.1:4310/v1/chat/completions"
	DefaultAgentProfile = "opencode"
	DefaultMaxSteps     = 16

	ModeAct  = "act"
	ModePlan = "plan"

	StatusIdle    = "idle"
	StatusRunning = "running"
	StatusError   = "error"
	StatusAborted = "aborted"

	headerAgentProfile = "X-MW-Agent"
	headerEchoProvider = "X-MW-Echo-Provider"
	headerEchoModel    = "X-MW-Echo-Model"
	headerEchoCache    = "X-MW-Echo-Cache"
	headerEchoRTKSaved = "X-MW-Echo-RTK-Saved"
)

// ErrSessionBusy indicates a prompt turn is already executing on the target session.
var ErrSessionBusy = errors.New("session is already running; abort the active turn first")

// Options configures the native coding agent engine.
type Options struct {
	DefaultGatewayURL   string
	DefaultAgentProfile string
	HTTPClient          *http.Client
	MaxSteps            int
}

// Engine manages coding agent sessions, sandboxed workspace tools, ReAct turns, and live SSE subscribers.
type Engine struct {
	hub        *store.HubStore
	db         *sql.DB
	opts       Options
	httpClient *http.Client

	mu          sync.RWMutex
	activeTurns map[string]turnHandle
	subscribers map[string]map[uint64]chan AgentEvent
	nextSubID   uint64
}

type turnHandle struct {
	cancel func()
}

// Session represents a persisted coding agent conversation bound to a workspace directory.
type Session struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	WorkDir        string `json:"workDir"`
	Project        string `json:"project"`
	AgentProfile   string `json:"agentProfile"`
	Model          string `json:"model"`
	Mode           string `json:"mode"`
	Status         string `json:"status"`
	TotalTokensIn  int64  `json:"totalTokensIn"`
	TotalTokensOut int64  `json:"totalTokensOut"`
	RTKTokensSaved int64  `json:"rtkTokensSaved"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// SessionCreateInput carries parameters for creating a new coding session.
type SessionCreateInput struct {
	Title        string `json:"title"`
	WorkDir      string `json:"workDir"`
	Project      string `json:"project"`
	AgentProfile string `json:"agentProfile"`
	Model        string `json:"model"`
	Mode         string `json:"mode"`
}

// SessionPatchInput carries partial updates for an existing coding session.
type SessionPatchInput struct {
	Title        *string `json:"title,omitempty"`
	WorkDir      *string `json:"workDir,omitempty"`
	Project      *string `json:"project,omitempty"`
	AgentProfile *string `json:"agentProfile,omitempty"`
	Model        *string `json:"model,omitempty"`
	Mode         *string `json:"mode,omitempty"`
}

// ToolCallFunction encodes the OpenAI tool call function name and JSON arguments string.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall represents a single OpenAI-compatible function call emitted by an assistant turn.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// MessageMeta captures gateway telemetry, step metadata, and execution flags attached to a message.
type MessageMeta struct {
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Cache          string `json:"cache,omitempty"`
	RTKTokensSaved int    `json:"rtkTokensSaved,omitempty"`
	TokensIn       int    `json:"tokensIn,omitempty"`
	TokensOut      int    `json:"tokensOut,omitempty"`
	Step           int    `json:"step,omitempty"`
	DurationMS     int64  `json:"durationMs,omitempty"`
	IsError        bool   `json:"isError,omitempty"`
	Compacted      bool   `json:"compacted,omitempty"`
}

// Message represents a single persisted turn entry in a coding session.
type Message struct {
	ID            string       `json:"id"`
	SessionID     string       `json:"sessionId"`
	Seq           int          `json:"seq"`
	Role          string       `json:"role"`
	Content       string       `json:"content"`
	ToolCalls     []ToolCall   `json:"toolCalls,omitempty"`
	ToolCallsJSON string       `json:"toolCallsJson,omitempty"`
	ToolCallID    string       `json:"toolCallId,omitempty"`
	ToolName      string       `json:"toolName,omitempty"`
	Meta          *MessageMeta `json:"meta,omitempty"`
	MetaJSON      string       `json:"metaJson,omitempty"`
	CreatedAt     string       `json:"createdAt"`
}

// AgentEvent is emitted to SSE subscribers and CLI callbacks during a coding agent turn.
type AgentEvent struct {
	Type           string    `json:"type"`
	SessionID      string    `json:"sessionId"`
	Step           int       `json:"step,omitempty"`
	Role           string    `json:"role,omitempty"`
	Content        string    `json:"content,omitempty"`
	Message        *Message  `json:"message,omitempty"`
	ToolCall       *ToolCall `json:"toolCall,omitempty"`
	ToolCallID     string    `json:"toolCallId,omitempty"`
	ToolName       string    `json:"toolName,omitempty"`
	Arguments      string    `json:"arguments,omitempty"`
	Output         string    `json:"output,omitempty"`
	ToolOutput     string    `json:"toolOutput,omitempty"`
	Reply          string    `json:"reply,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Model          string    `json:"model,omitempty"`
	Cache          string    `json:"cache,omitempty"`
	RTKSaved       int       `json:"rtkSaved,omitempty"`
	RTKTokensSaved int       `json:"rtkTokensSaved,omitempty"`
	TokensIn       int       `json:"tokensIn,omitempty"`
	TokensOut      int       `json:"tokensOut,omitempty"`
	Mode           string    `json:"mode,omitempty"`
	Error          string    `json:"error,omitempty"`
	Timestamp      string    `json:"timestamp"`
}

// TurnResult returns the updated session snapshot, persisted history, and final reply for a prompt turn.
type TurnResult struct {
	Session  Session   `json:"session"`
	Messages []Message `json:"messages"`
	Reply    string    `json:"reply"`
	Steps    int       `json:"steps"`
	Aborted  bool      `json:"aborted,omitempty"`
}

// CompactResult summarizes the outcome of compacting a session's message history.
type CompactResult struct {
	Compacted    bool   `json:"compacted"`
	Summary      string `json:"summary"`
	KeptMessages int    `json:"keptMessages"`
}

// ToolSpec describes a workspace or hub tool exposed to the LLM and HTTP discovery endpoint.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	ReadOnly    bool           `json:"readOnly"`
	Parameters  map[string]any `json:"parameters"`
}

// NewEngine initializes the SQLite coding agent tables in hub.DB() and returns a ready Engine.
func NewEngine(hub *store.HubStore, opts Options) (*Engine, error) {
	if hub == nil || hub.DB() == nil {
		return nil, errors.New("hub store with initialized SQLite DB is required")
	}

	resolvedOpts := Options{
		DefaultGatewayURL:   strings.TrimSpace(opts.DefaultGatewayURL),
		DefaultAgentProfile: strings.TrimSpace(opts.DefaultAgentProfile),
		HTTPClient:          opts.HTTPClient,
		MaxSteps:            opts.MaxSteps,
	}
	if resolvedOpts.DefaultGatewayURL == "" {
		resolvedOpts.DefaultGatewayURL = DefaultGatewayURL
	}
	if resolvedOpts.DefaultAgentProfile == "" {
		resolvedOpts.DefaultAgentProfile = DefaultAgentProfile
	}
	if resolvedOpts.MaxSteps <= 0 {
		resolvedOpts.MaxSteps = DefaultMaxSteps
	}
	httpClient := resolvedOpts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}

	engine := &Engine{
		hub:         hub,
		db:          hub.DB(),
		opts:        resolvedOpts,
		httpClient:  httpClient,
		activeTurns: make(map[string]turnHandle),
		subscribers: make(map[string]map[uint64]chan AgentEvent),
	}
	if err := engine.Migrate(); err != nil {
		return nil, err
	}
	return engine, nil
}

// Migrate idempotently creates the coding_sessions and coding_messages tables in SQLite.
func (e *Engine) Migrate() error {
	schemaSQL := `
CREATE TABLE IF NOT EXISTS coding_sessions (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  work_dir TEXT NOT NULL DEFAULT '',
  project TEXT NOT NULL DEFAULT '',
  agent_profile TEXT NOT NULL DEFAULT 'opencode',
  model TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT 'act',
  status TEXT NOT NULL DEFAULT 'idle',
  total_tokens_in INTEGER NOT NULL DEFAULT 0,
  total_tokens_out INTEGER NOT NULL DEFAULT 0,
  rtk_tokens_saved INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_coding_sessions_updated ON coding_sessions(updated_at DESC);

CREATE TABLE IF NOT EXISTS coding_messages (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  tool_calls_json TEXT NOT NULL DEFAULT '[]',
  tool_call_id TEXT NOT NULL DEFAULT '',
  tool_name TEXT NOT NULL DEFAULT '',
  meta_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  FOREIGN KEY(session_id) REFERENCES coding_sessions(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_coding_messages_session_seq ON coding_messages(session_id, seq ASC);
`
	if _, err := e.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("migrate coding agent schema: %w", err)
	}
	return nil
}

func generateRecordID(prefix string) string {
	randomBytes := make([]byte, 8)
	if _, err := rand.Read(randomBytes); err != nil {
		return fmt.Sprintf("%s_%x", prefix, time.Now().UTC().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(randomBytes)
}

func normalizeMode(rawMode string) (string, error) {
	cleaned := strings.ToLower(strings.TrimSpace(rawMode))
	if cleaned == "" {
		return ModeAct, nil
	}
	switch cleaned {
	case ModeAct, ModePlan:
		return cleaned, nil
	default:
		return "", fmt.Errorf("invalid mode %q; must be %q or %q", rawMode, ModeAct, ModePlan)
	}
}

func marshalToolCalls(calls []ToolCall) string {
	if len(calls) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(calls)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func unmarshalToolCalls(rawJSON string) []ToolCall {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil
	}
	var calls []ToolCall
	if err := json.Unmarshal([]byte(trimmed), &calls); err != nil || len(calls) == 0 {
		return nil
	}
	return calls
}

func marshalMessageMeta(meta *MessageMeta) string {
	if meta == nil {
		return "{}"
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func unmarshalMessageMeta(rawJSON string) *MessageMeta {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil
	}
	var parsed MessageMeta
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil
	}
	return &parsed
}
