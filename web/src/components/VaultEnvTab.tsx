import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Check,
  CloudUpload,
  Copy,
  Download,
  Eye,
  EyeOff,
  FileCode2,
  FolderLock,
  History,
  KeyRound,
  Lock,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
  Upload,
  X,
} from 'lucide-react';
import {
  backupVaultEnvToPool,
  deleteVaultEnvSecret,
  exportVaultEnvDotEnv,
  fetchVaultEnvAudit,
  fetchVaultEnvKeys,
  importVaultEnvDotEnv,
  putVaultEnvSecret,
  revealVaultEnvSecret,
  type ProjectSummary,
  type VaultAuditLogRecord,
  type VaultBackupSnapshotResult,
  type VaultEnvName,
  type VaultExportResult,
  type VaultSecretKeyMeta,
} from '../api';
import { copyTextToClipboard, formatBytes, formatShortTime } from '../utils';
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  StatTile,
  type BadgeTone,
} from './UiPrimitives';

interface VaultEnvTabProps {
  projects: ProjectSummary[];
  onReloadStorage?: () => Promise<void>;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

const VAULT_ENVIRONMENTS: Array<{ id: VaultEnvName; label: string; tone: BadgeTone }> = [
  { id: 'dev', label: 'DEV', tone: 'cyan' },
  { id: 'staging', label: 'STAGING', tone: 'yellow' },
  { id: 'prod', label: 'PROD', tone: 'magenta' },
  { id: 'shared', label: 'SHARED', tone: 'ok' },
];

const KEY_NAME_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;

function resolveAuditTone(action: string): BadgeTone {
  const lowered = (action || '').toLowerCase();
  if (lowered === 'reveal' || lowered === 'export') return 'yellow';
  if (lowered === 'delete') return 'err';
  if (lowered === 'backup') return 'magenta';
  if (lowered === 'import' || lowered === 'set') return 'ok';
  return 'neutral';
}

export function VaultEnvTab({ projects, onReloadStorage, onNotify }: VaultEnvTabProps) {
  const projectSlugs = useMemo(() => {
    const slugSet = new Set<string>(['moonwitness', 'mw-echo']);
    for (const proj of projects) {
      if (proj.slug?.trim()) {
        slugSet.add(proj.slug.trim());
      }
    }
    return Array.from(slugSet);
  }, [projects]);

  const [selectedProject, setSelectedProject] = useState<string>(
    projects[0]?.slug || 'moonwitness',
  );
  const [customProjectInput, setCustomProjectInput] = useState<string>('');
  const [activeEnv, setActiveEnv] = useState<VaultEnvName>('dev');

  const [keys, setKeys] = useState<VaultSecretKeyMeta[]>([]);
  const [auditLogs, setAuditLogs] = useState<VaultAuditLogRecord[]>([]);
  const [loadingVault, setLoadingVault] = useState(false);

  // Single secret form
  const [keyNameInput, setKeyNameInput] = useState('');
  const [secretValueInput, setSecretValueInput] = useState('');
  const [showSecretInput, setShowSecretInput] = useState(false);
  const [noteInput, setNoteInput] = useState('');
  const [savingSecret, setSavingSecret] = useState(false);

  // Bulk .env importer
  const [bulkDotEnvText, setBulkDotEnvText] = useState('');
  const [importingBulk, setImportingBulk] = useState(false);

  // Revealed plaintext cache (cleared when switching project/env)
  const [revealedByKey, setRevealedByKey] = useState<Record<string, string>>({});
  const [revealingKey, setRevealingKey] = useState<string | null>(null);
  const [deletingKey, setDeletingKey] = useState<string | null>(null);
  const [copiedKeyName, setCopiedKeyName] = useState<string | null>(null);

  // Export modal & 9Drive snapshot
  const [exportingEnv, setExportingEnv] = useState(false);
  const [exportModalData, setExportModalData] = useState<VaultExportResult | null>(null);
  const [copiedExport, setCopiedExport] = useState(false);
  const [backingUpPool, setBackingUpPool] = useState(false);
  const [lastBackupResult, setLastBackupResult] = useState<VaultBackupSnapshotResult | null>(null);

  const loadVaultData = useCallback(async () => {
    const targetProject = selectedProject.trim() || 'moonwitness';
    setLoadingVault(true);
    try {
      const [keysOutcome, auditOutcome] = await Promise.allSettled([
        fetchVaultEnvKeys(targetProject, activeEnv),
        fetchVaultEnvAudit(targetProject, 50),
      ]);
      if (keysOutcome.status === 'fulfilled') {
        setKeys(keysOutcome.value.keys ?? []);
      }
      if (auditOutcome.status === 'fulfilled') {
        setAuditLogs(auditOutcome.value.logs ?? []);
      }
    } finally {
      setLoadingVault(false);
    }
  }, [activeEnv, selectedProject]);

  useEffect(() => {
    setRevealedByKey({});
    void loadVaultData();
  }, [loadVaultData]);

  useEffect(() => {
    if (!exportModalData) return;
    const handleEsc = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') {
        setExportModalData(null);
      }
    };
    window.addEventListener('keydown', handleEsc);
    return () => window.removeEventListener('keydown', handleEsc);
  }, [exportModalData]);

  const handleApplyCustomProject = (ev: React.FormEvent) => {
    ev.preventDefault();
    const cleaned = customProjectInput.trim().toLowerCase().replace(/[^a-z0-9-_]+/g, '-');
    if (!cleaned) {
      onNotify('err', 'Enter a valid custom project slug.');
      return;
    }
    setSelectedProject(cleaned);
    setCustomProjectInput('');
    onNotify('info', `Switched VaultEnv project to "${cleaned}".`);
  };

  const handlePutSecret = async (ev: React.FormEvent) => {
    ev.preventDefault();
    const cleanKey = keyNameInput.trim();
    if (!KEY_NAME_PATTERN.test(cleanKey)) {
      onNotify('err', 'KEY_NAME must match ^[A-Za-z_][A-Za-z0-9_]*$.');
      return;
    }
    if (!secretValueInput) {
      onNotify('err', 'Secret value cannot be empty.');
      return;
    }
    setSavingSecret(true);
    try {
      await putVaultEnvSecret({
        project: selectedProject,
        envName: activeEnv,
        keyName: cleanKey,
        value: secretValueInput,
        note: noteInput.trim() || undefined,
      });
      setKeyNameInput('');
      setSecretValueInput('');
      setNoteInput('');
      setRevealedByKey((prev) => {
        const nextMap = { ...prev };
        delete nextMap[cleanKey];
        return nextMap;
      });
      onNotify('ok', `Encrypted "${cleanKey}" into ${selectedProject}/${activeEnv}.`);
      await loadVaultData();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to store secret';
      onNotify('err', message);
    } finally {
      setSavingSecret(false);
    }
  };

  const handleBulkImport = async (ev: React.FormEvent) => {
    ev.preventDefault();
    if (!bulkDotEnvText.trim()) {
      onNotify('err', 'Paste one or more KEY=VALUE lines to import.');
      return;
    }
    setImportingBulk(true);
    try {
      const summary = await importVaultEnvDotEnv({
        project: selectedProject,
        envName: activeEnv,
        rawDotEnv: bulkDotEnvText,
      });
      setBulkDotEnvText('');
      onNotify(
        'ok',
        `Imported ${summary.importedCount} key(s) (${summary.updatedCount} updated, ${summary.skippedCount} skipped) into ${selectedProject}/${activeEnv}.`,
      );
      await loadVaultData();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Bulk .env import failed';
      onNotify('err', message);
    } finally {
      setImportingBulk(false);
    }
  };

  const handleToggleReveal = async (keyMeta: VaultSecretKeyMeta) => {
    if (revealedByKey[keyMeta.keyName] !== undefined) {
      setRevealedByKey((prev) => {
        const nextMap = { ...prev };
        delete nextMap[keyMeta.keyName];
        return nextMap;
      });
      return;
    }
    setRevealingKey(keyMeta.keyName);
    try {
      const revealed = await revealVaultEnvSecret({
        project: selectedProject,
        envName: activeEnv,
        keyName: keyMeta.keyName,
      });
      setRevealedByKey((prev) => ({ ...prev, [keyMeta.keyName]: revealed.value }));
      await loadVaultData();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to reveal secret';
      onNotify('err', message);
    } finally {
      setRevealingKey(null);
    }
  };

  const handleCopyRevealed = async (keyName: string, plaintext: string) => {
    const copied = await copyTextToClipboard(plaintext);
    if (copied) {
      setCopiedKeyName(keyName);
      onNotify('ok', `Copied plaintext value for ${keyName}.`);
      window.setTimeout(() => {
        setCopiedKeyName((prev) => (prev === keyName ? null : prev));
      }, 1800);
    }
  };

  const handleDeleteSecret = async (keyMeta: VaultSecretKeyMeta) => {
    setDeletingKey(keyMeta.keyName);
    try {
      await deleteVaultEnvSecret(selectedProject, activeEnv, keyMeta.keyName);
      setRevealedByKey((prev) => {
        const nextMap = { ...prev };
        delete nextMap[keyMeta.keyName];
        return nextMap;
      });
      onNotify('ok', `Purged "${keyMeta.keyName}" from ${selectedProject}/${activeEnv}.`);
      await loadVaultData();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to delete secret';
      onNotify('err', message);
    } finally {
      setDeletingKey(null);
    }
  };

  const handleExportDotEnv = async () => {
    setExportingEnv(true);
    try {
      const exported = await exportVaultEnvDotEnv(selectedProject, activeEnv);
      setExportModalData(exported);
      onNotify(
        'ok',
        `Exported ${exported.keyCount} key(s) from ${exported.project}/${exported.envName}.`,
      );
      await loadVaultData();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to export .env';
      onNotify('err', message);
    } finally {
      setExportingEnv(false);
    }
  };

  const handleCopyExportDotEnv = async () => {
    if (!exportModalData) return;
    const copied = await copyTextToClipboard(exportModalData.dotenv);
    if (copied) {
      setCopiedExport(true);
      onNotify('ok', `Copied .env bundle for ${exportModalData.project}/${exportModalData.envName}.`);
      window.setTimeout(() => setCopiedExport(false), 1800);
    }
  };

  const handleDownloadExportDotEnv = () => {
    if (!exportModalData) return;
    const blob = new Blob([exportModalData.dotenv], { type: 'text/plain;charset=utf-8' });
    const objectUrl = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = objectUrl;
    anchor.download = `${exportModalData.project}.${exportModalData.envName}.env`;
    document.body.appendChild(anchor);
    anchor.click();
    document.body.removeChild(anchor);
    URL.revokeObjectURL(objectUrl);
  };

  const handleBackupToPool = async () => {
    setBackingUpPool(true);
    try {
      const snapshot = await backupVaultEnvToPool(selectedProject, activeEnv);
      setLastBackupResult(snapshot);
      onNotify(
        'ok',
        `Encrypted snapshot uploaded to 9Drive (${snapshot.keyCount} keys · ${formatBytes(snapshot.encryptedBytes)}).`,
      );
      await loadVaultData();
      if (onReloadStorage) {
        await onReloadStorage();
      }
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Encrypted 9Drive snapshot failed';
      onNotify('err', message);
    } finally {
      setBackingUpPool(false);
    }
  };

  const activeEnvMeta =
    VAULT_ENVIRONMENTS.find((envEntry) => envEntry.id === activeEnv) ?? VAULT_ENVIRONMENTS[0];

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 15"
        sticker="VAULTENV // AES-256-GCM"
        title="SECRETS // ENCRYPTED .ENV & KEY SYNC VAULT"
        subtitle="PROJECT-ISOLATED ENVIRONMENT SECRETS · ZERO PLAINTEXT AT REST · MASKED KEY LEDGER · BULK .ENV IMPORT/EXPORT & ENCRYPTED 9DRIVE SNAPSHOTS"
        tone="magenta"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={exportingEnv}
              onClick={() => void handleExportDotEnv()}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#00f0ff] px-3.5 py-2 font-mono text-xs font-extrabold text-[#08080c] uppercase disabled:opacity-50"
            >
              <Download className="h-3.5 w-3.5" />
              {exportingEnv ? 'EXPORTING...' : 'EXPORT .ENV'}
            </button>
            <button
              type="button"
              disabled={backingUpPool}
              onClick={() => void handleBackupToPool()}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ff2e88] px-3.5 py-2 font-display text-xs text-white uppercase hover:bg-[#ffe031] hover:text-[#08080c] disabled:opacity-50"
            >
              <CloudUpload className="h-3.5 w-3.5" />
              {backingUpPool ? 'ENCRYPTING...' : 'ENCRYPTED 9DRIVE SNAPSHOT'}
            </button>
            <button
              type="button"
              onClick={() => void loadVaultData()}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#1b1b2b] px-3 py-2 font-mono text-xs font-extrabold text-[#fff6de]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingVault ? 'animate-spin' : ''}`} />
              SYNC
            </button>
          </div>
        }
      />

      {/* Vault Telemetry Tiles */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="VAULTED KEYS"
          value={keys.length}
          sublabel={`${selectedProject} · ${activeEnv.toUpperCase()}`}
          icon={<KeyRound className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="ACTIVE PROJECT"
          value={selectedProject.toUpperCase()}
          sublabel={`${projectSlugs.length} known project namespaces`}
          icon={<FolderLock className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="ENVIRONMENT"
          value={activeEnv.toUpperCase()}
          sublabel="AES-256-GCM SecretBox encrypted"
          icon={<ShieldCheck className="h-4 w-4" />}
          tone={activeEnvMeta.tone}
        />
        <StatTile
          label="AUDIT EVENTS"
          value={auditLogs.length}
          sublabel="Reveal, export, import & backup log"
          icon={<History className="h-4 w-4" />}
          tone="magenta"
        />
      </div>

      {/* Project Namespace & Environment Selector */}
      <SectionCard
        title="NAMESPACE & ENVIRONMENT MATRIX"
        subtitle="Select a project namespace (or type a custom slug) and switch between dev, staging, prod, and shared environments."
        icon={<FolderLock className="h-4 w-4" />}
        sticker={`${selectedProject} / ${activeEnv.toUpperCase()}`}
        stickerTone={activeEnvMeta.tone}
        accent="yellow"
      >
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
          {/* Project dropdown + custom slug input */}
          <div className="space-y-2.5 lg:col-span-6">
            <label className="block font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
              Project Namespace
            </label>
            <div className="flex flex-wrap gap-2">
              <select
                value={projectSlugs.includes(selectedProject) ? selectedProject : '__custom__'}
                onChange={(ev) => {
                  if (ev.target.value !== '__custom__') {
                    setSelectedProject(ev.target.value);
                  }
                }}
                className="riot-input min-w-[180px] flex-1 px-3 py-2 font-mono text-xs font-bold text-[#ffe031]"
              >
                {projectSlugs.map((slugName) => (
                  <option key={slugName} value={slugName}>
                    {slugName}
                  </option>
                ))}
                {!projectSlugs.includes(selectedProject) ? (
                  <option value="__custom__">{selectedProject} (custom)</option>
                ) : null}
              </select>

              <form onSubmit={handleApplyCustomProject} className="flex flex-1 gap-2">
                <input
                  type="text"
                  value={customProjectInput}
                  onChange={(ev) => setCustomProjectInput(ev.target.value)}
                  placeholder="Custom slug (e.g. my-app)"
                  className="riot-input min-w-[140px] flex-1 px-3 py-2 font-mono text-xs"
                />
                <button
                  type="submit"
                  className="riot-btn shrink-0 bg-[#1b1b2b] px-3 py-2 font-mono text-xs font-extrabold text-[#00f0ff] uppercase"
                >
                  USE
                </button>
              </form>
            </div>
          </div>

          {/* Environment tabs */}
          <div className="space-y-2.5 lg:col-span-6">
            <label className="block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
              Target Environment
            </label>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {VAULT_ENVIRONMENTS.map((envItem) => {
                const isActive = activeEnv === envItem.id;
                return (
                  <button
                    key={envItem.id}
                    type="button"
                    onClick={() => setActiveEnv(envItem.id)}
                    className={`border-[2.5px] border-black px-3 py-2 font-display text-xs uppercase transition-all ${
                      isActive
                        ? '-translate-y-0.5 bg-[#ffe031] text-[#08080c] shadow-[4px_4px_0_#ff2e88]'
                        : 'bg-[#08080c] text-[#fff6de] shadow-[3px_3px_0_#000] hover:bg-[#1b1b2b] hover:text-[#00f0ff]'
                    }`}
                  >
                    {envItem.label}
                  </button>
                );
              })}
            </div>
          </div>
        </div>

        {lastBackupResult ? (
          <div className="mt-4 flex flex-wrap items-center justify-between gap-2 border-2 border-[#a3ff12] bg-[#08080c] px-3.5 py-2.5 shadow-[4px_4px_0_#a3ff12]">
            <div className="flex flex-wrap items-center gap-2">
              <ComicSticker tone="ok">9DRIVE SNAPSHOT LOCKED</ComicSticker>
              <span className="font-mono text-xs font-bold text-[#fff6de]">
                {lastBackupResult.logicalPath}
              </span>
              <MonoBadge tone="cyan">{lastBackupResult.keyCount} KEYS</MonoBadge>
              <MonoBadge tone="yellow">{formatBytes(lastBackupResult.encryptedBytes)}</MonoBadge>
            </div>
            <button
              type="button"
              onClick={() => setLastBackupResult(null)}
              className="text-[#a6a2b8] hover:text-[#ffe031]"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
        ) : null}
      </SectionCard>

      {/* Single Secret Injection & Bulk .env Importer */}
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-12">
        {/* Single Secret Form */}
        <SectionCard
          title="INJECT SINGLE SECRET"
          subtitle="Encrypt a single KEY_NAME -> value pair into the active project/env."
          icon={<Lock className="h-4 w-4" />}
          sticker="PUT /SECRET"
          stickerTone="ok"
          accent="ok"
          className="xl:col-span-5"
        >
          <form onSubmit={handlePutSecret} className="space-y-3">
            <div>
              <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                Key Name
              </label>
              <input
                type="text"
                value={keyNameInput}
                onChange={(ev) => setKeyNameInput(ev.target.value.toUpperCase())}
                placeholder="e.g. OPENAI_API_KEY"
                className="riot-input w-full px-3 py-2 font-mono text-xs font-bold"
              />
            </div>

            <div>
              <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                Secret Value (Masked by Default)
              </label>
              <div className="flex gap-2">
                <input
                  type={showSecretInput ? 'text' : 'password'}
                  value={secretValueInput}
                  onChange={(ev) => setSecretValueInput(ev.target.value)}
                  placeholder="sk-live-..."
                  className="riot-input min-w-0 flex-1 px-3 py-2 font-mono text-xs"
                />
                <button
                  type="button"
                  onClick={() => setShowSecretInput((prev) => !prev)}
                  className="riot-btn shrink-0 bg-[#1b1b2b] px-2.5 py-2 text-[#fff6de]"
                  title={showSecretInput ? 'Hide secret' : 'Show secret'}
                >
                  {showSecretInput ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                </button>
              </div>
            </div>

            <div>
              <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                Note (Optional)
              </label>
              <input
                type="text"
                value={noteInput}
                onChange={(ev) => setNoteInput(ev.target.value)}
                placeholder="Rotation schedule or service owner"
                className="riot-input w-full px-3 py-2 font-mono text-xs"
              />
            </div>

            <button
              type="submit"
              disabled={savingSecret}
              className="riot-btn inline-flex w-full items-center justify-center gap-1.5 bg-[#a3ff12] px-4 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
            >
              <Plus className="h-4 w-4" />
              {savingSecret ? 'ENCRYPTING...' : `LOCK INTO ${activeEnv.toUpperCase()}`}
            </button>
          </form>
        </SectionCard>

        {/* Bulk .env Importer */}
        <SectionCard
          title="BULK .ENV IMPORTER"
          subtitle="Paste raw .env contents (# comments, export KEY=val, quoted strings supported)."
          icon={<Upload className="h-4 w-4" />}
          sticker="BULK SYNC"
          stickerTone="cyan"
          accent="cyan"
          className="xl:col-span-7"
        >
          <form onSubmit={handleBulkImport} className="space-y-3">
            <textarea
              rows={7}
              value={bulkDotEnvText}
              onChange={(ev) => setBulkDotEnvText(ev.target.value)}
              placeholder={`# Paste .env entries for ${selectedProject} (${activeEnv})\nDATABASE_URL="postgres://user:pass@localhost:5432/app"\nexport REDIS_URL=redis://localhost:6379/0\nJWT_SECRET=super-secret-value`}
              className="riot-input w-full p-3 font-mono text-xs leading-relaxed"
            />
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                TARGET: {selectedProject} / {activeEnv.toUpperCase()}
              </span>
              <button
                type="submit"
                disabled={importingBulk}
                className="riot-btn inline-flex items-center gap-1.5 bg-[#00f0ff] px-4 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
              >
                <Upload className="h-3.5 w-3.5" />
                {importingBulk ? 'PARSING & ENCRYPTING...' : 'IMPORT .ENV PAYLOAD'}
              </button>
            </div>
          </form>
        </SectionCard>
      </div>

      {/* Masked Keys Table */}
      <SectionCard
        title="MASKED KEYS LEDGER"
        subtitle="Plaintext values are never transmitted on list. Use REVEAL for audited single-key decryption."
        icon={<KeyRound className="h-4 w-4" />}
        sticker={`${keys.length} KEYS`}
        stickerTone="yellow"
        accent="magenta"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={exportingEnv}
              onClick={() => void handleExportDotEnv()}
              className="riot-btn inline-flex items-center gap-1 bg-[#ffe031] px-3 py-1.5 font-mono text-[11px] font-extrabold text-[#08080c] uppercase disabled:opacity-50"
            >
              <FileCode2 className="h-3.5 w-3.5" />
              EXPORT .ENV
            </button>
            <button
              type="button"
              disabled={backingUpPool}
              onClick={() => void handleBackupToPool()}
              className="riot-btn inline-flex items-center gap-1 bg-[#ff2e88] px-3 py-1.5 font-mono text-[11px] font-extrabold text-white uppercase disabled:opacity-50"
            >
              <CloudUpload className="h-3.5 w-3.5" />
              9DRIVE SNAPSHOT
            </button>
          </div>
        }
      >
        {keys.length === 0 ? (
          <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] py-10 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
            NO SECRETS IN {selectedProject.toUpperCase()} / {activeEnv.toUpperCase()}. INJECT ONE ABOVE OR IMPORT A .ENV FILE.
          </div>
        ) : (
          <div className="overflow-x-auto border-2 border-black bg-[#08080c]">
            <table className="w-full border-collapse text-left font-mono text-xs">
              <thead>
                <tr className="border-b-2 border-black bg-[#1b1b2b] text-[10px] font-extrabold uppercase text-[#ffe031]">
                  <th className="px-3 py-2.5">KEY NAME</th>
                  <th className="px-3 py-2.5">VALUE / PREVIEW</th>
                  <th className="px-3 py-2.5">NOTE</th>
                  <th className="px-3 py-2.5">UPDATED</th>
                  <th className="px-3 py-2.5 text-right">ACTIONS</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[#252538]">
                {keys.map((keyMeta) => {
                  const revealedValue = revealedByKey[keyMeta.keyName];
                  const isRevealed = revealedValue !== undefined;
                  const isRevealing = revealingKey === keyMeta.keyName;
                  const isDeleting = deletingKey === keyMeta.keyName;
                  const isCopied = copiedKeyName === keyMeta.keyName;

                  return (
                    <tr key={keyMeta.id || keyMeta.keyName} className="hover:bg-[#13131d]">
                      <td className="px-3 py-2.5 font-extrabold text-[#fff6de]">
                        <div className="flex items-center gap-1.5">
                          <span>{keyMeta.keyName}</span>
                          <MonoBadge tone={activeEnvMeta.tone}>{keyMeta.envName}</MonoBadge>
                        </div>
                      </td>
                      <td className="max-w-xs px-3 py-2.5">
                        {isRevealed ? (
                          <code className="block break-all border border-[#a3ff12] bg-[#13131d] px-2 py-1 text-[11px] font-bold text-[#a3ff12]">
                            {revealedValue}
                          </code>
                        ) : (
                          <span className="text-[#00f0ff]">
                            {keyMeta.maskedPreview || '••••••••'}
                          </span>
                        )}
                      </td>
                      <td className="px-3 py-2.5 text-[11px] text-[#a6a2b8]">
                        {keyMeta.note || '—'}
                      </td>
                      <td className="px-3 py-2.5 text-[11px] text-[#a6a2b8]">
                        {formatShortTime(keyMeta.updatedAt)}
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex flex-wrap items-center justify-end gap-1.5">
                          <button
                            type="button"
                            disabled={isRevealing}
                            onClick={() => void handleToggleReveal(keyMeta)}
                            className={`riot-btn inline-flex items-center gap-1 px-2.5 py-1 text-[10px] font-extrabold uppercase disabled:opacity-50 ${
                              isRevealed
                                ? 'bg-[#ffe031] text-[#08080c]'
                                : 'bg-[#1b1b2b] text-[#00f0ff]'
                            }`}
                          >
                            {isRevealed ? (
                              <>
                                <EyeOff className="h-3 w-3" />
                                HIDE
                              </>
                            ) : (
                              <>
                                <Eye className="h-3 w-3" />
                                {isRevealing ? '...' : 'REVEAL'}
                              </>
                            )}
                          </button>

                          {isRevealed ? (
                            <button
                              type="button"
                              onClick={() => void handleCopyRevealed(keyMeta.keyName, revealedValue)}
                              className="riot-btn inline-flex items-center gap-1 bg-[#a3ff12] px-2.5 py-1 text-[10px] font-extrabold text-[#08080c] uppercase"
                            >
                              {isCopied ? (
                                <Check className="h-3 w-3" />
                              ) : (
                                <Copy className="h-3 w-3" />
                              )}
                              {isCopied ? 'COPIED' : 'COPY'}
                            </button>
                          ) : null}

                          <button
                            type="button"
                            disabled={isDeleting}
                            onClick={() => void handleDeleteSecret(keyMeta)}
                            className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2.5 py-1 text-[10px] font-extrabold text-white uppercase disabled:opacity-50"
                          >
                            <Trash2 className="h-3 w-3" />
                            {isDeleting ? '...' : 'DEL'}
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </SectionCard>

      {/* Audit Trail */}
      <SectionCard
        title="VAULTENV AUDIT TRAIL"
        subtitle="Immutable ledger of secret writes, bulk imports, single-key reveals, .env exports, and 9Drive backups."
        icon={<History className="h-4 w-4" />}
        sticker={`${auditLogs.length} EVENTS`}
        stickerTone="cream"
        accent="neutral"
      >
        {auditLogs.length === 0 ? (
          <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] py-8 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
            NO AUDIT EVENTS LOGGED FOR {selectedProject.toUpperCase()} YET.
          </div>
        ) : (
          <div className="max-h-72 space-y-2 overflow-y-auto pr-1">
            {auditLogs.map((logEntry) => (
              <div
                key={logEntry.id}
                className="flex flex-wrap items-center justify-between gap-2 border-2 border-black bg-[#08080c] px-3 py-2 font-mono text-xs"
              >
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <MonoBadge tone={resolveAuditTone(logEntry.action)}>
                    {logEntry.action || 'event'}
                  </MonoBadge>
                  <MonoBadge tone="cream">{logEntry.project}</MonoBadge>
                  {logEntry.envName ? <MonoBadge tone="cyan">{logEntry.envName}</MonoBadge> : null}
                  {logEntry.keyName ? (
                    <span className="font-extrabold text-[#ffe031]">{logEntry.keyName}</span>
                  ) : null}
                  {logEntry.detail ? (
                    <span className="truncate text-[11px] text-[#a6a2b8]">{logEntry.detail}</span>
                  ) : null}
                </div>
                <span className="shrink-0 text-[10px] font-bold text-[#a6a2b8]">
                  {formatShortTime(logEntry.createdAt)}
                </span>
              </div>
            ))}
          </div>
        )}
      </SectionCard>

      {/* Export .env Modal */}
      {exportModalData ? (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4 backdrop-blur-xs"
          onClick={() => setExportModalData(null)}
        >
          <div
            className="w-full max-w-3xl border-[3px] border-black bg-[#13131d] shadow-[8px_8px_0_#ffe031]"
            onClick={(ev) => ev.stopPropagation()}
          >
            <div className="flex flex-wrap items-center justify-between gap-2 border-b-[2.5px] border-black bg-[#1b1b2b] px-4 py-3">
              <div className="flex flex-wrap items-center gap-2">
                <ComicSticker tone="cyan">EXPORT .ENV</ComicSticker>
                <span className="font-display text-sm text-[#fff6de] uppercase">
                  {exportModalData.project} / {exportModalData.envName}
                </span>
                <MonoBadge tone="yellow">{exportModalData.keyCount} KEYS</MonoBadge>
              </div>
              <button
                type="button"
                onClick={() => setExportModalData(null)}
                className="text-[#a6a2b8] hover:text-[#ffe031]"
              >
                <X className="h-5 w-5" />
              </button>
            </div>

            <div className="space-y-3 p-4">
              <textarea
                readOnly
                rows={12}
                value={exportModalData.dotenv || '# Empty environment'}
                className="riot-input w-full p-3 font-mono text-xs leading-relaxed text-[#a3ff12]"
              />
              <div className="flex flex-wrap items-center justify-end gap-2">
                <button
                  type="button"
                  onClick={() => void handleCopyExportDotEnv()}
                  className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c] uppercase"
                >
                  {copiedExport ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
                  {copiedExport ? 'COPIED!' : 'COPY .ENV'}
                </button>
                <button
                  type="button"
                  onClick={handleDownloadExportDotEnv}
                  className="riot-btn inline-flex items-center gap-1.5 bg-[#00f0ff] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c] uppercase"
                >
                  <Download className="h-3.5 w-3.5" />
                  DOWNLOAD .{exportModalData.envName}.env
                </button>
                <button
                  type="button"
                  onClick={() => setExportModalData(null)}
                  className="riot-btn bg-[#1b1b2b] px-4 py-2 font-mono text-xs font-extrabold text-[#fff6de] uppercase"
                >
                  CLOSE
                </button>
              </div>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}
