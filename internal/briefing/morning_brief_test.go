package briefing

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateMorningBrief_AllMetricsAndAlerts(t *testing.T) {
	ref := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC) // 07:00 WIB
	brief := GenerateMorningBrief(MorningMetrics{
		Date:            ref,
		MRRIDR:          148_500_000,
		MRRGrowthPct:    12.4,
		ActiveCustomers: 412,
		OverdueInvoices: 7,
		NASHealthyNodes: 5,
		NASTotalNodes:   6,
		NASCPUUsagePct:  34.2,
		DiskUsedBytes:   900 * 1024 * 1024 * 1024,
		DiskTotalBytes:  1000 * 1024 * 1024 * 1024,
		CIPassingRepos:  11,
		CITotalRepos:    12,
		FailingCIRepos:  []string{"mw-billing"},
		OpenP1Incidents: 0,
	})

	if brief.OverallStatus != "AMBER" {
		t.Fatalf("OverallStatus = %s, want AMBER", brief.OverallStatus)
	}
	if brief.MRRFormatted != "Rp 148.500.000" {
		t.Fatalf("MRRFormatted = %s, want Rp 148.500.000", brief.MRRFormatted)
	}
	if !strings.Contains(brief.ScheduleTimeWIB, "T07:00:00+07:00") {
		t.Fatalf("expected 07:00 WIB schedule timestamp, got %s", brief.ScheduleTimeWIB)
	}
	if !strings.Contains(brief.Markdown, "5/6 routers online") {
		t.Fatalf("Markdown missing NAS health summary:\n%s", brief.Markdown)
	}
	if !strings.Contains(brief.Markdown, "90.0% used") {
		t.Fatalf("Markdown missing disk summary:\n%s", brief.Markdown)
	}
	if !strings.Contains(brief.Markdown, "mw-billing") {
		t.Fatalf("Markdown missing CI failing repo:\n%s", brief.Markdown)
	}
	if len(brief.Alerts) != 3 {
		t.Fatalf("expected 3 action alerts, got %v", brief.Alerts)
	}
}

func TestNext0700WIB(t *testing.T) {
	// 06:30 WIB -> same day 07:00 WIB
	before := time.Date(2026, 11, 27, 6, 30, 0, 0, WIBZone)
	next := Next0700WIB(before)
	if next.Day() != 27 || next.Hour() != 7 {
		t.Fatalf("expected 2026-11-27 07:00 WIB, got %v", next)
	}

	// 07:15 WIB -> next day 07:00 WIB
	after := time.Date(2026, 11, 27, 7, 15, 0, 0, WIBZone)
	nextDay := Next0700WIB(after)
	if nextDay.Day() != 28 || nextDay.Hour() != 7 {
		t.Fatalf("expected 2026-11-28 07:00 WIB, got %v", nextDay)
	}
}
