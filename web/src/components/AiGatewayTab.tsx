import { useEffect, useState } from 'react';
import {
  Activity,
  Bot,
  Cpu,
  Flame,
  KeyRound,
  Play,
  Plus,
  RefreshCw,
  Save,
  ShieldAlert,
  Sparkles,
  Trash2,
  Zap,
} from 'lucide-react';
import {
  clearAiCache,
  createAiProvider,
  DEFAULT_AGENT_IDS,
  deleteAiProvider,
  sendChatCompletionTest,
  updateAiAgent,
  updateAiSettings,
  type AiAgentRoute,
  type AiGatewayStats,
  type AiProviderCreateInput,
  type AiProviderRecord,
  type AiSettings,
  type ChatCompletionTestResult,
  type FreeModelCatalogEntry,
} from '../api';
import {
  formatCompactNumber,
  formatCooldownRemaining,
  formatShortTime,
  isCooldownActive,
} from '../utils';
import { MonoBadge, SectionCard, type BadgeTone } from './UiPrimitives';

export interface ProviderDraftPrefill {
  name: string;
  kind: string;
  tier: string;
  baseUrl: string;
  defaultModel: string;
  priority: number;
}

interface AiGatewayTabProps {
  aiStats: AiGatewayStats | null;
  providers: AiProviderRecord[];
  agents: AiAgentRoute[];
  catalogModels: FreeModelCatalogEntry[];
  providerPrefill: ProviderDraftPrefill | null;
  onConsumePrefill: () => void;
  onReloadGateway: () => Promise<void>;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

const FALLBACK_PRESETS = [
  {
    value: 'free,cheap,subscription,paid',
    label: 'Free First (free -> cheap -> subscription -> paid)',
  },
  {
    value: 'subscription,cheap,free',
    label: 'Subscription First (subscription -> cheap -> free)',
  },
  {
    value: 'cheap,free,subscription,paid',
    label: 'Cheap First (cheap -> free -> subscription -> paid)',
  },
  {
    value: 'free,paid',
    label: 'Free -> Paid Direct (free -> paid)',
  },
];

const TIER_TONES: Record<string, BadgeTone> = {
  free: 'cyan',
  cheap: 'yellow',
  subscription: 'magenta',
  paid: 'neutral',
};

interface AgentDraftState {
  targetModel: string;
  tierOrder: string;
  rtkEnabled: boolean;
  dirty: boolean;
}

export function AiGatewayTab({
  aiStats,
  providers,
  agents,
  catalogModels,
  providerPrefill,
  onConsumePrefill,
  onReloadGateway,
  onNotify,
}: AiGatewayTabProps) {
  // 9Router global settings state
  const [rtkEnabled, setRtkEnabled] = useState(true);
  const [cacheEnabled, setCacheEnabled] = useState(true);
  const [fallbackOrder, setFallbackOrder] = useState('free,cheap,subscription,paid');
  const [savingSettings, setSavingSettings] = useState(false);
  const [clearingCache, setClearingCache] = useState(false);

  // Magpie Agent Switchboard draft map
  const [agentDrafts, setAgentDrafts] = useState<Record<string, AgentDraftState>>({});
  const [savingAgentId, setSavingAgentId] = useState<string | null>(null);

  // Bifrost Provider creation form (kind matches Go constants: openai_compat | anthropic | gemini)
  const [provName, setProvName] = useState('');
  const [provKind, setProvKind] = useState('openai_compat');
  const [provTier, setProvTier] = useState('free');
  const [provBaseUrl, setProvBaseUrl] = useState('https://api.groq.com/openai/v1');
  const [provApiKey, setProvApiKey] = useState('');
  const [provDefaultModel, setProvDefaultModel] = useState('llama-3.3-70b-versatile');
  const [provPriority, setProvPriority] = useState('10');
  const [creatingProvider, setCreatingProvider] = useState(false);

  // Live Chat Test Playground
  const [playAgentId, setPlayAgentId] = useState<string>('claude-code');
  const [playModel, setPlayModel] = useState<string>('auto');
  const [playPrompt, setPlayPrompt] = useState<string>(
    'Reply in one short sentence confirming which model handled this request.',
  );
  const [sendingChat, setSendingChat] = useState(false);
  const [chatResult, setChatResult] = useState<ChatCompletionTestResult | null>(null);
  const [showRawHeaders, setShowRawHeaders] = useState(false);

  useEffect(() => {
    if (!aiStats?.settings) {
      return;
    }
    setRtkEnabled(aiStats.settings.rtkEnabled);
    setCacheEnabled(aiStats.settings.cacheEnabled);
    setFallbackOrder(aiStats.settings.fallbackOrder || 'free,cheap,subscription,paid');
  }, [aiStats]);

  useEffect(() => {
    const nextDrafts: Record<string, AgentDraftState> = {};
    const byId = new Map(agents.map((route) => [route.agentId, route]));
    const orderedIds = Array.from(
      new Set([...DEFAULT_AGENT_IDS, ...agents.map((route) => route.agentId)]),
    );
    for (const agentId of orderedIds) {
      const existing = byId.get(agentId);
      nextDrafts[agentId] = {
        targetModel: existing?.targetModel ?? '',
        tierOrder: existing?.tierOrder || 'free,cheap,subscription,paid',
        rtkEnabled: existing?.rtkEnabled ?? true,
        dirty: false,
      };
    }
    setAgentDrafts(nextDrafts);
  }, [agents]);

  useEffect(() => {
    if (!providerPrefill) {
      return;
    }
    setProvName(providerPrefill.name);
    setProvKind(providerPrefill.kind || 'openai_compat');
    setProvTier(providerPrefill.tier || 'free');
    setProvBaseUrl(providerPrefill.baseUrl);
    setProvDefaultModel(providerPrefill.defaultModel);
    setProvPriority(String(providerPrefill.priority || 10));
    onConsumePrefill();
  }, [providerPrefill, onConsumePrefill]);

  const modelSuggestions = Array.from(
    new Set([
      'auto',
      ...providers.map((provider) => provider.defaultModel).filter(Boolean),
      ...catalogModels.slice(0, 100).map((entry) => entry.modelId).filter(Boolean),
    ]),
  );

  const persistGlobalSettings = async (override?: Partial<AiSettings>) => {
    const nextSettings: Partial<AiSettings> = {
      rtkEnabled: override?.rtkEnabled ?? rtkEnabled,
      cacheEnabled: override?.cacheEnabled ?? cacheEnabled,
      fallbackOrder: override?.fallbackOrder ?? fallbackOrder,
    };
    setSavingSettings(true);
    try {
      const updated = await updateAiSettings(nextSettings);
      setRtkEnabled(updated.rtkEnabled);
      setCacheEnabled(updated.cacheEnabled);
      setFallbackOrder(updated.fallbackOrder);
      onNotify('ok', '9Router gateway settings saved.');
      await onReloadGateway();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to update AI settings';
      onNotify('err', message);
    } finally {
      setSavingSettings(false);
    }
  };

  const handleClearCache = async () => {
    setClearingCache(true);
    try {
      const outcome = await clearAiCache();
      onNotify('ok', `Exact response cache cleared (${outcome.purged} entries purged).`);
      await onReloadGateway();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to clear AI cache';
      onNotify('err', message);
    } finally {
      setClearingCache(false);
    }
  };

  const patchAgentDraft = (agentId: string, changes: Partial<AgentDraftState>) => {
    setAgentDrafts((prev) => {
      const current = prev[agentId] ?? {
        targetModel: '',
        tierOrder: 'free,cheap,subscription,paid',
        rtkEnabled: true,
        dirty: false,
      };
      return {
        ...prev,
        [agentId]: { ...current, ...changes, dirty: true },
      };
    });
  };

  const handleSaveAgent = async (agentId: string) => {
    const draft = agentDrafts[agentId];
    if (!draft) {
      return;
    }
    setSavingAgentId(agentId);
    try {
      await updateAiAgent(agentId, {
        targetModel: draft.targetModel.trim(),
        tierOrder: draft.tierOrder.trim() || 'free,cheap,subscription,paid',
        rtkEnabled: draft.rtkEnabled,
      });
      setAgentDrafts((prev) => ({
        ...prev,
        [agentId]: { ...draft, dirty: false },
      }));
      onNotify('ok', `Saved Magpie route for ${agentId}.`);
      await onReloadGateway();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : `Failed to update ${agentId}`;
      onNotify('err', message);
    } finally {
      setSavingAgentId(null);
    }
  };

  const handleAddProvider = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!provName.trim() || !provBaseUrl.trim()) {
      onNotify('err', 'Provider name and Base URL are required.');
      return;
    }
    const payload: AiProviderCreateInput = {
      name: provName.trim(),
      kind: provKind,
      tier: provTier,
      baseUrl: provBaseUrl.trim(),
      apiKey: provApiKey.trim(),
      defaultModel: provDefaultModel.trim(),
      priority: Number(provPriority) || 50,
    };
    setCreatingProvider(true);
    try {
      await createAiProvider(payload);
      setProvName('');
      setProvApiKey('');
      onNotify('ok', `Added Bifrost provider "${payload.name}".`);
      await onReloadGateway();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to add provider';
      onNotify('err', message);
    } finally {
      setCreatingProvider(false);
    }
  };

  const handleDeleteProvider = async (provider: AiProviderRecord) => {
    try {
      await deleteAiProvider(provider.id);
      onNotify('ok', `Removed provider "${provider.name}".`);
      await onReloadGateway();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to remove provider';
      onNotify('err', message);
    }
  };

  const handleRunPlayground = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!playPrompt.trim()) {
      onNotify('err', 'Enter a test prompt first.');
      return;
    }
    setSendingChat(true);
    try {
      const completion = await sendChatCompletionTest({
        agentId: playAgentId,
        model: playModel.trim() || 'auto',
        messages: [{ role: 'user', content: playPrompt.trim() }],
      });
      setChatResult(completion);
      onNotify(
        'ok',
        `Completion via ${completion.providerUsed} (${completion.latencyMs} ms, cache: ${completion.cacheStatus}).`,
      );
      await onReloadGateway();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Chat completion test failed';
      onNotify('err', message);
    } finally {
      setSendingChat(false);
    }
  };

  const orderedAgentIds = Object.keys(agentDrafts);
  const recentLogs = aiStats?.recentLogs ?? [];

  return (
    <div className="space-y-5">
      <datalist id="mw-model-suggestions">
        {modelSuggestions.map((modelName) => (
          <option key={modelName} value={modelName} />
        ))}
      </datalist>

      {/* 1. 9Router Token Saver & Cache Control Bar */}
      <SectionCard
        title="9Router Token Saver & Exact Cache Bar"
        subtitle="Global RTK tool-result compression, exact response caching, and tier fallback cascade."
        icon={<Zap className="h-4 w-4" />}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <MonoBadge tone="yellow">
              RTK SAVED: {formatCompactNumber(aiStats?.totalRtkTokensSaved ?? 0)} TOK
            </MonoBadge>
            <MonoBadge tone="cyan">
              CACHE: {formatCompactNumber(aiStats?.cacheHits ?? 0)} HITS (
              {formatCompactNumber(aiStats?.cacheTokensSaved ?? 0)} TOK)
            </MonoBadge>
            <MonoBadge tone="magenta">
              REQS: {formatCompactNumber(aiStats?.totalRequests ?? 0)} ·{' '}
              {Math.round(aiStats?.avgLatencyMs ?? 0)}ms AVG
            </MonoBadge>
            <button
              type="button"
              onClick={() => void onReloadGateway()}
              className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#1c1c23] px-2.5 py-1 font-mono text-[11px] font-semibold text-[#f3efe4] hover:border-[#ffe031]"
            >
              <RefreshCw className="h-3 w-3" />
              SYNC
            </button>
          </div>
        }
      >
        <div className="flex flex-wrap items-center gap-3">
          <button
            type="button"
            disabled={savingSettings}
            onClick={() => {
              const nextVal = !rtkEnabled;
              setRtkEnabled(nextVal);
              void persistGlobalSettings({ rtkEnabled: nextVal });
            }}
            className={`inline-flex items-center gap-2 rounded-md border px-3 py-2 font-mono text-xs font-bold transition-colors ${
              rtkEnabled
                ? 'border-[#ffe031] bg-[#ffe031]/15 text-[#ffe031]'
                : 'border-[#2a2a36] bg-[#0d0d10] text-[#9c988c]'
            }`}
          >
            <Sparkles className="h-3.5 w-3.5" />
            RTK TOOL COMPRESSION: {rtkEnabled ? 'ON' : 'OFF'}
          </button>

          <button
            type="button"
            disabled={savingSettings}
            onClick={() => {
              const nextVal = !cacheEnabled;
              setCacheEnabled(nextVal);
              void persistGlobalSettings({ cacheEnabled: nextVal });
            }}
            className={`inline-flex items-center gap-2 rounded-md border px-3 py-2 font-mono text-xs font-bold transition-colors ${
              cacheEnabled
                ? 'border-[#00b8f0] bg-[#00b8f0]/15 text-[#00b8f0]'
                : 'border-[#2a2a36] bg-[#0d0d10] text-[#9c988c]'
            }`}
          >
            <Zap className="h-3.5 w-3.5" />
            EXACT CACHE: {cacheEnabled ? 'ON' : 'OFF'}
          </button>

          <div className="flex flex-1 flex-wrap items-center gap-2 sm:min-w-[300px]">
            <label className="font-mono text-[11px] uppercase text-[#9c988c]">Fallback Order:</label>
            <select
              value={
                FALLBACK_PRESETS.some((preset) => preset.value === fallbackOrder)
                  ? fallbackOrder
                  : 'custom'
              }
              onChange={(ev) => {
                if (ev.target.value !== 'custom') {
                  setFallbackOrder(ev.target.value);
                  void persistGlobalSettings({ fallbackOrder: ev.target.value });
                }
              }}
              className="flex-1 rounded-md border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            >
              {FALLBACK_PRESETS.map((preset) => (
                <option key={preset.value} value={preset.value}>
                  {preset.label}
                </option>
              ))}
              {!FALLBACK_PRESETS.some((preset) => preset.value === fallbackOrder) ? (
                <option value="custom">Custom: {fallbackOrder}</option>
              ) : null}
            </select>
          </div>

          <button
            type="button"
            disabled={clearingCache}
            onClick={() => void handleClearCache()}
            className="inline-flex items-center gap-1.5 rounded-md border border-[#ef4444]/50 bg-[#ef4444]/10 px-3 py-2 font-mono text-xs font-bold text-[#ef4444] transition-colors hover:bg-[#ef4444]/20 disabled:opacity-50"
          >
            <Flame className="h-3.5 w-3.5" />
            {clearingCache ? 'CLEARING...' : 'CLEAR CACHE'}
          </button>
        </div>
      </SectionCard>

      {/* 2. Magpie Agent Switchboard */}
      <SectionCard
        title="Magpie Agent Switchboard"
        subtitle="Route each coding CLI independently to any model, fallback tier order, and per-agent RTK compression."
        icon={<Bot className="h-4 w-4" />}
      >
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
          {orderedAgentIds.map((agentId) => {
            const draft = agentDrafts[agentId];
            if (!draft) {
              return null;
            }
            const isSavingThis = savingAgentId === agentId;
            return (
              <div
                key={agentId}
                className={`rounded-lg border p-3.5 transition-colors ${
                  draft.dirty
                    ? 'border-[#ffe031]/70 bg-[#1c1c23]'
                    : 'border-[#2a2a36] bg-[#0d0d10]'
                }`}
              >
                <div className="mb-3 flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <Cpu className="h-4 w-4 text-[#ff2e88]" />
                    <span className="font-mono text-sm font-bold text-[#f3efe4]">{agentId}</span>
                  </div>
                  <div className="flex items-center gap-1.5">
                    {draft.dirty ? <MonoBadge tone="yellow">UNSAVED</MonoBadge> : null}
                    <button
                      type="button"
                      onClick={() =>
                        patchAgentDraft(agentId, { rtkEnabled: !draft.rtkEnabled })
                      }
                      className={`rounded border px-2 py-0.5 font-mono text-[10px] font-bold transition-colors ${
                        draft.rtkEnabled
                          ? 'border-[#22c55e]/50 bg-[#22c55e]/15 text-[#22c55e]'
                          : 'border-[#2a2a36] bg-[#15151a] text-[#9c988c]'
                      }`}
                    >
                      RTK {draft.rtkEnabled ? 'ON' : 'OFF'}
                    </button>
                  </div>
                </div>

                <div className="space-y-2.5">
                  <div>
                    <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
                      Target Model (empty = auto)
                    </label>
                    <input
                      type="text"
                      list="mw-model-suggestions"
                      value={draft.targetModel}
                      onChange={(ev) =>
                        patchAgentDraft(agentId, { targetModel: ev.target.value })
                      }
                      placeholder="auto / llama-3.3-70b-versatile..."
                      className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
                    />
                  </div>

                  <div>
                    <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
                      Fallback Tier Order
                    </label>
                    <select
                      value={
                        FALLBACK_PRESETS.some((preset) => preset.value === draft.tierOrder)
                          ? draft.tierOrder
                          : 'custom'
                      }
                      onChange={(ev) => {
                        if (ev.target.value !== 'custom') {
                          patchAgentDraft(agentId, { tierOrder: ev.target.value });
                        }
                      }}
                      className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
                    >
                      {FALLBACK_PRESETS.map((preset) => (
                        <option key={preset.value} value={preset.value}>
                          {preset.value}
                        </option>
                      ))}
                      {!FALLBACK_PRESETS.some((preset) => preset.value === draft.tierOrder) ? (
                        <option value="custom">{draft.tierOrder}</option>
                      ) : null}
                    </select>
                  </div>

                  <div className="flex justify-end pt-1">
                    <button
                      type="button"
                      disabled={isSavingThis}
                      onClick={() => void handleSaveAgent(agentId)}
                      className={`inline-flex items-center gap-1.5 rounded px-3 py-1.5 font-mono text-[11px] font-bold transition-colors ${
                        draft.dirty
                          ? 'bg-[#ffe031] text-[#0d0d10] hover:opacity-90'
                          : 'border border-[#2a2a36] bg-[#15151a] text-[#f3efe4] hover:border-[#ffe031] hover:text-[#ffe031]'
                      }`}
                    >
                      <Save className="h-3 w-3" />
                      {isSavingThis ? 'SAVING...' : 'SAVE ROUTE'}
                    </button>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      </SectionCard>

      {/* 3. Bifrost Provider Key Pool */}
      <SectionCard
        title="Bifrost Provider Key Pool"
        subtitle="Manage OpenAI-compatible, Anthropic & Gemini upstream keys with SecretBox encryption and live cooldown telemetry."
        icon={<KeyRound className="h-4 w-4" />}
      >
        <form
          onSubmit={handleAddProvider}
          className="mb-4 grid grid-cols-1 gap-2.5 rounded-lg border border-[#2a2a36] bg-[#0d0d10] p-3.5 md:grid-cols-2 lg:grid-cols-4"
        >
          <div>
            <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
              Provider Name
            </label>
            <input
              type="text"
              value={provName}
              onChange={(ev) => setProvName(ev.target.value)}
              placeholder="e.g. groq-free-1"
              className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
              Protocol Kind
            </label>
            <select
              value={provKind}
              onChange={(ev) => setProvKind(ev.target.value)}
              className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            >
              <option value="openai_compat">openai_compat (OpenAI / Groq / OpenRouter)</option>
              <option value="anthropic">anthropic (Claude API)</option>
              <option value="gemini">gemini (Native Gemini API)</option>
            </select>
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
              Cost Tier
            </label>
            <select
              value={provTier}
              onChange={(ev) => setProvTier(ev.target.value)}
              className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            >
              <option value="free">free</option>
              <option value="cheap">cheap</option>
              <option value="subscription">subscription</option>
              <option value="paid">paid</option>
            </select>
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
              Priority (Lower = First)
            </label>
            <input
              type="number"
              value={provPriority}
              onChange={(ev) => setProvPriority(ev.target.value)}
              className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>

          <div className="lg:col-span-2">
            <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
              Base URL
            </label>
            <input
              type="text"
              value={provBaseUrl}
              onChange={(ev) => setProvBaseUrl(ev.target.value)}
              placeholder="https://api.groq.com/openai/v1"
              className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
              Default Model
            </label>
            <input
              type="text"
              list="mw-model-suggestions"
              value={provDefaultModel}
              onChange={(ev) => setProvDefaultModel(ev.target.value)}
              placeholder="llama-3.3-70b-versatile"
              className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
              API Key (Encrypted at rest)
            </label>
            <div className="flex gap-2">
              <input
                type="password"
                value={provApiKey}
                onChange={(ev) => setProvApiKey(ev.target.value)}
                placeholder="sk-... / gsk_..."
                className="min-w-0 flex-1 rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
              <button
                type="submit"
                disabled={creatingProvider}
                className="inline-flex shrink-0 items-center gap-1 rounded bg-[#ffe031] px-3 py-1.5 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90 disabled:opacity-50"
              >
                <Plus className="h-3.5 w-3.5" />
                {creatingProvider ? '...' : 'ADD'}
              </button>
            </div>
          </div>
        </form>

        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-left text-xs">
            <thead>
              <tr className="border-b border-[#2a2a36] font-mono text-[10px] uppercase tracking-wider text-[#9c988c]">
                <th className="px-3 py-2">Provider</th>
                <th className="px-3 py-2">Kind / Tier</th>
                <th className="px-3 py-2">Endpoint &amp; Model</th>
                <th className="px-3 py-2">Priority</th>
                <th className="px-3 py-2">Live Telemetry</th>
                <th className="px-3 py-2 text-right">Action</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#2a2a36]/70">
              {providers.length > 0 ? (
                providers.map((provider) => {
                  const coolingDown = provider.inCooldown || isCooldownActive(provider.cooldownUntil);
                  const remaining = formatCooldownRemaining(provider.cooldownUntil);
                  const tierTone = TIER_TONES[provider.tier] ?? 'neutral';

                  return (
                    <tr key={provider.id} className="hover:bg-[#1c1c23]/60">
                      <td className="px-3 py-2.5">
                        <div className="font-mono font-bold text-[#f3efe4]">{provider.name}</div>
                        <div className="font-mono text-[10px] text-[#9c988c]">
                          {provider.hasApiKey ? 'Key encrypted' : 'No API key (local/public)'}
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex flex-wrap gap-1">
                          <MonoBadge tone="neutral">{provider.kind}</MonoBadge>
                          <MonoBadge tone={tierTone}>{provider.tier}</MonoBadge>
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="max-w-xs truncate font-mono text-[11px] text-[#f3efe4]">
                          {provider.defaultModel || '—'}
                        </div>
                        <div className="max-w-xs truncate font-mono text-[10px] text-[#9c988c]">
                          {provider.baseUrl}
                        </div>
                      </td>
                      <td className="px-3 py-2.5 font-mono text-[#9c988c]">{provider.priority}</td>
                      <td className="px-3 py-2.5">
                        <div className="flex flex-wrap items-center gap-1.5">
                          {coolingDown ? (
                            <MonoBadge tone="err">
                              <ShieldAlert className="h-3 w-3" />
                              COOLDOWN {remaining || 'ACTIVE'}
                            </MonoBadge>
                          ) : (
                            <MonoBadge tone="ok">HEALTHY</MonoBadge>
                          )}
                          <MonoBadge tone="cyan">REQ {provider.reqCount}</MonoBadge>
                          <MonoBadge tone={provider.errCount > 0 ? 'err' : 'neutral'}>
                            ERR {provider.errCount}
                          </MonoBadge>
                          {provider.tokensIn + provider.tokensOut > 0 ? (
                            <MonoBadge tone="neutral">
                              {formatCompactNumber(provider.tokensIn + provider.tokensOut)} TOK
                            </MonoBadge>
                          ) : null}
                        </div>
                      </td>
                      <td className="px-3 py-2.5 text-right">
                        <button
                          type="button"
                          onClick={() => void handleDeleteProvider(provider)}
                          className="inline-flex items-center gap-1 rounded border border-[#ef4444]/40 bg-[#ef4444]/10 px-2 py-1 font-mono text-[11px] text-[#ef4444] hover:bg-[#ef4444]/20"
                        >
                          <Trash2 className="h-3 w-3" />
                          DEL
                        </button>
                      </td>
                    </tr>
                  );
                })
              ) : (
                <tr>
                  <td colSpan={6} className="px-3 py-6 text-center font-mono text-xs text-[#9c988c]">
                    No Bifrost providers configured yet. Add an upstream key above or pick one from
                    the Free Models tab.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </SectionCard>

      {/* 4. Live Chat Test Playground */}
      <SectionCard
        title="Live Chat Test Playground"
        subtitle="Dispatch a real POST /v1/chat/completions probe and inspect latency, provider routing, cache & RTK headers."
        icon={<Play className="h-4 w-4" />}
      >
        <form onSubmit={handleRunPlayground} className="space-y-3">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div>
              <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
                Emulate Agent (X-MW-Agent)
              </label>
              <select
                value={playAgentId}
                onChange={(ev) => setPlayAgentId(ev.target.value)}
                className="w-full rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              >
                {orderedAgentIds.map((agentId) => (
                  <option key={agentId} value={agentId}>
                    {agentId}
                  </option>
                ))}
              </select>
            </div>

            <div className="sm:col-span-2">
              <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
                Model Override (or &quot;auto&quot; to follow Agent Switchboard)
              </label>
              <input
                type="text"
                list="mw-model-suggestions"
                value={playModel}
                onChange={(ev) => setPlayModel(ev.target.value)}
                placeholder="auto"
                className="w-full rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
            </div>
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
              Prompt Message
            </label>
            <textarea
              rows={3}
              value={playPrompt}
              onChange={(ev) => setPlayPrompt(ev.target.value)}
              className="w-full rounded border border-[#2a2a36] bg-[#0d0d10] px-3 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>

          <div className="flex items-center justify-between gap-3">
            <span className="font-mono text-[11px] text-[#9c988c]">
              Endpoint: POST /v1/chat/completions
            </span>
            <button
              type="submit"
              disabled={sendingChat}
              className="inline-flex items-center gap-2 rounded-md bg-[#ff2e88] px-4 py-2 font-mono text-xs font-extrabold text-white transition-opacity hover:opacity-90 disabled:opacity-50"
            >
              <Play className="h-3.5 w-3.5" />
              {sendingChat ? 'DISPATCHING...' : 'RUN LIVE TEST'}
            </button>
          </div>
        </form>

        {chatResult ? (
          <div className="mt-4 space-y-2.5 rounded-lg border border-[#2a2a36] bg-[#0d0d10] p-3.5">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex flex-wrap items-center gap-1.5">
                <MonoBadge tone="yellow">{chatResult.latencyMs} ms</MonoBadge>
                <MonoBadge tone="magenta">PROVIDER: {chatResult.providerUsed}</MonoBadge>
                <MonoBadge tone="cyan">MODEL: {chatResult.modelUsed}</MonoBadge>
                <MonoBadge
                  tone={chatResult.cacheStatus.toUpperCase() === 'HIT' ? 'ok' : 'neutral'}
                >
                  CACHE: {chatResult.cacheStatus}
                </MonoBadge>
                <MonoBadge tone="ok">RTK SAVED: {chatResult.rtkSavedTokens} TOK</MonoBadge>
                {chatResult.totalTokens > 0 ? (
                  <MonoBadge tone="neutral">
                    TOKENS: {chatResult.promptTokens}p / {chatResult.completionTokens}c (
                    {chatResult.totalTokens})
                  </MonoBadge>
                ) : null}
              </div>
              <button
                type="button"
                onClick={() => setShowRawHeaders((prev) => !prev)}
                className="font-mono text-[11px] text-[#00b8f0] hover:underline"
              >
                {showRawHeaders ? 'HIDE HEADERS' : 'INSPECT HEADERS'}
              </button>
            </div>

            <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded border border-[#2a2a36] bg-[#15151a] p-3 font-mono text-xs leading-relaxed text-[#f3efe4]">
              {chatResult.content || JSON.stringify(chatResult.rawPayload, null, 2)}
            </pre>

            {showRawHeaders ? (
              <pre className="max-h-48 overflow-auto rounded border border-[#2a2a36] bg-[#15151a] p-3 font-mono text-[11px] text-[#9c988c]">
                {JSON.stringify(chatResult.rawHeaders, null, 2)}
              </pre>
            ) : null}
          </div>
        ) : null}
      </SectionCard>

      {/* 5. Recent Gateway Telemetry Logs */}
      {recentLogs.length > 0 ? (
        <SectionCard
          title="Recent AI Gateway Requests"
          subtitle="Last 25 routed calls across OpenAI /v1/chat/completions & Anthropic /v1/messages."
          icon={<Activity className="h-4 w-4" />}
        >
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-left text-xs">
              <thead>
                <tr className="border-b border-[#2a2a36] font-mono text-[10px] uppercase text-[#9c988c]">
                  <th className="px-3 py-2">Time</th>
                  <th className="px-3 py-2">Agent</th>
                  <th className="px-3 py-2">Provider / Model</th>
                  <th className="px-3 py-2">Status</th>
                  <th className="px-3 py-2">Latency</th>
                  <th className="px-3 py-2">Tokens / RTK</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[#2a2a36]/70">
                {recentLogs.map((logEntry) => (
                  <tr key={logEntry.id} className="hover:bg-[#1c1c23]/60">
                    <td className="px-3 py-2 font-mono text-[11px] text-[#9c988c]">
                      {formatShortTime(logEntry.createdAt)}
                    </td>
                    <td className="px-3 py-2">
                      <MonoBadge tone="magenta">{logEntry.agentId}</MonoBadge>
                    </td>
                    <td className="px-3 py-2 font-mono text-[11px] text-[#f3efe4]">
                      {logEntry.resolvedProvider || '—'} ·{' '}
                      <span className="text-[#00b8f0]">
                        {logEntry.resolvedModel || logEntry.requestedModel}
                      </span>
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex gap-1">
                        <MonoBadge tone={logEntry.statusCode >= 200 && logEntry.statusCode < 300 ? 'ok' : 'err'}>
                          {logEntry.statusCode}
                        </MonoBadge>
                        {logEntry.cacheHit ? <MonoBadge tone="cyan">HIT</MonoBadge> : null}
                      </div>
                    </td>
                    <td className="px-3 py-2 font-mono text-[11px] text-[#ffe031]">
                      {logEntry.latencyMs}ms
                    </td>
                    <td className="px-3 py-2 font-mono text-[11px] text-[#9c988c]">
                      {logEntry.promptTokens}p/{logEntry.completionTokens}c
                      {logEntry.rtkTokensSaved > 0 ? (
                        <span className="ml-1.5 text-[#22c55e]">
                          (-{logEntry.rtkTokensSaved} RTK)
                        </span>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </SectionCard>
      ) : null}
    </div>
  );
}
