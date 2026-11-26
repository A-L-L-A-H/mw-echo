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
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  type BadgeTone,
} from './UiPrimitives';

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
    label: 'FREE FIRST (free -> cheap -> subscription -> paid)',
  },
  {
    value: 'subscription,cheap,free',
    label: 'SUB FIRST (subscription -> cheap -> free)',
  },
  {
    value: 'cheap,free,subscription,paid',
    label: 'CHEAP FIRST (cheap -> free -> subscription -> paid)',
  },
  {
    value: 'free,paid',
    label: 'FREE -> PAID DIRECT',
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
  const [rtkEnabled, setRtkEnabled] = useState(true);
  const [cacheEnabled, setCacheEnabled] = useState(true);
  const [fallbackOrder, setFallbackOrder] = useState('free,cheap,subscription,paid');
  const [savingSettings, setSavingSettings] = useState(false);
  const [clearingCache, setClearingCache] = useState(false);

  const [agentDrafts, setAgentDrafts] = useState<Record<string, AgentDraftState>>({});
  const [savingAgentId, setSavingAgentId] = useState<string | null>(null);

  const [provName, setProvName] = useState('');
  const [provKind, setProvKind] = useState('openai_compat');
  const [provTier, setProvTier] = useState('free');
  const [provBaseUrl, setProvBaseUrl] = useState('https://api.groq.com/openai/v1');
  const [provApiKey, setProvApiKey] = useState('');
  const [provDefaultModel, setProvDefaultModel] = useState('llama-3.3-70b-versatile');
  const [provPriority, setProvPriority] = useState('10');
  const [creatingProvider, setCreatingProvider] = useState(false);

  const [playAgentId, setPlayAgentId] = useState<string>('claude-code');
  const [playModel, setPlayModel] = useState<string>('auto');
  const [playPrompt, setPlayPrompt] = useState<string>(
    'Reply in one short sentence confirming which model handled this request.',
  );
  const [sendingChat, setSendingChat] = useState(false);
  const [chatResult, setChatResult] = useState<ChatCompletionTestResult | null>(null);
  const [showRawHeaders, setShowRawHeaders] = useState(false);

  useEffect(() => {
    if (!aiStats?.settings) return;
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
    if (!providerPrefill) return;
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
      onNotify('ok', '9Router Turbo settings locked.');
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
      onNotify('ok', `Purged ${outcome.purged} cached responses.`);
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
    if (!draft) return;
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
      onNotify('ok', `Equipped Magpie pilot "${agentId}".`);
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
      onNotify('ok', `Loaded Bifrost key "${payload.name}".`);
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
      onNotify('ok', `Ejected provider "${provider.name}".`);
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
        `HIT via ${completion.providerUsed} (${completion.latencyMs}ms · cache: ${completion.cacheStatus}).`,
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
    <div className="space-y-6">
      <datalist id="mw-model-suggestions">
        {modelSuggestions.map((modelName) => (
          <option key={modelName} value={modelName} />
        ))}
      </datalist>

      <StageHeroBanner
        sceneCode="SCENE 05"
        sticker="MECH RIG"
        title="AI GATEWAY // FIGHTER LOADOUT"
        subtitle="9ROUTER TURBO TOKEN SAVER + MAGPIE PILOT SWITCHBOARD + BIFROST AMMO POOL + LIVE FIRING RANGE"
        tone="magenta"
        actions={
          <button
            type="button"
            onClick={() => void onReloadGateway()}
            className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c]"
          >
            <RefreshCw className="h-3.5 w-3.5" />
            SYNC RIG
          </button>
        }
      />

      {/* 1. 9Router Turbo Token Saver */}
      <SectionCard
        title="9ROUTER TURBO TOKEN SAVER & EXACT CACHE"
        subtitle="Global RTK tool-result compression, exact SQLite response cache, and cost-tier failover order."
        icon={<Zap className="h-4 w-4" />}
        sticker="TURBO"
        stickerTone="yellow"
        accent="yellow"
        actions={
          <div className="flex flex-wrap items-center gap-1.5">
            <MonoBadge tone="yellow">
              RTK SAVED: {formatCompactNumber(aiStats?.totalRtkTokensSaved ?? 0)} TOK
            </MonoBadge>
            <MonoBadge tone="cyan">
              CACHE HITS: {formatCompactNumber(aiStats?.cacheHits ?? 0)} (
              {formatCompactNumber(aiStats?.cacheTokensSaved ?? 0)} TOK)
            </MonoBadge>
            <MonoBadge tone="magenta">
              SORTIES: {formatCompactNumber(aiStats?.totalRequests ?? 0)} ·{' '}
              {Math.round(aiStats?.avgLatencyMs ?? 0)}ms
            </MonoBadge>
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
            className={`riot-btn inline-flex items-center gap-2 px-4 py-2 font-mono text-xs font-extrabold uppercase ${
              rtkEnabled ? 'bg-[#ffe031] text-[#08080c]' : 'bg-[#1b1b2b] text-[#a6a2b8]'
            }`}
          >
            <Sparkles className="h-4 w-4" />
            RTK TURBO: {rtkEnabled ? 'ARMED' : 'OFF'}
          </button>

          <button
            type="button"
            disabled={savingSettings}
            onClick={() => {
              const nextVal = !cacheEnabled;
              setCacheEnabled(nextVal);
              void persistGlobalSettings({ cacheEnabled: nextVal });
            }}
            className={`riot-btn inline-flex items-center gap-2 px-4 py-2 font-mono text-xs font-extrabold uppercase ${
              cacheEnabled ? 'bg-[#00f0ff] text-[#08080c]' : 'bg-[#1b1b2b] text-[#a6a2b8]'
            }`}
          >
            <Zap className="h-4 w-4" />
            EXACT CACHE: {cacheEnabled ? 'ARMED' : 'OFF'}
          </button>

          <div className="flex min-w-[280px] flex-1 flex-wrap items-center gap-2">
            <label className="font-mono text-[11px] font-extrabold uppercase text-[#ffe031]">
              CASCADE:
            </label>
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
              className="riot-input flex-1 px-2.5 py-2 font-mono text-xs font-bold"
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
            className="riot-btn inline-flex items-center gap-1.5 bg-[#ff334b] px-3.5 py-2 font-mono text-xs font-extrabold text-white uppercase disabled:opacity-50"
          >
            <Flame className="h-3.5 w-3.5" />
            {clearingCache ? 'PURGING...' : 'PURGE CACHE'}
          </button>
        </div>
      </SectionCard>

      {/* 2. Magpie Pilot Switchboard */}
      <SectionCard
        title="MAGPIE PILOT SWITCHBOARD"
        subtitle="Assign target models, fallback tier order, and per-pilot RTK compression for every CLI agent."
        icon={<Bot className="h-4 w-4" />}
        sticker="6 PILOTS"
        stickerTone="magenta"
        accent="magenta"
      >
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
          {orderedAgentIds.map((agentId) => {
            const draft = agentDrafts[agentId];
            if (!draft) return null;
            const isSavingThis = savingAgentId === agentId;
            return (
              <div
                key={agentId}
                className={`border-[2.5px] border-black p-3.5 transition-all ${
                  draft.dirty
                    ? 'bg-[#1b1b2b] shadow-[5px_5px_0_#ffe031]'
                    : 'bg-[#08080c] shadow-[4px_4px_0_#000]'
                }`}
              >
                <div className="mb-3 flex items-center justify-between gap-2 border-b-2 border-black pb-2">
                  <div className="flex items-center gap-2">
                    <span className="flex h-6 w-6 items-center justify-center border-2 border-black bg-[#ff2e88] text-white">
                      <Cpu className="h-3.5 w-3.5" />
                    </span>
                    <span className="font-display text-sm text-[#fff6de] uppercase">{agentId}</span>
                  </div>
                  <div className="flex items-center gap-1.5">
                    {draft.dirty ? <ComicSticker tone="yellow">MODIFIED</ComicSticker> : null}
                    <button
                      type="button"
                      onClick={() =>
                        patchAgentDraft(agentId, { rtkEnabled: !draft.rtkEnabled })
                      }
                      className={`border-2 border-black px-2 py-0.5 font-mono text-[10px] font-extrabold ${
                        draft.rtkEnabled
                          ? 'bg-[#a3ff12] text-[#08080c]'
                          : 'bg-[#1b1b2b] text-[#a6a2b8]'
                      }`}
                    >
                      RTK {draft.rtkEnabled ? 'ON' : 'OFF'}
                    </button>
                  </div>
                </div>

                <div className="space-y-2.5">
                  <div>
                    <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                      Equipped Model (empty = auto)
                    </label>
                    <input
                      type="text"
                      list="mw-model-suggestions"
                      value={draft.targetModel}
                      onChange={(ev) =>
                        patchAgentDraft(agentId, { targetModel: ev.target.value })
                      }
                      placeholder="auto / llama-3.3-70b-versatile..."
                      className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
                    />
                  </div>

                  <div>
                    <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                      Tier Cascade
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
                      className="riot-input w-full px-2.5 py-1.5 font-mono text-xs font-bold"
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
                      className={`riot-btn inline-flex items-center gap-1.5 px-3.5 py-1.5 font-mono text-[11px] font-extrabold uppercase ${
                        draft.dirty
                          ? 'bg-[#ffe031] text-[#08080c]'
                          : 'bg-[#1b1b2b] text-[#fff6de]'
                      }`}
                    >
                      <Save className="h-3 w-3" />
                      {isSavingThis ? 'LOCKING...' : 'EQUIP'}
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
        title="BIFROST PROVIDER AMMO POOL"
        subtitle="Encrypted API keys with live 429 cooldown tracking, request telemetry, and failover priority."
        icon={<KeyRound className="h-4 w-4" />}
        sticker={`${providers.length} ARMED`}
        stickerTone="cyan"
        accent="cyan"
      >
        <form
          onSubmit={handleAddProvider}
          className="mb-4 grid grid-cols-1 gap-2.5 border-2 border-black bg-[#08080c] p-3.5 shadow-[4px_4px_0_#000] md:grid-cols-2 lg:grid-cols-4"
        >
          <div>
            <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
              Provider Name
            </label>
            <input
              type="text"
              value={provName}
              onChange={(ev) => setProvName(ev.target.value)}
              placeholder="groq-free-1"
              className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
            />
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
              Protocol Kind
            </label>
            <select
              value={provKind}
              onChange={(ev) => setProvKind(ev.target.value)}
              className="riot-input w-full px-2.5 py-1.5 font-mono text-xs font-bold"
            >
              <option value="openai_compat">openai_compat</option>
              <option value="anthropic">anthropic</option>
              <option value="gemini">gemini</option>
            </select>
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ff2e88]">
              Cost Tier
            </label>
            <select
              value={provTier}
              onChange={(ev) => setProvTier(ev.target.value)}
              className="riot-input w-full px-2.5 py-1.5 font-mono text-xs font-bold"
            >
              <option value="free">free</option>
              <option value="cheap">cheap</option>
              <option value="subscription">subscription</option>
              <option value="paid">paid</option>
            </select>
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
              Priority
            </label>
            <input
              type="number"
              value={provPriority}
              onChange={(ev) => setProvPriority(ev.target.value)}
              className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
            />
          </div>

          <div className="lg:col-span-2">
            <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
              Base URL
            </label>
            <input
              type="text"
              value={provBaseUrl}
              onChange={(ev) => setProvBaseUrl(ev.target.value)}
              placeholder="https://api.groq.com/openai/v1"
              className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
            />
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
              Default Model
            </label>
            <input
              type="text"
              list="mw-model-suggestions"
              value={provDefaultModel}
              onChange={(ev) => setProvDefaultModel(ev.target.value)}
              placeholder="llama-3.3-70b-versatile"
              className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
            />
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a3ff12]">
              API Key
            </label>
            <div className="flex gap-2">
              <input
                type="password"
                value={provApiKey}
                onChange={(ev) => setProvApiKey(ev.target.value)}
                placeholder="sk-... / gsk_..."
                className="riot-input min-w-0 flex-1 px-2.5 py-1.5 font-mono text-xs"
              />
              <button
                type="submit"
                disabled={creatingProvider}
                className="riot-btn inline-flex shrink-0 items-center gap-1 bg-[#ffe031] px-3 py-1.5 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
              >
                <Plus className="h-3.5 w-3.5" />
                {creatingProvider ? '...' : 'LOAD'}
              </button>
            </div>
          </div>
        </form>

        <div className="overflow-x-auto border-2 border-black bg-[#08080c]">
          <table className="w-full border-collapse text-left text-xs">
            <thead>
              <tr className="border-b-2 border-black bg-[#00f0ff] font-mono text-[10px] font-extrabold uppercase text-[#08080c]">
                <th className="px-3 py-2">Provider</th>
                <th className="px-3 py-2">Kind / Tier</th>
                <th className="px-3 py-2">Endpoint &amp; Model</th>
                <th className="px-3 py-2">Pri</th>
                <th className="px-3 py-2">Live Telemetry</th>
                <th className="px-3 py-2 text-right">Action</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#252538]">
              {providers.length > 0 ? (
                providers.map((provider) => {
                  const coolingDown = provider.inCooldown || isCooldownActive(provider.cooldownUntil);
                  const remaining = formatCooldownRemaining(provider.cooldownUntil);
                  const tierTone = TIER_TONES[provider.tier] ?? 'neutral';

                  return (
                    <tr key={provider.id} className="hover:bg-[#1b1b2b]">
                      <td className="px-3 py-2.5">
                        <div className="font-display text-xs text-[#fff6de] uppercase">
                          {provider.name}
                        </div>
                        <div className="font-mono text-[10px] text-[#a6a2b8]">
                          {provider.hasApiKey ? 'KEY ENCRYPTED' : 'LOCAL / NO KEY'}
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex flex-wrap gap-1">
                          <MonoBadge tone="neutral">{provider.kind}</MonoBadge>
                          <MonoBadge tone={tierTone}>{provider.tier}</MonoBadge>
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="max-w-xs truncate font-mono text-[11px] font-bold text-[#ffe031]">
                          {provider.defaultModel || '—'}
                        </div>
                        <div className="max-w-xs truncate font-mono text-[10px] text-[#a6a2b8]">
                          {provider.baseUrl}
                        </div>
                      </td>
                      <td className="px-3 py-2.5 font-mono font-bold text-[#fff6de]">
                        {provider.priority}
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex flex-wrap items-center gap-1.5">
                          {coolingDown ? (
                            <MonoBadge tone="err">
                              <ShieldAlert className="h-3 w-3" />
                              COOLDOWN {remaining || 'ON'}
                            </MonoBadge>
                          ) : (
                            <MonoBadge tone="ok">HOT</MonoBadge>
                          )}
                          <MonoBadge tone="cyan">REQ {provider.reqCount}</MonoBadge>
                          <MonoBadge tone={provider.errCount > 0 ? 'err' : 'neutral'}>
                            ERR {provider.errCount}
                          </MonoBadge>
                          {provider.tokensIn + provider.tokensOut > 0 ? (
                            <MonoBadge tone="cream">
                              {formatCompactNumber(provider.tokensIn + provider.tokensOut)} TOK
                            </MonoBadge>
                          ) : null}
                        </div>
                      </td>
                      <td className="px-3 py-2.5 text-right">
                        <button
                          type="button"
                          onClick={() => void handleDeleteProvider(provider)}
                          className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2 py-1 font-mono text-[10px] font-extrabold text-white"
                        >
                          <Trash2 className="h-3 w-3" />
                          EJECT
                        </button>
                      </td>
                    </tr>
                  );
                })
              ) : (
                <tr>
                  <td
                    colSpan={6}
                    className="px-3 py-6 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]"
                  >
                    NO PROVIDERS LOADED. ADD ONE ABOVE OR LOOT ONE FROM 06 LOOT!
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </SectionCard>

      {/* 4. Live Combat Test Firing Range */}
      <SectionCard
        title="LIVE FIRING RANGE // /V1/CHAT/COMPLETIONS"
        subtitle="Fire a real probe through the gateway and inspect latency, provider routing, cache & RTK headers."
        icon={<Play className="h-4 w-4" />}
        sticker="LIVE TEST"
        stickerTone="magenta"
      >
        <form onSubmit={handleRunPlayground} className="space-y-3">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div>
              <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ff2e88]">
                Emulate Pilot (X-MW-Agent)
              </label>
              <select
                value={playAgentId}
                onChange={(ev) => setPlayAgentId(ev.target.value)}
                className="riot-input w-full px-2.5 py-2 font-mono text-xs font-bold"
              >
                {orderedAgentIds.map((agentId) => (
                  <option key={agentId} value={agentId}>
                    {agentId}
                  </option>
                ))}
              </select>
            </div>

            <div className="sm:col-span-2">
              <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                Model Override (or &quot;auto&quot;)
              </label>
              <input
                type="text"
                list="mw-model-suggestions"
                value={playModel}
                onChange={(ev) => setPlayModel(ev.target.value)}
                placeholder="auto"
                className="riot-input w-full px-2.5 py-2 font-mono text-xs"
              />
            </div>
          </div>

          <div>
            <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
              Test Prompt
            </label>
            <textarea
              rows={3}
              value={playPrompt}
              onChange={(ev) => setPlayPrompt(ev.target.value)}
              className="riot-input w-full px-3 py-2 font-mono text-xs"
            />
          </div>

          <div className="flex items-center justify-between gap-3">
            <span className="font-mono text-[11px] font-bold text-[#a6a2b8]">
              TARGET: POST /v1/chat/completions
            </span>
            <button
              type="submit"
              disabled={sendingChat}
              className="riot-btn inline-flex items-center gap-2 bg-[#ff2e88] px-5 py-2 font-display text-xs text-white uppercase disabled:opacity-50"
            >
              <Play className="h-3.5 w-3.5" />
              {sendingChat ? 'FIRING...' : 'FIRE PROBE'}
            </button>
          </div>
        </form>

        {chatResult ? (
          <div className="mt-4 space-y-2.5 border-2 border-black bg-[#08080c] p-4 shadow-[5px_5px_0_#a3ff12]">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex flex-wrap items-center gap-1.5">
                <MonoBadge tone="yellow">{chatResult.latencyMs} MS</MonoBadge>
                <MonoBadge tone="magenta">PROV: {chatResult.providerUsed}</MonoBadge>
                <MonoBadge tone="cyan">MODEL: {chatResult.modelUsed}</MonoBadge>
                <MonoBadge
                  tone={chatResult.cacheStatus.toUpperCase() === 'HIT' ? 'ok' : 'neutral'}
                >
                  CACHE: {chatResult.cacheStatus}
                </MonoBadge>
                <MonoBadge tone="ok">RTK SAVED: {chatResult.rtkSavedTokens} TOK</MonoBadge>
              </div>
              <button
                type="button"
                onClick={() => setShowRawHeaders((prev) => !prev)}
                className="font-mono text-[11px] font-extrabold text-[#00f0ff] underline"
              >
                {showRawHeaders ? 'HIDE HEADERS' : 'RAW HEADERS'}
              </button>
            </div>

            <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words border-2 border-black bg-[#13131d] p-3 font-mono text-xs leading-relaxed text-[#fff6de]">
              {chatResult.content || JSON.stringify(chatResult.rawPayload, null, 2)}
            </pre>

            {showRawHeaders ? (
              <pre className="max-h-48 overflow-auto border-2 border-black bg-[#13131d] p-3 font-mono text-[11px] text-[#a6a2b8]">
                {JSON.stringify(chatResult.rawHeaders, null, 2)}
              </pre>
            ) : null}
          </div>
        ) : null}
      </SectionCard>

      {/* 5. Recent Sorties Log */}
      {recentLogs.length > 0 ? (
        <SectionCard
          title="RECENT RIG SORTIES"
          subtitle="Last 25 gateway calls across OpenAI & Anthropic protocols."
          icon={<Activity className="h-4 w-4" />}
        >
          <div className="overflow-x-auto border-2 border-black bg-[#08080c]">
            <table className="w-full border-collapse text-left text-xs">
              <thead>
                <tr className="border-b-2 border-black bg-[#ffe031] font-mono text-[10px] font-extrabold uppercase text-[#08080c]">
                  <th className="px-3 py-2">Time</th>
                  <th className="px-3 py-2">Pilot</th>
                  <th className="px-3 py-2">Provider / Model</th>
                  <th className="px-3 py-2">Status</th>
                  <th className="px-3 py-2">Latency</th>
                  <th className="px-3 py-2">Tokens / RTK</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[#252538]">
                {recentLogs.map((logEntry) => (
                  <tr key={logEntry.id} className="hover:bg-[#1b1b2b]">
                    <td className="px-3 py-2 font-mono text-[11px] text-[#a6a2b8]">
                      {formatShortTime(logEntry.createdAt)}
                    </td>
                    <td className="px-3 py-2">
                      <MonoBadge tone="magenta">{logEntry.agentId}</MonoBadge>
                    </td>
                    <td className="px-3 py-2 font-mono text-[11px] font-bold text-[#fff6de]">
                      {logEntry.resolvedProvider || '—'} ·{' '}
                      <span className="text-[#00f0ff]">
                        {logEntry.resolvedModel || logEntry.requestedModel}
                      </span>
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex gap-1">
                        <MonoBadge
                          tone={
                            logEntry.statusCode >= 200 && logEntry.statusCode < 300 ? 'ok' : 'err'
                          }
                        >
                          {logEntry.statusCode}
                        </MonoBadge>
                        {logEntry.cacheHit ? <MonoBadge tone="cyan">HIT</MonoBadge> : null}
                      </div>
                    </td>
                    <td className="px-3 py-2 font-mono text-[11px] font-extrabold text-[#ffe031]">
                      {logEntry.latencyMs}ms
                    </td>
                    <td className="px-3 py-2 font-mono text-[11px] text-[#a6a2b8]">
                      {logEntry.promptTokens}p/{logEntry.completionTokens}c
                      {logEntry.rtkTokensSaved > 0 ? (
                        <span className="ml-1.5 font-extrabold text-[#a3ff12]">
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
