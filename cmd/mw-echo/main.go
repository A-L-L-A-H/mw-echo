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
	"syscall"
	"time"

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
	if len(os.Args) > 1 && os.Args[1] == "import" {
		runImportCommand(os.Args[2:])
		return
	}

	addr := flag.String("addr", envOrDefault("MW_ECHO_ADDR", "127.0.0.1:4310"), "HTTP listen address")
	dataDir := flag.String("data-dir", defaultDataDir(), "SQLite and blob storage directory")
	authToken := flag.String("token", os.Getenv("MW_ECHO_TOKEN"), "Optional Bearer token for /api/* routes")
	flag.Parse()

	hub, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	app := server.NewEcho(hub, server.Config{
		Addr:      *addr,
		AuthToken: *authToken,
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
