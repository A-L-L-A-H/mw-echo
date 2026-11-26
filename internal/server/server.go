package server

import (
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/A-L-L-A-H/mw-echo/internal/importer"
	"github.com/A-L-L-A-H/mw-echo/internal/storage"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

//go:embed ui/index.html
var embeddedUI []byte

type Config struct {
	Addr      string
	AuthToken string
}

func NewEcho(hub *store.HubStore, cfg Config) *echo.Echo {
	app := echo.New()
	app.HideBanner = true
	app.HidePort = true
	app.Use(middleware.Recover())
	app.Use(middleware.Secure())

	pool, err := storage.NewPoolManager(hub)
	if err != nil {
		panic("init storage pool: " + err.Error())
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

	app.GET("/", func(c echo.Context) error {
		return c.Blob(http.StatusOK, "text/html; charset=utf-8", embeddedUI)
	})

	app.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "mw-echo",
			"dataDir": hub.DataDir(),
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
		updated, err := hub.UpdateTaskStatus(c.Request().Context(), c.Param("code"), patch.Status, patch.Priority, patch.Title)
		if errors.Is(err, sql.ErrNoRows) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "task not found"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

	// 9Drive-pattern Multi-Account Storage Pool (Google Drive + S3/R2 + Local fallback)
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

	// Unified storage routes (zero local disk waste when Google Drive / S3 pool connected)
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
		return c.JSON(http.StatusCreated, saved)
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
		return c.JSON(http.StatusOK, summary)
	})

	// Lightweight MCP JSON-RPC 2.0 endpoint for AI agents
	api.POST("/mcp", func(c echo.Context) error {
		return handleMCP(c, hub)
	})

	return app
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func handleMCP(c echo.Context, hub *store.HubStore) error {
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
			"serverInfo":      map[string]string{"name": "mw-echo", "version": "1.0.0"},
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
