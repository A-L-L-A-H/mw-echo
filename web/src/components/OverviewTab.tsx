import { useState } from 'react';
import {
  Activity,
  ArrowRight,
  BookOpen,
  Check,
  CheckSquare,
  Cloud,
  Copy,
  FolderGit2,
  MessageSquare,
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
  type WhatsAppStats,
} from '../api';
import { copyTextToClipboard, formatBytes, formatCompactNumber } from '../utils';
import {
  CopyableSnippet,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  StatTile,
} from './UiPrimitives';

interface OverviewTabProps {
  hubStats: HubStats | null;
  poolSummary: PoolSummary | null;
  aiStats: AiGatewayStats | null;
  whatsappStats: WhatsAppStats | null;
  aiAvailable: boolean;
  isRefreshing: boolean;
  onRefreshAll: () => Promise<void>;
  onOpenWhatsApp: () => void;
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
  whatsappStats,
  aiAvailable,
  isRefreshing,
  onRefreshAll,
  onOpenWhatsApp,
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
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 01"
        sticker="HQ STAGE"
        title="MOONWITNESS COMMAND DECK"
        subtitle="SQLITE FTS5 CODEX + TASKOS BOUNTY + GOWA COMMS + 9DRIVE VAULT + BIFROST/MAGPIE/9ROUTER RIG"
        tone="yellow"
        actions={
          <button
            type="button"
            onClick={() => void onRefreshAll()}
            disabled={isRefreshing}
            className="riot-btn inline-flex items-center gap-2 bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c] uppercase disabled:opacity-50"
          >
            <RefreshCw className={`h-4 w-4 ${isRefreshing ? 'animate-spin' : ''}`} />
            SYNC TELEMETRY
          </button>
        }
      />

      {/* 6 Explosive Comic Stat Stamps */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
        <StatTile
          label="CODEX DOCS"
          value={hubStats ? formatCompactNumber(hubStats.docsCount) : '—'}
          sublabel={`${Object.keys(hubStats?.docsByProject ?? {}).length} project(s) indexed`}
          icon={<BookOpen className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="BOUNTY OPEN"
          value={hubStats ? `${openTasks}/${totalTasks}` : '—'}
          sublabel={`${doneCount} cleared`}
          icon={<CheckSquare className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="VAULT FREE"
          value={poolSummary ? formatBytes(poolSummary.totalFreeBytes) : '—'}
          sublabel={
            poolSummary
              ? `${poolSummary.providers.length} acct · ${formatBytes(poolSummary.totalQuotaBytes)} cap`
              : 'Loading pool...'
          }
          icon={<Cloud className="h-4 w-4" />}
          tone="ok"
        />
        <StatTile
          label="RIG SORTIES"
          value={aiAvailable && aiStats ? formatCompactNumber(aiStats.totalRequests) : '0'}
          sublabel={
            aiAvailable && aiStats ? `${aiStats.providersCount} provider(s) armed` : 'Rig standby'
          }
          icon={<Activity className="h-4 w-4" />}
          tone="magenta"
        />
        <StatTile
          label="RTK SAVED"
          value={aiAvailable && aiStats ? formatCompactNumber(aiStats.totalRtkTokensSaved) : '0'}
          sublabel={
            aiAvailable && aiStats
              ? `${Math.round(aiStats.avgLatencyMs)}ms avg latency`
              : 'Turbo ready'
          }
          icon={<Sparkles className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="CACHE HITS"
          value={aiAvailable && aiStats ? formatCompactNumber(aiStats.cacheHits) : '0'}
          sublabel={
            aiAvailable && aiStats
              ? `${formatCompactNumber(aiStats.cacheTokensSaved)} tok saved`
              : 'Cache armed'
          }
          icon={<Zap className="h-4 w-4" />}
          tone="cyan"
        />
      </div>

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-12">
        {/* Local Repo Importer Deck */}
        <SectionCard
          title="ONE-CLICK REPO INGESTOR"
          subtitle="Slams Markdown docs into SQLite FTS5, TaskOS v2 tasks.json, and SHA-256 media blobs."
          icon={<FolderGit2 className="h-4 w-4" />}
          sticker="FAST LOAD"
          stickerTone="cyan"
          accent="yellow"
          className="xl:col-span-7"
        >
          <form onSubmit={handleImportSubmit} className="space-y-3.5">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <div className="sm:col-span-2">
                <label className="mb-1 block font-mono text-[11px] font-bold uppercase text-[#ffe031]">
                  Repository Directory
                </label>
                <input
                  type="text"
                  value={repoDir}
                  onChange={(ev) => setRepoDir(ev.target.value)}
                  placeholder="Z:\repo\mw-xv"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[11px] font-bold uppercase text-[#00f0ff]">
                  Project Slug
                </label>
                <input
                  type="text"
                  value={projectSlug}
                  onChange={(ev) => setProjectSlug(ev.target.value)}
                  placeholder="moonwitness"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
            </div>

            <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
              <label className="inline-flex cursor-pointer items-center gap-2 font-mono text-xs font-bold text-[#fff6de]">
                <input
                  type="checkbox"
                  checked={includeAssets}
                  onChange={(ev) => setIncludeAssets(ev.target.checked)}
                  className="h-4 w-4 accent-[#ffe031]"
                />
                <span>INCLUDE MEDIA ASSETS (SHA-256 DEDUPED)</span>
              </label>

              <button
                type="submit"
                disabled={importing}
                className="riot-btn inline-flex items-center gap-2 bg-[#ff2e88] px-5 py-2 font-display text-xs tracking-wider text-white uppercase disabled:opacity-50"
              >
                <FolderGit2 className="h-4 w-4" />
                {importing ? 'INGESTING...' : 'BLAST IMPORT!'}
              </button>
            </div>
          </form>

          {lastImport ? (
            <div className="mt-4 border-2 border-black bg-[#08080c] p-3 shadow-[4px_4px_0_#a3ff12]">
              <div className="mb-2 flex flex-wrap items-center gap-1.5">
                <MonoBadge tone="ok">IMPORT CLEARED</MonoBadge>
                <MonoBadge tone="yellow">{lastImport.docsImported} DOCS</MonoBadge>
                <MonoBadge tone="cyan">{lastImport.tasksImported} TASKS</MonoBadge>
                <MonoBadge tone="magenta">{lastImport.assetsImported} ASSETS</MonoBadge>
                {lastImport.skippedTooLarge > 0 ? (
                  <MonoBadge tone="err">{lastImport.skippedTooLarge} SKIPPED</MonoBadge>
                ) : null}
              </div>
              <div className="font-mono text-[11px] text-[#a6a2b8]">
                {lastImport.sourceDir} -&gt; {lastImport.project}
              </div>
            </div>
          ) : null}

          <div className="mt-4 grid grid-cols-1 gap-3 border-t-2 border-black pt-4 sm:grid-cols-2">
            <div className="border-2 border-black bg-[#08080c] p-3 shadow-[3px_3px_0_#000]">
              <div className="mb-2 font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                Bounty Status Breakdown
              </div>
              <div className="flex flex-wrap gap-1.5">
                {taskStatusEntries.length > 0 ? (
                  taskStatusEntries.map(([statusName, statusCount]) => (
                    <MonoBadge
                      key={statusName}
                      tone={
                        statusName === 'done'
                          ? 'ok'
                          : statusName === 'doing' || statusName === 'in_progress'
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
                  <span className="font-mono text-xs text-[#a6a2b8]">No tasks indexed yet.</span>
                )}
              </div>
            </div>

            <div className="border-2 border-black bg-[#08080c] p-3 shadow-[3px_3px_0_#000]">
              <div className="mb-2 font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                Codex By Arc
              </div>
              <div className="flex flex-wrap gap-1.5">
                {docsProjectEntries.length > 0 ? (
                  docsProjectEntries.map(([projName, docCount]) => (
                    <MonoBadge key={projName} tone="cream">
                      {projName}: {docCount}
                    </MonoBadge>
                  ))
                ) : (
                  <span className="font-mono text-xs text-[#a6a2b8]">No docs indexed yet.</span>
                )}
              </div>
            </div>
          </div>
        </SectionCard>

        {/* Agent Env Cartridge + GOWA Quick Launch */}
        <SectionCard
          title="AGENT LAUNCH CARTRIDGES"
          subtitle="Wire Claude Code, Codex, Gemini CLI, OpenCode & MCP clients straight to mw-echo."
          icon={<Terminal className="h-4 w-4" />}
          sticker="LIVE"
          stickerTone="ok"
          accent="magenta"
          actions={
            <div className="flex items-center gap-1.5">
              <button
                type="button"
                onClick={() => void handleCopyShellBlock('pwsh')}
                className="riot-btn inline-flex items-center gap-1 bg-[#ffe031] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#08080c]"
              >
                {copiedShell === 'pwsh' ? (
                  <Check className="h-3 w-3" />
                ) : (
                  <Copy className="h-3 w-3" />
                )}
                PWSH ALL
              </button>
              <button
                type="button"
                onClick={() => void handleCopyShellBlock('bash')}
                className="riot-btn inline-flex items-center gap-1 bg-[#00f0ff] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#08080c]"
              >
                {copiedShell === 'bash' ? (
                  <Check className="h-3 w-3" />
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

            {/* GOWA Comms Quick Launch Strip */}
            <div className="flex items-center justify-between gap-2 border-2 border-black bg-[#08080c] px-3 py-2.5 shadow-[4px_4px_0_#a3ff12]">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-1.5">
                  <MessageSquare className="h-4 w-4 text-[#a3ff12]" />
                  <span className="font-display text-xs text-[#fff6de] uppercase">
                    04 COMMS // GOWA
                  </span>
                  <MonoBadge tone={(whatsappStats?.connectedCount ?? 0) > 0 ? 'ok' : 'neutral'}>
                    {whatsappStats?.connectedCount ?? 0}/{whatsappStats?.devicesCount ?? 0} LINKED
                  </MonoBadge>
                </div>
                <div className="mt-1 font-mono text-[10px] font-bold text-[#a6a2b8]">
                  {formatCompactNumber(whatsappStats?.chatsCount ?? 0)} CHATS ·{' '}
                  {formatCompactNumber(whatsappStats?.messagesCount ?? 0)} MSGS ·{' '}
                  {(whatsappStats?.engine || 'gowa').toUpperCase()}
                </div>
              </div>
              <button
                type="button"
                onClick={onOpenWhatsApp}
                className="riot-btn inline-flex shrink-0 items-center gap-1 bg-[#a3ff12] px-3 py-1.5 font-mono text-[11px] font-extrabold text-[#08080c]"
              >
                JACK IN
                <ArrowRight className="h-3.5 w-3.5" />
              </button>
            </div>

            <div className="border-2 border-black bg-[#08080c] px-3 py-2 font-mono text-[11px] text-[#a6a2b8]">
              <div className="flex items-center justify-between">
                <span className="font-bold text-[#fff6de]">DATA VAULT DIR</span>
                <MonoBadge tone="ok">ONLINE</MonoBadge>
              </div>
              <div className="mt-1 truncate text-[#00f0ff]">
                {hubStats?.dataDir ?? 'Connecting...'}
              </div>
            </div>
          </div>
        </SectionCard>
      </div>
    </div>
  );
}
