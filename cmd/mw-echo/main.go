package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/codingagent"
	"github.com/A-L-L-A-H/mw-echo/internal/importer"
	"github.com/A-L-L-A-H/mw-echo/internal/server"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func defaultDataDir() string {
	if envDir := os.Getenv("MW_ECHO_DATA_DIR"); envDir != "" {
		return envDir
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		return filepath.Join(localAppData, "mw-echo")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "./data"
	}
	return filepath.Join(home, ".mw-echo")
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "import":
			runImportCommand(os.Args[2:])
			return
		case "code":
			runCodeCommand(os.Args[2:])
			return
		}
	}

	addr := flag.String("addr", envOrDefault("MW_ECHO_ADDR", "127.0.0.1:4310"), "HTTP listen address")
	dataDir := flag.String("data-dir", defaultDataDir(), "SQLite and blob storage directory")
	authToken := flag.String("token", os.Getenv("MW_ECHO_TOKEN"), "Optional Bearer token for /api/* routes")
	waUser := flag.String("wa-user", envOrDefault("MW_ECHO_WA_USER", os.Getenv("WHATSAPP_GATEWAY_USERNAME")), "Optional Basic Auth username for GOWA endpoints")
	waPass := flag.String("wa-pass", envOrDefault("MW_ECHO_WA_PASS", os.Getenv("WHATSAPP_GATEWAY_PASSWORD")), "Optional Basic Auth password for GOWA endpoints")
	flag.Parse()

	hub, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	app := server.NewEcho(hub, server.Config{
		Addr:            *addr,
		AuthToken:       *authToken,
		WAUser:          *waUser,
		WAPass:          *waPass,
		WAWebhookURL:    envOrDefault("MW_ECHO_WA_WEBHOOK_URL", os.Getenv("WHATSAPP_WEBHOOK_URL")),
		WAWebhookSecret: envOrDefault("MW_ECHO_WA_WEBHOOK_SECRET", os.Getenv("WHATSAPP_WEBHOOK_SECRET")),
	})

	go func() {
		log.Printf("[mw-echo] listening on http://%s (data: %s)", *addr, hub.DataDir())
		if err := app.Start(*addr); err != nil && err.Error() != "http: Server closed" {
			log.Fatalf("server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = app.Shutdown(ctx)
}

func runImportCommand(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	repoDir := fs.String("repo", "", "Repository path to import (docs/, tasks.json, root markdown)")
	project := fs.String("project", "moonwitness", "Project slug")
	dataDir := fs.String("data-dir", defaultDataDir(), "SQLite and blob storage directory")
	includeAssets := fs.Bool("assets", false, "Also import binary media assets (PNG/WebP/SVG/PDF/WebM)")
	_ = fs.Parse(args)

	if *repoDir == "" {
		log.Fatal("--repo is required")
	}
	hub, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	summary, err := importer.ImportRepo(context.Background(), hub, *project, *repoDir, *includeAssets)
	if err != nil {
		log.Fatalf("import failed: %v", err)
	}
	encoded, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Println(string(encoded))
}

func runCodeCommand(args []string) {
	fs := flag.NewFlagSet("code", flag.ExitOnError)
	workDir := fs.String("dir", ".", "Workspace directory")
	dataDir := fs.String("data-dir", defaultDataDir(), "SQLite and blob storage directory")
	agentProfile := fs.String("agent", envOrDefault("MW_ECHO_CODER_AGENT", codingagent.DefaultAgentProfile), "Magpie agent profile")
	model := fs.String("model", "", "Optional explicit model ID override")
	mode := fs.String("mode", codingagent.ModeAct, "Execution mode: act or plan")
	gatewayURL := fs.String("gateway", envOrDefault("MW_ECHO_GATEWAY_URL", codingagent.DefaultGatewayURL), "mw-echo AI gateway URL")
	_ = fs.Parse(args)

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		log.Fatal("usage: mw-echo code [--dir <path>] [--agent opencode] [--mode act|plan] <prompt>")
	}

	hub, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	engine, err := codingagent.NewEngine(hub, codingagent.Options{
		DefaultGatewayURL:   *gatewayURL,
		DefaultAgentProfile: *agentProfile,
	})
	if err != nil {
		log.Fatalf("init coding agent: %v", err)
	}

	reply, err := engine.RunOneShot(context.Background(), *workDir, *agentProfile, *model, *mode, prompt, func(ev codingagent.AgentEvent) {
		switch ev.Type {
		case "tool_call":
			fmt.Fprintf(os.Stderr, "[tool:%s] %s\n", ev.ToolName, ev.Arguments)
		case "tool_result":
			fmt.Fprintf(os.Stderr, "[result:%s] %s\n", ev.ToolName, strings.TrimSpace(ev.Output))
		}
	})
	if err != nil {
		log.Fatalf("coding agent failed: %v", err)
	}
	fmt.Println(reply)
}
