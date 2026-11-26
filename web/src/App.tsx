import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Activity,
  BookOpen,
  CheckSquare,
  Cloud,
  Cpu,
  FolderKanban,
  LayoutDashboard,
  Radar,
  Radio,
} from 'lucide-react';
import {
  fetchAiAgents,
  fetchAiProviders,
  fetchAiStats,
  fetchFreeModelCatalog,
  fetchHubStats,
  fetchProjects,
  fetchStorageObjects,
  fetchStoragePool,
  type AiAgentRoute,
  type AiGatewayStats,
  type AiProviderRecord,
  type FreeModelCatalogEntry,
  type HubStats,
  type PoolSummary,
  type PooledStorageObject,
  type ProjectSummary,
} from './api';
import { AiGatewayTab, type ProviderDraftPrefill } from './components/AiGatewayTab';
import { DocsTab } from './components/DocsTab';
import { FreeModelsTab } from './components/FreeModelsTab';
import { ObservatoryTab } from './components/ObservatoryTab';
import { OverviewTab } from './components/OverviewTab';
import { ProjectsTab } from './components/ProjectsTab';
import { StoragePoolTab } from './components/StoragePoolTab';
import { TasksTab } from './components/TasksTab';
import { MonoBadge, ToastStack, type ToastNotice } from './components/UiPrimitives';

type NavTabId =
  | 'overview'
  | 'projects'
  | 'tasks'
  | 'observatory'
  | 'gateway'
  | 'free-models'
  | 'storage'
  | 'docs';

interface NavTabMeta {
  id: NavTabId;
  label: string;
  shortLabel: string;
  icon: typeof LayoutDashboard;
}

const NAV_TABS: NavTabMeta[] = [
  { id: 'overview', label: 'Overview', shortLabel: 'Overview', icon: LayoutDashboard },
  { id: 'projects', label: 'Projects', shortLabel: 'Projects', icon: FolderKanban },
  { id: 'tasks', label: 'Tasks Board', shortLabel: 'Tasks', icon: CheckSquare },
  { id: 'observatory', label: 'Observatory', shortLabel: 'Observe', icon: Radio },
  { id: 'gateway', label: 'AI Gateway', shortLabel: 'Gateway', icon: Cpu },
  { id: 'free-models', label: 'Free Models', shortLabel: 'Free AI', icon: Radar },
  { id: 'storage', label: '9Drive Storage', shortLabel: '9Drive', icon: Cloud },
  { id: 'docs', label: 'Docs (FTS5)', shortLabel: 'Docs', icon: BookOpen },
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
      // Backend /api/projects may not be initialized yet; keep empty list gracefully.
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

  const reloadAll = useCallback(async () => {
    setRefreshingAll(true);
    try {
      await Promise.all([
        reloadHubCore(),
        reloadProjects(),
        reloadStorage(),
        reloadAiGateway(),
      ]);
    } finally {
      setRefreshingAll(false);
    }
  }, [reloadHubCore, reloadProjects, reloadStorage, reloadAiGateway]);

  useEffect(() => {
    void reloadAll();
  }, [reloadAll]);

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

  const tabBadgeCount = (tabId: NavTabId): string | null => {
    switch (tabId) {
      case 'projects':
        return projects.length > 0 ? String(projects.length) : null;
      case 'tasks':
        return openTasksCount > 0 ? String(openTasksCount) : null;
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
      default:
        return null;
    }
  };

  return (
    <div className="flex min-h-screen flex-col bg-[#0d0d10] text-[#f3efe4]">
      {/* Sticky Top Command Bar */}
      <header className="sticky top-0 z-30 border-b border-[#2a2a36] bg-[#15151a]/95 backdrop-blur">
        <div className="mx-auto flex max-w-[1600px] flex-wrap items-center justify-between gap-3 px-4 py-3">
          <div className="flex items-center gap-3">
            <div className="flex h-8 w-8 items-center justify-center rounded bg-[#ffe031] font-mono text-sm font-black text-[#0d0d10]">
              MW
            </div>
            <div>
              <div className="flex items-center gap-2">
                <span className="font-mono text-sm font-black tracking-widest text-[#f3efe4]">
                  MW-ECHO
                </span>
                <MonoBadge tone="yellow">CONTROL HUB</MonoBadge>
                <MonoBadge tone={aiAvailable ? 'ok' : 'neutral'}>
                  <Activity className="h-3 w-3" />
                  {aiAvailable ? 'GATEWAY LIVE' : 'HUB LIVE'}
                </MonoBadge>
              </div>
            </div>
          </div>

          <div className="hidden items-center gap-2 font-mono text-[11px] text-[#9c988c] lg:flex">
            <span>API 127.0.0.1:4310</span>
            <span className="text-[#2a2a36]">|</span>
            <span className="max-w-[260px] truncate">{hubStats?.dataDir ?? 'Connecting...'}</span>
          </div>
        </div>

        {/* Mobile / Tablet Horizontal Nav */}
        <nav className="flex gap-1 overflow-x-auto border-t border-[#2a2a36] px-4 py-2 lg:hidden">
          {NAV_TABS.map((navTab) => {
            const IconComp = navTab.icon;
            const isActive = activeTab === navTab.id;
            const badgeText = tabBadgeCount(navTab.id);
            return (
              <button
                key={navTab.id}
                type="button"
                onClick={() => setActiveTab(navTab.id)}
                className={`inline-flex shrink-0 items-center gap-1.5 rounded-md border px-3 py-1.5 font-mono text-xs font-bold transition-colors ${
                  isActive
                    ? 'border-[#ffe031] bg-[#ffe031] text-[#0d0d10]'
                    : 'border-[#2a2a36] bg-[#0d0d10] text-[#9c988c] hover:text-[#f3efe4]'
                }`}
              >
                <IconComp className="h-3.5 w-3.5" />
                {navTab.shortLabel}
                {badgeText ? (
                  <span
                    className={`rounded px-1 text-[10px] ${
                      isActive ? 'bg-[#0d0d10]/20 text-[#0d0d10]' : 'bg-[#1c1c23] text-[#f3efe4]'
                    }`}
                  >
                    {badgeText}
                  </span>
                ) : null}
              </button>
            );
          })}
        </nav>
      </header>

      {/* Main Layout: Sidebar (Desktop) + Content */}
      <div className="mx-auto flex w-full max-w-[1600px] flex-1">
        <aside className="hidden w-56 shrink-0 border-r border-[#2a2a36] bg-[#15151a]/60 p-3 lg:block">
          <div className="sticky top-[72px] space-y-1">
            <div className="px-2.5 pb-1.5 font-mono text-[10px] font-bold uppercase tracking-widest text-[#9c988c]">
              Modules
            </div>
            {NAV_TABS.map((navTab) => {
              const IconComp = navTab.icon;
              const isActive = activeTab === navTab.id;
              const badgeText = tabBadgeCount(navTab.id);
              return (
                <button
                  key={navTab.id}
                  type="button"
                  onClick={() => setActiveTab(navTab.id)}
                  className={`flex w-full items-center justify-between rounded-md border px-3 py-2 text-left font-mono text-xs font-bold transition-colors ${
                    isActive
                      ? 'border-[#ffe031] bg-[#ffe031] text-[#0d0d10]'
                      : 'border-transparent text-[#9c988c] hover:border-[#2a2a36] hover:bg-[#1c1c23] hover:text-[#f3efe4]'
                  }`}
                >
                  <span className="flex items-center gap-2">
                    <IconComp className="h-4 w-4" />
                    {navTab.label}
                  </span>
                  {badgeText ? (
                    <span
                      className={`rounded px-1.5 py-0.5 text-[10px] ${
                        isActive
                          ? 'bg-[#0d0d10]/20 text-[#0d0d10]'
                          : 'bg-[#0d0d10] text-[#9c988c]'
                      }`}
                    >
                      {badgeText}
                    </span>
                  ) : null}
                </button>
              );
            })}

            <div className="mt-6 rounded-md border border-[#2a2a36] bg-[#0d0d10] p-3 font-mono text-[10px] text-[#9c988c]">
              <div className="font-bold text-[#ffe031]">STACK</div>
              <div className="mt-1 space-y-0.5">
                <div>Go Echo + SQLite FTS5</div>
                <div>Projects + TaskOS v2</div>
                <div>OTLP + Host Discovery</div>
                <div>Bifrost · Magpie · 9Router</div>
                <div>9Drive Zero-Local Pool</div>
              </div>
            </div>
          </div>
        </aside>

        <main className="min-w-0 flex-1 p-4 lg:p-6">
          {activeTab === 'overview' ? (
            <OverviewTab
              hubStats={hubStats}
              poolSummary={poolSummary}
              aiStats={aiStats}
              aiAvailable={aiAvailable}
              isRefreshing={refreshingAll}
              onRefreshAll={reloadAll}
              onNotify={pushNotice}
            />
          ) : null}

          {activeTab === 'projects' ? (
            <ProjectsTab
              projects={projects}
              isLoading={loadingProjects}
              onReloadProjects={reloadProjects}
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
                await Promise.all([reloadHubCore(), reloadProjects()]);
              }}
              onNotify={pushNotice}
            />
          ) : null}

          {activeTab === 'observatory' ? (
            <ObservatoryTab
              onRepoImported={async () => {
                await Promise.all([reloadHubCore(), reloadProjects(), reloadStorage()]);
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
              onReloadGateway={reloadAiGateway}
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

          {activeTab === 'storage' ? (
            <StoragePoolTab
              poolSummary={poolSummary}
              objects={storageObjects}
              isLoading={loadingStorage}
              onReloadStorage={reloadStorage}
              onNotify={pushNotice}
            />
          ) : null}

          {activeTab === 'docs' ? (
            <DocsTab
              onDocsChanged={async () => {
                await Promise.all([reloadHubCore(), reloadProjects()]);
              }}
              onNotify={pushNotice}
            />
          ) : null}
        </main>
      </div>

      <ToastStack notices={notices} onDismiss={dismissNotice} />
    </div>
  );
}

export default App;
