package codingagent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/codingagent"
)

func TestSearchWorkspaceSymbols(t *testing.T) {
	// Create a temporary workspace with sample source files
	workspaceDir := t.TempDir()

	// Create a Go file with symbols
	goFile := filepath.Join(workspaceDir, "example.go")
	os.WriteFile(goFile, []byte(`package example

// Foo is a function declaration
func Foo(x int) string {
	return "hello"
}

// Bar is a struct declaration
type Bar struct {
	Name string
}`), 0o644)

	// Search for symbols
	symbols, err := codingagent.SearchWorkspaceSymbols(workspaceDir, "Foo", 50)
	if err != nil {
		t.Fatalf("SearchWorkspaceSymbols failed: %v", err)
	}

	if len(symbols) == 0 {
		t.Error("expected at least 1 matching symbol")
	}

	foundFoo := false
	for _, sym := range symbols {
		if sym.Name == "Foo" {
			foundFoo = true
			if sym.Kind != "func" {
				t.Errorf("expected kind 'func', got '%s'", sym.Kind)
			}
			if sym.Language != "go" {
				t.Errorf("expected language 'go', got '%s'", sym.Language)
			}
			break
		}
	}
	if !foundFoo {
		t.Error("expected to find symbol 'Foo'")
	}

	// Search for struct symbols
	symbols2, err := codingagent.SearchWorkspaceSymbols(workspaceDir, "Bar", 50)
	if err != nil {
		t.Fatalf("SearchWorkspaceSymbols failed: %v", err)
	}

	foundBar := false
	for _, sym := range symbols2 {
		if sym.Name == "Bar" {
			foundBar = true
			if sym.Kind != "struct" {
				t.Errorf("expected kind 'struct', got '%s'", sym.Kind)
			}
			break
		}
	}
	if !foundBar {
		t.Error("expected to find symbol 'Bar'")
	}

	// Search with no match
	symbols3, err := codingagent.SearchWorkspaceSymbols(workspaceDir, "NonExistent", 50)
	if err != nil {
		t.Fatalf("SearchWorkspaceSymbols failed: %v", err)
	}
	if len(symbols3) != 0 {
		t.Error("expected no matching symbols for 'NonExistent'")
	}

	t.Logf("SearchWorkspaceSymbols test passed! Found %d symbols for 'Foo'", len(symbols))
}
