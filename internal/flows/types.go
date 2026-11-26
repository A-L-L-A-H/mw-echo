package flows

import (
	"context"
	"errors"
	"net/http"
)

const (
	TriggerManual      = "manual"
	TriggerInterval    = "interval"
	TriggerRadarEvent  = "radar_event"
	TriggerWhatsAppMsg = "whatsapp_msg"

	StepKindRadarRun     = "radar_run"
	StepKindAIPrompt     = "ai_prompt"
	StepKindCreateTask   = "create_task"
	StepKindSendWhatsApp = "send_whatsapp"
	StepKindSendWebhook  = "send_webhook"
	StepKindBackup9Drive = "backup_9drive"

	TierA0 = "A0"
	TierA1 = "A1"
	TierA2 = "A2"
	TierA3 = "A3"

	RunStatusOK      = "ok"
	RunStatusError   = "error"
	RunStatusBlocked = "blocked"
)

var (
	// ErrApprovalRequired is returned when an A3 flow is executed without explicit approval.
	ErrApprovalRequired = errors.New("A3 flow requires explicit approval")
	// ErrValidation indicates an invalid flow definition or step configuration.
	ErrValidation = errors.New("flow validation failed")
)

// Options configures external integration hooks for the flows engine.
type Options struct {
	HTTPClient   *http.Client
	RunRadar     func(ctx context.Context, instanceID string) (string, error)
	RunAIPrompt  func(ctx context.Context, agentProfile, prompt string) (string, error)
	CreateTask   func(ctx context.Context, project, code, title, priority string) (string, error)
	SendWhatsApp func(ctx context.Context, deviceID, phone, message string) (string, error)
	Backup9Drive func(ctx context.Context) (string, error)
}

// FlowStep represents a single sequential action inside a flow.
type FlowStep struct {
	ID     string            `json:"id"`
	Kind   string            `json:"kind"`
	Config map[string]string `json:"config"`
}

// FlowRecord represents a persisted automation flow definition.
type FlowRecord struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	TriggerKind       string         `json:"triggerKind"`
	TriggerConfig     map[string]any `json:"triggerConfig"`
	TriggerConfigJSON string         `json:"triggerConfigJson,omitempty"`
	Steps             []FlowStep     `json:"steps"`
	ActionTier        string         `json:"actionTier"`
	Enabled           bool           `json:"enabled"`
	Approved          bool           `json:"approved"`
	LastRunAt         string         `json:"lastRunAt"`
	CreatedAt         string         `json:"createdAt"`
	UpdatedAt         string         `json:"updatedAt"`
}

// FlowPatchInput defines partial updates supported by PATCH /api/flows/:id.
type FlowPatchInput struct {
	Name              *string        `json:"name,omitempty"`
	Description       *string        `json:"description,omitempty"`
	TriggerKind       *string        `json:"triggerKind,omitempty"`
	TriggerConfig     map[string]any `json:"triggerConfig,omitempty"`
	TriggerConfigJSON *string        `json:"triggerConfigJson,omitempty"`
	Steps             *[]FlowStep    `json:"steps,omitempty"`
	ActionTier        *string        `json:"actionTier,omitempty"`
	Enabled           *bool          `json:"enabled,omitempty"`
	Approved          *bool          `json:"approved,omitempty"`
}

// StepLog captures the execution trace of an individual step within a FlowRun.
type StepLog struct {
	StepID     string `json:"stepId"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
}

// FlowRun captures the outcome and step logs of a flow execution.
type FlowRun struct {
	ID             string         `json:"id"`
	FlowID         string         `json:"flowId"`
	Status         string         `json:"status"`
	TriggerPayload string         `json:"triggerPayload"`
	Output         map[string]any `json:"output"`
	OutputJSON     string         `json:"outputJson,omitempty"`
	StepLogs       []StepLog      `json:"stepLogs"`
	StepLogsJSON   string         `json:"stepLogsJson,omitempty"`
	StartedAt      string         `json:"startedAt"`
	FinishedAt     string         `json:"finishedAt"`
}
