import { useState } from 'react';
import {
  Activity,
  BookOpen,
  Check,
  CheckSquare,
  Cloud,
  Copy,
  FolderGit2,
  RefreshCw,
  Sparkles,
  Terminal,
  Zap,
} from 'lucide-react';
import {
  importLocalRepo,
  type AiGatewayStats,
  type HubStats,
  type ImportSummary,
  type PoolSummary,
} from '../api';
import { copyTextToClipboard, formatBytes, formatCompactNumber } from '../utils';
import { CopyableSnippet, MonoBadge, SectionCard, StatTile } from './UiPrimitives';

interface OverviewTabProps {
  hubStats: HubStats | null;
  poolSummary: PoolSummary | null;
  aiStats: AiGatewayStats | null;
  aiAvailable: boolean;
  isRefreshing: boolean;
  onRefreshAll: () => Promise<void>;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

const FULL_POWERSHELL_SNIPPET = `$env:ANTHROPIC_BASE_URL="http://127.0.0.1:4310"
$env:OPENAI_BASE_URL="http://127.0.0.1:4310/v1"
$env:MW_ECHO_MCP_URL="http://127.0.0.1:4310/api/mcp"`;

const FULL_BASH_SNIPPET = `export ANTHROPIC_BASE_URL="http://127.0.0.1:4310"
export OPENAI_BASE_URL="http://127.0.0.1:4310/v1"
export MW_ECHO_MCP_URL="http://127.0.0.1:4310/api/mcp"`;

export function OverviewTab({
  hubStats,
  poolSummary,
  aiStats,
  aiAvailable,
  isRefreshing,
  onRefreshAll,
  onNotify,
}: OverviewTabProps) {
  const [repoDir, setRepoDir] = useState('Z:\\repo\\mw-xv');
  const [projectSlug, setProjectSlug] = useState('moonwitness');
  const [includeAssets, setIncludeAssets] = useState(false);
  const [importing, setImporting] = useState(false);
  const [lastImport, setLastImport] = useState<ImportSummary | null>(null);
  const [copiedShell, setCopiedShell] = useState<'pwsh' | 'bash' | null>(null);

  const doneCount = hubStats?.tasksByStatus?.done ?? 0;
  const totalTasks = hubStats?.tasksCount ?? 0;
  const openTasks = Math.max(0, totalTasks - doneCount);

  const handleImportSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!repoDir.trim()) {
      onNotify('err', 'Repository path is required.');
      return;
    }
    setImporting(true);
    try {
      const summary = await importLocalRepo({
        repoDir: repoDir.trim(),
        project: projectSlug.trim() || 'moonwitness',
        includeAssets,
      });
      setLastImport(summary);
      onNotify(
        'ok',
        `Imported ${summary.docsImported} docs, ${summary.tasksImported} tasks, ${summary.assetsImported} assets.`,
      );
      await onRefreshAll();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Repository import failed';
      onNotify('err', message);
    } finally {
      setImporting(false);
    }
  };

  const handleCopyShellBlock = async (shellKind: 'pwsh' | 'bash') => {
    const snippet = shellKind === 'pwsh' ? FULL_POWERSHELL_SNIPPET : FULL_BASH_SNIPPET;
    const copied = await copyTextToClipboard(snippet);
    if (copied) {
      setCopiedShell(shellKind);
      onNotify('ok', `Copied ${shellKind === 'pwsh' ? 'PowerShell' : 'Bash'} env block.`);
      window.setTimeout(() => setCopiedShell(null), 1800);
    }
  };

  const taskStatusEntries = Object.entries(hubStats?.tasksByStatus ?? {}).sort((left, right) =>
    left[0].localeCompare(right[0]),
  );
  const docsProjectEntries = Object.entries(hubStats?.docsByProject ?? {}).sort(
    (left, right) => right[1] - left[1],
  );

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-lg font-extrabold tracking-tight text-[#f3efe4]">
            CONTROL HUB OVERVIEW
          </h1>
          <p className="font-mono text-xs text-[#9c988c]">
            Local SQLite FTS5 + TaskOS v2 + 9Drive Cloud Pool + Bifrost/Magpie/9Router AI Gateway
          </p>
        </div>
        <button
          type="button"
          onClick={() => void onRefreshAll()}
          disabled={isRefreshing}
          className="inline-flex items-center gap-2 rounded-md border border-[#2a2a36] bg-[#15151a] px-3 py-1.5 font-mono text-xs font-semibold text-[#f3efe4] transition-colors hover:border-[#ffe031] hover:text-[#ffe031] disabled:opacity-50"
        >
          <RefreshCw className={`h-3.5 w-3.5 ${isRefreshing ? 'animate-spin' : ''}`} />
          SYNC TELEMETRY
        </button>
      </div>

      {/* 6 Combined Hub & AI Metrics */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
        <StatTile
          label="Docs FTS5"
          value={hubStats ? formatCompactNumber(hubStats.docsCount) : '—'}
          sublabel={`${Object.keys(hubStats?.docsByProject ?? {}).length} project(s) indexed`}
          icon={<BookOpen className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="Tasks Open / Total"
          value={hubStats ? `${openTasks} / ${totalTasks}` : '—'}
          sublabel={`${doneCount} completed`}
          icon={<CheckSquare className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="9Drive Free Space"
          value={poolSummary ? formatBytes(poolSummary.totalFreeBytes) : '—'}
          sublabel={
            poolSummary
              ? `${poolSummary.providers.length} account(s) · ${formatBytes(poolSummary.totalQuotaBytes)} cap`
              : 'Loading pool...'
          }
          icon={<Cloud className="h-4 w-4" />}
          tone="ok"
        />
        <StatTile
          label="AI Requests"
          value={aiAvailable && aiStats ? formatCompactNumber(aiStats.totalRequests) : '0'}
          sublabel={
            aiAvailable && aiStats
              ? `${aiStats.providersCount} provider(s) active`
              : 'Gateway standby'
          }
          icon={<Activity className="h-4 w-4" />}
          tone="magenta"
        />
        <StatTile
          label="RTK Tokens Saved"
          value={aiAvailable && aiStats ? formatCompactNumber(aiStats.totalRtkTokensSaved) : '0'}
          sublabel={
            aiAvailable && aiStats
              ? `${Math.round(aiStats.avgLatencyMs)}ms avg gateway latency`
              : 'RTK compression ready'
          }
          icon={<Sparkles className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="Exact Cache Hits"
          value={aiAvailable && aiStats ? formatCompactNumber(aiStats.cacheHits) : '0'}
          sublabel={
            aiAvailable && aiStats
              ? `${formatCompactNumber(aiStats.cacheTokensSaved)} cached tok saved`
              : 'Exact cache ready'
          }
          icon={<Zap className="h-4 w-4" />}
          tone="cyan"
        />
      </div>

      <div className="grid grid-cols-1 gap-5 xl:grid-cols-12">
        {/* One-Click Local Repo Importer */}
        <SectionCard
          title="One-Click Local Repository Importer"
          subtitle="Ingests Markdown docs into SQLite FTS5, TaskOS v2 tasks.json, and optional media assets."
          icon={<FolderGit2 className="h-4 w-4" />}
          className="xl:col-span-7"
        >
          <form onSubmit={handleImportSubmit} className="space-y-3">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <div className="sm:col-span-2">
                <label className="mb-1 block font-mono text-[11px] uppercase text-[#9c988c]">
                  Repository Directory
                </label>
                <input
                  type="text"
                  value={repoDir}
                  onChange={(ev) => setRepoDir(ev.target.value)}
                  placeholder="Z:\repo\mw-xv"
                  className="w-full rounded-md border border-[#2a2a36] bg-[#0d0d10] px-3 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[11px] uppercase text-[#9c988c]">
                  Project Slug
                </label>
                <input
                  type="text"
                  value={projectSlug}
                  onChange={(ev) => setProjectSlug(ev.target.value)}
                  placeholder="moonwitness"
                  className="w-full rounded-md border border-[#2a2a36] bg-[#0d0d10] px-3 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
                />
              </div>
            </div>

            <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
              <label className="inline-flex cursor-pointer items-center gap-2 text-xs text-[#f3efe4]">
                <input
                  type="checkbox"
                  checked={includeAssets}
                  onChange={(ev) => setIncludeAssets(ev.target.checked)}
                  className="h-4 w-4 accent-[#ffe031]"
                />
                <span>Include media assets (SHA-256 deduplicated blobs)</span>
              </label>

              <button
                type="submit"
                disabled={importing}
                className="inline-flex items-center gap-2 rounded-md bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#0d0d10] transition-opacity hover:opacity-90 disabled:opacity-50"
              >
                <FolderGit2 className="h-3.5 w-3.5" />
                {importing ? 'IMPORTING...' : 'IMPORT REPO NOW'}
              </button>
            </div>
          </form>

          {lastImport ? (
            <div className="mt-4 rounded-md border border-[#22c55e]/40 bg-[#0d0d10] p-3">
              <div className="mb-2 flex flex-wrap items-center gap-2">
                <MonoBadge tone="ok">IMPORT COMPLETE</MonoBadge>
                <MonoBadge tone="yellow">{lastImport.docsImported} DOCS</MonoBadge>
                <MonoBadge tone="cyan">{lastImport.tasksImported} TASKS</MonoBadge>
                <MonoBadge tone="magenta">{lastImport.assetsImported} ASSETS</MonoBadge>
                {lastImport.skippedTooLarge > 0 ? (
                  <MonoBadge tone="err">{lastImport.skippedTooLarge} SKIPPED</MonoBadge>
                ) : null}
              </div>
              <div className="font-mono text-[11px] text-[#9c988c]">
                Source: {lastImport.sourceDir} → Project: {lastImport.project}
              </div>
            </div>
          ) : null}

          {/* Index & Task Distribution */}
          <div className="mt-4 grid grid-cols-1 gap-3 border-t border-[#2a2a36] pt-4 sm:grid-cols-2">
            <div className="rounded-md border border-[#2a2a36] bg-[#0d0d10] p-3">
              <div className="mb-2 font-mono text-[11px] font-semibold uppercase text-[#9c988c]">
                Tasks By Status
              </div>
              <div className="flex flex-wrap gap-1.5">
                {taskStatusEntries.length > 0 ? (
                  taskStatusEntries.map(([statusName, statusCount]) => (
                    <MonoBadge
                      key={statusName}
                      tone={
                        statusName === 'done'
                          ? 'ok'
                          : statusName === 'doing'
                            ? 'yellow'
                            : statusName === 'blocked'
                              ? 'err'
                              : 'cyan'
                      }
                    >
                      {statusName}: {statusCount}
                    </MonoBadge>
                  ))
                ) : (
                  <span className="font-mono text-xs text-[#9c988c]">No tasks indexed yet.</span>
                )}
              </div>
            </div>

            <div className="rounded-md border border-[#2a2a36] bg-[#0d0d10] p-3">
              <div className="mb-2 font-mono text-[11px] font-semibold uppercase text-[#9c988c]">
                Docs By Project
              </div>
              <div className="flex flex-wrap gap-1.5">
                {docsProjectEntries.length > 0 ? (
                  docsProjectEntries.map(([projName, docCount]) => (
                    <MonoBadge key={projName} tone="neutral">
                      {projName}: {docCount}
                    </MonoBadge>
                  ))
                ) : (
                  <span className="font-mono text-xs text-[#9c988c]">No docs indexed yet.</span>
                )}
              </div>
            </div>
          </div>
        </SectionCard>

        {/* Quick Copy Agent Env Snippets */}
        <SectionCard
          title="Quick Copy Agent Env Snippets"
          subtitle="Point Claude Code, Codex, Gemini CLI, OpenCode & MCP clients to mw-echo."
          icon={<Terminal className="h-4 w-4" />}
          actions={
            <div className="flex items-center gap-1.5">
              <button
                type="button"
                onClick={() => void handleCopyShellBlock('pwsh')}
                className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#1c1c23] px-2 py-1 font-mono text-[11px] font-semibold text-[#ffe031] hover:border-[#ffe031]"
              >
                {copiedShell === 'pwsh' ? (
                  <Check className="h-3 w-3 text-[#22c55e]" />
                ) : (
                  <Copy className="h-3 w-3" />
                )}
                PWSH ALL
              </button>
              <button
                type="button"
                onClick={() => void handleCopyShellBlock('bash')}
                className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#1c1c23] px-2 py-1 font-mono text-[11px] font-semibold text-[#00b8f0] hover:border-[#00b8f0]"
              >
                {copiedShell === 'bash' ? (
                  <Check className="h-3 w-3 text-[#22c55e]" />
                ) : (
                  <Copy className="h-3 w-3" />
                )}
                BASH ALL
              </button>
            </div>
          }
          className="xl:col-span-5"
        >
          <div className="space-y-2.5">
            <CopyableSnippet
              label="ANTHROPIC / CLAUDE CODE"
              value="ANTHROPIC_BASE_URL=http://127.0.0.1:4310"
              tone="yellow"
            />
            <CopyableSnippet
              label="OPENAI / CODEX / OPENCODE"
              value="OPENAI_BASE_URL=http://127.0.0.1:4310/v1"
              tone="cyan"
            />
            <CopyableSnippet
              label="MCP JSON-RPC 2.0"
              value="http://127.0.0.1:4310/api/mcp"
              tone="magenta"
            />
            <div className="rounded-md border border-[#2a2a36] bg-[#0d0d10] px-3 py-2 font-mono text-[11px] text-[#9c988c]">
              <div className="flex items-center justify-between">
                <span>HUB DATA DIR</span>
                <span className="text-[#22c55e]">ONLINE</span>
              </div>
              <div className="mt-1 truncate text-[#f3efe4]">{hubStats?.dataDir ?? 'Connecting...'}</div>
            </div>
          </div>
        </SectionCard>
      </div>
    </div>
  );
}
