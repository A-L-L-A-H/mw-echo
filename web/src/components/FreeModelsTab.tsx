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
import { MonoBadge, SectionCard, StageHeroBanner } from './UiPrimitives';

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
      if (freeOnly && !entry.freeTier) return false;
      const label = entry.providerName || entry.providerSlug;
      if (providerFilter && label !== providerFilter) return false;
      if (!normalizedQuery) return true;
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
        `Sniffed ${sniffSummary.modelsIndexed} loot models across ${sniffSummary.providersFound} vendors (SHA: ${shortSha}).`,
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
      onNotify('ok', `Equipped ${entry.modelId} onto pilot "${targetAgentId}".`);
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
    onNotify('info', `Prefilled Bifrost loadout for ${entry.providerName} (${entry.modelId}).`);
  };

  const upstreamRepo = aiStats?.settings.upstreamRepo || 'tashfeenahmed/freellmapi';
  const upstreamSha = aiStats?.settings.upstreamSha || '';
  const lastSniffAt = aiStats?.settings.lastSniffAt || '';

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 06"
        sticker="BLACK MARKET"
        title="FREE LLM LOOT SNIFFER"
        subtitle="AUTO-HARVEST ZERO-COST LLM ENDPOINTS FROM TASHFEENAHMED/FREELLMAPI — ONE-CLICK EQUIP OR FORGE PROVIDER"
        tone="yellow"
        actions={
          <button
            type="button"
            disabled={sniffing}
            onClick={() => void handleSniffUpstream()}
            className="riot-btn inline-flex items-center gap-2 bg-[#ffe031] px-5 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
          >
            <Radar className={`h-4 w-4 ${sniffing ? 'animate-spin' : ''}`} />
            {sniffing ? 'SNIFFING UPSTREAM...' : 'SNIFF UPSTREAM (FREELLMAPI)'}
          </button>
        }
      />

      <SectionCard
        title="FREE MODEL STASH CATALOG"
        subtitle="Search across providers, context sizes, and equip directly to any Magpie CLI pilot."
        icon={<Radar className="h-4 w-4" />}
        sticker="FREE TIER"
        stickerTone="ok"
        accent="cyan"
      >
        {/* Upstream Telemetry Strip */}
        <div className="mb-4 flex flex-wrap items-center gap-2 border-2 border-black bg-[#08080c] px-3.5 py-2.5 shadow-[4px_4px_0_#000]">
          <MonoBadge tone="cyan">REPO: {upstreamRepo}</MonoBadge>
          <MonoBadge tone="yellow">
            <GitCommitHorizontal className="h-3 w-3" />
            SHA: {upstreamSha ? upstreamSha.slice(0, 12) : 'SEEDED'}
          </MonoBadge>
          <MonoBadge tone="magenta">
            <Server className="h-3 w-3" />
            VENDORS: {uniqueProviders.length}
          </MonoBadge>
          <MonoBadge tone="ok">
            <Sparkles className="h-3 w-3" />
            LOOT: {entries.length}
          </MonoBadge>
          <span className="ml-auto font-mono text-[11px] font-bold text-[#a6a2b8]">
            LAST SNIFF: {formatShortTime(lastSniffAt)}
          </span>
        </div>

        {/* Search + Filter + Quick Equip Pilot */}
        <div className="mb-4 flex flex-wrap items-center gap-2.5">
          <div className="relative min-w-[240px] flex-1">
            <Search className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-[#ffe031]" />
            <input
              type="search"
              value={searchQuery}
              onChange={(ev) => setSearchQuery(ev.target.value)}
              placeholder="Search model ID, provider, baseUrl, source..."
              className="riot-input w-full py-2 pr-3 pl-9 font-mono text-xs"
            />
          </div>

          <select
            value={providerFilter}
            onChange={(ev) => setProviderFilter(ev.target.value)}
            className="riot-input px-3 py-2 font-mono text-xs font-bold"
          >
            <option value="">All Vendors ({uniqueProviders.length})</option>
            {uniqueProviders.map((providerName) => (
              <option key={providerName} value={providerName}>
                {providerName}
              </option>
            ))}
          </select>

          <label className="inline-flex cursor-pointer items-center gap-2 border-2 border-black bg-[#08080c] px-3 py-2 font-mono text-xs font-extrabold text-[#a3ff12]">
            <input
              type="checkbox"
              checked={freeOnly}
              onChange={(ev) => setFreeOnly(ev.target.checked)}
              className="h-4 w-4 accent-[#a3ff12]"
            />
            FREE ONLY
          </label>

          <div className="flex items-center gap-2 border-2 border-black bg-[#ff2e88] px-3 py-1.5 shadow-[3px_3px_0_#000]">
            <Bot className="h-4 w-4 text-white" />
            <span className="font-mono text-[11px] font-extrabold text-white uppercase">
              EQUIP TO:
            </span>
            <select
              value={targetAgentId}
              onChange={(ev) => setTargetAgentId(ev.target.value)}
              className="border-2 border-black bg-[#08080c] px-2 py-0.5 font-mono text-xs font-extrabold text-[#ffe031] focus:outline-none"
            >
              {DEFAULT_AGENT_IDS.map((agentId) => (
                <option key={agentId} value={agentId}>
                  {agentId}
                </option>
              ))}
            </select>
          </div>
        </div>

        {/* Loot Table */}
        <div className="overflow-x-auto border-2 border-black bg-[#08080c]">
          <table className="w-full border-collapse text-left text-xs">
            <thead>
              <tr className="border-b-2 border-black bg-[#ffe031] font-mono text-[10px] font-extrabold uppercase tracking-wider text-[#08080c]">
                <th className="px-3 py-2">Vendor</th>
                <th className="px-3 py-2">Model ID</th>
                <th className="px-3 py-2">Context &amp; Tier</th>
                <th className="px-3 py-2">Base URL</th>
                <th className="px-3 py-2 text-right">Quick Equip</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#252538]">
              {filteredModels.length > 0 ? (
                filteredModels.map((entry) => (
                  <tr key={entry.id} className="hover:bg-[#1b1b2b]">
                    <td className="px-3 py-2.5">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <MonoBadge tone="cyan">{entry.providerName}</MonoBadge>
                        <MonoBadge tone="neutral">{entry.source}</MonoBadge>
                      </div>
                    </td>
                    <td className="px-3 py-2.5 font-mono font-extrabold text-[#fff6de]">
                      {entry.modelId}
                    </td>
                    <td className="px-3 py-2.5">
                      <div className="flex flex-wrap gap-1">
                        <MonoBadge tone="cream">
                          {formatCompactNumber(entry.contextWindow)} CTX
                        </MonoBadge>
                        <MonoBadge tone={entry.freeTier ? 'ok' : 'yellow'}>
                          {entry.freeTier ? 'FREE' : 'CHEAP'}
                        </MonoBadge>
                      </div>
                    </td>
                    <td className="px-3 py-2.5">
                      <div className="max-w-[230px] truncate font-mono text-[11px] text-[#a6a2b8]">
                        {entry.baseUrl || '—'}
                      </div>
                    </td>
                    <td className="px-3 py-2.5">
                      <div className="flex items-center justify-end gap-1.5">
                        <button
                          type="button"
                          disabled={assigningModelId === entry.id}
                          onClick={() => void handleUseInAgent(entry)}
                          className="riot-btn inline-flex items-center gap-1 bg-[#ff2e88] px-2.5 py-1 font-mono text-[10px] font-extrabold text-white uppercase disabled:opacity-50"
                        >
                          <Bot className="h-3 w-3" />
                          {assigningModelId === entry.id ? '...' : `EQUIP ${targetAgentId}`}
                        </button>
                        <button
                          type="button"
                          onClick={() => handleAddProvider(entry)}
                          className="riot-btn inline-flex items-center gap-1 bg-[#ffe031] px-2.5 py-1 font-mono text-[10px] font-extrabold text-[#08080c] uppercase"
                        >
                          <PlusCircle className="h-3 w-3" />
                          FORGE KEY
                        </button>
                      </div>
                    </td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td
                    colSpan={5}
                    className="px-3 py-8 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]"
                  >
                    {entries.length === 0
                      ? 'STASH EMPTY. HIT "SNIFF UPSTREAM" TO HARVEST FREE MODELS.'
                      : 'NO LOOT MATCHES YOUR SEARCH.'}
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
