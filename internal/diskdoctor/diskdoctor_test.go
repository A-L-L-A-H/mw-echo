package diskdoctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestListDrives(t *testing.T) {
	drives, err := ListDrives()
	if err != nil {
		t.Fatalf("ListDrives returned error: %v", err)
	}
	if len(drives) == 0 {
		t.Fatalf("expected at least 1 drive, got 0")
	}
	for _, drive := range drives {
		if drive.TotalBytes == 0 {
			t.Errorf("drive %s reported TotalBytes == 0", drive.Drive)
		}
		if drive.Status != "ok" && drive.Status != "warn" && drive.Status != "critical" {
			t.Errorf("drive %s has unexpected status %q", drive.Drive, drive.Status)
		}
	}
}

func TestScanDirectoryAttributionAndCandidates(t *testing.T) {
	sandboxRoot := t.TempDir()

	// Simulated node_modules
	nodeModFile := filepath.Join(sandboxRoot, "webapp", "node_modules", "pkg", "index.js")
	mustWriteBytes(t, nodeModFile, 4096)

	// Simulated Temp directory
	tempFile := filepath.Join(sandboxRoot, "Temp", "scratch.tmp")
	mustWriteBytes(t, tempFile, 2048)

	// Simulated Rust target with sibling Cargo.toml
	cargoToml := filepath.Join(sandboxRoot, "rustapp", "Cargo.toml")
	mustWriteBytes(t, cargoToml, 64)
	targetArtifact := filepath.Join(sandboxRoot, "rustapp", "target", "debug", "app.exe")
	mustWriteBytes(t, targetArtifact, 8192)

	report, err := ScanDirectory(context.Background(), sandboxRoot, 4, 10000)
	if err != nil {
		t.Fatalf("ScanDirectory failed: %v", err)
	}

	if report.ScannedBytes < 4096+2048+8192+64 {
		t.Errorf("expected ScannedBytes >= 14400, got %d", report.ScannedBytes)
	}
	if len(report.CleanCandidates) < 3 {
		t.Errorf("expected at least 3 clean candidates, got %d", len(report.CleanCandidates))
	}

	foundCategories := make(map[string]string)
	for _, app := range report.AppBreakdown {
		foundCategories[app.Category] = app.Safety
	}

	if safety, ok := foundCategories["Node.js / Bun Dependencies"]; !ok || safety != SafetyCaution {
		t.Errorf("expected Node.js / Bun Dependencies (caution), got ok=%v safety=%q", ok, safety)
	}
	if safety, ok := foundCategories["OS / App Temp Files"]; !ok || safety != SafetySafe {
		t.Errorf("expected OS / App Temp Files (safe), got ok=%v safety=%q", ok, safety)
	}
	if safety, ok := foundCategories["Rust / Cargo / Tauri Build"]; !ok || safety != SafetySafe {
		t.Errorf("expected Rust / Cargo / Tauri Build (safe), got ok=%v safety=%q", ok, safety)
	}
}

func TestCleanPathsDryRunLiveAndProtectedGuard(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	defer hub.Close()

	mgr, err := NewManager(hub, Options{})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// Protected path rejection
	protectedTargets := []string{"/", `C:\`, `C:\Windows`, `C:\Program Files`}
	if runtime.GOOS != "windows" {
		protectedTargets = []string{"/", "/Windows", "/System32"}
	}
	for _, blocked := range protectedTargets {
		if _, cleanErr := mgr.CleanPaths(context.Background(), []string{blocked}, true); cleanErr == nil {
			t.Errorf("expected protected path %q to be rejected, but CleanPaths succeeded", blocked)
		}
	}

	// Prepare cleanable temp directory
	cleanTarget := filepath.Join(t.TempDir(), "Temp")
	fileA := filepath.Join(cleanTarget, "a.tmp")
	fileB := filepath.Join(cleanTarget, "nested", "b.tmp")
	mustWriteBytes(t, fileA, 1024)
	mustWriteBytes(t, fileB, 2048)

	// Dry run must compute exact bytes without deleting
	dryResult, err := mgr.CleanPaths(context.Background(), []string{cleanTarget}, true)
	if err != nil {
		t.Fatalf("dryRun CleanPaths failed: %v", err)
	}
	if !dryResult.DryRun || dryResult.FreedBytes != 3072 || dryResult.DeletedFiles != 2 {
		t.Errorf("unexpected dryRun result: %+v", dryResult)
	}
	if _, statErr := os.Stat(fileA); statErr != nil {
		t.Errorf("dryRun deleted fileA unexpectedly: %v", statErr)
	}

	// Live run must delete files and record history
	liveResult, err := mgr.CleanPaths(context.Background(), []string{cleanTarget}, false)
	if err != nil {
		t.Fatalf("live CleanPaths failed: %v", err)
	}
	if liveResult.DryRun || liveResult.FreedBytes != 3072 || liveResult.DeletedFiles != 2 {
		t.Errorf("unexpected live result: %+v", liveResult)
	}
	if liveResult.HistoryID == "" {
		t.Errorf("expected non-empty HistoryID after live clean")
	}
	if _, statErr := os.Stat(fileA); !os.IsNotExist(statErr) {
		t.Errorf("expected fileA to be removed, stat err: %v", statErr)
	}

	history, err := mgr.ListHistory(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListHistory failed: %v", err)
	}
	if len(history) != 1 || history[0].FreedBytes != 3072 || history[0].DeletedFiles != 2 {
		t.Errorf("unexpected history records: %+v", history)
	}
}

func TestAnalyzeWithAIAndDeterministicFallback(t *testing.T) {
	drives := []DriveInfo{
		{
			Drive:      `C:\`,
			TotalBytes: 500 * 1024 * 1024 * 1024,
			UsedBytes:  460 * 1024 * 1024 * 1024,
			FreeBytes:  40 * 1024 * 1024 * 1024,
			UsedPct:    92.0,
			Status:     "critical",
		},
	}
	report := ScanReport{
		RootPath:     `C:\Users\dev`,
		ScannedBytes: 5 * 1024 * 1024 * 1024,
		ScannedFiles: 12000,
		DurationMs:   150,
		AppBreakdown: []AppUsage{
			{Category: "OS / App Temp Files", Safety: SafetySafe, Bytes: 3 * 1024 * 1024 * 1024, DirCount: 2},
		},
		CleanCandidates: []DirUsage{
			{Path: `C:\Users\dev\AppData\Local\Temp`, AppCategory: "OS / App Temp Files", Safety: SafetySafe, Bytes: 3 * 1024 * 1024 * 1024},
		},
	}

	// 1. Mock AI callback -> source="ai_gateway"
	aiMgr, err := NewManager(nil, Options{
		AIAnalyze: func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
			if !strings.Contains(userPrompt, "OS / App Temp Files") {
				return "", errors.New("missing telemetry in prompt")
			}
			return "Clear Local Temp immediately to recover 3.0 GiB on C:\\.", nil
		},
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	aiInsight, err := aiMgr.AnalyzeWithAI(context.Background(), drives, report)
	if err != nil {
		t.Fatalf("AnalyzeWithAI failed: %v", err)
	}
	if aiInsight.Source != "ai_gateway" {
		t.Errorf("expected source=ai_gateway, got %q", aiInsight.Source)
	}
	if aiInsight.EstimatedRecoverableBytes != 3*1024*1024*1024 {
		t.Errorf("unexpected recoverable bytes: %d", aiInsight.EstimatedRecoverableBytes)
	}

	// 2. Fallback when AIAnalyze is nil or returns error -> source="deterministic"
	fallbackMgr, err := NewManager(nil, Options{
		AIAnalyze: func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
			return "", errors.New("provider unavailable")
		},
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	fallbackInsight, err := fallbackMgr.AnalyzeWithAI(context.Background(), drives, report)
	if err != nil {
		t.Fatalf("AnalyzeWithAI fallback failed: %v", err)
	}
	if fallbackInsight.Source != "deterministic" {
		t.Errorf("expected source=deterministic, got %q", fallbackInsight.Source)
	}
	if !strings.Contains(fallbackInsight.Summary, "CRITICAL") {
		t.Errorf("expected deterministic summary to mention CRITICAL drive, got %q", fallbackInsight.Summary)
	}
}

func mustWriteBytes(t *testing.T, targetPath string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", targetPath, err)
	}
	payload := make([]byte, size)
	if err := os.WriteFile(targetPath, payload, 0o644); err != nil {
		t.Fatalf("write %s: %v", targetPath, err)
	}
}
