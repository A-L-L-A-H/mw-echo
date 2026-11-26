package codingagent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/codingagent"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestSearchWorkspaceSymbolsAndToolRegistration(t *testing.T) {
	workspace := t.TempDir()

	files := map[string]string{
		"pkg/auth.go":                    "package pkg\ntype TokenVerifier struct{}\nfunc VerifyRS256Token() error { return nil }\n",
		"web/client.ts":                  "export interface FleetStatus { ok: boolean }\nexport async function fetchFleetStatus() {}\n",
		"crate/lib.rs":                   "pub struct MeshNode {}\npub fn connect_mesh_node() {}\n",
		"scripts/audit.py":               "class AuditRunner:\n    pass\ndef run_audit():\n    return True\n",
		"node_modules/ignored/skip.ts":   "export function ShouldNeverAppear() {}\n",
		"vendor/ignored/skip.go":         "package ignored\nfunc VendorShouldNeverAppear() {}\n",
	}
	for relPath, content := range files {
		fullPath := filepath.Join(workspace, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	allSymbols, err := codingagent.SearchWorkspaceSymbols(workspace, "", 50)
	if err != nil {
		t.Fatalf("search all symbols: %v", err)
	}
	if len(allSymbols) != 8 {
		t.Fatalf("expected 8 symbols across 4 languages, got %d: %+v", len(allSymbols), allSymbols)
	}
	for _, sym := range allSymbols {
		if strings.Contains(sym.Name, "ShouldNeverAppear") {
			t.Fatalf("skipped directory leaked symbol: %+v", sym)
		}
	}

	filtered, err := codingagent.SearchWorkspaceSymbols(workspace, "FleetStatus", 10)
	if err != nil {
		t.Fatalf("search filtered symbols: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("expected 2 FleetStatus matches, got %d: %+v", len(filtered), filtered)
	}

	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = hub.Close() }()

	engine, err := codingagent.NewEngine(hub, codingagent.Options{})
	if err != nil {
		t.Fatalf("new coding engine: %v", err)
	}

	foundSpec := false
	for _, spec := range engine.ToolSpecs() {
		if spec.Name == "search_symbols" && spec.ReadOnly {
			foundSpec = true
			break
		}
	}
	if !foundSpec {
		t.Fatalf("search_symbols tool not registered in ToolSpecs")
	}

	toolOut, err := engine.ExecuteTool(context.Background(), codingagent.Session{
		WorkDir: workspace,
		Mode:    codingagent.ModePlan,
	}, "search_symbols", `{"query":"VerifyRS256Token","limit":5}`)
	if err != nil {
		t.Fatalf("execute search_symbols tool: %v", err)
	}
	if !strings.Contains(toolOut, "pkg/auth.go:3 [go/func] VerifyRS256Token") {
		t.Fatalf("unexpected search_symbols output: %s", toolOut)
	}
}
