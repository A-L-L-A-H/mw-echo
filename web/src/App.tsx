import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Activity,
  BookOpen,
  Bot,
  Brain,
  CheckSquare,
  Cloud,
  Cpu,
  Flame,
  FolderKanban,
  Globe2,
  KeyRound,
  Laptop,
  LayoutDashboard,
  MessageSquare,
  Network,
  Package,
  Radar,
  Radio,
  Terminal,
  Trophy,
  Workflow,
} from 'lucide-react';
import {
  fetchAiAgents,
  fetchAiProviders,
  fetchAiStats,
  fetchFreeModelCatalog,
  fetchGameProfile,
  fetchHubStats,
  fetchProjects,
  fetchStorageObjects,
  fetchStoragePool,
  fetchWhatsAppDevices,
  fetchWhatsAppStats,
  type AiAgentRoute,
  type AiGatewayStats,
  type AiProviderRecord,
  type FreeModelCatalogEntry,
  type GameProfile,
  type HubStats,
  type PoolSummary,
  type PooledStorageObject,
  type ProjectSummary,
  type WhatsAppDevice,
  type WhatsAppStats,
} from './api';
import { AiGatewayTab, type ProviderDraftPrefill } from './components/AiGatewayTab';
import { ArenaTab } from './components/ArenaTab';
import { BlueprintsTab } from './components/BlueprintsTab';
import { CoderTab } from './components/CoderTab';
import { DesktopHostTab } from './components/DesktopHostTab';
import { DocsTab } from './components/DocsTab';
import { FleetCockpitTab } from './components/FleetCockpitTab';
import { FlowsTab } from './components/FlowsTab';
import { FreeModelsTab } from './components/FreeModelsTab';
import { MemVaultTab } from './components/MemVaultTab';
import { ObservatoryTab } from './components/ObservatoryTab';
import { OverviewTab } from './components/OverviewTab';
import { ProjectsTab } from './components/ProjectsTab';
import { SidekickDrawer } from './components/SidekickDrawer';
import { StoragePoolTab } from './components/StoragePoolTab';
import { TasksTab } from './components/TasksTab';
import { ToastStack, ComicSticker, MonoBadge, type ToastNotice } from './components/UiPrimitives';
import { VaultEnvTab } from './components/VaultEnvTab';
import { WhatsAppTab } from './components/WhatsAppTab';
import { WorldRadarTab } from './components/WorldRadarTab';

type NavTabId =
  | 'overview'
  | 'projects'
  | 'tasks'
  | 'whatsapp'
  | 'gateway'
  | 'free-models'
  | 'observatory'
  | 'storage'
  | 'docs'
  | 'coder'
  | 'arena'
  | 'world-radar'
  | 'flows'
  | 'blueprints'
  | 'vault-env'
  | 'desktop'
  | 'memvault'
  | 'fleet';

interface NavTabMeta {
  id: NavTabId;
  sceneNum: string;
  stageName: string;
  icon: typeof LayoutDashboard;
}

const NAV_TABS: NavTabMeta[] = [
  { id: 'overview', sceneNum: '01', stageName: 'HQ STAGE', icon: LayoutDashboard },
  { id: 'projects', sceneNum: '02', stageName: 'ARCS', icon: FolderKanban },
  { id: 'tasks', sceneNum: '03', stageName: 'BOUNTY', icon: CheckSquare },
  { id: 'whatsapp', sceneNum: '04', stageName: 'COMMS', icon: MessageSquare },
  { id: 'gateway', sceneNum: '05', stageName: 'RIG', icon: Cpu },
  { id: 'free-models', sceneNum: '06', stageName: 'LOOT', icon: Radar },
  { id: 'observatory', sceneNum: '07', stageName: 'RADAR', icon: Radio },
  { id: 'storage', sceneNum: '08', stageName: 'VAULT', icon: Cloud },
  { id: 'docs', sceneNum: '09', stageName: 'CODEX', icon: BookOpen },
  { id: 'coder', sceneNum: '10', stageName: 'CODER', icon: Terminal },
  { id: 'arena', sceneNum: '11', stageName: 'ARENA', icon: Trophy },
  { id: 'world-radar', sceneNum: '12', stageName: 'WORLD', icon: Globe2 },
  { id: 'flows', sceneNum: '13', stageName: 'FLOWS', icon: Workflow },
  { id: 'blueprints', sceneNum: '14', stageName: 'PACKS', icon: Package },
  { id: 'vault-env', sceneNum: '15', stageName: 'SECRETS', icon: KeyRound },
  { id: 'desktop', sceneNum: '16', stageName: 'DESKTOP', icon: Laptop },
  { id: 'memvault', sceneNum: '18', stageName: 'MEMORY', icon: Brain },
  { id: 'fleet', sceneNum: '19', stageName: 'FLEET', icon: Network },
];

export function App() {
  const [activeTab, setActiveTab] = useState<NavTabId>('overview');
  const [hubStats, setHubStats] = useState<HubStats | null>(null);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [poolSummary, setPoolSummary] = useState<PoolSummary | null>(null);
  const [storageObjects, setStorageObjects] = useState<PooledStorageObject[]>([]);
  const [aiStats, setAiStats] = useState<AiGatewayStats | null>(null);
  const [aiProviders, setAiProviders] = useState<AiProviderRecord[]>([]);
  const [aiAgents, setAiAgents] = useState<AiAgentRoute[]>([]);
  const [catalogEntries, setCatalogEntries] = useState<FreeModelCatalogEntry[]>([]);
  const [aiAvailable, setAiAvailable] = useState(false);

  const [whatsappStats, setWhatsappStats] = useState<WhatsAppStats | null>(null);
  const [whatsappDevices, setWhatsappDevices] = useState<WhatsAppDevice[]>([]);
  const [loadingWhatsApp, setLoadingWhatsApp] = useState(false);

  const [gameProfile, setGameProfile] = useState<GameProfile | null>(null);
  const [loadingGame, setLoadingGame] = useState(false);
  const [sidekickOpen, setSidekickOpen] = useState(false);

  const [selectedTaskProject, setSelectedTaskProject] = useState('');
  const [providerPrefill, setProviderPrefill] = useState<ProviderDraftPrefill | null>(null);
  const [refreshingAll, setRefreshingAll] = useState(false);
  const [loadingProjects, setLoadingProjects] = useState(false);
  const [loadingStorage, setLoadingStorage] = useState(false);
  const [notices, setNotices] = useState<ToastNotice[]>([]);
  const noticeCounterRef = useRef(1);

  const pushNotice = useCallback((tone: 'ok' | 'err' | 'info', message: string) => {
    const nextId = noticeCounterRef.current;
    noticeCounterRef.current += 1;
    setNotices((prev) => [...prev.slice(-3), { id: nextId, tone, message }]);
    window.setTimeout(() => {
      setNotices((prev) => prev.filter((notice) => notice.id !== nextId));
    }, 4200);
  }, []);

  const dismissNotice = useCallback((noticeId: number) => {
    setNotices((prev) => prev.filter((notice) => notice.id !== noticeId));
  }, []);

  const reloadHubCore = useCallback(async () => {
    try {
      const statsReply = await fetchHubStats();
      setHubStats(statsReply);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to load hub stats';
      pushNotice('err', message);
    }
  }, [pushNotice]);

  const reloadProjects = useCallback(async () => {
    setLoadingProjects(true);
    try {
      const reply = await fetchProjects();
      setProjects(reply.projects ?? []);
    } catch {
      // Backend /api/projects may still be initializing.
    } finally {
      setLoadingProjects(false);
    }
  }, []);

  const reloadStorage = useCallback(
    async (searchTerm = '') => {
      setLoadingStorage(true);
      try {
        const [poolReply, objectsReply] = await Promise.all([
          fetchStoragePool(),
          fetchStorageObjects(searchTerm),
        ]);
        setPoolSummary(poolReply);
        setStorageObjects(objectsReply.objects ?? []);
      } catch (caughtError) {
        const message =
          caughtError instanceof Error ? caughtError.message : 'Failed to load 9Drive storage pool';
        pushNotice('err', message);
      } finally {
        setLoadingStorage(false);
      }
    },
    [pushNotice],
  );

  const reloadAiGateway = useCallback(async () => {
    const [statsOutcome, providersOutcome, agentsOutcome, catalogOutcome] =
      await Promise.allSettled([
        fetchAiStats(),
        fetchAiProviders(),
        fetchAiAgents(),
        fetchFreeModelCatalog(),
      ]);

    let anySuccess = false;
    if (statsOutcome.status === 'fulfilled') {
      setAiStats(statsOutcome.value);
      anySuccess = true;
    }
    if (providersOutcome.status === 'fulfilled') {
      setAiProviders(providersOutcome.value);
      anySuccess = true;
    }
    if (agentsOutcome.status === 'fulfilled') {
      setAiAgents(agentsOutcome.value);
      anySuccess = true;
    }
    if (catalogOutcome.status === 'fulfilled') {
      setCatalogEntries(catalogOutcome.value.entries);
      anySuccess = true;
    }
    setAiAvailable(anySuccess);
  }, []);

  const reloadWhatsApp = useCallback(async () => {
    setLoadingWhatsApp(true);
    try {
      const [statsOutcome, devicesOutcome] = await Promise.allSettled([
        fetchWhatsAppStats(),
        fetchWhatsAppDevices(),
      ]);
      if (statsOutcome.status === 'fulfilled') {
        setWhatsappStats(statsOutcome.value);
      }
      if (devicesOutcome.status === 'fulfilled') {
        setWhatsappDevices(devicesOutcome.value.devices ?? []);
      }
    } finally {
      setLoadingWhatsApp(false);
    }
  }, []);

  const reloadGameProfile = useCallback(async () => {
    setLoadingGame(true);
    try {
      const freshProfile = await fetchGameProfile();
      setGameProfile(freshProfile);
    } catch {
      // Backend /api/game/profile may still be starting.
    } finally {
      setLoadingGame(false);
    }
  }, []);

  const reloadAll = useCallback(async () => {
    setRefreshingAll(true);
    try {
      await Promise.all([
        reloadHubCore(),
        reloadProjects(),
        reloadStorage(),
        reloadAiGateway(),
        reloadWhatsApp(),
        reloadGameProfile(),
      ]);
    } finally {
      setRefreshingAll(false);
    }
  }, [
    reloadHubCore,
    reloadProjects,
    reloadStorage,
    reloadAiGateway,
    reloadWhatsApp,
    reloadGameProfile,
  ]);

  useEffect(() => {
    void reloadAll();
  }, [reloadAll]);

  // Global Ctrl+K / Cmd+K shortcut for Shadow Sidekick Drawer
  useEffect(() => {
    const handleGlobalKeyDown = (ev: KeyboardEvent) => {
      if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 'k') {
        ev.preventDefault();
        setSidekickOpen((prev) => !prev);
      }
    };
    window.addEventListener('keydown', handleGlobalKeyDown);
    return () => window.removeEventListener('keydown', handleGlobalKeyDown);
  }, []);

  const handleOpenProjectBoard = useCallback((projectSlug: string) => {
    setSelectedTaskProject(projectSlug);
    setActiveTab('tasks');
  }, []);

  const handleAddProviderShortcut = useCallback((prefill: ProviderDraftPrefill) => {
    setProviderPrefill(prefill);
    setActiveTab('gateway');
  }, []);

  const handleConsumePrefill = useCallback(() => {
    setProviderPrefill(null);
  }, []);

  const openTasksCount = Math.max(
    0,
    (hubStats?.tasksCount ?? 0) - (hubStats?.tasksByStatus?.done ?? 0),
  );

  const claimableQuestsCount = (gameProfile?.dailyQuests ?? []).filter(
    (quest) => quest.completed && !quest.claimed,
  ).length;

  const tabBadgeCount = (tabId: NavTabId): string | null => {
    switch (tabId) {
      case 'projects':
        return projects.length > 0 ? String(projects.length) : null;
      case 'tasks':
        return openTasksCount > 0 ? String(openTasksCount) : null;
      case 'whatsapp': {
        const totalDevs = whatsappStats?.devicesCount ?? whatsappDevices.length;
        const connectedDevs =
          whatsappStats?.connectedCount ??
          whatsappDevices.filter((dev) => dev.isConnected).length;
        return totalDevs > 0 ? `${connectedDevs}/${totalDevs}` : null;
      }
      case 'gateway':
        return aiProviders.length > 0 ? String(aiProviders.length) : null;
      case 'free-models':
        return catalogEntries.length > 0 ? String(catalogEntries.length) : null;
      case 'storage':
        return poolSummary && poolSummary.providers.length > 0
          ? String(poolSummary.providers.length)
          : null;
      case 'docs':
        return hubStats && hubStats.docsCount > 0 ? String(hubStats.docsCount) : null;
      case 'arena':
        return claimableQuestsCount > 0
          ? `!${claimableQuestsCount}`
          : gameProfile
            ? `LV${gameProfile.level}`
            : null;
      default:
        return null;
    }
  };

  const activeStageMeta = NAV_TABS.find((navTab) => navTab.id === activeTab) ?? NAV_TABS[0];
  const hudLevelPct = Math.min(100, Math.max(0, Math.round(gameProfile?.levelPct ?? 0)));

  return (
    <div className="riot-canvas relative flex min-h-screen flex-col overflow-x-clip text-[#fff6de]">
      {/* Giant faint tilted comic-zine watermark */}
      <div
        aria-hidden="true"
        className="pointer-events-none fixed inset-0 z-0 flex items-center justify-center overflow-hidden select-none"
      >
        <div className="-rotate-12 font-display text-[17vw] leading-none tracking-tighter text-[#ffe031]/[0.03] uppercase">
          RIOT//CHAOS
        </div>
      </div>

      {/* Subtle cinema CRT scanline overlay */}
      <div aria-hidden="true" className="riot-scanlines pointer-events-none fixed inset-0 z-40" />

      {/* Cinema Letterbox + Arcade Top Command Deck */}
      <header className="sticky top-0 z-30 border-b-[3px] border-black bg-[#08080c]/95 shadow-[0_6px_0_#000] backdrop-blur">
        {/* Top Arcade Gamification HUD & Film-Strip Status Bar */}
        <div className="border-b-2 border-black bg-[#050508] px-4 py-1.5">
          <div className="mx-auto flex max-w-[1620px] flex-wrap items-center justify-between gap-2.5 font-mono text-[11px] font-bold tracking-wider uppercase">
            {/* Left: REC + Scene + Live Signal */}
            <div className="flex flex-wrap items-center gap-3">
              <span className="inline-flex items-center gap-1.5 text-[#ff334b]">
                <span className="riot-rec-dot inline-block h-2.5 w-2.5 rounded-full bg-[#ff334b]" />
                [REC ● 24FPS]
              </span>
              <span className="text-[#ffe031]">
                SCENE {activeStageMeta.sceneNum}/{NAV_TABS.length} :: {activeStageMeta.stageName}
              </span>
              <span className="hidden items-center gap-1 text-[#a3ff12] xl:inline-flex">
                <Activity className="h-3.5 w-3.5" />
                {aiAvailable ? 'SIGNAL LOCKED' : 'HUB ONLINE'}
              </span>
            </div>

            {/* Center/Right: Live Arcade Gamification HUD + Shadow Sidekick Trigger */}
            <div className="flex flex-wrap items-center gap-2.5">
              <button
                type="button"
                onClick={() => setActiveTab('arena')}
                className="group flex items-center gap-2 border-2 border-black bg-[#13131d] px-2.5 py-1 shadow-[3px_3px_0_#000] transition-colors hover:border-[#ffe031]"
                title="Open 11 ARENA"
              >
                <span className="bg-[#ffe031] px-1.5 py-0.5 font-display text-[10px] text-[#08080c]">
                  LV.{gameProfile?.level ?? 1}
                </span>
                <span className="hidden text-[10px] font-extrabold text-[#fff6de] sm:inline">
                  {gameProfile?.rankTitle || 'RUNNER'}
                </span>
                <div className="h-2.5 w-20 overflow-hidden border border-black bg-[#08080c]">
                  <div
                    className="riot-xp-stripe h-full bg-[#a3ff12] transition-all"
                    style={{ width: `${hudLevelPct}%` }}
                  />
                </div>
                <span className="inline-flex items-center gap-0.5 text-[10px] font-extrabold text-[#ff6b00]">
                  <Flame className="h-3 w-3 text-[#ff334b]" />
                  {gameProfile?.streakDays ?? 0}D
                </span>
                {(gameProfile?.comboMult ?? 1) > 1 ? (
                  <span className="-rotate-2 border border-black bg-[#ff2e88] px-1.5 py-0 text-[10px] font-extrabold text-white">
                    {gameProfile?.comboMult.toFixed(1)}X COMBO!
                  </span>
                ) : null}
              </button>

              <button
                type="button"
                onClick={() => setSidekickOpen(true)}
                className="riot-btn inline-flex items-center gap-1.5 bg-[#ff2e88] px-3 py-1 font-display text-[11px] text-white uppercase hover:bg-[#ffe031] hover:text-[#08080c]"
              >
                <Bot className="h-3.5 w-3.5" />
                SHADOW SIDEKICK
                <span className="border border-black bg-[#08080c] px-1 py-0 font-mono text-[9px] text-[#ffe031]">
                  CTRL+K
                </span>
              </button>
            </div>
          </div>
        </div>

        {/* Explosive Emblem + Angled Chapter Select Bar */}
        <div className="mx-auto flex max-w-[1620px] flex-wrap items-center justify-between gap-3 px-4 py-2.5">
          <button
            type="button"
            onClick={() => setActiveTab('overview')}
            className="group flex items-center gap-2.5 text-left"
          >
            <div className="-rotate-3 border-[2.5px] border-black bg-[#ffe031] px-2.5 py-1 font-display text-lg leading-none text-[#08080c] shadow-[4px_4px_0_#ff2e88] transition-transform group-hover:rotate-0">
              MW
            </div>
            <div className="flex items-center gap-2">
              <span className="font-display text-xl tracking-tight text-[#fff6de] uppercase sm:text-2xl riot-title-rgb">
                MW//ECHO
              </span>
              <ComicSticker tone="cream" tilt="rotate-2">
                RIOT OS
              </ComicSticker>
            </div>
          </button>

          {/* Angled Comic / Arcade Chapter Select Deck (Stages 01..11) */}
          <nav
            aria-label="Stage navigation"
            className="flex flex-1 flex-wrap items-center justify-start gap-1.5 lg:justify-end"
          >
            {NAV_TABS.map((navTab) => {
              const IconComp = navTab.icon;
              const isActive = activeTab === navTab.id;
              const badgeText = tabBadgeCount(navTab.id);
              return (
                <button
                  key={navTab.id}
                  type="button"
                  onClick={() => setActiveTab(navTab.id)}
                  className={`-skew-x-6 border-[2.5px] border-black px-2.5 py-1.5 transition-all duration-100 ${
                    isActive
                      ? '-translate-y-0.5 bg-[#ffe031] text-[#08080c] shadow-[4px_4px_0_#ff2e88]'
                      : 'bg-[#13131d] text-[#fff6de] shadow-[3px_3px_0_#000] hover:-translate-x-0.5 hover:-translate-y-0.5 hover:bg-[#1b1b2b] hover:text-[#00f0ff] hover:shadow-[5px_5px_0_#00f0ff] active:translate-x-0.5 active:translate-y-0.5 active:shadow-[1px_1px_0_#000]'
                  }`}
                >
                  <span className="flex skew-x-6 items-center gap-1 font-mono text-[11px] font-extrabold tracking-tight uppercase">
                    <span className={isActive ? 'text-[#ff2e88]' : 'text-[#ffe031]'}>
                      {navTab.sceneNum}
                    </span>
                    <IconComp className="h-3.5 w-3.5" />
                    <span>{navTab.stageName}</span>
                    {badgeText ? (
                      <span
                        className={`ml-0.5 border border-black px-1 py-0 text-[9px] leading-tight ${
                          isActive
                            ? 'bg-[#08080c] text-[#ffe031]'
                            : 'bg-[#ff2e88] text-white'
                        }`}
                      >
                        {badgeText}
                      </span>
                    ) : null}
                  </span>
                </button>
              );
            })}
          </nav>
        </div>

        {/* Riot Hazard Tape Trim */}
        <div className="riot-hazard-tape h-2 border-t-2 border-black" />
      </header>

      {/* Main Cinema Stage Content */}
      <main className="relative z-10 mx-auto w-full max-w-[1620px] flex-1 px-4 py-5 lg:px-6 lg:py-6">
        {activeTab === 'overview' ? (
          <OverviewTab
            hubStats={hubStats}
            poolSummary={poolSummary}
            aiStats={aiStats}
            whatsappStats={whatsappStats}
            aiAvailable={aiAvailable}
            isRefreshing={refreshingAll}
            onRefreshAll={reloadAll}
            onOpenWhatsApp={() => setActiveTab('whatsapp')}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'projects' ? (
          <ProjectsTab
            projects={projects}
            isLoading={loadingProjects}
            onReloadProjects={async () => {
              await Promise.all([reloadProjects(), reloadGameProfile()]);
            }}
            onOpenProjectBoard={handleOpenProjectBoard}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'tasks' ? (
          <TasksTab
            projects={projects}
            selectedProject={selectedTaskProject}
            onSelectProject={setSelectedTaskProject}
            onTasksChanged={async () => {
              await Promise.all([reloadHubCore(), reloadProjects(), reloadGameProfile()]);
            }}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'whatsapp' ? (
          <WhatsAppTab
            stats={whatsappStats}
            devices={whatsappDevices}
            isLoading={loadingWhatsApp}
            onReloadWhatsApp={async () => {
              await Promise.all([reloadWhatsApp(), reloadGameProfile()]);
            }}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'gateway' ? (
          <AiGatewayTab
            aiStats={aiStats}
            providers={aiProviders}
            agents={aiAgents}
            catalogModels={catalogEntries}
            providerPrefill={providerPrefill}
            onConsumePrefill={handleConsumePrefill}
            onReloadGateway={async () => {
              await Promise.all([reloadAiGateway(), reloadGameProfile()]);
            }}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'free-models' ? (
          <FreeModelsTab
            entries={catalogEntries}
            aiStats={aiStats}
            onReloadCatalogAndStats={reloadAiGateway}
            onAddProviderShortcut={handleAddProviderShortcut}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'observatory' ? (
          <ObservatoryTab
            onRepoImported={async () => {
              await Promise.all([
                reloadHubCore(),
                reloadProjects(),
                reloadStorage(),
                reloadGameProfile(),
              ]);
            }}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'storage' ? (
          <StoragePoolTab
            poolSummary={poolSummary}
            objects={storageObjects}
            isLoading={loadingStorage}
            onReloadStorage={async (searchTerm) => {
              await Promise.all([reloadStorage(searchTerm), reloadGameProfile()]);
            }}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'docs' ? (
          <DocsTab
            onDocsChanged={async () => {
              await Promise.all([reloadHubCore(), reloadProjects(), reloadGameProfile()]);
            }}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'coder' ? (
          <CoderTab
            catalogModels={catalogEntries}
            onHubMutated={async () => {
              await Promise.all([
                reloadHubCore(),
                reloadProjects(),
                reloadAiGateway(),
                reloadGameProfile(),
              ]);
            }}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'arena' ? (
          <ArenaTab
            profile={gameProfile}
            isLoading={loadingGame}
            onProfileUpdated={(nextProfile) => setGameProfile(nextProfile)}
            onReloadArena={reloadGameProfile}
            onJumpToProjectBoard={handleOpenProjectBoard}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'world-radar' ? (
          <WorldRadarTab onNotify={pushNotice} />
        ) : null}

        {activeTab === 'flows' ? (
          <FlowsTab
            onNotify={pushNotice}
            onHubMutated={async () => {
              await Promise.all([
                reloadHubCore(),
                reloadProjects(),
                reloadStorage(),
                reloadWhatsApp(),
                reloadGameProfile(),
              ]);
            }}
          />
        ) : null}

        {activeTab === 'blueprints' ? (
          <BlueprintsTab onNotify={pushNotice} />
        ) : null}

        {activeTab === 'vault-env' ? (
          <VaultEnvTab
            projects={projects}
            onReloadStorage={reloadStorage}
            onNotify={pushNotice}
          />
        ) : null}

        {activeTab === 'desktop' ? (
          <DesktopHostTab onNotify={pushNotice} />
        ) : null}

        {activeTab === 'memvault' ? (
          <MemVaultTab onNotify={pushNotice} />
        ) : null}

        {activeTab === 'fleet' ? (
          <FleetCockpitTab />
        ) : null}
      </main>

      {/* Bottom Cinema Letterbox Footer */}
      <footer className="relative z-10 border-t-[2.5px] border-black bg-[#050508] px-4 py-2">
        <div className="mx-auto flex max-w-[1620px] flex-wrap items-center justify-between gap-2 font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
          <div className="flex flex-wrap items-center gap-2">
            <MonoBadge tone="yellow">MW//ECHO</MonoBadge>
            <span>
              GO ECHO · FLOWS · PACKS · WORLD RADAR · ARENA XP · SHADOW SIDEKICK · CODER · GOWA · FTS5 · BIFROST/MAGPIE/9ROUTER · 9DRIVE
            </span>
          </div>
          <span className="text-[#ff2e88]">ISSUE #14 // RIOT &amp; CHAOS COCKPIT</span>
        </div>
      </footer>

      <SidekickDrawer
        isOpen={sidekickOpen}
        onClose={() => setSidekickOpen(false)}
        onActionTriggered={async () => {
          await Promise.all([
            reloadHubCore(),
            reloadProjects(),
            reloadWhatsApp(),
            reloadGameProfile(),
          ]);
        }}
        onNotify={pushNotice}
      />

      <ToastStack notices={notices} onDismiss={dismissNotice} />
    </div>
  );
}

export default App;
