package flows

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

const (
	defaultHTTPTimeout    = 30 * time.Second
	defaultIntervalPeriod = time.Hour
	minIntervalPeriod     = 20 * time.Millisecond
)

// Engine manages SQLite-backed automation flows, governance, triggers, and interval scheduling.
type Engine struct {
	hub        *store.HubStore
	opts       Options
	httpClient *http.Client

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	wakeCh    chan struct{}
	closeOnce sync.Once
}

// NewEngine initializes the flows schema, seeds disabled starter templates on first run,
// and starts the background scheduler for enabled interval flows.
func NewEngine(hub *store.HubStore, opts Options) (*Engine, error) {
	if hub == nil || hub.DB() == nil {
		return nil, errors.New("hub store is required")
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}

	ctx, cancel := context.WithCancel(context.Background())
	engine := &Engine{
		hub:        hub,
		opts:       opts,
		httpClient: client,
		ctx:        ctx,
		cancel:     cancel,
		wakeCh:     make(chan struct{}, 1),
	}

	if err := engine.migrate(); err != nil {
		cancel()
		return nil, err
	}
	if err := engine.seedStarterFlows(context.Background()); err != nil {
		cancel()
		return nil, err
	}

	hub.RegisterCloser(engine.Close)

	engine.wg.Add(1)
	go engine.schedulerLoop()

	return engine, nil
}

// Close stops the background interval scheduler and waits for active scheduled work to finish.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		e.cancel()
		e.wg.Wait()
	})
	return nil
}

func (e *Engine) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS flows (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  trigger_kind TEXT NOT NULL DEFAULT 'manual',
  trigger_config_json TEXT NOT NULL DEFAULT '{}',
  steps_json TEXT NOT NULL DEFAULT '[]',
  action_tier TEXT NOT NULL DEFAULT 'A0',
  enabled INTEGER NOT NULL DEFAULT 0,
  approved INTEGER NOT NULL DEFAULT 0,
  last_run_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS flow_runs (
  id TEXT PRIMARY KEY,
  flow_id TEXT NOT NULL,
  status TEXT NOT NULL,
  trigger_payload TEXT NOT NULL DEFAULT '',
  output_json TEXT NOT NULL DEFAULT '{}',
  step_logs_json TEXT NOT NULL DEFAULT '[]',
  started_at TEXT NOT NULL,
  finished_at TEXT NOT NULL,
  FOREIGN KEY(flow_id) REFERENCES flows(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_flow_runs_flow_started ON flow_runs(flow_id, started_at DESC);
`
	if _, err := e.hub.DB().Exec(schema); err != nil {
		return fmt.Errorf("migrate flows schema: %w", err)
	}
	return nil
}

func (e *Engine) seedStarterFlows(ctx context.Context) error {
	var existingCount int
	if err := e.hub.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM flows`).Scan(&existingCount); err != nil {
		return fmt.Errorf("count existing flows: %w", err)
	}
	if existingCount > 0 {
		return nil
	}

	starters := []FlowRecord{
		{
			ID:            "flow-starter-radar-brief",
			Name:          "Radar Watch -> Sidekick Brief",
			Description:   "Runs World Radar watch collection and generates a concise Sidekick AI brief.",
			TriggerKind:   TriggerRadarEvent,
			TriggerConfig: map[string]any{"source": "worldradar"},
			Steps: []FlowStep{
				{
					ID:   "step-radar-run",
					Kind: StepKindRadarRun,
					Config: map[string]string{
						"instanceId": "default",
					},
				},
				{
					ID:   "step-sidekick-brief",
					Kind: StepKindAIPrompt,
					Config: map[string]string{
						"agentProfile": "sidekick",
						"prompt":       "Summarize these World Radar updates into an executive brief:\n{{prev}}\nTrigger: {{trigger}}",
					},
				},
			},
			ActionTier: TierA1,
			Enabled:    false,
			Approved:   false,
		},
		{
			ID:            "flow-starter-9drive-snapshot",
			Name:          "Daily 9Drive SQLite Vault Snapshot",
			Description:   "Creates an encrypted daily SQLite vault backup on 9Drive.",
			TriggerKind:   TriggerInterval,
			TriggerConfig: map[string]any{"interval": "24h"},
			Steps: []FlowStep{
				{
					ID:     "step-backup-9drive",
					Kind:   StepKindBackup9Drive,
					Config: map[string]string{},
				},
			},
			ActionTier: TierA2,
			Enabled:    false,
			Approved:   false,
		},
	}

	for _, starter := range starters {
		if _, err := e.UpsertFlow(ctx, starter); err != nil {
			return fmt.Errorf("seed starter flow %q: %w", starter.Name, err)
		}
	}
	return nil
}

// ListFlows returns all persisted flows ordered by creation and name.
func (e *Engine) ListFlows(ctx context.Context) ([]FlowRecord, error) {
	rows, err := e.hub.DB().QueryContext(ctx, `
SELECT id, name, description, trigger_kind, trigger_config_json, steps_json,
       action_tier, enabled, approved, last_run_at, created_at, updated_at
FROM flows
ORDER BY created_at ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	flowsList := make([]FlowRecord, 0)
	for rows.Next() {
		record, scanErr := scanFlowRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		flowsList = append(flowsList, record)
	}
	return flowsList, rows.Err()
}

// GetFlow loads a single flow by ID.
func (e *Engine) GetFlow(ctx context.Context, id string) (FlowRecord, error) {
	cleanID := strings.TrimSpace(id)
	if cleanID == "" {
		return FlowRecord{}, sql.ErrNoRows
	}
	row := e.hub.DB().QueryRowContext(ctx, `
SELECT id, name, description, trigger_kind, trigger_config_json, steps_json,
       action_tier, enabled, approved, last_run_at, created_at, updated_at
FROM flows WHERE id = ?`, cleanID)
	return scanFlowRow(row)
}

// UpsertFlow creates or updates a flow definition after validating trigger, tier, and step kinds.
func (e *Engine) UpsertFlow(ctx context.Context, input FlowRecord) (FlowRecord, error) {
	normalized, err := normalizeFlowInput(input)
	if err != nil {
		return FlowRecord{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	existing, getErr := e.GetFlow(ctx, normalized.ID)
	if getErr == nil {
		normalized.CreatedAt = existing.CreatedAt
		if normalized.LastRunAt == "" {
			normalized.LastRunAt = existing.LastRunAt
		}
	} else if errors.Is(getErr, sql.ErrNoRows) {
		normalized.CreatedAt = now
	} else {
		return FlowRecord{}, getErr
	}
	normalized.UpdatedAt = now

	triggerCfgBytes, err := json.Marshal(normalized.TriggerConfig)
	if err != nil {
		return FlowRecord{}, fmt.Errorf("marshal triggerConfig: %w", err)
	}
	normalized.TriggerConfigJSON = string(triggerCfgBytes)
	stepsBytes, err := json.Marshal(normalized.Steps)
	if err != nil {
		return FlowRecord{}, fmt.Errorf("marshal steps: %w", err)
	}

	_, err = e.hub.DB().ExecContext(ctx, `
INSERT INTO flows (
  id, name, description, trigger_kind, trigger_config_json, steps_json,
  action_tier, enabled, approved, last_run_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
  description = excluded.description,
  trigger_kind = excluded.trigger_kind,
  trigger_config_json = excluded.trigger_config_json,
  steps_json = excluded.steps_json,
  action_tier = excluded.action_tier,
  enabled = excluded.enabled,
  approved = excluded.approved,
  last_run_at = excluded.last_run_at,
  updated_at = excluded.updated_at`,
		normalized.ID,
		normalized.Name,
		normalized.Description,
		normalized.TriggerKind,
		string(triggerCfgBytes),
		string(stepsBytes),
		normalized.ActionTier,
		boolToInt(normalized.Enabled),
		boolToInt(normalized.Approved),
		normalized.LastRunAt,
		normalized.CreatedAt,
		normalized.UpdatedAt,
	)
	if err != nil {
		return FlowRecord{}, fmt.Errorf("upsert flow: %w", err)
	}

	e.notifyScheduler()
	return normalized, nil
}

// PatchFlow applies partial updates to an existing flow.
func (e *Engine) PatchFlow(ctx context.Context, id string, patch FlowPatchInput) (FlowRecord, error) {
	existing, err := e.GetFlow(ctx, id)
	if err != nil {
		return FlowRecord{}, err
	}

	if patch.Name != nil {
		existing.Name = *patch.Name
	}
	if patch.Description != nil {
		existing.Description = *patch.Description
	}
	if patch.TriggerKind != nil {
		existing.TriggerKind = *patch.TriggerKind
	}
	if patch.TriggerConfig != nil {
		existing.TriggerConfig = patch.TriggerConfig
	} else if patch.TriggerConfigJSON != nil {
		parsedCfg, parseErr := parseTriggerConfigJSON(*patch.TriggerConfigJSON)
		if parseErr != nil {
			return FlowRecord{}, parseErr
		}
		existing.TriggerConfig = parsedCfg
	}
	if patch.Steps != nil {
		existing.Steps = *patch.Steps
	}
	if patch.ActionTier != nil {
		existing.ActionTier = *patch.ActionTier
	}
	if patch.Enabled != nil {
		existing.Enabled = *patch.Enabled
	}
	if patch.Approved != nil {
		existing.Approved = *patch.Approved
	}

	return e.UpsertFlow(ctx, existing)
}

// DeleteFlow removes a flow and its recorded runs.
func (e *Engine) DeleteFlow(ctx context.Context, id string) error {
	cleanID := strings.TrimSpace(id)
	if cleanID == "" {
		return sql.ErrNoRows
	}
	execResult, err := e.hub.DB().ExecContext(ctx, `DELETE FROM flows WHERE id = ?`, cleanID)
	if err != nil {
		return err
	}
	affected, _ := execResult.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	e.notifyScheduler()
	return nil
}

// ListFlowRuns returns recent execution records for a given flow ID.
func (e *Engine) ListFlowRuns(ctx context.Context, flowID string, limit int) ([]FlowRun, error) {
	cleanID := strings.TrimSpace(flowID)
	if cleanID == "" {
		return nil, sql.ErrNoRows
	}
	if _, err := e.GetFlow(ctx, cleanID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}

	rows, err := e.hub.DB().QueryContext(ctx, `
SELECT id, flow_id, status, trigger_payload, output_json, step_logs_json, started_at, finished_at
FROM flow_runs
WHERE flow_id = ?
ORDER BY started_at DESC, rowid DESC
LIMIT ?`, cleanID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]FlowRun, 0)
	for rows.Next() {
		var runRec FlowRun
		var outputRaw, logsRaw string
		if err := rows.Scan(
			&runRec.ID, &runRec.FlowID, &runRec.Status, &runRec.TriggerPayload,
			&outputRaw, &logsRaw, &runRec.StartedAt, &runRec.FinishedAt,
		); err != nil {
			return nil, err
		}
		if runRec.Output == nil {
			runRec.Output = map[string]any{}
		}
		runRec.OutputJSON = outputRaw
		_ = json.Unmarshal([]byte(outputRaw), &runRec.Output)
		if runRec.Output == nil {
			runRec.Output = map[string]any{}
		}
		runRec.StepLogsJSON = logsRaw
		if err := json.Unmarshal([]byte(logsRaw), &runRec.StepLogs); err != nil || runRec.StepLogs == nil {
			runRec.StepLogs = []StepLog{}
		}
		runs = append(runs, runRec)
	}
	return runs, rows.Err()
}

// RunFlowByID executes a flow sequentially, enforcing A3 approval governance and recording a FlowRun.
func (e *Engine) RunFlowByID(ctx context.Context, id, payload string) (FlowRun, error) {
	flowRec, err := e.GetFlow(ctx, id)
	if err != nil {
		return FlowRun{}, err
	}

	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	runRec := FlowRun{
		ID:             generateID("run"),
		FlowID:         flowRec.ID,
		TriggerPayload: payload,
		Output:         map[string]any{},
		StepLogs:       make([]StepLog, 0, len(flowRec.Steps)),
		StartedAt:      startedAt,
	}

	if flowRec.ActionTier == TierA3 && !flowRec.Approved {
		runRec.Status = RunStatusBlocked
		runRec.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		runRec.Output["error"] = ErrApprovalRequired.Error()
		runRec.Output["actionTier"] = flowRec.ActionTier
		if saveErr := e.recordRun(ctx, &runRec); saveErr != nil {
			return runRec, fmt.Errorf("%w (record run failed: %v)", ErrApprovalRequired, saveErr)
		}
		return runRec, ErrApprovalRequired
	}

	prevOutput := ""
	stepOutputs := make(map[string]string, len(flowRec.Steps))
	var execErr error

	for idx, step := range flowRec.Steps {
		stepID := strings.TrimSpace(step.ID)
		if stepID == "" {
			stepID = fmt.Sprintf("step-%d", idx+1)
		}
		stepStart := time.Now().UTC().Format(time.RFC3339Nano)
		resolvedCfg := interpolateConfig(step.Config, payload, prevOutput, stepOutputs)

		stepOut, stepErr := e.executeStep(ctx, flowRec, stepID, step.Kind, resolvedCfg, payload, prevOutput)
		stepEnd := time.Now().UTC().Format(time.RFC3339Nano)

		if stepErr != nil {
			runRec.StepLogs = append(runRec.StepLogs, StepLog{
				StepID:     stepID,
				Kind:       step.Kind,
				Status:     RunStatusError,
				Error:      stepErr.Error(),
				StartedAt:  stepStart,
				FinishedAt: stepEnd,
			})
			execErr = stepErr
			break
		}

		prevOutput = stepOut
		stepOutputs[stepID] = stepOut
		runRec.StepLogs = append(runRec.StepLogs, StepLog{
			StepID:     stepID,
			Kind:       step.Kind,
			Status:     RunStatusOK,
			Output:     stepOut,
			StartedAt:  stepStart,
			FinishedAt: stepEnd,
		})
	}

	runRec.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	runRec.Output["lastOutput"] = prevOutput
	runRec.Output["stepsCompleted"] = len(runRec.StepLogs)
	if len(stepOutputs) > 0 {
		runRec.Output["stepOutputs"] = stepOutputs
	}
	if execErr != nil {
		runRec.Status = RunStatusError
		runRec.Output["error"] = execErr.Error()
	} else {
		runRec.Status = RunStatusOK
	}

	if saveErr := e.recordRun(ctx, &runRec); saveErr != nil {
		if execErr != nil {
			return runRec, fmt.Errorf("%w (record run failed: %v)", execErr, saveErr)
		}
		return runRec, saveErr
	}
	return runRec, execErr
}

// DispatchTrigger runs all enabled flows matching triggerKind (and optional triggerConfig filters).
func (e *Engine) DispatchTrigger(ctx context.Context, triggerKind, payload string) ([]FlowRun, error) {
	cleanKind := strings.TrimSpace(strings.ToLower(triggerKind))
	if !isValidTriggerKind(cleanKind) {
		return nil, fmt.Errorf("%w: unknown triggerKind %q", ErrValidation, triggerKind)
	}

	rows, err := e.hub.DB().QueryContext(ctx, `
SELECT id, name, description, trigger_kind, trigger_config_json, steps_json,
       action_tier, enabled, approved, last_run_at, created_at, updated_at
FROM flows
WHERE enabled = 1 AND trigger_kind = ?
ORDER BY created_at ASC, id ASC`, cleanKind)
	if err != nil {
		return nil, err
	}

	candidates := make([]FlowRecord, 0)
	for rows.Next() {
		flowRec, scanErr := scanFlowRow(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		candidates = append(candidates, flowRec)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return nil, rowsErr
	}
	_ = rows.Close()

	runs := make([]FlowRun, 0, len(candidates))
	var dispatchErrs []error
	for _, flowRec := range candidates {
		if !matchesTriggerFilter(flowRec.TriggerConfig, payload) {
			continue
		}
		runRec, runErr := e.RunFlowByID(ctx, flowRec.ID, payload)
		runs = append(runs, runRec)
		if runErr != nil {
			dispatchErrs = append(dispatchErrs, fmt.Errorf("flow %s: %w", flowRec.ID, runErr))
		}
	}
	return runs, errors.Join(dispatchErrs...)
}

func (e *Engine) executeStep(
	ctx context.Context,
	flowRec FlowRecord,
	stepID string,
	kind string,
	cfg map[string]string,
	triggerPayload string,
	prevOutput string,
) (string, error) {
	switch kind {
	case StepKindRadarRun:
		if e.opts.RunRadar == nil {
			return "", errors.New("RunRadar hook is not configured")
		}
		instanceID := firstNonEmpty(cfg["instanceId"], cfg["instance_id"], cfg["id"], "default")
		return e.opts.RunRadar(ctx, instanceID)

	case StepKindAIPrompt:
		if e.opts.RunAIPrompt == nil {
			return "", errors.New("RunAIPrompt hook is not configured")
		}
		agentProfile := firstNonEmpty(cfg["agentProfile"], cfg["agent_profile"], cfg["profile"], "default")
		prompt := firstNonEmpty(cfg["prompt"], cfg["message"], cfg["text"], prevOutput, triggerPayload)
		return e.opts.RunAIPrompt(ctx, agentProfile, prompt)

	case StepKindCreateTask:
		if e.opts.CreateTask == nil {
			return "", errors.New("CreateTask hook is not configured")
		}
		project := firstNonEmpty(cfg["project"], "moonwitness")
		code := firstNonEmpty(cfg["code"])
		if code == "" {
			code = strings.ToUpper(generateID("FLOW"))
		}
		title := firstNonEmpty(cfg["title"], prevOutput, triggerPayload, flowRec.Name)
		priority := firstNonEmpty(cfg["priority"], "p2")
		return e.opts.CreateTask(ctx, project, code, title, priority)

	case StepKindSendWhatsApp:
		if e.opts.SendWhatsApp == nil {
			return "", errors.New("SendWhatsApp hook is not configured")
		}
		deviceID := firstNonEmpty(cfg["deviceId"], cfg["device_id"])
		phone := firstNonEmpty(cfg["phone"], cfg["to"], cfg["recipient"])
		message := firstNonEmpty(cfg["message"], cfg["text"], cfg["body"], prevOutput, triggerPayload)
		if phone == "" {
			return "", errors.New("send_whatsapp requires phone")
		}
		return e.opts.SendWhatsApp(ctx, deviceID, phone, message)

	case StepKindSendTelegram, StepKindSendDiscord:
		if e.opts.SendComms == nil {
			return "", errors.New("SendComms hook is not configured")
		}
		commKind := "telegram"
		if kind == StepKindSendDiscord {
			commKind = "discord"
		}
		channelID := firstNonEmpty(cfg["channelId"], cfg["channel_id"])
		targetRef := firstNonEmpty(cfg["targetRef"], cfg["target_ref"], cfg["chatId"], cfg["chat_id"], cfg["target"])
		title := firstNonEmpty(cfg["title"], flowRec.Name)
		body := firstNonEmpty(cfg["body"], cfg["message"], cfg["text"], prevOutput, triggerPayload)
		priorityTier := firstNonEmpty(cfg["priorityTier"], cfg["priority_tier"], cfg["priority"])
		return e.opts.SendComms(ctx, commKind, channelID, targetRef, title, body, priorityTier)

	case StepKindSendWebhook:
		return e.executeWebhookStep(ctx, flowRec, stepID, cfg, triggerPayload, prevOutput)

	case StepKindBackup9Drive:
		if e.opts.Backup9Drive == nil {
			return "", errors.New("Backup9Drive hook is not configured")
		}
		return e.opts.Backup9Drive(ctx)

	default:
		return "", fmt.Errorf("unsupported step kind %q", kind)
	}
}

func (e *Engine) executeWebhookStep(
	ctx context.Context,
	flowRec FlowRecord,
	stepID string,
	cfg map[string]string,
	triggerPayload string,
	prevOutput string,
) (string, error) {
	targetURL := firstNonEmpty(cfg["url"], cfg["webhookUrl"], cfg["webhook_url"])
	if targetURL == "" {
		return "", errors.New("send_webhook requires url")
	}
	parsedURL, err := url.Parse(targetURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return "", fmt.Errorf("invalid webhook url %q", targetURL)
	}

	method := strings.ToUpper(firstNonEmpty(cfg["method"], http.MethodPost))
	bodyText := firstNonEmpty(cfg["body"], cfg["payload"], cfg["message"])
	contentType := firstNonEmpty(cfg["contentType"], cfg["content_type"])

	if bodyText == "" {
		encoded, marshalErr := json.Marshal(map[string]any{
			"flowId":   flowRec.ID,
			"flowName": flowRec.Name,
			"stepId":   stepID,
			"trigger":  triggerPayload,
			"prev":     prevOutput,
		})
		if marshalErr != nil {
			return "", marshalErr
		}
		bodyText = string(encoded)
		if contentType == "" {
			contentType = "application/json"
		}
	} else if contentType == "" {
		trimmed := strings.TrimSpace(bodyText)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			contentType = "application/json"
		} else {
			contentType = "text/plain; charset=utf-8"
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, parsedURL.String(), strings.NewReader(bodyText))
	if err != nil {
		return "", fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	if secret := firstNonEmpty(cfg["secret"]); secret != "" {
		req.Header.Set("X-Webhook-Secret", secret)
	}
	if authHeader := firstNonEmpty(cfg["authorization"]); authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	httpResp, err := e.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("webhook request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBytes, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 64*1024))
	if readErr != nil {
		return "", fmt.Errorf("read webhook response: %w", readErr)
	}
	respText := strings.TrimSpace(string(respBytes))
	if httpResp.StatusCode >= 400 {
		return "", fmt.Errorf("webhook HTTP %d: %s", httpResp.StatusCode, respText)
	}
	if respText == "" {
		return fmt.Sprintf("HTTP %d", httpResp.StatusCode), nil
	}
	return respText, nil
}

func (e *Engine) recordRun(ctx context.Context, runRec *FlowRun) error {
	outputBytes, err := json.Marshal(runRec.Output)
	if err != nil {
		return fmt.Errorf("marshal run output: %w", err)
	}
	logsBytes, err := json.Marshal(runRec.StepLogs)
	if err != nil {
		return fmt.Errorf("marshal step logs: %w", err)
	}
	runRec.OutputJSON = string(outputBytes)
	runRec.StepLogsJSON = string(logsBytes)

	tx, err := e.hub.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO flow_runs (
  id, flow_id, status, trigger_payload, output_json, step_logs_json, started_at, finished_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		runRec.ID, runRec.FlowID, runRec.Status, runRec.TriggerPayload,
		runRec.OutputJSON, runRec.StepLogsJSON, runRec.StartedAt, runRec.FinishedAt,
	); err != nil {
		return fmt.Errorf("insert flow run: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE flows SET last_run_at = ? WHERE id = ?`,
		runRec.FinishedAt, runRec.FlowID,
	); err != nil {
		return fmt.Errorf("update flow last_run_at: %w", err)
	}

	return tx.Commit()
}

func (e *Engine) notifyScheduler() {
	select {
	case e.wakeCh <- struct{}{}:
	default:
	}
}

func (e *Engine) schedulerLoop() {
	defer e.wg.Done()

	for {
		if e.ctx.Err() != nil {
			return
		}

		waitDur, hasWait := e.runDueIntervalFlows()
		if e.ctx.Err() != nil {
			return
		}

		if !hasWait {
			select {
			case <-e.ctx.Done():
				return
			case <-e.wakeCh:
			}
			continue
		}

		timer := time.NewTimer(waitDur)
		select {
		case <-e.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-e.wakeCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (e *Engine) runDueIntervalFlows() (time.Duration, bool) {
	rows, err := e.hub.DB().QueryContext(e.ctx, `
SELECT id, name, description, trigger_kind, trigger_config_json, steps_json,
       action_tier, enabled, approved, last_run_at, created_at, updated_at
FROM flows
WHERE enabled = 1 AND trigger_kind = ?
ORDER BY created_at ASC, id ASC`, TriggerInterval)
	if err != nil {
		return time.Second, true
	}

	intervalFlows := make([]FlowRecord, 0)
	for rows.Next() {
		flowRec, scanErr := scanFlowRow(rows)
		if scanErr == nil {
			intervalFlows = append(intervalFlows, flowRec)
		}
	}
	_ = rows.Close()

	if len(intervalFlows) == 0 {
		return 0, false
	}

	var nextWait time.Duration
	hasWait := false

	for _, flowRec := range intervalFlows {
		if e.ctx.Err() != nil {
			return 0, false
		}
		period := parseIntervalDuration(flowRec.TriggerConfig)
		anchor := parseAnchorTime(flowRec.LastRunAt, flowRec.UpdatedAt, flowRec.CreatedAt)
		dueAt := anchor.Add(period)
		now := time.Now().UTC()

		if !now.Before(dueAt) {
			_, _ = e.RunFlowByID(e.ctx, flowRec.ID, "interval")
			dueAt = time.Now().UTC().Add(period)
		}

		remaining := time.Until(dueAt)
		if remaining < minIntervalPeriod {
			remaining = minIntervalPeriod
		}
		if !hasWait || remaining < nextWait {
			nextWait = remaining
			hasWait = true
		}
	}

	return nextWait, hasWait
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFlowRow(scanner rowScanner) (FlowRecord, error) {
	var flowRec FlowRecord
	var triggerCfgRaw, stepsRaw string
	var enabledInt, approvedInt int

	if err := scanner.Scan(
		&flowRec.ID,
		&flowRec.Name,
		&flowRec.Description,
		&flowRec.TriggerKind,
		&triggerCfgRaw,
		&stepsRaw,
		&flowRec.ActionTier,
		&enabledInt,
		&approvedInt,
		&flowRec.LastRunAt,
		&flowRec.CreatedAt,
		&flowRec.UpdatedAt,
	); err != nil {
		return FlowRecord{}, err
	}

	flowRec.Enabled = enabledInt != 0
	flowRec.Approved = approvedInt != 0
	flowRec.TriggerConfigJSON = triggerCfgRaw

	if err := json.Unmarshal([]byte(triggerCfgRaw), &flowRec.TriggerConfig); err != nil || flowRec.TriggerConfig == nil {
		flowRec.TriggerConfig = map[string]any{}
	}
	if err := json.Unmarshal([]byte(stepsRaw), &flowRec.Steps); err != nil || flowRec.Steps == nil {
		flowRec.Steps = []FlowStep{}
	}
	for idx := range flowRec.Steps {
		if flowRec.Steps[idx].Config == nil {
			flowRec.Steps[idx].Config = map[string]string{}
		}
	}
	return flowRec, nil
}

func normalizeFlowInput(input FlowRecord) (FlowRecord, error) {
	normalized := input
	normalized.ID = strings.TrimSpace(normalized.ID)
	if normalized.ID == "" {
		normalized.ID = generateID("flow")
	}
	normalized.Name = strings.TrimSpace(normalized.Name)
	if normalized.Name == "" {
		return FlowRecord{}, fmt.Errorf("%w: flow name is required", ErrValidation)
	}
	normalized.Description = strings.TrimSpace(normalized.Description)

	normalized.TriggerKind = strings.ToLower(strings.TrimSpace(normalized.TriggerKind))
	if normalized.TriggerKind == "" {
		normalized.TriggerKind = TriggerManual
	}
	if !isValidTriggerKind(normalized.TriggerKind) {
		return FlowRecord{}, fmt.Errorf("%w: invalid triggerKind %q", ErrValidation, normalized.TriggerKind)
	}

	if normalized.TriggerConfig == nil && strings.TrimSpace(normalized.TriggerConfigJSON) != "" {
		parsedCfg, parseErr := parseTriggerConfigJSON(normalized.TriggerConfigJSON)
		if parseErr != nil {
			return FlowRecord{}, parseErr
		}
		normalized.TriggerConfig = parsedCfg
	}
	if normalized.TriggerConfig == nil {
		normalized.TriggerConfig = map[string]any{}
	}

	normalized.ActionTier = strings.ToUpper(strings.TrimSpace(normalized.ActionTier))
	if normalized.ActionTier == "" {
		normalized.ActionTier = TierA0
	}
	if !isValidActionTier(normalized.ActionTier) {
		return FlowRecord{}, fmt.Errorf("%w: invalid actionTier %q", ErrValidation, normalized.ActionTier)
	}

	if normalized.Steps == nil {
		normalized.Steps = []FlowStep{}
	}
	cleanSteps := make([]FlowStep, 0, len(normalized.Steps))
	for idx, step := range normalized.Steps {
		stepKind := strings.ToLower(strings.TrimSpace(step.Kind))
		if !isValidStepKind(stepKind) {
			return FlowRecord{}, fmt.Errorf("%w: invalid step kind %q", ErrValidation, step.Kind)
		}
		stepID := strings.TrimSpace(step.ID)
		if stepID == "" {
			stepID = fmt.Sprintf("step-%d", idx+1)
		}
		cfgCopy := make(map[string]string, len(step.Config))
		for cfgKey, cfgVal := range step.Config {
			cfgCopy[cfgKey] = cfgVal
		}
		cleanSteps = append(cleanSteps, FlowStep{
			ID:     stepID,
			Kind:   stepKind,
			Config: cfgCopy,
		})
	}
	normalized.Steps = cleanSteps
	return normalized, nil
}

func isValidTriggerKind(kind string) bool {
	switch kind {
	case TriggerManual, TriggerInterval, TriggerRadarEvent, TriggerWhatsAppMsg:
		return true
	default:
		return false
	}
}

func isValidActionTier(tier string) bool {
	switch tier {
	case TierA0, TierA1, TierA2, TierA3:
		return true
	default:
		return false
	}
}

func isValidStepKind(kind string) bool {
	switch kind {
	case StepKindRadarRun, StepKindAIPrompt, StepKindCreateTask,
		StepKindSendWhatsApp, StepKindSendTelegram, StepKindSendDiscord,
		StepKindSendWebhook, StepKindBackup9Drive:
		return true
	default:
		return false
	}
}

func interpolateConfig(cfg map[string]string, triggerPayload, prevOutput string, stepOutputs map[string]string) map[string]string {
	resolved := make(map[string]string, len(cfg))
	for cfgKey, cfgVal := range cfg {
		updated := strings.ReplaceAll(cfgVal, "{{trigger}}", triggerPayload)
		updated = strings.ReplaceAll(updated, "{{prev}}", prevOutput)
		for stepID, stepOut := range stepOutputs {
			updated = strings.ReplaceAll(updated, "{{steps."+stepID+"}}", stepOut)
		}
		resolved[cfgKey] = updated
	}
	return resolved
}

func matchesTriggerFilter(triggerConfig map[string]any, payload string) bool {
	if len(triggerConfig) == 0 {
		return true
	}
	lowerPayload := strings.ToLower(payload)
	for _, key := range []string{"contains", "pattern", "match"} {
		if rawVal, exists := triggerConfig[key]; exists {
			filterText := strings.ToLower(strings.TrimSpace(fmt.Sprint(rawVal)))
			if filterText != "" && !strings.Contains(lowerPayload, filterText) {
				return false
			}
		}
	}
	return true
}

func parseIntervalDuration(triggerConfig map[string]any) time.Duration {
	if len(triggerConfig) > 0 {
		for _, key := range []string{"interval", "every"} {
			if rawVal, exists := triggerConfig[key]; exists {
				if parsed, ok := parseDurationValue(rawVal, time.Second); ok {
					return clampInterval(parsed)
				}
			}
		}
		for _, key := range []string{"intervalSeconds", "seconds"} {
			if rawVal, exists := triggerConfig[key]; exists {
				if parsed, ok := parseDurationValue(rawVal, time.Second); ok {
					return clampInterval(parsed)
				}
			}
		}
		for _, key := range []string{"intervalMs", "ms"} {
			if rawVal, exists := triggerConfig[key]; exists {
				if parsed, ok := parseDurationValue(rawVal, time.Millisecond); ok {
					return clampInterval(parsed)
				}
			}
		}
	}
	return defaultIntervalPeriod
}

func parseDurationValue(raw any, numericUnit time.Duration) (time.Duration, bool) {
	switch typed := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		if dur, err := time.ParseDuration(trimmed); err == nil && dur > 0 {
			return dur, true
		}
		if secs, err := strconv.ParseFloat(trimmed, 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(numericUnit)), true
		}
	case float64:
		if typed > 0 {
			return time.Duration(typed * float64(numericUnit)), true
		}
	case int:
		if typed > 0 {
			return time.Duration(typed) * numericUnit, true
		}
	case int64:
		if typed > 0 {
			return time.Duration(typed) * numericUnit, true
		}
	}
	return 0, false
}

func clampInterval(dur time.Duration) time.Duration {
	if dur < minIntervalPeriod {
		return minIntervalPeriod
	}
	return dur
}

func parseAnchorTime(candidates ...string) time.Time {
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
			return parsed.UTC()
		}
		if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

func firstNonEmpty(values ...string) string {
	for _, candidate := range values {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func boolToInt(flag bool) int {
	if flag {
		return 1
	}
	return 0
}

func generateID(prefix string) string {
	var rawBytes [8]byte
	_, _ = rand.Read(rawBytes[:])
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(rawBytes[:]))
}

func parseTriggerConfigJSON(rawJSON string) (map[string]any, error) {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	parsed := map[string]any{}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, fmt.Errorf("%w: invalid triggerConfigJson: %v", ErrValidation, err)
	}
	if parsed == nil {
		return map[string]any{}, nil
	}
	return parsed, nil
}
