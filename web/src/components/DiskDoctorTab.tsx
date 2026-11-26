import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  Bot,
  CheckSquare,
  Cpu,
  FolderSearch,
  HardDrive,
  History,
  Loader2,
  Radar,
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Square,
  Trash2,
  Zap,
} from 'lucide-react';
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  StatTile,
  type BadgeTone,
} from './UiPrimitives';
import { formatBytes, formatCompactNumber, formatShortTime } from '../utils';

export interface DiskDoctorTabProps {
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

type DriveStatus = 'critical' | 'warn' | 'ok';
type SafetyLevel = 'safe' | 'caution' | 'protected';

interface DriveInfo {
  drive: string;
  label: string;
  totalBytes: number;
  usedBytes: number;
  freeBytes: number;
  usedPct: number;
  status: DriveStatus;
}

interface DirUsage {
  path: string;
  sizeBytes: number;
  fileCount: number;
  app: string;
  safety: SafetyLevel;
  reason: string;
}

interface AppUsage {
  app: string;
  sizeBytes: number;
  dirCount: number;
  safety: SafetyLevel;
  sharePct: number;
}

interface ScanReport {
  rootPath: string;
  scannedBytes: number;
  scannedFiles: number;
  durationMs: number;
  topDirectories: DirUsage[];
  appBreakdown: AppUsage[];
  cleanCandidates: DirUsage[];
}

interface CleanResult {
  dryRun: boolean;
  freedBytes: number;
  deletedFiles: number;
  skippedLocked: number;
  paths: string[];
  errors: string[];
}

interface CleanHistoryRecord {
  id: string;
  paths: string[];
  dryRun: boolean;
  freedBytes: number;
  deletedFiles: number;
  skippedLocked: number;
  createdAt: string;
}

interface InsightResult {
  source: string;
  summary: string;
  rootCauses: string[];
  recommendations: string[];
  estimatedRecoverableBytes: number;
}

type RawRecord = Record<string, unknown>;

function asRecord(value: unknown): RawRecord {
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    return value as RawRecord;
  }
  return {};
}

function pickString(source: RawRecord, keys: string[], fallback = ''): string {
  for (const key of keys) {
    const candidate = source[key];
    if (typeof candidate === 'string' && candidate.length > 0) {
      return candidate;
    }
  }
  return fallback;
}

function pickNumber(source: RawRecord, keys: string[], fallback = 0): number {
  for (const key of keys) {
    const candidate = source[key];
    if (typeof candidate === 'number' && Number.isFinite(candidate)) {
      return candidate;
    }
    if (typeof candidate === 'string' && candidate.trim() !== '') {
      const parsed = Number(candidate);
      if (Number.isFinite(parsed)) {
        return parsed;
      }
    }
  }
  return fallback;
}

function pickBool(source: RawRecord, keys: string[], fallback = false): boolean {
  for (const key of keys) {
    const candidate = source[key];
    if (typeof candidate === 'boolean') {
      return candidate;
    }
    if (typeof candidate === 'number') {
      return candidate !== 0;
    }
  }
  return fallback;
}

function pickStringArray(source: RawRecord, keys: string[]): string[] {
  for (const key of keys) {
    const candidate = source[key];
    if (Array.isArray(candidate)) {
      return candidate.filter((entry): entry is string => typeof entry === 'string');
    }
    if (typeof candidate === 'string' && candidate.trim().startsWith('[')) {
      try {
        const parsed = JSON.parse(candidate) as unknown;
        if (Array.isArray(parsed)) {
          return parsed.filter((entry): entry is string => typeof entry === 'string');
        }
      } catch {
        // Ignore malformed JSON string
      }
    }
  }
  return [];
}

function normalizeDriveStatus(rawStatus: string, usedPct: number, freeBytes: number): DriveStatus {
  const lowered = rawStatus.toLowerCase();
  if (lowered === 'critical' || usedPct >= 90 || freeBytes < 2 * 1024 * 1024 * 1024) {
    return 'critical';
  }
  if (lowered === 'warn' || lowered === 'warning' || usedPct >= 80) {
    return 'warn';
  }
  return 'ok';
}

function normalizeSafety(rawSafety: string): SafetyLevel {
  const lowered = rawSafety.toLowerCase();
  if (lowered === 'safe') {
    return 'safe';
  }
  if (lowered === 'protected') {
    return 'protected';
  }
  return 'caution';
}

function normalizeDrive(rawEntry: unknown): DriveInfo {
  const rec = asRecord(rawEntry);
  const totalBytes = pickNumber(rec, ['totalBytes', 'total_bytes', 'TotalBytes']);
  const freeBytes = pickNumber(rec, ['freeBytes', 'free_bytes', 'FreeBytes']);
  const usedBytes = pickNumber(
    rec,
    ['usedBytes', 'used_bytes', 'UsedBytes'],
    Math.max(0, totalBytes - freeBytes),
  );
  const computedPct = totalBytes > 0 ? Math.round((usedBytes / totalBytes) * 1000) / 10 : 0;
  const usedPct = pickNumber(rec, ['usedPct', 'used_pct', 'UsedPct'], computedPct);
  const status = normalizeDriveStatus(pickString(rec, ['status', 'Status']), usedPct, freeBytes);
  return {
    drive: pickString(rec, ['drive', 'Drive', 'mount', 'path'], 'C:\\'),
    label: pickString(rec, ['label', 'Label'], 'Local Disk'),
    totalBytes,
    usedBytes,
    freeBytes,
    usedPct,
    status,
  };
}

function normalizeDirUsage(rawEntry: unknown): DirUsage {
  const rec = asRecord(rawEntry);
  return {
    path: pickString(rec, ['path', 'Path']),
    sizeBytes: pickNumber(rec, ['sizeBytes', 'size_bytes', 'SizeBytes', 'bytes']),
    fileCount: pickNumber(rec, ['fileCount', 'file_count', 'FileCount', 'files']),
    app: pickString(rec, ['app', 'App', 'category', 'Category'], 'Unclassified'),
    safety: normalizeSafety(pickString(rec, ['safety', 'Safety'], 'caution')),
    reason: pickString(rec, ['reason', 'Reason', 'note']),
  };
}

function normalizeAppUsage(rawEntry: unknown, totalScannedBytes: number): AppUsage {
  const rec = asRecord(rawEntry);
  const sizeBytes = pickNumber(rec, ['sizeBytes', 'size_bytes', 'SizeBytes', 'bytes']);
  const fallbackPct =
    totalScannedBytes > 0 ? Math.round((sizeBytes / totalScannedBytes) * 1000) / 10 : 0;
  return {
    app: pickString(rec, ['app', 'App', 'name', 'Name'], 'Other'),
    sizeBytes,
    dirCount: pickNumber(rec, ['dirCount', 'dir_count', 'DirCount', 'count']),
    safety: normalizeSafety(pickString(rec, ['safety', 'Safety'], 'caution')),
    sharePct: pickNumber(rec, ['sharePct', 'share_pct', 'SharePct', 'pct'], fallbackPct),
  };
}

function normalizeScanReport(rawPayload: unknown): ScanReport {
  const rec = asRecord(rawPayload);
  const scannedBytes = pickNumber(rec, ['scannedBytes', 'scanned_bytes', 'ScannedBytes']);
  const rawTop = Array.isArray(rec.topDirectories)
    ? rec.topDirectories
    : Array.isArray(rec.top_directories)
      ? rec.top_directories
      : Array.isArray(rec.TopDirectories)
        ? rec.TopDirectories
        : [];
  const rawApps = Array.isArray(rec.appBreakdown)
    ? rec.appBreakdown
    : Array.isArray(rec.app_breakdown)
      ? rec.app_breakdown
      : Array.isArray(rec.AppBreakdown)
        ? rec.AppBreakdown
        : [];
  const rawCandidates = Array.isArray(rec.cleanCandidates)
    ? rec.cleanCandidates
    : Array.isArray(rec.clean_candidates)
      ? rec.clean_candidates
      : Array.isArray(rec.CleanCandidates)
        ? rec.CleanCandidates
        : [];

  return {
    rootPath: pickString(rec, ['rootPath', 'root_path', 'RootPath'], 'Quick Audit'),
    scannedBytes,
    scannedFiles: pickNumber(rec, ['scannedFiles', 'scanned_files', 'ScannedFiles']),
    durationMs: pickNumber(rec, ['durationMs', 'duration_ms', 'DurationMs']),
    topDirectories: rawTop.map(normalizeDirUsage),
    appBreakdown: rawApps.map((entry) => normalizeAppUsage(entry, scannedBytes)),
    cleanCandidates: rawCandidates.map(normalizeDirUsage),
  };
}

function normalizeCleanResult(rawPayload: unknown): CleanResult {
  const rec = asRecord(rawPayload);
  return {
    dryRun: pickBool(rec, ['dryRun', 'dry_run', 'DryRun']),
    freedBytes: pickNumber(rec, ['freedBytes', 'freed_bytes', 'FreedBytes']),
    deletedFiles: pickNumber(rec, ['deletedFiles', 'deleted_files', 'DeletedFiles']),
    skippedLocked: pickNumber(rec, ['skippedLocked', 'skipped_locked', 'SkippedLocked']),
    paths: pickStringArray(rec, ['paths', 'Paths', 'cleanedPaths', 'cleaned_paths']),
    errors: pickStringArray(rec, ['errors', 'Errors']),
  };
}

function normalizeHistoryRecord(rawEntry: unknown): CleanHistoryRecord {
  const rec = asRecord(rawEntry);
  return {
    id: pickString(rec, ['id', 'ID'], String(Date.now())),
    paths: pickStringArray(rec, ['paths', 'Paths', 'pathsJson', 'paths_json', 'PathsJSON']),
    dryRun: pickBool(rec, ['dryRun', 'dry_run', 'DryRun']),
    freedBytes: pickNumber(rec, ['freedBytes', 'freed_bytes', 'FreedBytes']),
    deletedFiles: pickNumber(rec, ['deletedFiles', 'deleted_files', 'DeletedFiles']),
    skippedLocked: pickNumber(rec, ['skippedLocked', 'skipped_locked', 'SkippedLocked']),
    createdAt: pickString(rec, ['createdAt', 'created_at', 'CreatedAt']),
  };
}

function normalizeInsightResult(rawPayload: unknown): InsightResult {
  const rec = asRecord(rawPayload);
  return {
    source: pickString(rec, ['source', 'Source'], 'deterministic'),
    summary: pickString(rec, ['summary', 'Summary']),
    rootCauses: pickStringArray(rec, ['rootCauses', 'root_causes', 'RootCauses']),
    recommendations: pickStringArray(rec, ['recommendations', 'Recommendations']),
    estimatedRecoverableBytes: pickNumber(rec, [
      'estimatedRecoverableBytes',
      'estimated_recoverable_bytes',
      'EstimatedRecoverableBytes',
    ]),
  };
}

async function diskRequest<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers ?? {}),
    },
  });
  const bodyText = await response.text();
  let parsedBody: unknown = {};
  if (bodyText.trim().length > 0) {
    try {
      parsedBody = JSON.parse(bodyText) as unknown;
    } catch {
      parsedBody = { error: bodyText };
    }
  }
  if (!response.ok) {
    const errRecord = asRecord(parsedBody);
    const errMessage =
      pickString(errRecord, ['error', 'message', 'detail']) ||
      `Request failed (${response.status})`;
    throw new Error(errMessage);
  }
  return parsedBody as T;
}

async function fetchDrives(): Promise<DriveInfo[]> {
  const payload = await diskRequest<unknown>('/api/disk/drives');
  const rec = asRecord(payload);
  const rawList = Array.isArray(rec.drives)
    ? rec.drives
    : Array.isArray(payload)
      ? payload
      : [];
  return rawList.map(normalizeDrive);
}

async function fetchQuickAudit(): Promise<ScanReport> {
  const payload = await diskRequest<unknown>('/api/disk/quick-audit');
  return normalizeScanReport(payload);
}

async function postScanDirectory(path: string, maxDepth: number): Promise<ScanReport> {
  const payload = await diskRequest<unknown>('/api/disk/scan', {
    method: 'POST',
    body: JSON.stringify({ path, maxDepth }),
  });
  return normalizeScanReport(payload);
}

async function postCleanPaths(paths: string[], dryRun: boolean): Promise<CleanResult> {
  const payload = await diskRequest<unknown>('/api/disk/clean', {
    method: 'POST',
    body: JSON.stringify({ paths, dryRun }),
  });
  return normalizeCleanResult(payload);
}

async function postDiskInsight(path: string, report: ScanReport | null): Promise<InsightResult> {
  const payload = await diskRequest<unknown>('/api/disk/insight', {
    method: 'POST',
    body: JSON.stringify({ path, report }),
  });
  return normalizeInsightResult(payload);
}

async function fetchCleanHistory(): Promise<CleanHistoryRecord[]> {
  const payload = await diskRequest<unknown>('/api/disk/history');
  const rec = asRecord(payload);
  const rawList = Array.isArray(rec.history)
    ? rec.history
    : Array.isArray(payload)
      ? payload
      : [];
  return rawList.map(normalizeHistoryRecord);
}

function safetyBadgeTone(safety: SafetyLevel): BadgeTone {
  if (safety === 'safe') {
    return 'ok';
  }
  if (safety === 'protected') {
    return 'err';
  }
  return 'yellow';
}

function driveBarColor(status: DriveStatus): string {
  if (status === 'critical') {
    return 'bg-[#ff2e88]';
  }
  if (status === 'warn') {
    return 'bg-[#ffe031]';
  }
  return 'bg-[#a3ff12]';
}

function driveBadgeTone(status: DriveStatus): BadgeTone {
  if (status === 'critical') {
    return 'magenta';
  }
  if (status === 'warn') {
    return 'yellow';
  }
  return 'ok';
}

function formatGigabytes(byteCount: number): string {
  const gb = Math.max(0, byteCount) / (1024 * 1024 * 1024);
  return `${gb >= 100 ? gb.toFixed(1) : gb.toFixed(2)} GB`;
}

const APP_BAR_COLORS = [
  'bg-[#ff2e88]',
  'bg-[#ffe031]',
  'bg-[#00f0ff]',
  'bg-[#a3ff12]',
  'bg-[#ff6b35]',
  'bg-[#c084fc]',
];

export function DiskDoctorTab({ onNotify }: DiskDoctorTabProps) {
  const [drives, setDrives] = useState<DriveInfo[]>([]);
  const [loadingDrives, setLoadingDrives] = useState(false);

  const [scanPath, setScanPath] = useState('C:\\');
  const [maxDepth, setMaxDepth] = useState(3);
  const [scanning, setScanning] = useState(false);
  const [scanReport, setScanReport] = useState<ScanReport | null>(null);

  const [queueFilter, setQueueFilter] = useState<'candidates' | 'top'>('candidates');
  const [selectedPaths, setSelectedPaths] = useState<string[]>([]);
  const [cleaning, setCleaning] = useState(false);
  const [lastCleanResult, setLastCleanResult] = useState<CleanResult | null>(null);

  const [history, setHistory] = useState<CleanHistoryRecord[]>([]);
  const [loadingHistory, setLoadingHistory] = useState(false);

  const [insight, setInsight] = useState<InsightResult | null>(null);
  const [loadingInsight, setLoadingInsight] = useState(false);

  const loadDrives = useCallback(async () => {
    setLoadingDrives(true);
    try {
      const nextDrives = await fetchDrives();
      setDrives(nextDrives);
    } catch (err) {
      onNotify('err', `Drive telemetry failed: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setLoadingDrives(false);
    }
  }, [onNotify]);

  const loadHistory = useCallback(async () => {
    setLoadingHistory(true);
    try {
      const records = await fetchCleanHistory();
      setHistory(records);
    } catch (err) {
      onNotify(
        'err',
        `Cleanup history failed: ${err instanceof Error ? err.message : String(err)}`,
      );
    } finally {
      setLoadingHistory(false);
    }
  }, [onNotify]);

  useEffect(() => {
    void loadDrives();
    void loadHistory();
  }, [loadDrives, loadHistory]);

  const handleQuickAudit = useCallback(async () => {
    setScanning(true);
    setLastCleanResult(null);
    try {
      const report = await fetchQuickAudit();
      setScanReport(report);
      const safeDefaults = report.cleanCandidates
        .filter((entry) => entry.safety === 'safe')
        .map((entry) => entry.path);
      setSelectedPaths(safeDefaults);
      setQueueFilter(report.cleanCandidates.length > 0 ? 'candidates' : 'top');
      onNotify(
        'ok',
        `Quick Blame Audit complete: ${formatBytes(report.scannedBytes)} across ${formatCompactNumber(report.scannedFiles)} files (${report.durationMs}ms).`,
      );
    } catch (err) {
      onNotify('err', `Quick Audit failed: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setScanning(false);
    }
  }, [onNotify]);

  const handleScanPath = useCallback(
    async (targetPath: string, depthOverride?: number) => {
      const trimmed = targetPath.trim();
      if (!trimmed) {
        onNotify('err', 'Enter a directory or drive path to scan.');
        return;
      }
      setScanPath(trimmed);
      setScanning(true);
      setLastCleanResult(null);
      try {
        const report = await postScanDirectory(trimmed, depthOverride ?? maxDepth);
        setScanReport(report);
        const safeDefaults = report.cleanCandidates
          .filter((entry) => entry.safety === 'safe')
          .map((entry) => entry.path);
        setSelectedPaths(safeDefaults);
        setQueueFilter(report.cleanCandidates.length > 0 ? 'candidates' : 'top');
        onNotify(
          'ok',
          `Scanned ${report.rootPath}: ${formatBytes(report.scannedBytes)} (${report.durationMs}ms).`,
        );
      } catch (err) {
        onNotify('err', `Directory scan failed: ${err instanceof Error ? err.message : String(err)}`);
      } finally {
        setScanning(false);
      }
    },
    [maxDepth, onNotify],
  );

  const visibleQueueRows = useMemo(() => {
    if (!scanReport) {
      return [];
    }
    return queueFilter === 'candidates' ? scanReport.cleanCandidates : scanReport.topDirectories;
  }, [queueFilter, scanReport]);

  const toggleSelectPath = (targetPath: string, safety: SafetyLevel) => {
    if (safety === 'protected') {
      onNotify('err', 'Protected system paths cannot be selected for cleanup.');
      return;
    }
    setSelectedPaths((prev) =>
      prev.includes(targetPath)
        ? prev.filter((existing) => existing !== targetPath)
        : [...prev, targetPath],
    );
  };

  const selectAllSafe = () => {
    const safePaths = visibleQueueRows
      .filter((row) => row.safety === 'safe')
      .map((row) => row.path);
    setSelectedPaths(safePaths);
  };

  const handleCleanAction = async (dryRun: boolean) => {
    if (selectedPaths.length === 0) {
      onNotify('info', 'Select at least one non-protected directory first.');
      return;
    }
    setCleaning(true);
    try {
      const outcome = await postCleanPaths(selectedPaths, dryRun);
      setLastCleanResult(outcome);
      if (dryRun) {
        onNotify(
          'info',
          `DRY-RUN PREVIEW: Would free ${formatBytes(outcome.freedBytes)} (${formatCompactNumber(outcome.deletedFiles)} files).`,
        );
      } else {
        onNotify(
          'ok',
          `CLEANED: Freed ${formatBytes(outcome.freedBytes)} (${formatCompactNumber(outcome.deletedFiles)} files, ${outcome.skippedLocked} locked skipped).`,
        );
        await Promise.all([loadDrives(), loadHistory()]);
      }
    } catch (err) {
      onNotify(
        'err',
        `${dryRun ? 'Dry-run' : 'Cleanup'} failed: ${err instanceof Error ? err.message : String(err)}`,
      );
    } finally {
      setCleaning(false);
    }
  };

  const handleDeepInsight = async () => {
    setLoadingInsight(true);
    try {
      const diagnosis = await postDiskInsight(scanReport?.rootPath || scanPath, scanReport);
      setInsight(diagnosis);
      onNotify(
        'ok',
        `AI Deep Insight ready (${diagnosis.source === 'ai_gateway' ? 'AI GATEWAY' : 'DETERMINISTIC ENGINE'}).`,
      );
    } catch (err) {
      onNotify('err', `AI Insight failed: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setLoadingInsight(false);
    }
  };

  const criticalDriveCount = useMemo(
    () => drives.filter((drive) => drive.status === 'critical').length,
    [drives],
  );

  const totalFreeBytes = useMemo(
    () => drives.reduce((acc, drive) => acc + drive.freeBytes, 0),
    [drives],
  );

  const recoverableCandidateBytes = useMemo(
    () =>
      (scanReport?.cleanCandidates ?? [])
        .filter((entry) => entry.safety === 'safe')
        .reduce((acc, entry) => acc + entry.sizeBytes, 0),
    [scanReport],
  );

  const maxAppBytes = useMemo(() => {
    const breakdown = scanReport?.appBreakdown ?? [];
    if (breakdown.length === 0) {
      return 1;
    }
    return Math.max(1, ...breakdown.map((entry) => entry.sizeBytes));
  }, [scanReport]);

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="STAGE // DISK DOCTOR"
        sticker={criticalDriveCount > 0 ? `${criticalDriveCount} CRITICAL` : 'ARMORY READY'}
        tone={criticalDriveCount > 0 ? 'magenta' : 'ok'}
        title="INTELLIGENT DISK & DIRECTORY DOCTOR"
        subtitle="Live Windows/Unix drive pressure gauges, instant app-blame attribution, safe dry-run cleanup queue, and AI root-cause diagnosis."
        actions={
          <>
            <button
              type="button"
              onClick={() => void loadDrives()}
              disabled={loadingDrives}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#fff6de] px-3 py-2 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#ffe031] disabled:opacity-60"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingDrives ? 'animate-spin' : ''}`} />
              REFRESH DRIVES
            </button>
            <button
              type="button"
              onClick={() => void handleQuickAudit()}
              disabled={scanning}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-3.5 py-2 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#a3ff12] disabled:opacity-60"
            >
              {scanning ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Zap className="h-3.5 w-3.5" />
              )}
              QUICK BLAME AUDIT
            </button>
          </>
        }
      />

      {/* TOP TELEMETRY STRIP */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="DETECTED DRIVES"
          value={drives.length}
          sublabel={criticalDriveCount > 0 ? `${criticalDriveCount} CRITICAL PRESSURE` : 'ALL VOLUMES MONITORED'}
          icon={<HardDrive className="h-4 w-4" />}
          tone={criticalDriveCount > 0 ? 'magenta' : 'cyan'}
        />
        <StatTile
          label="TOTAL FREE SPACE"
          value={formatGigabytes(totalFreeBytes)}
          sublabel="ACROSS MOUNTED VOLUMES"
          icon={<Radar className="h-4 w-4" />}
          tone="ok"
        />
        <StatTile
          label="LAST SCAN FOOTPRINT"
          value={scanReport ? formatBytes(scanReport.scannedBytes) : '—'}
          sublabel={
            scanReport
              ? `${formatCompactNumber(scanReport.scannedFiles)} FILES IN ${scanReport.durationMs}MS`
              : 'RUN QUICK AUDIT OR SCAN'
          }
          icon={<FolderSearch className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="SAFE RECOVERABLE"
          value={scanReport ? formatBytes(recoverableCandidateBytes) : '—'}
          sublabel={`${selectedPaths.length} PATH(S) QUEUED`}
          icon={<Trash2 className="h-4 w-4" />}
          tone="magenta"
        />
      </div>

      {/* 1. LIVE DRIVE PRESSURE GAUGES */}
      <SectionCard
        title="LIVE DRIVE PRESSURE GAUGES"
        subtitle="Real-time volume utilization. Critical >= 90% or < 2 GB free, Warn >= 80%, OK < 80%."
        icon={<HardDrive className="h-4 w-4" />}
        sticker="TELEMETRY"
        stickerTone="cyan"
        accent="cyan"
      >
        {drives.length === 0 ? (
          <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] p-6 text-center font-mono text-xs text-[#a6a2b8]">
            {loadingDrives ? 'Probing mounted drives...' : 'No drives reported by /api/disk/drives.'}
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            {drives.map((driveInfo) => {
              const clampedPct = Math.min(100, Math.max(0, driveInfo.usedPct));
              return (
                <div
                  key={driveInfo.drive}
                  className="flex flex-col justify-between border-[2.5px] border-black bg-[#0d0d16] p-4 shadow-[4px_4px_0_#000]"
                >
                  <div>
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <span className="font-display text-lg text-[#fff6de]">
                          {driveInfo.drive}
                        </span>
                        <span className="truncate font-mono text-[11px] font-bold text-[#a6a2b8]">
                          {driveInfo.label}
                        </span>
                      </div>
                      <MonoBadge tone={driveBadgeTone(driveInfo.status)}>
                        {driveInfo.status}
                      </MonoBadge>
                    </div>

                    <div className="mt-3">
                      <div className="mb-1 flex items-center justify-between font-mono text-[11px] font-bold">
                        <span className="text-[#fff6de]">{clampedPct.toFixed(1)}% USED</span>
                        <span className="text-[#00f0ff]">
                          {formatGigabytes(driveInfo.freeBytes)} FREE
                        </span>
                      </div>
                      <div className="h-4 w-full overflow-hidden border-2 border-black bg-[#1b1b2b]">
                        <div
                          className={`h-full transition-all duration-300 ${driveBarColor(driveInfo.status)}`}
                          style={{ width: `${clampedPct}%` }}
                        />
                      </div>
                      <div className="mt-1.5 flex items-center justify-between font-mono text-[10px] text-[#a6a2b8]">
                        <span>USED: {formatGigabytes(driveInfo.usedBytes)}</span>
                        <span>TOTAL: {formatGigabytes(driveInfo.totalBytes)}</span>
                      </div>
                    </div>
                  </div>

                  <button
                    type="button"
                    onClick={() => void handleScanPath(driveInfo.drive)}
                    disabled={scanning}
                    className="riot-btn mt-4 inline-flex w-full items-center justify-center gap-1.5 bg-[#1b1b2b] px-3 py-2 font-mono text-[11px] font-extrabold text-[#fff6de] hover:bg-[#ffe031] hover:text-[#08080c] disabled:opacity-50"
                  >
                    <FolderSearch className="h-3.5 w-3.5" />
                    SCAN THIS DRIVE
                  </button>
                </div>
              );
            })}
          </div>
        )}
      </SectionCard>

      {/* 2. QUICK BLAME AUDIT & DIRECTORY SCANNER */}
      <SectionCard
        title="QUICK BLAME AUDIT & DIRECTORY SCANNER"
        subtitle="Audit known OS/dev hotspots in seconds or deep-scan any custom directory path."
        icon={<FolderSearch className="h-4 w-4" />}
        sticker="SCANNER"
        stickerTone="yellow"
        accent="yellow"
      >
        <div className="flex flex-col gap-3 lg:flex-row lg:items-end">
          <div className="flex-1">
            <label
              htmlFor="disk-doctor-path-input"
              className="mb-1 block font-mono text-[11px] font-extrabold uppercase text-[#ffe031]"
            >
              TARGET PATH
            </label>
            <input
              id="disk-doctor-path-input"
              type="text"
              value={scanPath}
              onChange={(event) => setScanPath(event.target.value)}
              placeholder="C:\ or Z:\repo"
              className="w-full border-2 border-black bg-[#08080c] px-3 py-2 font-mono text-xs font-bold text-[#fff6de] placeholder-[#68647c] focus:border-[#00f0ff] focus:outline-none"
            />
          </div>

          <div className="w-full lg:w-36">
            <label
              htmlFor="disk-doctor-depth-select"
              className="mb-1 block font-mono text-[11px] font-extrabold uppercase text-[#00f0ff]"
            >
              MAX DEPTH
            </label>
            <select
              id="disk-doctor-depth-select"
              value={maxDepth}
              onChange={(event) => setMaxDepth(Number(event.target.value))}
              className="w-full border-2 border-black bg-[#08080c] px-3 py-2 font-mono text-xs font-bold text-[#fff6de] focus:border-[#00f0ff] focus:outline-none"
            >
              <option value={2}>Depth 2 (Fast)</option>
              <option value={3}>Depth 3 (Default)</option>
              <option value={4}>Depth 4 (Deep)</option>
              <option value={5}>Depth 5 (Max)</option>
            </select>
          </div>

          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              onClick={() => void handleScanPath(scanPath)}
              disabled={scanning}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#00f0ff] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#a3ff12] disabled:opacity-60"
            >
              {scanning ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <FolderSearch className="h-3.5 w-3.5" />
              )}
              SCAN PATH
            </button>
            <button
              type="button"
              onClick={() => void handleQuickAudit()}
              disabled={scanning}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#ff2e88] hover:text-white disabled:opacity-60"
            >
              <Zap className="h-3.5 w-3.5" />
              QUICK BLAME AUDIT
            </button>
          </div>
        </div>

        <div className="mt-3 flex flex-wrap items-center gap-2">
          <span className="font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">PRESETS:</span>
          {['C:\\', 'Z:\\repo', 'C:\\Users'].map((preset) => (
            <button
              key={preset}
              type="button"
              onClick={() => setScanPath(preset)}
              className="border border-[#3a3a58] bg-[#1b1b2b] px-2 py-0.5 font-mono text-[11px] font-bold text-[#fff6de] hover:border-[#ffe031] hover:text-[#ffe031]"
            >
              {preset}
            </button>
          ))}
        </div>
      </SectionCard>

      {/* 3. APP BLAME BREAKDOWN */}
      <SectionCard
        title="APP BLAME BREAKDOWN"
        subtitle="Attribution by runtime, build cache, package store, container image, or OS temp folder."
        icon={<Cpu className="h-4 w-4" />}
        sticker="WHO ATE MY DISK?"
        stickerTone="magenta"
        accent="magenta"
      >
        {!scanReport || scanReport.appBreakdown.length === 0 ? (
          <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] p-6 text-center font-mono text-xs text-[#a6a2b8]">
            Run <strong>QUICK BLAME AUDIT</strong> or <strong>SCAN PATH</strong> to attribute disk
            usage by application/runtime.
          </div>
        ) : (
          <div className="space-y-3">
            {scanReport.appBreakdown.map((appEntry, index) => {
              const barPct = Math.max(2, Math.min(100, (appEntry.sizeBytes / maxAppBytes) * 100));
              const barColor = APP_BAR_COLORS[index % APP_BAR_COLORS.length];
              return (
                <div
                  key={`${appEntry.app}-${index}`}
                  className="border-2 border-black bg-[#0d0d16] p-3"
                >
                  <div className="mb-1.5 flex flex-wrap items-center justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <span className="font-mono text-xs font-extrabold text-[#fff6de]">
                        {appEntry.app}
                      </span>
                      <MonoBadge tone={safetyBadgeTone(appEntry.safety)}>
                        {appEntry.safety}
                      </MonoBadge>
                      {appEntry.dirCount > 0 ? (
                        <span className="font-mono text-[10px] text-[#a6a2b8]">
                          ({appEntry.dirCount} dirs)
                        </span>
                      ) : null}
                    </div>
                    <div className="font-mono text-xs font-extrabold text-[#ffe031]">
                      {formatBytes(appEntry.sizeBytes)}{' '}
                      <span className="text-[#a6a2b8]">({appEntry.sharePct.toFixed(1)}%)</span>
                    </div>
                  </div>
                  <div className="h-3.5 w-full overflow-hidden border-2 border-black bg-[#1b1b2b]">
                    <div
                      className={`h-full transition-all duration-300 ${barColor}`}
                      style={{ width: `${barPct}%` }}
                    />
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </SectionCard>

      {/* 4. ACTIONABLE CLEANUP QUEUE + HISTORY */}
      <SectionCard
        title="ACTIONABLE CLEANUP QUEUE"
        subtitle="Select safe or caution directories, preview exact bytes with Dry-Run, then execute guarded cleanup."
        icon={<Trash2 className="h-4 w-4" />}
        sticker="GUARDED CLEANER"
        stickerTone="ok"
        accent="ok"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => setQueueFilter('candidates')}
              className={`border-2 border-black px-2.5 py-1 font-mono text-[11px] font-extrabold uppercase ${
                queueFilter === 'candidates'
                  ? 'bg-[#ffe031] text-[#08080c]'
                  : 'bg-[#08080c] text-[#a6a2b8]'
              }`}
            >
              CLEAN CANDIDATES ({scanReport?.cleanCandidates.length ?? 0})
            </button>
            <button
              type="button"
              onClick={() => setQueueFilter('top')}
              className={`border-2 border-black px-2.5 py-1 font-mono text-[11px] font-extrabold uppercase ${
                queueFilter === 'top'
                  ? 'bg-[#00f0ff] text-[#08080c]'
                  : 'bg-[#08080c] text-[#a6a2b8]'
              }`}
            >
              TOP DIRECTORIES ({scanReport?.topDirectories.length ?? 0})
            </button>
          </div>
        }
      >
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3 border-2 border-black bg-[#0d0d16] p-3">
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={selectAllSafe}
              className="border-2 border-black bg-[#1b1b2b] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#a3ff12] hover:bg-[#a3ff12] hover:text-[#08080c]"
            >
              SELECT ALL SAFE
            </button>
            <button
              type="button"
              onClick={() => setSelectedPaths([])}
              className="border-2 border-black bg-[#1b1b2b] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#fff6de] hover:bg-[#3a3a58]"
            >
              CLEAR ({selectedPaths.length})
            </button>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => void handleCleanAction(true)}
              disabled={cleaning || selectedPaths.length === 0}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#00f0ff] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#ffe031] disabled:opacity-50"
            >
              {cleaning ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <ShieldCheck className="h-3.5 w-3.5" />
              )}
              DRY-RUN PREVIEW
            </button>
            <button
              type="button"
              onClick={() => void handleCleanAction(false)}
              disabled={cleaning || selectedPaths.length === 0}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ff2e88] px-3 py-1.5 font-mono text-xs font-extrabold text-white hover:bg-[#ff334b] disabled:opacity-50"
            >
              {cleaning ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Trash2 className="h-3.5 w-3.5" />
              )}
              CLEAN SELECTED ({selectedPaths.length})
            </button>
          </div>
        </div>

        {lastCleanResult ? (
          <div
            className={`mb-4 border-2 border-black p-3 font-mono text-xs ${
              lastCleanResult.dryRun
                ? 'bg-[#00f0ff]/15 text-[#00f0ff]'
                : 'bg-[#a3ff12]/15 text-[#a3ff12]'
            }`}
          >
            <div className="flex flex-wrap items-center justify-between gap-2 font-extrabold">
              <span>
                {lastCleanResult.dryRun ? 'DRY-RUN ESTIMATE:' : 'CLEANUP EXECUTED:'}{' '}
                {formatBytes(lastCleanResult.freedBytes)} across{' '}
                {formatCompactNumber(lastCleanResult.deletedFiles)} files
              </span>
              {lastCleanResult.skippedLocked > 0 ? (
                <MonoBadge tone="yellow">
                  {lastCleanResult.skippedLocked} LOCKED SKIPPED
                </MonoBadge>
              ) : null}
            </div>
            {lastCleanResult.errors.length > 0 ? (
              <div className="mt-2 text-[#ff334b]">
                Errors: {lastCleanResult.errors.join(' | ')}
              </div>
            ) : null}
          </div>
        ) : null}

        {visibleQueueRows.length === 0 ? (
          <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] p-6 text-center font-mono text-xs text-[#a6a2b8]">
            No directories in this view yet. Run a scan or quick audit above.
          </div>
        ) : (
          <div className="overflow-x-auto border-2 border-black bg-[#08080c]">
            <table className="w-full border-collapse text-left font-mono text-xs">
              <thead>
                <tr className="border-b-2 border-black bg-[#1b1b2b] text-[10px] uppercase text-[#ffe031]">
                  <th className="w-10 px-3 py-2">SEL</th>
                  <th className="px-3 py-2">SAFETY</th>
                  <th className="px-3 py-2">APP / CAUSE</th>
                  <th className="px-3 py-2">PATH</th>
                  <th className="px-3 py-2 text-right">FILES</th>
                  <th className="px-3 py-2 text-right">SIZE</th>
                </tr>
              </thead>
              <tbody>
                {visibleQueueRows.map((row) => {
                  const isSelected = selectedPaths.includes(row.path);
                  const isProtected = row.safety === 'protected';
                  return (
                    <tr
                      key={row.path}
                      className={`border-b border-[#24243a] transition-colors ${
                        isSelected ? 'bg-[#ffe031]/10' : 'hover:bg-[#13131d]'
                      }`}
                    >
                      <td className="px-3 py-2">
                        <button
                          type="button"
                          onClick={() => toggleSelectPath(row.path, row.safety)}
                          disabled={isProtected}
                          className="text-[#fff6de] disabled:cursor-not-allowed disabled:opacity-35"
                          aria-label={`Select ${row.path}`}
                        >
                          {isSelected ? (
                            <CheckSquare className="h-4 w-4 text-[#a3ff12]" />
                          ) : (
                            <Square className="h-4 w-4 text-[#a6a2b8]" />
                          )}
                        </button>
                      </td>
                      <td className="px-3 py-2">
                        <MonoBadge tone={safetyBadgeTone(row.safety)}>{row.safety}</MonoBadge>
                      </td>
                      <td className="px-3 py-2 font-bold text-[#00f0ff]">{row.app}</td>
                      <td className="max-w-md truncate px-3 py-2 text-[#fff6de]" title={row.path}>
                        {row.path}
                      </td>
                      <td className="px-3 py-2 text-right text-[#a6a2b8]">
                        {formatCompactNumber(row.fileCount)}
                      </td>
                      <td className="px-3 py-2 text-right font-extrabold text-[#ffe031]">
                        {formatBytes(row.sizeBytes)}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

        {/* CLEANUP HISTORY LOG */}
        <div className="mt-5 border-t-2 border-black pt-4">
          <div className="mb-3 flex items-center justify-between">
            <div className="flex items-center gap-2">
              <History className="h-4 w-4 text-[#00f0ff]" />
              <h3 className="font-display text-xs tracking-wider uppercase text-[#fff6de]">
                CLEANUP HISTORY LOG
              </h3>
              <MonoBadge tone="neutral">{history.length}</MonoBadge>
            </div>
            <button
              type="button"
              onClick={() => void loadHistory()}
              disabled={loadingHistory}
              className="border border-[#3a3a58] bg-[#1b1b2b] px-2 py-1 font-mono text-[10px] font-bold text-[#fff6de] hover:border-[#00f0ff]"
            >
              {loadingHistory ? 'LOADING...' : 'RELOAD LOG'}
            </button>
          </div>

          {history.length === 0 ? (
            <div className="border border-dashed border-[#3a3a58] bg-[#08080c] p-3 font-mono text-[11px] text-[#a6a2b8]">
              No cleanup executions recorded yet.
            </div>
          ) : (
            <div className="space-y-2">
              {history.slice(0, 8).map((entry) => (
                <div
                  key={entry.id}
                  className="flex flex-wrap items-center justify-between gap-2 border border-[#2a2a42] bg-[#08080c] px-3 py-2 font-mono text-[11px]"
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <MonoBadge tone={entry.dryRun ? 'cyan' : 'ok'}>
                      {entry.dryRun ? 'DRY-RUN' : 'CLEANED'}
                    </MonoBadge>
                    <span className="font-extrabold text-[#a3ff12]">
                      {formatBytes(entry.freedBytes)}
                    </span>
                    <span className="truncate text-[#a6a2b8]">
                      ({formatCompactNumber(entry.deletedFiles)} files
                      {entry.skippedLocked > 0 ? `, ${entry.skippedLocked} locked` : ''}) —{' '}
                      {entry.paths.join(', ') || 'selected targets'}
                    </span>
                  </div>
                  <span className="shrink-0 text-[10px] text-[#68647c]">
                    {formatShortTime(entry.createdAt)}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      </SectionCard>

      {/* 5. AI DEEP INSIGHT PANEL */}
      <SectionCard
        title="AI DEEP INSIGHT PANEL"
        subtitle="AI Gateway root-cause diagnosis with automatic deterministic engineering fallback."
        icon={<Bot className="h-4 w-4" />}
        sticker={insight ? insight.source.toUpperCase() : 'AI DIAGNOSTICS'}
        stickerTone={insight?.source === 'ai_gateway' ? 'cyan' : 'yellow'}
        accent="yellow"
        actions={
          <button
            type="button"
            onClick={() => void handleDeepInsight()}
            disabled={loadingInsight}
            className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-3.5 py-2 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#00f0ff] disabled:opacity-60"
          >
            {loadingInsight ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Sparkles className="h-3.5 w-3.5" />
            )}
            AI DEEP INSIGHT
          </button>
        }
      >
        {!insight ? (
          <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] p-6 text-center font-mono text-xs text-[#a6a2b8]">
            Click <strong>AI DEEP INSIGHT</strong> to synthesize root-cause bloat analysis,
            prioritized cleanup steps, and estimated recoverable gigabytes.
          </div>
        ) : (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center justify-between gap-3 border-2 border-black bg-[#0d0d16] p-4">
              <div className="flex flex-wrap items-center gap-2">
                <ComicSticker
                  tone={insight.source === 'ai_gateway' ? 'cyan' : 'yellow'}
                  tilt="-rotate-1"
                >
                  {insight.source === 'ai_gateway' ? 'AI GATEWAY' : 'DETERMINISTIC FALLBACK'}
                </ComicSticker>
                <span className="font-mono text-xs font-bold text-[#fff6de]">
                  {insight.summary}
                </span>
              </div>
              <MonoBadge tone="ok">
                RECOVERABLE: {formatGigabytes(insight.estimatedRecoverableBytes)}
              </MonoBadge>
            </div>

            <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
              <div className="border-2 border-black bg-[#08080c] p-4">
                <div className="mb-2 flex items-center gap-2 font-display text-xs uppercase text-[#ff2e88]">
                  <ShieldAlert className="h-4 w-4" />
                  ROOT-CAUSE DIAGNOSIS
                </div>
                {insight.rootCauses.length === 0 ? (
                  <p className="font-mono text-xs text-[#a6a2b8]">No critical root causes flagged.</p>
                ) : (
                  <ul className="space-y-2 font-mono text-xs text-[#fff6de]">
                    {insight.rootCauses.map((cause, idx) => (
                      <li key={idx} className="flex items-start gap-2">
                        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[#ff2e88]" />
                        <span>{cause}</span>
                      </li>
                    ))}
                  </ul>
                )}
              </div>

              <div className="border-2 border-black bg-[#08080c] p-4">
                <div className="mb-2 flex items-center gap-2 font-display text-xs uppercase text-[#a3ff12]">
                  <Sparkles className="h-4 w-4" />
                  PRIORITIZED CLEANUP STEPS
                </div>
                {insight.recommendations.length === 0 ? (
                  <p className="font-mono text-xs text-[#a6a2b8]">No cleanup steps recommended.</p>
                ) : (
                  <ol className="space-y-2 font-mono text-xs text-[#fff6de]">
                    {insight.recommendations.map((step, idx) => (
                      <li key={idx} className="flex items-start gap-2">
                        <span className="flex h-5 w-5 shrink-0 items-center justify-center border border-black bg-[#a3ff12] text-[10px] font-extrabold text-[#08080c]">
                          {idx + 1}
                        </span>
                        <span>{step}</span>
                      </li>
                    ))}
                  </ol>
                )}
              </div>
            </div>
          </div>
        )}
      </SectionCard>
    </div>
  );
}
