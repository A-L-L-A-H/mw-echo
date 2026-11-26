package gamification_test

import (
	"context"
	"testing"

	"github.com/A-L-L-A-H/mw-echo/internal/gamification"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
)

func TestGamificationXPLevelComboBossAndQuests(t *testing.T) {
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	defer func() { _ = hub.Close() }()

	ctx := context.Background()
	_, _ = hub.UpsertTask(ctx, store.TaskRecord{Code: "BOSS-1", Project: "riot-app", Milestone: "Act 1", Title: "Defeat bug", Priority: "p0", Status: "done"})
	_, _ = hub.UpsertTask(ctx, store.TaskRecord{Code: "BOSS-2", Project: "riot-app", Milestone: "Act 1", Title: "Ship feature", Priority: "p1", Status: "todo"})

	eng, err := gamification.NewEngine(hub, gamification.TelemetryHook{
		GetRTKTokensSaved: func(context.Context) int64 { return 1500 },
		GetWAConnected:    func(context.Context) int { return 1 },
		GetCoderTurns:     func(context.Context) int { return 2 },
	})
	if err != nil {
		t.Fatalf("init engine: %v", err)
	}

	ev1, err := eng.Award(ctx, "task_done", "Cleared BOSS-1", 250, "BOSS-1", true)
	if err != nil || ev1.AwardedXP != 250 {
		t.Fatalf("first award failed: %+v err=%v", ev1, err)
	}
	ev2, err := eng.Award(ctx, "task_done", "Cleared rapid task", 100, "BOSS-2", true)
	if err != nil || ev2.Multiplier <= 1.0 {
		t.Fatalf("expected combo multiplier > 1.0, got %+v err=%v", ev2, err)
	}

	profile, err := eng.Profile(ctx)
	if err != nil {
		t.Fatalf("profile failed: %v", err)
	}
	if profile.Level < 2 || len(profile.BossBattles) != 1 || profile.BossBattles[0].CurrentHP != 100 {
		t.Fatalf("unexpected profile/boss state: %+v", profile)
	}

	claimed, err := eng.ClaimQuest(ctx, "bounty_hunter")
	if err != nil || claimed.AwardedXP <= 0 {
		t.Fatalf("expected bounty_hunter quest claimable, got err=%v", err)
	}
	if _, err := eng.ClaimQuest(ctx, "bounty_hunter"); err == nil {
		t.Fatal("expected duplicate daily quest claim to fail")
	}
}
