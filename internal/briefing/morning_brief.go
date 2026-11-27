package briefing

import (
	"fmt"
	"strings"
	"time"
)

// WIBOffset is UTC+7 (Western Indonesian Time).
var WIBZone = time.FixedZone("WIB", 7*3600)

// MorningMetrics contains the operational telemetry summarized in the 07:00 WIB Executive Brief.
type MorningMetrics struct {
	Date            time.Time
	RecipientName   string
	MRRIDR          int64
	MRRGrowthPct    float64
	ActiveCustomers int
	OverdueInvoices int
	NASHealthyNodes int
	NASTotalNodes   int
	NASCPUUsagePct  float64
	DiskUsedBytes   int64
	DiskTotalBytes  int64
	CIPassingRepos  int
	CITotalRepos    int
	FailingCIRepos  []string
	OpenP1Incidents int
}

// ExecutiveBrief is the generated 07:00 WIB Morning Executive Brief report.
type ExecutiveBrief struct {
	ScheduleTimeWIB string   `json:"scheduleTimeWib"`
	OverallStatus   string   `json:"overallStatus"`
	MRRFormatted    string   `json:"mrrFormatted"`
	NASStatusLine   string   `json:"nasStatusLine"`
	DiskUsagePct    float64  `json:"diskUsagePct"`
	DiskStatusLine  string   `json:"diskStatusLine"`
	CIStatusLine    string   `json:"ciStatusLine"`
	Alerts          []string `json:"alerts,omitempty"`
	Markdown        string   `json:"markdown"`
}

// Next0700WIB returns the next 07:00 WIB schedule timestamp on or after from.
func Next0700WIB(from time.Time) time.Time {
	inWIB := from.In(WIBZone)
	candidate := time.Date(inWIB.Year(), inWIB.Month(), inWIB.Day(), 7, 0, 0, 0, WIBZone)
	if !candidate.After(inWIB) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate
}

// GenerateMorningBrief compiles MRR, NAS health, disk capacity, and CI status into the 07:00 WIB brief.
func GenerateMorningBrief(m MorningMetrics) ExecutiveBrief {
	refDate := m.Date
	if refDate.IsZero() {
		refDate = time.Now()
	}
	wibDate := refDate.In(WIBZone)
	scheduled := time.Date(wibDate.Year(), wibDate.Month(), wibDate.Day(), 7, 0, 0, 0, WIBZone)

	var diskPct float64
	if m.DiskTotalBytes > 0 && m.DiskUsedBytes >= 0 {
		diskPct = (float64(m.DiskUsedBytes) / float64(m.DiskTotalBytes)) * 100.0
	}

	var alerts []string
	overall := "GREEN"

	if m.NASTotalNodes > 0 && m.NASHealthyNodes < m.NASTotalNodes {
		overall = "AMBER"
		alerts = append(alerts, fmt.Sprintf("NAS degraded: %d/%d nodes healthy", m.NASHealthyNodes, m.NASTotalNodes))
	}
	if diskPct >= 85.0 {
		overall = "AMBER"
		alerts = append(alerts, fmt.Sprintf("Disk usage critical: %.1f%% used", diskPct))
	}
	if len(m.FailingCIRepos) > 0 || (m.CITotalRepos > 0 && m.CIPassingRepos < m.CITotalRepos) {
		overall = "AMBER"
		if len(m.FailingCIRepos) > 0 {
			alerts = append(alerts, "CI failing on: "+strings.Join(m.FailingCIRepos, ", "))
		} else {
			alerts = append(alerts, fmt.Sprintf("CI degraded: %d/%d repos green", m.CIPassingRepos, m.CITotalRepos))
		}
	}
	if m.OpenP1Incidents > 0 {
		overall = "RED"
		alerts = append(alerts, fmt.Sprintf("%d open P1 incident(s) require immediate triage", m.OpenP1Incidents))
	}

	mrrFormatted := formatRupiah(m.MRRIDR)
	nasLine := fmt.Sprintf("%d/%d routers online (CPU %.1f%%)", m.NASHealthyNodes, m.NASTotalNodes, m.NASCPUUsagePct)
	diskLine := fmt.Sprintf("%.1f%% used (%s / %s)", diskPct, formatGiB(m.DiskUsedBytes), formatGiB(m.DiskTotalBytes))
	ciLine := fmt.Sprintf("%d/%d fleet pipelines green", m.CIPassingRepos, m.CITotalRepos)
	if len(m.FailingCIRepos) > 0 {
		ciLine += " (failing: " + strings.Join(m.FailingCIRepos, ", ") + ")"
	}

	var md strings.Builder
	md.WriteString(fmt.Sprintf("# MORNING EXECUTIVE BRIEF — %s (07:00 WIB) [%s]\n\n", scheduled.Format("2006-01-02"), overall))
	md.WriteString(fmt.Sprintf("- **MRR & Billing**: %s (%+.1f%% MoM) | %d pelanggan aktif | %d invoice jatuh tempo\n",
		mrrFormatted, m.MRRGrowthPct, m.ActiveCustomers, m.OverdueInvoices))
	md.WriteString(fmt.Sprintf("- **NAS Health**: %s\n", nasLine))
	md.WriteString(fmt.Sprintf("- **Storage / Disk**: %s\n", diskLine))
	md.WriteString(fmt.Sprintf("- **CI / Release Fleet**: %s\n", ciLine))
	if len(alerts) > 0 {
		md.WriteString("\n## Action Alerts\n")
		for _, a := range alerts {
			md.WriteString("- [!] " + a + "\n")
		}
	}

	return ExecutiveBrief{
		ScheduleTimeWIB: scheduled.Format(time.RFC3339),
		OverallStatus:   overall,
		MRRFormatted:    mrrFormatted,
		NASStatusLine:   nasLine,
		DiskUsagePct:    diskPct,
		DiskStatusLine:  diskLine,
		CIStatusLine:    ciLine,
		Alerts:          alerts,
		Markdown:        strings.TrimSpace(md.String()),
	}
}

func formatRupiah(amount int64) string {
	neg := amount < 0
	if neg {
		amount = -amount
	}
	raw := fmt.Sprintf("%d", amount)
	var parts []string
	for len(raw) > 3 {
		parts = append([]string{raw[len(raw)-3:]}, parts...)
		raw = raw[:len(raw)-3]
	}
	parts = append([]string{raw}, parts...)
	out := "Rp " + strings.Join(parts, ".")
	if neg {
		return "-" + out
	}
	return out
}

func formatGiB(bytes int64) string {
	const gib = 1024 * 1024 * 1024
	return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(gib))
}
