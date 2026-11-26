package diskdoctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	SafetySafe      = "safe"
	SafetyCaution   = "caution"
	SafetyProtected = "protected"

	defaultMaxDepth   = 3
	defaultMaxEntries = 50000
	quickAuditTimeout = 2500 * time.Millisecond
)

// DirUsage represents aggregated storage usage for a single directory.
type DirUsage struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Bytes       int64  `json:"bytes"`
	FileCount   int64  `json:"fileCount"`
	AppCategory string `json:"appCategory"`
	Safety      string `json:"safety"`
	Depth       int    `json:"depth"`
}

// AppUsage represents storage usage grouped by attributed application or subsystem.
type AppUsage struct {
	Category  string   `json:"category"`
	Safety    string   `json:"safety"`
	Bytes     int64    `json:"bytes"`
	FileCount int64    `json:"fileCount"`
	DirCount  int      `json:"dirCount"`
	TopPaths  []string `json:"topPaths"`
}

// ScanReport summarizes a directory scan or quick hotspot audit.
type ScanReport struct {
	RootPath        string     `json:"rootPath"`
	ScannedBytes    int64      `json:"scannedBytes"`
	ScannedFiles    int64      `json:"scannedFiles"`
	DurationMs      int64      `json:"durationMs"`
	TopDirectories  []DirUsage `json:"topDirectories"`
	AppBreakdown    []AppUsage `json:"appBreakdown"`
	CleanCandidates []DirUsage `json:"cleanCandidates"`
}

// ClassifyPath maps a directory or file path to its attributed application category and safety tier.
func ClassifyPath(targetPath string) (string, string) {
	cleaned := filepath.Clean(targetPath)
	normalized := strings.ToLower(filepath.ToSlash(cleaned))
	baseName := strings.ToLower(filepath.Base(cleaned))

	// Protected OS / application data paths take highest precedence.
	if hasPathSegment(normalized, "windows") ||
		hasPathSegment(normalized, "program files") ||
		hasPathSegment(normalized, "program files (x86)") ||
		hasPathSegment(normalized, "system32") ||
		hasPathSegment(normalized, "mw-echo-data") {
		return "System / Protected", SafetyProtected
	}

	if baseName == "node_modules" || hasPathSegment(normalized, "node_modules") {
		return "Node.js / Bun Dependencies", SafetyCaution
	}

	if baseName == "target" || hasPathSegment(normalized, "target") {
		if isRustTargetPath(cleaned, normalized) {
			return "Rust / Cargo / Tauri Build", SafetySafe
		}
	}

	switch baseName {
	case ".next", ".turbo", ".vite", "dist", "build":
		return "Frontend Build Cache", SafetySafe
	}

	if baseName == "temp" || baseName == "tmp" || hasPathSegment(normalized, "temp") || hasPathSegment(normalized, "tmp") {
		return "OS / App Temp Files", SafetySafe
	}

	if strings.Contains(normalized, "npm-cache") ||
		strings.Contains(normalized, ".bun/install/cache") ||
		strings.Contains(normalized, ".pnpm-store") ||
		strings.Contains(normalized, ".yarn/cache") {
		return "JS Package Cache", SafetySafe
	}

	if strings.Contains(normalized, "go-build") || strings.Contains(normalized, "go/pkg/mod/cache") {
		return "Go Build / Mod Cache", SafetySafe
	}

	if strings.Contains(normalized, "pip/cache") ||
		strings.Contains(normalized, ".cache/uv") ||
		strings.Contains(normalized, ".cache/huggingface") {
		return "Python / ML Cache", SafetyCaution
	}

	if hasPathSegment(normalized, "docker") || strings.Contains(normalized, "ext4.vhdx") {
		return "Docker WSL2 / Containers", SafetyCaution
	}

	if (strings.Contains(normalized, "chrome/user data/") || strings.Contains(normalized, "edge/user data/")) &&
		strings.Contains(normalized, "/cache") {
		return "Browser Cache", SafetySafe
	}

	if baseName == "worktrees" || hasPathSegment(normalized, "worktrees") {
		return "AI Agent Worktrees", SafetyCaution
	}

	return "General Files", SafetyCaution
}

func hasPathSegment(slashLowerPath string, segment string) bool {
	parts := strings.Split(slashLowerPath, "/")
	for _, part := range parts {
		if part == segment {
			return true
		}
	}
	return false
}

func isRustTargetPath(cleanedPath string, slashLowerPath string) bool {
	if strings.Contains(slashLowerPath, "src-tauri") || strings.Contains(slashLowerPath, "rust") {
		return true
	}
	parentDir := filepath.Dir(cleanedPath)
	if _, err := os.Stat(filepath.Join(parentDir, "Cargo.toml")); err == nil {
		return true
	}
	return false
}

type walkTracker struct {
	maxDepth      int
	maxEntries    int
	visitedCount  int
	totalBytes    int64
	totalFiles    int64
	recordedDirs  []DirUsage
}

// ScanDirectory performs a fast bounded walk aggregating directory sizes and app attribution.
func ScanDirectory(ctx context.Context, rootPath string, maxDepth int, maxEntries int) (ScanReport, error) {
	if strings.TrimSpace(rootPath) == "" {
		return ScanReport{}, fmt.Errorf("rootPath is required")
	}
	cleanedRoot := filepath.Clean(rootPath)
	rootInfo, err := os.Lstat(cleanedRoot)
	if err != nil {
		return ScanReport{}, fmt.Errorf("stat root path: %w", err)
	}
	if !rootInfo.IsDir() {
		return ScanReport{}, fmt.Errorf("root path is not a directory: %s", cleanedRoot)
	}

	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}

	startedAt := time.Now()
	tracker := &walkTracker{
		maxDepth:     maxDepth,
		maxEntries:   maxEntries,
		recordedDirs: make([]DirUsage, 0, 128),
	}

	_, _ = tracker.walkDir(ctx, cleanedRoot, 0)

	report := buildScanReport(cleanedRoot, tracker.totalBytes, tracker.totalFiles, startedAt, tracker.recordedDirs)
	return report, nil
}

func (w *walkTracker) walkDir(ctx context.Context, dirPath string, depth int) (int64, int64) {
	if ctx.Err() != nil || w.visitedCount >= w.maxEntries {
		return 0, 0
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return 0, 0
	}

	var dirBytes int64
	var dirFiles int64

	for _, entry := range entries {
		if ctx.Err() != nil || w.visitedCount >= w.maxEntries {
			break
		}
		w.visitedCount++

		// Skip symlinks and Windows reparse points to avoid cycles.
		entryType := entry.Type()
		if entryType&os.ModeSymlink != 0 || entryType&os.ModeIrregular != 0 {
			continue
		}

		childPath := filepath.Join(dirPath, entry.Name())
		if entry.IsDir() {
			subBytes, subFiles := w.walkDir(ctx, childPath, depth+1)
			dirBytes += subBytes
			dirFiles += subFiles
			continue
		}

		fileInfo, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		fileSize := fileInfo.Size()
		dirBytes += fileSize
		dirFiles++
		w.totalBytes += fileSize
		w.totalFiles++
	}

	if depth > 0 && depth <= w.maxDepth && (dirBytes > 0 || dirFiles > 0) {
		category, safety := ClassifyPath(dirPath)
		w.recordedDirs = append(w.recordedDirs, DirUsage{
			Path:        dirPath,
			Name:        filepath.Base(dirPath),
			Bytes:       dirBytes,
			FileCount:   dirFiles,
			AppCategory: category,
			Safety:      safety,
			Depth:       depth,
		})
	}

	return dirBytes, dirFiles
}

func buildScanReport(rootPath string, totalBytes int64, totalFiles int64, startedAt time.Time, dirs []DirUsage) ScanReport {
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].Bytes > dirs[j].Bytes
	})

	topLimit := 30
	if len(dirs) < topLimit {
		topLimit = len(dirs)
	}
	topDirs := make([]DirUsage, topLimit)
	copy(topDirs, dirs[:topLimit])

	// Group by app category, preferring leaf/attributed directories to avoid double counting parent+child.
	categoryMap := make(map[string]*AppUsage)
	candidates := make([]DirUsage, 0, 32)
	for _, dirEntry := range dirs {
		if dirEntry.Safety == SafetySafe || (dirEntry.Safety == SafetyCaution && dirEntry.AppCategory != "General Files") {
			if len(candidates) < 25 {
				candidates = append(candidates, dirEntry)
			}
		}

		agg, exists := categoryMap[dirEntry.AppCategory]
		if !exists {
			agg = &AppUsage{
				Category: dirEntry.AppCategory,
				Safety:   dirEntry.Safety,
				TopPaths: make([]string, 0, 5),
			}
			categoryMap[dirEntry.AppCategory] = agg
		}
		agg.Bytes += dirEntry.Bytes
		agg.FileCount += dirEntry.FileCount
		agg.DirCount++
		if len(agg.TopPaths) < 5 {
			agg.TopPaths = append(agg.TopPaths, dirEntry.Path)
		}
	}

	appBreakdown := make([]AppUsage, 0, len(categoryMap))
	for _, agg := range categoryMap {
		appBreakdown = append(appBreakdown, *agg)
	}
	sort.Slice(appBreakdown, func(i, j int) bool {
		return appBreakdown[i].Bytes > appBreakdown[j].Bytes
	})

	return ScanReport{
		RootPath:        rootPath,
		ScannedBytes:    totalBytes,
		ScannedFiles:    totalFiles,
		DurationMs:      time.Since(startedAt).Milliseconds(),
		TopDirectories:  topDirs,
		AppBreakdown:    appBreakdown,
		CleanCandidates: candidates,
	}
}

// QuickAudit scans known OS and developer cache hotspots in parallel within a strict timeout.
func QuickAudit(ctx context.Context) (ScanReport, error) {
	startedAt := time.Now()
	auditCtx, cancel := context.WithTimeout(ctx, quickAuditTimeout)
	defer cancel()

	hotspots := resolveHotspotPaths()
	var waitGroup sync.WaitGroup
	var collectMu sync.Mutex

	var totalBytes int64
	var totalFiles int64
	collectedDirs := make([]DirUsage, 0, len(hotspots))

	for _, spotPath := range hotspots {
		info, err := os.Stat(spotPath)
		if err != nil || !info.IsDir() {
			continue
		}

		waitGroup.Add(1)
		go func(target string) {
			defer waitGroup.Done()
			tracker := &walkTracker{
				maxDepth:     1,
				maxEntries:   8000,
				recordedDirs: make([]DirUsage, 0, 8),
			}
			spotBytes, spotFiles := tracker.walkDir(auditCtx, target, 0)
			if spotBytes == 0 && spotFiles == 0 {
				return
			}
			category, safety := ClassifyPath(target)
			collectMu.Lock()
			totalBytes += spotBytes
			totalFiles += spotFiles
			collectedDirs = append(collectedDirs, DirUsage{
				Path:        target,
				Name:        filepath.Base(target),
				Bytes:       spotBytes,
				FileCount:   spotFiles,
				AppCategory: category,
				Safety:      safety,
				Depth:       1,
			})
			collectMu.Unlock()
		}(spotPath)
	}

	waitGroup.Wait()

	return buildScanReport("quick-audit", totalBytes, totalFiles, startedAt, collectedDirs), nil
}

func resolveHotspotPaths() []string {
	localAppData := os.Getenv("LOCALAPPDATA")
	userProfile := os.Getenv("USERPROFILE")
	if userProfile == "" {
		homeDir, _ := os.UserHomeDir()
		userProfile = homeDir
	}
	tempEnv := os.Getenv("TEMP")

	rawPaths := make([]string, 0, 12)
	if localAppData != "" {
		rawPaths = append(rawPaths,
			filepath.Join(localAppData, "Temp"),
			filepath.Join(localAppData, "npm-cache"),
			filepath.Join(localAppData, "go-build"),
			filepath.Join(localAppData, "Docker"),
			filepath.Join(localAppData, "Google", "Chrome", "User Data", "Default", "Cache"),
			filepath.Join(localAppData, "Microsoft", "Edge", "User Data", "Default", "Cache"),
		)
	}
	if tempEnv != "" {
		rawPaths = append(rawPaths, tempEnv)
	}
	if userProfile != "" {
		rawPaths = append(rawPaths,
			filepath.Join(userProfile, ".bun", "install", "cache"),
			filepath.Join(userProfile, ".cache"),
			filepath.Join(userProfile, "Downloads"),
		)
	}

	seen := make(map[string]struct{}, len(rawPaths))
	deduped := make([]string, 0, len(rawPaths))
	for _, candidate := range rawPaths {
		cleaned := filepath.Clean(candidate)
		key := strings.ToLower(cleaned)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, cleaned)
	}
	return deduped
}
