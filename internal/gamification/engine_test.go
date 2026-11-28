package gamification_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/gamification"
	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

func openTestEngine(t *testing.T, hook gamification.TelemetryHook) (*store.HubStore, *gamification.Engine) {
	t.Helper()
	hub, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open hub: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	eng, err := gamification.NewEngine(hub, hook)
	if err != nil {
		t.Fatalf("init engine: %v", err)
	}
	return hub, eng
}

func TestXPLedgerFormulaAndLevelProgression(t *testing.T) {
	for _, lvl := range []int{1, 2, 5, 10, 30, 50} {
		want := int64(math.Floor(120 * math.Pow(float64(lvl), 1.45)))
		if got := gamification.XPForNextLevel(lvl); got != want {
			t.Fatalf("XPForNextLevel(%d) = %d, want %d", lvl, got, want)
		}
	}

	lvl1Span := gamification.XPForNextLevel(1)
	lvl, title, floorXP, nextXP, _ := gamification.ComputeLevel(lvl1Span - 1)
	if lvl != 1 || floorXP != 0 || nextXP != lvl1Span || title != "ROOKIE RUNNER" {
		t.Fatalf("unexpected level 1 boundary: lvl=%d title=%q floor=%d next=%d", lvl, title, floorXP, nextXP)
	}

	lvl2Span := gamification.XPForNextLevel(2)
	lvl, _, floorXP, nextXP, _ = gamification.ComputeLevel(lvl1Span)
	if lvl != 2 || floorXP != lvl1Span || nextXP != lvl1Span+lvl2Span {
		t.Fatalf("unexpected level 2 transition: lvl=%d floor=%d next=%d", lvl, floorXP, nextXP)
	}
}

func TestArcadeComboMultipliersAnd30MinWindow(t *testing.T) {
	_, eng := openTestEngine(t, gamification.TelemetryHook{})
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	eng.SetClock(func() time.Time { return now })

	ctx := context.Background()
	wantMults := []float64{1, 2, 4, 8, 8}
	for i, want := range wantMults {
		ev, err := eng.Award(ctx, "task_done", "Combo action", 20, "C", true)
		if err != nil {
			t.Fatalf("award %d failed: %v", i+1, err)
		}
		if ev.Multiplier != want {
			t.Fatalf("action %d multiplier = %.1f, want %.1f", i+1, ev.Multiplier, want)
		}
		now = now.Add(5 * time.Minute)
	}

	// Advancing beyond the 30-minute arcade window resets combo back to x1.
	now = now.Add(31 * time.Minute)
	resetEv, err := eng.Award(ctx, "task_done", "Post-cooldown action", 20, "C-RESET", true)
	if err != nil {
		t.Fatalf("post-cooldown award failed: %v", err)
	}
	if resetEv.Multiplier != 1.0 || resetEv.AwardedXP != 20 {
		t.Fatalf("expected combo reset to x1 after 30m window, got %+v", resetEv)
	}
}

func TestDailyStreakCap500BonusXPPer24Hours(t *testing.T) {
	_, eng := openTestEngine(t, gamification.TelemetryHook{})
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	eng.SetClock(func() time.Time { return now })

	ctx := context.Background()

	// Action 1: x1, 0 bonus.
	ev1, err := eng.Award(ctx, "task_done", "Step 1", 100, "D1", true)
	if err != nil || ev1.AwardedXP != 100 {
		t.Fatalf("step 1 failed: %+v err=%v", ev1, err)
	}
	// Action 2 (1 min later): x2 -> base 100 + 100 bonus (total bonus used = 100).
	now = now.Add(1 * time.Minute)
	ev2, err := eng.Award(ctx, "task_done", "Step 2", 100, "D2", true)
	if err != nil || ev2.AwardedXP != 200 {
		t.Fatalf("step 2 failed: %+v err=%v", ev2, err)
	}
	// Action 3 (1 min later): x4 -> raw bonus 300 -> total bonus used = 400.
	now = now.Add(1 * time.Minute)
	ev3, err := eng.Award(ctx, "task_done", "Step 3", 100, "D3", true)
	if err != nil || ev3.AwardedXP != 400 {
		t.Fatalf("step 3 failed: %+v err=%v", ev3, err)
	}
	// Action 4 (1 min later): x8 -> raw bonus 700, only 100 cap remains -> awarded 200.
	now = now.Add(1 * time.Minute)
	ev4, err := eng.Award(ctx, "task_done", "Step 4", 100, "D4", true)
	if err != nil || ev4.AwardedXP != 200 {
		t.Fatalf("step 4 expected 500 cap enforcement (awarded=200), got %+v err=%v", ev4, err)
	}
	// Action 5 (1 min later): 0 bonus cap left -> awarded == baseXP (100).
	now = now.Add(1 * time.Minute)
	ev5, err := eng.Award(ctx, "task_done", "Step 5", 100, "D5", true)
	if err != nil || ev5.AwardedXP != 100 {
		t.Fatalf("step 5 expected 0 remaining bonus cap (awarded=100), got %+v err=%v", ev5, err)
	}

	prof, err := eng.Profile(ctx)
	if err != nil {
		t.Fatalf("profile failed: %v", err)
	}
	if prof.DailyBonusXP != 500 || prof.DailyBonusCap != 500 {
		t.Fatalf("expected DailyBonusXP=500 / Cap=500, got %d / %d", prof.DailyBonusXP, prof.DailyBonusCap)
	}

	// After 24+ hours, the rolling 24h bonus window resets and streak advances to day 2.
	now = now.Add(25 * time.Hour)
	evNextDay, err := eng.Award(ctx, "task_done", "Day 2 Action", 100, "D6", true)
	if err != nil {
		t.Fatalf("day 2 award failed: %v", err)
	}
	if evNextDay.AwardedXP != 110 {
		t.Fatalf("expected day 2 streak 1.1x (110 XP) after 24h cap reset, got %+v", evNextDay)
	}
}

func TestBossBattleAutoGenerationAndKOReward(t *testing.T) {
	hub, eng := openTestEngine(t, gamification.TelemetryHook{})
	ctx := context.Background()

	_, _ = hub.UpsertTask(ctx, store.TaskRecord{Code: "BOSS-1", Project: "riot-app", Milestone: "Act 1",
		Title: "Defeat bug", Priority: "p0", Status: "done"})
	_, _ = hub.UpsertTask(ctx, store.TaskRecord{Code: "BOSS-2", Project: "riot-app", Milestone: "Act 1",
		Title: "Ship feature", Priority: "p1", Status: "todo"})

	prof, err := eng.Profile(ctx)
	if err != nil {
		t.Fatalf("profile failed: %v", err)
	}
	if len(prof.BossBattles) != 1 {
		t.Fatalf("expected 1 boss battle, got %d", len(prof.BossBattles))
	}
	boss := prof.BossBattles[0]
	if boss.MaxHP != 200 || boss.CurrentHP != 100 || boss.Defeated || boss.KORewardXP != 500 {
		t.Fatalf("unexpected active boss state: %+v", boss)
	}

	// Clear final task -> Boss reaches 0 HP and auto-awards +500 XP KO once.
	_, err = hub.UpdateTaskStatus(ctx, "BOSS-2", "done", "", "")
	if err != nil {
		t.Fatalf("update task status: %v", err)
	}

	profAfterKO, err := eng.Profile(ctx)
	if err != nil {
		t.Fatalf("profile after KO failed: %v", err)
	}
	if !profAfterKO.BossBattles[0].Defeated || profAfterKO.BossBattles[0].CurrentHP != 0 {
		t.Fatalf("expected defeated boss at 0 HP, got %+v", profAfterKO.BossBattles[0])
	}
	if profAfterKO.TotalXP != 500 {
		t.Fatalf("expected exact +500 XP Boss KO reward, got totalXP=%d", profAfterKO.TotalXP)
	}

	// Idempotency: repeated Profile/SyncBossKOs must NOT double-award Boss KO XP.
	kos, err := eng.SyncBossKOs(ctx)
	if err != nil || len(kos) != 0 {
		t.Fatalf("expected 0 duplicate KOs, got %+v err=%v", kos, err)
	}
	profRepeat, _ := eng.Profile(ctx)
	if profRepeat.TotalXP != 500 {
		t.Fatalf("expected totalXP to stay 500, got %d", profRepeat.TotalXP)
	}

	var bossBreakerUnlocked bool
	for _, b := range profRepeat.Badges {
		if b.ID == "boss_breaker" && b.Unlocked {
			bossBreakerUnlocked = true
		}
	}
	if !bossBreakerUnlocked {
		t.Fatal("expected boss_breaker badge to be unlocked after Boss KO")
	}
}

func TestDailyQuestsStampsAndHTTPHandlers(t *testing.T) {
	_, eng := openTestEngine(t, gamification.TelemetryHook{
		GetRTKTokensSaved: func(context.Context) int64 { return 1500 },
		GetWAConnected:    func(context.Context) int { return 1 },
		GetCoderTurns:     func(context.Context) int { return 2 },
	})
	ctx := context.Background()

	_, err := eng.Award(ctx, "task_done", "Task A", 100, "Q-1", true)
	if err != nil {
		t.Fatalf("award 1: %v", err)
	}
	_, err = eng.Award(ctx, "task_done", "Task B", 100, "Q-2", true)
	if err != nil {
		t.Fatalf("award 2: %v", err)
	}

	claimed, err := eng.ClaimQuest(ctx, "bounty_hunter")
	if err != nil || claimed.AwardedXP <= 0 {
		t.Fatalf("expected bounty_hunter quest claimable, got %+v err=%v", claimed, err)
	}
	if _, err := eng.ClaimQuest(ctx, "bounty_hunter"); err == nil {
		t.Fatal("expected duplicate daily quest claim to fail")
	}

	prof, err := eng.Profile(ctx)
	if err != nil {
		t.Fatalf("profile failed: %v", err)
	}
	var stampEarned bool
	for _, st := range prof.Stamps {
		if st.ID == "stamp_bounty_hunter" && st.Earned && st.EarnedCount == 1 {
			stampEarned = true
		}
	}
	if !stampEarned {
		t.Fatalf("expected stamp_bounty_hunter earned in profile stamps: %+v", prof.Stamps)
	}
	if prof.UIContract.Shortcut != "Ctrl+K" || prof.UIContract.DrawerStyle != "comic-speech-bubble" {
		t.Fatalf("unexpected UI contract: %+v", prof.UIContract)
	}

	e := echo.New()
	eng.RegisterRoutes(e.Group("/api"))

	stampsRec := httptest.NewRecorder()
	e.ServeHTTP(stampsRec, httptest.NewRequest(http.MethodGet, "/api/game/stamps", nil))
	if stampsRec.Code != http.StatusOK || !strings.Contains(stampsRec.Body.String(), "stamp_bounty_hunter") {
		t.Fatalf("GET /api/game/stamps failed: %d %s", stampsRec.Code, stampsRec.Body.String())
	}

	claimRec := httptest.NewRecorder()
	e.ServeHTTP(claimRec, httptest.NewRequest(http.MethodPost, "/api/game/quests/token_pirate/claim", nil))
	if claimRec.Code != http.StatusOK {
		t.Fatalf("POST /api/game/quests/token_pirate/claim failed: %d %s", claimRec.Code, claimRec.Body.String())
	}
	var claimBody map[string]any
	if err := json.Unmarshal(claimRec.Body.Bytes(), &claimBody); err != nil {
		t.Fatalf("unmarshal claim body: %v", err)
	}
}
