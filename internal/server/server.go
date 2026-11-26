package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/aigateway"
	"github.com/A-L-L-A-H/mw-echo/internal/assistant"
	"github.com/A-L-L-A-H/mw-echo/internal/blueprints"
	"github.com/A-L-L-A-H/mw-echo/internal/codingagent"
	"github.com/A-L-L-A-H/mw-echo/internal/comms"
	"github.com/A-L-L-A-H/mw-echo/internal/desktop"
	"github.com/A-L-L-A-H/mw-echo/internal/diskdoctor"
	"github.com/A-L-L-A-H/mw-echo/internal/fleet"
	"github.com/A-L-L-A-H/mw-echo/internal/flows"
	"github.com/A-L-L-A-H/mw-echo/internal/gamification"
	"github.com/A-L-L-A-H/mw-echo/internal/importer"
	"github.com/A-L-L-A-H/mw-echo/internal/memvault"
	"github.com/A-L-L-A-H/mw-echo/internal/meshfleet"
	"github.com/A-L-L-A-H/mw-echo/internal/observatory"
	"github.com/A-L-L-A-H/mw-echo/internal/storage"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/A-L-L-A-H/mw-echo/internal/vaultenv"
	"github.com/A-L-L-A-H/mw-echo/internal/whatsapp"
	"github.com/A-L-L-A-H/mw-echo/internal/worldradar"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

//go:embed all:ui/dist
var embeddedUIDist embed.FS

type Config struct {
	Addr            string
	AuthToken       string
	WAUser          string
	WAPass          string
	WAWebhookURL    string
	WAWebhookSecret string
	RIDJWKSURL      string
	RIDIssuer       string
	RIDAudience     string
	RIDHTTPClient   *http.Client
	EchoPublicURL   string
	RIDURL          string
	MyLoveURL       string
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type memoryResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *memoryResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *memoryResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *memoryResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func newInternalGatewayClient(app *echo.Echo) *http.Client {
	return &http.Client{
		Timeout: 90 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			rec := &memoryResponseWriter{}
			app.ServeHTTP(rec, req)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			raw := rec.body.Bytes()
			return &http.Response{
				StatusCode:    status,
				Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
				Header:        rec.header.Clone(),
				Body:          io.NopCloser(bytes.NewReader(raw)),
				ContentLength: int64(len(raw)),
				Request:       req,
			}, nil
		}),
	}
}

func taskPriorityXP(priority string) int {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "p0":
		return 250
	case "p1":
		return 100
	case "p3":
		return 25
	default:
		return 50
	}
}

func isReservedBackendPath(reqPath string) bool {
	reservedPrefixes := []string{
		"/api", "/v1", "/health",
		"/app", "/devices", "/chats", "/chat", "/statics", "/send", "/message",
	}
	for _, prefix := range reservedPrefixes {
		if reqPath == prefix || strings.HasPrefix(reqPath, prefix+"/") {
			return true
		}
	}
	return false
}

func NewEcho(hub *store.HubStore, cfg Config) *echo.Echo {
	app := echo.New()
	app.HideBanner = true
	app.HidePort = true
	app.Use(middleware.Recover())
	app.Use(middleware.Secure())

	internalGatewayURL := "http://mw-echo.local/v1/chat/completions"
	internalClient := newInternalGatewayClient(app)

	pool, err := storage.NewPoolManager(hub)
	if err != nil {
		panic("init storage pool: " + err.Error())
	}
	aiGateway, err := aigateway.NewGateway(hub)
	if err != nil {
		panic("init ai gateway: " + err.Error())
	}
	obs, err := observatory.New(hub)
	if err != nil {
		panic("init observatory: " + err.Error())
	}
	obs.SetErrorTriageMonitor(observatory.NewErrorTriageMonitor(observatory.ErrorTriageOptions{
		OnSpike: func(ctx context.Context, serviceName, sampleError string, count int) error {
			code := fmt.Sprintf("TRIAGE-%s-%d", strings.ToUpper(strings.ReplaceAll(serviceName, " ", "-")), time.Now().UTC().Unix())
			title := fmt.Sprintf("[AUTO-TRIAGE] %s error spike (%d errors): %s", serviceName, count, sampleError)
			savedTask, taskErr := hub.UpsertTask(ctx, store.TaskRecord{
				Project:  "mw-echo",
				Code:     code,
				Title:    title,
				Priority: "P1",
				Status:   "todo",
			})
			if taskErr != nil {
				log.Printf("[observatory/triage] failed to create P1 bounty task for %s: %v", serviceName, taskErr)
				return taskErr
			}
			log.Printf("[observatory/triage] created P1 bounty task %s for %s (%d error spans): %s", savedTask.Code, serviceName, count, sampleError)
			return nil
		},
	}))

	coderEngine, err := codingagent.NewEngine(hub, codingagent.Options{
		DefaultGatewayURL:   internalGatewayURL,
		DefaultAgentProfile: codingagent.DefaultAgentProfile,
		HTTPClient:          internalClient,
	})
	if err != nil {
		panic("init coding agent: " + err.Error())
	}

	var flowEngine *flows.Engine

	waMgr, err := whatsapp.NewManager(hub, whatsapp.Options{
		BasicUser:            cfg.WAUser,
		BasicPass:            cfg.WAPass,
		DefaultWebhookURL:    cfg.WAWebhookURL,
		DefaultWebhookSecret: cfg.WAWebhookSecret,
		AutoReply: func(ctx context.Context, agentID, sender, text string) (string, error) {
			if flowEngine != nil {
				triggerPayload, _ := json.Marshal(map[string]string{
					"agentId": agentID,
					"sender":  sender,
					"text":    text,
				})
				_, _ = flowEngine.DispatchTrigger(ctx, flows.TriggerWhatsAppMsg, string(triggerPayload))
			}
			return callInternalGatewayChat(
				ctx,
				internalClient,
				internalGatewayURL,
				agentID,
				"You are a helpful WhatsApp auto-reply assistant for mw-echo. Answer briefly and clearly.",
				fmt.Sprintf("From %s: %s", sender, text),
			)
		},
		ArchiveMedia: func(ctx context.Context, project, logicalPath, filename, mimeType string, content []byte) (string, error) {
			savedObj, upErr := pool.Upload(ctx, project, logicalPath, filepath.Base(filename), mimeType, "", bytes.NewReader(content))
			if upErr != nil {
				return "", upErr
			}
			return savedObj.ID, nil
		},
	})
	if err != nil {
		panic("init whatsapp manager: " + err.Error())
	}
	hub.RegisterCloser(waMgr.Close)

	gameEngine, err := gamification.NewEngine(hub, gamification.TelemetryHook{
		GetRTKTokensSaved: func(ctx context.Context) int64 {
			st, stErr := aiGateway.Stats(ctx)
			if stErr != nil {
				return 0
			}
			return st.TotalRTKTokensSaved + st.CacheTokensSaved
		},
		GetWAConnected: func(ctx context.Context) int {
			st, stErr := waMgr.Stats(ctx)
			if stErr != nil {
				return 0
			}
			return st.ConnectedCount
		},
		GetCoderTurns: func(ctx context.Context) int {
			sessions, sessErr := coderEngine.ListSessions(ctx, 50)
			if sessErr != nil {
				return 0
			}
			return len(sessions)
		},
	})
	if err != nil {
		panic("init gamification: " + err.Error())
	}

	sidekick, err := assistant.New(hub, assistant.Options{
		GatewayURL: internalGatewayURL,
		HTTPClient: internalClient,
		Hooks: assistant.Hooks{
			SendWhatsApp: func(ctx context.Context, deviceID, phone, message string) (string, error) {
				sent, sendErr := waMgr.SendText(ctx, deviceID, phone, message, "", nil)
				if sendErr != nil {
					return "", sendErr
				}
				_, _ = gameEngine.Award(ctx, "wa_send", "Sent WhatsApp via Sidekick", 15, sent.MessageID, true)
				return sent.MessageID, nil
			},
			LaunchCoderMission: func(ctx context.Context, workDir, prompt string) (string, error) {
				sess, createErr := coderEngine.CreateSession(ctx, codingagent.SessionCreateInput{
					WorkDir: workDir,
					Mode:    codingagent.ModeAct,
				})
				if createErr != nil {
					return "", createErr
				}
				if startErr := coderEngine.StartPromptAsync(sess.ID, prompt); startErr != nil {
					return "", startErr
				}
				_, _ = gameEngine.Award(ctx, "coder_mission", "Launched Coder Mission", 60, sess.ID, true)
				return sess.ID, nil
			},
			GetGameSummary: func(ctx context.Context) string {
				prof, profErr := gameEngine.Profile(ctx)
				if profErr != nil {
					return ""
				}
				return fmt.Sprintf("Level %d (%s) | XP=%d/%d | Streak=%dd | Combo=%.1fx | ActiveBosses=%d",
					prof.Level, prof.RankTitle, prof.TotalXP, prof.NextLevelXP, prof.StreakDays, prof.ComboMult, len(prof.BossBattles))
			},
			AwardXP: func(ctx context.Context, eventType, label string, baseXP int, refID string) {
				_, _ = gameEngine.Award(ctx, eventType, label, baseXP, refID, true)
			},
		},
	})
	if err != nil {
		panic("init assistant: " + err.Error())
	}

	worldRadar, err := worldradar.NewEngine(hub, worldradar.Options{
		GatewayURL: internalGatewayURL,
		HTTPClient: internalClient,
	})
	if err != nil {
		panic("init world radar: " + err.Error())
	}
	worldRadar.StartScheduler(5 * time.Minute)

	commsMgr, err := comms.NewManager(hub, comms.Options{})
	if err != nil {
		panic("init comms manager: " + err.Error())
	}

	vaultEnv, err := vaultenv.NewVault(hub, vaultenv.Options{
		UploadBackup: func(ctx context.Context, project, logicalPath, filename, mimeType string, content []byte) (string, error) {
			savedObj, upErr := pool.Upload(ctx, project, logicalPath, filepath.Base(filename), mimeType, "", bytes.NewReader(content))
			if upErr != nil {
				return "", upErr
			}
			return savedObj.ID, nil
		},
	})
	if err != nil {
		panic("init vaultenv: " + err.Error())
	}

	desktopMgr, err := desktop.NewManager(hub, desktop.Options{
		KnownSecrets: []string{cfg.AuthToken, cfg.WAPass, cfg.WAWebhookSecret},
	})
	if err != nil {
		panic("init desktop manager: " + err.Error())
	}

	meshMgr, err := meshfleet.NewManager(hub, meshfleet.Options{})
	if err != nil {
		panic("init mesh fleet manager: " + err.Error())
	}

	diskMgr, err := diskdoctor.NewManager(hub, diskdoctor.Options{
		AIAnalyze: func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
			return callInternalGatewayChat(ctx, internalClient, internalGatewayURL, "sidekick", systemPrompt, userPrompt)
		},
	})
	if err != nil {
		panic("init disk doctor manager: " + err.Error())
	}

	flowEngine, err = flows.NewEngine(hub, flows.Options{
		HTTPClient: internalClient,
		RunRadar: func(ctx context.Context, instanceID string) (string, error) {
			resolvedID := strings.TrimSpace(instanceID)
			if resolvedID == "" || resolvedID == "default" {
				instances, listErr := worldRadar.ListInstances(ctx)
				if listErr != nil {
					return "", listErr
				}
				if len(instances) == 0 {
					return "no radar instances installed", nil
				}
				resolvedID = instances[0].ID
			}
			summary, runErr := worldRadar.RunInstance(ctx, resolvedID)
			if runErr != nil {
				return "", runErr
			}
			encoded, _ := json.Marshal(summary)
			return string(encoded), nil
		},
		RunAIPrompt: func(ctx context.Context, agentProfile, prompt string) (string, error) {
			return callInternalGatewayChat(
				ctx,
				internalClient,
				internalGatewayURL,
				agentProfile,
				"You are an automation step inside mw-echo Flows. Respond concisely with actionable output.",
				prompt,
			)
		},
		CreateTask: func(ctx context.Context, project, code, title, priority string) (string, error) {
			saved, taskErr := hub.UpsertTask(ctx, store.TaskRecord{
				Project:  project,
				Code:     code,
				Title:    title,
				Priority: priority,
				Status:   "todo",
			})
			if taskErr != nil {
				return "", taskErr
			}
			return saved.Code, nil
		},
		SendWhatsApp: func(ctx context.Context, deviceID, phone, message string) (string, error) {
			sent, sendErr := waMgr.SendText(ctx, deviceID, phone, message, "", nil)
			if sendErr != nil {
				return "", sendErr
			}
			return sent.MessageID, nil
		},
		SendComms: func(ctx context.Context, kind, channelID, targetRef, title, body, priorityTier string) (string, error) {
			delivery, sendErr := commsMgr.Send(ctx, comms.SendInput{
				ChannelID:    channelID,
				Kind:         kind,
				TargetRef:    targetRef,
				Title:        title,
				Body:         body,
				PriorityTier: priorityTier,
			})
			if sendErr != nil {
				return "", sendErr
			}
			return delivery.ID, nil
		},
		Backup9Drive: func(ctx context.Context) (string, error) {
			backups, backupErr := pool.BackupDatabases(ctx)
			if backupErr != nil {
				return "", backupErr
			}
			encoded, _ := json.Marshal(backups)
			return string(encoded), nil
		},
	})
	if err != nil {
		panic("init flows engine: " + err.Error())
	}
	hub.RegisterCloser(flowEngine.Close)

	blueprintCat, err := blueprints.NewCatalog(hub)
	if err != nil {
		panic("init blueprints catalog: " + err.Error())
	}

	memMgr, err := memvault.NewManager(hub)
	if err != nil {
		panic("init memvault manager: " + err.Error())
	}

	fleetMgr, err := fleet.NewManager(hub, fleet.Options{})
	if err != nil {
		panic("init fleet manager: " + err.Error())
	}

	ridJWKSURL := firstNonEmpty(cfg.RIDJWKSURL, os.Getenv("MW_RID_JWKS_URL"))
	ridIssuer := firstNonEmpty(cfg.RIDIssuer, os.Getenv("MW_RID_ISSUER"))
	ridAudience := firstNonEmpty(cfg.RIDAudience, os.Getenv("MW_RID_AUDIENCE"))

	var ridVerifier *RIDVerifier
	if ridJWKSURL != "" {
		verifier, verifierErr := NewRIDVerifier(RIDVerifierConfig{
			JWKSURL:    ridJWKSURL,
			Issuer:     ridIssuer,
			Audience:   ridAudience,
			HTTPClient: cfg.RIDHTTPClient,
		})
		if verifierErr != nil {
			panic("init mw-rid verifier: " + verifierErr.Error())
		}
		ridVerifier = verifier
	}

	staticAuthToken := strings.TrimSpace(cfg.AuthToken)
	authEnabled := staticAuthToken != "" || ridVerifier != nil

	authGuard := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !authEnabled {
				return next(c)
			}
			header := strings.TrimSpace(c.Request().Header.Get("Authorization"))
			provided, hasBearer := strings.CutPrefix(header, "Bearer ")
			provided = strings.TrimSpace(provided)
			if !hasBearer || provided == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			}
			if staticAuthToken != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(staticAuthToken)) == 1 {
				return next(c)
			}
			if ridVerifier != nil {
				claims, verifyErr := ridVerifier.VerifyAccessToken(c.Request().Context(), provided)
				if verifyErr == nil {
					c.Set("rid_claims", claims)
					return next(c)
				}
			}
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
	}

	distFS, err := fs.Sub(embeddedUIDist, "ui/dist")
	if err != nil {
		panic("init embedded ui/dist: " + err.Error())
	}
	indexHTML, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		panic("read embedded ui/dist/index.html: " + err.Error())
	}
	assetHandler := echo.WrapHandler(http.FileServer(http.FS(distFS)))

	app.GET("/", func(c echo.Context) error {
		return c.Blob(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	})
	app.GET("/assets/*", assetHandler)
	app.GET("/*", func(c echo.Context) error {
		reqPath := c.Request().URL.Path
		if isReservedBackendPath(reqPath) {
			return echo.ErrNotFound
		}
		trimmed := strings.TrimPrefix(reqPath, "/")
		if trimmed != "" {
			if info, statErr := fs.Stat(distFS, trimmed); statErr == nil && !info.IsDir() {
				return assetHandler(c)
			}
		}
		return c.Blob(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	})

	app.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "mw-echo",
			"dataDir": hub.DataDir(),
		})
	})

	// Drop-in FreeLLMAPI compose health probe compatibility
	app.GET("/api/ping", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"ok":      true,
			"service": "mw-echo",
		})
	})

	// Mobile pairing discovery for mw-pocket. Intentionally public so mobile clients can
	// discover endpoints and capabilities before sign-in; never exposes raw secret tokens.
	app.GET("/api/pocket/pair-config", func(c echo.Context) error {
		echoURL := firstNonEmpty(cfg.EchoPublicURL, os.Getenv("MW_ECHO_PUBLIC_URL"))
		if echoURL == "" {
			scheme := "http"
			if c.Request().TLS != nil {
				scheme = "https"
			}
			host := strings.TrimSpace(c.Request().Host)
			if host == "" {
				host = strings.TrimSpace(cfg.Addr)
			}
			if host == "" {
				host = "127.0.0.1:4310"
			}
			echoURL = scheme + "://" + host
		}
		ridURL := firstNonEmpty(cfg.RIDURL, os.Getenv("MW_RID_URL"), "http://127.0.0.1:4320")
		myloveURL := firstNonEmpty(cfg.MyLoveURL, os.Getenv("MW_MYLOVE_URL"), "http://127.0.0.1:4330")

		pairQuery := url.Values{}
		pairQuery.Set("echo", echoURL)
		pairQuery.Set("rid", ridURL)
		pairQuery.Set("mylove", myloveURL)

		return c.JSON(http.StatusOK, map[string]any{
			"pairUri":      "mwpocket://pair?" + pairQuery.Encode(),
			"echoUrl":      echoURL,
			"ridUrl":       ridURL,
			"myloveUrl":    myloveURL,
			"authRequired": authEnabled,
			"capabilities": []string{
				"tasks",
				"docs",
				"storage",
				"radar",
				"comms",
				"coder",
				"fleet",
				"whatsapp",
				"ai-gateway",
			},
			"version": "2.0.0",
		})
	})

	api := app.Group("/api", authGuard)

	api.GET("/stats", func(c echo.Context) error {
		stats, err := hub.Stats(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, stats)
	})

	// Docs + FTS5 search
	api.GET("/docs", func(c echo.Context) error {
		limit, _ := strconv.Atoi(c.QueryParam("limit"))
		docs, err := hub.ListDocs(c.Request().Context(), c.QueryParam("project"), c.QueryParam("category"), c.QueryParam("q"), limit)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{"docs": docs, "count": len(docs)})
	})

	api.GET("/docs/:id", func(c echo.Context) error {
		doc, err := hub.GetDoc(c.Request().Context(), c.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "doc not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, doc)
	})

	api.PUT("/docs", func(c echo.Context) error {
		var payload store.DocRecord
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid doc JSON"})
		}
		saved, err := hub.UpsertDoc(c.Request().Context(), payload)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		_, _ = gameEngine.Award(c.Request().Context(), "doc_save", "Updated Codex "+saved.RelPath, 20, saved.ID, true)
		return c.JSON(http.StatusOK, saved)
	})

	api.DELETE("/docs/:id", func(c echo.Context) error {
		err := hub.DeleteDoc(c.Request().Context(), c.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "doc not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
	})

	// Tasks (TaskOS v2 compatible)
	api.GET("/tasks", func(c echo.Context) error {
		limit, _ := strconv.Atoi(c.QueryParam("limit"))
		tasks, err := hub.ListTasks(c.Request().Context(), c.QueryParam("project"), c.QueryParam("status"), c.QueryParam("priority"), c.QueryParam("q"), limit)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{"tasks": tasks, "count": len(tasks)})
	})

	api.GET("/tasks/:code", func(c echo.Context) error {
		task, err := hub.GetTask(c.Request().Context(), c.Param("code"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "task not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, task)
	})

	api.POST("/tasks", func(c echo.Context) error {
		var payload store.TaskRecord
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid task JSON"})
		}
		saved, err := hub.UpsertTask(c.Request().Context(), payload)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		if strings.EqualFold(saved.Status, "done") {
			_, _ = gameEngine.Award(c.Request().Context(), "task_done", "Cleared "+saved.Code, taskPriorityXP(saved.Priority), saved.Code, true)
		} else {
			_, _ = gameEngine.Award(c.Request().Context(), "task_create", "Queued "+saved.Code, 15, saved.Code, true)
		}
		return c.JSON(http.StatusCreated, saved)
	})

	api.PATCH("/tasks/:code", func(c echo.Context) error {
		var patch struct {
			Status   string `json:"status"`
			Priority string `json:"priority"`
			Title    string `json:"title"`
		}
		if err := c.Bind(&patch); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid patch JSON"})
		}
		prev, _ := hub.GetTask(c.Request().Context(), c.Param("code"))
		updated, err := hub.UpdateTaskStatus(c.Request().Context(), c.Param("code"), patch.Status, patch.Priority, patch.Title)
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "task not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		if !strings.EqualFold(prev.Status, "done") && strings.EqualFold(updated.Status, "done") {
			_, _ = gameEngine.Award(c.Request().Context(), "task_done", "Cleared "+updated.Code, taskPriorityXP(updated.Priority), updated.Code, true)
		}
		return c.JSON(http.StatusOK, updated)
	})

	api.DELETE("/tasks/:code", func(c echo.Context) error {
		err := hub.DeleteTask(c.Request().Context(), c.Param("code"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "task not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
	})

	api.GET("/export/tasks.json", func(c echo.Context) error {
		exported, err := hub.ExportTaskosV2(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, exported)
	})

	// Project Management
	api.GET("/projects", func(c echo.Context) error {
		projects, err := hub.ListProjects(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{"projects": projects, "count": len(projects)})
	})

	api.GET("/projects/:slug", func(c echo.Context) error {
		project, err := hub.GetProject(c.Request().Context(), c.Param("slug"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "project not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, project)
	})

	upsertProjectHandler := func(c echo.Context) error {
		var payload store.ProjectRecord
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid project JSON"})
		}
		if slugParam := strings.TrimSpace(c.Param("slug")); slugParam != "" {
			payload.Slug = slugParam
		}
		saved, err := hub.UpsertProject(c.Request().Context(), payload)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		summary, err := hub.GetProject(c.Request().Context(), saved.Slug)
		if err != nil {
			return c.JSON(http.StatusOK, saved)
		}
		return c.JSON(http.StatusOK, summary)
	}
	api.POST("/projects", upsertProjectHandler)
	api.PUT("/projects/:slug", upsertProjectHandler)

	api.DELETE("/projects/:slug", func(c echo.Context) error {
		err := hub.DeleteProject(c.Request().Context(), c.Param("slug"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "project not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
	})

	// 9Drive Multi-Account Storage Pool
	app.GET("/api/providers/gdrive/oauth/callback", func(c echo.Context) error {
		state := c.QueryParam("state")
		code := c.QueryParam("code")
		if state == "" || code == "" {
			return c.HTML(http.StatusBadRequest, "<h3>Missing OAuth state or code.</h3>")
		}
		rec, err := pool.CompleteGoogleOAuth(c.Request().Context(), state, code)
		if err != nil {
			return c.HTML(http.StatusBadRequest, "<h3>Google Drive OAuth failed: "+err.Error()+"</h3>")
		}
		return c.HTML(http.StatusOK, "<h3>Connected Google Drive: "+rec.Name+" ("+rec.AccountRef+")</h3><script>setTimeout(()=>location.href='/',1500)</script>")
	})

	api.GET("/providers", func(c echo.Context) error {
		summary, err := pool.PoolSummary(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, summary)
	})

	api.POST("/providers", func(c echo.Context) error {
		var input storage.ProviderCreateInput
		if err := c.Bind(&input); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid provider JSON"})
		}
		rec, err := pool.AddProvider(c.Request().Context(), input)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusCreated, rec)
	})

	api.DELETE("/providers/:id", func(c echo.Context) error {
		err := pool.DeleteProvider(c.Request().Context(), c.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "provider not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
	})

	api.POST("/providers/:id/sync-quota", func(c echo.Context) error {
		rec, err := pool.SyncQuota(c.Request().Context(), c.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "provider not found"})
		}
		if err != nil {
			return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, rec)
	})

	api.PUT("/providers/policy", func(c echo.Context) error {
		var body struct {
			RoutingPolicy string `json:"routingPolicy"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		}
		if err := pool.SetRoutingPolicy(c.Request().Context(), body.RoutingPolicy); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]string{"routingPolicy": pool.GetRoutingPolicy(c.Request().Context())})
	})

	api.POST("/providers/gdrive/oauth/start", func(c echo.Context) error {
		var body struct {
			Name         string `json:"name"`
			ClientID     string `json:"clientId"`
			ClientSecret string `json:"clientSecret"`
			RedirectURI  string `json:"redirectUri"`
			Priority     int    `json:"priority"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		}
		redirectURI := strings.TrimSpace(body.RedirectURI)
		if redirectURI == "" {
			scheme := "http"
			if c.Request().TLS != nil {
				scheme = "https"
			}
			redirectURI = scheme + "://" + c.Request().Host + "/api/providers/gdrive/oauth/callback"
		}
		authURL, state, err := pool.StartGoogleOAuth(body.Name, body.ClientID, body.ClientSecret, redirectURI, body.Priority)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]string{"authUrl": authURL, "state": state, "redirectUri": redirectURI})
	})

	// Unified storage routes
	api.GET("/storage", func(c echo.Context) error {
		limit, _ := strconv.Atoi(c.QueryParam("limit"))
		objects, err := pool.ListObjects(c.Request().Context(), c.QueryParam("project"), c.QueryParam("q"), limit)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]any{"objects": objects, "count": len(objects)})
	})

	api.POST("/storage/upload", func(c echo.Context) error {
		fileHeader, err := c.FormFile("file")
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "multipart field 'file' is required"})
		}
		src, err := fileHeader.Open()
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		defer src.Close()

		project := c.FormValue("project")
		logicalPath := c.FormValue("logicalPath")
		providerID := c.FormValue("providerId")
		saved, err := pool.Upload(c.Request().Context(), project, logicalPath, fileHeader.Filename, fileHeader.Header.Get("Content-Type"), providerID, src)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		_, _ = gameEngine.Award(c.Request().Context(), "vault_upload", "Vault stored "+saved.Filename, 30, saved.ID, true)
		return c.JSON(http.StatusCreated, saved)
	})

	api.POST("/storage/backup", func(c echo.Context) error {
		backups, err := pool.BackupDatabases(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		refID := "backup"
		if len(backups) > 0 {
			refID = backups[0].ID
		}
		_, _ = gameEngine.Award(c.Request().Context(), "vault_backup", "Backed up DBs to 9Drive", 35, refID, true)
		return c.JSON(http.StatusOK, map[string]any{
			"backups":  backups,
			"backedUp": backups,
			"count":    len(backups),
		})
	})

	api.POST("/storage/restore/verify", func(c echo.Context) error {
		var req struct {
			ObjectID  string `json:"objectId"`
			ObjectKey string `json:"objectKey"`
		}
		if c.Request().Body != nil && c.Request().ContentLength != 0 {
			if err := c.Bind(&req); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid restore verify JSON"})
			}
		}
		targetID := firstNonEmpty(req.ObjectID, req.ObjectKey)
		report, err := pool.VerifyLatestBackupSnapshot(c.Request().Context(), targetID)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, report)
	})

	api.GET("/storage/:id/download", func(c echo.Context) error {
		obj, stream, err := pool.OpenDownload(c.Request().Context(), c.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "object not found"})
		}
		if err != nil {
			return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
		}
		defer stream.Close()
		c.Response().Header().Set("ETag", `"`+obj.SHA256+`"`)
		c.Response().Header().Set("X-MW-Echo-Provider", obj.ProviderKind+":"+obj.ProviderName)
		c.Response().Header().Set("Content-Type", obj.MimeType)
		c.Response().WriteHeader(http.StatusOK)
		_, _ = io.Copy(c.Response().Writer, stream)
		return nil
	})

	api.DELETE("/storage/:id", func(c echo.Context) error {
		err := pool.DeleteObject(c.Request().Context(), c.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "object not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
	})

	// Local repository importer
	api.POST("/import/repo", func(c echo.Context) error {
		var req struct {
			RepoDir       string `json:"repoDir"`
			Project       string `json:"project"`
			IncludeAssets bool   `json:"includeAssets"`
		}
		if err := c.Bind(&req); err != nil || strings.TrimSpace(req.RepoDir) == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "repoDir is required"})
		}
		project := strings.TrimSpace(req.Project)
		if project == "" {
			project = "moonwitness"
		}
		summary, err := importer.ImportRepo(c.Request().Context(), hub, project, req.RepoDir, req.IncludeAssets)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		_, _ = gameEngine.Award(c.Request().Context(), "repo_import", "Imported repo "+project, 75, project, true)
		return c.JSON(http.StatusOK, summary)
	})

	// Unified MCP JSON-RPC 2.0 endpoint
	api.POST("/mcp", func(c echo.Context) error {
		return handleMCP(c, hub, pool, waMgr, worldRadar, blueprintCat, flowEngine, commsMgr, vaultEnv, desktopMgr, memMgr, fleetMgr)
	})

	// Trigger matching radar_event flows whenever a World Radar instance run completes via HTTP.
	api.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			if err == nil &&
				c.Request().Method == http.MethodPost &&
				c.Path() == "/api/radar/instances/:id/run" &&
				c.Response().Status < 400 {
				payload, _ := json.Marshal(map[string]string{"instanceId": c.Param("id")})
				_, _ = flowEngine.DispatchTrigger(c.Request().Context(), flows.TriggerRadarEvent, string(payload))
			}
			return err
		}
	})

	// Unified AI Gateway (Bifrost + 9Router RTK + Magpie + FreeLLMAPI)
	app.Pre(aigateway.RocksoulProxyMiddleware(os.Getenv))
	aiGateway.RegisterRoutes(app, api)

	// Observatory (OTLP ingest + Host protocol auto-discovery + Local Git Repo Radar)
	obs.RegisterRoutes(app, api)

	// Native Go WhatsApp Multi-Device (GOWA drop-in REST + React Inbox)
	waMgr.RegisterRoutes(app, api)

	// Built-in Coding Agent (Pi / OpenCode / Codex loop routed through mw-echo AI Gateway)
	coderEngine.RegisterRoutes(api)

	// Gamification Engine (Rank / XP / Combos / Boss Milestones / Quests / Badges)
	gameEngine.RegisterRoutes(api)

	// Shadow Sidekick AI Assistant (Personas + Daily Brief + Workspace Tools)
	sidekick.RegisterRoutes(api)

	// R!OTS World Radar Network (12 WORLD intelligence engine)
	worldRadar.RegisterRoutes(api)

	// Automation Flows Engine (13 FLOWS)
	flowEngine.RegisterRoutes(api)

	// Self-Hosted PaaS Blueprint Armory (14 PACKS)
	blueprintCat.RegisterRoutes(api)

	// Omnichannel Comms Hub (Telegram + Discord)
	commsMgr.RegisterRoutes(api)

	// Encrypted .env & Secret Sync Vault (15 SECRETS)
	vaultEnv.RegisterRoutes(api)

	// Desktop Host & Diagnostics (16 DESKTOP)
	desktopMgr.RegisterRoutes(api)

	// Private Mesh Fleet & Git Worktrees
	meshMgr.RegisterRoutes(api)

	// Intelligent Disk & Directory Doctor (17 DISK)
	diskMgr.RegisterRoutes(api)

	// Local-first Memory Vault & Hard Privacy Firewall
	memMgr.RegisterRoutes(api)

	// Sovereign Fleet & ARSY Constitutional Governor (19 FLEET)
	fleetMgr.RegisterRoutes(api)

	return app
}

func callInternalGatewayChat(ctx context.Context, client *http.Client, gatewayURL, agentID, systemPrompt, userPrompt string) (string, error) {
	reqPayload := map[string]any{
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}
	encoded, _ := json.Marshal(reqPayload)
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL, bytes.NewReader(encoded))
	if reqErr != nil {
		return "", reqErr
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(agentID) != "" {
		req.Header.Set("X-MW-Agent", strings.TrimSpace(agentID))
	}
	resp, doErr := client.Do(req)
	if doErr != nil {
		return "", doErr
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("gateway chat HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if unmarshalErr := json.Unmarshal(bodyBytes, &parsed); unmarshalErr != nil {
		return "", unmarshalErr
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("empty gateway choice")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func firstNonEmpty(values ...string) string {
	for _, candidate := range values {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func handleMCP(
	c echo.Context,
	hub *store.HubStore,
	pool *storage.PoolManager,
	waMgr *whatsapp.Manager,
	worldRadar *worldradar.Engine,
	blueprintCat *blueprints.Catalog,
	flowEngine *flows.Engine,
	commsMgr *comms.Manager,
	vaultEnv *vaultenv.Vault,
	desktopMgr *desktop.Manager,
	memMgr *memvault.Manager,
	fleetMgr *fleet.Manager,
) error {
	var rpc mcpRequest
	if err := c.Bind(&rpc); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "invalid JSON"}})
	}
	reply := func(result any) error {
		return c.JSON(http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
	}
	fail := func(code int, msg string) error {
		return c.JSON(http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "error": map[string]any{"code": code, "message": msg}})
	}

	switch rpc.Method {
	case "initialize":
		return reply(map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]string{"name": "mw-echo", "version": "2.0.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		})
	case "tools/list":
		return reply(map[string]any{
			"tools": []map[string]any{
				{"name": "echo_search_docs", "description": "Full-text search Markdown docs stored in mw-echo.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]string{"type": "string"}, "project": map[string]string{"type": "string"}, "limit": map[string]string{"type": "integer"}}}},
				{"name": "echo_get_doc", "description": "Fetch full Markdown content of a doc by id or relPath.", "inputSchema": map[string]any{"type": "object", "required": []string{"id"}, "properties": map[string]any{"id": map[string]string{"type": "string"}}}},
				{"name": "echo_list_tasks", "description": "Query canonical tasks by project, status, priority, or keyword.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"project": map[string]string{"type": "string"}, "status": map[string]string{"type": "string"}, "query": map[string]string{"type": "string"}, "limit": map[string]string{"type": "integer"}}}},
				{"name": "echo_update_task", "description": "Update a task's status, priority, or title in mw-echo.", "inputSchema": map[string]any{"type": "object", "required": []string{"code"}, "properties": map[string]any{"code": map[string]string{"type": "string"}, "status": map[string]string{"type": "string"}, "priority": map[string]string{"type": "string"}, "title": map[string]string{"type": "string"}}}},
				{"name": "echo_list_storage", "description": "List stored binary assets (images, PDFs, media) in mw-echo.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"project": map[string]string{"type": "string"}, "query": map[string]string{"type": "string"}}}},
				{"name": "echo_list_projects", "description": "List projects with task counts, progress percentages, and milestone rollups.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
				{"name": "echo_upsert_project", "description": "Create or update a project in mw-echo.", "inputSchema": map[string]any{"type": "object", "required": []string{"slug"}, "properties": map[string]any{"slug": map[string]string{"type": "string"}, "name": map[string]string{"type": "string"}, "description": map[string]string{"type": "string"}, "status": map[string]string{"type": "string"}}}},
				{"name": "echo_wa_devices", "description": "List connected WhatsApp GOWA multi-device instances.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
				{"name": "echo_wa_send_message", "description": "Send a WhatsApp message via native Go GOWA.", "inputSchema": map[string]any{"type": "object", "required": []string{"phone", "message"}, "properties": map[string]any{"deviceId": map[string]string{"type": "string"}, "phone": map[string]string{"type": "string"}, "message": map[string]string{"type": "string"}}}},
				{"name": "echo_wa_list_chats", "description": "List recent WhatsApp conversations stored in mw-echo.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"deviceId": map[string]string{"type": "string"}, "query": map[string]string{"type": "string"}, "limit": map[string]string{"type": "integer"}}}},
				{"name": "echo_wa_get_messages", "description": "Read messages for a WhatsApp chat JID.", "inputSchema": map[string]any{"type": "object", "required": []string{"chatJid"}, "properties": map[string]any{"deviceId": map[string]string{"type": "string"}, "chatJid": map[string]string{"type": "string"}, "limit": map[string]string{"type": "integer"}}}},
				{"name": "echo_radar_events", "description": "List clustered R!OTS World Radar events optionally filtered by domain ID (D01-D23).", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"domain": map[string]string{"type": "string"}, "limit": map[string]string{"type": "integer"}}}},
				{"name": "echo_radar_run", "description": "Execute a World Radar monitoring instance collection & assessment cycle.", "inputSchema": map[string]any{"type": "object", "required": []string{"instanceId"}, "properties": map[string]any{"instanceId": map[string]string{"type": "string"}}}},
				{"name": "echo_search_blueprints", "description": "Search self-hosted PaaS Docker Compose blueprints via FTS5.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]string{"type": "string"}, "source": map[string]string{"type": "string"}, "category": map[string]string{"type": "string"}, "limit": map[string]string{"type": "integer"}}}},
				{"name": "echo_run_flow", "description": "Execute an automation flow by ID with an optional trigger payload.", "inputSchema": map[string]any{"type": "object", "required": []string{"id"}, "properties": map[string]any{"id": map[string]string{"type": "string"}, "triggerPayload": map[string]string{"type": "string"}}}},
				{"name": "echo_comms_send", "description": "Send an outbound message or alert through a configured Telegram or Discord channel.", "inputSchema": map[string]any{"type": "object", "required": []string{"body"}, "properties": map[string]any{"channelId": map[string]string{"type": "string"}, "kind": map[string]string{"type": "string"}, "targetRef": map[string]string{"type": "string"}, "title": map[string]string{"type": "string"}, "body": map[string]string{"type": "string"}, "priorityTier": map[string]string{"type": "string"}}}},
				{"name": "echo_env_keys", "description": "List redacted secret key metadata in VaultEnv for a project and environment (never exposes plaintext secret values).", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"project": map[string]string{"type": "string"}, "env": map[string]string{"type": "string"}}}},
				{"name": "echo_desktop_doctor", "description": "Run MW-ECHO desktop runtime health diagnostics and generate a redacted support report.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
				{"name": "echo_local_ai_discover", "description": "Discover local loopback AI runtimes (Ollama, LM Studio, vLLM) and available models.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
				{"name": "echo_disk_audit", "description": "Read-only disk telemetry and hotspot quick audit across local volumes (never deletes files).", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
				{"name": "echo_memvault_search", "description": "Full-text search cloud-safe public conversations in local MemVault (strictly enforces cloudSafeOnly=true privacy firewall).", "inputSchema": map[string]any{"type": "object", "required": []string{"query"}, "properties": map[string]any{"query": map[string]string{"type": "string"}, "limit": map[string]string{"type": "integer"}}}},
				{"name": "echo_fleet_probe", "description": "Concurrently probe /health across all registered Sovereign Fleet services.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
				{"name": "echo_arsy_audit_verify", "description": "Verify the SHA-256 Merkle hash chain integrity of the ARSY constitutional audit log.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
				{"name": "echo_storage_restore_verify", "description": "Run a non-destructive 9Drive SQLite backup restore & PRAGMA integrity_check verification drill.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"objectId": map[string]string{"type": "string"}}}},
			},
		})
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(rpc.Params, &call); err != nil {
			return fail(-32602, "invalid tool params")
		}
		strArg := func(key string) string {
			if v, ok := call.Arguments[key].(string); ok {
				return v
			}
			return ""
		}
		intArg := func(key string, fallback int) int {
			if v, ok := call.Arguments[key].(float64); ok {
				return int(v)
			}
			return fallback
		}
		ctx := c.Request().Context()
		var out any
		var err error
		switch call.Name {
		case "echo_search_docs":
			out, err = hub.ListDocs(ctx, strArg("project"), strArg("category"), strArg("query"), intArg("limit", 25))
		case "echo_get_doc":
			out, err = hub.GetDoc(ctx, strArg("id"))
		case "echo_list_tasks":
			out, err = hub.ListTasks(ctx, strArg("project"), strArg("status"), strArg("priority"), strArg("query"), intArg("limit", 50))
		case "echo_update_task":
			out, err = hub.UpdateTaskStatus(ctx, strArg("code"), strArg("status"), strArg("priority"), strArg("title"))
		case "echo_list_storage":
			out, err = hub.ListBlobs(ctx, strArg("project"), strArg("query"), intArg("limit", 50))
		case "echo_list_projects":
			out, err = hub.ListProjects(ctx)
		case "echo_upsert_project":
			out, err = hub.UpsertProject(ctx, store.ProjectRecord{
				Slug:        strArg("slug"),
				Name:        strArg("name"),
				Description: strArg("description"),
				Status:      strArg("status"),
			})
		case "echo_wa_devices":
			out, err = waMgr.ListDevices(ctx)
		case "echo_wa_send_message":
			deviceID := strArg("deviceId")
			if deviceID == "" {
				deviceID = "default"
			}
			out, err = waMgr.SendText(ctx, deviceID, strArg("phone"), strArg("message"), "", nil)
		case "echo_wa_list_chats":
			out, err = waMgr.ListChats(ctx, strArg("deviceId"), strArg("query"), intArg("limit", 50))
		case "echo_wa_get_messages":
			deviceID := strArg("deviceId")
			if deviceID == "" {
				deviceID = "default"
			}
			out, err = waMgr.ListMessages(ctx, deviceID, strArg("chatJid"), intArg("limit", 50))
		case "echo_radar_events":
			out, err = worldRadar.ListEvents(ctx, strArg("domain"), intArg("limit", 50))
		case "echo_radar_run":
			instanceID := strArg("instanceId")
			if instanceID == "" {
				instanceID = strArg("id")
			}
			out, err = worldRadar.RunInstance(ctx, instanceID)
		case "echo_search_blueprints":
			out, err = blueprintCat.Search(ctx, strArg("query"), strArg("source"), strArg("category"), intArg("limit", 25))
		case "echo_run_flow":
			flowID := strArg("id")
			if flowID == "" {
				flowID = strArg("flowId")
			}
			payload := strArg("triggerPayload")
			if payload == "" {
				payload = strArg("payload")
			}
			out, err = flowEngine.RunFlowByID(ctx, flowID, payload)
		case "echo_comms_send":
			out, err = commsMgr.Send(ctx, comms.SendInput{
				ChannelID:    strArg("channelId"),
				Kind:         strArg("kind"),
				TargetRef:    strArg("targetRef"),
				Title:        strArg("title"),
				Body:         strArg("body"),
				PriorityTier: strArg("priorityTier"),
			})
		case "echo_env_keys":
			envName := strArg("env")
			if envName == "" {
				envName = strArg("envName")
			}
			out, err = vaultEnv.ListKeys(ctx, strArg("project"), envName)
		case "echo_desktop_doctor":
			out, err = desktopMgr.RunDoctor(ctx)
		case "echo_local_ai_discover":
			out = desktopMgr.DiscoverLocalAI(ctx, nil)
		case "echo_disk_audit":
			drives, drivesErr := diskdoctor.ListDrives()
			if drivesErr != nil {
				err = drivesErr
			} else {
				quickAudit, auditErr := diskdoctor.QuickAudit(ctx)
				if auditErr != nil {
					err = auditErr
				} else {
					out = map[string]any{
						"drives":     drives,
						"quickAudit": quickAudit,
					}
				}
			}
		case "echo_memvault_search":
			out, err = memMgr.Search(ctx, strArg("query"), true, intArg("limit", 25))
		case "echo_fleet_probe":
			out, err = fleetMgr.ProbeFleet(ctx)
		case "echo_arsy_audit_verify":
			out, err = fleetMgr.VerifyAuditChain(ctx)
		case "echo_storage_restore_verify":
			out, err = pool.VerifyLatestBackupSnapshot(ctx, firstNonEmpty(strArg("objectId"), strArg("objectKey")))
		default:
			return fail(-32601, "unknown tool: "+call.Name)
		}
		if err != nil {
			return fail(-32000, err.Error())
		}
		encoded, _ := json.MarshalIndent(out, "", "  ")
		return reply(map[string]any{
			"content": []map[string]string{{"type": "text", "text": string(encoded)}},
		})
	default:
		return fail(-32601, "method not found: "+rpc.Method)
	}
}
