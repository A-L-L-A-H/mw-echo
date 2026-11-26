package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/codingagent"
	"github.com/A-L-L-A-H/mw-echo/internal/desktop"
	"github.com/A-L-L-A-H/mw-echo/internal/diskdoctor"
	"github.com/A-L-L-A-H/mw-echo/internal/importer"
	"github.com/A-L-L-A-H/mw-echo/internal/server"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/A-L-L-A-H/mw-echo/internal/vaultenv"
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
		case "env":
			runEnvCommand(os.Args[2:])
			return
		case "doctor":
			runDoctorCommand(os.Args[2:])
			return
		case "desktop":
			runDesktopCommand(os.Args[2:])
			return
		case "disk":
			runDiskCommand(os.Args[2:])
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

	onEvent := func(ev codingagent.AgentEvent) {
		switch ev.Type {
		case "tool_call":
			fmt.Fprintf(os.Stderr, "[tool:%s] %s\n", ev.ToolName, ev.Arguments)
		case "tool_result":
			fmt.Fprintf(os.Stderr, "[result:%s] %s\n", ev.ToolName, strings.TrimSpace(ev.Output))
		}
	}

	if prompt == "" {
		runCodeREPL(engine, *workDir, *agentProfile, *model, *mode, onEvent)
		return
	}

	reply, err := engine.RunOneShot(context.Background(), *workDir, *agentProfile, *model, *mode, prompt, onEvent)
	if err != nil {
		log.Fatalf("coding agent failed: %v", err)
	}
	fmt.Println(reply)
}

func runCodeREPL(
	engine *codingagent.Engine,
	workDir string,
	agentProfile string,
	model string,
	mode string,
	onEvent func(codingagent.AgentEvent),
) {
	sess, err := engine.CreateSession(context.Background(), codingagent.SessionCreateInput{
		WorkDir:      workDir,
		AgentProfile: agentProfile,
		Model:        model,
		Mode:         mode,
	})
	if err != nil {
		log.Fatalf("create coding session: %v", err)
	}

	fmt.Printf("MW-ECHO CODER REPL // session=%s mode=%s agent=%s — /plan /act /model <id> /exit\n", sess.ID, sess.Mode, sess.AgentProfile)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Printf("[%s] > ", sess.Mode)
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			break
		}
		if line == "/plan" {
			nextMode := codingagent.ModePlan
			updated, updErr := engine.UpdateSession(context.Background(), sess.ID, codingagent.SessionPatchInput{Mode: &nextMode})
			if updErr != nil {
				fmt.Fprintf(os.Stderr, "mode update failed: %v\n", updErr)
				continue
			}
			sess = updated
			fmt.Printf("mode switched to %s\n", sess.Mode)
			continue
		}
		if line == "/act" {
			nextMode := codingagent.ModeAct
			updated, updErr := engine.UpdateSession(context.Background(), sess.ID, codingagent.SessionPatchInput{Mode: &nextMode})
			if updErr != nil {
				fmt.Fprintf(os.Stderr, "mode update failed: %v\n", updErr)
				continue
			}
			sess = updated
			fmt.Printf("mode switched to %s\n", sess.Mode)
			continue
		}
		if line == "/model" || strings.HasPrefix(line, "/model ") {
			nextModel := strings.TrimSpace(strings.TrimPrefix(line, "/model"))
			if nextModel == "" {
				fmt.Printf("current model: %s\n", sess.Model)
				continue
			}
			updated, updErr := engine.UpdateSession(context.Background(), sess.ID, codingagent.SessionPatchInput{Model: &nextModel})
			if updErr != nil {
				fmt.Fprintf(os.Stderr, "model update failed: %v\n", updErr)
				continue
			}
			sess = updated
			fmt.Printf("model switched to %s\n", sess.Model)
			continue
		}

		outcome, turnErr := engine.RunPromptWithCallback(context.Background(), sess.ID, line, onEvent)
		if turnErr != nil {
			fmt.Fprintf(os.Stderr, "coding agent error: %v\n", turnErr)
			continue
		}
		sess = outcome.Session
		fmt.Println(outcome.Reply)
	}
	if scanErr := scanner.Err(); scanErr != nil {
		log.Fatalf("repl input error: %v", scanErr)
	}
}

func runEnvCommand(args []string) {
	if len(args) == 0 || args[0] != "export" {
		log.Fatal("usage: mw-echo env export --project <slug> --env <dev|staging|prod|shared> [--data-dir <path>]")
	}
	fs := flag.NewFlagSet("env export", flag.ExitOnError)
	project := fs.String("project", "default", "Project slug")
	envName := fs.String("env", vaultenv.EnvDev, "Environment name (dev|staging|prod|shared)")
	dataDir := fs.String("data-dir", defaultDataDir(), "SQLite and blob storage directory")
	_ = fs.Parse(args[1:])

	hub, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	vaultEnv, err := vaultenv.NewVault(hub, vaultenv.Options{})
	if err != nil {
		log.Fatalf("init vaultenv: %v", err)
	}
	dotenv, err := vaultEnv.ExportDotEnv(context.Background(), *project, *envName)
	if err != nil {
		log.Fatalf("export dotenv: %v", err)
	}
	fmt.Print(dotenv)
}

func runDoctorCommand(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	repair := fs.Bool("repair", false, "Run self-repair actions before diagnostics")
	dataDir := fs.String("data-dir", defaultDataDir(), "SQLite and blob storage directory")
	_ = fs.Parse(args)

	hub, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	desktopMgr, err := desktop.NewManager(hub, desktop.Options{})
	if err != nil {
		log.Fatalf("init desktop manager: %v", err)
	}

	if *repair {
		repairRes, repErr := desktopMgr.RunRepair(context.Background())
		if repErr != nil {
			log.Fatalf("doctor repair failed: %v", repErr)
		}
		fmt.Println(repairRes.Report.SupportReportMarkdown)
		return
	}

	report, err := desktopMgr.RunDoctor(context.Background())
	if err != nil {
		log.Fatalf("doctor failed: %v", err)
	}
	fmt.Println(report.SupportReportMarkdown)
}

func runDesktopCommand(args []string) {
	fs := flag.NewFlagSet("desktop", flag.ExitOnError)
	addr := fs.String("addr", envOrDefault("MW_ECHO_ADDR", "127.0.0.1:4310"), "HTTP listen address")
	dataDir := fs.String("data-dir", defaultDataDir(), "SQLite and blob storage directory")
	authToken := fs.String("token", os.Getenv("MW_ECHO_TOKEN"), "Optional Bearer token for /api/* routes")
	waUser := fs.String("wa-user", envOrDefault("MW_ECHO_WA_USER", os.Getenv("WHATSAPP_GATEWAY_USERNAME")), "Optional Basic Auth username for GOWA endpoints")
	waPass := fs.String("wa-pass", envOrDefault("MW_ECHO_WA_PASS", os.Getenv("WHATSAPP_GATEWAY_PASSWORD")), "Optional Basic Auth password for GOWA endpoints")
	_ = fs.Parse(args)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}

	hub, err := store.Open(*dataDir)
	if err != nil {
		_ = ln.Close()
		log.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	app := server.NewEcho(hub, server.Config{
		Addr:            ln.Addr().String(),
		AuthToken:       *authToken,
		WAUser:          *waUser,
		WAPass:          *waPass,
		WAWebhookURL:    envOrDefault("MW_ECHO_WA_WEBHOOK_URL", os.Getenv("WHATSAPP_WEBHOOK_URL")),
		WAWebhookSecret: envOrDefault("MW_ECHO_WA_WEBHOOK_SECRET", os.Getenv("WHATSAPP_WEBHOOK_SECRET")),
	})
	app.Listener = ln

	go func() {
		log.Printf("[mw-echo] desktop listening on http://%s (data: %s)", ln.Addr().String(), hub.DataDir())
		if serveErr := app.StartServer(app.Server); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Fatalf("desktop server error: %v", serveErr)
		}
	}()

	launchURL := "http://" + ln.Addr().String()
	if winErr := desktop.OpenDesktopWindow(launchURL); winErr != nil {
		log.Printf("[mw-echo] desktop window warning: %v", winErr)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = app.Shutdown(ctx)
}

func runDiskCommand(args []string) {
	fs := flag.NewFlagSet("disk", flag.ExitOnError)
	scanPath := fs.String("scan", "", "Optional directory path to scan instead of quick hotspot audit")
	withAI := fs.Bool("ai", false, "Run AI/engineering deep diagnosis on the scan report")
	dataDir := fs.String("data-dir", defaultDataDir(), "SQLite and blob storage directory")
	_ = fs.Parse(args)

	ctx := context.Background()

	drives, err := diskdoctor.ListDrives()
	if err != nil {
		log.Fatalf("list drives failed: %v", err)
	}
	fmt.Println("=== DRIVES ===")
	for _, drive := range drives {
		freeGB := float64(drive.FreeBytes) / (1024 * 1024 * 1024)
		totalGB := float64(drive.TotalBytes) / (1024 * 1024 * 1024)
		fmt.Printf("  %-4s  %5.1f%% used  %6.1f GB free / %6.1f GB total  [%s]\n",
			drive.Drive, drive.UsedPct, freeGB, totalGB, strings.ToUpper(drive.Status))
	}

	var report diskdoctor.ScanReport
	if strings.TrimSpace(*scanPath) != "" {
		report, err = diskdoctor.ScanDirectory(ctx, *scanPath, 3, 50000)
		if err != nil {
			log.Fatalf("disk scan failed: %v", err)
		}
	} else {
		report, err = diskdoctor.QuickAudit(ctx)
		if err != nil {
			log.Fatalf("disk quick-audit failed: %v", err)
		}
	}

	fmt.Printf("\n=== AUDIT (%s | %.2f GiB | %d files | %dms) ===\n",
		report.RootPath, float64(report.ScannedBytes)/(1024*1024*1024), report.ScannedFiles, report.DurationMs)

	fmt.Println("\nTop App Breakdown:")
	for idx, appUsage := range report.AppBreakdown {
		if idx >= 8 {
			break
		}
		fmt.Printf("  %-28s  %8.2f MiB  (%d dirs)  [%s]\n",
			appUsage.Category, float64(appUsage.Bytes)/(1024*1024), appUsage.DirCount, appUsage.Safety)
	}

	fmt.Println("\nClean Candidates:")
	for idx, candidate := range report.CleanCandidates {
		if idx >= 10 {
			break
		}
		fmt.Printf("  [%-9s] %8.2f MiB  %s  (%s)\n",
			candidate.Safety, float64(candidate.Bytes)/(1024*1024), candidate.Path, candidate.AppCategory)
	}

	if *withAI {
		hub, hubErr := store.Open(*dataDir)
		if hubErr != nil {
			log.Fatalf("open hub store: %v", hubErr)
		}
		defer func() { _ = hub.Close() }()

		diskMgr, mgrErr := diskdoctor.NewManager(hub, diskdoctor.Options{})
		if mgrErr != nil {
			log.Fatalf("init disk doctor: %v", mgrErr)
		}
		insight, insightErr := diskMgr.AnalyzeWithAI(ctx, drives, report)
		if insightErr != nil {
			log.Fatalf("disk insight failed: %v", insightErr)
		}
		fmt.Printf("\n=== DIAGNOSIS (%s) ===\n%s\n", insight.Source, insight.Summary)
		if len(insight.RootCauses) > 0 {
			fmt.Println("\nRoot Causes:")
			for _, cause := range insight.RootCauses {
				fmt.Printf("  - %s\n", cause)
			}
		}
		if len(insight.Recommendations) > 0 {
			fmt.Println("\nRecommendations:")
			for _, rec := range insight.Recommendations {
				fmt.Printf("  - %s\n", rec)
			}
		}
	}
}
