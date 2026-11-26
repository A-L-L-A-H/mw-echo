package flows_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/flows"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func openTestEngine(t *testing.T, opts flows.Options) (*flows.Engine, *store.HubStore) {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	engine, err := flows.NewEngine(hub, opts)
	if err != nil {
		t.Fatalf("new flows engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine, hub
}

func TestSeededStarterFlowsAndCRUD(t *testing.T) {
	ctx := context.Background()
	engine, _ := openTestEngine(t, flows.Options{})

	listed, err := engine.ListFlows(ctx)
	if err != nil {
		t.Fatalf("list seeded flows: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 seeded starter flows, got %d", len(listed))
	}

	foundRadar := false
	foundBackup := false
	for _, flowRec := range listed {
		if flowRec.Enabled {
			t.Fatalf("expected seeded starter flow %q to be disabled by default", flowRec.Name)
		}
		if flowRec.Name == "Radar Watch -> Sidekick Brief" {
			foundRadar = true
		}
		if flowRec.Name == "Daily 9Drive SQLite Vault Snapshot" {
			foundBackup = true
		}
	}
	if !foundRadar || !foundBackup {
		t.Fatalf("missing expected starter templates: radar=%v backup=%v", foundRadar, foundBackup)
	}

	created, err := engine.UpsertFlow(ctx, flows.FlowRecord{
		Name:        "Manual Smoke Flow",
		Description: "Verifies CRUD lifecycle",
		TriggerKind: flows.TriggerManual,
		ActionTier:  flows.TierA1,
		Steps: []flows.FlowStep{
			{
				ID:     "backup",
				Kind:   flows.StepKindBackup9Drive,
				Config: map[string]string{},
			},
		},
	})
	if err != nil {
		t.Fatalf("create flow: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected generated flow ID")
	}

	enabledFlag := true
	updatedName := "Manual Smoke Flow v2"
	patched, err := engine.PatchFlow(ctx, created.ID, flows.FlowPatchInput{
		Name:    &updatedName,
		Enabled: &enabledFlag,
	})
	if err != nil {
		t.Fatalf("patch flow: %v", err)
	}
	if patched.Name != updatedName || !patched.Enabled {
		t.Fatalf("unexpected patched flow: %+v", patched)
	}

	if err := engine.DeleteFlow(ctx, created.ID); err != nil {
		t.Fatalf("delete flow: %v", err)
	}
	if _, err := engine.GetFlow(ctx, created.ID); err == nil {
		t.Fatal("expected deleted flow lookup to fail")
	}
}

func TestSequentialMultiStepInterpolation(t *testing.T) {
	ctx := context.Background()

	var webhookMu sync.Mutex
	var receivedBody string
	var receivedSecret string
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ := io.ReadAll(r.Body)
		webhookMu.Lock()
		receivedBody = string(rawBody)
		receivedSecret = r.Header.Get("X-Webhook-Secret")
		webhookMu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer webhookSrv.Close()

	var capturedPrompt string
	var capturedTaskTitle string
	engine, _ := openTestEngine(t, flows.Options{
		HTTPClient: webhookSrv.Client(),
		RunAIPrompt: func(ctx context.Context, agentProfile, prompt string) (string, error) {
			capturedPrompt = agentProfile + "::" + prompt
			return "AI-SUMMARY-42", nil
		},
		CreateTask: func(ctx context.Context, project, code, title, priority string) (string, error) {
			capturedTaskTitle = title
			return project + "/" + code + ":" + priority + ":" + title, nil
		},
	})

	flowRec, err := engine.UpsertFlow(ctx, flows.FlowRecord{
		Name:        "Triage Pipeline",
		TriggerKind: flows.TriggerRadarEvent,
		ActionTier:  flows.TierA2,
		Enabled:     true,
		Steps: []flows.FlowStep{
			{
				ID:   "ai",
				Kind: flows.StepKindAIPrompt,
				Config: map[string]string{
					"agentProfile": "sidekick",
					"prompt":       "Analyze event: {{trigger}}",
				},
			},
			{
				ID:   "task",
				Kind: flows.StepKindCreateTask,
				Config: map[string]string{
					"project":  "moonwitness",
					"code":     "FLOW-101",
					"title":    "Followup: {{prev}}",
					"priority": "p1",
				},
			},
			{
				ID:   "hook",
				Kind: flows.StepKindSendWebhook,
				Config: map[string]string{
					"url":    webhookSrv.URL,
					"secret": "top-secret",
					"body":   `{"task":"{{prev}}","trigger":"{{trigger}}"}`,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create pipeline flow: %v", err)
	}

	runRec, err := engine.RunFlowByID(ctx, flowRec.ID, "cable-cut-alert")
	if err != nil {
		t.Fatalf("run flow: %v", err)
	}
	if runRec.Status != flows.RunStatusOK {
		t.Fatalf("expected status ok, got %s", runRec.Status)
	}
	if len(runRec.StepLogs) != 3 {
		t.Fatalf("expected 3 step logs, got %d", len(runRec.StepLogs))
	}
	if capturedPrompt != "sidekick::Analyze event: cable-cut-alert" {
		t.Fatalf("unexpected prompt interpolation: %q", capturedPrompt)
	}
	if capturedTaskTitle != "Followup: AI-SUMMARY-42" {
		t.Fatalf("unexpected task title interpolation: %q", capturedTaskTitle)
	}

	webhookMu.Lock()
	gotBody := receivedBody
	gotSecret := receivedSecret
	webhookMu.Unlock()

	expectedBody := `{"task":"moonwitness/FLOW-101:p1:Followup: AI-SUMMARY-42","trigger":"cable-cut-alert"}`
	if gotBody != expectedBody {
		t.Fatalf("unexpected webhook body: got %q want %q", gotBody, expectedBody)
	}
	if gotSecret != "top-secret" {
		t.Fatalf("unexpected webhook secret: %q", gotSecret)
	}

	runs, err := engine.ListFlowRuns(ctx, flowRec.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != runRec.ID {
		t.Fatalf("unexpected runs list: %+v", runs)
	}
}

func TestA3GovernanceBlockAndApprovalPass(t *testing.T) {
	ctx := context.Background()
	var sentCount atomic.Int32
	engine, _ := openTestEngine(t, flows.Options{
		SendWhatsApp: func(ctx context.Context, deviceID, phone, message string) (string, error) {
			sentCount.Add(1)
			return "msg-" + phone + "-" + message, nil
		},
	})

	flowRec, err := engine.UpsertFlow(ctx, flows.FlowRecord{
		Name:        "Broadcast A3 Flow",
		TriggerKind: flows.TriggerManual,
		ActionTier:  flows.TierA3,
		Enabled:     true,
		Approved:    false,
		Steps: []flows.FlowStep{
			{
				ID:   "wa",
				Kind: flows.StepKindSendWhatsApp,
				Config: map[string]string{
					"deviceId": "dev-1",
					"phone":    "628111222333",
					"message":  "Alert: {{trigger}}",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create A3 flow: %v", err)
	}

	blockedRun, runErr := engine.RunFlowByID(ctx, flowRec.ID, "high-severity")
	if !errors.Is(runErr, flows.ErrApprovalRequired) {
		t.Fatalf("expected ErrApprovalRequired, got %v", runErr)
	}
	if runErr.Error() != "A3 flow requires explicit approval" {
		t.Fatalf("unexpected error string: %q", runErr.Error())
	}
	if blockedRun.Status != flows.RunStatusBlocked {
		t.Fatalf("expected blocked run status, got %s", blockedRun.Status)
	}
	if sentCount.Load() != 0 {
		t.Fatalf("expected SendWhatsApp not to run while unapproved, got %d", sentCount.Load())
	}

	approvedFlag := true
	if _, err := engine.PatchFlow(ctx, flowRec.ID, flows.FlowPatchInput{
		Approved: &approvedFlag,
	}); err != nil {
		t.Fatalf("approve A3 flow: %v", err)
	}

	okRun, okErr := engine.RunFlowByID(ctx, flowRec.ID, "high-severity")
	if okErr != nil {
		t.Fatalf("expected approved A3 flow to succeed, got %v", okErr)
	}
	if okRun.Status != flows.RunStatusOK || sentCount.Load() != 1 {
		t.Fatalf("expected ok run with 1 whatsapp send, got status=%s count=%d", okRun.Status, sentCount.Load())
	}
}

func TestIntervalSchedulerAndCloseCleanup(t *testing.T) {
	ctx := context.Background()
	var ticks atomic.Int32
	engine, _ := openTestEngine(t, flows.Options{
		Backup9Drive: func(ctx context.Context) (string, error) {
			ticks.Add(1)
			return "snapshot-ok", nil
		},
	})

	_, err := engine.UpsertFlow(ctx, flows.FlowRecord{
		Name:          "Fast Interval Snapshot",
		TriggerKind:   flows.TriggerInterval,
		TriggerConfig: map[string]any{"interval": "35ms"},
		ActionTier:    flows.TierA0,
		Enabled:       true,
		Steps: []flows.FlowStep{
			{
				ID:     "snap",
				Kind:   flows.StepKindBackup9Drive,
				Config: map[string]string{},
			},
		},
	})
	if err != nil {
		t.Fatalf("create interval flow: %v", err)
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for ticks.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ticks.Load() < 2 {
		t.Fatalf("expected interval scheduler to run at least twice, got %d", ticks.Load())
	}

	if err := engine.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	countAtClose := ticks.Load()
	time.Sleep(120 * time.Millisecond)
	if ticks.Load() != countAtClose {
		t.Fatalf("expected interval runs to stop after Close: before=%d after=%d", countAtClose, ticks.Load())
	}
}

func TestDispatchTriggerAndHTTPRoutes(t *testing.T) {
	ctx := context.Background()
	var radarRuns atomic.Int32
	engine, _ := openTestEngine(t, flows.Options{
		RunRadar: func(ctx context.Context, instanceID string) (string, error) {
			radarRuns.Add(1)
			return "radar:" + instanceID, nil
		},
		RunAIPrompt: func(ctx context.Context, agentProfile, prompt string) (string, error) {
			return "brief:" + prompt, nil
		},
	})

	// Test DispatchTrigger on matching enabled whatsapp_msg flow.
	_, err := engine.UpsertFlow(ctx, flows.FlowRecord{
		Name:          "WA Triggered Radar",
		TriggerKind:   flows.TriggerWhatsAppMsg,
		TriggerConfig: map[string]any{"contains": "urgent"},
		ActionTier:    flows.TierA1,
		Enabled:       true,
		Steps: []flows.FlowStep{
			{
				ID:     "radar",
				Kind:   flows.StepKindRadarRun,
				Config: map[string]string{"instanceId": "inst-99"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create trigger flow: %v", err)
	}

	noMatchRuns, err := engine.DispatchTrigger(ctx, flows.TriggerWhatsAppMsg, "hello world")
	if err != nil {
		t.Fatalf("dispatch non-matching: %v", err)
	}
	if len(noMatchRuns) != 0 || radarRuns.Load() != 0 {
		t.Fatalf("expected 0 runs for non-matching payload, got %d", len(noMatchRuns))
	}

	matchedRuns, err := engine.DispatchTrigger(ctx, flows.TriggerWhatsAppMsg, "URGENT outage")
	if err != nil {
		t.Fatalf("dispatch matching: %v", err)
	}
	if len(matchedRuns) != 1 || matchedRuns[0].Status != flows.RunStatusOK || radarRuns.Load() != 1 {
		t.Fatalf("expected 1 successful triggered run, got %+v", matchedRuns)
	}

	// Test HTTP routes mounted under /api/flows.
	app := echo.New()
	api := app.Group("/api")
	engine.RegisterRoutes(api)

	// GET /api/flows
	listReq := httptest.NewRequest(http.MethodGet, "/api/flows", nil)
	listRec := httptest.NewRecorder()
	app.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/flows status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Flows []flows.FlowRecord `json:"flows"`
		Count int                `json:"count"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listResp.Count != 3 || len(listResp.Flows) != 3 {
		t.Fatalf("expected 3 flows, got count=%d len=%d", listResp.Count, len(listResp.Flows))
	}

	// POST /api/flows
	createBody := `{
		"name": "HTTP Created A3 Flow",
		"triggerKind": "manual",
		"actionTier": "A3",
		"enabled": true,
		"steps": [{"id":"ai","kind":"ai_prompt","config":{"agentProfile":"sidekick","prompt":"hi {{trigger}}"}}]
	}`
	postReq := httptest.NewRequest(http.MethodPost, "/api/flows", strings.NewReader(createBody))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	app.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/flows status=%d body=%s", postRec.Code, postRec.Body.String())
	}
	var createdFlow flows.FlowRecord
	if err := json.Unmarshal(postRec.Body.Bytes(), &createdFlow); err != nil {
		t.Fatalf("decode created flow: %v", err)
	}

	// POST /api/flows/:id/run while unapproved -> 403
	runReq := httptest.NewRequest(http.MethodPost, "/api/flows/"+createdFlow.ID+"/run", bytes.NewBufferString(`{"triggerPayload":"ops"}`))
	runReq.Header.Set("Content-Type", "application/json")
	runRec := httptest.NewRecorder()
	app.ServeHTTP(runRec, runReq)
	if runRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unapproved A3 run, got %d body=%s", runRec.Code, runRec.Body.String())
	}

	// PATCH /api/flows/:id to approve
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/flows/"+createdFlow.ID, bytes.NewBufferString(`{"approved":true}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	app.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH /api/flows/:id status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}

	// POST /api/flows/:id/run after approval -> 200
	runOKReq := httptest.NewRequest(http.MethodPost, "/api/flows/"+createdFlow.ID+"/run", bytes.NewBufferString(`{"triggerPayload":"ops"}`))
	runOKReq.Header.Set("Content-Type", "application/json")
	runOKRec := httptest.NewRecorder()
	app.ServeHTTP(runOKRec, runOKReq)
	if runOKRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for approved A3 run, got %d body=%s", runOKRec.Code, runOKRec.Body.String())
	}
	var completedRun flows.FlowRun
	if err := json.Unmarshal(runOKRec.Body.Bytes(), &completedRun); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if completedRun.Status != flows.RunStatusOK || completedRun.Output["lastOutput"] != "brief:hi ops" {
		t.Fatalf("unexpected run response: %+v", completedRun)
	}

	// GET /api/flows/:id/runs
	runsReq := httptest.NewRequest(http.MethodGet, "/api/flows/"+createdFlow.ID+"/runs", nil)
	runsRec := httptest.NewRecorder()
	app.ServeHTTP(runsRec, runsReq)
	if runsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/flows/:id/runs status=%d body=%s", runsRec.Code, runsRec.Body.String())
	}
	var runsResp struct {
		Runs  []flows.FlowRun `json:"runs"`
		Count int             `json:"count"`
	}
	if err := json.Unmarshal(runsRec.Body.Bytes(), &runsResp); err != nil {
		t.Fatalf("decode runs list: %v", err)
	}
	if runsResp.Count != 2 {
		t.Fatalf("expected 2 runs (1 blocked + 1 ok), got %d", runsResp.Count)
	}

	// DELETE /api/flows/:id
	delReq := httptest.NewRequest(http.MethodDelete, "/api/flows/"+createdFlow.ID, nil)
	delRec := httptest.NewRecorder()
	app.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/flows/:id status=%d body=%s", delRec.Code, delRec.Body.String())
	}
}
