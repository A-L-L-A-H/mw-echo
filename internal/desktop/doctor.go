package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	checkStatusOK   = "ok"
	checkStatusWarn = "warn"
	checkStatusFail = "fail"
)

// DoctorCheck represents a single desktop runtime diagnostic check.
type DoctorCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // "ok", "warn", "fail"
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// DoctorReport aggregates all desktop diagnostic checks and a redacted Markdown support bundle.
type DoctorReport struct {
	Healthy               bool          `json:"healthy"`
	GeneratedAt           string        `json:"generatedAt"`
	Platform              string        `json:"platform"`
	Arch                  string        `json:"arch"`
	Checks                []DoctorCheck `json:"checks"`
	SupportReportMarkdown string        `json:"supportReportMarkdown"`
}

// DoctorRepairResult captures self-repair actions performed and the post-repair DoctorReport.
type DoctorRepairResult struct {
	Repaired        bool         `json:"repaired"`
	ActionsTaken    []string     `json:"actionsTaken"`
	PrunedTempFiles int          `json:"prunedTempFiles"`
	Report          DoctorReport `json:"report"`
}

// RunDoctor executes the 5 desktop health checks and builds a redacted Markdown support report.
func (m *Manager) RunDoctor(ctx context.Context) (DoctorReport, error) {
	checks := []DoctorCheck{
		m.checkSQLiteIntegrity(ctx),
		m.checkDataDirWritable(),
		m.checkLogsDirWritable(),
		m.checkEasyTierBinary(),
		m.checkRuntimeMemory(),
	}

	healthy := true
	for _, check := range checks {
		if check.Status == checkStatusFail {
			healthy = false
			break
		}
	}

	generatedAt := time.Now().UTC().Format(time.RFC3339)
	markdown := m.formatSupportReportMarkdown(checks, generatedAt)

	return DoctorReport{
		Healthy:               healthy,
		GeneratedAt:           generatedAt,
		Platform:              runtime.GOOS,
		Arch:                  runtime.GOARCH,
		Checks:                checks,
		SupportReportMarkdown: markdown,
	}, nil
}

// RunRepair creates missing logs/ and tmp/ directories, prunes *.tmp files, runs WAL checkpoint, logs the repair, and returns a refreshed DoctorReport.
func (m *Manager) RunRepair(ctx context.Context) (DoctorRepairResult, error) {
	actionsTaken := make([]string, 0, 4)
	prunedTempFiles := 0

	logsDir := filepath.Join(m.dataDir, "logs")
	if _, statErr := os.Stat(logsDir); os.IsNotExist(statErr) {
		if mkErr := os.MkdirAll(logsDir, 0o755); mkErr != nil {
			return DoctorRepairResult{}, fmt.Errorf("create logs dir: %w", mkErr)
		}
		actionsTaken = append(actionsTaken, fmt.Sprintf("created_logs_dir:%s", logsDir))
	}

	tmpDir := filepath.Join(m.dataDir, "tmp")
	if _, statErr := os.Stat(tmpDir); os.IsNotExist(statErr) {
		if mkErr := os.MkdirAll(tmpDir, 0o755); mkErr != nil {
			return DoctorRepairResult{}, fmt.Errorf("create tmp dir: %w", mkErr)
		}
		actionsTaken = append(actionsTaken, fmt.Sprintf("created_tmp_dir:%s", tmpDir))
	} else if statErr == nil {
		dirEntries, readErr := os.ReadDir(tmpDir)
		if readErr != nil {
			return DoctorRepairResult{}, fmt.Errorf("read tmp dir: %w", readErr)
		}
		for _, entry := range dirEntries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".stale") {
				if rmErr := os.Remove(filepath.Join(tmpDir, name)); rmErr == nil {
					prunedTempFiles++
				}
			}
		}
		if prunedTempFiles > 0 {
			actionsTaken = append(actionsTaken, fmt.Sprintf("pruned_temp_files:%d", prunedTempFiles))
		}
	}

	if _, walErr := m.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); walErr != nil {
		return DoctorRepairResult{}, fmt.Errorf("wal_checkpoint: %w", walErr)
	}
	actionsTaken = append(actionsTaken, "wal_checkpoint_truncate")

	summaryMsg := fmt.Sprintf("Desktop doctor repair completed (actions=%d, pruned=%d)", len(actionsTaken), prunedTempFiles)
	if _, logErr := m.Log(ctx, "info", "doctor", summaryMsg, strings.Join(actionsTaken, ", ")); logErr != nil {
		return DoctorRepairResult{}, fmt.Errorf("log repair event: %w", logErr)
	}

	refreshedReport, reportErr := m.RunDoctor(ctx)
	if reportErr != nil {
		return DoctorRepairResult{}, reportErr
	}

	return DoctorRepairResult{
		Repaired:        true,
		ActionsTaken:    actionsTaken,
		PrunedTempFiles: prunedTempFiles,
		Report:          refreshedReport,
	}, nil
}

func (m *Manager) checkSQLiteIntegrity(ctx context.Context) DoctorCheck {
	var integrityResult string
	if err := m.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrityResult); err != nil {
		return DoctorCheck{
			ID:      "sqlite_integrity",
			Status:  checkStatusFail,
			Message: "SQLite integrity_check failed",
			Detail:  err.Error(),
		}
	}
	if strings.EqualFold(strings.TrimSpace(integrityResult), "ok") {
		return DoctorCheck{
			ID:      "sqlite_integrity",
			Status:  checkStatusOK,
			Message: "SQLite integrity_check passed",
		}
	}
	return DoctorCheck{
		ID:      "sqlite_integrity",
		Status:  checkStatusFail,
		Message: "SQLite integrity issue detected",
		Detail:  integrityResult,
	}
}

func (m *Manager) checkDataDirWritable() DoctorCheck {
	return checkDirectoryWritable("data_dir_writable", "Desktop data directory", m.dataDir, false)
}

func (m *Manager) checkLogsDirWritable() DoctorCheck {
	logsDir := filepath.Join(m.dataDir, "logs")
	return checkDirectoryWritable("logs_dir_writable", "Desktop logs directory", logsDir, true)
}

func checkDirectoryWritable(checkID, label, dirPath string, warnIfMissing bool) DoctorCheck {
	info, statErr := os.Stat(dirPath)
	if os.IsNotExist(statErr) {
		status := checkStatusFail
		if warnIfMissing {
			status = checkStatusWarn
		}
		return DoctorCheck{
			ID:      checkID,
			Status:  status,
			Message: label + " does not exist yet",
			Detail:  dirPath,
		}
	}
	if statErr != nil {
		return DoctorCheck{
			ID:      checkID,
			Status:  checkStatusFail,
			Message: label + " is inaccessible",
			Detail:  statErr.Error(),
		}
	}
	if !info.IsDir() {
		return DoctorCheck{
			ID:      checkID,
			Status:  checkStatusFail,
			Message: label + " path is not a directory",
			Detail:  dirPath,
		}
	}

	probePath := filepath.Join(dirPath, fmt.Sprintf(".doctor-probe-%d-%d.tmp", os.Getpid(), time.Now().UnixNano()))
	if writeErr := os.WriteFile(probePath, []byte("ok"), 0o600); writeErr != nil {
		return DoctorCheck{
			ID:      checkID,
			Status:  checkStatusFail,
			Message: label + " is not writable",
			Detail:  writeErr.Error(),
		}
	}
	_ = os.Remove(probePath)

	return DoctorCheck{
		ID:      checkID,
		Status:  checkStatusOK,
		Message: label + " is writable",
		Detail:  dirPath,
	}
}

func (m *Manager) checkEasyTierBinary() DoctorCheck {
	binPath := findEasyTierBinary(m.dataDir, m.easytierBinPath)
	if binPath == "" {
		return DoctorCheck{
			ID:      "easytier_binary",
			Status:  checkStatusWarn,
			Message: "EasyTier mesh binary not installed (optional for Workspace mode)",
		}
	}
	return DoctorCheck{
		ID:      "easytier_binary",
		Status:  checkStatusOK,
		Message: "EasyTier mesh binary available",
		Detail:  binPath,
	}
}

func findEasyTierBinary(dataDir, explicitPath string) string {
	isRegularFile := func(candidate string) bool {
		if candidate == "" {
			return false
		}
		info, err := os.Stat(candidate)
		return err == nil && !info.IsDir()
	}

	if trimmed := strings.TrimSpace(explicitPath); isRegularFile(trimmed) {
		return trimmed
	}
	if envBin := strings.TrimSpace(os.Getenv("MW_EASYTIER_BIN")); isRegularFile(envBin) {
		return envBin
	}

	binName := "easytier-core"
	if runtime.GOOS == "windows" {
		binName = "easytier-core.exe"
	}
	dataBin := filepath.Join(dataDir, "bin", binName)
	if isRegularFile(dataBin) {
		return dataBin
	}

	if pathBin, lookErr := exec.LookPath(binName); lookErr == nil && isRegularFile(pathBin) {
		return pathBin
	}
	return ""
}

func (m *Manager) checkRuntimeMemory() DoctorCheck {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	allocMB := float64(memStats.Alloc) / (1024 * 1024)
	sysMB := float64(memStats.Sys) / (1024 * 1024)
	goroutines := runtime.NumGoroutine()

	return DoctorCheck{
		ID:      "runtime_memory",
		Status:  checkStatusOK,
		Message: fmt.Sprintf("Go runtime healthy (heap=%.2f MiB, sys=%.2f MiB, goroutines=%d)", allocMB, sysMB, goroutines),
		Detail:  fmt.Sprintf("allocBytes=%d sysBytes=%d numGoroutine=%d", memStats.Alloc, memStats.Sys, goroutines),
	}
}

func (m *Manager) formatSupportReportMarkdown(checks []DoctorCheck, generatedAt string) string {
	lines := []string{
		"# MoonWitness Desktop Support Report",
		"",
		fmt.Sprintf("- Generated At: `%s`", generatedAt),
		fmt.Sprintf("- Platform: `%s` (`%s`)", runtime.GOOS, runtime.GOARCH),
		fmt.Sprintf("- Go Runtime: `%s`", runtime.Version()),
		"",
		"## Health Checks",
	}

	for _, check := range checks {
		redactedMsg, _ := RedactSensitiveText(check.Message, m.knownSecrets...)
		line := fmt.Sprintf("- [%s] **%s**: %s", strings.ToUpper(check.Status), check.ID, redactedMsg)
		if check.Detail != "" {
			redactedDetail, _ := RedactSensitiveText(check.Detail, m.knownSecrets...)
			line += fmt.Sprintf(" (%s)", redactedDetail)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}
