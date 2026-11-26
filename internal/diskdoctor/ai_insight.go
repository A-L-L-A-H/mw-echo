package diskdoctor

import (
	"context"
	"fmt"
	"strings"
)

// InsightResult contains an engineering diagnosis of disk pressure and actionable cleanup advice.
type InsightResult struct {
	Source                    string   `json:"source"`
	Summary                   string   `json:"summary"`
	RootCauses                []string `json:"rootCauses"`
	Recommendations           []string `json:"recommendations"`
	EstimatedRecoverableBytes int64    `json:"estimatedRecoverableBytes"`
}

// AnalyzeWithAI produces a disk health diagnosis using the configured AI callback,
// falling back to a deterministic engineering diagnosis if AI is unconfigured or fails.
func (m *Manager) AnalyzeWithAI(ctx context.Context, drives []DriveInfo, report ScanReport) (InsightResult, error) {
	deterministic := buildDeterministicInsight(drives, report)

	if m == nil || m.opts.AIAnalyze == nil {
		return deterministic, nil
	}

	systemPrompt := "You are a senior systems engineer diagnosing disk space pressure. Be concise, concrete, and cite exact paths and byte sizes."
	userPrompt := formatTelemetryPrompt(drives, report, deterministic.EstimatedRecoverableBytes)

	aiText, err := m.opts.AIAnalyze(ctx, systemPrompt, userPrompt)
	if err != nil || strings.TrimSpace(aiText) == "" {
		return deterministic, nil
	}

	return InsightResult{
		Source:                    "ai_gateway",
		Summary:                   strings.TrimSpace(aiText),
		RootCauses:                deterministic.RootCauses,
		Recommendations:           deterministic.Recommendations,
		EstimatedRecoverableBytes: deterministic.EstimatedRecoverableBytes,
	}, nil
}

func buildDeterministicInsight(drives []DriveInfo, report ScanReport) InsightResult {
	var recoverableBytes int64
	for _, candidate := range report.CleanCandidates {
		if candidate.Safety == SafetySafe {
			recoverableBytes += candidate.Bytes
		}
	}

	rootCauses := make([]string, 0, 5)
	recommendations := make([]string, 0, 5)

	criticalDrives := make([]string, 0, 2)
	for _, drive := range drives {
		if drive.Status == "critical" {
			criticalDrives = append(criticalDrives, fmt.Sprintf("%s (%.1f%% used, %s free)", drive.Drive, drive.UsedPct, formatHumanBytes(int64(drive.FreeBytes))))
		}
	}

	if len(criticalDrives) > 0 {
		rootCauses = append(rootCauses, fmt.Sprintf("Critical volume pressure detected on: %s", strings.Join(criticalDrives, ", ")))
	}

	for idx, app := range report.AppBreakdown {
		if idx >= 3 {
			break
		}
		rootCauses = append(rootCauses, fmt.Sprintf("%s consuming %s across %d directories (%s)", app.Category, formatHumanBytes(app.Bytes), app.DirCount, app.Safety))
	}

	for idx, candidate := range report.CleanCandidates {
		if idx >= 3 {
			break
		}
		recommendations = append(recommendations, fmt.Sprintf("Clean [%s] %s (%s) — %s", candidate.Safety, candidate.Path, formatHumanBytes(candidate.Bytes), candidate.AppCategory))
	}

	if len(recommendations) == 0 {
		recommendations = append(recommendations, "No high-impact safe cache candidates found in the scanned scope; run a deeper directory scan on high-usage volumes.")
	}

	summary := fmt.Sprintf(
		"Scanned %s (%d files) in %dms across %d volumes. Estimated safe recoverable space: %s.",
		formatHumanBytes(report.ScannedBytes),
		report.ScannedFiles,
		report.DurationMs,
		len(drives),
		formatHumanBytes(recoverableBytes),
	)
	if len(criticalDrives) > 0 {
		summary = fmt.Sprintf("CRITICAL: %s. %s", strings.Join(criticalDrives, ", "), summary)
	}

	return InsightResult{
		Source:                    "deterministic",
		Summary:                   summary,
		RootCauses:                rootCauses,
		Recommendations:           recommendations,
		EstimatedRecoverableBytes: recoverableBytes,
	}
}

func formatTelemetryPrompt(drives []DriveInfo, report ScanReport, recoverableBytes int64) string {
	var builder strings.Builder
	builder.WriteString("Drives:\n")
	for _, drive := range drives {
		builder.WriteString(fmt.Sprintf("- %s [%s] %.1f%% used (%s free / %s total)\n",
			drive.Drive, drive.Status, drive.UsedPct,
			formatHumanBytes(int64(drive.FreeBytes)),
			formatHumanBytes(int64(drive.TotalBytes)),
		))
	}

	builder.WriteString(fmt.Sprintf("\nScan Root: %s (%s, %d files, safe recoverable: %s)\n",
		report.RootPath, formatHumanBytes(report.ScannedBytes), report.ScannedFiles, formatHumanBytes(recoverableBytes)))

	builder.WriteString("\nApp Breakdown:\n")
	for idx, app := range report.AppBreakdown {
		if idx >= 6 {
			break
		}
		builder.WriteString(fmt.Sprintf("- %s (%s): %s\n", app.Category, app.Safety, formatHumanBytes(app.Bytes)))
	}

	builder.WriteString("\nTop Clean Candidates:\n")
	for idx, candidate := range report.CleanCandidates {
		if idx >= 6 {
			break
		}
		builder.WriteString(fmt.Sprintf("- %s [%s/%s]: %s\n", candidate.Path, candidate.AppCategory, candidate.Safety, formatHumanBytes(candidate.Bytes)))
	}

	return builder.String()
}

func formatHumanBytes(byteCount int64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case byteCount >= gib:
		return fmt.Sprintf("%.2f GiB", float64(byteCount)/float64(gib))
	case byteCount >= mib:
		return fmt.Sprintf("%.1f MiB", float64(byteCount)/float64(mib))
	case byteCount >= kib:
		return fmt.Sprintf("%.1f KiB", float64(byteCount)/float64(kib))
	default:
		return fmt.Sprintf("%d B", byteCount)
	}
}
