package gamification

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/A-L-L-A-H/mw-echo/internal/store"
	"github.com/labstack/echo/v4"
)

// ProfileSnapshot summarizes the operator's gamification state, bosses, quests, and badges.
type ProfileSnapshot struct {
	Level        int             `json:"level"`
	RankTitle    string          `json:"rankTitle"`
	TotalXP      int64           `json:"totalXp"`
	LevelFloorXP int64           `json:"levelFloorXp"`
	NextLevelXP  int64           `json:"nextLevelXp"`
	LevelPct     float64         `json:"levelPct"`
	StreakDays   int             `json:"streakDays"`
	ComboCount   int             `json:"comboCount"`
	ComboMult    float64         `json:"comboMult"`
	LastActionAt string          `json:"lastActionAt,omitempty"`
	BossBattles  []BossBattle    `json:"bossBattles"`
	DailyQuests  []DailyQuest    `json:"dailyQuests"`
	Badges       []BadgeRecord   `json:"badges"`
	RecentEvents []XPActionEvent `json:"recentEvents"`
}

// BossBattle models a project milestone as an arcade boss whose HP tracks remaining open tasks.
type BossBattle struct {
	ID           string  `json:"id"`
	Project      string  `json:"project"`
	Milestone    string  `json:"milestone"`
	TotalTasks   int     `json:"totalTasks"`
	DoneTasks    int     `json:"doneTasks"`
	MaxHP        int     `json:"maxHp"`
	CurrentHP    int     `json:"currentHp"`
	RemainingPct float64 `json:"remainingPct"`
	Defeated     bool    `json:"defeated"`
	ThreatLabel  string  `json:"threatLabel"`
}

// DailyQuest tracks daily measurable activity across tasks, RTK token savings, coding agent, and radar.
type DailyQuest struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	RewardXP    int    `json:"rewardXp"`
	Progress    int    `json:"progress"`
	Target      int    `json:"target"`
	Completed   bool   `json:"completed"`
	Claimed     bool   `json:"claimed"`
}

// BadgeRecord represents an achievement stamp earned by real hub actions.
type BadgeRecord struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	StampColor  string `json:"stampColor"`
	Unlocked    bool   `json:"unlocked"`
	UnlockedAt  string `json:"unlockedAt,omitempty"`
}

// XPActionEvent records an immutable XP award event.
type XPActionEvent struct {
	ID         string  `json:"id"`
	EventType  string  `json:"eventType"`
	Label      string  `json:"label"`
	RefID      string  `json:"refId,omitempty"`
	BaseXP     int     `json:"baseXp"`
	Multiplier float64 `json:"multiplier"`
	AwardedXP  int     `json:"awardedXp"`
	CreatedAt  string  `json:"createdAt"`
}

// TelemetryHook lets gamification read live RTK tokens saved and WhatsApp device counts cleanly.
type TelemetryHook struct {
	GetRTKTokensSaved func(ctx context.Context) int64
	GetWAConnected    func(ctx context.Context) int
	GetCoderTurns     func(ctx context.Context) int
}

// Engine persists deterministic XP, combos, streaks, daily quests, and badges in HubStore SQLite.
type Engine struct {
	hub  *store.HubStore
	hook TelemetryHook
	mu   sync.Mutex
}

// NewEngine initializes the gamification SQLite tables and default profile row.
func NewEngine(hub *store.HubStore, hook TelemetryHook) (*Engine, error) {
	eng := &Engine{hub: hub, hook: hook}
	if err := eng.migrate(); err != nil {
		return nil, err
	}
	return eng, nil
}

func (e *Engine) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS game_profile (
  id TEXT PRIMARY KEY,
  total_xp INTEGER NOT NULL DEFAULT 0,
  streak_days INTEGER NOT NULL DEFAULT 0,
  last_active_day TEXT NOT NULL DEFAULT '',
  last_action_at TEXT NOT NULL DEFAULT '',
  combo_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS game_events (
  id TEXT PRIMARY KEY,
  event_type TEXT NOT NULL,
  label TEXT NOT NULL,
  ref_id TEXT NOT NULL DEFAULT '',
  base_xp INTEGER NOT NULL,
  multiplier REAL NOT NULL DEFAULT 1.0,
  awarded_xp INTEGER NOT NULL,
  day_key TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_game_events_created ON game_events(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_game_events_day ON game_events(day_key, event_type);

CREATE TABLE IF NOT EXISTS game_badges (
  id TEXT PRIMARY KEY,
  unlocked_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS game_quest_claims (
  claim_key TEXT PRIMARY KEY,
  quest_id TEXT NOT NULL,
  day_key TEXT NOT NULL,
  claimed_at TEXT NOT NULL
);
`
	if _, err := e.hub.DB().Exec(schema); err != nil {
		return fmt.Errorf("migrate gamification schema: %w", err)
	}
	_, err := e.hub.DB().Exec(`INSERT INTO game_profile (id, total_xp, streak_days, last_active_day, last_action_at, combo_count) VALUES ('operator', 0, 0, '', '', 0) ON CONFLICT(id) DO NOTHING`)
	return err
}

func makeGameEventID(parts ...string) string {
	hasher := sha256.New()
	for _, p := range parts {
		hasher.Write([]byte(p))
		hasher.Write([]byte{0})
	}
	hasher.Write([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	return hex.EncodeToString(hasher.Sum(nil))[:24]
}

// ComputeLevel maps cumulative XP to current level, floor XP, next threshold XP, and Riot rank title.
func ComputeLevel(totalXP int64) (level int, rankTitle string, floorXP, nextXP int64, pct float64) {
	level = 1
	floorXP = 0
	for {
		span := int64(math.Floor(120 * math.Pow(float64(level), 1.45)))
		if totalXP < floorXP+span {
			nextXP = floorXP + span
			if span > 0 {
				pct = math.Round((float64(totalXP-floorXP)/float64(span))*1000) / 10
			}
			break
		}
		floorXP += span
		level++
	}
	switch {
	case level >= 50:
		rankTitle = "MOONWITNESS OVERLORD"
	case level >= 30:
		rankTitle = "SHADOW SYNDICATE"
	case level >= 20:
		rankTitle = "CHAOS ARCHITECT"
	case level >= 10:
		rankTitle = "SIGNAL BREAKER"
	case level >= 5:
		rankTitle = "STREET CODER"
	default:
		rankTitle = "ROOKIE RUNNER"
	}
	return level, rankTitle, floorXP, nextXP, pct
}

func comboMultiplier(count int) float64 {
	switch {
	case count >= 5:
		return 5.0
	case count == 4:
		return 3.0
	case count == 3:
		return 2.0
	case count == 2:
		return 1.5
	default:
		return 1.0
	}
}

// Award credits XP for a real action, updates streak + 30m combo window, and unlocks matching badges.
func (e *Engine) Award(ctx context.Context, eventType, label string, baseXP int, refID string, comboEligible bool) (XPActionEvent, error) {
	if baseXP <= 0 {
		return XPActionEvent{}, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	yesterday := now.Add(-24 * time.Hour).Format("2006-01-02")

	var totalXP int64
	var streakDays, comboCount int
	var lastDay, lastActionRaw string
	err := e.hub.DB().QueryRowContext(ctx, `
SELECT total_xp, streak_days, last_active_day, last_action_at, combo_count
FROM game_profile WHERE id = 'operator'`).Scan(&totalXP, &streakDays, &lastDay, &lastActionRaw, &comboCount)
	if err != nil {
		return XPActionEvent{}, err
	}

	if lastDay == "" {
		streakDays = 1
	} else if lastDay == yesterday {
		streakDays++
	} else if lastDay != today {
		streakDays = 1
	}

	mult := 1.0
	if comboEligible {
		prevTime, parseErr := time.Parse(time.RFC3339Nano, lastActionRaw)
		if parseErr == nil && now.Sub(prevTime) <= 30*time.Minute {
			comboCount++
		} else {
			comboCount = 1
		}
		mult = comboMultiplier(comboCount)
	}
	streakBonus := 1.0 + math.Min(float64(maxInt(streakDays-1, 0))*0.10, 0.50)
	effectiveMult := math.Round(mult*streakBonus*100) / 100
	awarded := int(math.Round(float64(baseXP) * effectiveMult))

	ev := XPActionEvent{
		ID:         makeGameEventID(eventType, label, refID),
		EventType:  strings.TrimSpace(eventType),
		Label:      strings.TrimSpace(label),
		RefID:      strings.TrimSpace(refID),
		BaseXP:     baseXP,
		Multiplier: effectiveMult,
		AwardedXP:  awarded,
		CreatedAt:  now.Format(time.RFC3339Nano),
	}

	tx, err := e.hub.DB().BeginTx(ctx, nil)
	if err != nil {
		return XPActionEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
UPDATE game_profile
SET total_xp = ?, streak_days = ?, last_active_day = ?, last_action_at = ?, combo_count = ?
WHERE id = 'operator'`,
		totalXP+int64(awarded), streakDays, today, ev.CreatedAt, comboCount,
	); err != nil {
		return XPActionEvent{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO game_events (id, event_type, label, ref_id, base_xp, multiplier, awarded_xp, day_key, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.EventType, ev.Label, ev.RefID, ev.BaseXP, ev.Multiplier, ev.AwardedXP, today, ev.CreatedAt,
	); err != nil {
		return XPActionEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return XPActionEvent{}, err
	}

	e.evaluateBadges(ctx, comboCount)
	return ev, nil
}

func (e *Engine) unlockBadge(ctx context.Context, badgeID string) {
	_, _ = e.hub.DB().ExecContext(ctx, `
INSERT INTO game_badges (id, unlocked_at) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`,
		badgeID, time.Now().UTC().Format(time.RFC3339),
	)
}

func (e *Engine) evaluateBadges(ctx context.Context, comboCount int) {
	stats, err := e.hub.Stats(ctx)
	if err == nil {
		if stats.TasksByStatus["done"] > 0 {
			e.unlockBadge(ctx, "first_blood")
		}
		if stats.DocsCount >= 50 {
			e.unlockBadge(ctx, "archive_master")
		}
		if stats.StorageCount > 0 {
			e.unlockBadge(ctx, "zero_disk_monk")
		}
	}
	if comboCount >= 3 {
		e.unlockBadge(ctx, "combo_freak")
	}
	if e.hook.GetRTKTokensSaved != nil && e.hook.GetRTKTokensSaved(ctx) >= 10000 {
		e.unlockBadge(ctx, "token_pirate")
	}
	if e.hook.GetWAConnected != nil && e.hook.GetWAConnected(ctx) > 0 {
		e.unlockBadge(ctx, "gowa_operator")
	}
	projects, err := e.hub.ListProjects(ctx)
	if err == nil {
		for _, proj := range projects {
			for _, ms := range proj.Milestones {
				if ms.TotalTasks > 0 && ms.DoneTasks == ms.TotalTasks {
					e.unlockBadge(ctx, "boss_breaker")
					return
				}
			}
		}
	}
}

// Profile returns the complete gamification HUD payload.
func (e *Engine) Profile(ctx context.Context) (ProfileSnapshot, error) {
	var totalXP int64
	var streakDays, comboCount int
	var lastActionRaw string
	err := e.hub.DB().QueryRowContext(ctx, `
SELECT total_xp, streak_days, last_action_at, combo_count
FROM game_profile WHERE id = 'operator'`).Scan(&totalXP, &streakDays, &lastActionRaw, &comboCount)
	if err != nil && err != sql.ErrNoRows {
		return ProfileSnapshot{}, err
	}

	e.evaluateBadges(ctx, comboCount)

	// Decay visible combo multiplier if 30m window elapsed.
	activeMult := 1.0
	if lastActionRaw != "" {
		if prevTime, parseErr := time.Parse(time.RFC3339Nano, lastActionRaw); parseErr == nil && time.Since(prevTime) <= 30*time.Minute {
			activeMult = comboMultiplier(comboCount)
		} else {
			comboCount = 0
		}
	}

	level, rankTitle, floorXP, nextXP, levelPct := ComputeLevel(totalXP)

	snap := ProfileSnapshot{
		Level:        level,
		RankTitle:    rankTitle,
		TotalXP:      totalXP,
		LevelFloorXP: floorXP,
		NextLevelXP:  nextXP,
		LevelPct:     levelPct,
		StreakDays:   streakDays,
		ComboCount:   comboCount,
		ComboMult:    activeMult,
		LastActionAt: lastActionRaw,
		BossBattles:  e.buildBossBattles(ctx),
		DailyQuests:  e.buildDailyQuests(ctx),
		Badges:       e.buildBadges(ctx),
		RecentEvents: e.RecentEvents(ctx, 20),
	}
	return snap, nil
}

func (e *Engine) buildBossBattles(ctx context.Context) []BossBattle {
	projects, err := e.hub.ListProjects(ctx)
	if err != nil {
		return []BossBattle{}
	}
	battles := make([]BossBattle, 0)
	for _, proj := range projects {
		for _, ms := range proj.Milestones {
			if ms.TotalTasks == 0 {
				continue
			}
			maxHP := ms.TotalTasks * 100
			currentHP := maxInt(0, (ms.TotalTasks-ms.DoneTasks)*100)
			remPct := 0.0
			if maxHP > 0 {
				remPct = math.Round((float64(currentHP)/float64(maxHP))*1000) / 10
			}
			threat := "ELITE BOSS"
			if currentHP == 0 {
				threat = "DEFEATED"
			} else if ms.TotalTasks >= 50 {
				threat = "RAID TITAN"
			} else if remPct <= 25 {
				threat = "LOW HP · FINISH IT"
			}
			battles = append(battles, BossBattle{
				ID:           proj.Slug + "::" + ms.Name,
				Project:      proj.Slug,
				Milestone:    ms.Name,
				TotalTasks:   ms.TotalTasks,
				DoneTasks:    ms.DoneTasks,
				MaxHP:        maxHP,
				CurrentHP:    currentHP,
				RemainingPct: remPct,
				Defeated:     currentHP == 0,
				ThreatLabel:  threat,
			})
		}
	}
	if len(battles) > 16 {
		battles = battles[:16]
	}
	return battles
}

func (e *Engine) buildDailyQuests(ctx context.Context) []DailyQuest {
	today := time.Now().UTC().Format("2006-01-02")

	dayCount := func(eventTypes ...string) int {
		if len(eventTypes) == 0 {
			return 0
		}
		placeholders := make([]string, len(eventTypes))
		args := []any{today}
		for i, et := range eventTypes {
			placeholders[i] = "?"
			args = append(args, et)
		}
		var cnt int
		_ = e.hub.DB().QueryRowContext(ctx, fmt.Sprintf(`
SELECT COUNT(*) FROM game_events WHERE day_key = ? AND event_type IN (%s)`, strings.Join(placeholders, ",")), args...).Scan(&cnt)
		return cnt
	}

	claimed := map[string]bool{}
	claimRows, err := e.hub.DB().QueryContext(ctx, `SELECT quest_id FROM game_quest_claims WHERE day_key = ?`, today)
	if err == nil {
		for claimRows.Next() {
			var qid string
			if claimRows.Scan(&qid) == nil {
				claimed[qid] = true
			}
		}
		_ = claimRows.Close()
	}

	bountyProgress := dayCount("task_done")
	coderProgress := dayCount("coder_turn", "coder_mission")
	if e.hook.GetCoderTurns != nil {
		if turns := e.hook.GetCoderTurns(ctx); turns > coderProgress {
			coderProgress = turns
		}
	}
	signalProgress := dayCount("repo_import", "sniff_models", "radar_scan")
	var tokensProgress int
	if e.hook.GetRTKTokensSaved != nil {
		tokensProgress = int(e.hook.GetRTKTokensSaved(ctx))
	}

	return []DailyQuest{
		{
			ID:          "bounty_hunter",
			Title:       "BOUNTY HUNTER",
			Description: "Clear 2 tasks on the Kanban board today",
			RewardXP:    150,
			Progress:    minInt(bountyProgress, 2),
			Target:      2,
			Completed:   bountyProgress >= 2,
			Claimed:     claimed["bounty_hunter"],
		},
		{
			ID:          "token_pirate",
			Title:       "TOKEN PIRATE",
			Description: "Save 1,000+ tokens via 9Router RTK compression",
			RewardXP:    120,
			Progress:    minInt(tokensProgress, 1000),
			Target:      1000,
			Completed:   tokensProgress >= 1000,
			Claimed:     claimed["token_pirate"],
		},
		{
			ID:          "code_ronin",
			Title:       "CODE RONIN",
			Description: "Run at least 1 Coding Agent mission today",
			RewardXP:    130,
			Progress:    minInt(coderProgress, 1),
			Target:      1,
			Completed:   coderProgress >= 1,
			Claimed:     claimed["code_ronin"],
		},
		{
			ID:          "signal_check",
			Title:       "SIGNAL RECON",
			Description: "Scan host repos, import a repo, or sniff free LLM catalog",
			RewardXP:    90,
			Progress:    minInt(signalProgress, 1),
			Target:      1,
			Completed:   signalProgress >= 1,
			Claimed:     claimed["signal_check"],
		},
	}
}

func (e *Engine) buildBadges(ctx context.Context) []BadgeRecord {
	unlockedAt := map[string]string{}
	rows, err := e.hub.DB().QueryContext(ctx, `SELECT id, unlocked_at FROM game_badges`)
	if err == nil {
		for rows.Next() {
			var id, ts string
			if rows.Scan(&id, &ts) == nil {
				unlockedAt[id] = ts
			}
		}
		_ = rows.Close()
	}

	catalog := []BadgeRecord{
		{ID: "first_blood", Title: "FIRST BLOOD", Description: "Clear your first task bounty", StampColor: "#ff2e88"},
		{ID: "combo_freak", Title: "OVERDRIVE 3X", Description: "Chain 3+ rapid actions in 30 minutes", StampColor: "#ffe031"},
		{ID: "boss_breaker", Title: "BOSS SLAYER", Description: "Clear 100% of tasks in any milestone boss", StampColor: "#a3ff12"},
		{ID: "zero_disk_monk", Title: "CLOUD VAULT", Description: "Store objects in the 9Drive pool", StampColor: "#00f0ff"},
		{ID: "token_pirate", Title: "TOKEN PIRATE", Description: "Compress 10,000+ tokens with 9Router RTK", StampColor: "#ffe031"},
		{ID: "gowa_operator", Title: "COMMS CHIEF", Description: "Connect a live WhatsApp GOWA device", StampColor: "#a3ff12"},
		{ID: "archive_master", Title: "CODEX KEEPER", Description: "Index 50+ docs in SQLite FTS5", StampColor: "#ff2e88"},
	}
	for i := range catalog {
		if ts, ok := unlockedAt[catalog[i].ID]; ok {
			catalog[i].Unlocked = true
			catalog[i].UnlockedAt = ts
		}
	}
	return catalog
}

// RecentEvents lists latest XP action events.
func (e *Engine) RecentEvents(ctx context.Context, limit int) []XPActionEvent {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := e.hub.DB().QueryContext(ctx, `
SELECT id, event_type, label, ref_id, base_xp, multiplier, awarded_xp, created_at
FROM game_events ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return []XPActionEvent{}
	}
	defer rows.Close()

	events := make([]XPActionEvent, 0)
	for rows.Next() {
		var ev XPActionEvent
		if rows.Scan(&ev.ID, &ev.EventType, &ev.Label, &ev.RefID, &ev.BaseXP, &ev.Multiplier, &ev.AwardedXP, &ev.CreatedAt) == nil {
			events = append(events, ev)
		}
	}
	return events
}

// ClaimQuest validates quest completion for today and grants its bonus XP once per day.
func (e *Engine) ClaimQuest(ctx context.Context, questID string) (XPActionEvent, error) {
	quests := e.buildDailyQuests(ctx)
	var targetQuest *DailyQuest
	for i := range quests {
		if quests[i].ID == strings.TrimSpace(questID) {
			targetQuest = &quests[i]
			break
		}
	}
	if targetQuest == nil {
		return XPActionEvent{}, fmt.Errorf("unknown quest %q", questID)
	}
	if !targetQuest.Completed {
		return XPActionEvent{}, fmt.Errorf("quest %q is not completed yet", questID)
	}
	if targetQuest.Claimed {
		return XPActionEvent{}, fmt.Errorf("quest %q already claimed today", questID)
	}

	today := time.Now().UTC().Format("2006-01-02")
	claimKey := today + ":" + targetQuest.ID
	if _, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO game_quest_claims (claim_key, quest_id, day_key, claimed_at) VALUES (?, ?, ?, ?)`,
		claimKey, targetQuest.ID, today, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		return XPActionEvent{}, err
	}
	return e.Award(ctx, "quest_claim", "Quest Cleared: "+targetQuest.Title, targetQuest.RewardXP, targetQuest.ID, true)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// RegisterRoutes attaches gamification HUD endpoints under /api/game/*.
func (e *Engine) RegisterRoutes(api *echo.Group) {
	grp := api.Group("/game")
	grp.GET("/profile", func(c echo.Context) error {
		profile, err := e.Profile(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, profile)
	})
	grp.GET("/feed", func(c echo.Context) error {
		limit, _ := strconv.Atoi(c.QueryParam("limit"))
		events := e.RecentEvents(c.Request().Context(), limit)
		return c.JSON(http.StatusOK, map[string]any{"events": events, "count": len(events)})
	})
	grp.POST("/quests/:id/claim", func(c echo.Context) error {
		awarded, err := e.ClaimQuest(c.Request().Context(), c.Param("id"))
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		profile, _ := e.Profile(c.Request().Context())
		return c.JSON(http.StatusOK, map[string]any{"awarded": awarded, "profile": profile})
	})
}
