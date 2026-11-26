import { useMemo, useState } from 'react';
import {
  Bot,
  GitCommitHorizontal,
  PlusCircle,
  Radar,
  Search,
  Server,
  Sparkles,
} from 'lucide-react';
import {
  DEFAULT_AGENT_IDS,
  sniffFreeModelCatalog,
  updateAiAgent,
  type AiGatewayStats,
  type FreeModelCatalogEntry,
} from '../api';
import { formatCompactNumber, formatShortTime } from '../utils';
import type { ProviderDraftPrefill } from './AiGatewayTab';
import { MonoBadge, SectionCard } from './UiPrimitives';

interface FreeModelsTabProps {
  entries: FreeModelCatalogEntry[];
  aiStats: AiGatewayStats | null;
  onReloadCatalogAndStats: () => Promise<void>;
  onAddProviderShortcut: (prefill: ProviderDraftPrefill) => void;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

export function FreeModelsTab({
  entries,
  aiStats,
  onReloadCatalogAndStats,
  onAddProviderShortcut,
  onNotify,
}: FreeModelsTabProps) {
  const [searchQuery, setSearchQuery] = useState('');
  const [providerFilter, setProviderFilter] = useState('');
  const [freeOnly, setFreeOnly] = useState(true);
  const [targetAgentId, setTargetAgentId] = useState<string>('claude-code');
  const [sniffing, setSniffing] = useState(false);
  const [assigningModelId, setAssigningModelId] = useState<string | null>(null);

  const uniqueProviders = useMemo(() => {
    const names = Array.from(
      new Set(entries.map((entry) => entry.providerName || entry.providerSlug).filter(Boolean)),
    );
    return names.sort((left, right) => left.localeCompare(right));
  }, [entries]);

  const filteredModels = useMemo(() => {
    const normalizedQuery = searchQuery.trim().toLowerCase();
    return entries.filter((entry) => {
      if (freeOnly && !entry.freeTier) {
        return false;
      }
      const label = entry.providerName || entry.providerSlug;
      if (providerFilter && label !== providerFilter) {
        return false;
      }
      if (!normalizedQuery) {
        return true;
      }
      const haystack = `${entry.providerSlug} ${entry.providerName} ${entry.modelId} ${entry.baseUrl} ${entry.source}`.toLowerCase();
      return haystack.includes(normalizedQuery);
    });
  }, [entries, freeOnly, providerFilter, searchQuery]);

  const handleSniffUpstream = async () => {
    setSniffing(true);
    try {
      const sniffSummary = await sniffFreeModelCatalog();
      await onReloadCatalogAndStats();
      const shortSha = sniffSummary.upstreamSha ? sniffSummary.upstreamSha.slice(0, 10) : 'curated';
      onNotify(
        'ok',
        `Sniffed ${sniffSummary.modelsIndexed} models across ${sniffSummary.providersFound} providers (SHA: ${shortSha}).`,
      );
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to sniff freellmapi upstream';
      onNotify('err', message);
    } finally {
      setSniffing(false);
    }
  };

  const handleUseInAgent = async (entry: FreeModelCatalogEntry) => {
    setAssigningModelId(entry.id);
    try {
      await updateAiAgent(targetAgentId, {
        targetModel: entry.modelId,
        tierOrder: 'free,cheap,subscription,paid',
        rtkEnabled: true,
      });
      onNotify('ok', `Assigned ${entry.modelId} to Magpie agent "${targetAgentId}".`);
      await onReloadCatalogAndStats();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to update agent route';
      onNotify('err', message);
    } finally {
      setAssigningModelId(null);
    }
  };

  const handleAddProvider = (entry: FreeModelCatalogEntry) => {
    onAddProviderShortcut({
      name: `${entry.providerSlug}-free`,
      kind: 'openai_compat',
      tier: entry.freeTier ? 'free' : 'cheap',
      baseUrl: entry.baseUrl || '',
      defaultModel: entry.modelId,
      priority: 10,
    });
    onNotify('info', `Prefilled Bifrost provider form for ${entry.providerName} (${entry.modelId}).`);
  };

  const upstreamRepo = aiStats?.settings.upstreamRepo || 'tashfeenahmed/freellmapi';
  const upstreamSha = aiStats?.settings.upstreamSha || '';
  const lastSniffAt = aiStats?.settings.lastSniffAt || '';

  return (
    <div className="space-y-5">
      <SectionCard
        title="FreeLLMAPI Upstream Model Sniffer"
        subtitle="Sync live zero-cost LLM endpoints & free-tier models from tashfeenahmed/freellmapi."
        icon={<Radar className="h-4 w-4" />}
        actions={
          <button
            type="button"
            disabled={sniffing}
            onClick={() => void handleSniffUpstream()}
            className="inline-flex items-center gap-2 rounded-md bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#0d0d10] transition-opacity hover:opacity-90 disabled:opacity-50"
          >
            <Radar className={`h-3.5 w-3.5 ${sniffing ? 'animate-spin' : ''}`} />
            {sniffing ? 'SNIFFING UPSTREAM...' : 'SNIFF UPSTREAM (freellmapi)'}
          </button>
        }
      >
        {/* Upstream Telemetry Strip */}
        <div className="mb-4 flex flex-wrap items-center gap-2 rounded-md border border-[#2a2a36] bg-[#0d0d10] px-3.5 py-2.5">
          <MonoBadge tone="cyan">REPO: {upstreamRepo}</MonoBadge>
          <MonoBadge tone="yellow">
            <GitCommitHorizontal className="h-3 w-3" />
            SHA: {upstreamSha ? upstreamSha.slice(0, 12) : 'SEEDED'}
          </MonoBadge>
          <MonoBadge tone="magenta">
            <Server className="h-3 w-3" />
            PROVIDERS: {uniqueProviders.length}
          </MonoBadge>
          <MonoBadge tone="ok">
            <Sparkles className="h-3 w-3" />
            MODELS: {entries.length}
          </MonoBadge>
          <span className="ml-auto font-mono text-[11px] text-[#9c988c]">
            Last Sniff: {formatShortTime(lastSniffAt)}
          </span>
        </div>

        {/* Search + Filter + Quick Agent Target */}
        <div className="mb-4 flex flex-wrap items-center gap-2.5">
          <div className="relative min-w-[240px] flex-1">
            <Search className="pointer-events-none absolute top-1/2 left-3 h-3.5 w-3.5 -translate-y-1/2 text-[#9c988c]" />
            <input
              type="search"
              value={searchQuery}
              onChange={(ev) => setSearchQuery(ev.target.value)}
              placeholder="Search model ID, provider, endpoint, source..."
              className="w-full rounded-md border border-[#2a2a36] bg-[#0d0d10] py-2 pr-3 pl-9 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>

          <select
            value={providerFilter}
            onChange={(ev) => setProviderFilter(ev.target.value)}
            className="rounded-md border border-[#2a2a36] bg-[#0d0d10] px-3 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
          >
            <option value="">All Providers ({uniqueProviders.length})</option>
            {uniqueProviders.map((providerName) => (
              <option key={providerName} value={providerName}>
                {providerName}
              </option>
            ))}
          </select>

          <label className="inline-flex cursor-pointer items-center gap-1.5 rounded-md border border-[#2a2a36] bg-[#0d0d10] px-3 py-2 font-mono text-xs text-[#f3efe4]">
            <input
              type="checkbox"
              checked={freeOnly}
              onChange={(ev) => setFreeOnly(ev.target.checked)}
              className="accent-[#ffe031]"
            />
            Free Only
          </label>

          <div className="flex items-center gap-2 rounded-md border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-1.5">
            <Bot className="h-3.5 w-3.5 text-[#ff2e88]" />
            <span className="font-mono text-[11px] text-[#9c988c]">Use In:</span>
            <select
              value={targetAgentId}
              onChange={(ev) => setTargetAgentId(ev.target.value)}
              className="bg-transparent font-mono text-xs font-bold text-[#ffe031] focus:outline-none"
            >
              {DEFAULT_AGENT_IDS.map((agentId) => (
                <option key={agentId} value={agentId} className="bg-[#15151a] text-[#f3efe4]">
                  {agentId}
                </option>
              ))}
            </select>
          </div>
        </div>

        {/* Catalog Table */}
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-left text-xs">
            <thead>
              <tr className="border-b border-[#2a2a36] font-mono text-[10px] uppercase tracking-wider text-[#9c988c]">
                <th className="px-3 py-2">Provider</th>
                <th className="px-3 py-2">Model ID</th>
                <th className="px-3 py-2">Context &amp; Tier</th>
                <th className="px-3 py-2">Base URL</th>
                <th className="px-3 py-2 text-right">Shortcuts</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#2a2a36]/70">
              {filteredModels.length > 0 ? (
                filteredModels.map((entry) => (
                  <tr key={entry.id} className="hover:bg-[#1c1c23]/60">
                    <td className="px-3 py-2.5">
                      <div className="flex items-center gap-1.5">
                        <MonoBadge tone="cyan">{entry.providerName}</MonoBadge>
                        <MonoBadge tone="neutral">{entry.source}</MonoBadge>
                      </div>
                    </td>
                    <td className="px-3 py-2.5 font-mono font-bold text-[#f3efe4]">
                      {entry.modelId}
                    </td>
                    <td className="px-3 py-2.5">
                      <div className="flex flex-wrap gap-1">
                        <MonoBadge tone="neutral">
                          {formatCompactNumber(entry.contextWindow)} CTX
                        </MonoBadge>
                        <MonoBadge tone={entry.freeTier ? 'ok' : 'yellow'}>
                          {entry.freeTier ? 'FREE' : 'CHEAP'}
                        </MonoBadge>
                      </div>
                    </td>
                    <td className="px-3 py-2.5">
                      <div className="max-w-[230px] truncate font-mono text-[11px] text-[#9c988c]">
                        {entry.baseUrl || '—'}
                      </div>
                    </td>
                    <td className="px-3 py-2.5">
                      <div className="flex items-center justify-end gap-1.5">
                        <button
                          type="button"
                          disabled={assigningModelId === entry.id}
                          onClick={() => void handleUseInAgent(entry)}
                          className="inline-flex items-center gap-1 rounded border border-[#ff2e88]/50 bg-[#ff2e88]/10 px-2.5 py-1 font-mono text-[11px] font-bold text-[#ff2e88] hover:bg-[#ff2e88]/20 disabled:opacity-50"
                        >
                          <Bot className="h-3 w-3" />
                          {assigningModelId === entry.id ? '...' : `USE IN ${targetAgentId}`}
                        </button>
                        <button
                          type="button"
                          onClick={() => handleAddProvider(entry)}
                          className="inline-flex items-center gap-1 rounded border border-[#ffe031]/50 bg-[#ffe031]/10 px-2.5 py-1 font-mono text-[11px] font-bold text-[#ffe031] hover:bg-[#ffe031]/20"
                        >
                          <PlusCircle className="h-3 w-3" />
                          ADD PROVIDER
                        </button>
                      </div>
                    </td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={5} className="px-3 py-8 text-center font-mono text-xs text-[#9c988c]">
                    {entries.length === 0
                      ? 'Catalog is empty. Click "SNIFF UPSTREAM (freellmapi)" to populate live free models.'
                      : 'No models match your search or filter.'}
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
