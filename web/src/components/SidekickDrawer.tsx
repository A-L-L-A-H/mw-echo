import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Bot,
  Flame,
  Send,
  Sparkles,
  Trash2,
  Wrench,
  X,
  Zap,
} from 'lucide-react';
import {
  clearSidekickHistory,
  fetchSidekickContext,
  fetchSidekickHistory,
  fetchSidekickPersonas,
  requestSidekickBrief,
  sendSidekickChat,
  type SidekickLiveContext,
  type SidekickMessage,
  type SidekickPersona,
} from '../api';
import { formatCompactNumber, formatShortTime } from '../utils';
import { ComicSticker, MonoBadge } from './UiPrimitives';

interface SidekickDrawerProps {
  isOpen: boolean;
  onClose: () => void;
  onActionTriggered: () => Promise<void>;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

const FALLBACK_PERSONAS: SidekickPersona[] = [
  {
    id: 'tactician',
    name: 'TACTICIAN',
    role: 'Strategic Route Mapping & High-Level Planning',
    accent: '#3b82f6',
    colorName: 'Blue',
    agentProfile: 'claude-code',
  },
  {
    id: 'hacker',
    name: 'HACKER',
    role: 'Low-Level Code Execution & Vulnerability Scanning',
    accent: '#10b981',
    colorName: 'Emerald',
    agentProfile: 'opencode',
  },
  {
    id: 'oracle',
    name: 'ORACLE',
    role: 'Architectural Insights & Deep Diagnostics',
    accent: '#8b5cf6',
    colorName: 'Purple',
    agentProfile: 'gemini-cli',
  },
  {
    id: 'dispatcher',
    name: 'DISPATCHER',
    role: 'Task Orchestration & Event Streaming Coordination',
    accent: '#f59e0b',
    colorName: 'Amber',
    agentProfile: 'default',
  },
];

function extractToolChips(toolSummary?: string): string[] {
  if (!toolSummary || !toolSummary.trim()) {
    return [];
  }
  return toolSummary
    .split(/[,;|\n]+/)
    .map((part) => part.trim())
    .filter(Boolean)
    .slice(0, 6);
}

export function SidekickDrawer({
  isOpen,
  onClose,
  onActionTriggered,
  onNotify,
}: SidekickDrawerProps) {
  const [personas, setPersonas] = useState<SidekickPersona[]>(FALLBACK_PERSONAS);
  const [activePersonaId, setActivePersonaId] = useState<string>('tactician');
  const [messages, setMessages] = useState<SidekickMessage[]>([]);
  const [loadingHistory, setLoadingHistory] = useState(false);
  const [liveContext, setLiveContext] = useState<SidekickLiveContext | null>(null);
  const [promptInput, setPromptInput] = useState('');
  const [sending, setSending] = useState(false);
  const [briefing, setBriefing] = useState(false);

  const inputRef = useRef<HTMLInputElement | null>(null);
  const endRef = useRef<HTMLDivElement | null>(null);

  const loadSidekickData = useCallback(async () => {
    setLoadingHistory(true);
    try {
      const [personaOutcome, historyOutcome, contextOutcome] = await Promise.allSettled([
        fetchSidekickPersonas(),
        fetchSidekickHistory(60),
        fetchSidekickContext(),
      ]);
      if (
        personaOutcome.status === 'fulfilled' &&
        personaOutcome.value.personas &&
        personaOutcome.value.personas.length > 0
      ) {
        setPersonas(personaOutcome.value.personas);
      }
      if (historyOutcome.status === 'fulfilled') {
        setMessages(historyOutcome.value.messages ?? []);
      }
      if (contextOutcome.status === 'fulfilled') {
        setLiveContext(contextOutcome.value);
      }
    } finally {
      setLoadingHistory(false);
    }
  }, []);

  useEffect(() => {
    if (!isOpen) return;
    void loadSidekickData();
    window.setTimeout(() => inputRef.current?.focus(), 60);
  }, [isOpen, loadSidekickData]);

  useEffect(() => {
    if (isOpen) {
      endRef.current?.scrollIntoView({ behavior: 'smooth' });
    }
  }, [messages.length, isOpen]);

  useEffect(() => {
    if (!isOpen) return;
    const handleEsc = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', handleEsc);
    return () => window.removeEventListener('keydown', handleEsc);
  }, [isOpen, onClose]);

  if (!isOpen) {
    return null;
  }

  const activePersona =
    personas.find((persona) => persona.id === activePersonaId) ?? personas[0] ?? FALLBACK_PERSONAS[0];

  const handleSendChat = async (event: React.FormEvent) => {
    event.preventDefault();
    const trimmed = promptInput.trim();
    if (!trimmed) return;
    setSending(true);
    setPromptInput('');

    const optimisticUserMsg: SidekickMessage = {
      id: `user-${Date.now()}`,
      persona: activePersona.id,
      role: 'user',
      content: trimmed,
      createdAt: new Date().toISOString(),
    };
    setMessages((prev) => [...prev, optimisticUserMsg]);

    try {
      const replyMsg = await sendSidekickChat({
        persona: activePersona.id,
        prompt: trimmed,
      });
      setMessages((prev) => [...prev, replyMsg]);
      await onActionTriggered();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Sidekick transmission failed';
      onNotify('err', message);
    } finally {
      setSending(false);
    }
  };

  const handleDailyBrief = async () => {
    setBriefing(true);
    try {
      const briefMsg = await requestSidekickBrief(activePersona.id);
      setMessages((prev) => [...prev, briefMsg]);
      onNotify('ok', `${activePersona.name} delivered your Daily Brief.`);
      await onActionTriggered();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to generate daily brief';
      onNotify('err', message);
    } finally {
      setBriefing(false);
    }
  };

  const handleClearHistory = async () => {
    try {
      await clearSidekickHistory();
      setMessages([]);
      onNotify('ok', 'Shadow Sidekick memory cleared.');
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to clear history';
      onNotify('err', message);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-black/70 backdrop-blur-xs">
      {/* Backdrop click closes */}
      <div className="flex-1" onClick={onClose} aria-hidden="true" />

      {/* Comic Speech-Bubble Floating Drawer */}
      <aside className="relative flex h-full w-full max-w-lg flex-col border-l-[3px] border-black bg-[#13131d] shadow-[-8px_0_0_#ff2e88]">
        {/* Header */}
        <div className="border-b-[2.5px] border-black bg-[#08080c] px-4 py-3">
          <div className="flex items-center justify-between gap-2">
            <div className="flex items-center gap-2.5">
              <div
                className="-rotate-3 flex h-9 w-9 items-center justify-center border-2 border-black text-[#08080c] shadow-[3px_3px_0_#ff2e88]"
                style={{ backgroundColor: activePersona.accent || '#ffe031' }}
              >
                <Bot className="h-5 w-5" />
              </div>
              <div>
                <div className="flex items-center gap-2">
                  <span className="font-display text-base text-[#fff6de] uppercase riot-title-rgb">
                    SHADOW SIDEKICK
                  </span>
                  <ComicSticker tone="magenta" tilt="rotate-2">
                    CTRL+K
                  </ComicSticker>
                </div>
                <div className="font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                  {activePersona.name} · {activePersona.role} ({activePersona.agentProfile})
                </div>
              </div>
            </div>

            <button
              type="button"
              onClick={onClose}
              className="riot-btn bg-[#1b1b2b] p-1.5 text-[#fff6de] hover:bg-[#ff334b]"
              aria-label="Close Shadow Sidekick"
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          {/* Persona Selector Pills */}
          <div className="mt-3 flex flex-wrap items-center gap-1.5">
            {personas.map((persona) => {
              const selected = persona.id === activePersona.id;
              return (
                <button
                  key={persona.id}
                  type="button"
                  onClick={() => setActivePersonaId(persona.id)}
                  className={`-skew-x-6 border-2 border-black px-2.5 py-1 font-mono text-[11px] font-extrabold uppercase transition-transform ${
                    selected
                      ? '-translate-y-0.5 text-[#08080c] shadow-[3px_3px_0_#000]'
                      : 'bg-[#1b1b2b] text-[#fff6de] hover:bg-[#25253a]'
                  }`}
                  style={selected ? { backgroundColor: persona.accent || '#ffe031' } : undefined}
                >
                  <span className="inline-block skew-x-6">{persona.name}</span>
                </button>
              );
            })}
          </div>

          {/* Action Bar: Daily Brief & Clear */}
          <div className="mt-3 flex items-center justify-between gap-2">
            <button
              type="button"
              disabled={briefing}
              onClick={() => void handleDailyBrief()}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-3 py-1.5 font-display text-[11px] text-[#08080c] uppercase disabled:opacity-50"
            >
              <Flame className={`h-3.5 w-3.5 ${briefing ? 'animate-pulse' : ''}`} />
              {briefing ? 'BRIEFING...' : 'DAILY BRIEF'}
            </button>

            <button
              type="button"
              onClick={() => void handleClearHistory()}
              className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-2.5 py-1.5 font-mono text-[11px] font-extrabold text-[#ff334b]"
            >
              <Trash2 className="h-3 w-3" />
              CLEAR
            </button>
          </div>

          {liveContext ? (
            <div className="mt-2.5 flex flex-wrap items-center gap-1.5 border border-black bg-[#13131d] px-2 py-1.5 font-mono text-[10px] font-bold">
              <span className="text-[#00f0ff]">{liveContext.header}</span>
              <span className="text-[#a6a2b8]">·</span>
              <span className="text-[#ffe031]">P0:{liveContext.p0TasksPending}</span>
              <span className="text-[#a3ff12]">DOCS:{liveContext.docsCount}</span>
              <span className="text-[#ff2e88]">
                9ROUTER:{formatCompactNumber(liveContext.routerTokensSaved)}
              </span>
            </div>
          ) : null}
        </div>

        <div className="riot-hazard-magenta h-1.5 border-b-2 border-black" />

        {/* Speech-Bubble Chat Feed */}
        <div className="flex-1 space-y-3 overflow-y-auto bg-[#08080c] p-4">
          {loadingHistory && messages.length === 0 ? (
            <div className="font-mono text-xs text-[#a6a2b8]">Summoning Shadow Sidekick...</div>
          ) : messages.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-3 text-center">
              <ComicSticker tone="yellow">SIDEKICK STANDING BY!</ComicSticker>
              <p className="max-w-xs font-mono text-xs text-[#a6a2b8]">
                Ask me to create tasks, complete bounties, search docs, send WhatsApp alerts, or
                hit DAILY BRIEF for a tactical sitrep.
              </p>
            </div>
          ) : (
            messages.map((msg) => {
              const isUser = msg.role === 'user';
              const toolChips = extractToolChips(msg.toolSummary);
              const personaObj = personas.find((per) => per.id === msg.persona);
              const personaAccent = personaObj?.accent || '#00f0ff';

              return (
                <div key={msg.id} className={`flex ${isUser ? 'justify-end' : 'justify-start'}`}>
                  <div
                    className={`max-w-[88%] border-2 border-black p-3 text-xs ${
                      isUser
                        ? 'bg-[#ffe031] text-[#08080c] shadow-[4px_4px_0_#000]'
                        : 'bg-[#13131d] text-[#fff6de] shadow-[4px_4px_0_#ff2e88]'
                    }`}
                  >
                    <div className="mb-1.5 flex flex-wrap items-center justify-between gap-2">
                      <span
                        className="border border-black px-1.5 py-0.5 font-mono text-[10px] font-extrabold uppercase text-[#08080c]"
                        style={{
                          backgroundColor: isUser ? '#fff6de' : personaAccent,
                        }}
                      >
                        {isUser ? 'YOU' : (personaObj?.name || msg.persona || 'SIDEKICK')}
                      </span>
                      <div className="flex items-center gap-1.5">
                        {msg.model ? <MonoBadge tone="neutral">{msg.model}</MonoBadge> : null}
                        <span
                          className={`font-mono text-[9px] font-bold ${
                            isUser ? 'text-[#08080c]/70' : 'text-[#a6a2b8]'
                          }`}
                        >
                          {formatShortTime(msg.createdAt)}
                        </span>
                      </div>
                    </div>

                    {toolChips.length > 0 ? (
                      <div className="mb-2 flex flex-wrap gap-1">
                        {toolChips.map((chipText) => (
                          <MonoBadge key={chipText} tone="ok">
                            <Wrench className="h-2.5 w-2.5" />
                            {chipText}
                          </MonoBadge>
                        ))}
                      </div>
                    ) : null}

                    <div className="whitespace-pre-wrap break-words font-sans text-xs font-medium leading-relaxed">
                      {msg.content}
                    </div>
                  </div>
                </div>
              );
            })
          )}
          {sending || briefing ? (
            <div className="inline-flex items-center gap-1.5 border-2 border-black bg-[#ff2e88] px-3 py-1 font-mono text-[11px] font-extrabold text-white shadow-[3px_3px_0_#000]">
              <Sparkles className="h-3.5 w-3.5 animate-spin" />
              {activePersona.name} EXECUTING...
            </div>
          ) : null}
          <div ref={endRef} />
        </div>

        {/* Prompt Input Bar */}
        <form
          onSubmit={handleSendChat}
          className="border-t-[2.5px] border-black bg-[#1b1b2b] p-3"
        >
          <div className="flex gap-2">
            <input
              ref={inputRef}
              type="text"
              value={promptInput}
              onChange={(ev) => setPromptInput(ev.target.value)}
              placeholder={`Command ${activePersona.name}... (e.g. "create P1 task...")`}
              className="riot-input min-w-0 flex-1 px-3 py-2 font-mono text-xs"
            />
            <button
              type="submit"
              disabled={sending}
              className="riot-btn inline-flex shrink-0 items-center gap-1.5 bg-[#a3ff12] px-4 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
            >
              {sending ? <Zap className="h-4 w-4 animate-pulse" /> : <Send className="h-4 w-4" />}
              SEND
            </button>
          </div>
        </form>
      </aside>
    </div>
  );
}
