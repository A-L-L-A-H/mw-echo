import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Activity,
  AlertTriangle,
  Bot,
  Check,
  CheckCircle2,
  Copy,
  FolderGit2,
  GitBranch,
  HardDrive,
  HeartPulse,
  Laptop,
  Network,
  Play,
  Plus,
  Radar,
  RefreshCw,
  ScrollText,
  Search,
  Server,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Terminal,
  Trash2,
  Wrench,
  XCircle,
  Zap,
} from 'lucide-react';
import { copyTextToClipboard, formatShortTime } from '../utils';
import {
  ComicSticker,
  CopyableSnippet,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  StatTile,
  type BadgeTone,
} from './UiPrimitives';

export interface DesktopHostTabProps {
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

type DesktopMode = 'workspace' | 'developer';

interface DesktopStatus {
  mode: DesktopMode;
  platform: string;
  arch: string;
  hostname: string;
  goVersion: string;
  uptimeSeconds: number;
  uptimeFormatted: string;
  dataDir: string;
  logsDir: string;
  pid: number;
  numCpu: number;
}

type DoctorCheckName =
  | 'sqlite_integrity'
  | 'data_dir_writable'
  | 'logs_dir_writable'
  | 'easytier_binary'
  | 'runtime_memory';

interface DoctorCheckResult {
  name: DoctorCheckName | string;
  status: string;
  ok: boolean;
  detail: string;
  durationMs: number;
}

interface DoctorReport {
  overallStatus: string;
  healthy: boolean;
  checkedAt: string;
  checks: DoctorCheckResult[];
  supportReportMarkdown: string;
  repairActions: string[];
}

interface LocalAIModelEntry {
  id: string;
  name: string;
  provider: string;
  sizeBytes?: number;
  family?: string;
}

interface LocalAIProviderStatus {
  id: string;
  name: string;
  baseUrl: string;
  online: boolean;
  latencyMs: number;
  models: string[];
  error: string;
}

interface LocalAIDiscoveryResult {
  onlineCount: number;
  scannedAt: string;
  providers: LocalAIProviderStatus[];
  models: LocalAIModelEntry[];
  recommendedWorkspaceModel: string;
  recommendedDevModel: string;
}

interface MeshNodeRecord {
  id: string;
  label: string;
  baseUrl: string;
  tokenHint: string;
  tokenConfigured: boolean;
  lastStatus: string;
  lastProbeAt: string;
  createdAt: string;
  updatedAt: string;
}

interface MeshProbeOutcome {
  nodeId: string;
  status: string;
  healthy: boolean;
  latencyMs: number;
  detail: string;
  probedAt: string;
}

interface MeshJoinCommandResult {
  command: string;
  networkName: string;
  peerAddr: string;
  hostname: string;
}

interface GitWorktreeEntry {
  path: string;
  head: string;
  branch: string;
  bare: boolean;
  detached: boolean;
  locked: boolean;
  prunable: boolean;
}

interface RiskEvaluationResult {
  tier: string;
  allowed: boolean;
  requiresApproval: boolean;
  summary: string;
  reasons: string[];
  matchedRules: string[];
  redactedPreview: string;
}

interface DesktopLogEntry {
  id: string;
  level: string;
  source: string;
  message: string;
  detail: string;
  redacted: boolean;
  createdAt: string;
}

const REQUIRED_DOCTOR_CHECKS: DoctorCheckName[] = [
  'sqlite_integrity',
  'data_dir_writable',
  'logs_dir_writable',
  'easytier_binary',
  'runtime_memory',
];

const PRIVATE_MESH_IPV4_REGEX =
  /^https?:\/\/(?:10\.\d{1,3}\.\d{1,3}\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3})(?::\d{1,5})?(?:\/.*)?$/i;

function asRecord(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' ? (value as Record<string, unknown>) : {};
}

function pickString(record: Record<string, unknown>, keys: string[], fallback = ''): string {
  for (const key of keys) {
    const candidate = record[key];
    if (typeof candidate === 'string' && candidate.length > 0) {
      return candidate;
    }
  }
  return fallback;
}

function pickNumber(record: Record<string, unknown>, keys: string[], fallback = 0): number {
  for (const key of keys) {
    const candidate = record[key];
    if (typeof candidate === 'number' && Number.isFinite(candidate)) {
      return candidate;
    }
  }
  return fallback;
}

function pickBoolean(record: Record<string, unknown>, keys: string[], fallback: boolean): boolean {
  for (const key of keys) {
    const candidate = record[key];
    if (typeof candidate === 'boolean') {
      return candidate;
    }
  }
  return fallback;
}

function pickStringArray(record: Record<string, unknown>, keys: string[]): string[] {
  for (const key of keys) {
    const candidate = record[key];
    if (Array.isArray(candidate)) {
      return candidate
        .map((entry) => {
          if (typeof entry === 'string') return entry;
          const entryRec = asRecord(entry);
          return pickString(entryRec, ['name', 'id', 'model']);
        })
        .filter((entry) => entry.length > 0);
    }
  }
  return [];
}

async function requestDesktopJson<TResponse>(url: string, init?: RequestInit): Promise<TResponse> {
  const httpResponse = await fetch(url, init);
  const rawBody = await httpResponse.text();
  let parsedBody: unknown = {};

  if (rawBody.trim().length > 0) {
    try {
      parsedBody = JSON.parse(rawBody);
    } catch {
      if (!httpResponse.ok) {
        throw new Error(rawBody || `HTTP ${httpResponse.status}`);
      }
      throw new Error('Server returned invalid JSON payload');
    }
  }

  if (!httpResponse.ok) {
    const errRecord = asRecord(parsedBody);
    const nestedErr = asRecord(errRecord.error);
    const errMessage =
      pickString(nestedErr, ['message', 'error']) ||
      pickString(errRecord, ['error', 'message', 'detail']) ||
      `HTTP ${httpResponse.status}`;
    throw new Error(errMessage);
  }

  return parsedBody as TResponse;
}

function formatUptime(seconds: number): string {
  const safeSec = Math.max(0, Math.floor(seconds));
  const days = Math.floor(safeSec / 86400);
  const hours = Math.floor((safeSec % 86400) / 3600);
  const minutes = Math.floor((safeSec % 3600) / 60);
  const secs = safeSec % 60;
  if (days > 0) return `${days}d ${hours}h ${minutes}m`;
  if (hours > 0) return `${hours}h ${minutes}m ${secs}s`;
  if (minutes > 0) return `${minutes}m ${secs}s`;
  return `${secs}s`;
}

function normalizeMode(rawMode: string): DesktopMode {
  return rawMode.trim().toLowerCase() === 'developer' ? 'developer' : 'workspace';
}

function normalizeDesktopStatus(raw: unknown): DesktopStatus {
  const rec = asRecord(raw);
  const uptimeSeconds = pickNumber(rec, ['uptimeSeconds', 'uptime_seconds', 'uptimeSec', 'uptime_sec', 'uptime'], 0);
  return {
    mode: normalizeMode(pickString(rec, ['mode', 'desktopMode', 'desktop_mode'], 'workspace')),
    platform: pickString(rec, ['platform', 'os', 'goos'], 'unknown'),
    arch: pickString(rec, ['arch', 'goarch'], 'unknown'),
    hostname: pickString(rec, ['hostname', 'host'], 'local-host'),
    goVersion: pickString(rec, ['goVersion', 'go_version'], ''),
    uptimeSeconds,
    uptimeFormatted:
      pickString(rec, ['uptimeFormatted', 'uptime_formatted', 'uptimeHuman']) ||
      formatUptime(uptimeSeconds),
    dataDir: pickString(rec, ['dataDir', 'data_dir']),
    logsDir: pickString(rec, ['logsDir', 'logs_dir']),
    pid: pickNumber(rec, ['pid'], 0),
    numCpu: pickNumber(rec, ['numCpu', 'num_cpu', 'cpus'], 0),
  };
}

function normalizeDoctorCheck(raw: unknown): DoctorCheckResult {
  const rec = asRecord(raw);
  const statusText = pickString(rec, ['status', 'state'], 'unknown');
  const okFlag = pickBoolean(
    rec,
    ['ok', 'healthy', 'passed'],
    ['pass', 'passed', 'ok', 'healthy'].includes(statusText.toLowerCase()),
  );
  return {
    name: pickString(rec, ['name', 'id', 'check'], 'unknown'),
    status: statusText,
    ok: okFlag,
    detail: pickString(rec, ['detail', 'message', 'summary']),
    durationMs: pickNumber(rec, ['durationMs', 'duration_ms', 'latencyMs'], 0),
  };
}

function normalizeDoctorReport(raw: unknown): DoctorReport {
  const rec = asRecord(raw);
  const rawChecks = Array.isArray(rec.checks) ? rec.checks : [];
  const checks = rawChecks.map(normalizeDoctorCheck);
  const allOk = checks.length > 0 && checks.every((check) => check.ok);
  const overallStatus = pickString(
    rec,
    ['overallStatus', 'overall_status', 'status'],
    allOk ? 'healthy' : 'degraded',
  );
  return {
    overallStatus,
    healthy: pickBoolean(
      rec,
      ['healthy', 'ok'],
      overallStatus.toLowerCase() === 'healthy' || overallStatus.toLowerCase() === 'ok' || allOk,
    ),
    checkedAt: pickString(rec, ['checkedAt', 'checked_at', 'timestamp']),
    checks,
    supportReportMarkdown: pickString(rec, [
      'supportReportMarkdown',
      'support_report_markdown',
      'supportReport',
      'support_report',
      'markdown',
    ]),
    repairActions: pickStringArray(rec, ['repairActions', 'repair_actions', 'actionsTaken', 'actions']),
  };
}

function normalizeLocalAIDiscovery(raw: unknown): LocalAIDiscoveryResult {
  const rec = asRecord(raw);
  const rawProviders = Array.isArray(rec.providers) ? rec.providers : [];
  const providers: LocalAIProviderStatus[] = rawProviders.map((entry) => {
    const providerRec = asRecord(entry);
    return {
      id: pickString(providerRec, ['id', 'provider', 'name'], 'provider'),
      name: pickString(providerRec, ['name', 'provider', 'id'], 'Loopback AI'),
      baseUrl: pickString(providerRec, ['baseUrl', 'base_url', 'url', 'endpoint']),
      online: pickBoolean(providerRec, ['online', 'available', 'healthy'], false),
      latencyMs: pickNumber(providerRec, ['latencyMs', 'latency_ms'], 0),
      models: pickStringArray(providerRec, ['models', 'modelIds']),
      error: pickString(providerRec, ['error', 'err']),
    };
  });

  const rawModels = Array.isArray(rec.models) ? rec.models : [];
  const models: LocalAIModelEntry[] = rawModels.map((entry) => {
    if (typeof entry === 'string') {
      return { id: entry, name: entry, provider: 'loopback' };
    }
    const modelRec = asRecord(entry);
    const modelName = pickString(modelRec, ['name', 'id', 'model'], 'unknown-model');
    return {
      id: pickString(modelRec, ['id', 'name'], modelName),
      name: modelName,
      provider: pickString(modelRec, ['provider', 'source', 'runtime'], 'loopback'),
      sizeBytes: pickNumber(modelRec, ['sizeBytes', 'size_bytes', 'size'], 0) || undefined,
      family: pickString(modelRec, ['family', 'quantization']) || undefined,
    };
  });

  const computedOnlineCount = providers.filter((provider) => provider.online).length;
  return {
    onlineCount: pickNumber(rec, ['onlineCount', 'online_count'], computedOnlineCount),
    scannedAt: pickString(rec, ['scannedAt', 'scanned_at', 'timestamp']),
    providers,
    models,
    recommendedWorkspaceModel: pickString(rec, [
      'recommendedWorkspaceModel',
      'recommended_workspace_model',
    ]),
    recommendedDevModel: pickString(rec, ['recommendedDevModel', 'recommended_dev_model']),
  };
}

function normalizeMeshNode(raw: unknown): MeshNodeRecord {
  const rec = asRecord(raw);
  return {
    id: pickString(rec, ['id']),
    label: pickString(rec, ['label', 'name'], 'mesh-node'),
    baseUrl: pickString(rec, ['baseUrl', 'base_url', 'endpoint']),
    tokenHint: pickString(rec, ['tokenHint', 'token_hint']),
    tokenConfigured: pickBoolean(rec, ['tokenConfigured', 'token_configured'], false),
    lastStatus: pickString(rec, ['lastStatus', 'last_status', 'status'], 'unknown'),
    lastProbeAt: pickString(rec, ['lastProbeAt', 'last_probe_at']),
    createdAt: pickString(rec, ['createdAt', 'created_at']),
    updatedAt: pickString(rec, ['updatedAt', 'updated_at']),
  };
}

function normalizeWorktree(raw: unknown): GitWorktreeEntry {
  const rec = asRecord(raw);
  return {
    path: pickString(rec, ['path', 'worktree', 'dir']),
    head: pickString(rec, ['head', 'commit', 'sha']),
    branch: pickString(rec, ['branch', 'ref'], '(detached)'),
    bare: pickBoolean(rec, ['bare'], false),
    detached: pickBoolean(rec, ['detached'], false),
    locked: pickBoolean(rec, ['locked'], false),
    prunable: pickBoolean(rec, ['prunable'], false),
  };
}

function normalizeRiskEvaluation(raw: unknown): RiskEvaluationResult {
  const rec = asRecord(raw);
  const tier = pickString(rec, ['tier', 'riskTier', 'risk_tier'], 'R0').toUpperCase();
  return {
    tier,
    allowed: pickBoolean(rec, ['allowed'], tier === 'R0' || tier === 'R1'),
    requiresApproval: pickBoolean(
      rec,
      ['requiresApproval', 'requires_approval', 'needsApproval'],
      tier !== 'R0',
    ),
    summary: pickString(rec, ['summary', 'description', 'recommendation', 'message']),
    reasons: pickStringArray(rec, ['reasons', 'findings', 'triggers']),
    matchedRules: pickStringArray(rec, ['matchedRules', 'matched_rules', 'rules']),
    redactedPreview: pickString(rec, ['redactedPreview', 'redacted_preview', 'sanitizedInput']),
  };
}

function normalizeLogEntry(raw: unknown): DesktopLogEntry {
  const rec = asRecord(raw);
  return {
    id: pickString(rec, ['id']),
    level: pickString(rec, ['level', 'severity'], 'info').toLowerCase(),
    source: pickString(rec, ['source', 'component'], 'desktop'),
    message: pickString(rec, ['message', 'msg']),
    detail: pickString(rec, ['detail', 'metadata']),
    redacted: pickBoolean(rec, ['redacted'], false),
    createdAt: pickString(rec, ['createdAt', 'created_at', 'timestamp']),
  };
}

async function fetchDesktopStatus(): Promise<DesktopStatus> {
  const rawReply = await requestDesktopJson<unknown>('/api/desktop/status');
  return normalizeDesktopStatus(rawReply);
}

async function updateDesktopMode(mode: DesktopMode): Promise<DesktopStatus> {
  const rawReply = await requestDesktopJson<unknown>('/api/desktop/mode', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ mode }),
  });
  return normalizeDesktopStatus(rawReply);
}

async function runDesktopDoctor(): Promise<DoctorReport> {
  const rawReply = await requestDesktopJson<unknown>('/api/desktop/doctor');
  return normalizeDoctorReport(rawReply);
}

async function runDesktopDoctorRepair(): Promise<DoctorReport> {
  const rawReply = await requestDesktopJson<unknown>('/api/desktop/doctor/repair', {
    method: 'POST',
  });
  return normalizeDoctorReport(rawReply);
}

async function scanLocalAI(): Promise<LocalAIDiscoveryResult> {
  const rawReply = await requestDesktopJson<unknown>('/api/desktop/local-ai');
  return normalizeLocalAIDiscovery(rawReply);
}

async function fetchMeshNodes(): Promise<MeshNodeRecord[]> {
  const rawReply = await requestDesktopJson<unknown>('/api/mesh/nodes');
  const rec = asRecord(rawReply);
  const rawList = Array.isArray(rec.nodes) ? rec.nodes : Array.isArray(rawReply) ? rawReply : [];
  return rawList.map(normalizeMeshNode);
}

async function registerMeshNode(payload: {
  label: string;
  baseUrl: string;
  adminToken: string;
}): Promise<MeshNodeRecord> {
  const rawReply = await requestDesktopJson<unknown>('/api/mesh/nodes', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  const rec = asRecord(rawReply);
  return normalizeMeshNode(rec.node ?? rawReply);
}

async function deleteMeshNode(nodeId: string): Promise<void> {
  await requestDesktopJson<{ deleted?: boolean }>(`/api/mesh/nodes/${encodeURIComponent(nodeId)}`, {
    method: 'DELETE',
  });
}

async function probeMeshNode(nodeId: string): Promise<MeshProbeOutcome> {
  const rawReply = await requestDesktopJson<unknown>(
    `/api/mesh/nodes/${encodeURIComponent(nodeId)}/probe`,
    { method: 'POST' },
  );
  const rec = asRecord(rawReply);
  const status = pickString(rec, ['status', 'lastStatus', 'last_status'], 'unknown');
  return {
    nodeId: pickString(rec, ['nodeId', 'node_id', 'id'], nodeId),
    status,
    healthy: pickBoolean(
      rec,
      ['healthy', 'ok', 'online'],
      status.toLowerCase() === 'online' || status.toLowerCase() === 'healthy',
    ),
    latencyMs: pickNumber(rec, ['latencyMs', 'latency_ms'], 0),
    detail: pickString(rec, ['detail', 'error', 'message']),
    probedAt: pickString(rec, ['probedAt', 'probed_at', 'lastProbeAt', 'last_probe_at']),
  };
}

async function generateVpsJoinCommand(payload: {
  networkName: string;
  peerAddr: string;
  hostname: string;
}): Promise<MeshJoinCommandResult> {
  const rawReply = await requestDesktopJson<unknown>('/api/mesh/join-command', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  const rec = asRecord(rawReply);
  return {
    command: pickString(rec, ['command', 'joinCommand', 'join_command', 'shell']),
    networkName: pickString(rec, ['networkName', 'network_name'], payload.networkName),
    peerAddr: pickString(rec, ['peerAddr', 'peer_addr'], payload.peerAddr),
    hostname: pickString(rec, ['hostname'], payload.hostname),
  };
}

async function fetchGitWorktrees(repoDir: string): Promise<{ repoDir: string; worktrees: GitWorktreeEntry[] }> {
  const params = new URLSearchParams({ repoDir: repoDir.trim() });
  const rawReply = await requestDesktopJson<unknown>(`/api/mesh/worktrees?${params.toString()}`);
  const rec = asRecord(rawReply);
  const rawList = Array.isArray(rec.worktrees)
    ? rec.worktrees
    : Array.isArray(rawReply)
      ? rawReply
      : [];
  return {
    repoDir: pickString(rec, ['repoDir', 'repo_dir'], repoDir.trim()),
    worktrees: rawList.map(normalizeWorktree),
  };
}

async function createGitWorktree(payload: {
  repoDir: string;
  branch: string;
  targetDir: string;
}): Promise<void> {
  await requestDesktopJson<unknown>('/api/mesh/worktrees', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

async function removeGitWorktree(repoDir: string, targetDir: string): Promise<void> {
  const params = new URLSearchParams({
    repoDir: repoDir.trim(),
    targetDir: targetDir.trim(),
  });
  await requestDesktopJson<unknown>(`/api/mesh/worktrees?${params.toString()}`, {
    method: 'DELETE',
  });
}

async function evaluateRiskGate(payload: {
  action: string;
  targetPath: string;
  targetCommand: string;
  targetEndpointUrl: string;
}): Promise<RiskEvaluationResult> {
  const rawReply = await requestDesktopJson<unknown>('/api/desktop/risk/evaluate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  return normalizeRiskEvaluation(rawReply);
}

async function fetchDesktopLogs(filters: {
  level?: string;
  source?: string;
  limit?: number;
}): Promise<DesktopLogEntry[]> {
  const params = new URLSearchParams();
  if (filters.level && filters.level.trim()) params.set('level', filters.level.trim());
  if (filters.source && filters.source.trim()) params.set('source', filters.source.trim());
  params.set('limit', String(filters.limit ?? 120));
  const rawReply = await requestDesktopJson<unknown>(`/api/desktop/logs?${params.toString()}`);
  const rec = asRecord(rawReply);
  const rawList = Array.isArray(rec.logs) ? rec.logs : Array.isArray(rawReply) ? rawReply : [];
  return rawList.map(normalizeLogEntry);
}

function resolveRiskTierTone(tier: string): BadgeTone {
  const upper = (tier || '').toUpperCase();
  if (upper === 'R4' || upper === 'CRITICAL') return 'err';
  if (upper === 'R3' || upper === 'HIGH') return 'magenta';
  if (upper === 'R2' || upper === 'MEDIUM') return 'yellow';
  if (upper === 'R1' || upper === 'LOW') return 'cyan';
  return 'ok';
}

function resolveLogLevelTone(level: string): BadgeTone {
  const lowered = (level || '').toLowerCase();
  if (lowered === 'error' || lowered === 'err' || lowered === 'fatal') return 'err';
  if (lowered === 'warn' || lowered === 'warning') return 'yellow';
  if (lowered === 'debug') return 'neutral';
  return 'cyan';
}

function resolveNodeStatusTone(status: string): BadgeTone {
  const lowered = (status || '').toLowerCase();
  if (lowered === 'online' || lowered === 'healthy' || lowered === 'ok') return 'ok';
  if (lowered === 'offline' || lowered === 'error' || lowered === 'unreachable') return 'err';
  if (lowered === 'degraded') return 'yellow';
  return 'neutral';
}

function formatDoctorCheckLabel(checkName: string): string {
  switch (checkName) {
    case 'sqlite_integrity':
      return 'SQLITE INTEGRITY';
    case 'data_dir_writable':
      return 'DATA DIR WRITABLE';
    case 'logs_dir_writable':
      return 'LOGS DIR WRITABLE';
    case 'easytier_binary':
      return 'EASYTIER BINARY';
    case 'runtime_memory':
      return 'RUNTIME MEMORY';
    default:
      return checkName.replace(/_/g, ' ').toUpperCase();
  }
}

export function DesktopHostTab({ onNotify }: DesktopHostTabProps) {
  // 1. Desktop Runtime & Mode Switch
  const [status, setStatus] = useState<DesktopStatus | null>(null);
  const [loadingStatus, setLoadingStatus] = useState(false);
  const [switchingMode, setSwitchingMode] = useState(false);

  // 2. Doctor & Self-Repair
  const [doctorReport, setDoctorReport] = useState<DoctorReport | null>(null);
  const [runningDoctor, setRunningDoctor] = useState(false);
  const [repairingDoctor, setRepairingDoctor] = useState(false);
  const [copiedSupportReport, setCopiedSupportReport] = useState(false);

  // 3. Local AI Radar
  const [localAI, setLocalAI] = useState<LocalAIDiscoveryResult | null>(null);
  const [scanningLocalAI, setScanningLocalAI] = useState(false);

  // 4. Private Mesh VPS Fleet & Git Worktrees
  const [meshNodes, setMeshNodes] = useState<MeshNodeRecord[]>([]);
  const [loadingMeshNodes, setLoadingMeshNodes] = useState(false);
  const [nodeLabelInput, setNodeLabelInput] = useState('');
  const [nodeBaseUrlInput, setNodeBaseUrlInput] = useState('http://100.64.0.10:4318');
  const [nodeTokenInput, setNodeTokenInput] = useState('');
  const [registeringNode, setRegisteringNode] = useState(false);
  const [probingNodeId, setProbingNodeId] = useState<string | null>(null);
  const [deletingNodeId, setDeletingNodeId] = useState<string | null>(null);

  const [joinNetworkName, setJoinNetworkName] = useState('mw-mesh');
  const [joinPeerAddr, setJoinPeerAddr] = useState('tcp://100.64.0.1:11010');
  const [joinHostname, setJoinHostname] = useState('mw-vps-node-01');
  const [joinCommandResult, setJoinCommandResult] = useState<MeshJoinCommandResult | null>(null);
  const [generatingJoinCmd, setGeneratingJoinCmd] = useState(false);

  const [repoDirInput, setRepoDirInput] = useState('Z:\\repo\\mw-echo');
  const [worktrees, setWorktrees] = useState<GitWorktreeEntry[]>([]);
  const [loadingWorktrees, setLoadingWorktrees] = useState(false);
  const [newWorktreeBranch, setNewWorktreeBranch] = useState('');
  const [newWorktreeTargetDir, setNewWorktreeTargetDir] = useState('');
  const [mutatingWorktree, setMutatingWorktree] = useState(false);

  // 5. Risk Gate Sandbox & Redacted Log Hub
  const [riskAction, setRiskAction] = useState('shell.exec');
  const [riskTargetPath, setRiskTargetPath] = useState('.env');
  const [riskTargetCommand, setRiskTargetCommand] = useState('git status');
  const [riskTargetEndpoint, setRiskTargetEndpoint] = useState('');
  const [evaluatingRisk, setEvaluatingRisk] = useState(false);
  const [riskResult, setRiskResult] = useState<RiskEvaluationResult | null>(null);

  const [logs, setLogs] = useState<DesktopLogEntry[]>([]);
  const [loadingLogs, setLoadingLogs] = useState(false);
  const [logLevelFilter, setLogLevelFilter] = useState('');
  const [logSourceFilter, setLogSourceFilter] = useState('');
  const [logSearchText, setLogSearchText] = useState('');

  const loadDesktopBootstrap = useCallback(async () => {
    setLoadingStatus(true);
    setLoadingMeshNodes(true);
    setLoadingLogs(true);
    try {
      const [statusOutcome, doctorOutcome, nodesOutcome, logsOutcome] = await Promise.allSettled([
        fetchDesktopStatus(),
        runDesktopDoctor(),
        fetchMeshNodes(),
        fetchDesktopLogs({ limit: 120 }),
      ]);
      if (statusOutcome.status === 'fulfilled') {
        setStatus(statusOutcome.value);
      }
      if (doctorOutcome.status === 'fulfilled') {
        setDoctorReport(doctorOutcome.value);
      }
      if (nodesOutcome.status === 'fulfilled') {
        setMeshNodes(nodesOutcome.value);
      }
      if (logsOutcome.status === 'fulfilled') {
        setLogs(logsOutcome.value);
      }
    } finally {
      setLoadingStatus(false);
      setLoadingMeshNodes(false);
      setLoadingLogs(false);
    }
  }, []);

  useEffect(() => {
    void loadDesktopBootstrap();
  }, [loadDesktopBootstrap]);

  const handleSwitchMode = async (nextMode: DesktopMode) => {
    if (status?.mode === nextMode) return;
    setSwitchingMode(true);
    try {
      const updatedStatus = await updateDesktopMode(nextMode);
      setStatus(updatedStatus);
      onNotify('ok', `Desktop host switched to ${updatedStatus.mode.toUpperCase()} mode.`);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to update desktop mode';
      onNotify('err', message);
    } finally {
      setSwitchingMode(false);
    }
  };

  const handleRunDoctor = async () => {
    setRunningDoctor(true);
    try {
      const report = await runDesktopDoctor();
      setDoctorReport(report);
      onNotify(
        report.healthy ? 'ok' : 'info',
        `Doctor finished: ${report.overallStatus.toUpperCase()} (${report.checks.filter((check) => check.ok).length}/${report.checks.length} checks passed).`,
      );
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Doctor check failed';
      onNotify('err', message);
    } finally {
      setRunningDoctor(false);
    }
  };

  const handleAutoRepair = async () => {
    setRepairingDoctor(true);
    try {
      const report = await runDesktopDoctorRepair();
      setDoctorReport(report);
      onNotify(
        report.healthy ? 'ok' : 'info',
        `Auto-repair completed (${report.overallStatus.toUpperCase()}).`,
      );
      const refreshedLogs = await fetchDesktopLogs({
        level: logLevelFilter,
        source: logSourceFilter,
        limit: 120,
      }).catch(() => null);
      if (refreshedLogs) {
        setLogs(refreshedLogs);
      }
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Auto-repair failed';
      onNotify('err', message);
    } finally {
      setRepairingDoctor(false);
    }
  };

  const handleCopySupportReport = async () => {
    const markdownReport =
      doctorReport?.supportReportMarkdown ||
      [
        '# MW-ECHO Desktop Support Report',
        `- Mode: ${status?.mode ?? 'unknown'}`,
        `- Platform: ${status?.platform ?? 'unknown'} / ${status?.arch ?? 'unknown'}`,
        `- Overall Health: ${doctorReport?.overallStatus ?? 'not-run'}`,
        ...(doctorReport?.checks ?? []).map(
          (check) => `- ${check.name}: ${check.status} (${check.detail || 'ok'})`,
        ),
      ].join('\n');

    const copied = await copyTextToClipboard(markdownReport);
    if (copied) {
      setCopiedSupportReport(true);
      onNotify('ok', 'Copied redacted Markdown Support Report to clipboard.');
      window.setTimeout(() => setCopiedSupportReport(false), 1800);
    } else {
      onNotify('err', 'Clipboard access blocked while copying Support Report.');
    }
  };

  const handleScanLocalAI = async () => {
    setScanningLocalAI(true);
    try {
      const discovery = await scanLocalAI();
      setLocalAI(discovery);
      onNotify(
        discovery.onlineCount > 0 ? 'ok' : 'info',
        `Loopback AI Radar: ${discovery.onlineCount} online provider(s), ${discovery.models.length} model(s) discovered.`,
      );
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Loopback AI scan failed';
      onNotify('err', message);
    } finally {
      setScanningLocalAI(false);
    }
  };

  const handleRegisterMeshNode = async (ev: React.FormEvent) => {
    ev.preventDefault();
    const cleanLabel = nodeLabelInput.trim();
    const cleanUrl = nodeBaseUrlInput.trim();
    if (!cleanLabel) {
      onNotify('err', 'Mesh node label is required.');
      return;
    }
    if (!PRIVATE_MESH_IPV4_REGEX.test(cleanUrl)) {
      onNotify(
        'err',
        'Private mesh URL must target RFC1918 (10.x / 172.16-31.x / 192.168.x) or CGNAT (100.64-127.x) HTTP/HTTPS endpoint.',
      );
      return;
    }
    setRegisteringNode(true);
    try {
      const created = await registerMeshNode({
        label: cleanLabel,
        baseUrl: cleanUrl,
        adminToken: nodeTokenInput.trim(),
      });
      setMeshNodes((prev) => [created, ...prev.filter((node) => node.id !== created.id)]);
      setNodeLabelInput('');
      setNodeTokenInput('');
      onNotify('ok', `Registered private mesh node "${created.label}".`);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to register mesh node';
      onNotify('err', message);
    } finally {
      setRegisteringNode(false);
    }
  };

  const handleProbeNode = async (node: MeshNodeRecord) => {
    setProbingNodeId(node.id);
    try {
      const outcome = await probeMeshNode(node.id);
      setMeshNodes((prev) =>
        prev.map((existing) =>
          existing.id === node.id
            ? {
                ...existing,
                lastStatus: outcome.status,
                lastProbeAt: outcome.probedAt || new Date().toISOString(),
              }
            : existing,
        ),
      );
      onNotify(
        outcome.healthy ? 'ok' : 'err',
        `Probe ${node.label}: ${outcome.status.toUpperCase()}${outcome.latencyMs ? ` (${outcome.latencyMs}ms)` : ''}${outcome.detail ? ` — ${outcome.detail}` : ''}`,
      );
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : `Probe failed for ${node.label}`;
      onNotify('err', message);
    } finally {
      setProbingNodeId(null);
    }
  };

  const handleDeleteNode = async (node: MeshNodeRecord) => {
    setDeletingNodeId(node.id);
    try {
      await deleteMeshNode(node.id);
      setMeshNodes((prev) => prev.filter((existing) => existing.id !== node.id));
      onNotify('info', `Removed mesh node "${node.label}".`);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to delete mesh node';
      onNotify('err', message);
    } finally {
      setDeletingNodeId(null);
    }
  };

  const handleGenerateJoinCommand = async (ev: React.FormEvent) => {
    ev.preventDefault();
    if (!joinNetworkName.trim() || !joinPeerAddr.trim()) {
      onNotify('err', 'Network name and peer address are required for join command.');
      return;
    }
    setGeneratingJoinCmd(true);
    try {
      const generated = await generateVpsJoinCommand({
        networkName: joinNetworkName.trim(),
        peerAddr: joinPeerAddr.trim(),
        hostname: joinHostname.trim() || 'mw-vps-node',
      });
      setJoinCommandResult(generated);
      onNotify('ok', `Generated EasyTier VPS join command for "${generated.hostname}".`);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to generate join command';
      onNotify('err', message);
    } finally {
      setGeneratingJoinCmd(false);
    }
  };

  const handleInspectWorktrees = async (ev?: React.FormEvent) => {
    if (ev) ev.preventDefault();
    const cleanRepo = repoDirInput.trim();
    if (!cleanRepo) {
      onNotify('err', 'Repository path is required.');
      return;
    }
    setLoadingWorktrees(true);
    try {
      const result = await fetchGitWorktrees(cleanRepo);
      setWorktrees(result.worktrees);
      onNotify('ok', `Loaded ${result.worktrees.length} Git worktree(s) from ${result.repoDir}.`);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to inspect Git worktrees';
      onNotify('err', message);
    } finally {
      setLoadingWorktrees(false);
    }
  };

  const handleCreateWorktree = async (ev: React.FormEvent) => {
    ev.preventDefault();
    if (!repoDirInput.trim() || !newWorktreeBranch.trim() || !newWorktreeTargetDir.trim()) {
      onNotify('err', 'Repo directory, branch, and target directory are required.');
      return;
    }
    setMutatingWorktree(true);
    try {
      await createGitWorktree({
        repoDir: repoDirInput.trim(),
        branch: newWorktreeBranch.trim(),
        targetDir: newWorktreeTargetDir.trim(),
      });
      setNewWorktreeBranch('');
      setNewWorktreeTargetDir('');
      onNotify('ok', 'Created isolated Git worktree sandbox.');
      await handleInspectWorktrees();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to create worktree';
      onNotify('err', message);
    } finally {
      setMutatingWorktree(false);
    }
  };

  const handleRemoveWorktree = async (targetDir: string) => {
    if (!repoDirInput.trim()) return;
    setMutatingWorktree(true);
    try {
      await removeGitWorktree(repoDirInput.trim(), targetDir);
      onNotify('info', `Removed worktree ${targetDir}.`);
      await handleInspectWorktrees();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to remove worktree';
      onNotify('err', message);
    } finally {
      setMutatingWorktree(false);
    }
  };

  const handleEvaluateRisk = async (ev: React.FormEvent) => {
    ev.preventDefault();
    setEvaluatingRisk(true);
    try {
      const evaluated = await evaluateRiskGate({
        action: riskAction.trim() || 'shell.exec',
        targetPath: riskTargetPath.trim(),
        targetCommand: riskTargetCommand.trim(),
        targetEndpointUrl: riskTargetEndpoint.trim(),
      });
      setRiskResult(evaluated);
      onNotify('info', `Risk Gate classified action as ${evaluated.tier}.`);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Risk Gate evaluation failed';
      onNotify('err', message);
    } finally {
      setEvaluatingRisk(false);
    }
  };

  const handleRefreshLogs = useCallback(async () => {
    setLoadingLogs(true);
    try {
      const fetchedLogs = await fetchDesktopLogs({
        level: logLevelFilter,
        source: logSourceFilter,
        limit: 120,
      });
      setLogs(fetchedLogs);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to load redacted logs';
      onNotify('err', message);
    } finally {
      setLoadingLogs(false);
    }
  }, [logLevelFilter, logSourceFilter, onNotify]);

  const doctorChecksByName = useMemo(() => {
    const checkMap = new Map<string, DoctorCheckResult>();
    for (const check of doctorReport?.checks ?? []) {
      checkMap.set(check.name, check);
    }
    return checkMap;
  }, [doctorReport]);

  const filteredLogs = useMemo(() => {
    const needle = logSearchText.trim().toLowerCase();
    return logs.filter((entry) => {
      if (logLevelFilter && entry.level !== logLevelFilter.toLowerCase()) return false;
      if (logSourceFilter && !entry.source.toLowerCase().includes(logSourceFilter.toLowerCase())) {
        return false;
      }
      if (!needle) return true;
      return (
        entry.message.toLowerCase().includes(needle) ||
        entry.detail.toLowerCase().includes(needle) ||
        entry.source.toLowerCase().includes(needle)
      );
    });
  }, [logs, logLevelFilter, logSourceFilter, logSearchText]);

  const activeMode: DesktopMode = status?.mode ?? 'workspace';
  const passedDoctorCount =
    doctorReport?.checks.filter((check) => check.ok).length ?? 0;

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="STAGE 16 // DESKTOP HOST"
        sticker={activeMode === 'developer' ? 'DX OVERDRIVE' : 'WORKSPACE SAFE'}
        title="DESKTOP HOST, DOCTOR & PRIVATE MESH"
        subtitle="Unified pure-Go host runtime, self-repair diagnostics, strict loopback AI radar, RFC1918/CGNAT mesh fleet, Git worktree sandbox & Risk Gate."
        tone={activeMode === 'developer' ? 'magenta' : 'cyan'}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <div className="inline-flex border-[2.5px] border-black bg-[#08080c] p-0.5 shadow-[4px_4px_0_#000]">
              <button
                type="button"
                disabled={switchingMode}
                onClick={() => void handleSwitchMode('workspace')}
                className={`px-3 py-1.5 font-mono text-xs font-extrabold uppercase transition-colors ${
                  activeMode === 'workspace'
                    ? 'bg-[#00f0ff] text-[#08080c]'
                    : 'text-[#a6a2b8] hover:text-[#fff6de]'
                }`}
              >
                WORKSPACE
              </button>
              <button
                type="button"
                disabled={switchingMode}
                onClick={() => void handleSwitchMode('developer')}
                className={`px-3 py-1.5 font-mono text-xs font-extrabold uppercase transition-colors ${
                  activeMode === 'developer'
                    ? 'bg-[#ff2e88] text-white'
                    : 'text-[#a6a2b8] hover:text-[#fff6de]'
                }`}
              >
                DEVELOPER (DX)
              </button>
            </div>
            <button
              type="button"
              onClick={() => void loadDesktopBootstrap()}
              disabled={loadingStatus}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingStatus ? 'animate-spin' : ''}`} />
              SYNC HOST
            </button>
          </div>
        }
      />

      {/* 1. DESKTOP RUNTIME STAT TILES */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="HOST PLATFORM"
          value={(status?.platform || '—').toUpperCase()}
          sublabel={`ARCH: ${(status?.arch || '—').toUpperCase()} ${status?.numCpu ? `// ${status.numCpu} CPU` : ''}`}
          icon={<Laptop className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="RUNTIME UPTIME"
          value={status?.uptimeFormatted || '—'}
          sublabel={status?.hostname ? `NODE: ${status.hostname}` : 'LOCAL RUNTIME'}
          icon={<Activity className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="EXPERIENCE MODE"
          value={activeMode === 'developer' ? 'DEVELOPER DX' : 'WORKSPACE'}
          sublabel={activeMode === 'developer' ? 'FULL TOOLING & WORKTREES' : 'GUARDED DAILY OPS'}
          icon={<Zap className="h-4 w-4" />}
          tone={activeMode === 'developer' ? 'magenta' : 'ok'}
        />
        <StatTile
          label="DOCTOR HEALTH"
          value={
            doctorReport
              ? `${passedDoctorCount}/${doctorReport.checks.length || 5}`
              : 'UNSCANNED'
          }
          sublabel={
            doctorReport
              ? doctorReport.healthy
                ? 'ALL SUBSYSTEMS NOMINAL'
                : 'ACTION REQUIRED'
              : 'RUN SELF-DIAGNOSTICS'
          }
          icon={<HeartPulse className="h-4 w-4" />}
          tone={doctorReport ? (doctorReport.healthy ? 'ok' : 'err') : 'cream'}
        />
      </div>

      {/* 2. DOCTOR & SELF-REPAIR */}
      <SectionCard
        title="DOCTOR & SELF-REPAIR"
        subtitle="Deterministic integrity probes for SQLite, writable directories, EasyTier mesh binary & Go runtime memory."
        icon={<HeartPulse className="h-4 w-4" />}
        sticker={doctorReport?.healthy ? '5/5 NOMINAL' : 'DIAGNOSTICS'}
        stickerTone={doctorReport?.healthy ? 'ok' : 'yellow'}
        accent={doctorReport?.healthy ? 'ok' : 'yellow'}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => void handleRunDoctor()}
              disabled={runningDoctor}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#00f0ff] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${runningDoctor ? 'animate-spin' : ''}`} />
              {runningDoctor ? 'PROBING...' : 'RUN DOCTOR'}
            </button>
            <button
              type="button"
              onClick={() => void handleAutoRepair()}
              disabled={repairingDoctor}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#a3ff12] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
            >
              <Wrench className={`h-3.5 w-3.5 ${repairingDoctor ? 'animate-spin' : ''}`} />
              {repairingDoctor ? 'REPAIRING...' : 'AUTO-REPAIR'}
            </button>
            <button
              type="button"
              onClick={() => void handleCopySupportReport()}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#fff6de] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
            >
              {copiedSupportReport ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
              {copiedSupportReport ? 'COPIED MD!' : 'COPY SUPPORT REPORT'}
            </button>
          </div>
        }
      >
        <div className="grid grid-cols-1 gap-3 md:grid-cols-5">
          {REQUIRED_DOCTOR_CHECKS.map((checkName) => {
            const checkRecord = doctorChecksByName.get(checkName);
            const badgeTone: BadgeTone = !checkRecord
              ? 'neutral'
              : checkRecord.ok
                ? 'ok'
                : 'err';
            return (
              <div
                key={checkName}
                className="flex flex-col justify-between border-2 border-black bg-[#08080c] p-3 shadow-[3px_3px_0_#000]"
              >
                <div className="flex items-start justify-between gap-2">
                  <span className="font-mono text-[11px] font-extrabold text-[#fff6de]">
                    {formatDoctorCheckLabel(checkName)}
                  </span>
                  {checkRecord ? (
                    checkRecord.ok ? (
                      <CheckCircle2 className="h-4 w-4 shrink-0 text-[#a3ff12]" />
                    ) : (
                      <XCircle className="h-4 w-4 shrink-0 text-[#ff334b]" />
                    )
                  ) : (
                    <AlertTriangle className="h-4 w-4 shrink-0 text-[#a6a2b8]" />
                  )}
                </div>
                <div className="mt-2 flex flex-wrap items-center gap-1.5">
                  <MonoBadge tone={badgeTone}>
                    {checkRecord ? checkRecord.status.toUpperCase() : 'PENDING'}
                  </MonoBadge>
                  {checkRecord?.durationMs ? (
                    <MonoBadge tone="neutral">{checkRecord.durationMs}ms</MonoBadge>
                  ) : null}
                </div>
                <p className="mt-2 line-clamp-2 font-mono text-[11px] text-[#a6a2b8]">
                  {checkRecord?.detail || 'Awaiting doctor probe'}
                </p>
              </div>
            );
          })}
        </div>

        {doctorReport?.supportReportMarkdown ? (
          <div className="mt-4">
            <CopyableSnippet
              label="REDACTED SUPPORT REPORT (MARKDOWN)"
              value={doctorReport.supportReportMarkdown.replace(/\s+/g, ' ').slice(0, 220)}
              tone="cream"
            />
          </div>
        ) : null}
      </SectionCard>

      {/* 3. LOCAL AI RADAR (OLLAMA / LM STUDIO / vLLM) */}
      <SectionCard
        title="LOCAL AI RADAR // LOOPBACK ONLY"
        subtitle="Strict 127.0.0.1 / localhost / ::1 probe across Ollama (:11434), LM Studio (:1234) & vLLM (:8000)."
        icon={<Radar className="h-4 w-4" />}
        sticker={localAI ? `${localAI.onlineCount} ONLINE` : 'LOOPBACK GUARD'}
        stickerTone={localAI && localAI.onlineCount > 0 ? 'ok' : 'cyan'}
        accent="cyan"
        actions={
          <button
            type="button"
            onClick={() => void handleScanLocalAI()}
            disabled={scanningLocalAI}
            className="riot-btn inline-flex items-center gap-1.5 bg-[#00f0ff] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
          >
            <Radar className={`h-3.5 w-3.5 ${scanningLocalAI ? 'animate-spin' : ''}`} />
            {scanningLocalAI ? 'SCANNING LOOPBACK...' : 'SCAN LOOPBACK AI'}
          </button>
        }
      >
        <div className="mb-4 grid grid-cols-1 gap-3 md:grid-cols-2">
          <div className="flex items-center justify-between border-2 border-black bg-[#08080c] px-3.5 py-2.5 shadow-[3px_3px_0_#000]">
            <div>
              <div className="font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                RECOMMENDED WORKSPACE MODEL
              </div>
              <div className="mt-0.5 flex items-center gap-2 font-mono text-xs font-extrabold text-[#00f0ff]">
                <Bot className="h-3.5 w-3.5" />
                {localAI?.recommendedWorkspaceModel || 'Run scan to auto-select'}
              </div>
            </div>
            <MonoBadge tone="cyan">WORKSPACE</MonoBadge>
          </div>
          <div className="flex items-center justify-between border-2 border-black bg-[#08080c] px-3.5 py-2.5 shadow-[3px_3px_0_#000]">
            <div>
              <div className="font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                RECOMMENDED DEVELOPER (DX) MODEL
              </div>
              <div className="mt-0.5 flex items-center gap-2 font-mono text-xs font-extrabold text-[#ff2e88]">
                <Sparkles className="h-3.5 w-3.5" />
                {localAI?.recommendedDevModel || 'Prefers coder / deepseek / qwen-coder'}
              </div>
            </div>
            <MonoBadge tone="magenta">DEV DX</MonoBadge>
          </div>
        </div>

        {localAI && localAI.providers.length > 0 ? (
          <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
            {localAI.providers.map((provider) => (
              <div
                key={`${provider.id}-${provider.baseUrl}`}
                className="border-2 border-black bg-[#08080c] p-3 shadow-[3px_3px_0_#000]"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="font-display text-xs uppercase text-[#fff6de]">
                    {provider.name}
                  </span>
                  <div className="flex items-center gap-1.5">
                    <MonoBadge tone={provider.online ? 'ok' : 'neutral'}>
                      {provider.online ? 'ONLINE' : 'OFFLINE'}
                    </MonoBadge>
                    {provider.latencyMs > 0 ? (
                      <MonoBadge tone="yellow">{provider.latencyMs}ms</MonoBadge>
                    ) : null}
                  </div>
                </div>
                <div className="mt-1 truncate font-mono text-[11px] text-[#a6a2b8]">
                  {provider.baseUrl}
                </div>
                <div className="mt-2.5 flex flex-wrap gap-1.5">
                  {provider.models.length > 0 ? (
                    provider.models.slice(0, 8).map((modelName) => (
                      <MonoBadge key={modelName} tone="cream">
                        {modelName}
                      </MonoBadge>
                    ))
                  ) : (
                    <span className="font-mono text-[11px] text-[#a6a2b8]">
                      {provider.error || 'No models reported'}
                    </span>
                  )}
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div className="border-2 border-dashed border-[#353552] bg-[#08080c] px-4 py-5 text-center font-mono text-xs text-[#a6a2b8]">
            Click <strong className="text-[#00f0ff]">SCAN LOOPBACK AI</strong> to probe local Ollama, LM Studio, and vLLM servers.
          </div>
        )}
      </SectionCard>

      {/* 4. PRIVATE MESH VPS FLEET & GIT WORKTREES */}
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <SectionCard
          title="PRIVATE MESH VPS FLEET"
          subtitle="Zero public-IP SSRF: restricted strictly to 10.x, 172.16-31.x, 192.168.x & 100.64.x EasyTier mesh."
          icon={<Network className="h-4 w-4" />}
          sticker={`${meshNodes.length} NODES`}
          stickerTone="yellow"
          accent="yellow"
          actions={
            <button
              type="button"
              onClick={() => void loadDesktopBootstrap()}
              disabled={loadingMeshNodes}
              className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#fff6de]"
            >
              <RefreshCw className={`h-3 w-3 ${loadingMeshNodes ? 'animate-spin' : ''}`} />
              REFRESH
            </button>
          }
        >
          <form onSubmit={handleRegisterMeshNode} className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
            <input
              type="text"
              value={nodeLabelInput}
              onChange={(ev) => setNodeLabelInput(ev.target.value)}
              placeholder="Node label (e.g. sg-vps-01)"
              className="riot-input px-3 py-2 font-mono text-xs"
            />
            <input
              type="text"
              value={nodeBaseUrlInput}
              onChange={(ev) => setNodeBaseUrlInput(ev.target.value)}
              placeholder="http://100.64.0.10:4318"
              className="riot-input px-3 py-2 font-mono text-xs"
            />
            <div className="flex gap-2">
              <input
                type="password"
                value={nodeTokenInput}
                onChange={(ev) => setNodeTokenInput(ev.target.value)}
                placeholder="Admin token (optional)"
                className="riot-input min-w-0 flex-1 px-3 py-2 font-mono text-xs"
              />
              <button
                type="submit"
                disabled={registeringNode}
                className="riot-btn inline-flex shrink-0 items-center gap-1 bg-[#ffe031] px-3 py-2 font-mono text-xs font-extrabold text-[#08080c]"
              >
                <Plus className="h-3.5 w-3.5" />
                ADD
              </button>
            </div>
          </form>

          <div className="mt-4 space-y-2">
            {meshNodes.length > 0 ? (
              meshNodes.map((node) => (
                <div
                  key={node.id}
                  className="flex flex-wrap items-center justify-between gap-2 border-2 border-black bg-[#08080c] px-3 py-2.5 shadow-[3px_3px_0_#000]"
                >
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <Server className="h-3.5 w-3.5 text-[#ffe031]" />
                      <span className="font-mono text-xs font-extrabold text-[#fff6de]">
                        {node.label}
                      </span>
                      <MonoBadge tone={resolveNodeStatusTone(node.lastStatus)}>
                        {node.lastStatus.toUpperCase()}
                      </MonoBadge>
                      {node.tokenConfigured ? (
                        <MonoBadge tone="cyan">{node.tokenHint || 'TOKEN SET'}</MonoBadge>
                      ) : null}
                    </div>
                    <div className="mt-1 font-mono text-[11px] text-[#a6a2b8]">
                      {node.baseUrl}
                      {node.lastProbeAt ? ` // probed ${formatShortTime(node.lastProbeAt)}` : ''}
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <button
                      type="button"
                      onClick={() => void handleProbeNode(node)}
                      disabled={probingNodeId === node.id}
                      className="riot-btn inline-flex items-center gap-1 bg-[#00f0ff] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#08080c]"
                    >
                      <Play className="h-3 w-3" />
                      {probingNodeId === node.id ? 'PROBING...' : 'PROBE'}
                    </button>
                    <button
                      type="button"
                      onClick={() => void handleDeleteNode(node)}
                      disabled={deletingNodeId === node.id}
                      aria-label={`Remove ${node.label}`}
                      className="riot-btn inline-flex items-center bg-[#ff334b] px-2 py-1 text-white"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  </div>
                </div>
              ))
            ) : (
              <div className="border-2 border-dashed border-[#353552] bg-[#08080c] px-3 py-4 text-center font-mono text-xs text-[#a6a2b8]">
                No private mesh VPS nodes registered yet.
              </div>
            )}
          </div>

          {/* VPS JOIN COMMAND GENERATOR */}
          <div className="mt-5 border-t-2 border-black pt-4">
            <div className="mb-2 flex items-center gap-2">
              <Terminal className="h-4 w-4 text-[#00f0ff]" />
              <span className="font-display text-xs uppercase text-[#fff6de]">
                VPS JOIN COMMAND GENERATOR
              </span>
            </div>
            <form onSubmit={handleGenerateJoinCommand} className="grid grid-cols-1 gap-2 sm:grid-cols-4">
              <input
                type="text"
                value={joinNetworkName}
                onChange={(ev) => setJoinNetworkName(ev.target.value)}
                placeholder="Network name"
                className="riot-input px-2.5 py-1.5 font-mono text-xs"
              />
              <input
                type="text"
                value={joinPeerAddr}
                onChange={(ev) => setJoinPeerAddr(ev.target.value)}
                placeholder="tcp://100.64.0.1:11010"
                className="riot-input px-2.5 py-1.5 font-mono text-xs"
              />
              <input
                type="text"
                value={joinHostname}
                onChange={(ev) => setJoinHostname(ev.target.value)}
                placeholder="VPS hostname"
                className="riot-input px-2.5 py-1.5 font-mono text-xs"
              />
              <button
                type="submit"
                disabled={generatingJoinCmd}
                className="riot-btn inline-flex items-center justify-center gap-1 bg-[#a3ff12] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
              >
                <Zap className="h-3.5 w-3.5" />
                {generatingJoinCmd ? 'BUILDING...' : 'BUILD JOIN CMD'}
              </button>
            </form>
            {joinCommandResult?.command ? (
              <div className="mt-3">
                <CopyableSnippet
                  label={`EASYTIER JOIN // ${joinCommandResult.hostname}`}
                  value={joinCommandResult.command}
                  tone="ok"
                />
              </div>
            ) : null}
          </div>
        </SectionCard>

        {/* GIT WORKTREE SANDBOX INSPECTOR */}
        <SectionCard
          title="GIT WORKTREE SANDBOX"
          subtitle="Inspect isolated agent worktrees with strict path containment (rejects .. traversal)."
          icon={<FolderGit2 className="h-4 w-4" />}
          sticker={`${worktrees.length} TREES`}
          stickerTone="magenta"
          accent="magenta"
        >
          <form onSubmit={handleInspectWorktrees} className="flex flex-wrap gap-2">
            <input
              type="text"
              value={repoDirInput}
              onChange={(ev) => setRepoDirInput(ev.target.value)}
              placeholder="Z:\repo\mw-echo"
              className="riot-input min-w-0 flex-1 px-3 py-2 font-mono text-xs"
            />
            <button
              type="submit"
              disabled={loadingWorktrees}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ff2e88] px-3.5 py-2 font-mono text-xs font-extrabold text-white"
            >
              <Search className={`h-3.5 w-3.5 ${loadingWorktrees ? 'animate-spin' : ''}`} />
              {loadingWorktrees ? 'INSPECTING...' : 'INSPECT WORKTREES'}
            </button>
          </form>

          <form onSubmit={handleCreateWorktree} className="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-3">
            <input
              type="text"
              value={newWorktreeBranch}
              onChange={(ev) => setNewWorktreeBranch(ev.target.value)}
              placeholder="Branch (e.g. sandbox/feat-1)"
              className="riot-input px-2.5 py-1.5 font-mono text-xs"
            />
            <input
              type="text"
              value={newWorktreeTargetDir}
              onChange={(ev) => setNewWorktreeTargetDir(ev.target.value)}
              placeholder="Target dir (inside repo parent)"
              className="riot-input px-2.5 py-1.5 font-mono text-xs"
            />
            <button
              type="submit"
              disabled={mutatingWorktree}
              className="riot-btn inline-flex items-center justify-center gap-1 bg-[#fff6de] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
            >
              <Plus className="h-3.5 w-3.5" />
              SPAWN SANDBOX
            </button>
          </form>

          <div className="mt-4 space-y-2">
            {worktrees.length > 0 ? (
              worktrees.map((tree) => (
                <div
                  key={tree.path}
                  className="flex flex-wrap items-center justify-between gap-2 border-2 border-black bg-[#08080c] px-3 py-2.5 shadow-[3px_3px_0_#000]"
                >
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <GitBranch className="h-3.5 w-3.5 text-[#ff2e88]" />
                      <span className="truncate font-mono text-xs font-extrabold text-[#fff6de]">
                        {tree.branch || '(detached)'}
                      </span>
                      {tree.head ? (
                        <MonoBadge tone="neutral">{tree.head.slice(0, 8)}</MonoBadge>
                      ) : null}
                      {tree.locked ? <MonoBadge tone="yellow">LOCKED</MonoBadge> : null}
                      {tree.prunable ? <MonoBadge tone="err">PRUNABLE</MonoBadge> : null}
                    </div>
                    <div className="mt-1 truncate font-mono text-[11px] text-[#a6a2b8]">
                      {tree.path}
                    </div>
                  </div>
                  <button
                    type="button"
                    disabled={mutatingWorktree}
                    onClick={() => void handleRemoveWorktree(tree.path)}
                    className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#ff334b] hover:bg-[#ff334b] hover:text-white"
                  >
                    <Trash2 className="h-3 w-3" />
                    REMOVE
                  </button>
                </div>
              ))
            ) : (
              <div className="border-2 border-dashed border-[#353552] bg-[#08080c] px-3 py-5 text-center font-mono text-xs text-[#a6a2b8]">
                Click <strong className="text-[#ff2e88]">INSPECT WORKTREES</strong> to enumerate active sandboxes for a Git repository.
              </div>
            )}
          </div>
        </SectionCard>
      </div>

      {/* 5. RISK GATE SANDBOX & REDACTED LOG HUB */}
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
        <SectionCard
          title="RISK GATE SANDBOX // R0–R4"
          subtitle="Test path, command & remote egress rules against the pure-Go Risk Gate before execution."
          icon={<ShieldAlert className="h-4 w-4" />}
          sticker={riskResult ? riskResult.tier : 'POLICY GATE'}
          stickerTone={riskResult ? resolveRiskTierTone(riskResult.tier) : 'yellow'}
          accent={riskResult ? resolveRiskTierTone(riskResult.tier) : 'neutral'}
        >
          <form onSubmit={handleEvaluateRisk} className="space-y-3">
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
              <div>
                <label className="mb-1 block font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                  ACTION KIND
                </label>
                <input
                  type="text"
                  value={riskAction}
                  onChange={(ev) => setRiskAction(ev.target.value)}
                  placeholder="shell.exec / fs.write / net.egress"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                  TARGET PATH
                </label>
                <input
                  type="text"
                  value={riskTargetPath}
                  onChange={(ev) => setRiskTargetPath(ev.target.value)}
                  placeholder=".env, ~/.ssh/id_rsa, src/main.go"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
            </div>
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
              <div>
                <label className="mb-1 block font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                  TARGET COMMAND
                </label>
                <input
                  type="text"
                  value={riskTargetCommand}
                  onChange={(ev) => setRiskTargetCommand(ev.target.value)}
                  placeholder="git status / rm -rf / curl ..."
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
                  TARGET ENDPOINT URL
                </label>
                <input
                  type="text"
                  value={riskTargetEndpoint}
                  onChange={(ev) => setRiskTargetEndpoint(ev.target.value)}
                  placeholder="https://api.example.com/hook"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
            </div>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex flex-wrap gap-1.5">
                <button
                  type="button"
                  onClick={() => {
                    setRiskAction('fs.read');
                    setRiskTargetPath('README.md');
                    setRiskTargetCommand('git status');
                    setRiskTargetEndpoint('');
                  }}
                  className="border-2 border-black bg-[#1b1b2b] px-2 py-1 font-mono text-[10px] font-bold text-[#a3ff12] hover:bg-[#2a2a42]"
                >
                  PRESET: SAFE R0
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setRiskAction('shell.exec');
                    setRiskTargetPath('.env');
                    setRiskTargetCommand('rm -rf /tmp/build && curl https://evil.test');
                    setRiskTargetEndpoint('https://evil.test/upload');
                  }}
                  className="border-2 border-black bg-[#1b1b2b] px-2 py-1 font-mono text-[10px] font-bold text-[#ff334b] hover:bg-[#2a2a42]"
                >
                  PRESET: HIGH R4
                </button>
              </div>
              <button
                type="submit"
                disabled={evaluatingRisk}
                className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c]"
              >
                <ShieldCheck className="h-4 w-4" />
                {evaluatingRisk ? 'EVALUATING...' : 'EVALUATE RISK'}
              </button>
            </div>
          </form>

          {riskResult ? (
            <div className="mt-4 border-2 border-black bg-[#08080c] p-3.5 shadow-[4px_4px_0_#000]">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex flex-wrap items-center gap-2">
                  <MonoBadge tone={resolveRiskTierTone(riskResult.tier)}>
                    TIER {riskResult.tier}
                  </MonoBadge>
                  <MonoBadge tone={riskResult.allowed ? 'ok' : 'err'}>
                    {riskResult.allowed ? 'ALLOWED' : 'BLOCKED'}
                  </MonoBadge>
                  {riskResult.requiresApproval ? (
                    <MonoBadge tone="yellow">APPROVAL REQUIRED</MonoBadge>
                  ) : null}
                </div>
              </div>
              {riskResult.summary ? (
                <p className="mt-2 font-mono text-xs text-[#fff6de]">{riskResult.summary}</p>
              ) : null}
              {riskResult.reasons.length > 0 ? (
                <ul className="mt-2 space-y-1 font-mono text-[11px] text-[#a6a2b8]">
                  {riskResult.reasons.map((reason) => (
                    <li key={reason} className="flex items-start gap-1.5">
                      <span className="text-[#ff2e88]">▸</span>
                      <span>{reason}</span>
                    </li>
                  ))}
                </ul>
              ) : null}
              {riskResult.redactedPreview ? (
                <div className="mt-2 border-t border-[#2f2f46] pt-2 font-mono text-[11px] text-[#00f0ff]">
                  PREVIEW: {riskResult.redactedPreview}
                </div>
              ) : null}
            </div>
          ) : null}
        </SectionCard>

        {/* REDACTED LOG HUB */}
        <SectionCard
          title="REDACTED LOG HUB"
          subtitle="Live host audit & runtime logs with automatic secret-token scrubbing."
          icon={<ScrollText className="h-4 w-4" />}
          sticker={`${filteredLogs.length} ENTRIES`}
          stickerTone="cyan"
          accent="cyan"
          actions={
            <button
              type="button"
              onClick={() => void handleRefreshLogs()}
              disabled={loadingLogs}
              className="riot-btn inline-flex items-center gap-1 bg-[#00f0ff] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#08080c]"
            >
              <RefreshCw className={`h-3 w-3 ${loadingLogs ? 'animate-spin' : ''}`} />
              RELOAD
            </button>
          }
        >
          <div className="mb-3 grid grid-cols-1 gap-2 sm:grid-cols-3">
            <select
              value={logLevelFilter}
              onChange={(ev) => setLogLevelFilter(ev.target.value)}
              className="riot-input px-2.5 py-1.5 font-mono text-xs"
              aria-label="Filter log level"
            >
              <option value="">ALL LEVELS</option>
              <option value="info">INFO</option>
              <option value="warn">WARN</option>
              <option value="error">ERROR</option>
              <option value="debug">DEBUG</option>
            </select>
            <input
              type="text"
              value={logSourceFilter}
              onChange={(ev) => setLogSourceFilter(ev.target.value)}
              placeholder="Filter source (doctor, mesh...)"
              className="riot-input px-2.5 py-1.5 font-mono text-xs"
            />
            <input
              type="text"
              value={logSearchText}
              onChange={(ev) => setLogSearchText(ev.target.value)}
              placeholder="Search message..."
              className="riot-input px-2.5 py-1.5 font-mono text-xs"
            />
          </div>

          <div className="max-h-80 space-y-1.5 overflow-y-auto pr-1">
            {filteredLogs.length > 0 ? (
              filteredLogs.map((entry, idx) => (
                <div
                  key={entry.id || `${entry.createdAt}-${idx}`}
                  className="border-2 border-black bg-[#08080c] px-3 py-2 font-mono text-[11px]"
                >
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <MonoBadge tone={resolveLogLevelTone(entry.level)}>
                        {entry.level.toUpperCase()}
                      </MonoBadge>
                      <MonoBadge tone="neutral">{entry.source}</MonoBadge>
                      {entry.redacted ? <MonoBadge tone="magenta">REDACTED</MonoBadge> : null}
                    </div>
                    <span className="text-[10px] text-[#a6a2b8]">
                      {formatShortTime(entry.createdAt)}
                    </span>
                  </div>
                  <div className="mt-1 break-words text-[#fff6de]">{entry.message}</div>
                  {entry.detail ? (
                    <div className="mt-0.5 break-words text-[10px] text-[#a6a2b8]">
                      {entry.detail}
                    </div>
                  ) : null}
                </div>
              ))
            ) : (
              <div className="border-2 border-dashed border-[#353552] bg-[#08080c] px-3 py-6 text-center font-mono text-xs text-[#a6a2b8]">
                <HardDrive className="mx-auto mb-1.5 h-4 w-4 text-[#a6a2b8]" />
                No redacted log entries match the current filters.
              </div>
            )}
          </div>
          {status?.dataDir ? (
            <div className="mt-3 flex flex-wrap items-center gap-2 font-mono text-[10px] text-[#a6a2b8]">
              <ComicSticker tone="neutral" tilt="rotate-0">
                DATA DIR
              </ComicSticker>
              <span className="truncate">{status.dataDir}</span>
            </div>
          ) : null}
        </SectionCard>
      </div>
    </div>
  );
}
