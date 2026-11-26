import { useState } from 'react';
import {
  ArrowRight,
  Award,
  CheckCircle2,
  Flame,
  Lock,
  RefreshCw,
  Skull,
  Sparkles,
  Swords,
  Target,
  Trophy,
  Zap,
} from 'lucide-react';
import {
  claimGameQuest,
  type GameBossBattle,
  type GameDailyQuest,
  type GameProfile,
} from '../api';
import { formatCompactNumber, formatShortTime } from '../utils';
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
} from './UiPrimitives';

interface ArenaTabProps {
  profile: GameProfile | null;
  isLoading: boolean;
  onProfileUpdated: (nextProfile: GameProfile) => void;
  onReloadArena: () => Promise<void>;
  onJumpToProjectBoard: (projectSlug: string) => void;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

const HP_SEGMENTS = [10, 20, 30, 40, 50, 60, 70, 80, 90, 100];

export function ArenaTab({
  profile,
  isLoading,
  onProfileUpdated,
  onReloadArena,
  onJumpToProjectBoard,
  onNotify,
}: ArenaTabProps) {
  const [claimingQuestId, setClaimingQuestId] = useState<string | null>(null);

  const handleClaimQuest = async (quest: GameDailyQuest) => {
    setClaimingQuestId(quest.id);
    try {
      const claimReply = await claimGameQuest(quest.id);
      if (claimReply.profile) {
        onProfileUpdated(claimReply.profile);
      } else {
        await onReloadArena();
      }
      onNotify('ok', `CLAIMED +${claimReply.awarded || quest.rewardXp} XP from "${quest.title}"!`);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Quest claim failed';
      onNotify('err', message);
    } finally {
      setClaimingQuestId(null);
    }
  };

  const bossBattles: GameBossBattle[] = profile?.bossBattles ?? [];
  const dailyQuests: GameDailyQuest[] = profile?.dailyQuests ?? [];
  const badges = profile?.badges ?? [];
  const recentEvents = profile?.recentEvents ?? [];

  const levelPct = Math.min(100, Math.max(0, Math.round(profile?.levelPct ?? 0)));
  const unlockedBadgesCount = badges.filter((badge) => badge.unlocked).length;

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 11"
        sticker="RIOT ARENA"
        title="BOSS RAIDS, DAILY QUESTS & XP STAMPS"
        subtitle="TURN PROJECT MILESTONES INTO ARCADE BOSS HP BARS, STACK STREAK COMBOS & COLLECT RIOT BADGES"
        tone="magenta"
        actions={
          <button
            type="button"
            onClick={() => void onReloadArena()}
            className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c]"
          >
            <RefreshCw className={`h-3.5 w-3.5 ${isLoading ? 'animate-spin' : ''}`} />
            SYNC ARENA
          </button>
        }
      />

      {/* Fighter Rank Banner Card */}
      <section className="relative overflow-hidden border-[3px] border-black bg-[#13131d] p-5 shadow-[7px_7px_0_#ffe031]">
        <div className="riot-halftone-corner pointer-events-none absolute inset-y-0 right-0 w-1/3 opacity-45" />
        <div className="relative flex flex-wrap items-center justify-between gap-5">
          <div className="flex flex-wrap items-center gap-4">
            <div className="-rotate-3 flex h-20 w-20 flex-col items-center justify-center border-[3px] border-black bg-[#ffe031] text-[#08080c] shadow-[5px_5px_0_#ff2e88]">
              <span className="font-mono text-[10px] font-extrabold leading-none">LEVEL</span>
              <span className="font-display text-3xl leading-none">{profile?.level ?? 1}</span>
            </div>

            <div>
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-display text-2xl text-[#fff6de] uppercase riot-title-rgb">
                  {profile?.rankTitle || 'ROOKIE RUNNER'}
                </span>
                {(profile?.comboMult ?? 1) > 1 ? (
                  <ComicSticker tone="magenta" tilt="rotate-2">
                    {profile?.comboMult.toFixed(1)}X COMBO!
                  </ComicSticker>
                ) : null}
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-2 font-mono text-xs font-bold">
                <MonoBadge tone="yellow">
                  <Zap className="h-3 w-3" />
                  {formatCompactNumber(profile?.totalXp ?? 0)} TOTAL XP
                </MonoBadge>
                <MonoBadge tone="err">
                  <Flame className="h-3 w-3" />
                  {profile?.streakDays ?? 0}D STREAK
                </MonoBadge>
                <MonoBadge tone="cyan">
                  <Trophy className="h-3 w-3" />
                  {unlockedBadgesCount}/{badges.length} STAMPS
                </MonoBadge>
              </div>
            </div>
          </div>

          {/* XP Gauge to Next Level */}
          <div className="w-full min-w-[260px] max-w-md flex-1">
            <div className="mb-1.5 flex items-center justify-between font-mono text-xs font-extrabold">
              <span className="text-[#00f0ff]">NEXT LEVEL PROGRESS</span>
              <span className="text-[#ffe031]">
                {formatCompactNumber(profile?.totalXp ?? 0)} /{' '}
                {formatCompactNumber(profile?.nextLevelXp ?? 100)} XP ({levelPct}%)
              </span>
            </div>
            <div className="h-5 w-full overflow-hidden border-[2.5px] border-black bg-[#08080c]">
              <div
                className="riot-xp-stripe h-full border-r-2 border-black bg-gradient-to-r from-[#ffe031] via-[#ff2e88] to-[#00f0ff] transition-all"
                style={{ width: `${levelPct}%` }}
              />
            </div>
          </div>
        </div>
      </section>

      {/* 1. Boss Battles Grid */}
      <SectionCard
        title="BOSS BATTLES // PROJECT MILESTONE RAIDS"
        subtitle="Each project milestone is a live Arcade Boss — complete tasks in 03 BOUNTY to drain its HP bar!"
        icon={<Swords className="h-4 w-4" />}
        sticker={`${bossBattles.filter((boss) => !boss.defeated).length} ALIVE`}
        stickerTone="err"
        accent="magenta"
      >
        {bossBattles.length === 0 ? (
          <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] py-10 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
            NO BOSS BATTLES SPAWNED YET. IMPORT A REPO OR CREATE PROJECT MILESTONE TASKS!
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            {bossBattles.map((boss) => {
              const hpPct = Math.min(100, Math.max(0, Math.round(boss.remainingPct ?? 0)));
              const hpColor = boss.defeated
                ? '#a3ff12'
                : hpPct > 60
                  ? '#ff334b'
                  : hpPct > 25
                    ? '#ffe031'
                    : '#00f0ff';

              return (
                <article
                  key={boss.id}
                  className={`relative border-[2.5px] border-black p-4 transition-transform hover:-translate-x-0.5 hover:-translate-y-0.5 ${
                    boss.defeated
                      ? 'bg-[#08080c] opacity-85 shadow-[5px_5px_0_#a3ff12]'
                      : 'bg-[#08080c] shadow-[5px_5px_0_#ff334b]'
                  }`}
                >
                  <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        {boss.defeated ? (
                          <CheckCircle2 className="h-4 w-4 text-[#a3ff12]" />
                        ) : (
                          <Skull className="h-4 w-4 text-[#ff334b]" />
                        )}
                        <h3 className="font-display text-sm text-[#fff6de] uppercase">
                          {boss.milestone || 'GENERAL RAID'}
                        </h3>
                        <MonoBadge tone="cyan">{boss.project}</MonoBadge>
                      </div>
                    </div>

                    <ComicSticker
                      tone={boss.defeated ? 'ok' : 'err'}
                      tilt={boss.defeated ? '-rotate-3' : 'rotate-2'}
                    >
                      {boss.defeated ? 'DEFEATED!' : boss.threatLabel || 'RAID TITAN'}
                    </ComicSticker>
                  </div>

                  {/* Segmented 10-Block Arcade Boss HP Bar */}
                  <div className="mb-3 border-2 border-black bg-[#13131d] p-2.5">
                    <div className="mb-1.5 flex items-center justify-between font-mono text-[11px] font-extrabold">
                      <span className="text-[#a6a2b8]">
                        HP {boss.currentHp} / {boss.maxHp}
                      </span>
                      <span style={{ color: hpColor }}>{hpPct}% REMAINING</span>
                    </div>
                    <div className="grid grid-cols-10 gap-1">
                      {HP_SEGMENTS.map((threshold) => {
                        const lit = !boss.defeated && hpPct >= threshold - 9;
                        return (
                          <div
                            key={threshold}
                            className="h-4 border border-black transition-all"
                            style={{
                              backgroundColor: lit ? hpColor : '#1b1b2b',
                            }}
                          />
                        );
                      })}
                    </div>
                  </div>

                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="flex items-center gap-2 font-mono text-[11px] font-bold text-[#a6a2b8]">
                      <MonoBadge tone="yellow">
                        {boss.doneTasks}/{boss.totalTasks} BOUNTIES CLEARED
                      </MonoBadge>
                    </div>
                    <button
                      type="button"
                      onClick={() => onJumpToProjectBoard(boss.project)}
                      className="riot-btn inline-flex items-center gap-1 bg-[#ffe031] px-3 py-1 font-mono text-[11px] font-extrabold text-[#08080c] uppercase"
                    >
                      ATTACK BOARD
                      <ArrowRight className="h-3 w-3" />
                    </button>
                  </div>
                </article>
              );
            })}
          </div>
        )}
      </SectionCard>

      {/* 2. Daily Quests Board + Live XP Feed */}
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-12">
        <SectionCard
          title="DAILY QUESTS BOARD"
          subtitle="Hit daily targets across Tasks, Docs, Gateway & WhatsApp to claim bonus XP."
          icon={<Target className="h-4 w-4" />}
          sticker="DAILY LOOT"
          stickerTone="yellow"
          accent="yellow"
          className="xl:col-span-7"
        >
          <div className="space-y-3">
            {dailyQuests.length === 0 ? (
              <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] py-8 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
                NO ACTIVE DAILY QUESTS.
              </div>
            ) : (
              dailyQuests.map((quest) => {
                const pct =
                  quest.target > 0
                    ? Math.min(100, Math.round((quest.progress / quest.target) * 100))
                    : 0;
                const canClaim = quest.completed && !quest.claimed;
                return (
                  <div
                    key={quest.id}
                    className={`border-2 border-black bg-[#08080c] p-3.5 ${
                      canClaim ? 'shadow-[5px_5px_0_#a3ff12]' : 'shadow-[4px_4px_0_#000]'
                    }`}
                  >
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <div>
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-display text-sm text-[#fff6de] uppercase">
                            {quest.title}
                          </span>
                          <MonoBadge tone="yellow">+{quest.rewardXp} XP</MonoBadge>
                        </div>
                        <p className="mt-0.5 font-mono text-[11px] text-[#a6a2b8]">
                          {quest.description}
                        </p>
                      </div>

                      {quest.claimed ? (
                        <ComicSticker tone="ok" tilt="-rotate-2">
                          CLAIMED
                        </ComicSticker>
                      ) : canClaim ? (
                        <button
                          type="button"
                          disabled={claimingQuestId === quest.id}
                          onClick={() => void handleClaimQuest(quest)}
                          className="riot-btn inline-flex items-center gap-1.5 bg-[#a3ff12] px-4 py-1.5 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
                        >
                          <Sparkles className="h-3.5 w-3.5" />
                          {claimingQuestId === quest.id ? '...' : `CLAIM +${quest.rewardXp} XP`}
                        </button>
                      ) : (
                        <MonoBadge tone="neutral">
                          {quest.progress}/{quest.target}
                        </MonoBadge>
                      )}
                    </div>

                    <div className="mt-2.5 h-3 w-full overflow-hidden border-2 border-black bg-[#1b1b2b]">
                      <div
                        className="riot-xp-stripe h-full transition-all"
                        style={{
                          width: `${pct}%`,
                          backgroundColor: quest.claimed || canClaim ? '#a3ff12' : '#00f0ff',
                        }}
                      />
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </SectionCard>

        {/* Live XP Action Feed */}
        <SectionCard
          title="LIVE XP ACTION FEED"
          subtitle="Real-time combo-multiplied XP ticker."
          icon={<Zap className="h-4 w-4" />}
          sticker="FEED"
          stickerTone="cyan"
          accent="cyan"
          className="xl:col-span-5"
        >
          <div className="max-h-[430px] space-y-2 overflow-y-auto pr-1">
            {recentEvents.length === 0 ? (
              <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] py-8 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
                COMPLETE A TASK, SEND A PROMPT, OR IMPORT A REPO TO EARN XP!
              </div>
            ) : (
              recentEvents.map((xpEvent) => (
                <div
                  key={xpEvent.id}
                  className="flex items-center justify-between gap-2 border-2 border-black bg-[#08080c] px-3 py-2 shadow-[3px_3px_0_#000]"
                >
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <MonoBadge tone="cream">{xpEvent.eventType}</MonoBadge>
                      <span className="truncate font-mono text-xs font-bold text-[#fff6de]">
                        {xpEvent.label}
                      </span>
                    </div>
                    <div className="mt-0.5 font-mono text-[10px] text-[#a6a2b8]">
                      {xpEvent.refId ? `${xpEvent.refId} · ` : ''}
                      {formatShortTime(xpEvent.createdAt)}
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    {xpEvent.multiplier > 1 ? (
                      <MonoBadge tone="magenta">{xpEvent.multiplier.toFixed(1)}X</MonoBadge>
                    ) : null}
                    <MonoBadge tone="ok">+{xpEvent.awardedXp} XP</MonoBadge>
                  </div>
                </div>
              ))
            )}
          </div>
        </SectionCard>
      </div>

      {/* 3. Achievement Stamp Wall */}
      <SectionCard
        title="ACHIEVEMENT STAMP WALL"
        subtitle="Unlock comic badges across combat, vaulting, token piracy, comms & codex mastery."
        icon={<Award className="h-4 w-4" />}
        sticker={`${unlockedBadgesCount}/${badges.length} UNLOCKED`}
        stickerTone="ok"
        accent="ok"
      >
        <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          {badges.map((badge, badgeIdx) => {
            const tiltClass = badgeIdx % 2 === 0 ? '-rotate-1' : 'rotate-1';
            const accentHex = badge.stampColor || '#ffe031';
            return (
              <div
                key={badge.id}
                className={`relative border-[2.5px] border-black p-3.5 transition-transform hover:rotate-0 ${tiltClass} ${
                  badge.unlocked
                    ? 'bg-[#08080c] shadow-[5px_5px_0_#000]'
                    : 'bg-[#0d0d14] opacity-55'
                }`}
                style={badge.unlocked ? { boxShadow: `5px 5px 0 ${accentHex}` } : undefined}
              >
                <div className="mb-2 flex items-center justify-between gap-2">
                  <span
                    className="inline-flex items-center gap-1 border-2 border-black px-2 py-0.5 font-comic text-sm tracking-wider uppercase"
                    style={{
                      backgroundColor: badge.unlocked ? accentHex : '#1b1b2b',
                      color: badge.unlocked ? '#08080c' : '#a6a2b8',
                    }}
                  >
                    {badge.unlocked ? (
                      <Trophy className="h-3.5 w-3.5" />
                    ) : (
                      <Lock className="h-3.5 w-3.5" />
                    )}
                    {badge.title}
                  </span>
                  {badge.unlocked ? <MonoBadge tone="ok">GOT</MonoBadge> : null}
                </div>

                <p className="font-mono text-[11px] text-[#fff6de]/85">{badge.description}</p>
                {badge.unlocked && badge.unlockedAt ? (
                  <div className="mt-2 font-mono text-[9px] font-bold uppercase text-[#a6a2b8]">
                    STAMPED {formatShortTime(badge.unlockedAt)}
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      </SectionCard>
    </div>
  );
}
