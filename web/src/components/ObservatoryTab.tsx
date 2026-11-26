import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Activity,
  BookOpen,
  CheckCircle2,
  CheckSquare,
  Clock,
  Cpu,
  Download,
  FolderGit2,
  GitBranch,
  Globe,
  Radar,
  Radio,
  RefreshCw,
  Search,
  Server,
} from 'lucide-react';
import {
  fetchObservatoryEvents,
  fetchObservatoryRepos,
  fetchObservatoryServices,
  fetchObservatoryStats,
  importLocalRepo,
  scanObservatoryRepos,
  type DiscoveredGitRepo,
  type DiscoveredHostService,
  type GitRepoRadarResponse,
  type HostServicesDiscoveryResponse,
  type ObservatoryEventRecord,
  type ObservatoryStats,
} from '../api';
import { formatCompactNumber, formatShortTime } from '../utils';
import { CopyableSnippet, MonoBadge, SectionCard, StatTile, type BadgeTone } from './UiPrimitives';

interface ObservatoryTabProps {
  onRepoImported: () => Promise<void>;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

const SERVICE_CATEGORY_TONES: Record<string, BadgeTone> = {
  ai: 'magenta',
  'ai-runtime': 'magenta',
  observability: 'yellow',
  storage: 'cyan',
  database: 'cyan',
  dev: 'ok',
};

function resolveCategoryTone(category: string): BadgeTone {
  const lowered = (category || '').toLowerCase();
  if (SERVICE_CATEGORY_TONES[lowered]) {
    return SERVICE_CATEGORY_TONES[lowered];
  }
  if (lowered.includes('ai') || lowered.includes('llm')) return 'magenta';
  if (lowered.includes('obs') || lowered.includes('otel') || lowered.includes('metric')) return 'yellow';
  if (lowered.includes('stor') || lowered.includes('db') || lowered.includes('cache')) return 'cyan';
  return 'neutral';
}

export function ObservatoryTab({ onRepoImported, onNotify }: ObservatoryTabProps) {
  // 1. OTLP Telemetry & Events
  const [otlpStats, setOtlpStats] = useState<ObservatoryStats | null>(null);
  const [events, setEvents] = useState<ObservatoryEventRecord[]>([]);
  const [eventKindFilter, setEventKindFilter] = useState('');
  const [eventServiceFilter, setEventServiceFilter] = useState('');
  const [loadingEvents, setLoadingEvents] = useState(false);

  // 2. Host Protocol Auto-Discovery
  const [discovery, setDiscovery] = useState<HostServicesDiscoveryResponse | null>(null);
  const [scanningServices, setScanningServices] = useState(false);
  const [serviceCategoryFilter, setServiceCategoryFilter] = useState('');

  // 3. Local Git Repo Radar
  const [radar, setRadar] = useState<GitRepoRadarResponse | null>(null);
  const [rootsInput, setRootsInput] = useState('Z:\\repo');
  const [scanningRepos, setScanningRepos] = useState(false);
  const [repoSearch, setRepoSearch] = useState('');
  const [importingRepoPath, setImportingRepoPath] = useState<string | null>(null);

  const loadOtlpTelemetry = useCallback(async () => {
    setLoadingEvents(true);
    try {
      const [statsReply, eventsReply] = await Promise.all([
        fetchObservatoryStats(),
        fetchObservatoryEvents({
          kind: eventKindFilter || undefined,
          service: eventServiceFilter || undefined,
          limit: 100,
        }),
      ]);
      setOtlpStats(statsReply);
      setEvents(eventsReply.events ?? []);
    } catch {
      // Backend observatory endpoint may still be initializing; fail quietly on initial mount.
    } finally {
      setLoadingEvents(false);
    }
  }, [eventKindFilter, eventServiceFilter]);

  const loadHostServices = useCallback(
    async (notifyOnSuccess = false) => {
      setScanningServices(true);
      try {
        const servicesReply = await fetchObservatoryServices();
        setDiscovery(servicesReply);
        if (notifyOnSuccess) {
          onNotify(
            'ok',
            `Host discovery finished: ${servicesReply.count} service(s) online across ${servicesReply.probesRun} probes.`,
          );
        }
      } catch (caughtError) {
        if (notifyOnSuccess) {
          const message =
            caughtError instanceof Error ? caughtError.message : 'Host port discovery failed';
          onNotify('err', message);
        }
      } finally {
        setScanningServices(false);
      }
    },
    [onNotify],
  );

  const loadGitRepos = useCallback(async () => {
    setScanningRepos(true);
    try {
      const reposReply = await fetchObservatoryRepos();
      setRadar(reposReply);
      if (reposReply.roots && reposReply.roots.length > 0) {
        setRootsInput(reposReply.roots.join(', '));
      }
    } catch {
      // Fail softly on initial load if backend not yet serving /api/observatory/repos.
    } finally {
      setScanningRepos(false);
    }
  }, []);

  useEffect(() => {
    void loadOtlpTelemetry();
  }, [loadOtlpTelemetry]);

  useEffect(() => {
    void loadHostServices(false);
    void loadGitRepos();
  }, [loadHostServices, loadGitRepos]);

  const handleCustomRootScan = async (event: React.FormEvent) => {
    event.preventDefault();
    const parsedRoots = rootsInput
      .split(/[,;\n]+/)
      .map((segment) => segment.trim())
      .filter(Boolean);
    if (parsedRoots.length === 0) {
      onNotify('err', 'Enter at least one root directory to scan.');
      return;
    }
    setScanningRepos(true);
    try {
      const scanReply = await scanObservatoryRepos(parsedRoots);
      setRadar(scanReply);
      onNotify(
        'ok',
        `Repo Radar discovered ${scanReply.count} Git repo(s) under ${parsedRoots.length} root(s).`,
      );
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Git repo scan failed';
      onNotify('err', message);
    } finally {
      setScanningRepos(false);
    }
  };

  const handleImportDiscoveredRepo = async (repo: DiscoveredGitRepo) => {
    setImportingRepoPath(repo.path);
    try {
      const summary = await importLocalRepo({
        repoDir: repo.path,
        project: repo.name,
        includeAssets: true,
      });
      onNotify(
        'ok',
        `Imported "${repo.name}": ${summary.docsImported} docs, ${summary.tasksImported} tasks, ${summary.assetsImported} assets.`,
      );
      await Promise.all([onRepoImported(), loadGitRepos()]);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Repo import failed';
      onNotify('err', message);
    } finally {
      setImportingRepoPath(null);
    }
  };

  const serviceOptions = useMemo(() => {
    return Object.keys(otlpStats?.countsByService ?? {}).sort((left, right) =>
      left.localeCompare(right),
    );
  }, [otlpStats]);

  const kindOptions = useMemo(() => {
    const known = new Set(['trace', 'metric', 'log', ...Object.keys(otlpStats?.countsByKind ?? {})]);
    return Array.from(known);
  }, [otlpStats]);

  const discoveredServices: DiscoveredHostService[] = discovery?.services ?? [];
  const serviceCategories = useMemo(() => {
    return Array.from(new Set(discoveredServices.map((svc) => svc.category).filter(Boolean))).sort();
  }, [discoveredServices]);

  const filteredServices = useMemo(() => {
    if (!serviceCategoryFilter) {
      return discoveredServices;
    }
    return discoveredServices.filter((svc) => svc.category === serviceCategoryFilter);
  }, [discoveredServices, serviceCategoryFilter]);

  const discoveredRepos = radar?.repos ?? [];
  const filteredRepos = useMemo(() => {
    const query = repoSearch.trim().toLowerCase();
    if (!query) {
      return discoveredRepos;
    }
    return discoveredRepos.filter((repo) =>
      `${repo.name} ${repo.path} ${repo.branch} ${repo.remoteUrl}`.toLowerCase().includes(query),
    );
  }, [discoveredRepos, repoSearch]);

  return (
    <div className="space-y-5">
      {/* Top OTLP & Host Discovery Telemetry Strip */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="OTLP Events Ingested"
          value={formatCompactNumber(otlpStats?.totalEvents ?? 0)}
          sublabel={`${Object.keys(otlpStats?.countsByService ?? {}).length} instrumented service(s)`}
          icon={<Activity className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="Agent Tokens (In / Out)"
          value={`${formatCompactNumber(otlpStats?.totalTokensIn ?? 0)} / ${formatCompactNumber(otlpStats?.totalTokensOut ?? 0)}`}
          sublabel="Aggregated from OTLP spans"
          icon={<Cpu className="h-4 w-4" />}
          tone="magenta"
        />
        <StatTile
          label="Avg Span Duration"
          value={`${Math.round(otlpStats?.avgDurationMs ?? 0)} ms`}
          sublabel="Across traces, metrics & logs"
          icon={<Clock className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="Host Services & Repos"
          value={`${discovery?.count ?? 0} svc · ${radar?.count ?? 0} git`}
          sublabel={
            discovery
              ? `${discovery.probesRun} host probes run`
              : 'Auto-discovery ready'
          }
          icon={<Radio className="h-4 w-4" />}
          tone="ok"
        />
      </div>

      {/* 1. Host Protocol Auto-Discovery */}
      <SectionCard
        title="Host Protocol Auto-Discovery"
        subtitle="Automatically probes local AI runtimes (Ollama, LM Studio, vLLM, mw-echo), OTLP/Prometheus/Grafana, Postgres/Redis/MinIO/Garage/Qdrant, and Dev servers."
        icon={<Radio className="h-4 w-4" />}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {discovery ? (
              <MonoBadge tone="ok">
                {discovery.count} LIVE / {discovery.probesRun} PROBES
              </MonoBadge>
            ) : null}
            <select
              value={serviceCategoryFilter}
              onChange={(ev) => setServiceCategoryFilter(ev.target.value)}
              className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            >
              <option value="">All Categories</option>
              {serviceCategories.map((catName) => (
                <option key={catName} value={catName}>
                  {catName}
                </option>
              ))}
            </select>
            <button
              type="button"
              disabled={scanningServices}
              onClick={() => void loadHostServices(true)}
              className="inline-flex items-center gap-1.5 rounded bg-[#ffe031] px-3.5 py-1.5 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90 disabled:opacity-50"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${scanningServices ? 'animate-spin' : ''}`} />
              {scanningServices ? 'SCANNING...' : 'RESCAN HOST PORTS'}
            </button>
          </div>
        }
      >
        {filteredServices.length === 0 ? (
          <div className="rounded-md border border-dashed border-[#2a2a36] bg-[#0d0d10] py-8 text-center font-mono text-xs text-[#9c988c]">
            {scanningServices
              ? 'Probing local host ports...'
              : 'No matching services discovered yet. Click "RESCAN HOST PORTS".'}
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
            {filteredServices.map((service) => {
              const catTone = resolveCategoryTone(service.category);
              const isHealthy =
                service.status.toLowerCase() === 'up' ||
                service.status.toLowerCase() === 'ok' ||
                service.status.toLowerCase() === 'online';
              return (
                <div
                  key={`${service.port}-${service.name}`}
                  className="rounded-lg border border-[#2a2a36] bg-[#0d0d10] p-3.5 transition-colors hover:border-[#3d3d4f]"
                >
                  <div className="flex items-start justify-between gap-2">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <Server className="h-3.5 w-3.5 shrink-0 text-[#00b8f0]" />
                        <span className="truncate font-mono text-sm font-bold text-[#f3efe4]">
                          {service.name}
                        </span>
                      </div>
                      <div className="mt-1 flex flex-wrap gap-1">
                        <MonoBadge tone={catTone}>{service.category}</MonoBadge>
                        <MonoBadge tone="neutral">{service.protocol}</MonoBadge>
                        <MonoBadge tone="yellow">:{service.port}</MonoBadge>
                      </div>
                    </div>
                    <div className="flex flex-col items-end gap-1">
                      <MonoBadge tone={isHealthy ? 'ok' : 'err'}>
                        {service.status || 'UP'}
                      </MonoBadge>
                      <span className="font-mono text-[10px] text-[#9c988c]">
                        {service.latencyMs}ms
                      </span>
                    </div>
                  </div>

                  <div className="mt-2.5 flex items-center justify-between gap-2 rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5">
                    <code className="truncate font-mono text-[11px] text-[#00b8f0]">
                      {service.endpoint}
                    </code>
                    {service.endpoint.startsWith('http') ? (
                      <a
                        href={service.endpoint}
                        target="_blank"
                        rel="noreferrer"
                        className="shrink-0 text-[#9c988c] hover:text-[#ffe031]"
                        title="Open endpoint"
                      >
                        <Globe className="h-3.5 w-3.5" />
                      </a>
                    ) : null}
                  </div>

                  {service.detail ? (
                    <div className="mt-2 truncate font-mono text-[10px] text-[#9c988c]">
                      {service.detail}
                    </div>
                  ) : null}
                </div>
              );
            })}
          </div>
        )}
        {discovery?.scannedAt ? (
          <div className="mt-3 text-right font-mono text-[10px] text-[#9c988c]">
            Last host scan: {formatShortTime(discovery.scannedAt)}
          </div>
        ) : null}
      </SectionCard>

      {/* 2. Local Git Repo Radar */}
      <SectionCard
        title="Local Git Repo Radar"
        subtitle="Auto-discover all local Git repositories across configured host roots and import docs + tasks + media in 1 click."
        icon={<Radar className="h-4 w-4" />}
        actions={
          <MonoBadge tone="cyan">{discoveredRepos.length} REPOS DISCOVERED</MonoBadge>
        }
      >
        <form onSubmit={handleCustomRootScan} className="mb-4 flex flex-wrap items-center gap-2.5">
          <div className="min-w-[260px] flex-1">
            <input
              type="text"
              value={rootsInput}
              onChange={(ev) => setRootsInput(ev.target.value)}
              placeholder="Scan roots (comma-separated, e.g. Z:\repo, C:\dev)"
              className="w-full rounded border border-[#2a2a36] bg-[#0d0d10] px-3 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>
          <button
            type="submit"
            disabled={scanningRepos}
            className="inline-flex items-center gap-1.5 rounded bg-[#00b8f0] px-3.5 py-2 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90 disabled:opacity-50"
          >
            <Radar className={`h-3.5 w-3.5 ${scanningRepos ? 'animate-spin' : ''}`} />
            {scanningRepos ? 'SCANNING ROOTS...' : 'SCAN ROOTS'}
          </button>
          <div className="relative min-w-[200px] sm:w-64">
            <Search className="pointer-events-none absolute top-1/2 left-3 h-3.5 w-3.5 -translate-y-1/2 text-[#9c988c]" />
            <input
              type="search"
              value={repoSearch}
              onChange={(ev) => setRepoSearch(ev.target.value)}
              placeholder="Filter repos..."
              className="w-full rounded border border-[#2a2a36] bg-[#0d0d10] py-2 pr-3 pl-9 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>
        </form>

        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-left text-xs">
            <thead>
              <tr className="border-b border-[#2a2a36] font-mono text-[10px] uppercase tracking-wider text-[#9c988c]">
                <th className="px-3 py-2">Repository</th>
                <th className="px-3 py-2">Branch</th>
                <th className="px-3 py-2">Signals</th>
                <th className="px-3 py-2">Modified</th>
                <th className="px-3 py-2 text-right">Action</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#2a2a36]/70">
              {filteredRepos.length > 0 ? (
                filteredRepos.map((repo) => {
                  const isImporting = importingRepoPath === repo.path;
                  return (
                    <tr key={repo.path} className="hover:bg-[#1c1c23]/60">
                      <td className="px-3 py-2.5">
                        <div className="flex items-center gap-2">
                          <FolderGit2 className="h-3.5 w-3.5 shrink-0 text-[#ffe031]" />
                          <span className="font-mono font-bold text-[#f3efe4]">{repo.name}</span>
                        </div>
                        <div className="mt-0.5 max-w-md truncate font-mono text-[10px] text-[#9c988c]">
                          {repo.path}
                        </div>
                        {repo.remoteUrl ? (
                          <div className="max-w-md truncate font-mono text-[10px] text-[#00b8f0]/80">
                            {repo.remoteUrl}
                          </div>
                        ) : null}
                      </td>
                      <td className="px-3 py-2.5">
                        <MonoBadge tone="magenta">
                          <GitBranch className="h-3 w-3" />
                          {repo.branch || 'detached'}
                        </MonoBadge>
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex flex-wrap gap-1">
                          {repo.hasDocs ? (
                            <MonoBadge tone="yellow">
                              <BookOpen className="h-3 w-3" />
                              DOCS
                            </MonoBadge>
                          ) : null}
                          {repo.hasTasksJson ? (
                            <MonoBadge tone="cyan">
                              <CheckSquare className="h-3 w-3" />
                              TASKS.JSON
                            </MonoBadge>
                          ) : null}
                          {repo.imported ? (
                            <MonoBadge tone="ok">
                              <CheckCircle2 className="h-3 w-3" />
                              IMPORTED
                            </MonoBadge>
                          ) : (
                            <MonoBadge tone="neutral">UNIMPORTED</MonoBadge>
                          )}
                        </div>
                      </td>
                      <td className="px-3 py-2.5 font-mono text-[11px] text-[#9c988c]">
                        {formatShortTime(repo.modifiedAt)}
                      </td>
                      <td className="px-3 py-2.5 text-right">
                        <button
                          type="button"
                          disabled={isImporting}
                          onClick={() => void handleImportDiscoveredRepo(repo)}
                          className="inline-flex items-center gap-1.5 rounded bg-[#ffe031] px-3 py-1.5 font-mono text-[11px] font-extrabold text-[#0d0d10] hover:opacity-90 disabled:opacity-50"
                        >
                          <Download className="h-3 w-3" />
                          {isImporting ? 'IMPORTING...' : repo.imported ? 'RE-IMPORT' : 'IMPORT REPO'}
                        </button>
                      </td>
                    </tr>
                  );
                })
              ) : (
                <tr>
                  <td colSpan={5} className="px-3 py-6 text-center font-mono text-xs text-[#9c988c]">
                    {scanningRepos
                      ? 'Scanning local directories for .git repositories...'
                      : 'No Git repositories discovered. Enter root folders above and click "SCAN ROOTS".'}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </SectionCard>

      {/* 3. OTLP Telemetry & Agent Observatory */}
      <SectionCard
        title="OTLP Telemetry & Agent Observatory"
        subtitle="Built-in OpenTelemetry HTTP receiver for /v1/traces, /v1/metrics, and /v1/logs emitted by coding agents & local services."
        icon={<Activity className="h-4 w-4" />}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <select
              value={eventKindFilter}
              onChange={(ev) => setEventKindFilter(ev.target.value)}
              className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            >
              <option value="">All Kinds</option>
              {kindOptions.map((kindName) => (
                <option key={kindName} value={kindName}>
                  {kindName}
                </option>
              ))}
            </select>
            <select
              value={eventServiceFilter}
              onChange={(ev) => setEventServiceFilter(ev.target.value)}
              className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            >
              <option value="">All Services</option>
              {serviceOptions.map((svcName) => (
                <option key={svcName} value={svcName}>
                  {svcName}
                </option>
              ))}
            </select>
            <button
              type="button"
              onClick={() => void loadOtlpTelemetry()}
              className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#1c1c23] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] hover:border-[#ffe031]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingEvents ? 'animate-spin' : ''}`} />
              REFRESH
            </button>
          </div>
        }
      >
        {/* Copyable OTLP Snippet */}
        <div className="mb-4 grid grid-cols-1 gap-2.5 lg:grid-cols-2">
          <CopyableSnippet
            label="OTEL EXPORTER ENDPOINT (/v1/traces, /v1/metrics, /v1/logs)"
            value="OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4310"
            tone="yellow"
          />
          <div className="flex flex-wrap items-center gap-1.5 rounded-md border border-[#2a2a36] bg-[#0d0d10] px-3 py-2">
            <span className="font-mono text-[10px] uppercase text-[#9c988c]">Events By Kind:</span>
            {Object.entries(otlpStats?.countsByKind ?? {}).length > 0 ? (
              Object.entries(otlpStats?.countsByKind ?? {}).map(([kindKey, kindCount]) => (
                <MonoBadge key={kindKey} tone="cyan">
                  {kindKey}: {kindCount}
                </MonoBadge>
              ))
            ) : (
              <span className="font-mono text-xs text-[#9c988c]">Waiting for OTLP telemetry...</span>
            )}
          </div>
        </div>

        {/* Live Events Table */}
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-left text-xs">
            <thead>
              <tr className="border-b border-[#2a2a36] font-mono text-[10px] uppercase tracking-wider text-[#9c988c]">
                <th className="px-3 py-2">Time</th>
                <th className="px-3 py-2">Kind</th>
                <th className="px-3 py-2">Service / Agent</th>
                <th className="px-3 py-2">Span / Event Name</th>
                <th className="px-3 py-2">Status</th>
                <th className="px-3 py-2">Duration</th>
                <th className="px-3 py-2 text-right">Tokens / Cost</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#2a2a36]/70">
              {events.length > 0 ? (
                events.map((evt) => {
                  const statusLower = (evt.status || '').toLowerCase();
                  const statusTone: BadgeTone =
                    statusLower.includes('err') || statusLower === 'fail'
                      ? 'err'
                      : statusLower === 'ok' || statusLower === 'unset' || statusLower === ''
                        ? 'ok'
                        : 'yellow';
                  return (
                    <tr key={evt.id} className="hover:bg-[#1c1c23]/60">
                      <td className="px-3 py-2 font-mono text-[11px] text-[#9c988c]">
                        {formatShortTime(evt.createdAt)}
                      </td>
                      <td className="px-3 py-2">
                        <MonoBadge tone="cyan">{evt.kind}</MonoBadge>
                      </td>
                      <td className="px-3 py-2">
                        <div className="flex flex-wrap gap-1">
                          <MonoBadge tone="neutral">{evt.service || 'unknown'}</MonoBadge>
                          {evt.agent ? <MonoBadge tone="magenta">{evt.agent}</MonoBadge> : null}
                        </div>
                      </td>
                      <td className="max-w-xs truncate px-3 py-2 font-mono text-xs text-[#f3efe4]">
                        {evt.name}
                      </td>
                      <td className="px-3 py-2">
                        <MonoBadge tone={statusTone}>{evt.status || 'ok'}</MonoBadge>
                      </td>
                      <td className="px-3 py-2 font-mono text-[11px] text-[#ffe031]">
                        {Math.round(evt.durationMs)}ms
                      </td>
                      <td className="px-3 py-2 text-right font-mono text-[11px] text-[#9c988c]">
                        {evt.tokensIn > 0 || evt.tokensOut > 0
                          ? `${formatCompactNumber(evt.tokensIn)}/${formatCompactNumber(evt.tokensOut)}`
                          : '—'}
                        {evt.costUsd > 0 ? (
                          <span className="ml-1.5 text-[#22c55e]">
                            ${evt.costUsd.toFixed(4)}
                          </span>
                        ) : null}
                      </td>
                    </tr>
                  );
                })
              ) : (
                <tr>
                  <td colSpan={7} className="px-3 py-6 text-center font-mono text-xs text-[#9c988c]">
                    No OTLP events recorded yet. Export{' '}
                    <code className="text-[#ffe031]">
                      OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4310
                    </code>{' '}
                    from any agent or service to stream traces, metrics &amp; logs here.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </SectionCard>
    </div>
  );
}
