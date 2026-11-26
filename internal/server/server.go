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
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/aigateway"
	"github.com/A-L-L-A-H/mw-echo/internal/assistant"
	"github.com/A-L-L-A-H/mw-echo/internal/codingagent"
	"github.com/A-L-L-A-H/mw-echo/internal/gamification"
	"github.com/A-L-L-A-H/mw-echo/internal/importer"
	"github.com/A-L-L-A-H/mw-echo/internal/observatory"
	"github.com/A-L-L-A-H/mw-echo/internal/storage"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
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

	coderEngine, err := codingagent.NewEngine(hub, codingagent.Options{
		DefaultGatewayURL:   internalGatewayURL,
		DefaultAgentProfile: codingagent.DefaultAgentProfile,
		HTTPClient:          internalClient,
	})
	if err != nil {
		panic("init coding agent: " + err.Error())
	}

	waMgr, err := whatsapp.NewManager(hub, whatsapp.Options{
		BasicUser:            cfg.WAUser,
		BasicPass:            cfg.WAPass,
		DefaultWebhookURL:    cfg.WAWebhookURL,
		DefaultWebhookSecret: cfg.WAWebhookSecret,
		AutoReply: func(ctx context.Context, agentID, sender, text string) (string, error) {
			reqPayload := map[string]any{
				"messages": []map[string]string{
					{"role": "system", "content": "You are a helpful WhatsApp auto-reply assistant for mw-echo. Answer briefly and clearly."},
					{"role": "user", "content": fmt.Sprintf("From %s: %s", sender, text)},
				},
			}
			encoded, _ := json.Marshal(reqPayload)
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, internalGatewayURL, bytes.NewReader(encoded))
			if reqErr != nil {
				return "", reqErr
			}
			req.Header.Set("Content-Type", "application/json")
			if strings.TrimSpace(agentID) != "" {
				req.Header.Set("X-MW-Agent", strings.TrimSpace(agentID))
			}
			resp, doErr := internalClient.Do(req)
			if doErr != nil {
				return "", doErr
			}
			bodyBytes, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode >= 400 {
				return "", fmt.Errorf("gateway auto-reply HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
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
				return "", errors.New("empty auto-reply choice")
			}
			return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
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

	authGuard := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if strings.TrimSpace(cfg.AuthToken) == "" {
				return next(c)
			}
			header := strings.TrimSpace(c.Request().Header.Get("Authorization"))
			provided := strings.TrimPrefix(header, "Bearer ")
			if header == provided || subtle.ConstantTimeCompare([]byte(provided), []byte(cfg.AuthToken)) != 1 {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			}
			return next(c)
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
			"backups": backups,
			"count":   len(backups),
		})
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
		return handleMCP(c, hub, waMgr, worldRadar)
	})

	// Unified AI Gateway (Bifrost + 9Router RTK + Magpie + FreeLLMAPI)
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

	return app
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func handleMCP(c echo.Context, hub *store.HubStore, waMgr *whatsapp.Manager, worldRadar *worldradar.Engine) error {
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
