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

const (
	// ComboWindow is the 30-minute arcade session window for chaining combo multipliers.
	ComboWindow = 30 * time.Minute
	// DailyBonusCapXP caps streak/combo bonus XP within any rolling 24-hour window.
	DailyBonusCapXP = 500
	// BossKORewardXP is the flat XP bounty awarded upon defeating a milestone boss (0 HP).
	BossKORewardXP = 500
)

// UIContract describes the comic speech-bubble drawer and daily mission widget contract.
type UIContract struct {
	Shortcut                string `json:"shortcut"`
	DrawerStyle             string `json:"drawerStyle"`
	DailyMissionBriefWidget bool   `json:"dailyMissionBriefWidget"`
	ArcadeStampsEnabled     bool   `json:"arcadeStampsEnabled"`
}

// DefaultUIContract returns the canonical AC5 drawer/widget contract.
func DefaultUIContract() UIContract {
	return UIContract{
		Shortcut:                "Ctrl+K",
		DrawerStyle:             "comic-speech-bubble",
		DailyMissionBriefWidget: true,
		ArcadeStampsEnabled:     true,
	}
}

// ProfileSnapshot summarizes the operator's gamification state, bosses, quests, stamps, and badges.
type ProfileSnapshot struct {
	Level         int             `json:"level"`
	RankGrade     string          `json:"rankGrade"`
	RankTitle     string          `json:"rankTitle"`
	TotalXP       int64           `json:"totalXp"`
	LevelFloorXP  int64           `json:"levelFloorXp"`
	NextLevelXP   int64           `json:"nextLevelXp"`
	LevelPct      float64         `json:"levelPct"`
	StreakDays    int             `json:"streakDays"`
	DailyBonusXP  int             `json:"dailyBonusXp"`
	DailyBonusCap int             `json:"dailyBonusCap"`
	ComboCount    int             `json:"comboCount"`
	ComboMult     float64         `json:"comboMult"`
	LastActionAt  string          `json:"lastActionAt,omitempty"`
	BossBattles   []BossBattle    `json:"bossBattles"`
	DailyQuests   []DailyQuest    `json:"dailyQuests"`
	Stamps        []StampRecord   `json:"stamps"`
	Badges        []BadgeRecord   `json:"badges"`
	RecentEvents  []XPActionEvent `json:"recentEvents"`
	UIContract    UIContract      `json:"uiContract"`
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
	KORewardXP   int     `json:"koRewardXp"`
	ThreatLabel  string  `json:"threatLabel"`
}

// DailyQuest tracks daily measurable activity across tasks, RTK token savings, coding agent, and radar.
type DailyQuest struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	RewardXP    int    `json:"rewardXp"`
	StampID     string `json:"stampId"`
	Progress    int    `json:"progress"`
	Target      int    `json:"target"`
	Completed   bool   `json:"completed"`
	Claimed     bool   `json:"claimed"`
}

// StampRecord represents a retro arcade stamp awarded upon daily quest completion.
type StampRecord struct {
	ID           string `json:"id"`
	QuestID      string `json:"questId"`
	Title        string `json:"title"`
	StampColor   string `json:"stampColor"`
	Earned       bool   `json:"earned"`
	EarnedCount  int    `json:"earnedCount"`
	LastEarnedAt string `json:"lastEarnedAt,omitempty"`
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

// Engine persists deterministic XP, combos, streaks, daily quests, boss KOs, and stamps in HubStore SQLite.
type Engine struct {
	hub   *store.HubStore
	hook  TelemetryHook
	nowFn func() time.Time
	mu    sync.Mutex
}

// NewEngine initializes the gamification SQLite tables and default profile row.
func NewEngine(hub *store.HubStore, hook TelemetryHook) (*Engine, error) {
	eng := &Engine{
		hub:   hub,
		hook:  hook,
		nowFn: func() time.Time { return time.Now().UTC() },
	}
	if err := eng.migrate(); err != nil {
		return nil, err
	}
	return eng, nil
}

// SetClock overrides the engine clock for deterministic time-window testing.
func (e *Engine) SetClock(fn func() time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if fn == nil {
		e.nowFn = func() time.Time { return time.Now().UTC() }
		return
	}
	e.nowFn = fn
}

func (e *Engine) nowUTC() time.Time {
	if e.nowFn != nil {
		return e.nowFn().UTC()
	}
	return time.Now().UTC()
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

CREATE TABLE IF NOT EXISTS game_boss_kos (
  boss_id TEXT PRIMARY KEY,
  project TEXT NOT NULL,
  milestone TEXT NOT NULL,
  reward_xp INTEGER NOT NULL,
  defeated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS game_stamps (
  id TEXT PRIMARY KEY,
  quest_id TEXT NOT NULL,
  title TEXT NOT NULL,
  stamp_color TEXT NOT NULL,
  earned_count INTEGER NOT NULL DEFAULT 0,
  last_earned_at TEXT NOT NULL DEFAULT ''
);
`
	if _, err := e.hub.DB().Exec(schema); err != nil {
		return fmt.Errorf("migrate gamification schema: %w", err)
	}
	_, err := e.hub.DB().Exec(`INSERT INTO game_profile (id, total_xp, streak_days, last_active_day, last_action_at, combo_count) VALUES ('operator', 0, 0, '', '', 0) ON CONFLICT(id) DO NOTHING`)
	return err
}

func makeGameEventID(now time.Time, parts ...string) string {
	hasher := sha256.New()
	for _, p := range parts {
		hasher.Write([]byte(p))
		hasher.Write([]byte{0})
	}
	hasher.Write([]byte(strconv.FormatInt(now.UnixNano(), 10)))
	return hex.EncodeToString(hasher.Sum(nil))[:24]
}

// XPForNextLevel returns the XP span required to advance from level L to L+1: floor(120 * L^1.45).
func XPForNextLevel(level int) int64 {
	if level < 1 {
		level = 1
	}
	return int64(math.Floor(120 * math.Pow(float64(level), 1.45)))
}

// RankForLevel maps a numeric level to its short grade and Riot rank title.
func RankForLevel(level int) (grade string, title string) {
	switch {
	case level >= 50:
		return "SS", "MOONWITNESS OVERLORD"
	case level >= 30:
		return "S", "SHADOW SYNDICATE"
	case level >= 20:
		return "A", "CHAOS ARCHITECT"
	case level >= 10:
		return "B", "SIGNAL BREAKER"
	case level >= 5:
		return "C", "STREET CODER"
	default:
		return "D", "ROOKIE RUNNER"
	}
}

// ComputeLevel maps cumulative XP to current level, floor XP, next threshold XP, and Riot rank title.
func ComputeLevel(totalXP int64) (level int, rankTitle string, floorXP, nextXP int64, pct float64) {
	if totalXP < 0 {
		totalXP = 0
	}
	level = 1
	floorXP = 0
	for {
		span := XPForNextLevel(level)
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
	_, rankTitle = RankForLevel(level)
	return level, rankTitle, floorXP, nextXP, pct
}

// ComboMultiplier maps an active 30-minute arcade session combo count to x1, x2, x4, or x8.
func ComboMultiplier(count int) float64 {
	switch {
	case count >= 4:
		return 8.0
	case count == 3:
		return 4.0
	case count == 2:
		return 2.0
	default:
		return 1.0
	}
}

func (e *Engine) bonusXPInLast24hLocked(ctx context.Context, now time.Time) int {
	today := now.Format("2006-01-02")
	yesterday := now.Add(-24 * time.Hour).Format("2006-01-02")
	rows, err := e.hub.DB().QueryContext(ctx, `
SELECT base_xp, awarded_xp, created_at
FROM game_events
WHERE day_key IN (?, ?)`, today, yesterday)
	if err != nil {
		return 0
	}
	defer rows.Close()

	cutoff := now.Add(-24 * time.Hour)
	totalBonus := 0
	for rows.Next() {
		var baseXP, awardedXP int
		var createdRaw string
		if rows.Scan(&baseXP, &awardedXP, &createdRaw) != nil {
			continue
		}
		ts, parseErr := time.Parse(time.RFC3339Nano, createdRaw)
		if parseErr != nil {
			continue
		}
		if !ts.Before(cutoff) && !ts.After(now) && awardedXP > baseXP {
			totalBonus += awardedXP - baseXP
		}
	}
	return totalBonus
}

// Award credits XP for a real action, enforces the 30m combo & 500 XP/24h bonus cap, and triggers Boss KOs.
func (e *Engine) Award(ctx context.Context, eventType, label string, baseXP int, refID string, comboEligible bool) (XPActionEvent, error) {
	if baseXP <= 0 {
		return XPActionEvent{}, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.nowUTC()
	ev, err := e.awardLocked(ctx, now, eventType, label, baseXP, refID, comboEligible, true)
	if err != nil {
		return XPActionEvent{}, err
	}
	_, _ = e.syncBossKOsLocked(ctx, now)
	e.evaluateBadgesLocked(ctx, now)
	return ev, nil
}

func (e *Engine) awardLocked(ctx context.Context, now time.Time, eventType, label string, baseXP int, refID string, comboEligible, allowBonus bool) (XPActionEvent, error) {
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
		if parseErr == nil && now.Sub(prevTime) <= ComboWindow && now.Sub(prevTime) >= 0 {
			comboCount++
		} else {
			comboCount = 1
		}
		mult = ComboMultiplier(comboCount)
	}

	awarded := baseXP
	effectiveMult := 1.0
	if allowBonus {
		streakBonus := 1.0 + math.Min(float64(maxInt(streakDays-1, 0))*0.10, 0.50)
		rawMult := math.Round(mult*streakBonus*100) / 100
		rawAwarded := int(math.Round(float64(baseXP) * rawMult))
		rawBonus := maxInt(0, rawAwarded-baseXP)

		usedBonus := e.bonusXPInLast24hLocked(ctx, now)
		remainingCap := maxInt(0, DailyBonusCapXP-usedBonus)
		cappedBonus := minInt(rawBonus, remainingCap)
		awarded = baseXP + cappedBonus
		effectiveMult = math.Round((float64(awarded)/float64(baseXP))*100) / 100
	}

	nextLastAction := lastActionRaw
	if comboEligible || nextLastAction == "" {
		nextLastAction = now.Format(time.RFC3339Nano)
	}

	ev := XPActionEvent{
		ID:         makeGameEventID(now, eventType, label, refID),
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
		totalXP+int64(awarded), streakDays, today, nextLastAction, comboCount,
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

	return ev, nil
}

// SyncBossKOs checks all milestone bosses and awards +500 XP once per newly defeated boss.
func (e *Engine) SyncBossKOs(ctx context.Context) ([]XPActionEvent, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.nowUTC()
	kos, err := e.syncBossKOsLocked(ctx, now)
	if err != nil {
		return nil, err
	}
	e.evaluateBadgesLocked(ctx, now)
	return kos, nil
}

func (e *Engine) syncBossKOsLocked(ctx context.Context, now time.Time) ([]XPActionEvent, error) {
	battles := e.buildBossBattles(ctx)
	var awardedKOs []XPActionEvent
	for _, b := range battles {
		if !b.Defeated || b.TotalTasks == 0 {
			continue
		}
		res, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO game_boss_kos (boss_id, project, milestone, reward_xp, defeated_at)
VALUES (?, ?, ?, ?, ?) ON CONFLICT(boss_id) DO NOTHING`,
			b.ID, b.Project, b.Milestone, BossKORewardXP, now.Format(time.RFC3339),
		)
		if err != nil {
			return awardedKOs, err
		}
		rowsAffected, _ := res.RowsAffected()
		if rowsAffected == 0 {
			continue
		}
		ev, err := e.awardLocked(ctx, now, "boss_ko", fmt.Sprintf("Boss KO: %s / %s", b.Project, b.Milestone), BossKORewardXP, b.ID, false, false)
		if err != nil {
			return awardedKOs, err
		}
		awardedKOs = append(awardedKOs, ev)
		e.unlockBadgeLocked(ctx, now, "boss_breaker")
	}
	return awardedKOs, nil
}

func (e *Engine) unlockBadgeLocked(ctx context.Context, now time.Time, badgeID string) {
	_, _ = e.hub.DB().ExecContext(ctx, `
INSERT INTO game_badges (id, unlocked_at) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`,
		badgeID, now.Format(time.RFC3339),
	)
}

func (e *Engine) awardStampLocked(ctx context.Context, now time.Time, stampID, questID, title, color string) {
	_, _ = e.hub.DB().ExecContext(ctx, `
INSERT INTO game_stamps (id, quest_id, title, stamp_color, earned_count, last_earned_at)
VALUES (?, ?, ?, ?, 1, ?)
ON CONFLICT(id) DO UPDATE SET
  earned_count = game_stamps.earned_count + 1,
  last_earned_at = excluded.last_earned_at`,
		stampID, questID, title, color, now.Format(time.RFC3339),
	)
}

func (e *Engine) evaluateBadgesLocked(ctx context.Context, now time.Time) {
	var comboCount int
	_ = e.hub.DB().QueryRowContext(ctx, `SELECT combo_count FROM game_profile WHERE id = 'operator'`).Scan(&comboCount)

	stats, err := e.hub.Stats(ctx)
	if err == nil {
		if stats.TasksByStatus["done"] > 0 {
			e.unlockBadgeLocked(ctx, now, "first_blood")
		}
		if stats.DocsCount >= 50 {
			e.unlockBadgeLocked(ctx, now, "archive_master")
		}
		if stats.StorageCount > 0 {
			e.unlockBadgeLocked(ctx, now, "zero_disk_monk")
		}
	}
	if comboCount >= 3 {
		e.unlockBadgeLocked(ctx, now, "combo_freak")
	}
	if e.hook.GetRTKTokensSaved != nil && e.hook.GetRTKTokensSaved(ctx) >= 10000 {
		e.unlockBadgeLocked(ctx, now, "token_pirate")
	}
	if e.hook.GetWAConnected != nil && e.hook.GetWAConnected(ctx) > 0 {
		e.unlockBadgeLocked(ctx, now, "gowa_operator")
	}
	projects, err := e.hub.ListProjects(ctx)
	if err == nil {
		for _, proj := range projects {
			for _, ms := range proj.Milestones {
				if ms.TotalTasks > 0 && ms.DoneTasks == ms.TotalTasks {
					e.unlockBadgeLocked(ctx, now, "boss_breaker")
					break
				}
			}
		}
	}
}

// Profile returns the complete gamification HUD payload.
func (e *Engine) Profile(ctx context.Context) (ProfileSnapshot, error) {
	e.mu.Lock()
	now := e.nowUTC()
	_, _ = e.syncBossKOsLocked(ctx, now)
	e.evaluateBadgesLocked(ctx, now)

	var totalXP int64
	var streakDays, comboCount int
	var lastActionRaw string
	err := e.hub.DB().QueryRowContext(ctx, `
SELECT total_xp, streak_days, last_action_at, combo_count
FROM game_profile WHERE id = 'operator'`).Scan(&totalXP, &streakDays, &lastActionRaw, &comboCount)
	if err != nil && err != sql.ErrNoRows {
		e.mu.Unlock()
		return ProfileSnapshot{}, err
	}

	activeMult := 1.0
	if lastActionRaw != "" {
		if prevTime, parseErr := time.Parse(time.RFC3339Nano, lastActionRaw); parseErr == nil && now.Sub(prevTime) <= ComboWindow && now.Sub(prevTime) >= 0 {
			activeMult = ComboMultiplier(comboCount)
		} else {
			comboCount = 0
		}
	}
	dailyBonusUsed := e.bonusXPInLast24hLocked(ctx, now)
	e.mu.Unlock()

	level, rankTitle, floorXP, nextXP, levelPct := ComputeLevel(totalXP)
	rankGrade, _ := RankForLevel(level)

	return ProfileSnapshot{
		Level:         level,
		RankGrade:     rankGrade,
		RankTitle:     rankTitle,
		TotalXP:       totalXP,
		LevelFloorXP:  floorXP,
		NextLevelXP:   nextXP,
		LevelPct:      levelPct,
		StreakDays:    streakDays,
		DailyBonusXP:  dailyBonusUsed,
		DailyBonusCap: DailyBonusCapXP,
		ComboCount:    comboCount,
		ComboMult:     activeMult,
		LastActionAt:  lastActionRaw,
		BossBattles:   e.buildBossBattles(ctx),
		DailyQuests:   e.buildDailyQuests(ctx, now),
		Stamps:        e.buildStamps(ctx),
		Badges:        e.buildBadges(ctx),
		RecentEvents:  e.RecentEvents(ctx, 20),
		UIContract:    DefaultUIContract(),
	}, nil
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
				KORewardXP:   BossKORewardXP,
				ThreatLabel:  threat,
			})
		}
	}
	if len(battles) > 16 {
		battles = battles[:16]
	}
	return battles
}

type stampMeta struct {
	ID      string
	QuestID string
	Title   string
	Color   string
}

var defaultStampCatalog = []stampMeta{
	{ID: "stamp_bounty_hunter", QuestID: "bounty_hunter", Title: "BOUNTY CLEAR", Color: "#3b82f6"},
	{ID: "stamp_token_pirate", QuestID: "token_pirate", Title: "RTK COMPRESSOR", Color: "#10b981"},
	{ID: "stamp_code_ronin", QuestID: "code_ronin", Title: "RONIN STRIKE", Color: "#8b5cf6"},
	{ID: "stamp_signal_check", QuestID: "signal_check", Title: "RADAR LOCK", Color: "#f59e0b"},
}

func findStampForQuest(questID string) (stampMeta, bool) {
	for _, s := range defaultStampCatalog {
		if s.QuestID == questID {
			return s, true
		}
	}
	return stampMeta{}, false
}

func (e *Engine) buildDailyQuests(ctx context.Context, now time.Time) []DailyQuest {
	today := now.Format("2006-01-02")

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
			StampID:     "stamp_bounty_hunter",
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
			StampID:     "stamp_token_pirate",
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
			StampID:     "stamp_code_ronin",
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
			StampID:     "stamp_signal_check",
			Progress:    minInt(signalProgress, 1),
			Target:      1,
			Completed:   signalProgress >= 1,
			Claimed:     claimed["signal_check"],
		},
	}
}

func (e *Engine) buildStamps(ctx context.Context) []StampRecord {
	earnedMap := map[string]StampRecord{}
	rows, err := e.hub.DB().QueryContext(ctx, `SELECT id, earned_count, last_earned_at FROM game_stamps`)
	if err == nil {
		for rows.Next() {
			var id, lastAt string
			var cnt int
			if rows.Scan(&id, &cnt, &lastAt) == nil {
				earnedMap[id] = StampRecord{Earned: cnt > 0, EarnedCount: cnt, LastEarnedAt: lastAt}
			}
		}
		_ = rows.Close()
	}

	out := make([]StampRecord, len(defaultStampCatalog))
	for i, meta := range defaultStampCatalog {
		rec := StampRecord{
			ID:         meta.ID,
			QuestID:    meta.QuestID,
			Title:      meta.Title,
			StampColor: meta.Color,
		}
		if st, ok := earnedMap[meta.ID]; ok {
			rec.Earned = st.Earned
			rec.EarnedCount = st.EarnedCount
			rec.LastEarnedAt = st.LastEarnedAt
		}
		out[i] = rec
	}
	return out
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
		{ID: "combo_freak", Title: "OVERDRIVE 4X", Description: "Chain 3+ rapid actions in 30 minutes", StampColor: "#ffe031"},
		{ID: "boss_breaker", Title: "BOSS SLAYER", Description: "Clear 100% of tasks in any milestone boss (+500 XP KO)", StampColor: "#a3ff12"},
		{ID: "zero_disk_monk", Title: "CLOUD VAULT", Description: "Store objects in the 9Drive pool", StampColor: "#00f0ff"},
		{ID: "token_pirate", Title: "TOKEN PIRATE", Description: "Compress 10,000+ tokens with 9Router RTK", StampColor: "#ffe031"},
		{ID: "gowa_operator", Title: "COMMS CHIEF", Description: "Connect a live WhatsApp GOWA device", StampColor: "#a3ff12"},
		{ID: "archive_master", Title: "CODEX KEEPER", Description: "Index 50+ docs in SQLite FTS5", StampColor: "#ff2e88"},
		{ID: "quest_stamper", Title: "ARCADE STAMPER", Description: "Earn your first retro arcade stamp by clearing a daily quest", StampColor: "#3b82f6"},
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

// ClaimQuest validates quest completion for today, grants its bonus XP once per day, and stamps the arcade badge.
func (e *Engine) ClaimQuest(ctx context.Context, questID string) (XPActionEvent, error) {
	now := e.nowUTC()
	quests := e.buildDailyQuests(ctx, now)
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

	today := now.Format("2006-01-02")
	claimKey := today + ":" + targetQuest.ID
	if _, err := e.hub.DB().ExecContext(ctx, `
INSERT INTO game_quest_claims (claim_key, quest_id, day_key, claimed_at) VALUES (?, ?, ?, ?)`,
		claimKey, targetQuest.ID, today, now.Format(time.RFC3339),
	); err != nil {
		return XPActionEvent{}, err
	}

	e.mu.Lock()
	if stamp, ok := findStampForQuest(targetQuest.ID); ok {
		e.awardStampLocked(ctx, now, stamp.ID, stamp.QuestID, stamp.Title, stamp.Color)
	}
	e.unlockBadgeLocked(ctx, now, "quest_stamper")
	ev, err := e.awardLocked(ctx, now, "quest_claim", "Quest Cleared: "+targetQuest.Title, targetQuest.RewardXP, targetQuest.ID, true, true)
	if err == nil {
		_, _ = e.syncBossKOsLocked(ctx, now)
		e.evaluateBadgesLocked(ctx, now)
	}
	e.mu.Unlock()
	return ev, err
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
	grp.GET("/bosses", func(c echo.Context) error {
		bosses := e.buildBossBattles(c.Request().Context())
		return c.JSON(http.StatusOK, map[string]any{"bossBattles": bosses, "count": len(bosses)})
	})
	grp.GET("/stamps", func(c echo.Context) error {
		stamps := e.buildStamps(c.Request().Context())
		return c.JSON(http.StatusOK, map[string]any{"stamps": stamps, "count": len(stamps), "contract": DefaultUIContract()})
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
