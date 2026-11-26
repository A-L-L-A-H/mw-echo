import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Bot,
  ChevronDown,
  ChevronRight,
  Cpu,
  Eye,
  FileCode2,
  FolderGit2,
  GitBranch,
  Minimize2,
  OctagonX,
  Plus,
  RefreshCw,
  Send,
  ShieldAlert,
  Sparkles,
  Terminal,
  Trash2,
  Wrench,
  X,
  Zap,
} from 'lucide-react';
import {
  abortCodingSession,
  compactCodingSession,
  createCodingSession,
  DEFAULT_AGENT_IDS,
  deleteCodingSession,
  fetchCodingSessionDetail,
  fetchCodingSessions,
  fetchObservatoryRepos,
  forkCodingSession,
  patchCodingSession,
  sendCodingPrompt,
  type CodingAgentMode,
  type CodingMessage,
  type CodingSession,
  type CodingStreamEvent,
  type DiscoveredGitRepo,
  type FreeModelCatalogEntry,
} from '../api';
import { formatCompactNumber, formatShortTime } from '../utils';
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  type BadgeTone,
} from './UiPrimitives';

interface CoderTabProps {
  catalogModels: FreeModelCatalogEntry[];
  onHubMutated: () => Promise<void>;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

interface ParsedToolCall {
  id: string;
  name: string;
  argsText: string;
}

interface LiveFeedEntry {
  key: string;
  kind: 'user' | 'assistant' | 'tool_call' | 'tool_result' | 'system' | 'error';
  title: string;
  body: string;
  toolName?: string;
  createdAt?: string;
}

function normalizeToolBadgeName(rawName: string): string {
  const cleaned = (rawName || 'TOOL').trim().toUpperCase();
  if (cleaned.includes('READ')) return 'READ_FILE';
  if (cleaned.includes('EDIT') || cleaned.includes('PATCH')) return 'EDIT_FILE';
  if (cleaned.includes('WRITE') || cleaned.includes('CREATE')) return 'WRITE_FILE';
  if (cleaned.includes('GREP') || cleaned.includes('SEARCH_CODE') || cleaned.includes('FIND')) return 'GREP';
  if (cleaned.includes('SHELL') || cleaned.includes('BASH') || cleaned.includes('EXEC') || cleaned.includes('RUN')) return 'RUN_SHELL';
  if (cleaned.includes('DOC')) return 'HUB_DOCS';
  if (cleaned.includes('TASK')) return 'HUB_TASKS';
  return cleaned;
}

function resolveToolTone(toolLabel: string): BadgeTone {
  switch (toolLabel) {
    case 'READ_FILE':
      return 'cyan';
    case 'EDIT_FILE':
    case 'WRITE_FILE':
      return 'yellow';
    case 'GREP':
      return 'cream';
    case 'RUN_SHELL':
      return 'magenta';
    case 'HUB_DOCS':
    case 'HUB_TASKS':
      return 'ok';
    default:
      return 'neutral';
  }
}

function parseToolCallsJson(rawJson?: string): ParsedToolCall[] {
  if (!rawJson || !rawJson.trim()) {
    return [];
  }
  try {
    const parsed: unknown = JSON.parse(rawJson);
    if (!Array.isArray(parsed)) {
      return [];
    }
    return parsed.map((rawCall, idx) => {
      const record = rawCall && typeof rawCall === 'object' ? (rawCall as Record<string, unknown>) : {};
      const fnObj =
        record.function && typeof record.function === 'object'
          ? (record.function as Record<string, unknown>)
          : {};
      const callName =
        (typeof fnObj.name === 'string' && fnObj.name) ||
        (typeof record.name === 'string' && record.name) ||
        'tool';
      const rawArgs = fnObj.arguments ?? record.arguments ?? '';
      const argsText =
        typeof rawArgs === 'string' ? rawArgs : JSON.stringify(rawArgs, null, 2);
      const callId = (typeof record.id === 'string' && record.id) || `tc-${idx}`;
      return { id: callId, name: callName, argsText };
    });
  } catch {
    return [];
  }
}

function mapMessagesToFeed(messages: CodingMessage[]): LiveFeedEntry[] {
  const entries: LiveFeedEntry[] = [];
  for (const msg of messages) {
    if (msg.role === 'user') {
      entries.push({
        key: msg.id || `u-${msg.seq}`,
        kind: 'user',
        title: 'OPERATOR COMMAND',
        body: msg.content,
        createdAt: msg.createdAt,
      });
      continue;
    }

    if (msg.role === 'tool') {
      const badgeName = normalizeToolBadgeName(msg.toolName || 'TOOL');
      entries.push({
        key: msg.id || `tr-${msg.seq}`,
        kind: 'tool_result',
        title: badgeName,
        toolName: badgeName,
        body: msg.content,
        createdAt: msg.createdAt,
      });
      continue;
    }

    if (msg.role === 'assistant') {
      if (msg.content && msg.content.trim()) {
        entries.push({
          key: `${msg.id || msg.seq}-txt`,
          kind: 'assistant',
          title: 'CODER AGENT',
          body: msg.content,
          createdAt: msg.createdAt,
        });
      }
      const calls: ParsedToolCall[] =
        msg.toolCalls && msg.toolCalls.length > 0
          ? msg.toolCalls.map((call, idx) => ({
              id: call.id || `tc-${idx}`,
              name: call.function?.name || 'tool',
              argsText: call.function?.arguments || '{}',
            }))
          : parseToolCallsJson(msg.toolCallsJson);
      for (const call of calls) {
        const badgeName = normalizeToolBadgeName(call.name);
        entries.push({
          key: `${msg.id || msg.seq}-${call.id}`,
          kind: 'tool_call',
          title: badgeName,
          toolName: badgeName,
          body: call.argsText,
          createdAt: msg.createdAt,
        });
      }
      continue;
    }

    entries.push({
      key: msg.id || `sys-${msg.seq}`,
      kind: 'system',
      title: msg.role.toUpperCase(),
      body: msg.content,
      createdAt: msg.createdAt,
    });
  }
  return entries;
}

export function CoderTab({ catalogModels, onHubMutated, onNotify }: CoderTabProps) {
  const [sessions, setSessions] = useState<CodingSession[]>([]);
  const [loadingSessions, setLoadingSessions] = useState(false);
  const [discoveredRepos, setDiscoveredRepos] = useState<DiscoveredGitRepo[]>([]);

  const [activeSessionId, setActiveSessionId] = useState<string>('');
  const [activeSession, setActiveSession] = useState<CodingSession | null>(null);
  const [persistedMessages, setPersistedMessages] = useState<CodingMessage[]>([]);
  const [streamEvents, setStreamEvents] = useState<LiveFeedEntry[]>([]);
  const [isStreamingTurn, setIsStreamingTurn] = useState(false);
  const [sseConnected, setSseConnected] = useState(false);

  // Create New Mission Drawer
  const [createOpen, setCreateOpen] = useState(false);
  const [newTitle, setNewTitle] = useState('');
  const [newWorkDir, setNewWorkDir] = useState('Z:\\repo\\mw-xv');
  const [newProject, setNewProject] = useState('moonwitness');
  const [newProfile, setNewProfile] = useState('opencode');
  const [newModel, setNewModel] = useState('');
  const [newMode, setNewMode] = useState<CodingAgentMode>('act');
  const [creatingSession, setCreatingSession] = useState(false);

  // Prompt input & collapsible tool cards
  const [promptText, setPromptText] = useState('');
  const [sendingPrompt, setSendingPrompt] = useState(false);
  const [expandedCards, setExpandedCards] = useState<Record<string, boolean>>({});
  const [busyAction, setBusyAction] = useState<'fork' | 'compact' | 'abort' | 'mode' | null>(null);

  const feedBottomRef = useRef<HTMLDivElement | null>(null);

  const loadSessions = useCallback(async () => {
    setLoadingSessions(true);
    try {
      const reply = await fetchCodingSessions();
      const loadedList = reply.sessions ?? [];
      setSessions(loadedList);
      setActiveSessionId((prevId) => {
        if (prevId && loadedList.some((sess) => sess.id === prevId)) {
          return prevId;
        }
        return loadedList[0]?.id ?? '';
      });
    } catch {
      // Backend /api/agent/sessions may still be booting.
    } finally {
      setLoadingSessions(false);
    }
  }, []);

  const loadSessionDetail = useCallback(async (sessionId: string) => {
    if (!sessionId) {
      setActiveSession(null);
      setPersistedMessages([]);
      return;
    }
    try {
      const detail = await fetchCodingSessionDetail(sessionId);
      setActiveSession(detail.session);
      setPersistedMessages(detail.messages ?? []);
      setIsStreamingTurn(detail.session.status === 'running');
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to load coding session';
      onNotify('err', message);
    }
  }, [onNotify]);

  useEffect(() => {
    void loadSessions();
    void fetchObservatoryRepos()
      .then((radarReply) => setDiscoveredRepos(radarReply.repos ?? []))
      .catch(() => {
        // Optional helper dropdown from repo radar.
      });
  }, [loadSessions]);

  useEffect(() => {
    setStreamEvents([]);
    if (!activeSessionId) {
      setActiveSession(null);
      setPersistedMessages([]);
      return;
    }
    void loadSessionDetail(activeSessionId);
  }, [activeSessionId, loadSessionDetail]);

  // Live EventSource SSE stream for active session
  useEffect(() => {
    if (!activeSessionId) {
      setSseConnected(false);
      return;
    }

    const streamUrl = `/api/agent/sessions/${encodeURIComponent(activeSessionId)}/events`;
    const eventSource = new EventSource(streamUrl);

    eventSource.onopen = () => {
      setSseConnected(true);
    };

    eventSource.onmessage = (ev) => {
      if (!ev.data) {
        return;
      }
      try {
        const parsedEvent = JSON.parse(ev.data) as CodingStreamEvent;
        const eventKey = `${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;

        if (parsedEvent.type === 'turn_start') {
          setIsStreamingTurn(true);
          return;
        }

        if (parsedEvent.type === 'step') {
          setStreamEvents((prev) => [
            ...prev,
            {
              key: eventKey,
              kind: 'system',
              title: `STEP ${parsedEvent.step ?? ''}`.trim(),
              body: parsedEvent.content || 'Thinking / executing next tool call...',
              createdAt: parsedEvent.timestamp,
            },
          ]);
          return;
        }

        if (parsedEvent.type === 'assistant_message') {
          const textBody =
            parsedEvent.content || parsedEvent.reply || parsedEvent.message?.content || '';
          if (textBody.trim()) {
            setStreamEvents((prev) => [
              ...prev,
              {
                key: eventKey,
                kind: 'assistant',
                title: parsedEvent.model ? `CODER AGENT (${parsedEvent.model})` : 'CODER AGENT',
                body: textBody,
                createdAt: parsedEvent.timestamp,
              },
            ]);
          }
          return;
        }

        if (parsedEvent.type === 'tool_call') {
          const rawToolName =
            parsedEvent.toolName || parsedEvent.toolCall?.function?.name || 'TOOL';
          const badgeName = normalizeToolBadgeName(rawToolName);
          const rawArgs =
            parsedEvent.arguments ||
            parsedEvent.toolCall?.function?.arguments ||
            parsedEvent.content ||
            '{}';
          setStreamEvents((prev) => [
            ...prev,
            {
              key: eventKey,
              kind: 'tool_call',
              title: badgeName,
              toolName: badgeName,
              body: rawArgs,
              createdAt: parsedEvent.timestamp,
            },
          ]);
          return;
        }

        if (parsedEvent.type === 'tool_result') {
          const badgeName = normalizeToolBadgeName(parsedEvent.toolName || 'TOOL');
          setStreamEvents((prev) => [
            ...prev,
            {
              key: eventKey,
              kind: 'tool_result',
              title: badgeName,
              toolName: badgeName,
              body: parsedEvent.toolOutput || parsedEvent.output || parsedEvent.content || '',
              createdAt: parsedEvent.timestamp,
            },
          ]);
          return;
        }

        if (parsedEvent.type === 'error') {
          setIsStreamingTurn(false);
          setStreamEvents((prev) => [
            ...prev,
            {
              key: eventKey,
              kind: 'error',
              title: 'COMBAT ERROR',
              body: parsedEvent.error || parsedEvent.content || 'Unknown agent error',
              createdAt: parsedEvent.timestamp,
            },
          ]);
          void loadSessionDetail(activeSessionId);
          return;
        }

        if (parsedEvent.type === 'turn_end' || parsedEvent.type === 'aborted') {
          setIsStreamingTurn(false);
          setStreamEvents([]);
          void loadSessionDetail(activeSessionId);
          void loadSessions();
          void onHubMutated();
        }
      } catch {
        // Ignore keepalive or non-JSON comment frames.
      }
    };

    eventSource.onerror = () => {
      setSseConnected(false);
    };

    return () => {
      eventSource.close();
      setSseConnected(false);
    };
  }, [activeSessionId, loadSessionDetail, loadSessions, onHubMutated]);

  const combinedFeed = [...mapMessagesToFeed(persistedMessages), ...streamEvents];

  useEffect(() => {
    feedBottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [combinedFeed.length]);

  const handleCreateSession = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!newWorkDir.trim()) {
      onNotify('err', 'Workspace directory is required.');
      return;
    }
    setCreatingSession(true);
    try {
      const created = await createCodingSession({
        title: newTitle.trim() || undefined,
        workDir: newWorkDir.trim(),
        project: newProject.trim() || undefined,
        agentProfile: newProfile.trim() || 'opencode',
        model: newModel.trim() || undefined,
        mode: newMode,
      });
      setCreateOpen(false);
      setNewTitle('');
      await loadSessions();
      setActiveSessionId(created.id);
      onNotify('ok', `Launched coding mission "${created.title || created.id}".`);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to create mission';
      onNotify('err', message);
    } finally {
      setCreatingSession(false);
    }
  };

  const handleSwitchMode = async (targetMode: CodingAgentMode) => {
    if (!activeSession || activeSession.mode === targetMode) {
      return;
    }
    setBusyAction('mode');
    try {
      const updated = await patchCodingSession(activeSession.id, { mode: targetMode });
      setActiveSession(updated);
      setSessions((prev) =>
        prev.map((sess) => (sess.id === updated.id ? updated : sess)),
      );
      onNotify(
        'info',
        targetMode === 'act'
          ? 'Switched to ACT // LIVE FIRE mode (writes & shell enabled).'
          : 'Switched to PLAN // RECON mode (read-only reconnaissance).',
      );
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to switch mode';
      onNotify('err', message);
    } finally {
      setBusyAction(null);
    }
  };

  const handleCompactSession = async () => {
    if (!activeSession) return;
    setBusyAction('compact');
    try {
      const compactOut = await compactCodingSession(activeSession.id);
      onNotify('ok', `Compacted mission context (kept ${compactOut.keptMessages} messages).`);
      await loadSessionDetail(activeSession.id);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Compact failed';
      onNotify('err', message);
    } finally {
      setBusyAction(null);
    }
  };

  const handleForkSession = async () => {
    if (!activeSession) return;
    setBusyAction('fork');
    try {
      const branched = await forkCodingSession(
        activeSession.id,
        `${activeSession.title || 'Mission'} [FORK]`,
      );
      await loadSessions();
      setActiveSessionId(branched.id);
      onNotify('ok', `Forked mission into "${branched.title}".`);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Fork failed';
      onNotify('err', message);
    } finally {
      setBusyAction(null);
    }
  };

  const handleAbortTurn = async () => {
    if (!activeSession) return;
    setBusyAction('abort');
    try {
      await abortCodingSession(activeSession.id);
      setIsStreamingTurn(false);
      onNotify('info', 'Abort signal transmitted to active turn.');
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Abort failed';
      onNotify('err', message);
    } finally {
      setBusyAction(null);
    }
  };

  const handleDeleteSession = async (session: CodingSession) => {
    try {
      await deleteCodingSession(session.id);
      onNotify('ok', `Deleted mission "${session.title || session.id}".`);
      if (activeSessionId === session.id) {
        setActiveSessionId('');
      }
      await loadSessions();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Delete failed';
      onNotify('err', message);
    }
  };

  const handleSendPrompt = async (event: React.FormEvent) => {
    event.preventDefault();
    const trimmed = promptText.trim();
    if (!trimmed) {
      return;
    }
    if (!activeSession) {
      onNotify('err', 'Create or select a coding mission first.');
      return;
    }

    // Handle quick slash triggers directly
    if (trimmed === '/plan') {
      setPromptText('');
      await handleSwitchMode('plan');
      return;
    }
    if (trimmed === '/act') {
      setPromptText('');
      await handleSwitchMode('act');
      return;
    }
    if (trimmed === '/compact') {
      setPromptText('');
      await handleCompactSession();
      return;
    }

    setSendingPrompt(true);
    setPromptText('');
    setIsStreamingTurn(true);
    setStreamEvents((prev) => [
      ...prev,
      {
        key: `optimistic-${Date.now()}`,
        kind: 'user',
        title: 'OPERATOR COMMAND',
        body: trimmed,
        createdAt: new Date().toISOString(),
      },
    ]);

    try {
      await sendCodingPrompt(activeSession.id, trimmed);
    } catch (caughtError) {
      setIsStreamingTurn(false);
      const message = caughtError instanceof Error ? caughtError.message : 'Prompt dispatch failed';
      onNotify('err', message);
    } finally {
      setSendingPrompt(false);
    }
  };

  const toggleCardExpanded = (cardKey: string) => {
    setExpandedCards((prev) => ({
      ...prev,
      [cardKey]: !prev[cardKey],
    }));
  };

  const activeMode: CodingAgentMode =
    activeSession?.mode === 'plan' ? 'plan' : 'act';

  return (
    <div className="space-y-5">
      <datalist id="mw-coder-models">
        <option value="auto" />
        {catalogModels.slice(0, 60).map((entry) => (
          <option key={entry.id} value={entry.modelId} />
        ))}
      </datalist>

      <StageHeroBanner
        sceneCode="SCENE 10"
        sticker="CODER AGENT"
        title="RIOT CODER // AUTONOMOUS COMBAT CONSOLE"
        subtitle="BUILT-IN PI/OPENCODE AGENT WIRED TO MW-ECHO AI GATEWAY — LIVE SSE TOOL CALLS, ACT/PLAN MODES, FORK & COMPACT"
        tone="ok"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <MonoBadge tone={sseConnected ? 'ok' : 'neutral'}>
              SSE: {sseConnected ? 'LOCKED' : 'STANDBY'}
            </MonoBadge>
            <button
              type="button"
              onClick={() => setCreateOpen((prev) => !prev)}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-display text-xs text-[#08080c] uppercase"
            >
              <Plus className="h-4 w-4" />
              NEW MISSION
            </button>
          </div>
        }
      />

      {/* + New Mission Cartridge Creator */}
      {createOpen ? (
        <SectionCard
          title="LAUNCH NEW CODER MISSION CARTRIDGE"
          subtitle="Bind a local workspace or discovered Git repo, pick your Magpie agent profile, and choose ACT or PLAN."
          icon={<Terminal className="h-4 w-4" />}
          sticker="DEPLOY"
          stickerTone="magenta"
          accent="yellow"
          actions={
            <button
              type="button"
              onClick={() => setCreateOpen(false)}
              className="riot-btn bg-[#1b1b2b] p-1.5 text-[#fff6de]"
            >
              <X className="h-4 w-4" />
            </button>
          }
        >
          <form onSubmit={handleCreateSession} className="space-y-3">
            {discoveredRepos.length > 0 ? (
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                  Quick Pick Discovered Git Repo (from 07 RADAR)
                </label>
                <div className="flex flex-wrap gap-1.5">
                  {discoveredRepos.slice(0, 8).map((repo) => (
                    <button
                      key={repo.path}
                      type="button"
                      onClick={() => {
                        setNewWorkDir(repo.path);
                        setNewProject(repo.name);
                        if (!newTitle) setNewTitle(`Hack ${repo.name}`);
                      }}
                      className={`riot-btn inline-flex items-center gap-1 px-2.5 py-1 font-mono text-[11px] font-extrabold ${
                        newWorkDir === repo.path
                          ? 'bg-[#00f0ff] text-[#08080c]'
                          : 'bg-[#08080c] text-[#fff6de]'
                      }`}
                    >
                      <FolderGit2 className="h-3 w-3" />
                      {repo.name}
                    </button>
                  ))}
                </div>
              </div>
            ) : null}

            <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                  Mission Title
                </label>
                <input
                  type="text"
                  value={newTitle}
                  onChange={(ev) => setNewTitle(ev.target.value)}
                  placeholder="Fix auth bug / build feature"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
              <div className="md:col-span-2">
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                  Workspace Directory
                </label>
                <input
                  type="text"
                  value={newWorkDir}
                  onChange={(ev) => setNewWorkDir(ev.target.value)}
                  placeholder="Z:\repo\mw-xv"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
            </div>

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                  Project Slug
                </label>
                <input
                  type="text"
                  value={newProject}
                  onChange={(ev) => setNewProject(ev.target.value)}
                  placeholder="moonwitness"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ff2e88]">
                  Agent Profile (Gateway)
                </label>
                <select
                  value={newProfile}
                  onChange={(ev) => setNewProfile(ev.target.value)}
                  className="riot-input w-full px-3 py-2 font-mono text-xs font-bold"
                >
                  {DEFAULT_AGENT_IDS.map((agentId) => (
                    <option key={agentId} value={agentId}>
                      {agentId}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                  Model Override (optional)
                </label>
                <input
                  type="text"
                  list="mw-coder-models"
                  value={newModel}
                  onChange={(ev) => setNewModel(ev.target.value)}
                  placeholder="auto (from Magpie switchboard)"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a3ff12]">
                  Initial Mode
                </label>
                <div className="grid grid-cols-2 border-2 border-black bg-[#08080c]">
                  <button
                    type="button"
                    onClick={() => setNewMode('act')}
                    className={`py-2 font-mono text-xs font-extrabold ${
                      newMode === 'act' ? 'bg-[#ff2e88] text-white' : 'text-[#a6a2b8]'
                    }`}
                  >
                    ACT
                  </button>
                  <button
                    type="button"
                    onClick={() => setNewMode('plan')}
                    className={`border-l-2 border-black py-2 font-mono text-xs font-extrabold ${
                      newMode === 'plan' ? 'bg-[#00f0ff] text-[#08080c]' : 'text-[#a6a2b8]'
                    }`}
                  >
                    PLAN
                  </button>
                </div>
              </div>
            </div>

            <div className="flex justify-end pt-1">
              <button
                type="submit"
                disabled={creatingSession}
                className="riot-btn inline-flex items-center gap-2 bg-[#a3ff12] px-5 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
              >
                <Zap className="h-4 w-4" />
                {creatingSession ? 'BOOTING...' : 'START MISSION'}
              </button>
            </div>
          </form>
        </SectionCard>
      ) : null}

      {/* Split Layout: Left Session Cartridge Deck + Right Combat Console */}
      <div className="grid grid-cols-1 gap-5 lg:grid-cols-12">
        {/* Left Session Cartridge Deck */}
        <div className="flex flex-col border-[2.5px] border-black bg-[#13131d] shadow-[6px_6px_0_#000] lg:col-span-4 xl:col-span-3">
          <div className="flex items-center justify-between border-b-[2.5px] border-black bg-[#1b1b2b] px-3.5 py-2.5">
            <div className="flex items-center gap-2">
              <Bot className="h-4 w-4 text-[#ffe031]" />
              <span className="font-display text-xs text-[#fff6de] uppercase">
                MISSION CARTRIDGES ({sessions.length})
              </span>
            </div>
            <button
              type="button"
              onClick={() => void loadSessions()}
              className="border-2 border-black bg-[#08080c] p-1 text-[#ffe031] hover:bg-[#ffe031] hover:text-[#08080c]"
              title="Refresh sessions"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingSessions ? 'animate-spin' : ''}`} />
            </button>
          </div>

          <div className="max-h-[70vh] flex-1 space-y-2.5 overflow-y-auto p-3">
            {sessions.length === 0 ? (
              <div className="border-2 border-dashed border-[#2f2f46] bg-[#08080c] p-5 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
                NO MISSIONS LOADED. CLICK &quot;NEW MISSION&quot; ABOVE.
              </div>
            ) : (
              sessions.map((session) => {
                const isSelected = session.id === activeSessionId;
                return (
                  <div
                    key={session.id}
                    onClick={() => setActiveSessionId(session.id)}
                    role="button"
                    tabIndex={0}
                    onKeyDown={(ev) => {
                      if (ev.key === 'Enter' || ev.key === ' ') {
                        setActiveSessionId(session.id);
                      }
                    }}
                    className={`cursor-pointer border-2 border-black p-3 transition-all ${
                      isSelected
                        ? '-translate-x-0.5 -translate-y-0.5 bg-[#ffe031] text-[#08080c] shadow-[4px_4px_0_#ff2e88]'
                        : 'bg-[#08080c] text-[#fff6de] shadow-[3px_3px_0_#000] hover:bg-[#1b1b2b]'
                    }`}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <span className="line-clamp-1 font-display text-xs uppercase">
                        {session.title || 'UNTITLED MISSION'}
                      </span>
                      <button
                        type="button"
                        onClick={(ev) => {
                          ev.stopPropagation();
                          void handleDeleteSession(session);
                        }}
                        className={`shrink-0 border border-black p-0.5 ${
                          isSelected
                            ? 'bg-[#08080c] text-[#ff334b]'
                            : 'bg-[#1b1b2b] text-[#a6a2b8] hover:bg-[#ff334b] hover:text-white'
                        }`}
                        title="Delete session"
                      >
                        <Trash2 className="h-3 w-3" />
                      </button>
                    </div>

                    <div
                      className={`mt-1 truncate font-mono text-[10px] font-bold ${
                        isSelected ? 'text-[#08080c]/80' : 'text-[#00f0ff]'
                      }`}
                    >
                      {session.workDir}
                    </div>

                    <div className="mt-2 flex flex-wrap items-center gap-1">
                      <span
                        className={`border border-black px-1.5 py-0.5 font-mono text-[9px] font-extrabold uppercase ${
                          session.mode === 'plan'
                            ? 'bg-[#00f0ff] text-[#08080c]'
                            : 'bg-[#ff2e88] text-white'
                        }`}
                      >
                        {session.mode === 'plan' ? 'PLAN' : 'ACT'}
                      </span>
                      <span
                        className={`border border-black px-1.5 py-0.5 font-mono text-[9px] font-extrabold uppercase ${
                          isSelected
                            ? 'bg-[#08080c] text-[#ffe031]'
                            : 'bg-[#1b1b2b] text-[#fff6de]'
                        }`}
                      >
                        {session.agentProfile || 'opencode'}
                      </span>
                      {session.rtkTokensSaved > 0 ? (
                        <span className="border border-black bg-[#a3ff12] px-1.5 py-0.5 font-mono text-[9px] font-extrabold text-[#08080c]">
                          -{formatCompactNumber(session.rtkTokensSaved)} RTK
                        </span>
                      ) : null}
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </div>

        {/* Main Combat Console */}
        <div className="flex min-h-[72vh] flex-col border-[2.5px] border-black bg-[#13131d] shadow-[6px_6px_0_#00f0ff] lg:col-span-8 xl:col-span-9">
          {/* Mission HUD Top Bar */}
          <div className="flex flex-wrap items-center justify-between gap-3 border-b-[2.5px] border-black bg-[#1b1b2b] px-4 py-3">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="truncate font-display text-sm text-[#fff6de] uppercase">
                  {activeSession ? activeSession.title || activeSession.id : 'NO ACTIVE MISSION'}
                </h2>
                {activeSession ? (
                  <>
                    <MonoBadge tone="cyan">{activeSession.agentProfile || 'opencode'}</MonoBadge>
                    {activeSession.model ? (
                      <MonoBadge tone="neutral">{activeSession.model}</MonoBadge>
                    ) : null}
                    <MonoBadge tone="ok">
                      <Sparkles className="h-3 w-3" />
                      RTK SAVED: {formatCompactNumber(activeSession.rtkTokensSaved)}
                    </MonoBadge>
                    <MonoBadge tone="yellow">
                      TOK: {formatCompactNumber(activeSession.totalTokensIn)}/
                      {formatCompactNumber(activeSession.totalTokensOut)}
                    </MonoBadge>
                  </>
                ) : null}
              </div>
              {activeSession ? (
                <div className="mt-1 truncate font-mono text-[10px] font-bold text-[#a6a2b8]">
                  WORKDIR: {activeSession.workDir}
                </div>
              ) : null}
            </div>

            {activeSession ? (
              <div className="flex flex-wrap items-center gap-2">
                {/* ACT // LIVE FIRE vs PLAN // RECON toggle */}
                <div className="inline-flex border-2 border-black bg-[#08080c] shadow-[3px_3px_0_#000]">
                  <button
                    type="button"
                    disabled={busyAction === 'mode'}
                    onClick={() => void handleSwitchMode('act')}
                    className={`inline-flex items-center gap-1 px-2.5 py-1 font-mono text-[11px] font-extrabold uppercase ${
                      activeMode === 'act'
                        ? 'bg-[#ff2e88] text-white'
                        : 'text-[#a6a2b8] hover:text-[#fff6de]'
                    }`}
                  >
                    <Zap className="h-3 w-3" />
                    ACT // LIVE FIRE
                  </button>
                  <button
                    type="button"
                    disabled={busyAction === 'mode'}
                    onClick={() => void handleSwitchMode('plan')}
                    className={`inline-flex items-center gap-1 border-l-2 border-black px-2.5 py-1 font-mono text-[11px] font-extrabold uppercase ${
                      activeMode === 'plan'
                        ? 'bg-[#00f0ff] text-[#08080c]'
                        : 'text-[#a6a2b8] hover:text-[#fff6de]'
                    }`}
                  >
                    <Eye className="h-3 w-3" />
                    PLAN // RECON
                  </button>
                </div>

                <button
                  type="button"
                  disabled={busyAction === 'fork'}
                  onClick={() => void handleForkSession()}
                  className="riot-btn inline-flex items-center gap-1 bg-[#13131d] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#00f0ff]"
                  title="Fork mission branch"
                >
                  <GitBranch className="h-3.5 w-3.5" />
                  FORK
                </button>

                <button
                  type="button"
                  disabled={busyAction === 'compact'}
                  onClick={() => void handleCompactSession()}
                  className="riot-btn inline-flex items-center gap-1 bg-[#13131d] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#ffe031]"
                  title="Compact context history"
                >
                  <Minimize2 className="h-3.5 w-3.5" />
                  COMPACT
                </button>

                {isStreamingTurn ? (
                  <button
                    type="button"
                    disabled={busyAction === 'abort'}
                    onClick={() => void handleAbortTurn()}
                    className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-3 py-1 font-mono text-[11px] font-extrabold text-white"
                  >
                    <OctagonX className="h-3.5 w-3.5" />
                    ABORT
                  </button>
                ) : null}
              </div>
            ) : null}
          </div>

          {/* Live Combat Mission Feed */}
          <div className="max-h-[54vh] flex-1 space-y-3 overflow-y-auto bg-[#08080c] p-4">
            {!activeSession ? (
              <div className="flex h-full min-h-[280px] flex-col items-center justify-center gap-3 text-center">
                <ComicSticker tone="yellow">SELECT OR CREATE A MISSION</ComicSticker>
                <p className="max-w-md font-mono text-xs text-[#a6a2b8]">
                  Launch a coding mission targeting any local Git repository. Every prompt routes
                  through mw-echo&apos;s Bifrost/Magpie/9Router gateway with RTK compression.
                </p>
              </div>
            ) : combinedFeed.length === 0 ? (
              <div className="flex h-full min-h-[280px] flex-col items-center justify-center gap-2 text-center">
                <ComicSticker tone="cyan">COMBAT CONSOLE ARMED</ComicSticker>
                <p className="font-mono text-xs text-[#a6a2b8]">
                  Send an instruction below or use /plan, /act, /compact.
                </p>
              </div>
            ) : (
              combinedFeed.map((entry) => {
                if (entry.kind === 'user') {
                  return (
                    <div key={entry.key} className="flex justify-end">
                      <div className="max-w-[85%] border-2 border-black bg-[#ffe031] px-3.5 py-2.5 text-[#08080c] shadow-[4px_4px_0_#000]">
                        <div className="mb-1 flex items-center justify-between gap-3 font-mono text-[10px] font-extrabold uppercase">
                          <span>{entry.title}</span>
                          {entry.createdAt ? (
                            <span className="opacity-70">{formatShortTime(entry.createdAt)}</span>
                          ) : null}
                        </div>
                        <div className="whitespace-pre-wrap break-words font-mono text-xs font-bold leading-relaxed">
                          {entry.body}
                        </div>
                      </div>
                    </div>
                  );
                }

                if (entry.kind === 'tool_call' || entry.kind === 'tool_result') {
                  const badgeTone = resolveToolTone(entry.toolName || 'TOOL');
                  const isOpen = Boolean(expandedCards[entry.key]);
                  const previewLine = entry.body.replace(/\s+/g, ' ').slice(0, 110);
                  return (
                    <div
                      key={entry.key}
                      className="border-2 border-black bg-[#13131d] shadow-[4px_4px_0_#000]"
                    >
                      <button
                        type="button"
                        onClick={() => toggleCardExpanded(entry.key)}
                        className="flex w-full items-center justify-between gap-2 border-b border-black bg-[#1b1b2b] px-3 py-2 text-left"
                      >
                        <div className="flex min-w-0 items-center gap-2">
                          {isOpen ? (
                            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-[#ffe031]" />
                          ) : (
                            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-[#ffe031]" />
                          )}
                          <MonoBadge tone={badgeTone}>
                            {entry.kind === 'tool_call' ? (
                              <Wrench className="h-3 w-3" />
                            ) : (
                              <FileCode2 className="h-3 w-3" />
                            )}
                            {entry.title}
                          </MonoBadge>
                          <MonoBadge tone={entry.kind === 'tool_call' ? 'neutral' : 'ok'}>
                            {entry.kind === 'tool_call' ? 'CALL' : 'OUTPUT'}
                          </MonoBadge>
                          {!isOpen ? (
                            <span className="truncate font-mono text-[11px] text-[#a6a2b8]">
                              {previewLine}
                            </span>
                          ) : null}
                        </div>
                        <span className="shrink-0 font-mono text-[10px] font-bold text-[#a6a2b8]">
                          {isOpen ? 'HIDE' : 'INSPECT'}
                        </span>
                      </button>

                      {isOpen ? (
                        <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words bg-[#050508] p-3 font-mono text-[11px] leading-relaxed text-[#fff6de]">
                          {entry.body || '(empty output)'}
                        </pre>
                      ) : null}
                    </div>
                  );
                }

                if (entry.kind === 'error') {
                  return (
                    <div
                      key={entry.key}
                      className="border-2 border-black bg-[#ff334b] px-3.5 py-2.5 text-white shadow-[4px_4px_0_#000]"
                    >
                      <div className="mb-1 flex items-center gap-1.5 font-mono text-[10px] font-extrabold uppercase">
                        <ShieldAlert className="h-3.5 w-3.5" />
                        {entry.title}
                      </div>
                      <pre className="whitespace-pre-wrap break-words font-mono text-xs font-bold">
                        {entry.body}
                      </pre>
                    </div>
                  );
                }

                if (entry.kind === 'system') {
                  return (
                    <div
                      key={entry.key}
                      className="flex items-center gap-2 font-mono text-[11px] font-bold text-[#00f0ff]"
                    >
                      <Cpu className="h-3.5 w-3.5 shrink-0" />
                      <span>[{entry.title}]</span>
                      <span className="text-[#a6a2b8]">{entry.body}</span>
                    </div>
                  );
                }

                return (
                  <div
                    key={entry.key}
                    className="border-2 border-black bg-[#13131d] px-4 py-3 shadow-[4px_4px_0_#00f0ff]"
                  >
                    <div className="mb-1.5 flex items-center justify-between gap-2 border-b border-[#252538] pb-1.5">
                      <MonoBadge tone="cyan">
                        <Bot className="h-3 w-3" />
                        {entry.title}
                      </MonoBadge>
                      {entry.createdAt ? (
                        <span className="font-mono text-[10px] font-bold text-[#a6a2b8]">
                          {formatShortTime(entry.createdAt)}
                        </span>
                      ) : null}
                    </div>
                    <pre className="whitespace-pre-wrap break-words font-sans text-sm leading-relaxed text-[#fff6de]">
                      {entry.body}
                    </pre>
                  </div>
                );
              })
            )}

            {isStreamingTurn ? (
              <div className="inline-flex items-center gap-2 border-2 border-black bg-[#ff2e88] px-3 py-1 font-mono text-xs font-extrabold text-white shadow-[3px_3px_0_#000]">
                <RefreshCw className="h-3.5 w-3.5 animate-spin" />
                AGENT EXECUTING TURN...
              </div>
            ) : null}
            <div ref={feedBottomRef} />
          </div>

          {/* Bottom Command Prompt + Quick Slash Pills */}
          <form
            onSubmit={handleSendPrompt}
            className="space-y-2.5 border-t-[2.5px] border-black bg-[#1b1b2b] p-3.5"
          >
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                QUICK SLASH:
              </span>
              <button
                type="button"
                disabled={!activeSession}
                onClick={() => void handleSwitchMode('plan')}
                className="border-2 border-black bg-[#00f0ff] px-2 py-0.5 font-mono text-[10px] font-extrabold text-[#08080c] hover:opacity-90 disabled:opacity-40"
              >
                /plan
              </button>
              <button
                type="button"
                disabled={!activeSession}
                onClick={() => void handleSwitchMode('act')}
                className="border-2 border-black bg-[#ff2e88] px-2 py-0.5 font-mono text-[10px] font-extrabold text-white hover:opacity-90 disabled:opacity-40"
              >
                /act
              </button>
              <button
                type="button"
                disabled={!activeSession}
                onClick={() => void handleCompactSession()}
                className="border-2 border-black bg-[#ffe031] px-2 py-0.5 font-mono text-[10px] font-extrabold text-[#08080c] hover:opacity-90 disabled:opacity-40"
              >
                /compact
              </button>
            </div>

            <div className="flex gap-2">
              <textarea
                rows={2}
                value={promptText}
                onChange={(ev) => setPromptText(ev.target.value)}
                onKeyDown={(ev) => {
                  if (ev.key === 'Enter' && !ev.shiftKey) {
                    ev.preventDefault();
                    void handleSendPrompt(ev);
                  }
                }}
                disabled={!activeSession}
                placeholder={
                  activeSession
                    ? 'Issue coding command... (Enter to fire, Shift+Enter for newline)'
                    : 'Create or select a mission cartridge first...'
                }
                className="riot-input min-w-0 flex-1 px-3 py-2 font-mono text-xs disabled:opacity-50"
              />
              <button
                type="submit"
                disabled={!activeSession || sendingPrompt}
                className="riot-btn inline-flex shrink-0 items-center gap-1.5 bg-[#a3ff12] px-5 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
              >
                <Send className="h-4 w-4" />
                {sendingPrompt ? '...' : 'FIRE'}
              </button>
            </div>
          </form>
        </div>
      </div>
    </div>
  );
}
