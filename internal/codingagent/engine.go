package codingagent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// --- Session Store & Lifecycle Operations ---

// CreateSession persists a new coding agent session in SQLite.
func (e *Engine) CreateSession(ctx context.Context, input SessionCreateInput) (Session, error) {
	workDir := strings.TrimSpace(input.WorkDir)
	if workDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Session{}, fmt.Errorf("resolve working directory: %w", err)
		}
		workDir = cwd
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return Session{}, fmt.Errorf("resolve workDir %q: %w", workDir, err)
	}
	absWorkDir = filepath.Clean(absWorkDir)

	mode, err := normalizeMode(input.Mode)
	if err != nil {
		return Session{}, err
	}

	profile := strings.TrimSpace(input.AgentProfile)
	if profile == "" {
		profile = e.opts.DefaultAgentProfile
	}

	title := strings.TrimSpace(input.Title)
	if title == "" {
		baseName := filepath.Base(absWorkDir)
		if baseName != "" && baseName != "." && baseName != string(filepath.Separator) {
			title = "Session: " + baseName
		} else {
			title = "New Coding Session"
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	session := Session{
		ID:           generateRecordID("cs"),
		Title:        title,
		WorkDir:      absWorkDir,
		Project:      strings.TrimSpace(input.Project),
		AgentProfile: profile,
		Model:        strings.TrimSpace(input.Model),
		Mode:         mode,
		Status:       StatusIdle,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	_, err = e.db.ExecContext(ctx, `
INSERT INTO coding_sessions (
  id, title, work_dir, project, agent_profile, model, mode, status,
  total_tokens_in, total_tokens_out, rtk_tokens_saved, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?)`,
		session.ID, session.Title, session.WorkDir, session.Project,
		session.AgentProfile, session.Model, session.Mode, session.Status,
		session.CreatedAt, session.UpdatedAt,
	)
	if err != nil {
		return Session{}, fmt.Errorf("insert coding session: %w", err)
	}
	return session, nil
}

// ListSessions returns persisted coding sessions ordered by most recently updated first.
func (e *Engine) ListSessions(ctx context.Context, limit int) ([]Session, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := e.db.QueryContext(ctx, `
SELECT id, title, work_dir, project, agent_profile, model, mode, status,
       total_tokens_in, total_tokens_out, rtk_tokens_saved, created_at, updated_at
FROM coding_sessions
ORDER BY updated_at DESC, rowid DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := make([]Session, 0)
	for rows.Next() {
		var record Session
		if err := rows.Scan(
			&record.ID, &record.Title, &record.WorkDir, &record.Project,
			&record.AgentProfile, &record.Model, &record.Mode, &record.Status,
			&record.TotalTokensIn, &record.TotalTokensOut, &record.RTKTokensSaved,
			&record.CreatedAt, &record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		sessions = append(sessions, record)
	}
	return sessions, rows.Err()
}

// GetSession retrieves a single coding session by ID.
func (e *Engine) GetSession(ctx context.Context, sessionID string) (Session, error) {
	var record Session
	err := e.db.QueryRowContext(ctx, `
SELECT id, title, work_dir, project, agent_profile, model, mode, status,
       total_tokens_in, total_tokens_out, rtk_tokens_saved, created_at, updated_at
FROM coding_sessions
WHERE id = ?`, strings.TrimSpace(sessionID)).Scan(
		&record.ID, &record.Title, &record.WorkDir, &record.Project,
		&record.AgentProfile, &record.Model, &record.Mode, &record.Status,
		&record.TotalTokensIn, &record.TotalTokensOut, &record.RTKTokensSaved,
		&record.CreatedAt, &record.UpdatedAt,
	)
	if err != nil {
		return Session{}, err
	}
	return record, nil
}

// ListMessages retrieves all messages for a coding session ordered by sequence ascending.
func (e *Engine) ListMessages(ctx context.Context, sessionID string) ([]Message, error) {
	rows, err := e.db.QueryContext(ctx, `
SELECT id, session_id, seq, role, content, tool_calls_json, tool_call_id, tool_name, meta_json, created_at
FROM coding_messages
WHERE session_id = ?
ORDER BY seq ASC, rowid ASC`, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]Message, 0)
	for rows.Next() {
		var msg Message
		var toolCallsRaw, metaRaw string
		if err := rows.Scan(
			&msg.ID, &msg.SessionID, &msg.Seq, &msg.Role, &msg.Content,
			&toolCallsRaw, &msg.ToolCallID, &msg.ToolName, &metaRaw, &msg.CreatedAt,
		); err != nil {
			return nil, err
		}
		msg.ToolCalls = unmarshalToolCalls(toolCallsRaw)
		if len(msg.ToolCalls) > 0 {
			msg.ToolCallsJSON = toolCallsRaw
		}
		msg.Meta = unmarshalMessageMeta(metaRaw)
		if msg.Meta != nil {
			msg.MetaJSON = metaRaw
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

// GetSessionWithMessages retrieves a session along with its full ordered message history.
func (e *Engine) GetSessionWithMessages(ctx context.Context, sessionID string) (Session, []Message, error) {
	session, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, nil, err
	}
	messages, err := e.ListMessages(ctx, session.ID)
	if err != nil {
		return Session{}, nil, err
	}
	return session, messages, nil
}

// UpdateSession applies partial configuration updates to an existing coding session.
func (e *Engine) UpdateSession(ctx context.Context, sessionID string, patch SessionPatchInput) (Session, error) {
	existing, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}

	if patch.Title != nil {
		trimmedTitle := strings.TrimSpace(*patch.Title)
		if trimmedTitle != "" {
			existing.Title = trimmedTitle
		}
	}
	if patch.WorkDir != nil {
		trimmedDir := strings.TrimSpace(*patch.WorkDir)
		if trimmedDir == "" {
			return Session{}, errors.New("workDir cannot be empty")
		}
		absDir, absErr := filepath.Abs(trimmedDir)
		if absErr != nil {
			return Session{}, fmt.Errorf("resolve workDir: %w", absErr)
		}
		existing.WorkDir = filepath.Clean(absDir)
	}
	if patch.Project != nil {
		existing.Project = strings.TrimSpace(*patch.Project)
	}
	if patch.AgentProfile != nil {
		trimmedProfile := strings.TrimSpace(*patch.AgentProfile)
		if trimmedProfile != "" {
			existing.AgentProfile = trimmedProfile
		}
	}
	if patch.Model != nil {
		existing.Model = strings.TrimSpace(*patch.Model)
	}
	if patch.Mode != nil {
		normalizedMode, modeErr := normalizeMode(*patch.Mode)
		if modeErr != nil {
			return Session{}, modeErr
		}
		existing.Mode = normalizedMode
	}
	existing.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)

	_, err = e.db.ExecContext(ctx, `
UPDATE coding_sessions
SET title = ?, work_dir = ?, project = ?, agent_profile = ?, model = ?, mode = ?, updated_at = ?
WHERE id = ?`,
		existing.Title, existing.WorkDir, existing.Project, existing.AgentProfile,
		existing.Model, existing.Mode, existing.UpdatedAt, existing.ID,
	)
	if err != nil {
		return Session{}, err
	}
	return existing, nil
}

// DeleteSession aborts any running turn and removes the session and all of its messages.
func (e *Engine) DeleteSession(ctx context.Context, sessionID string) error {
	cleanID := strings.TrimSpace(sessionID)
	e.AbortSession(cleanID)

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM coding_messages WHERE session_id = ?`, cleanID); err != nil {
		return err
	}
	execOut, err := tx.ExecContext(ctx, `DELETE FROM coding_sessions WHERE id = ?`, cleanID)
	if err != nil {
		return err
	}
	affected, _ := execOut.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// ForkSession creates a Pi-style branched copy of an existing session and its messages up to latest (or optional upToSeq).
func (e *Engine) ForkSession(ctx context.Context, sessionID, title string, upToSeq ...int) (Session, error) {
	parent, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	parentMessages, err := e.ListMessages(ctx, parent.ID)
	if err != nil {
		return Session{}, err
	}

	maxSeq := 0
	if len(upToSeq) > 0 && upToSeq[0] > 0 {
		maxSeq = upToSeq[0]
	}

	copiedMessages := make([]Message, 0, len(parentMessages))
	for _, msg := range parentMessages {
		if maxSeq > 0 && msg.Seq > maxSeq {
			continue
		}
		copiedMessages = append(copiedMessages, msg)
	}

	forkTitle := strings.TrimSpace(title)
	if forkTitle == "" {
		forkTitle = parent.Title + " (fork)"
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	forked := Session{
		ID:           generateRecordID("cs"),
		Title:        forkTitle,
		WorkDir:      parent.WorkDir,
		Project:      parent.Project,
		AgentProfile: parent.AgentProfile,
		Model:        parent.Model,
		Mode:         parent.Mode,
		Status:       StatusIdle,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
INSERT INTO coding_sessions (
  id, title, work_dir, project, agent_profile, model, mode, status,
  total_tokens_in, total_tokens_out, rtk_tokens_saved, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?)`,
		forked.ID, forked.Title, forked.WorkDir, forked.Project,
		forked.AgentProfile, forked.Model, forked.Mode, forked.Status,
		forked.CreatedAt, forked.UpdatedAt,
	)
	if err != nil {
		return Session{}, fmt.Errorf("insert forked session: %w", err)
	}

	for idx, origMsg := range copiedMessages {
		newMsgID := generateRecordID("cm")
		_, err := tx.ExecContext(ctx, `
INSERT INTO coding_messages (
  id, session_id, seq, role, content, tool_calls_json, tool_call_id, tool_name, meta_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newMsgID, forked.ID, idx+1, origMsg.Role, origMsg.Content,
			marshalToolCalls(origMsg.ToolCalls), origMsg.ToolCallID, origMsg.ToolName,
			marshalMessageMeta(origMsg.Meta), origMsg.CreatedAt,
		)
		if err != nil {
			return Session{}, fmt.Errorf("copy forked message %d: %w", idx+1, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return forked, nil
}

// CompactSession summarizes older messages into a concise system context block while preserving recent turns cleanly.
func (e *Engine) CompactSession(ctx context.Context, sessionID string) (CompactResult, error) {
	session, err := e.GetSession(ctx, sessionID)
	if err != nil {
		return CompactResult{}, err
	}
	messages, err := e.ListMessages(ctx, session.ID)
	if err != nil {
		return CompactResult{}, err
	}

	totalCount := len(messages)
	if totalCount <= 2 {
		return CompactResult{
			Compacted:    false,
			Summary:      "Session has too few messages to compact.",
			KeptMessages: totalCount,
		}, nil
	}

	splitIdx := chooseCompactionSplitIndex(messages)
	if splitIdx < 2 {
		return CompactResult{
			Compacted:    false,
			Summary:      "No safe compaction boundary found without splitting active tool calls.",
			KeptMessages: totalCount,
		}, nil
	}

	olderMessages := messages[:splitIdx]
	recentMessages := messages[splitIdx:]
	summaryText := buildCompactionSummary(olderMessages)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	summaryMsg := Message{
		ID:        generateRecordID("cm"),
		SessionID: session.ID,
		Seq:       1,
		Role:      "system",
		Content:   summaryText,
		Meta:      &MessageMeta{Compacted: true},
		CreatedAt: now,
	}

	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return CompactResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM coding_messages WHERE session_id = ?`, session.ID); err != nil {
		return CompactResult{}, err
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO coding_messages (
  id, session_id, seq, role, content, tool_calls_json, tool_call_id, tool_name, meta_json, created_at
) VALUES (?, ?, ?, ?, ?, '[]', '', '', ?, ?)`,
		summaryMsg.ID, summaryMsg.SessionID, summaryMsg.Seq, summaryMsg.Role, summaryMsg.Content,
		marshalMessageMeta(summaryMsg.Meta), summaryMsg.CreatedAt,
	); err != nil {
		return CompactResult{}, err
	}

	for idx, kept := range recentMessages {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO coding_messages (
  id, session_id, seq, role, content, tool_calls_json, tool_call_id, tool_name, meta_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			kept.ID, session.ID, idx+2, kept.Role, kept.Content,
			marshalToolCalls(kept.ToolCalls), kept.ToolCallID, kept.ToolName,
			marshalMessageMeta(kept.Meta), kept.CreatedAt,
		); err != nil {
			return CompactResult{}, err
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE coding_sessions SET updated_at = ? WHERE id = ?`, now, session.ID); err != nil {
		return CompactResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return CompactResult{}, err
	}

	return CompactResult{
		Compacted:    true,
		Summary:      summaryText,
		KeptMessages: 1 + len(recentMessages),
	}, nil
}

func chooseCompactionSplitIndex(messages []Message) int {
	totalCount := len(messages)
	keepTarget := 2
	if totalCount > 6 {
		keepTarget = 4
	} else if totalCount <= 3 {
		keepTarget = 1
	}

	splitIdx := totalCount - keepTarget
	// Never leave an orphaned tool response message at the beginning of the kept slice.
	for splitIdx < totalCount && messages[splitIdx].Role == "tool" {
		splitIdx++
	}
	if splitIdx >= totalCount {
		// Fall back to keeping nothing if only trailing tools existed; scan backward for a clean boundary.
		splitIdx = totalCount - keepTarget
		for splitIdx > 0 && messages[splitIdx].Role == "tool" {
			splitIdx--
		}
	}
	return splitIdx
}

func buildCompactionSummary(older []Message) string {
	var userNotes []string
	var assistantNotes []string
	var toolTrace []string

	for _, msg := range older {
		switch msg.Role {
		case "user":
			if condensed := condenseText(msg.Content, 160); condensed != "" {
				userNotes = append(userNotes, condensed)
			}
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				for _, call := range msg.ToolCalls {
					toolTrace = append(toolTrace, fmt.Sprintf("%s(%s)", call.Function.Name, condenseText(call.Function.Arguments, 80)))
				}
			}
			if condensed := condenseText(msg.Content, 180); condensed != "" {
				assistantNotes = append(assistantNotes, condensed)
			}
		case "tool":
			statusTag := "ok"
			if msg.Meta != nil && msg.Meta.IsError {
				statusTag = "error"
			}
			toolTrace = append(toolTrace, fmt.Sprintf("%s -> %s: %s", msg.ToolName, statusTag, condenseText(msg.Content, 90)))
		case "system":
			if condensed := condenseText(msg.Content, 200); condensed != "" {
				assistantNotes = append(assistantNotes, condensed)
			}
		}
	}

	var summary strings.Builder
	fmt.Fprintf(&summary, "[Compacted Conversation Summary — %d earlier messages]\n", len(older))
	if len(userNotes) > 0 {
		summary.WriteString("User requests:\n")
		for _, note := range limitSlice(userNotes, 6) {
			fmt.Fprintf(&summary, "- %s\n", note)
		}
	}
	if len(toolTrace) > 0 {
		summary.WriteString("Tool activity:\n")
		for _, trace := range limitSlice(toolTrace, 10) {
			fmt.Fprintf(&summary, "- %s\n", trace)
		}
	}
	if len(assistantNotes) > 0 {
		summary.WriteString("Prior findings & replies:\n")
		for _, note := range limitSlice(assistantNotes, 6) {
			fmt.Fprintf(&summary, "- %s\n", note)
		}
	}
	return strings.TrimRight(summary.String(), "\n")
}

func condenseText(raw string, maxChars int) string {
	collapsed := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if len(collapsed) <= maxChars {
		return collapsed
	}
	if maxChars <= 3 {
		return collapsed[:maxChars]
	}
	return collapsed[:maxChars-3] + "..."
}

func limitSlice(values []string, maxLen int) []string {
	if len(values) <= maxLen {
		return values
	}
	return values[len(values)-maxLen:]
}

// --- Live Event Bus & Cancellation ---

// Subscribe registers an SSE or programmatic event listener for the given sessionID.
func (e *Engine) Subscribe(sessionID string) (<-chan AgentEvent, func()) {
	cleanID := strings.TrimSpace(sessionID)
	eventCh := make(chan AgentEvent, 64)

	e.mu.Lock()
	e.nextSubID++
	subID := e.nextSubID
	if e.subscribers[cleanID] == nil {
		e.subscribers[cleanID] = make(map[uint64]chan AgentEvent)
	}
	e.subscribers[cleanID][subID] = eventCh
	e.mu.Unlock()

	var unsubscribeOnce bool
	unsubscribe := func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if unsubscribeOnce {
			return
		}
		unsubscribeOnce = true
		if listeners := e.subscribers[cleanID]; listeners != nil {
			delete(listeners, subID)
			if len(listeners) == 0 {
				delete(e.subscribers, cleanID)
			}
		}
	}
	return eventCh, unsubscribe
}

func (e *Engine) emitEvent(event AgentEvent, onEvent func(AgentEvent)) {
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if onEvent != nil {
		onEvent(event)
	}

	e.mu.RLock()
	listeners := e.subscribers[event.SessionID]
	targets := make([]chan AgentEvent, 0, len(listeners))
	for _, listenerCh := range listeners {
		targets = append(targets, listenerCh)
	}
	e.mu.RUnlock()

	for _, listenerCh := range targets {
		select {
		case listenerCh <- event:
		default:
		}
	}
}

// AbortSession cancels any in-flight LLM request or shell command for sessionID.
func (e *Engine) AbortSession(sessionID string) bool {
	cleanID := strings.TrimSpace(sessionID)
	e.mu.Lock()
	handle, running := e.activeTurns[cleanID]
	e.mu.Unlock()
	if !running || handle.cancel == nil {
		return false
	}
	handle.cancel()
	return true
}

func (e *Engine) acquireTurn(parentCtx context.Context, sessionID string) (context.Context, func(), error) {
	cleanID := strings.TrimSpace(sessionID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, busy := e.activeTurns[cleanID]; busy {
		return nil, nil, ErrSessionBusy
	}
	turnCtx, cancel := context.WithCancel(parentCtx)
	e.activeTurns[cleanID] = turnHandle{cancel: cancel}
	release := func() {
		cancel()
		e.mu.Lock()
		delete(e.activeTurns, cleanID)
		e.mu.Unlock()
	}
	return turnCtx, release, nil
}

// --- ReAct / Pi Agent Execution Loop ---

// RunPrompt executes a full coding agent turn for sessionID and returns the updated session, messages, and final reply.
func (e *Engine) RunPrompt(ctx context.Context, sessionID, prompt string) (TurnResult, error) {
	return e.RunPromptWithCallback(ctx, sessionID, prompt, nil)
}

// RunPromptWithCallback executes a prompt turn while streaming AgentEvents to onEvent and SSE subscribers.
func (e *Engine) RunPromptWithCallback(ctx context.Context, sessionID, prompt string, onEvent func(AgentEvent)) (TurnResult, error) {
	turnCtx, release, err := e.acquireTurn(ctx, sessionID)
	if err != nil {
		return TurnResult{}, err
	}
	defer release()
	return e.executeAcquiredTurn(turnCtx, sessionID, prompt, onEvent)
}

// StartPromptAsync reserves the session turn synchronously and launches execution in a background goroutine.
func (e *Engine) StartPromptAsync(sessionID, prompt string) error {
	if _, err := e.GetSession(context.Background(), sessionID); err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" {
		return errors.New("prompt is required")
	}
	turnCtx, release, err := e.acquireTurn(context.Background(), sessionID)
	if err != nil {
		return err
	}
	go func() {
		defer release()
		_, _ = e.executeAcquiredTurn(turnCtx, sessionID, prompt, nil)
	}()
	return nil
}

// RunOneShot creates an ephemeral session in SQLite and runs a single coding agent prompt for CLI workflows (`mw-echo code`).
func (e *Engine) RunOneShot(ctx context.Context, workDir, agentProfile, model, mode, prompt string, onEvent func(AgentEvent)) (string, error) {
	trimmedPrompt := strings.TrimSpace(prompt)
	if trimmedPrompt == "" {
		return "", errors.New("prompt is required")
	}
	session, err := e.CreateSession(ctx, SessionCreateInput{
		Title:        condenseText(trimmedPrompt, 64),
		WorkDir:      workDir,
		AgentProfile: agentProfile,
		Model:        model,
		Mode:         mode,
	})
	if err != nil {
		return "", err
	}
	outcome, err := e.RunPromptWithCallback(ctx, session.ID, trimmedPrompt, onEvent)
	if err != nil {
		return outcome.Reply, err
	}
	return outcome.Reply, nil
}

func (e *Engine) executeAcquiredTurn(turnCtx context.Context, sessionID, rawPrompt string, onEvent func(AgentEvent)) (TurnResult, error) {
	trimmedPrompt := strings.TrimSpace(rawPrompt)
	if trimmedPrompt == "" {
		return TurnResult{}, errors.New("prompt is required")
	}

	session, err := e.GetSession(turnCtx, sessionID)
	if err != nil {
		return TurnResult{}, err
	}

	// Handle slash commands (/plan, /act, /compact).
	handled, commandResult, nextPrompt, session, slashErr := e.handleSlashCommand(turnCtx, session, trimmedPrompt, onEvent)
	if slashErr != nil {
		return TurnResult{}, slashErr
	}
	if handled {
		return commandResult, nil
	}
	trimmedPrompt = nextPrompt

	// Mark session running.
	if err := e.setSessionStatus(context.Background(), session.ID, StatusRunning); err != nil {
		return TurnResult{}, err
	}
	session.Status = StatusRunning

	// Persist user message.
	userMsg, err := e.appendMessage(context.Background(), session.ID, Message{
		Role:    "user",
		Content: trimmedPrompt,
	})
	if err != nil {
		_ = e.setSessionStatus(context.Background(), session.ID, StatusError)
		return TurnResult{}, err
	}
	if strings.HasPrefix(session.Title, "Session:") || session.Title == "New Coding Session" {
		if userMsg.Seq == 1 {
			derivedTitle := condenseText(trimmedPrompt, 64)
			if derivedTitle != "" {
				session.Title = derivedTitle
				_, _ = e.db.ExecContext(context.Background(), `UPDATE coding_sessions SET title = ?, updated_at = ? WHERE id = ?`,
					derivedTitle, time.Now().UTC().Format(time.RFC3339Nano), session.ID)
			}
		}
	}

	e.emitEvent(AgentEvent{
		Type:      "turn_start",
		SessionID: session.ID,
		Mode:      session.Mode,
		Message:   &userMsg,
	}, onEvent)

	toolSchemas := e.openAIToolSchemas()
	finalReply := ""
	executedSteps := 0

	for step := 1; step <= e.opts.MaxSteps; step++ {
		if turnCtx.Err() != nil {
			return e.finishAbortedTurn(session.ID, executedSteps, onEvent)
		}

		executedSteps = step
		e.emitEvent(AgentEvent{
			Type:      "step",
			SessionID: session.ID,
			Step:      step,
			Mode:      session.Mode,
		}, onEvent)

		history, histErr := e.ListMessages(turnCtx, session.ID)
		if histErr != nil {
			return e.finishErroredTurn(session.ID, executedSteps, histErr, onEvent)
		}

		gatewayMessages := buildGatewayMessages(session, history)
		completion, gwMeta, callErr := e.callGatewayCompletion(turnCtx, session, gatewayMessages, toolSchemas)
		if callErr != nil {
			if errors.Is(turnCtx.Err(), context.Canceled) || errors.Is(callErr, context.Canceled) {
				return e.finishAbortedTurn(session.ID, executedSteps, onEvent)
			}
			return e.finishErroredTurn(session.ID, executedSteps, callErr, onEvent)
		}

		// Record token & RTK usage on the session.
		if err := e.addSessionTelemetry(context.Background(), session.ID, int64(gwMeta.TokensIn), int64(gwMeta.TokensOut), int64(gwMeta.RTKTokensSaved)); err != nil {
			return e.finishErroredTurn(session.ID, executedSteps, err, onEvent)
		}

		gwMeta.Step = step
		assistantText := completion.Content
		assistantCalls := completion.ToolCalls

		assistantMsg, appendErr := e.appendMessage(context.Background(), session.ID, Message{
			Role:      "assistant",
			Content:   assistantText,
			ToolCalls: assistantCalls,
			Meta:      &gwMeta,
		})
		if appendErr != nil {
			return e.finishErroredTurn(session.ID, executedSteps, appendErr, onEvent)
		}

		e.emitEvent(AgentEvent{
			Type:           "assistant_message",
			SessionID:      session.ID,
			Step:           step,
			Role:           "assistant",
			Content:        assistantText,
			Message:        &assistantMsg,
			Reply:          assistantText,
			Provider:       gwMeta.Provider,
			Model:          gwMeta.Model,
			Cache:          gwMeta.Cache,
			RTKSaved:       gwMeta.RTKTokensSaved,
			RTKTokensSaved: gwMeta.RTKTokensSaved,
			TokensIn:       gwMeta.TokensIn,
			TokensOut:      gwMeta.TokensOut,
			Mode:           session.Mode,
		}, onEvent)

		if len(assistantCalls) == 0 {
			finalReply = assistantText
			if err := e.setSessionStatus(context.Background(), session.ID, StatusIdle); err != nil {
				return TurnResult{}, err
			}
			updatedSession, updatedMessages, loadErr := e.GetSessionWithMessages(context.Background(), session.ID)
			if loadErr != nil {
				return TurnResult{}, loadErr
			}
			e.emitEvent(AgentEvent{
				Type:           "turn_end",
				SessionID:      session.ID,
				Step:           step,
				Role:           "assistant",
				Content:        finalReply,
				Reply:          finalReply,
				Provider:       gwMeta.Provider,
				Model:          gwMeta.Model,
				Cache:          gwMeta.Cache,
				RTKSaved:       gwMeta.RTKTokensSaved,
				RTKTokensSaved: gwMeta.RTKTokensSaved,
				TokensIn:       gwMeta.TokensIn,
				TokensOut:      gwMeta.TokensOut,
				Mode:           updatedSession.Mode,
			}, onEvent)
			return TurnResult{
				Session:  updatedSession,
				Messages: updatedMessages,
				Reply:    finalReply,
				Steps:    executedSteps,
			}, nil
		}

		// Execute tool calls sequentially within this step.
		for _, call := range assistantCalls {
			if turnCtx.Err() != nil {
				return e.finishAbortedTurn(session.ID, executedSteps, onEvent)
			}

			callCopy := call
			e.emitEvent(AgentEvent{
				Type:       "tool_call",
				SessionID:  session.ID,
				Step:       step,
				Role:       "assistant",
				ToolCall:   &callCopy,
				ToolCallID: call.ID,
				ToolName:   call.Function.Name,
				Arguments:  call.Function.Arguments,
				Content:    call.Function.Arguments,
				Mode:       session.Mode,
			}, onEvent)

			toolStarted := time.Now()
			toolOutput, toolErr := e.ExecuteTool(turnCtx, session, call.Function.Name, call.Function.Arguments)
			durationMS := time.Since(toolStarted).Milliseconds()

			if errors.Is(turnCtx.Err(), context.Canceled) || errors.Is(toolErr, context.Canceled) {
				return e.finishAbortedTurn(session.ID, executedSteps, onEvent)
			}

			isToolErr := false
			if toolErr != nil {
				isToolErr = true
				toolOutput = "ERROR: " + toolErr.Error()
			}

			toolMsg, saveToolErr := e.appendMessage(context.Background(), session.ID, Message{
				Role:       "tool",
				Content:    toolOutput,
				ToolCallID: call.ID,
				ToolName:   call.Function.Name,
				Meta: &MessageMeta{
					Step:       step,
					DurationMS: durationMS,
					IsError:    isToolErr,
				},
			})
			if saveToolErr != nil {
				return e.finishErroredTurn(session.ID, executedSteps, saveToolErr, onEvent)
			}

			e.emitEvent(AgentEvent{
				Type:       "tool_result",
				SessionID:  session.ID,
				Step:       step,
				Role:       "tool",
				ToolCall:   &callCopy,
				ToolCallID: call.ID,
				ToolName:   call.Function.Name,
				Output:     toolOutput,
				ToolOutput: toolOutput,
				Content:    toolOutput,
				Message:    &toolMsg,
				Mode:       session.Mode,
			}, onEvent)
		}
	}

	// MaxSteps reached without a final text reply.
	finalReply = fmt.Sprintf("Reached maximum step limit (%d steps) before final assistant response.", e.opts.MaxSteps)
	wrapMsg, _ := e.appendMessage(context.Background(), session.ID, Message{
		Role:    "assistant",
		Content: finalReply,
		Meta:    &MessageMeta{Step: executedSteps},
	})
	_ = e.setSessionStatus(context.Background(), session.ID, StatusIdle)
	updatedSession, updatedMessages, loadErr := e.GetSessionWithMessages(context.Background(), session.ID)
	if loadErr != nil {
		return TurnResult{}, loadErr
	}
	e.emitEvent(AgentEvent{
		Type:      "turn_end",
		SessionID: session.ID,
		Step:      executedSteps,
		Message:   &wrapMsg,
		Reply:     finalReply,
		Mode:      updatedSession.Mode,
	}, onEvent)

	return TurnResult{
		Session:  updatedSession,
		Messages: updatedMessages,
		Reply:    finalReply,
		Steps:    executedSteps,
	}, nil
}

func (e *Engine) handleSlashCommand(ctx context.Context, session Session, prompt string, onEvent func(AgentEvent)) (bool, TurnResult, string, Session, error) {
	if !strings.HasPrefix(prompt, "/") {
		return false, TurnResult{}, prompt, session, nil
	}

	fields := strings.Fields(prompt)
	if len(fields) == 0 {
		return false, TurnResult{}, prompt, session, nil
	}
	cmd := strings.ToLower(fields[0])
	remainder := strings.TrimSpace(strings.TrimPrefix(prompt, fields[0]))

	switch cmd {
	case "/plan", "/act":
		targetMode := ModeAct
		if cmd == "/plan" {
			targetMode = ModePlan
		}
		updated, err := e.UpdateSession(ctx, session.ID, SessionPatchInput{Mode: &targetMode})
		if err != nil {
			return false, TurnResult{}, "", session, err
		}
		if remainder != "" {
			return false, TurnResult{}, remainder, updated, nil
		}

		ackReply := "Switched session to act mode (full tool access enabled)."
		if targetMode == ModePlan {
			ackReply = "Switched session to plan mode (read-only: write_file, edit_file, and run_shell are blocked)."
		}
		userMsg, err := e.appendMessage(ctx, updated.ID, Message{Role: "user", Content: prompt})
		if err != nil {
			return false, TurnResult{}, "", updated, err
		}
		e.emitEvent(AgentEvent{Type: "turn_start", SessionID: updated.ID, Mode: updated.Mode, Message: &userMsg}, onEvent)
		ackMsg, err := e.appendMessage(ctx, updated.ID, Message{Role: "assistant", Content: ackReply})
		if err != nil {
			return false, TurnResult{}, "", updated, err
		}
		e.emitEvent(AgentEvent{Type: "assistant_message", SessionID: updated.ID, Role: "assistant", Mode: updated.Mode, Message: &ackMsg, Content: ackReply, Reply: ackReply}, onEvent)
		e.emitEvent(AgentEvent{Type: "turn_end", SessionID: updated.ID, Role: "assistant", Mode: updated.Mode, Content: ackReply, Reply: ackReply}, onEvent)

		latestSession, latestMsgs, err := e.GetSessionWithMessages(ctx, updated.ID)
		if err != nil {
			return false, TurnResult{}, "", updated, err
		}
		return true, TurnResult{Session: latestSession, Messages: latestMsgs, Reply: ackReply}, "", latestSession, nil

	case "/compact":
		e.emitEvent(AgentEvent{Type: "turn_start", SessionID: session.ID, Mode: session.Mode}, onEvent)
		compacted, err := e.CompactSession(ctx, session.ID)
		if err != nil {
			return false, TurnResult{}, "", session, err
		}
		ackReply := fmt.Sprintf("Compacted session history (%d messages kept).\n%s", compacted.KeptMessages, compacted.Summary)
		if !compacted.Compacted {
			ackReply = compacted.Summary
		}
		e.emitEvent(AgentEvent{Type: "assistant_message", SessionID: session.ID, Role: "assistant", Mode: session.Mode, Content: ackReply, Reply: ackReply}, onEvent)
		e.emitEvent(AgentEvent{Type: "turn_end", SessionID: session.ID, Role: "assistant", Mode: session.Mode, Content: ackReply, Reply: ackReply}, onEvent)

		latestSession, latestMsgs, err := e.GetSessionWithMessages(ctx, session.ID)
		if err != nil {
			return false, TurnResult{}, "", session, err
		}
		return true, TurnResult{Session: latestSession, Messages: latestMsgs, Reply: ackReply}, "", latestSession, nil

	default:
		return false, TurnResult{}, prompt, session, nil
	}
}

func (e *Engine) finishAbortedTurn(sessionID string, steps int, onEvent func(AgentEvent)) (TurnResult, error) {
	_ = e.setSessionStatus(context.Background(), sessionID, StatusAborted)
	session, messages, _ := e.GetSessionWithMessages(context.Background(), sessionID)
	e.emitEvent(AgentEvent{
		Type:      "aborted",
		SessionID: sessionID,
		Step:      steps,
		Mode:      session.Mode,
		Error:     "turn aborted",
	}, onEvent)
	return TurnResult{
		Session:  session,
		Messages: messages,
		Steps:    steps,
		Aborted:  true,
	}, context.Canceled
}

func (e *Engine) finishErroredTurn(sessionID string, steps int, turnErr error, onEvent func(AgentEvent)) (TurnResult, error) {
	_ = e.setSessionStatus(context.Background(), sessionID, StatusError)
	session, messages, _ := e.GetSessionWithMessages(context.Background(), sessionID)
	e.emitEvent(AgentEvent{
		Type:      "error",
		SessionID: sessionID,
		Step:      steps,
		Mode:      session.Mode,
		Error:     turnErr.Error(),
	}, onEvent)
	return TurnResult{
		Session:  session,
		Messages: messages,
		Steps:    steps,
	}, turnErr
}

func (e *Engine) setSessionStatus(ctx context.Context, sessionID, status string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := e.db.ExecContext(ctx, `UPDATE coding_sessions SET status = ?, updated_at = ? WHERE id = ?`, status, now, sessionID)
	return err
}

func (e *Engine) addSessionTelemetry(ctx context.Context, sessionID string, tokensIn, tokensOut, rtkSaved int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := e.db.ExecContext(ctx, `
UPDATE coding_sessions
SET total_tokens_in = total_tokens_in + ?,
    total_tokens_out = total_tokens_out + ?,
    rtk_tokens_saved = rtk_tokens_saved + ?,
    updated_at = ?
WHERE id = ?`, tokensIn, tokensOut, rtkSaved, now, sessionID)
	return err
}

func (e *Engine) appendMessage(ctx context.Context, sessionID string, msg Message) (Message, error) {
	var nextSeq int
	if err := e.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM coding_messages WHERE session_id = ?`, sessionID).Scan(&nextSeq); err != nil {
		return Message{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	msg.ID = generateRecordID("cm")
	msg.SessionID = sessionID
	msg.Seq = nextSeq
	msg.CreatedAt = now

	toolCallsRaw := marshalToolCalls(msg.ToolCalls)
	metaRaw := marshalMessageMeta(msg.Meta)
	if len(msg.ToolCalls) > 0 {
		msg.ToolCallsJSON = toolCallsRaw
	}
	if msg.Meta != nil {
		msg.MetaJSON = metaRaw
	}

	_, err := e.db.ExecContext(ctx, `
INSERT INTO coding_messages (
  id, session_id, seq, role, content, tool_calls_json, tool_call_id, tool_name, meta_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.SessionID, msg.Seq, msg.Role, msg.Content,
		toolCallsRaw, msg.ToolCallID, msg.ToolName,
		metaRaw, msg.CreatedAt,
	)
	if err != nil {
		return Message{}, err
	}
	_, _ = e.db.ExecContext(ctx, `UPDATE coding_sessions SET updated_at = ? WHERE id = ?`, now, sessionID)
	return msg, nil
}

// --- OpenAI AI Gateway Wire Transport ---

type gatewayChatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type gatewayCompletionRequest struct {
	Model      string               `json:"model"`
	Messages   []gatewayChatMessage `json:"messages"`
	Tools      []map[string]any     `json:"tools,omitempty"`
	ToolChoice string               `json:"tool_choice,omitempty"`
}

type parsedAssistantTurn struct {
	Content   string
	ToolCalls []ToolCall
}

func buildSystemPrompt(session Session) string {
	modeNote := "ACT mode (read, write, edit, and shell tools are enabled)."
	if strings.EqualFold(session.Mode, ModePlan) {
		modeNote = "PLAN mode (read-only analysis: write_file, edit_file, and run_shell are blocked)."
	}
	return fmt.Sprintf(
		"You are mw-echo Native Coding Agent operating inside workspace %q in %s "+
			"Inspect relevant files before modifying them, make minimal verified edits within the workspace sandbox, "+
			"and provide concise technical answers citing exact file paths.",
		session.WorkDir, modeNote,
	)
}

func buildGatewayMessages(session Session, history []Message) []gatewayChatMessage {
	outbound := make([]gatewayChatMessage, 0, len(history)+1)
	outbound = append(outbound, gatewayChatMessage{
		Role:    "system",
		Content: buildSystemPrompt(session),
	})
	for _, msg := range history {
		wire := gatewayChatMessage{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
			ToolCalls:  msg.ToolCalls,
		}
		if msg.Role == "tool" && msg.ToolName != "" {
			wire.Name = msg.ToolName
		}
		outbound = append(outbound, wire)
	}
	return outbound
}

func (e *Engine) callGatewayCompletion(
	ctx context.Context,
	session Session,
	messages []gatewayChatMessage,
	toolSchemas []map[string]any,
) (parsedAssistantTurn, MessageMeta, error) {
	targetModel := strings.TrimSpace(session.Model)
	if targetModel == "" {
		targetModel = "auto"
	}

	reqBody := gatewayCompletionRequest{
		Model:      targetModel,
		Messages:   messages,
		Tools:      toolSchemas,
		ToolChoice: "auto",
	}
	encodedReq, err := json.Marshal(reqBody)
	if err != nil {
		return parsedAssistantTurn{}, MessageMeta{}, fmt.Errorf("marshal gateway request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.opts.DefaultGatewayURL, bytes.NewReader(encodedReq))
	if err != nil {
		return parsedAssistantTurn{}, MessageMeta{}, fmt.Errorf("create gateway HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	agentProfile := strings.TrimSpace(session.AgentProfile)
	if agentProfile == "" {
		agentProfile = e.opts.DefaultAgentProfile
	}
	httpReq.Header.Set(headerAgentProfile, agentProfile)

	httpResp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return parsedAssistantTurn{}, MessageMeta{}, fmt.Errorf("gateway request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, readErr := io.ReadAll(io.LimitReader(httpResp.Body, 16*1024*1024))
	if readErr != nil {
		return parsedAssistantTurn{}, MessageMeta{}, fmt.Errorf("read gateway response: %w", readErr)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return parsedAssistantTurn{}, MessageMeta{}, fmt.Errorf("gateway HTTP %d: %s", httpResp.StatusCode, condenseText(string(respBody), 300))
	}

	var envelope struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role         string            `json:"role"`
				Content      any               `json:"content"`
				ToolCalls    []ToolCall        `json:"tool_calls"`
				FunctionCall *ToolCallFunction `json:"function_call"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return parsedAssistantTurn{}, MessageMeta{}, fmt.Errorf("decode gateway response JSON: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return parsedAssistantTurn{}, MessageMeta{}, errors.New("gateway returned empty choices array")
	}

	primaryMsg := envelope.Choices[0].Message
	textReply := extractContentText(primaryMsg.Content)

	parsedCalls := make([]ToolCall, 0, len(primaryMsg.ToolCalls))
	for idx, call := range primaryMsg.ToolCalls {
		cleanName := strings.TrimSpace(call.Function.Name)
		if cleanName == "" {
			continue
		}
		if strings.TrimSpace(call.ID) == "" {
			call.ID = fmt.Sprintf("call_%d_%s", idx+1, generateRecordID("tc"))
		}
		if strings.TrimSpace(call.Type) == "" {
			call.Type = "function"
		}
		call.Function.Name = cleanName
		parsedCalls = append(parsedCalls, call)
	}

	// Support legacy single function_call field if tool_calls was omitted.
	if len(parsedCalls) == 0 && primaryMsg.FunctionCall != nil && strings.TrimSpace(primaryMsg.FunctionCall.Name) != "" {
		parsedCalls = append(parsedCalls, ToolCall{
			ID:       generateRecordID("call"),
			Type:     "function",
			Function: *primaryMsg.FunctionCall,
		})
	}

	rtkSaved, _ := strconv.Atoi(strings.TrimSpace(httpResp.Header.Get(headerEchoRTKSaved)))
	resolvedModel := strings.TrimSpace(httpResp.Header.Get(headerEchoModel))
	if resolvedModel == "" {
		resolvedModel = strings.TrimSpace(envelope.Model)
	}

	meta := MessageMeta{
		Provider:       strings.TrimSpace(httpResp.Header.Get(headerEchoProvider)),
		Model:          resolvedModel,
		Cache:          strings.TrimSpace(httpResp.Header.Get(headerEchoCache)),
		RTKTokensSaved: rtkSaved,
		TokensIn:       envelope.Usage.PromptTokens,
		TokensOut:      envelope.Usage.CompletionTokens,
	}

	return parsedAssistantTurn{
		Content:   textReply,
		ToolCalls: parsedCalls,
	}, meta, nil
}

func extractContentText(rawContent any) string {
	switch typedVal := rawContent.(type) {
	case nil:
		return ""
	case string:
		return typedVal
	case []any:
		parts := make([]string, 0, len(typedVal))
		for _, block := range typedVal {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if textStr, hasText := blockMap["text"].(string); hasText && textStr != "" {
				parts = append(parts, textStr)
			}
		}
		return strings.Join(parts, "\n")
	default:
		encoded, err := json.Marshal(typedVal)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}
