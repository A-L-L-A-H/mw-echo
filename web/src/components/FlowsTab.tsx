import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  Clock,
  Play,
  Plus,
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Trash2,
  Workflow,
  Zap,
} from 'lucide-react';
import {
  createFlow,
  deleteFlow,
  fetchFlowRuns,
  fetchFlows,
  patchFlow,
  runFlow,
  type FlowActionTier,
  type FlowRecord,
  type FlowRunRecord,
  type FlowStep,
  type FlowStepKind,
  type FlowTriggerKind,
} from '../api';
import { formatShortTime } from '../utils';
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  StatTile,
  type BadgeTone,
} from './UiPrimitives';

interface FlowsTabProps {
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
  onHubMutated?: () => Promise<void>;
}

const TRIGGER_KIND_OPTIONS: Array<{ value: FlowTriggerKind; label: string; defaultConfig: string }> = [
  { value: 'manual', label: 'manual (On-demand / MCP)', defaultConfig: '{}' },
  { value: 'interval', label: 'interval (Scheduled Loop)', defaultConfig: '{"intervalSec": 300}' },
  { value: 'radar_event', label: 'radar_event (World Radar Hook)', defaultConfig: '{"domain": "D01"}' },
  { value: 'whatsapp_msg', label: 'whatsapp_msg (Incoming Comms)', defaultConfig: '{"keyword": "brief"}' },
];

const ACTION_TIER_OPTIONS: Array<{ value: FlowActionTier; label: string; hint: string }> = [
  { value: 'A0', label: 'A0 · READ-ONLY', hint: 'Read-only telemetry & radar queries' },
  { value: 'A1', label: 'A1 · LOCAL SAFE', hint: 'Local DB tasks & AI briefs' },
  { value: 'A2', label: 'A2 · EXTERNAL SYNC', hint: 'Webhooks, WhatsApp & 9Drive uploads' },
  { value: 'A3', label: 'A3 · HIGH-RISK GATED', hint: 'Requires explicit Approved switch before execution' },
];

const STEP_KIND_OPTIONS: Array<{ value: FlowStepKind; label: string; defaultConfig: Record<string, string> }> = [
  {
    value: 'radar_run',
    label: 'radar_run (Run World Radar Pack)',
    defaultConfig: { instanceId: '' },
  },
  {
    value: 'ai_prompt',
    label: 'ai_prompt (Sidekick / Gateway LLM)',
    defaultConfig: { agentProfile: 'default', prompt: 'Summarize latest signal: {{prev}}' },
  },
  {
    value: 'create_task',
    label: 'create_task (Forge TaskOS Bounty)',
    defaultConfig: { project: 'moonwitness', code: '', title: 'Follow-up: {{prev}}', priority: 'p2' },
  },
  {
    value: 'send_whatsapp',
    label: 'send_whatsapp (Dispatch GOWA Msg)',
    defaultConfig: { deviceId: 'primary', phone: '', message: '{{prev}}' },
  },
  {
    value: 'send_webhook',
    label: 'send_webhook (POST JSON Callback)',
    defaultConfig: { url: 'https://example.com/hook', method: 'POST', body: '{"output":"{{prev}}"}' },
  },
  {
    value: 'backup_9drive',
    label: 'backup_9drive (Snapshot SQLite -> 9Drive)',
    defaultConfig: {},
  },
];

function resolveTierTone(tier: string): BadgeTone {
  switch ((tier || '').toUpperCase()) {
    case 'A0':
      return 'cyan';
    case 'A1':
      return 'ok';
    case 'A2':
      return 'yellow';
    case 'A3':
      return 'err';
    default:
      return 'neutral';
  }
}

function resolveRunStatusTone(status: string): BadgeTone {
  const normalized = (status || '').toLowerCase();
  if (normalized === 'ok' || normalized === 'success') return 'ok';
  if (normalized === 'blocked') return 'yellow';
  if (normalized === 'error' || normalized === 'failed') return 'err';
  return 'neutral';
}

function formatPrettyJson(rawJson: string): string {
  if (!rawJson || !rawJson.trim()) {
    return '—';
  }
  try {
    const parsed: unknown = JSON.parse(rawJson);
    return JSON.stringify(parsed, null, 2);
  } catch {
    return rawJson;
  }
}

function summarizeStepConfig(step: FlowStep): string {
  const entries = Object.entries(step.config ?? {}).filter(([, val]) => val.trim().length > 0);
  if (entries.length === 0) {
    return 'no params';
  }
  return entries
    .slice(0, 2)
    .map(([cfgKey, cfgVal]) => `${cfgKey}=${cfgVal.length > 26 ? `${cfgVal.slice(0, 26)}...` : cfgVal}`)
    .join(' · ');
}

function createDraftStep(index: number, kind: FlowStepKind = 'ai_prompt'): FlowStep {
  const template = STEP_KIND_OPTIONS.find((opt) => opt.value === kind) ?? STEP_KIND_OPTIONS[1];
  return {
    id: `step-${index + 1}`,
    kind: template.value,
    config: { ...template.defaultConfig },
  };
}

export function FlowsTab({ onNotify, onHubMutated }: FlowsTabProps) {
  const [flows, setFlows] = useState<FlowRecord[]>([]);
  const [loadingFlows, setLoadingFlows] = useState(false);
  const [filterMode, setFilterMode] = useState<'all' | 'enabled' | 'a3'>('all');
  const [searchQuery, setSearchQuery] = useState('');

  const [showForgeBuilder, setShowForgeBuilder] = useState(false);
  const [draftName, setDraftName] = useState('');
  const [draftDescription, setDraftDescription] = useState('');
  const [draftTriggerKind, setDraftTriggerKind] = useState<FlowTriggerKind>('manual');
  const [draftTriggerConfig, setDraftTriggerConfig] = useState('{}');
  const [draftActionTier, setDraftActionTier] = useState<FlowActionTier>('A1');
  const [draftSteps, setDraftSteps] = useState<FlowStep[]>([createDraftStep(0, 'ai_prompt')]);
  const [creatingFlow, setCreatingFlow] = useState(false);

  const [busyFlowId, setBusyFlowId] = useState<string | null>(null);
  const [runningFlowId, setRunningFlowId] = useState<string | null>(null);
  const [triggerPayloads, setTriggerPayloads] = useState<Record<string, string>>({});
  const [expandedRunFlowIds, setExpandedRunFlowIds] = useState<Record<string, boolean>>({});
  const [runsByFlowId, setRunsByFlowId] = useState<Record<string, FlowRunRecord[]>>({});
  const [loadingRunsFlowId, setLoadingRunsFlowId] = useState<string | null>(null);

  const loadFlows = useCallback(async () => {
    setLoadingFlows(true);
    try {
      const reply = await fetchFlows();
      setFlows(reply.flows ?? []);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to load flows';
      onNotify('err', message);
    } finally {
      setLoadingFlows(false);
    }
  }, [onNotify]);

  useEffect(() => {
    void loadFlows();
  }, [loadFlows]);

  const loadRunsForFlow = useCallback(
    async (flowId: string) => {
      setLoadingRunsFlowId(flowId);
      try {
        const reply = await fetchFlowRuns(flowId);
        setRunsByFlowId((prev) => ({ ...prev, [flowId]: reply.runs ?? [] }));
      } catch (caughtError) {
        const message =
          caughtError instanceof Error ? caughtError.message : 'Failed to load flow run logs';
        onNotify('err', message);
      } finally {
        setLoadingRunsFlowId(null);
      }
    },
    [onNotify],
  );

  const handleToggleRunsInspector = (flowId: string) => {
    const nextOpen = !expandedRunFlowIds[flowId];
    setExpandedRunFlowIds((prev) => ({ ...prev, [flowId]: nextOpen }));
    if (nextOpen) {
      void loadRunsForFlow(flowId);
    }
  };

  const handleToggleEnabled = async (flow: FlowRecord) => {
    setBusyFlowId(flow.id);
    try {
      const updated = await patchFlow(flow.id, { enabled: !flow.enabled });
      setFlows((prev) => prev.map((existing) => (existing.id === flow.id ? updated : existing)));
      onNotify('ok', `${updated.name}: ${updated.enabled ? 'ARMED' : 'DISARMED'}.`);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to toggle flow';
      onNotify('err', message);
    } finally {
      setBusyFlowId(null);
    }
  };

  const handleToggleApproved = async (flow: FlowRecord) => {
    setBusyFlowId(flow.id);
    try {
      const updated = await patchFlow(flow.id, { approved: !flow.approved });
      setFlows((prev) => prev.map((existing) => (existing.id === flow.id ? updated : existing)));
      onNotify(
        'ok',
        `${updated.name}: ${updated.approved ? 'A3 CLEARANCE GRANTED' : 'APPROVAL REVOKED'}.`,
      );
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to update flow approval';
      onNotify('err', message);
    } finally {
      setBusyFlowId(null);
    }
  };

  const handleDeleteFlow = async (flow: FlowRecord) => {
    setBusyFlowId(flow.id);
    try {
      await deleteFlow(flow.id);
      setFlows((prev) => prev.filter((existing) => existing.id !== flow.id));
      onNotify('ok', `Scrapped flow "${flow.name}".`);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to delete flow';
      onNotify('err', message);
    } finally {
      setBusyFlowId(null);
    }
  };

  const handleRunFlow = async (flow: FlowRecord) => {
    setRunningFlowId(flow.id);
    const payloadText = (triggerPayloads[flow.id] ?? '').trim();
    try {
      const runRecord = await runFlow(flow.id, payloadText);
      const statusNormalized = (runRecord.status || '').toLowerCase();
      if (statusNormalized === 'blocked') {
        onNotify('err', `Flow "${flow.name}" blocked: A3 flow requires explicit approval.`);
      } else if (statusNormalized === 'error') {
        onNotify('err', `Flow "${flow.name}" failed during execution. Inspect run logs.`);
      } else {
        onNotify('ok', `Flow "${flow.name}" executed (${runRecord.status.toUpperCase()}).`);
      }
      setExpandedRunFlowIds((prev) => ({ ...prev, [flow.id]: true }));
      await Promise.all([loadFlows(), loadRunsForFlow(flow.id)]);
      if (onHubMutated) {
        await onHubMutated();
      }
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Flow execution failed';
      onNotify('err', message);
      setExpandedRunFlowIds((prev) => ({ ...prev, [flow.id]: true }));
      await loadRunsForFlow(flow.id);
    } finally {
      setRunningFlowId(null);
    }
  };

  const handleTriggerKindChange = (nextKind: FlowTriggerKind) => {
    setDraftTriggerKind(nextKind);
    const meta = TRIGGER_KIND_OPTIONS.find((opt) => opt.value === nextKind);
    if (meta) {
      setDraftTriggerConfig(meta.defaultConfig);
    }
  };

  const handleStepKindChange = (stepIndex: number, nextKind: FlowStepKind) => {
    const template = STEP_KIND_OPTIONS.find((opt) => opt.value === nextKind);
    setDraftSteps((prev) =>
      prev.map((stepObj, idx) =>
        idx === stepIndex
          ? {
              ...stepObj,
              kind: nextKind,
              config: template ? { ...template.defaultConfig } : {},
            }
          : stepObj,
      ),
    );
  };

  const handleStepFieldChange = (stepIndex: number, fieldKey: string, fieldValue: string) => {
    setDraftSteps((prev) =>
      prev.map((stepObj, idx) =>
        idx === stepIndex
          ? {
              ...stepObj,
              config: {
                ...stepObj.config,
                [fieldKey]: fieldValue,
              },
            }
          : stepObj,
      ),
    );
  };

  const handleStepIdChange = (stepIndex: number, nextStepId: string) => {
    setDraftSteps((prev) =>
      prev.map((stepObj, idx) => (idx === stepIndex ? { ...stepObj, id: nextStepId } : stepObj)),
    );
  };

  const handleMoveStep = (stepIndex: number, direction: -1 | 1) => {
    const targetIndex = stepIndex + direction;
    if (targetIndex < 0 || targetIndex >= draftSteps.length) return;
    setDraftSteps((prev) => {
      const copy = [...prev];
      const moved = copy[stepIndex];
      copy[stepIndex] = copy[targetIndex];
      copy[targetIndex] = moved;
      return copy;
    });
  };

  const handleRemoveStep = (stepIndex: number) => {
    if (draftSteps.length <= 1) return;
    setDraftSteps((prev) => prev.filter((_, idx) => idx !== stepIndex));
  };

  const handleForgeFlowSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!draftName.trim()) {
      onNotify('err', 'Flow name is required.');
      return;
    }
    if (draftSteps.length === 0) {
      onNotify('err', 'Add at least one step to the flow pipeline.');
      return;
    }
    setCreatingFlow(true);
    try {
      const cleanedSteps: FlowStep[] = draftSteps.map((stepObj, idx) => ({
        id: stepObj.id.trim() || `step-${idx + 1}`,
        kind: stepObj.kind,
        config: stepObj.config,
      }));
      const created = await createFlow({
        name: draftName.trim(),
        description: draftDescription.trim(),
        triggerKind: draftTriggerKind,
        triggerConfigJson: draftTriggerConfig.trim() || '{}',
        steps: cleanedSteps,
        actionTier: draftActionTier,
      });
      setFlows((prev) => [created, ...prev]);
      setDraftName('');
      setDraftDescription('');
      setDraftTriggerKind('manual');
      setDraftTriggerConfig('{}');
      setDraftActionTier('A1');
      setDraftSteps([createDraftStep(0, 'ai_prompt')]);
      setShowForgeBuilder(false);
      onNotify('ok', `Forged flow "${created.name}" (${created.actionTier}).`);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to create flow';
      onNotify('err', message);
    } finally {
      setCreatingFlow(false);
    }
  };

  const enabledCount = useMemo(() => flows.filter((flow) => flow.enabled).length, [flows]);
  const a3FlowsCount = useMemo(
    () => flows.filter((flow) => flow.actionTier.toUpperCase() === 'A3').length,
    [flows],
  );
  const unapprovedA3Count = useMemo(
    () => flows.filter((flow) => flow.actionTier.toUpperCase() === 'A3' && !flow.approved).length,
    [flows],
  );
  const executedFlowsCount = useMemo(
    () => flows.filter((flow) => Boolean(flow.lastRunAt)).length,
    [flows],
  );

  const visibleFlows = useMemo(() => {
    const normalizedQuery = searchQuery.trim().toLowerCase();
    return flows.filter((flow) => {
      if (filterMode === 'enabled' && !flow.enabled) return false;
      if (filterMode === 'a3' && flow.actionTier.toUpperCase() !== 'A3') return false;
      if (!normalizedQuery) return true;
      const haystack = `${flow.name} ${flow.description} ${flow.triggerKind} ${flow.actionTier} ${flow.steps
        .map((stepObj) => stepObj.kind)
        .join(' ')}`.toLowerCase();
      return haystack.includes(normalizedQuery);
    });
  }, [filterMode, flows, searchQuery]);

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 13"
        sticker="AUTOMATION ENGINE"
        title="FLOWS // TRIGGER & ACTION PIPELINES"
        subtitle="CHAIN RADAR SCANS, AI PROMPTS ({{prev}} / {{trigger}}), TASKS, WHATSAPP, WEBHOOKS & 9DRIVE BACKUPS WITH A0-A3 GOVERNANCE"
        tone="magenta"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => setShowForgeBuilder((prev) => !prev)}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ff2e88] px-4 py-2 font-display text-xs text-white uppercase hover:bg-[#ffe031] hover:text-[#08080c]"
            >
              <Plus className="h-4 w-4" />
              {showForgeBuilder ? 'CLOSE BUILDER' : '+ FORGE FLOW'}
            </button>
            <button
              type="button"
              onClick={() => void loadFlows()}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingFlows ? 'animate-spin' : ''}`} />
              REFRESH
            </button>
          </div>
        }
      />

      {/* Flows Telemetry Tiles */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="TOTAL FLOWS"
          value={flows.length}
          sublabel="Seeded + user-forged pipelines"
          icon={<Workflow className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="ARMED FLOWS"
          value={enabledCount}
          sublabel="Active for triggers & intervals"
          icon={<Zap className="h-4 w-4" />}
          tone="ok"
        />
        <StatTile
          label="A3 GOVERNANCE"
          value={a3FlowsCount}
          sublabel={
            unapprovedA3Count > 0
              ? `${unapprovedA3Count} awaiting explicit approval`
              : 'All A3 flows cleared'
          }
          icon={<ShieldAlert className="h-4 w-4" />}
          tone={unapprovedA3Count > 0 ? 'err' : 'yellow'}
        />
        <StatTile
          label="PIPELINES RUN"
          value={executedFlowsCount}
          sublabel="Flows with recorded run history"
          icon={<Clock className="h-4 w-4" />}
          tone="magenta"
        />
      </div>

      {/* Collapsible "+ Forge Flow" Builder */}
      {showForgeBuilder ? (
        <SectionCard
          title="FORGE NEW AUTOMATION FLOW"
          subtitle="Compose sequential multi-step pipelines with {{prev}} and {{trigger}} interpolation."
          icon={<Sparkles className="h-4 w-4" />}
          sticker="STEP BUILDER"
          stickerTone="magenta"
          accent="magenta"
        >
          <form onSubmit={(event) => void handleForgeFlowSubmit(event)} className="space-y-4">
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-4">
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                  Flow Name *
                </label>
                <input
                  type="text"
                  value={draftName}
                  onChange={(ev) => setDraftName(ev.target.value)}
                  placeholder="e.g. Radar Threat -> WhatsApp Alert"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                  required
                />
              </div>

              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                  Trigger Kind
                </label>
                <select
                  value={draftTriggerKind}
                  onChange={(ev) => handleTriggerKindChange(ev.target.value)}
                  className="riot-input w-full px-3 py-2 font-mono text-xs font-bold"
                >
                  {TRIGGER_KIND_OPTIONS.map((opt) => (
                    <option key={opt.value} value={opt.value}>
                      {opt.label}
                    </option>
                  ))}
                </select>
              </div>

              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ff2e88]">
                  Action Tier (Governance)
                </label>
                <select
                  value={draftActionTier}
                  onChange={(ev) => setDraftActionTier(ev.target.value)}
                  className="riot-input w-full px-3 py-2 font-mono text-xs font-bold"
                >
                  {ACTION_TIER_OPTIONS.map((opt) => (
                    <option key={opt.value} value={opt.value}>
                      {opt.label}
                    </option>
                  ))}
                </select>
              </div>

              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                  Trigger Config JSON
                </label>
                <input
                  type="text"
                  value={draftTriggerConfig}
                  onChange={(ev) => setDraftTriggerConfig(ev.target.value)}
                  placeholder='{"intervalSec": 300}'
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
            </div>

            <div>
              <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                Description
              </label>
              <input
                type="text"
                value={draftDescription}
                onChange={(ev) => setDraftDescription(ev.target.value)}
                placeholder="Describe what triggers this pipeline and what outcome it produces..."
                className="riot-input w-full px-3 py-2 font-mono text-xs"
              />
            </div>

            {draftActionTier === 'A3' ? (
              <div className="flex items-center gap-2 border-2 border-black bg-[#ff334b]/15 px-3 py-2 font-mono text-xs font-bold text-[#ff334b]">
                <AlertTriangle className="h-4 w-4 shrink-0" />
                A3 flows start unapproved and will block execution until you flip the APPROVE switch.
              </div>
            ) : null}

            {/* Sequential Step Builder */}
            <div className="space-y-3 border-2 border-black bg-[#08080c] p-3.5">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <MonoBadge tone="yellow">PIPELINE STEPS ({draftSteps.length})</MonoBadge>
                  <span className="font-mono text-[11px] text-[#a6a2b8]">
                    Use <code className="text-[#00f0ff]">{'{{prev}}'}</code> for previous step output &amp;{' '}
                    <code className="text-[#ffe031]">{'{{trigger}}'}</code> for trigger payload
                  </span>
                </div>
                <button
                  type="button"
                  onClick={() => setDraftSteps((prev) => [...prev, createDraftStep(prev.length, 'ai_prompt')])}
                  className="riot-btn inline-flex items-center gap-1 bg-[#00f0ff] px-3 py-1.5 font-mono text-[11px] font-extrabold text-[#08080c]"
                >
                  <Plus className="h-3.5 w-3.5" />
                  ADD STEP
                </button>
              </div>

              <div className="space-y-3">
                {draftSteps.map((stepObj, stepIdx) => (
                  <div
                    key={`${stepIdx}-${stepObj.id}`}
                    className="border-2 border-[#2f2f46] bg-[#13131d] p-3"
                  >
                    <div className="mb-2.5 flex flex-wrap items-center justify-between gap-2">
                      <div className="flex flex-wrap items-center gap-2">
                        <MonoBadge tone="magenta">STEP #{stepIdx + 1}</MonoBadge>
                        <input
                          type="text"
                          value={stepObj.id}
                          onChange={(ev) => handleStepIdChange(stepIdx, ev.target.value)}
                          placeholder={`step-${stepIdx + 1}`}
                          className="riot-input w-28 px-2 py-1 font-mono text-[11px]"
                        />
                        <select
                          value={stepObj.kind}
                          onChange={(ev) => handleStepKindChange(stepIdx, ev.target.value)}
                          className="riot-input px-2.5 py-1 font-mono text-xs font-bold text-[#ffe031]"
                        >
                          {STEP_KIND_OPTIONS.map((opt) => (
                            <option key={opt.value} value={opt.value}>
                              {opt.label}
                            </option>
                          ))}
                        </select>
                      </div>

                      <div className="flex items-center gap-1">
                        <button
                          type="button"
                          disabled={stepIdx === 0}
                          onClick={() => handleMoveStep(stepIdx, -1)}
                          className="riot-btn bg-[#1b1b2b] p-1 text-[#fff6de] disabled:opacity-40"
                          title="Move up"
                        >
                          <ArrowUp className="h-3.5 w-3.5" />
                        </button>
                        <button
                          type="button"
                          disabled={stepIdx === draftSteps.length - 1}
                          onClick={() => handleMoveStep(stepIdx, 1)}
                          className="riot-btn bg-[#1b1b2b] p-1 text-[#fff6de] disabled:opacity-40"
                          title="Move down"
                        >
                          <ArrowDown className="h-3.5 w-3.5" />
                        </button>
                        <button
                          type="button"
                          disabled={draftSteps.length <= 1}
                          onClick={() => handleRemoveStep(stepIdx)}
                          className="riot-btn bg-[#ff334b] p-1 text-white disabled:opacity-40"
                          title="Remove step"
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </button>
                      </div>
                    </div>

                    {stepObj.kind === 'radar_run' ? (
                      <div>
                        <label className="mb-1 block font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                          Radar Instance ID
                        </label>
                        <input
                          type="text"
                          value={stepObj.config.instanceId ?? ''}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'instanceId', ev.target.value)}
                          placeholder="instance-id (or leave blank for default pack)"
                          className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
                        />
                      </div>
                    ) : null}

                    {stepObj.kind === 'ai_prompt' ? (
                      <div className="grid grid-cols-1 gap-2 md:grid-cols-4">
                        <div>
                          <label className="mb-1 block font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                            Agent Profile
                          </label>
                          <input
                            type="text"
                            value={stepObj.config.agentProfile ?? 'default'}
                            onChange={(ev) =>
                              handleStepFieldChange(stepIdx, 'agentProfile', ev.target.value)
                            }
                            placeholder="default"
                            className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
                          />
                        </div>
                        <div className="md:col-span-3">
                          <label className="mb-1 block font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                            Prompt Template
                          </label>
                          <input
                            type="text"
                            value={stepObj.config.prompt ?? ''}
                            onChange={(ev) => handleStepFieldChange(stepIdx, 'prompt', ev.target.value)}
                            placeholder="Analyze {{trigger}} and summarize {{prev}}"
                            className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
                          />
                        </div>
                      </div>
                    ) : null}

                    {stepObj.kind === 'create_task' ? (
                      <div className="grid grid-cols-1 gap-2 md:grid-cols-4">
                        <input
                          type="text"
                          value={stepObj.config.project ?? 'moonwitness'}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'project', ev.target.value)}
                          placeholder="project"
                          className="riot-input px-2.5 py-1.5 font-mono text-xs"
                        />
                        <input
                          type="text"
                          value={stepObj.config.code ?? ''}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'code', ev.target.value)}
                          placeholder="code (optional)"
                          className="riot-input px-2.5 py-1.5 font-mono text-xs"
                        />
                        <input
                          type="text"
                          value={stepObj.config.title ?? ''}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'title', ev.target.value)}
                          placeholder="Task title (supports {{prev}})"
                          className="riot-input px-2.5 py-1.5 font-mono text-xs"
                        />
                        <select
                          value={stepObj.config.priority ?? 'p2'}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'priority', ev.target.value)}
                          className="riot-input px-2.5 py-1.5 font-mono text-xs font-bold"
                        >
                          <option value="p0">p0 (Critical)</option>
                          <option value="p1">p1 (High)</option>
                          <option value="p2">p2 (Medium)</option>
                          <option value="p3">p3 (Low)</option>
                        </select>
                      </div>
                    ) : null}

                    {stepObj.kind === 'send_whatsapp' ? (
                      <div className="grid grid-cols-1 gap-2 md:grid-cols-4">
                        <input
                          type="text"
                          value={stepObj.config.deviceId ?? 'primary'}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'deviceId', ev.target.value)}
                          placeholder="deviceId"
                          className="riot-input px-2.5 py-1.5 font-mono text-xs"
                        />
                        <input
                          type="text"
                          value={stepObj.config.phone ?? ''}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'phone', ev.target.value)}
                          placeholder="phone (e.g. 62812...)"
                          className="riot-input px-2.5 py-1.5 font-mono text-xs"
                        />
                        <input
                          type="text"
                          value={stepObj.config.message ?? '{{prev}}'}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'message', ev.target.value)}
                          placeholder="Message ({{prev}})"
                          className="riot-input md:col-span-2 px-2.5 py-1.5 font-mono text-xs"
                        />
                      </div>
                    ) : null}

                    {stepObj.kind === 'send_webhook' ? (
                      <div className="grid grid-cols-1 gap-2 md:grid-cols-4">
                        <input
                          type="url"
                          value={stepObj.config.url ?? ''}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'url', ev.target.value)}
                          placeholder="https://hooks.example.com/..."
                          className="riot-input md:col-span-2 px-2.5 py-1.5 font-mono text-xs"
                        />
                        <select
                          value={stepObj.config.method ?? 'POST'}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'method', ev.target.value)}
                          className="riot-input px-2.5 py-1.5 font-mono text-xs font-bold"
                        >
                          <option value="POST">POST</option>
                          <option value="PUT">PUT</option>
                        </select>
                        <input
                          type="text"
                          value={stepObj.config.body ?? ''}
                          onChange={(ev) => handleStepFieldChange(stepIdx, 'body', ev.target.value)}
                          placeholder='{"text":"{{prev}}"}'
                          className="riot-input px-2.5 py-1.5 font-mono text-xs"
                        />
                      </div>
                    ) : null}

                    {stepObj.kind === 'backup_9drive' ? (
                      <div className="font-mono text-xs text-[#a3ff12]">
                        Snapshots <code>mw-echo.db</code> and <code>whatsapp_sessions.db</code> to the
                        configured 9Drive remote pool.
                      </div>
                    ) : null}
                  </div>
                ))}
              </div>
            </div>

            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setShowForgeBuilder(false)}
                className="riot-btn bg-[#1b1b2b] px-4 py-2 font-mono text-xs font-extrabold text-[#fff6de]"
              >
                CANCEL
              </button>
              <button
                type="submit"
                disabled={creatingFlow}
                className="riot-btn inline-flex items-center gap-2 bg-[#a3ff12] px-5 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
              >
                <Zap className="h-4 w-4" />
                {creatingFlow ? 'FORGING...' : 'FORGE & SAVE FLOW'}
              </button>
            </div>
          </form>
        </SectionCard>
      ) : null}

      {/* Registered Flows Deck */}
      <SectionCard
        title="AUTOMATION WORKFLOW DECK"
        subtitle="Arm triggers, grant A3 clearance, fire 1-click manual runs, and inspect step-by-step execution logs."
        icon={<Workflow className="h-4 w-4" />}
        sticker="GOVERNED"
        stickerTone="yellow"
        accent="yellow"
        actions={
          <div className="flex flex-wrap items-center gap-1.5">
            {(
              [
                { key: 'all', label: `ALL (${flows.length})` },
                { key: 'enabled', label: `ARMED (${enabledCount})` },
                { key: 'a3', label: `A3 GATED (${a3FlowsCount})` },
              ] as const
            ).map((filterOpt) => (
              <button
                key={filterOpt.key}
                type="button"
                onClick={() => setFilterMode(filterOpt.key)}
                className={`border-2 border-black px-2.5 py-1 font-mono text-[10px] font-extrabold uppercase ${
                  filterMode === filterOpt.key
                    ? 'bg-[#ffe031] text-[#08080c] shadow-[2px_2px_0_#000]'
                    : 'bg-[#08080c] text-[#a6a2b8] hover:text-[#fff6de]'
                }`}
              >
                {filterOpt.label}
              </button>
            ))}
          </div>
        }
      >
        <div className="mb-4">
          <input
            type="search"
            value={searchQuery}
            onChange={(ev) => setSearchQuery(ev.target.value)}
            placeholder="Filter workflows by name, trigger kind, action tier, or step kind..."
            className="riot-input w-full px-3 py-2 font-mono text-xs"
          />
        </div>

        {visibleFlows.length === 0 ? (
          <div className="border-2 border-black bg-[#08080c] px-4 py-8 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
            {loadingFlows
              ? 'LOADING AUTOMATION FLOWS...'
              : 'NO FLOWS MATCH CURRENT FILTER. CLICK "+ FORGE FLOW" TO CREATE ONE.'}
          </div>
        ) : (
          <div className="space-y-4">
            {visibleFlows.map((flow) => {
              const isA3 = flow.actionTier.toUpperCase() === 'A3';
              const isBusy = busyFlowId === flow.id;
              const isRunning = runningFlowId === flow.id;
              const runsOpen = Boolean(expandedRunFlowIds[flow.id]);
              const flowRuns = runsByFlowId[flow.id] ?? [];
              const loadingRuns = loadingRunsFlowId === flow.id;

              return (
                <article
                  key={flow.id}
                  className="border-[2.5px] border-black bg-[#08080c] p-4 shadow-[5px_5px_0_#000]"
                >
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <h3 className="font-display text-base text-[#fff6de] uppercase">
                          {flow.name}
                        </h3>
                        <MonoBadge tone={resolveTierTone(flow.actionTier)}>
                          {flow.actionTier}
                        </MonoBadge>
                        <MonoBadge tone="cyan">{flow.triggerKind}</MonoBadge>
                        {flow.enabled ? (
                          <ComicSticker tone="ok" tilt="-rotate-2">
                            ARMED
                          </ComicSticker>
                        ) : (
                          <MonoBadge tone="neutral">DISARMED</MonoBadge>
                        )}
                        {isA3 ? (
                          flow.approved ? (
                            <MonoBadge tone="ok">
                              <ShieldCheck className="h-3 w-3" />
                              A3 APPROVED
                            </MonoBadge>
                          ) : (
                            <MonoBadge tone="err">
                              <ShieldAlert className="h-3 w-3" />
                              APPROVAL REQUIRED
                            </MonoBadge>
                          )
                        ) : null}
                      </div>

                      {flow.description ? (
                        <p className="mt-1.5 font-mono text-xs text-[#a6a2b8]">
                          {flow.description}
                        </p>
                      ) : null}

                      <div className="mt-2 flex flex-wrap items-center gap-3 font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                        <span>TRIGGER CFG: {flow.triggerConfigJson || '{}'}</span>
                        <span>LAST RUN: {formatShortTime(flow.lastRunAt)}</span>
                      </div>
                    </div>

                    {/* Switches + Run Controls */}
                    <div className="flex flex-wrap items-center gap-2">
                      {/* Enable Switch */}
                      <button
                        type="button"
                        role="switch"
                        aria-checked={flow.enabled}
                        disabled={isBusy}
                        onClick={() => void handleToggleEnabled(flow)}
                        className={`inline-flex items-center gap-2 border-2 border-black px-2.5 py-1.5 font-mono text-[11px] font-extrabold uppercase transition-colors disabled:opacity-50 ${
                          flow.enabled
                            ? 'bg-[#a3ff12] text-[#08080c]'
                            : 'bg-[#1b1b2b] text-[#a6a2b8] hover:text-[#fff6de]'
                        }`}
                      >
                        <span
                          className={`inline-block h-2.5 w-2.5 border border-black ${
                            flow.enabled ? 'bg-[#08080c]' : 'bg-[#3a3a58]'
                          }`}
                        />
                        {flow.enabled ? 'ENABLED' : 'DISABLED'}
                      </button>

                      {/* Approve Switch */}
                      <button
                        type="button"
                        role="switch"
                        aria-checked={flow.approved}
                        disabled={isBusy}
                        onClick={() => void handleToggleApproved(flow)}
                        className={`inline-flex items-center gap-2 border-2 border-black px-2.5 py-1.5 font-mono text-[11px] font-extrabold uppercase transition-colors disabled:opacity-50 ${
                          flow.approved
                            ? 'bg-[#00f0ff] text-[#08080c]'
                            : isA3
                              ? 'bg-[#ff334b]/20 text-[#ff334b] hover:bg-[#ff334b] hover:text-white'
                              : 'bg-[#1b1b2b] text-[#a6a2b8] hover:text-[#fff6de]'
                        }`}
                      >
                        <ShieldCheck className="h-3.5 w-3.5" />
                        {flow.approved ? 'APPROVED' : 'APPROVE'}
                      </button>

                      {/* Delete Flow */}
                      <button
                        type="button"
                        disabled={isBusy}
                        onClick={() => void handleDeleteFlow(flow)}
                        className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2.5 py-1.5 font-mono text-[11px] font-extrabold text-white disabled:opacity-50"
                        title="Delete flow"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                      </button>
                    </div>
                  </div>

                  {/* Step Pipeline Visualization */}
                  <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-[#252538] pt-3">
                    <span className="font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                      PIPELINE:
                    </span>
                    {flow.steps.length > 0 ? (
                      flow.steps.map((stepObj, idx) => (
                        <div key={`${flow.id}-${stepObj.id}-${idx}`} className="flex items-center gap-1.5">
                          <div className="border border-[#353552] bg-[#13131d] px-2 py-1 font-mono text-[11px]">
                            <span className="font-extrabold text-[#ff2e88]">{idx + 1}.</span>{' '}
                            <span className="font-extrabold text-[#fff6de]">{stepObj.kind}</span>{' '}
                            <span className="text-[10px] text-[#a6a2b8]">
                              ({summarizeStepConfig(stepObj)})
                            </span>
                          </div>
                          {idx < flow.steps.length - 1 ? (
                            <span className="font-mono text-xs font-extrabold text-[#ffe031]">→</span>
                          ) : null}
                        </div>
                      ))
                    ) : (
                      <span className="font-mono text-[11px] text-[#a6a2b8]">No steps defined</span>
                    )}
                  </div>

                  {/* Run Bar + Logs Toggle */}
                  <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-[#252538] pt-3">
                    <input
                      type="text"
                      value={triggerPayloads[flow.id] ?? ''}
                      onChange={(ev) =>
                        setTriggerPayloads((prev) => ({ ...prev, [flow.id]: ev.target.value }))
                      }
                      placeholder="Optional {{trigger}} payload..."
                      className="riot-input min-w-[200px] flex-1 px-2.5 py-1.5 font-mono text-xs"
                    />
                    <button
                      type="button"
                      disabled={isRunning}
                      onClick={() => void handleRunFlow(flow)}
                      className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-1.5 font-display text-xs text-[#08080c] uppercase hover:bg-[#a3ff12] disabled:opacity-50"
                    >
                      <Play className={`h-3.5 w-3.5 ${isRunning ? 'animate-pulse' : ''}`} />
                      {isRunning ? 'RUNNING...' : 'RUN FLOW'}
                    </button>
                    <button
                      type="button"
                      onClick={() => handleToggleRunsInspector(flow.id)}
                      className="riot-btn inline-flex items-center gap-1.5 bg-[#1b1b2b] px-3 py-1.5 font-mono text-[11px] font-extrabold text-[#00f0ff]"
                    >
                      {runsOpen ? (
                        <ChevronDown className="h-3.5 w-3.5" />
                      ) : (
                        <ChevronRight className="h-3.5 w-3.5" />
                      )}
                      RUN LOGS
                      {runsByFlowId[flow.id] ? ` (${runsByFlowId[flow.id].length})` : ''}
                    </button>
                  </div>

                  {/* Collapsible Run Logs Inspector */}
                  {runsOpen ? (
                    <div className="mt-3 border-2 border-black bg-[#13131d] p-3">
                      <div className="mb-2 flex items-center justify-between">
                        <MonoBadge tone="cyan">EXECUTION LOG INSPECTOR</MonoBadge>
                        <button
                          type="button"
                          onClick={() => void loadRunsForFlow(flow.id)}
                          className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-2.5 py-1 font-mono text-[10px] font-extrabold text-[#ffe031]"
                        >
                          <RefreshCw className={`h-3 w-3 ${loadingRuns ? 'animate-spin' : ''}`} />
                          RELOAD LOGS
                        </button>
                      </div>

                      {loadingRuns && flowRuns.length === 0 ? (
                        <div className="py-3 font-mono text-xs text-[#a6a2b8]">
                          Loading run history...
                        </div>
                      ) : flowRuns.length === 0 ? (
                        <div className="py-3 font-mono text-xs text-[#a6a2b8]">
                          No runs recorded yet. Click RUN FLOW to execute.
                        </div>
                      ) : (
                        <div className="space-y-2.5">
                          {flowRuns.map((runRecord) => (
                            <div
                              key={runRecord.id}
                              className="border-2 border-black bg-[#08080c] p-2.5"
                            >
                              <div className="flex flex-wrap items-center justify-between gap-2">
                                <div className="flex flex-wrap items-center gap-2">
                                  <MonoBadge tone={resolveRunStatusTone(runRecord.status)}>
                                    {runRecord.status}
                                  </MonoBadge>
                                  <span className="font-mono text-[11px] font-bold text-[#fff6de]">
                                    {runRecord.id.slice(0, 12)}
                                  </span>
                                  {runRecord.triggerPayload ? (
                                    <MonoBadge tone="neutral">
                                      trigger: {runRecord.triggerPayload}
                                    </MonoBadge>
                                  ) : null}
                                </div>
                                <span className="font-mono text-[10px] text-[#a6a2b8]">
                                  {formatShortTime(runRecord.startedAt)} →{' '}
                                  {formatShortTime(runRecord.finishedAt)}
                                </span>
                              </div>

                              <div className="mt-2 grid grid-cols-1 gap-2 lg:grid-cols-2">
                                <div>
                                  <div className="mb-1 font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                                    Step Logs
                                  </div>
                                  <pre className="max-h-40 overflow-auto border border-[#252538] bg-[#050508] p-2 font-mono text-[11px] text-[#fff6de]">
                                    {formatPrettyJson(runRecord.stepLogsJson)}
                                  </pre>
                                </div>
                                <div>
                                  <div className="mb-1 font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                                    Final Output
                                  </div>
                                  <pre className="max-h-40 overflow-auto border border-[#252538] bg-[#050508] p-2 font-mono text-[11px] text-[#a3ff12]">
                                    {formatPrettyJson(runRecord.outputJson)}
                                  </pre>
                                </div>
                              </div>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  ) : null}
                </article>
              );
            })}
          </div>
        )}
      </SectionCard>
    </div>
  );
}
