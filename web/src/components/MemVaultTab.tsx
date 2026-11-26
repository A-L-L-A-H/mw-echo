import { useCallback, useEffect, useState } from 'react';
import {
  Brain,
  FileUp,
  Lock,
  RefreshCw,
  Search,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Upload,
} from 'lucide-react';
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  StatTile,
} from './UiPrimitives';

interface MemVaultStats {
  totalConversations: number;
  totalMessages: number;
  cloudSafeCount: number;
  bySensitivity: Record<string, number>;
}

interface MemVaultConversation {
  id: string;
  source: string;
  title: string;
  sensitivity: 'public' | 'private' | 'child_sensitive' | string;
  allowCloudLLM: boolean;
  sha256: string;
  messageCount: number;
  createdAt: string;
  importedAt: string;
}

interface MemVaultSearchHit {
  messageId: string;
  conversationId: string;
  conversationTitle: string;
  source: string;
  sensitivity: string;
  allowCloudLLM: boolean;
  role: string;
  content: string;
  snippet: string;
  createdAt: string;
}

interface MemVaultImportReport {
  importedConversations: number;
  skippedDuplicates: number;
  importedMessages: number;
  childSensitiveFlagged: number;
  conversationIds: string[];
}

export function MemVaultTab({
  onNotify,
}: {
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}) {
  const [vaultStats, setVaultStats] = useState<MemVaultStats | null>(null);
  const [conversations, setConversations] = useState<MemVaultConversation[]>([]);
  const [loadingVault, setLoadingVault] = useState(false);

  const [pastedJSON, setPastedJSON] = useState('');
  const [importing, setImporting] = useState(false);

  const [searchQuery, setSearchQuery] = useState('');
  const [cloudSafeOnly, setCloudSafeOnly] = useState(true);
  const [searchHits, setSearchHits] = useState<MemVaultSearchHit[]>([]);
  const [searching, setSearching] = useState(false);
  const [updatingId, setUpdatingId] = useState<string | null>(null);

  const loadVault = useCallback(async () => {
    setLoadingVault(true);
    try {
      const [statsResp, convResp] = await Promise.all([
        fetch('/api/memvault/stats'),
        fetch('/api/memvault/conversations?limit=100'),
      ]);
      if (statsResp.ok) {
        const statsJson = (await statsResp.json()) as MemVaultStats;
        setVaultStats(statsJson);
      }
      if (convResp.ok) {
        const convJson = (await convResp.json()) as { conversations?: MemVaultConversation[] };
        setConversations(convJson.conversations ?? []);
      }
    } catch (caughtErr) {
      onNotify('err', caughtErr instanceof Error ? caughtErr.message : 'Failed to load MemVault');
    } finally {
      setLoadingVault(false);
    }
  }, [onNotify]);

  useEffect(() => {
    void loadVault();
  }, [loadVault]);

  const handleImportFile = async (selectedFile: File) => {
    setImporting(true);
    try {
      const formBody = new FormData();
      formBody.append('file', selectedFile);
      const importResp = await fetch('/api/memvault/import', {
        method: 'POST',
        body: formBody,
      });
      if (!importResp.ok) {
        const errPayload = (await importResp.json().catch(() => ({}))) as { error?: string };
        throw new Error(errPayload.error || `Import failed (HTTP ${importResp.status})`);
      }
      const report = (await importResp.json()) as MemVaultImportReport;
      onNotify(
        'ok',
        `Imported ${report.importedConversations} convs (${report.importedMessages} msgs, ${report.childSensitiveFlagged} child-flagged)`,
      );
      await loadVault();
    } catch (caughtErr) {
      onNotify('err', caughtErr instanceof Error ? caughtErr.message : 'Import failed');
    } finally {
      setImporting(false);
    }
  };

  const handleImportPasted = async () => {
    if (!pastedJSON.trim()) {
      onNotify('info', 'Paste ChatGPT conversations.json first');
      return;
    }
    setImporting(true);
    try {
      const importResp = await fetch('/api/memvault/import', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: pastedJSON,
      });
      if (!importResp.ok) {
        const errPayload = (await importResp.json().catch(() => ({}))) as { error?: string };
        throw new Error(errPayload.error || `Import failed (HTTP ${importResp.status})`);
      }
      const report = (await importResp.json()) as MemVaultImportReport;
      setPastedJSON('');
      onNotify(
        'ok',
        `Imported ${report.importedConversations} convs (${report.importedMessages} msgs, ${report.skippedDuplicates} dupes skipped)`,
      );
      await loadVault();
    } catch (caughtErr) {
      onNotify('err', caughtErr instanceof Error ? caughtErr.message : 'Import failed');
    } finally {
      setImporting(false);
    }
  };

  const handleSearch = async () => {
    const trimmed = searchQuery.trim();
    if (!trimmed) {
      setSearchHits([]);
      return;
    }
    setSearching(true);
    try {
      const params = new URLSearchParams({
        q: trimmed,
        cloudSafeOnly: String(cloudSafeOnly),
        limit: '40',
      });
      const searchResp = await fetch(`/api/memvault/search?${params.toString()}`);
      if (!searchResp.ok) {
        const errPayload = (await searchResp.json().catch(() => ({}))) as { error?: string };
        throw new Error(errPayload.error || `Search failed (HTTP ${searchResp.status})`);
      }
      const searchJson = (await searchResp.json()) as { hits?: MemVaultSearchHit[] };
      setSearchHits(searchJson.hits ?? []);
    } catch (caughtErr) {
      onNotify('err', caughtErr instanceof Error ? caughtErr.message : 'Search failed');
    } finally {
      setSearching(false);
    }
  };

  const handleSensitivityChange = async (
    conv: MemVaultConversation,
    nextSensitivity: 'public' | 'private' | 'child_sensitive',
  ) => {
    setUpdatingId(conv.id);
    const nextAllowCloud = nextSensitivity === 'public';
    try {
      const patchResp = await fetch(`/api/memvault/conversations/${encodeURIComponent(conv.id)}/privacy`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          sensitivity: nextSensitivity,
          allowCloudLLM: nextAllowCloud,
        }),
      });
      if (!patchResp.ok) {
        const errPayload = (await patchResp.json().catch(() => ({}))) as { error?: string };
        throw new Error(errPayload.error || `Privacy update failed (HTTP ${patchResp.status})`);
      }
      onNotify('ok', `Updated "${conv.title}" -> ${nextSensitivity.toUpperCase()}`);
      await loadVault();
    } catch (caughtErr) {
      onNotify('err', caughtErr instanceof Error ? caughtErr.message : 'Privacy update failed');
    } finally {
      setUpdatingId(null);
    }
  };

  const sensitivityTone = (level: string): 'ok' | 'yellow' | 'err' => {
    if (level === 'public') return 'ok';
    if (level === 'child_sensitive') return 'err';
    return 'yellow';
  };

  return (
    <div className="space-y-5">
      <StageHeroBanner
        sceneCode="SCENE 18 // MEMVAULT"
        sticker="LOCAL-FIRST MEMORY"
        title="MEMORY VAULT & PRIVACY FIREWALL"
        subtitle="Import ChatGPT conversations.json/.zip, run FTS5 recall, and enforce cloud LLM privacy boundaries."
        tone="cyan"
        actions={
          <button
            type="button"
            onClick={() => void loadVault()}
            disabled={loadingVault}
            className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c] uppercase hover:bg-[#00f0ff]"
          >
            <RefreshCw className={`h-3.5 w-3.5 ${loadingVault ? 'animate-spin' : ''}`} />
            REFRESH
          </button>
        }
      />

      <div className="grid grid-cols-2 gap-3.5 lg:grid-cols-4">
        <StatTile
          label="CONVERSATIONS"
          value={vaultStats?.totalConversations ?? conversations.length}
          sublabel="INDEXED IN SQLITE"
          icon={<Brain className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="MESSAGES"
          value={vaultStats?.totalMessages ?? 0}
          sublabel="FTS5 FULL-TEXT"
          icon={<Sparkles className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="CLOUD SAFE"
          value={vaultStats?.cloudSafeCount ?? 0}
          sublabel="PUBLIC / CLOUD OK"
          icon={<ShieldCheck className="h-4 w-4" />}
          tone="ok"
        />
        <StatTile
          label="CHILD SENSITIVE"
          value={vaultStats?.bySensitivity?.child_sensitive ?? 0}
          sublabel="HARD FIREWALL LOCK"
          icon={<ShieldAlert className="h-4 w-4" />}
          tone="err"
        />
      </div>

      <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
        <SectionCard
          title="IMPORT CHATGPT ARCHIVE"
          subtitle="Upload conversations.json / export .zip or paste raw JSON"
          icon={<Upload className="h-4 w-4" />}
          sticker="ZERO CLOUD LEAK"
          stickerTone="ok"
          accent="cyan"
        >
          <div className="space-y-3">
            <label className="flex cursor-pointer items-center justify-between gap-3 border-2 border-dashed border-[#00f0ff] bg-[#08080c] px-4 py-3 font-mono text-xs font-bold text-[#fff6de] hover:bg-[#1b1b2b]">
              <span className="inline-flex items-center gap-2">
                <FileUp className="h-4 w-4 text-[#00f0ff]" />
                {importing ? 'IMPORTING...' : 'CHOOSE .JSON OR .ZIP EXPORT'}
              </span>
              <MonoBadge tone="cyan">FILE</MonoBadge>
              <input
                type="file"
                accept=".json,.zip,application/json,application/zip"
                className="hidden"
                disabled={importing}
                onChange={(ev) => {
                  const chosen = ev.target.files?.[0];
                  if (chosen) {
                    void handleImportFile(chosen);
                    ev.target.value = '';
                  }
                }}
              />
            </label>

            <textarea
              value={pastedJSON}
              onChange={(ev) => setPastedJSON(ev.target.value)}
              rows={5}
              placeholder='Paste ChatGPT conversations.json array here: [{"title":"...","mapping":{...}}]'
              className="w-full border-2 border-black bg-[#08080c] p-3 font-mono text-xs text-[#fff6de] placeholder:text-[#6e6a86] focus:border-[#ffe031] focus:outline-none"
            />

            <button
              type="button"
              onClick={() => void handleImportPasted()}
              disabled={importing}
              className="riot-btn inline-flex items-center gap-2 bg-[#00f0ff] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c] uppercase hover:bg-[#ffe031]"
            >
              <Upload className="h-3.5 w-3.5" />
              {importing ? 'IMPORTING...' : 'IMPORT PASTED JSON'}
            </button>
          </div>
        </SectionCard>

        <SectionCard
          title="FTS5 MEMORY SEARCH"
          subtitle="Instant full-text recall across all imported conversations"
          icon={<Search className="h-4 w-4" />}
          accent="yellow"
          actions={
            <button
              type="button"
              onClick={() => setCloudSafeOnly((prev) => !prev)}
              className={`inline-flex items-center gap-1.5 border-2 border-black px-2.5 py-1 font-mono text-[11px] font-extrabold uppercase shadow-[2px_2px_0_#000] ${
                cloudSafeOnly
                  ? 'bg-[#a3ff12] text-[#08080c]'
                  : 'bg-[#ff334b] text-white'
              }`}
            >
              {cloudSafeOnly ? (
                <ShieldCheck className="h-3.5 w-3.5" />
              ) : (
                <Lock className="h-3.5 w-3.5" />
              )}
              CLOUD PRIVACY FIREWALL: {cloudSafeOnly ? 'ON' : 'OFF (LOCAL ALL)'}
            </button>
          }
        >
          <div className="space-y-3">
            <div className="flex gap-2">
              <input
                type="text"
                value={searchQuery}
                onChange={(ev) => setSearchQuery(ev.target.value)}
                onKeyDown={(ev) => {
                  if (ev.key === 'Enter') {
                    void handleSearch();
                  }
                }}
                placeholder="Search memories (FTS5)..."
                className="flex-1 border-2 border-black bg-[#08080c] px-3 py-2 font-mono text-xs text-[#fff6de] placeholder:text-[#6e6a86] focus:border-[#ffe031] focus:outline-none"
              />
              <button
                type="button"
                onClick={() => void handleSearch()}
                disabled={searching}
                className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c] uppercase hover:bg-[#a3ff12]"
              >
                <Search className="h-3.5 w-3.5" />
                {searching ? 'SCAN...' : 'SEARCH'}
              </button>
            </div>

            {searchHits.length === 0 ? (
              <div className="border-2 border-black bg-[#08080c] p-4 font-mono text-xs text-[#a6a2b8]">
                {searchQuery.trim()
                  ? 'No matching memories under current firewall filter.'
                  : 'Enter a query to search imported memories.'}
              </div>
            ) : (
              <div className="max-h-72 space-y-2 overflow-y-auto pr-1">
                {searchHits.map((hit) => (
                  <div
                    key={hit.messageId}
                    className="border-2 border-black bg-[#08080c] p-3 shadow-[3px_3px_0_#000]"
                  >
                    <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
                      <span className="font-mono text-xs font-extrabold text-[#ffe031]">
                        {hit.conversationTitle || hit.conversationId}
                      </span>
                      <div className="flex items-center gap-1.5">
                        <MonoBadge tone="neutral">{hit.role}</MonoBadge>
                        <MonoBadge tone={sensitivityTone(hit.sensitivity)}>
                          {hit.sensitivity}
                        </MonoBadge>
                      </div>
                    </div>
                    <p className="font-mono text-xs leading-relaxed text-[#fff6de]">
                      {hit.snippet || hit.content.slice(0, 220)}
                    </p>
                  </div>
                ))}
              </div>
            )}
          </div>
        </SectionCard>
      </div>

      <SectionCard
        title="CONVERSATIONS & PRIVACY CONTROLS"
        subtitle="Switch sensitivity between public, private, and child_sensitive"
        icon={<Brain className="h-4 w-4" />}
        sticker={`${conversations.length} STORED`}
        stickerTone="magenta"
        accent="magenta"
      >
        {conversations.length === 0 ? (
          <div className="border-2 border-black bg-[#08080c] p-5 text-center font-mono text-xs text-[#a6a2b8]">
            No conversations imported yet. Upload a ChatGPT export above to populate Stage 18 MEMORY.
          </div>
        ) : (
          <div className="space-y-2.5">
            {conversations.map((conv) => (
              <div
                key={conv.id}
                className="flex flex-wrap items-center justify-between gap-3 border-2 border-black bg-[#08080c] px-3.5 py-2.5 shadow-[3px_3px_0_#000]"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate font-mono text-xs font-extrabold text-[#fff6de]">
                      {conv.title || 'Untitled Conversation'}
                    </span>
                    <MonoBadge tone={sensitivityTone(conv.sensitivity)}>
                      {conv.sensitivity}
                    </MonoBadge>
                    {conv.allowCloudLLM ? (
                      <ComicSticker tone="ok" tilt="rotate-0">
                        CLOUD OK
                      </ComicSticker>
                    ) : (
                      <MonoBadge tone="neutral">LOCAL ONLY</MonoBadge>
                    )}
                  </div>
                  <div className="mt-1 font-mono text-[10px] text-[#a6a2b8] uppercase">
                    {conv.source} · {conv.messageCount} msgs · {conv.id}
                  </div>
                </div>

                <div className="flex items-center gap-1.5">
                  {(['public', 'private', 'child_sensitive'] as const).map((level) => {
                    const isCurrent = conv.sensitivity === level;
                    return (
                      <button
                        key={level}
                        type="button"
                        disabled={updatingId === conv.id || isCurrent}
                        onClick={() => void handleSensitivityChange(conv, level)}
                        className={`border-2 border-black px-2 py-1 font-mono text-[10px] font-extrabold uppercase transition-colors ${
                          isCurrent
                            ? 'bg-[#ffe031] text-[#08080c]'
                            : 'bg-[#1b1b2b] text-[#fff6de] hover:bg-[#00f0ff] hover:text-[#08080c]'
                        }`}
                      >
                        {level === 'child_sensitive' ? 'CHILD' : level}
                      </button>
                    );
                  })}
                </div>
              </div>
            ))}
          </div>
        )}
      </SectionCard>
    </div>
  );
}
