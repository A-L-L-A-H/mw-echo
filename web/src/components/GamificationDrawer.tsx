import { useCallback, useEffect, useState } from 'react';
import {
  ArrowRight,
  Award,
  CheckCircle2,
  Flame,
  Skull,
  Sparkles,
  Swords,
  Target,
  Trophy,
  X,
  Zap,
} from 'lucide-react';
import {
  claimGameQuest,
  fetchSidekickContext,
  requestSidekickBrief,
  type GameBossBattle,
  type GameDailyQuest,
  type GameProfile,
  type GameStampRecord,
  type SidekickLiveContext,
  type SidekickMessage,
} from '../api';
import { formatCompactNumber } from '../utils';
import { ComicSticker, MonoBadge } from './UiPrimitives';

interface GamificationDrawerProps {
  isOpen: boolean;
  profile: GameProfile | null;
  onClose: () => void;
  onProfileUpdated: (nextProfile: GameProfile) => void;
  onReloadProfile: () => Promise<void>;
  onOpenArena: () => void;
  onJumpToProjectBoard: (projectSlug: string) => void;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

const BRIEF_PERSONAS = [
  { id: 'tactician', label: 'TACTICIAN', accent: '#3b82f6' },
  { id: 'hacker', label: 'HACKER', accent: '#10b981' },
  { id: 'oracle', label: 'ORACLE', accent: '#8b5cf6' },
  { id: 'dispatcher', label: 'DISPATCHER', accent: '#f59e0b' },
] as const;

export function GamificationDrawer({
  isOpen,
  profile,
  onClose,
  onProfileUpdated,
  onReloadProfile,
  onOpenArena,
  onJumpToProjectBoard,
  onNotify,
}: GamificationDrawerProps) {
  const [liveContext, setLiveContext] = useState<SidekickLiveContext | null>(null);
  const [briefPersona, setBriefPersona] = useState<string>('tactician');
  const [briefReply, setBriefReply] = useState<SidekickMessage | null>(null);
  const [loadingBrief, setLoadingBrief] = useState(false);
  const [claimingQuestId, setClaimingQuestId] = useState<string | null>(null);

  const loadTelemetry = useCallback(async () => {
    try {
      const ctx = await fetchSidekickContext();
      setLiveContext(ctx);
    } catch {
      // Soft fail while backend boots.
    }
  }, []);

  useEffect(() => {
    if (!isOpen) return;
    void loadTelemetry();
    void onReloadProfile();
  }, [isOpen, loadTelemetry, onReloadProfile]);

  useEffect(() => {
    if (!isOpen) return;
    const onKeyDown = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [isOpen, onClose]);

  if (!isOpen) {
    return null;
  }

  const levelPct = Math.min(100, Math.max(0, Math.round(profile?.levelPct ?? 0)));
  const bossBattles: GameBossBattle[] = profile?.bossBattles ?? [];
  const dailyQuests: GameDailyQuest[] = profile?.dailyQuests ?? [];
  const stamps: GameStampRecord[] = profile?.stamps ?? [];
  const earnedStampsCount = stamps.filter((st) => st.earned).length;
  const dailyBonusCap = profile?.dailyBonusCap ?? 500;
  const dailyBonusXp = profile?.dailyBonusXp ?? 0;

  const handleRequestMissionBrief = async () => {
    setLoadingBrief(true);
    try {
      const msg = await requestSidekickBrief(briefPersona);
      setBriefReply(msg);
      onNotify('ok', `${briefPersona.toUpperCase()} mission brief updated.`);
      await onReloadProfile();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Mission brief failed';
      onNotify('err', message);
    } finally {
      setLoadingBrief(false);
    }
  };

  const handleClaimQuest = async (quest: GameDailyQuest) => {
    setClaimingQuestId(quest.id);
    try {
      const claimReply = await claimGameQuest(quest.id);
      if (claimReply.profile) {
        onProfileUpdated(claimReply.profile);
      } else {
        await onReloadProfile();
      }
      onNotify('ok', `STAMP EARNED! +${quest.rewardXp} XP (${quest.title})`);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Quest claim failed';
      onNotify('err', message);
    } finally {
      setClaimingQuestId(null);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-black/70 backdrop-blur-xs">
      <div className="flex-1" onClick={onClose} aria-hidden="true" />

      <aside className="relative flex h-full w-full max-w-xl flex-col border-l-[3px] border-black bg-[#13131d] shadow-[-8px_0_0_#ffe031]">
        {/* Header */}
        <div className="border-b-[2.5px] border-black bg-[#08080c] px-4 py-3">
          <div className="flex items-center justify-between gap-2">
            <div className="flex items-center gap-2.5">
              <div className="-rotate-3 flex h-9 w-9 items-center justify-center border-2 border-black bg-[#ffe031] text-[#08080c] shadow-[3px_3px_0_#ff2e88]">
                <Trophy className="h-5 w-5" />
              </div>
              <div>
                <div className="flex items-center gap-2">
                  <span className="font-display text-base text-[#fff6de] uppercase riot-title-rgb">
                    ARCADE HUD // QUEST DRAWER
                  </span>
                  <ComicSticker tone="yellow" tilt="rotate-2">
                    RANK {profile?.rankGrade || liveContext?.rank || 'D'}
                  </ComicSticker>
                </div>
                <div className="font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                  LV.{profile?.level ?? 1} {profile?.rankTitle || 'ROOKIE RUNNER'} · COMBO{' '}
                  {(profile?.comboMult ?? 1).toFixed(1)}X · BONUS {dailyBonusXp}/{dailyBonusCap} XP
                </div>
              </div>
            </div>

            <button
              type="button"
              onClick={onClose}
              className="riot-btn bg-[#1b1b2b] p-1.5 text-[#fff6de] hover:bg-[#ff334b]"
              aria-label="Close Arcade HUD Drawer"
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          {/* XP Bar + Live Context Injection Strip */}
          <div className="mt-3 space-y-2 border-2 border-black bg-[#13131d] p-2.5">
            <div className="flex items-center justify-between font-mono text-[10px] font-extrabold">
              <span className="text-[#00f0ff]">
                {liveContext?.header ||
                  `[RANK: ${profile?.rankGrade || 'D'} | XP: ${profile?.totalXp ?? 0}/${profile?.nextLevelXp ?? 120} | COMBO: x${profile?.comboMult ?? 1}]`}
              </span>
              <span className="text-[#ffe031]">{levelPct}%</span>
            </div>
            <div className="h-3 w-full overflow-hidden border border-black bg-[#08080c]">
              <div
                className="riot-xp-stripe h-full bg-gradient-to-r from-[#ffe031] via-[#ff2e88] to-[#00f0ff] transition-all"
                style={{ width: `${levelPct}%` }}
              />
            </div>
            <div className="flex flex-wrap items-center gap-1.5 font-mono text-[10px] font-bold">
              <MonoBadge tone="err">
                <Flame className="h-3 w-3" />
                {profile?.streakDays ?? 0}D STREAK
              </MonoBadge>
              <MonoBadge tone="magenta">
                P0 PENDING: {liveContext?.p0TasksPending ?? 0}
              </MonoBadge>
              <MonoBadge tone="cyan">DOCS: {liveContext?.docsCount ?? 0}</MonoBadge>
              <MonoBadge tone="ok">
                9ROUTER SAVED: {formatCompactNumber(liveContext?.routerTokensSaved ?? 0)} TOK
              </MonoBadge>
            </div>
          </div>
        </div>

        <div className="riot-hazard-tape h-1.5 border-b-2 border-black" />

        {/* Body */}
        <div className="flex-1 space-y-4 overflow-y-auto bg-[#08080c] p-4">
          {/* Daily Mission Brief Speech-Bubble Widget */}
          <section className="border-2 border-black bg-[#13131d] p-3 shadow-[4px_4px_0_#00f0ff]">
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-1.5">
                <Sparkles className="h-4 w-4 text-[#ffe031]" />
                <span className="font-display text-xs text-[#fff6de] uppercase">
                  DAILY MISSION BRIEF
                </span>
              </div>
              <button
                type="button"
                disabled={loadingBrief}
                onClick={() => void handleRequestMissionBrief()}
                className="riot-btn inline-flex items-center gap-1 bg-[#ffe031] px-2.5 py-1 font-mono text-[10px] font-extrabold text-[#08080c] uppercase disabled:opacity-50"
              >
                <Zap className={`h-3 w-3 ${loadingBrief ? 'animate-spin' : ''}`} />
                {loadingBrief ? 'BRIEFING...' : 'RUN BRIEF'}
              </button>
            </div>

            <div className="mb-2 flex flex-wrap gap-1">
              {BRIEF_PERSONAS.map((persona) => {
                const selected = persona.id === briefPersona;
                return (
                  <button
                    key={persona.id}
                    type="button"
                    onClick={() => setBriefPersona(persona.id)}
                    className={`border border-black px-2 py-0.5 font-mono text-[10px] font-extrabold uppercase ${
                      selected ? 'text-[#08080c]' : 'bg-[#08080c] text-[#a6a2b8]'
                    }`}
                    style={selected ? { backgroundColor: persona.accent } : undefined}
                  >
                    {persona.label}
                  </button>
                );
              })}
            </div>

            <div className="border-2 border-black bg-[#08080c] p-2.5 font-sans text-xs leading-relaxed text-[#fff6de]">
              {briefReply ? (
                <div className="whitespace-pre-wrap">{briefReply.content}</div>
              ) : (
                <span className="font-mono text-[11px] text-[#a6a2b8]">
                  Hit RUN BRIEF for a 3-bullet operator sitrep from {briefPersona.toUpperCase()}.
                </span>
              )}
            </div>
          </section>

          {/* Active Boss Battles */}
          <section className="border-2 border-black bg-[#13131d] p-3 shadow-[4px_4px_0_#ff2e88]">
            <div className="mb-2 flex items-center justify-between">
              <div className="flex items-center gap-1.5">
                <Swords className="h-4 w-4 text-[#ff2e88]" />
                <span className="font-display text-xs text-[#fff6de] uppercase">
                  BOSS BATTLES ({bossBattles.filter((b) => !b.defeated).length} ALIVE)
                </span>
              </div>
              <MonoBadge tone="yellow">+500 XP KO</MonoBadge>
            </div>

            {bossBattles.length === 0 ? (
              <div className="font-mono text-[11px] text-[#a6a2b8]">
                No milestone bosses active yet.
              </div>
            ) : (
              <div className="space-y-2">
                {bossBattles.slice(0, 4).map((boss) => {
                  const hpPct = Math.min(100, Math.max(0, Math.round(boss.remainingPct ?? 0)));
                  return (
                    <div
                      key={boss.id}
                      className="border border-black bg-[#08080c] p-2.5 font-mono text-[11px]"
                    >
                      <div className="flex items-center justify-between gap-2">
                        <div className="flex items-center gap-1.5 truncate">
                          {boss.defeated ? (
                            <CheckCircle2 className="h-3.5 w-3.5 shrink-0 text-[#a3ff12]" />
                          ) : (
                            <Skull className="h-3.5 w-3.5 shrink-0 text-[#ff334b]" />
                          )}
                          <span className="truncate font-extrabold text-[#fff6de]">
                            {boss.milestone || 'GENERAL RAID'}
                          </span>
                        </div>
                        <button
                          type="button"
                          onClick={() => {
                            onJumpToProjectBoard(boss.project);
                            onClose();
                          }}
                          className="inline-flex items-center gap-0.5 text-[10px] font-extrabold text-[#ffe031] hover:underline"
                        >
                          {boss.project}
                          <ArrowRight className="h-3 w-3" />
                        </button>
                      </div>
                      <div className="mt-1.5 flex items-center justify-between text-[10px] text-[#a6a2b8]">
                        <span>
                          HP {boss.currentHp}/{boss.maxHp}
                        </span>
                        <span>
                          {boss.doneTasks}/{boss.totalTasks} DONE ({hpPct}% LEFT)
                        </span>
                      </div>
                      <div className="mt-1 h-2 w-full overflow-hidden border border-black bg-[#1b1b2b]">
                        <div
                          className="h-full transition-all"
                          style={{
                            width: `${hpPct}%`,
                            backgroundColor: boss.defeated ? '#a3ff12' : '#ff334b',
                          }}
                        />
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </section>

          {/* Daily Quests */}
          <section className="border-2 border-black bg-[#13131d] p-3 shadow-[4px_4px_0_#a3ff12]">
            <div className="mb-2 flex items-center justify-between">
              <div className="flex items-center gap-1.5">
                <Target className="h-4 w-4 text-[#a3ff12]" />
                <span className="font-display text-xs text-[#fff6de] uppercase">
                  DAILY QUESTS
                </span>
              </div>
              <MonoBadge tone="cyan">
                {dailyQuests.filter((q) => q.claimed).length}/{dailyQuests.length} CLAIMED
              </MonoBadge>
            </div>

            <div className="space-y-2">
              {dailyQuests.map((quest) => {
                const canClaim = quest.completed && !quest.claimed;
                const pct =
                  quest.target > 0
                    ? Math.min(100, Math.round((quest.progress / quest.target) * 100))
                    : 0;
                return (
                  <div key={quest.id} className="border border-black bg-[#08080c] p-2.5">
                    <div className="flex items-center justify-between gap-2">
                      <div>
                        <div className="font-mono text-xs font-extrabold text-[#fff6de]">
                          {quest.title}{' '}
                          <span className="text-[#ffe031]">(+{quest.rewardXp} XP)</span>
                        </div>
                        <div className="font-mono text-[10px] text-[#a6a2b8]">
                          {quest.description}
                        </div>
                      </div>
                      {quest.claimed ? (
                        <MonoBadge tone="ok">CLAIMED</MonoBadge>
                      ) : canClaim ? (
                        <button
                          type="button"
                          disabled={claimingQuestId === quest.id}
                          onClick={() => void handleClaimQuest(quest)}
                          className="riot-btn bg-[#a3ff12] px-2.5 py-1 font-mono text-[10px] font-extrabold text-[#08080c] uppercase disabled:opacity-50"
                        >
                          {claimingQuestId === quest.id ? '...' : 'CLAIM'}
                        </button>
                      ) : (
                        <MonoBadge tone="neutral">
                          {quest.progress}/{quest.target}
                        </MonoBadge>
                      )}
                    </div>
                    <div className="mt-1.5 h-1.5 w-full overflow-hidden border border-black bg-[#1b1b2b]">
                      <div
                        className="h-full transition-all"
                        style={{
                          width: `${pct}%`,
                          backgroundColor: quest.claimed || canClaim ? '#a3ff12' : '#00f0ff',
                        }}
                      />
                    </div>
                  </div>
                );
              })}
            </div>
          </section>

          {/* Retro Arcade Stamp Collection */}
          <section className="border-2 border-black bg-[#13131d] p-3">
            <div className="mb-2 flex items-center justify-between">
              <div className="flex items-center gap-1.5">
                <Award className="h-4 w-4 text-[#ffe031]" />
                <span className="font-display text-xs text-[#fff6de] uppercase">
                  RETRO ARCADE STAMPS ({earnedStampsCount}/{stamps.length})
                </span>
              </div>
              <button
                type="button"
                onClick={() => {
                  onOpenArena();
                  onClose();
                }}
                className="font-mono text-[10px] font-extrabold text-[#00f0ff] uppercase hover:underline"
              >
                OPEN 11 ARENA →
              </button>
            </div>

            {stamps.length === 0 ? (
              <div className="font-mono text-[11px] text-[#a6a2b8]">
                Complete daily quests to stamp your arcade card.
              </div>
            ) : (
              <div className="grid grid-cols-2 gap-2">
                {stamps.map((stamp) => (
                  <div
                    key={stamp.id}
                    className={`border-2 border-black p-2 font-mono text-[10px] ${
                      stamp.earned ? 'bg-[#08080c]' : 'bg-[#0d0d14] opacity-55'
                    }`}
                    style={
                      stamp.earned ? { boxShadow: `3px 3px 0 ${stamp.stampColor}` } : undefined
                    }
                  >
                    <div className="flex items-center justify-between gap-1">
                      <span
                        className="border border-black px-1.5 py-0.5 font-extrabold uppercase"
                        style={{
                          backgroundColor: stamp.earned ? stamp.stampColor : '#1b1b2b',
                          color: stamp.earned ? '#08080c' : '#a6a2b8',
                        }}
                      >
                        {stamp.title}
                      </span>
                      {stamp.earned ? (
                        <span className="font-extrabold text-[#a3ff12]">x{stamp.earnedCount}</span>
                      ) : null}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>
        </div>
      </aside>
    </div>
  );
}
