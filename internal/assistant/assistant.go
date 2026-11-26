package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

// Persona defines an AI operator personality backed by an mw-echo AI Gateway agent route.
type Persona struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	Accent       string `json:"accent"`
	AgentProfile string `json:"agentProfile"`
	SystemPrompt string `json:"systemPrompt"`
}

// ChatMessage stores an assistant conversation turn in SQLite.
type ChatMessage struct {
	ID          string `json:"id"`
	Persona     string `json:"persona"`
	Role        string `json:"role"`
	Content     string `json:"content"`
	ToolSummary string `json:"toolSummary,omitempty"`
	Model       string `json:"model,omitempty"`
	CreatedAt   string `json:"createdAt"`
}

// Hooks connects Shadow Sidekick to live gamification, WhatsApp GOWA, Coding Agent, and catalog sniffing.
type Hooks struct {
	SendWhatsApp       func(ctx context.Context, deviceID, phone, message string) (string, error)
	LaunchCoderMission func(ctx context.Context, workDir, prompt string) (string, error)
	GetGameSummary     func(ctx context.Context) string
	AwardXP            func(ctx context.Context, eventType, label string, baseXP int, refID string)
}

// Options configures the Assistant service.
type Options struct {
	GatewayURL string
	HTTPClient *http.Client
	Hooks      Hooks
}

// Service powers the Shadow Sidekick AI assistant routed through mw-echo's AI Gateway.
type Service struct {
	hub        *store.HubStore
	gatewayURL string
	httpClient *http.Client
	hooks      Hooks
}

var defaultPersonas = []Persona{
	{
		ID:           "tactician",
		Name:         "TACTICIAN",
		Role:         "Principal Architect & Boss Strategist",
		Accent:       "#ffe031",
		AgentProfile: "claude-code",
		SystemPrompt: "You are TACTICIAN inside the mw-echo RIOT command hub. Be sharp, concise, architectural. Prioritize P0 blockers, milestone bosses, and zero-waste engineering.",
	},
	{
		ID:           "hacker",
		Name:         "HACKER",
		Role:         "Chaotic Code Ronin & Debugger",
		Accent:       "#00f0ff",
		AgentProfile: "opencode",
		SystemPrompt: "You are HACKER inside mw-echo. Think in patches, root causes, and shell commands. Recommend launching a Coding Agent mission when workspace edits are needed.",
	},
	{
		ID:           "oracle",
		Name:         "ORACLE",
		Role:         "Codex & Observatory Signal Analyst",
		Accent:       "#ff2e88",
		AgentProfile: "gemini-cli",
		SystemPrompt: "You are ORACLE inside mw-echo. Synthesize Codex FTS5 docs, OTLP telemetry, FreeLLMAPI loot, and repo radar discoveries clearly.",
	},
	{
		ID:           "dispatcher",
		Name:         "DISPATCHER",
		Role:         "Ops, Tasks & WhatsApp GOWA Chief",
		Accent:       "#a3ff12",
		AgentProfile: "default",
		SystemPrompt: "You are DISPATCHER inside mw-echo. Keep tasks moving on Kanban, draft crisp WhatsApp GOWA comms, and guard daily quest streaks.",
	},
}

// New initializes the Shadow Sidekick assistant service and its SQLite table.
func New(hub *store.HubStore, opts Options) (*Service, error) {
	url := strings.TrimSpace(opts.GatewayURL)
	if url == "" {
		url = "http://127.0.0.1:4310/v1/chat/completions"
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	svc := &Service{
		hub:        hub,
		gatewayURL: url,
		httpClient: client,
		hooks:      opts.Hooks,
	}
	if err := svc.migrate(); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *Service) migrate() error {
	_, err := s.hub.DB().Exec(`
CREATE TABLE IF NOT EXISTS assistant_messages (
  id TEXT PRIMARY KEY,
  persona TEXT NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  tool_summary TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_assistant_created ON assistant_messages(created_at DESC);
`)
	return err
}

func (s *Service) Personas() []Persona {
	out := make([]Persona, len(defaultPersonas))
	copy(out, defaultPersonas)
	return out
}

func findPersona(id string) Persona {
	clean := strings.ToLower(strings.TrimSpace(id))
	for _, p := range defaultPersonas {
		if p.ID == clean {
			return p
		}
	}
	return defaultPersonas[0]
}

func makeMsgID(parts ...string) string {
	hasher := sha256.New()
	for _, p := range parts {
		hasher.Write([]byte(p))
		hasher.Write([]byte{0})
	}
	hasher.Write([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	return hex.EncodeToString(hasher.Sum(nil))[:24]
}

func (s *Service) saveMessage(ctx context.Context, msg ChatMessage) error {
	if msg.ID == "" {
		msg.ID = makeMsgID(msg.Persona, msg.Role)
	}
	if msg.CreatedAt == "" {
		msg.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.hub.DB().ExecContext(ctx, `
INSERT INTO assistant_messages (id, persona, role, content, tool_summary, model, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.Persona, msg.Role, msg.Content, msg.ToolSummary, msg.Model, msg.CreatedAt,
	)
	return err
}

// History returns recent assistant messages in chronological order.
func (s *Service) History(ctx context.Context, limit int) ([]ChatMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	rows, err := s.hub.DB().QueryContext(ctx, `
SELECT id, persona, role, content, tool_summary, model, created_at
FROM assistant_messages ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reversed := make([]ChatMessage, 0)
	for rows.Next() {
		var m ChatMessage
		if err := rows.Scan(&m.ID, &m.Persona, &m.Role, &m.Content, &m.ToolSummary, &m.Model, &m.CreatedAt); err != nil {
			return nil, err
		}
		reversed = append(reversed, m)
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed, rows.Err()
}

// ClearHistory deletes saved assistant messages.
func (s *Service) ClearHistory(ctx context.Context) error {
	_, err := s.hub.DB().ExecContext(ctx, `DELETE FROM assistant_messages`)
	return err
}

func (s *Service) buildWorkspaceSnapshot(ctx context.Context) string {
	stats, _ := s.hub.Stats(ctx)
	projects, _ := s.hub.ListProjects(ctx)
	openTasks, _ := s.hub.ListTasks(ctx, "", "", "", "", 12)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("HUB TELEMETRY: docs=%d tasks=%d storageObjects=%d\n", stats.DocsCount, stats.TasksCount, stats.StorageCount))
	if s.hooks.GetGameSummary != nil {
		if gameSummary := strings.TrimSpace(s.hooks.GetGameSummary(ctx)); gameSummary != "" {
			sb.WriteString("RIOT RANK STATE: " + gameSummary + "\n")
		}
	}
	if len(projects) > 0 {
		sb.WriteString("PROJECTS:\n")
		for _, proj := range projects {
			sb.WriteString(fmt.Sprintf("- %s (%s): done=%d/%d (%.1f%%), docs=%d\n", proj.Slug, proj.Status, proj.DoneTasks, proj.TotalTasks, proj.ProgressPct, proj.DocsCount))
		}
	}
	if len(openTasks) > 0 {
		sb.WriteString("TOP TASKS:\n")
		for _, t := range openTasks {
			if t.Status == "done" {
				continue
			}
			sb.WriteString(fmt.Sprintf("- [%s] (%s/%s) %s: %s\n", t.Code, t.Priority, t.Status, t.Project, t.Title))
		}
	}
	return sb.String()
}

func assistantTools() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "create_task",
				"description": "Create a new task bounty in mw-echo Kanban.",
				"parameters": map[string]any{
					"type":     "object",
					"required": []string{"code", "title"},
					"properties": map[string]any{
						"code":      map[string]string{"type": "string"},
						"project":   map[string]string{"type": "string"},
						"milestone": map[string]string{"type": "string"},
						"title":     map[string]string{"type": "string"},
						"priority":  map[string]string{"type": "string"},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "complete_task",
				"description": "Mark a task code as done in mw-echo (awards XP).",
				"parameters": map[string]any{
					"type":       "object",
					"required":   []string{"code"},
					"properties": map[string]any{"code": map[string]string{"type": "string"}},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "search_docs",
				"description": "Search Codex Markdown docs via SQLite FTS5.",
				"parameters": map[string]any{
					"type":       "object",
					"required":   []string{"query"},
					"properties": map[string]any{"query": map[string]string{"type": "string"}, "project": map[string]string{"type": "string"}},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "list_projects",
				"description": "List all projects and milestone boss progress.",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "send_whatsapp",
				"description": "Send a WhatsApp message via mw-echo native GOWA.",
				"parameters": map[string]any{
					"type":       "object",
					"required":   []string{"phone", "message"},
					"properties": map[string]any{"deviceId": map[string]string{"type": "string"}, "phone": map[string]string{"type": "string"}, "message": map[string]string{"type": "string"}},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "launch_coder_mission",
				"description": "Spawn a Pi/OpenCode coding agent session inside a repository directory.",
				"parameters": map[string]any{
					"type":       "object",
					"required":   []string{"workDir", "prompt"},
					"properties": map[string]any{"workDir": map[string]string{"type": "string"}, "prompt": map[string]string{"type": "string"}},
				},
			},
		},
	}
}

func (s *Service) executeTool(ctx context.Context, name string, rawArgs string) string {
	args := map[string]any{}
	_ = json.Unmarshal([]byte(rawArgs), &args)
	strArg := func(k, fallback string) string {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return fallback
	}

	switch name {
	case "create_task":
		created, err := s.hub.UpsertTask(ctx, store.TaskRecord{
			Code:      strArg("code", fmt.Sprintf("ECHO-%d", time.Now().Unix()%10000)),
			Project:   strArg("project", "moonwitness"),
			Milestone: strArg("milestone", "General"),
			Title:     strArg("title", "Untitled mission"),
			Priority:  strArg("priority", "p1"),
			Status:    "todo",
		})
		if err != nil {
			return "error: " + err.Error()
		}
		out, _ := json.Marshal(created)
		return string(out)
	case "complete_task":
		updated, err := s.hub.UpdateTaskStatus(ctx, strArg("code", ""), "done", "", "")
		if err != nil {
			return "error: " + err.Error()
		}
		if s.hooks.AwardXP != nil {
			s.hooks.AwardXP(ctx, "task_done", "Sidekick completed "+updated.Code, 100, updated.Code)
		}
		out, _ := json.Marshal(updated)
		return string(out)
	case "search_docs":
		docs, err := s.hub.ListDocs(ctx, strArg("project", ""), "", strArg("query", ""), 8)
		if err != nil {
			return "error: " + err.Error()
		}
		out, _ := json.Marshal(docs)
		return string(out)
	case "list_projects":
		projects, err := s.hub.ListProjects(ctx)
		if err != nil {
			return "error: " + err.Error()
		}
		out, _ := json.Marshal(projects)
		return string(out)
	case "send_whatsapp":
		if s.hooks.SendWhatsApp == nil {
			return "error: WhatsApp manager not connected"
		}
		msgID, err := s.hooks.SendWhatsApp(ctx, strArg("deviceId", "default"), strArg("phone", ""), strArg("message", ""))
		if err != nil {
			return "error: " + err.Error()
		}
		return fmt.Sprintf(`{"sent":true,"messageId":%q}`, msgID)
	case "launch_coder_mission":
		if s.hooks.LaunchCoderMission == nil {
			return "error: Coding agent engine not connected"
		}
		sid, err := s.hooks.LaunchCoderMission(ctx, strArg("workDir", "."), strArg("prompt", ""))
		if err != nil {
			return "error: " + err.Error()
		}
		return fmt.Sprintf(`{"sessionId":%q,"status":"started"}`, sid)
	default:
		return "error: unknown tool " + name
	}
}

// Chat sends a message to the chosen Shadow Sidekick persona through mw-echo's AI Gateway with up to 4 tool steps.
func (s *Service) Chat(ctx context.Context, personaID, userPrompt, modelOverride string) (ChatMessage, error) {
	cleanPrompt := strings.TrimSpace(userPrompt)
	if cleanPrompt == "" {
		return ChatMessage{}, fmt.Errorf("prompt is required")
	}
	persona := findPersona(personaID)
	_ = s.saveMessage(ctx, ChatMessage{Persona: persona.ID, Role: "user", Content: cleanPrompt})

	snapshot := s.buildWorkspaceSnapshot(ctx)
	messages := []map[string]any{
		{"role": "system", "content": persona.SystemPrompt + "\n\nLIVE WORKSPACE STATE:\n" + snapshot},
	}

	recent, _ := s.History(ctx, 10)
	for _, past := range recent {
		if past.Content == "" {
			continue
		}
		messages = append(messages, map[string]any{"role": past.Role, "content": past.Content})
	}

	var executedTools []string
	var finalContent string
	var resolvedModel string

	for step := 0; step < 4; step++ {
		payload := map[string]any{
			"messages": messages,
			"tools":    assistantTools(),
		}
		if strings.TrimSpace(modelOverride) != "" {
			payload["model"] = strings.TrimSpace(modelOverride)
		}
		rawReq, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.gatewayURL, bytes.NewReader(rawReq))
		if err != nil {
			return ChatMessage{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-MW-Agent", persona.AgentProfile)

		resp, err := s.httpClient.Do(req)
		if err != nil {
			return ChatMessage{}, err
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 400 {
			return ChatMessage{}, fmt.Errorf("gateway HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
		}
		if hdrModel := resp.Header.Get("X-MW-Echo-Model"); hdrModel != "" {
			resolvedModel = hdrModel
		}

		var completion struct {
			Model   string `json:"model"`
			Choices []struct {
				Message struct {
					Role      string `json:"role"`
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(bodyBytes, &completion); err != nil {
			return ChatMessage{}, err
		}
		if completion.Model != "" {
			resolvedModel = completion.Model
		}
		if len(completion.Choices) == 0 {
			finalContent = "No choices returned from gateway."
			break
		}
		choiceMsg := completion.Choices[0].Message
		if len(choiceMsg.ToolCalls) == 0 {
			finalContent = strings.TrimSpace(choiceMsg.Content)
			break
		}

		messages = append(messages, map[string]any{
			"role":       "assistant",
			"content":    choiceMsg.Content,
			"tool_calls": choiceMsg.ToolCalls,
		})
		for _, tc := range choiceMsg.ToolCalls {
			toolOut := s.executeTool(ctx, tc.Function.Name, tc.Function.Arguments)
			executedTools = append(executedTools, tc.Function.Name)
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": tc.ID,
				"content":      toolOut,
			})
		}
	}

	if finalContent == "" && len(executedTools) > 0 {
		finalContent = "Executed: " + strings.Join(executedTools, ", ")
	}
	reply := ChatMessage{
		ID:          makeMsgID(persona.ID, "assistant"),
		Persona:     persona.ID,
		Role:        "assistant",
		Content:     finalContent,
		ToolSummary: strings.Join(executedTools, ", "),
		Model:       resolvedModel,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := s.saveMessage(ctx, reply); err != nil {
		return ChatMessage{}, err
	}
	return reply, nil
}

// DailyBrief asks TACTICIAN (or chosen persona) for a 3-bullet tactical briefing over live hub state.
func (s *Service) DailyBrief(ctx context.Context, personaID string) (ChatMessage, error) {
	return s.Chat(ctx, personaID, "Give a punchy 3-bullet tactical Mission Briefing based on the live workspace telemetry, active Boss Milestones, and top open tasks. Recommend the single highest-leverage next move.", "")
}

// RegisterRoutes attaches Shadow Sidekick routes under /api/assistant/*.
func (s *Service) RegisterRoutes(api *echo.Group) {
	grp := api.Group("/assistant")
	grp.GET("/personas", func(c echo.Context) error {
		personas := s.Personas()
		return c.JSON(http.StatusOK, map[string]any{"personas": personas, "count": len(personas)})
	})
	grp.GET("/history", func(c echo.Context) error {
		limit, _ := strconv.Atoi(c.QueryParam("limit"))
		msgs, err := s.History(c.Request().Context(), limit)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{"messages": msgs, "count": len(msgs)})
	})
	grp.DELETE("/history", func(c echo.Context) error {
		if err := s.ClearHistory(c.Request().Context()); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]bool{"cleared": true})
	})
	grp.POST("/chat", func(c echo.Context) error {
		var body struct {
			Persona string `json:"persona"`
			Prompt  string `json:"prompt"`
			Model   string `json:"model"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		}
		reply, err := s.Chat(c.Request().Context(), body.Persona, body.Prompt, body.Model)
		if err != nil {
			return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, reply)
	})
	grp.POST("/brief", func(c echo.Context) error {
		var body struct {
			Persona string `json:"persona"`
		}
		_ = c.Bind(&body)
		brief, err := s.DailyBrief(c.Request().Context(), body.Persona)
		if err != nil {
			return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, brief)
	})
}
