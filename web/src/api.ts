// Typed API client for mw-echo Hub, Projects, Docs, Tasks, 9Drive Pool, Observatory & AI Gateway

export interface HubStats {
  docsCount: number;
  tasksCount: number;
  storageCount: number;
  storageTotalBytes: number;
  tasksByStatus: Record<string, number>;
  docsByProject: Record<string, number>;
  dataDir: string;
}

export interface ImportRepoRequest {
  repoDir: string;
  project: string;
  includeAssets: boolean;
}

export interface ImportSummary {
  project: string;
  sourceDir: string;
  docsImported: number;
  tasksImported: number;
  assetsImported: number;
  skippedTooLarge: number;
}

export interface DocRecord {
  id: string;
  project: string;
  relPath: string;
  title: string;
  category: string;
  content?: string;
  snippet?: string;
  sha256: string;
  sizeBytes: number;
  updatedAt: string;
}

export interface DocUpsertInput {
  id?: string;
  project: string;
  relPath: string;
  title?: string;
  category?: string;
  content: string;
}

export interface DocsListResponse {
  docs: DocRecord[];
  count: number;
}

export type TaskStatus = 'todo' | 'doing' | 'in_progress' | 'blocked' | 'backlog' | 'done';
export type TaskPriority = 'p0' | 'p1' | 'p2' | 'p3';

export interface TaskRecord {
  code: string;
  project: string;
  series: string;
  milestone: string;
  title: string;
  summary: string;
  priority: string;
  status: string;
  dependsOn: string[];
  tags: string[];
  payloadJson?: string;
  updatedAt: string;
}

export interface TaskCreateInput {
  code: string;
  project: string;
  series?: string;
  milestone?: string;
  title: string;
  summary?: string;
  priority: string;
  status: string;
  dependsOn?: string[];
  tags?: string[];
}

export interface TaskPatchInput {
  status?: string;
  priority?: string;
  title?: string;
}

export interface TasksListResponse {
  tasks: TaskRecord[];
  count: number;
}

// --- Projects ---

export type ProjectStatus = 'active' | 'paused' | 'archived' | string;

export interface ProjectMilestoneSummary {
  name: string;
  totalTasks: number;
  doneTasks: number;
  inProgressTasks: number;
  todoTasks: number;
  blockedTasks: number;
  progressPct: number;
}

export interface ProjectSummary {
  slug: string;
  name: string;
  description: string;
  repoPath: string;
  status: ProjectStatus;
  color: string;
  defaultAgent: string;
  createdAt: string;
  updatedAt: string;
  totalTasks: number;
  doneTasks: number;
  inProgressTasks: number;
  todoTasks: number;
  blockedTasks: number;
  docsCount: number;
  storageCount: number;
  storageBytes: number;
  progressPct: number;
  milestones: ProjectMilestoneSummary[];
}

export interface ProjectUpsertInput {
  slug: string;
  name: string;
  description: string;
  repoPath: string;
  status: ProjectStatus;
  color: string;
  defaultAgent: string;
}

export interface ProjectsListResponse {
  projects: ProjectSummary[];
  count: number;
}

// --- 9Drive Storage Pool ---

export type RoutingPolicy = 'most_free' | 'round_robin' | 'priority';

export interface StorageProviderRecord {
  id: string;
  name: string;
  kind: 'gdrive' | 's3' | 'local' | string;
  accountRef?: string;
  quotaBytes: number;
  usedBytes: number;
  freeBytes: number;
  priority: number;
  enabled: boolean;
  lastSyncAt?: string;
  createdAt: string;
}

export interface PoolSummary {
  routingPolicy: RoutingPolicy;
  totalQuotaBytes: number;
  totalUsedBytes: number;
  totalFreeBytes: number;
  providers: StorageProviderRecord[];
}

export interface GDriveProviderConfig {
  clientId: string;
  clientSecret: string;
  refreshToken: string;
  rootFolder?: string;
}

export interface S3ProviderConfig {
  endpoint: string;
  region: string;
  bucket: string;
  accessKey: string;
  secretKey: string;
  forcePathStyle: boolean;
  prefix?: string;
}

export interface StorageProviderCreateInput {
  name: string;
  kind: 'gdrive' | 's3' | 'local';
  quotaBytes?: number;
  priority: number;
  gdrive?: GDriveProviderConfig;
  s3?: S3ProviderConfig;
}

export interface GDriveOAuthStartInput {
  name: string;
  clientId: string;
  clientSecret: string;
  redirectUri?: string;
  priority: number;
}

export interface GDriveOAuthStartResponse {
  authUrl: string;
  state: string;
  redirectUri: string;
}

export interface PooledStorageObject {
  id: string;
  project: string;
  logicalPath: string;
  filename: string;
  mimeType: string;
  sha256: string;
  sizeBytes: number;
  createdAt: string;
  providerId: string;
  providerKind: string;
  providerName: string;
  remoteId?: string;
}

export interface StorageListResponse {
  objects: PooledStorageObject[];
  count: number;
}

// --- Observatory (OTLP + Host Discovery + Local Git Repo Radar) ---

export interface ObservatoryStats {
  totalEvents: number;
  countsByKind: Record<string, number>;
  countsByService: Record<string, number>;
  totalTokensIn: number;
  totalTokensOut: number;
  avgDurationMs: number;
}

export interface ObservatoryEventRecord {
  id: string;
  kind: string;
  service: string;
  agent: string;
  name: string;
  status: string;
  durationMs: number;
  tokensIn: number;
  tokensOut: number;
  costUsd: number;
  createdAt: string;
}

export interface ObservatoryEventsResponse {
  events: ObservatoryEventRecord[];
  count: number;
}

export interface DiscoveredHostService {
  port: number;
  name: string;
  category: string;
  protocol: string;
  endpoint: string;
  status: string;
  latencyMs: number;
  detail: string;
  detectedAt: string;
}

export interface HostServicesDiscoveryResponse {
  services: DiscoveredHostService[];
  count: number;
  probesRun: number;
  scannedAt: string;
}

export interface DiscoveredGitRepo {
  name: string;
  path: string;
  branch: string;
  remoteUrl: string;
  hasDocs: boolean;
  hasTasksJson: boolean;
  imported: boolean;
  modifiedAt: string;
}

export interface GitRepoRadarResponse {
  repos: DiscoveredGitRepo[];
  count: number;
  roots: string[];
  scannedAt: string;
}

// --- AI Gateway (Bifrost + 9Router + Magpie + FreeLLMAPI) ---

export type AiProviderKind = 'openai_compat' | 'anthropic' | 'gemini' | string;
export type AiTier = 'free' | 'cheap' | 'subscription' | 'paid' | string;

export interface AiSettings {
  rtkEnabled: boolean;
  cacheEnabled: boolean;
  fallbackOrder: string;
  upstreamRepo?: string;
  upstreamSha?: string;
  lastSniffAt?: string;
}

export interface AiRequestLog {
  id: string;
  agentId: string;
  requestedModel: string;
  resolvedProvider: string;
  resolvedModel: string;
  statusCode: number;
  latencyMs: number;
  promptTokens: number;
  completionTokens: number;
  rtkTokensSaved: number;
  cacheHit: boolean;
  createdAt: string;
}

export interface AiGatewayStats {
  totalRequests: number;
  cacheHits: number;
  totalRtkTokensSaved: number;
  cacheTokensSaved: number;
  avgLatencyMs: number;
  providersCount: number;
  catalogCount: number;
  settings: AiSettings;
  recentLogs: AiRequestLog[];
}

export interface AiProviderRecord {
  id: string;
  name: string;
  kind: AiProviderKind;
  tier: AiTier;
  baseUrl: string;
  hasApiKey: boolean;
  defaultModel: string;
  models: string[];
  priority: number;
  enabled: boolean;
  cooldownUntil?: string;
  inCooldown: boolean;
  reqCount: number;
  errCount: number;
  tokensIn: number;
  tokensOut: number;
  createdAt?: string;
}

export interface AiProviderCreateInput {
  name: string;
  kind: AiProviderKind;
  tier: AiTier;
  baseUrl: string;
  apiKey: string;
  defaultModel: string;
  models?: string[];
  priority: number;
}

export interface AiAgentRoute {
  agentId: string;
  targetModel: string;
  tierOrder: string;
  rtkEnabled: boolean;
  updatedAt?: string;
}

export interface AiAgentUpdateInput {
  targetModel?: string;
  tierOrder?: string;
  rtkEnabled?: boolean;
}

export interface FreeModelCatalogEntry {
  id: string;
  providerSlug: string;
  providerName: string;
  baseUrl: string;
  modelId: string;
  contextWindow: number;
  freeTier: boolean;
  source: string;
  updatedAt?: string;
}

export interface FreeModelCatalogListResponse {
  entries: FreeModelCatalogEntry[];
  count: number;
}

export interface SniffUpstreamResult {
  updated: boolean;
  upstreamSha: string;
  previousSha: string;
  providersFound: number;
  modelsIndexed: number;
  lastSniffAt?: string;
}

export interface ChatCompletionMessage {
  role: 'system' | 'user' | 'assistant';
  content: string;
}

export interface ChatCompletionTestRequest {
  agentId: string;
  model: string;
  messages: ChatCompletionMessage[];
  temperature?: number;
  maxTokens?: number;
}

export interface ChatCompletionTestResult {
  content: string;
  modelUsed: string;
  providerUsed: string;
  agentUsed: string;
  finishReason: string;
  latencyMs: number;
  promptTokens: number;
  completionTokens: number;
  totalTokens: number;
  cacheStatus: string;
  rtkSavedTokens: string;
  rawHeaders: Record<string, string>;
  rawPayload: Record<string, unknown>;
}

const DEFAULT_FALLBACK_ORDER = 'free,cheap,subscription,paid';

export const DEFAULT_AGENT_IDS = [
  'claude-code',
  'codex',
  'gemini-cli',
  'opencode',
  'antigravity',
  'default',
] as const;

class ApiError extends Error {
  readonly statusCode: number;

  constructor(message: string, statusCode: number) {
    super(message);
    this.name = 'ApiError';
    this.statusCode = statusCode;
  }
}

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

async function requestJson<TResponse>(url: string, init?: RequestInit): Promise<TResponse> {
  const response = await fetch(url, init);
  const rawText = await response.text();
  let parsed: unknown = {};
  if (rawText.trim().length > 0) {
    try {
      parsed = JSON.parse(rawText);
    } catch {
      if (!response.ok) {
        throw new ApiError(rawText || `HTTP ${response.status}`, response.status);
      }
      throw new ApiError('Server returned invalid JSON payload', response.status);
    }
  }
  if (!response.ok) {
    const errRecord = asRecord(parsed);
    const nestedErr = asRecord(errRecord.error);
    const errMessage =
      pickString(nestedErr, ['message', 'error']) ||
      pickString(errRecord, ['error', 'message', 'detail'], `HTTP ${response.status}`);
    throw new ApiError(errMessage, response.status);
  }
  return parsed as TResponse;
}

// --- Hub Stats & Importer ---

export function fetchHubStats(): Promise<HubStats> {
  return requestJson<HubStats>('/api/stats');
}

export function importLocalRepo(payload: ImportRepoRequest): Promise<ImportSummary> {
  return requestJson<ImportSummary>('/api/import/repo', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

// --- Docs (FTS5) ---

export function fetchDocs(query = '', project = '', category = '', limit = 150): Promise<DocsListResponse> {
  const params = new URLSearchParams();
  if (query.trim()) params.set('q', query.trim());
  if (project.trim()) params.set('project', project.trim());
  if (category.trim()) params.set('category', category.trim());
  params.set('limit', String(limit));
  return requestJson<DocsListResponse>(`/api/docs?${params.toString()}`);
}

export function fetchDocById(docId: string): Promise<DocRecord> {
  return requestJson<DocRecord>(`/api/docs/${encodeURIComponent(docId)}`);
}

export function upsertDoc(payload: DocUpsertInput): Promise<DocRecord> {
  return requestJson<DocRecord>('/api/docs', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

export function deleteDoc(docId: string): Promise<{ deleted: boolean }> {
  return requestJson<{ deleted: boolean }>(`/api/docs/${encodeURIComponent(docId)}`, {
    method: 'DELETE',
  });
}

// --- Tasks (TaskOS v2) ---

export function fetchTasks(filters: {
  query?: string;
  status?: string;
  priority?: string;
  project?: string;
  limit?: number;
} = {}): Promise<TasksListResponse> {
  const params = new URLSearchParams();
  if (filters.query?.trim()) params.set('q', filters.query.trim());
  if (filters.status?.trim()) params.set('status', filters.status.trim());
  if (filters.priority?.trim()) params.set('priority', filters.priority.trim());
  if (filters.project?.trim()) params.set('project', filters.project.trim());
  params.set('limit', String(filters.limit ?? 500));
  return requestJson<TasksListResponse>(`/api/tasks?${params.toString()}`);
}

export function createTask(payload: TaskCreateInput): Promise<TaskRecord> {
  return requestJson<TaskRecord>('/api/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

export function patchTask(taskCode: string, patch: TaskPatchInput): Promise<TaskRecord> {
  return requestJson<TaskRecord>(`/api/tasks/${encodeURIComponent(taskCode)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  });
}

export function deleteTask(taskCode: string): Promise<{ deleted: boolean }> {
  return requestJson<{ deleted: boolean }>(`/api/tasks/${encodeURIComponent(taskCode)}`, {
    method: 'DELETE',
  });
}

export function fetchTasksExportJson(): Promise<Record<string, unknown>> {
  return requestJson<Record<string, unknown>>('/api/export/tasks.json');
}

// --- Projects ---

export function fetchProjects(): Promise<ProjectsListResponse> {
  return requestJson<ProjectsListResponse>('/api/projects');
}

export function createProject(payload: ProjectUpsertInput): Promise<ProjectSummary> {
  return requestJson<ProjectSummary>('/api/projects', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

export function updateProject(slug: string, payload: ProjectUpsertInput): Promise<ProjectSummary> {
  return requestJson<ProjectSummary>(`/api/projects/${encodeURIComponent(slug)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

export function deleteProject(slug: string): Promise<{ deleted: boolean }> {
  return requestJson<{ deleted: boolean }>(`/api/projects/${encodeURIComponent(slug)}`, {
    method: 'DELETE',
  });
}

// --- 9Drive Storage Pool & Objects ---

export function fetchStoragePool(): Promise<PoolSummary> {
  return requestJson<PoolSummary>('/api/providers');
}

export function addStorageProvider(payload: StorageProviderCreateInput): Promise<StorageProviderRecord> {
  return requestJson<StorageProviderRecord>('/api/providers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

export function deleteStorageProvider(providerId: string): Promise<{ deleted: boolean }> {
  return requestJson<{ deleted: boolean }>(`/api/providers/${encodeURIComponent(providerId)}`, {
    method: 'DELETE',
  });
}

export function syncStorageProviderQuota(providerId: string): Promise<StorageProviderRecord> {
  return requestJson<StorageProviderRecord>(`/api/providers/${encodeURIComponent(providerId)}/sync-quota`, {
    method: 'POST',
  });
}

export function updateStorageRoutingPolicy(routingPolicy: RoutingPolicy): Promise<{ routingPolicy: RoutingPolicy }> {
  return requestJson<{ routingPolicy: RoutingPolicy }>('/api/providers/policy', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ routingPolicy }),
  });
}

export function startGDriveOAuth(payload: GDriveOAuthStartInput): Promise<GDriveOAuthStartResponse> {
  return requestJson<GDriveOAuthStartResponse>('/api/providers/gdrive/oauth/start', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

export function fetchStorageObjects(query = '', project = '', limit = 250): Promise<StorageListResponse> {
  const params = new URLSearchParams();
  if (query.trim()) params.set('q', query.trim());
  if (project.trim()) params.set('project', project.trim());
  params.set('limit', String(limit));
  return requestJson<StorageListResponse>(`/api/storage?${params.toString()}`);
}

export function uploadStorageObject(params: {
  file: File;
  project: string;
  logicalPath?: string;
  providerId?: string;
}): Promise<PooledStorageObject> {
  const formBody = new FormData();
  formBody.append('file', params.file);
  formBody.append('project', params.project.trim() || 'moonwitness');
  if (params.logicalPath?.trim()) {
    formBody.append('logicalPath', params.logicalPath.trim());
  }
  if (params.providerId?.trim()) {
    formBody.append('providerId', params.providerId.trim());
  }
  return requestJson<PooledStorageObject>('/api/storage/upload', {
    method: 'POST',
    body: formBody,
  });
}

export function deleteStorageObject(objectId: string): Promise<{ deleted: boolean }> {
  return requestJson<{ deleted: boolean }>(`/api/storage/${encodeURIComponent(objectId)}`, {
    method: 'DELETE',
  });
}

// --- Observatory (OTLP + Host Discovery + Local Git Repo Radar) ---

export function fetchObservatoryStats(): Promise<ObservatoryStats> {
  return requestJson<ObservatoryStats>('/api/observatory/stats');
}

export function fetchObservatoryEvents(filters: {
  kind?: string;
  service?: string;
  limit?: number;
} = {}): Promise<ObservatoryEventsResponse> {
  const params = new URLSearchParams();
  if (filters.kind?.trim()) params.set('kind', filters.kind.trim());
  if (filters.service?.trim()) params.set('service', filters.service.trim());
  params.set('limit', String(filters.limit ?? 100));
  return requestJson<ObservatoryEventsResponse>(`/api/observatory/events?${params.toString()}`);
}

export function fetchObservatoryServices(): Promise<HostServicesDiscoveryResponse> {
  return requestJson<HostServicesDiscoveryResponse>('/api/observatory/services');
}

export function fetchObservatoryRepos(): Promise<GitRepoRadarResponse> {
  return requestJson<GitRepoRadarResponse>('/api/observatory/repos');
}

export function scanObservatoryRepos(roots: string[]): Promise<GitRepoRadarResponse> {
  return requestJson<GitRepoRadarResponse>('/api/observatory/repos/scan', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ roots }),
  });
}

// --- AI Gateway (Exact alignment with internal/aigateway/types.go & gateway.go) ---

function normalizeAiSettings(rawSettings: unknown): AiSettings {
  const rec = asRecord(rawSettings);
  return {
    rtkEnabled: pickBoolean(rec, ['rtkEnabled'], true),
    cacheEnabled: pickBoolean(rec, ['cacheEnabled'], true),
    fallbackOrder: pickString(rec, ['fallbackOrder'], DEFAULT_FALLBACK_ORDER),
    upstreamRepo: pickString(rec, ['upstreamRepo'], 'tashfeenahmed/freellmapi'),
    upstreamSha: pickString(rec, ['upstreamSha']),
    lastSniffAt: pickString(rec, ['lastSniffAt']),
  };
}

function normalizeAiRequestLog(rawLog: unknown): AiRequestLog {
  const rec = asRecord(rawLog);
  return {
    id: pickString(rec, ['id']),
    agentId: pickString(rec, ['agentId'], 'default'),
    requestedModel: pickString(rec, ['requestedModel']),
    resolvedProvider: pickString(rec, ['resolvedProvider']),
    resolvedModel: pickString(rec, ['resolvedModel']),
    statusCode: pickNumber(rec, ['statusCode'], 0),
    latencyMs: pickNumber(rec, ['latencyMs'], 0),
    promptTokens: pickNumber(rec, ['promptTokens'], 0),
    completionTokens: pickNumber(rec, ['completionTokens'], 0),
    rtkTokensSaved: pickNumber(rec, ['rtkTokensSaved'], 0),
    cacheHit: pickBoolean(rec, ['cacheHit'], false),
    createdAt: pickString(rec, ['createdAt']),
  };
}

function normalizeAiStats(rawStats: unknown): AiGatewayStats {
  const rec = asRecord(rawStats);
  const nestedSettings = rec.settings !== undefined ? rec.settings : rec;
  const rawLogs = Array.isArray(rec.recentLogs) ? rec.recentLogs : [];
  return {
    totalRequests: pickNumber(rec, ['totalRequests'], 0),
    cacheHits: pickNumber(rec, ['cacheHits'], 0),
    totalRtkTokensSaved: pickNumber(rec, ['totalRtkTokensSaved', 'rtkTokensSaved'], 0),
    cacheTokensSaved: pickNumber(rec, ['cacheTokensSaved'], 0),
    avgLatencyMs: pickNumber(rec, ['avgLatencyMs'], 0),
    providersCount: pickNumber(rec, ['providersCount'], 0),
    catalogCount: pickNumber(rec, ['catalogCount'], 0),
    settings: normalizeAiSettings(nestedSettings),
    recentLogs: rawLogs.map(normalizeAiRequestLog),
  };
}

function normalizeAiProvider(rawProvider: unknown): AiProviderRecord {
  const rec = asRecord(rawProvider);
  const rawModels = Array.isArray(rec.models) ? rec.models.map(String) : [];
  return {
    id: pickString(rec, ['id']),
    name: pickString(rec, ['name'], 'unnamed-provider'),
    kind: pickString(rec, ['kind'], 'openai_compat'),
    tier: pickString(rec, ['tier'], 'free'),
    baseUrl: pickString(rec, ['baseUrl']),
    hasApiKey: pickBoolean(rec, ['hasApiKey'], false),
    defaultModel: pickString(rec, ['defaultModel']),
    models: rawModels,
    priority: pickNumber(rec, ['priority'], 100),
    enabled: pickBoolean(rec, ['enabled'], true),
    cooldownUntil: pickString(rec, ['cooldownUntil']),
    inCooldown: pickBoolean(rec, ['inCooldown'], false),
    reqCount: pickNumber(rec, ['reqCount', 'requestCount'], 0),
    errCount: pickNumber(rec, ['errCount', 'errorCount'], 0),
    tokensIn: pickNumber(rec, ['tokensIn'], 0),
    tokensOut: pickNumber(rec, ['tokensOut'], 0),
    createdAt: pickString(rec, ['createdAt']),
  };
}

function normalizeAiAgent(rawAgent: unknown): AiAgentRoute {
  const rec = asRecord(rawAgent);
  return {
    agentId: pickString(rec, ['agentId', 'id'], 'default'),
    targetModel: pickString(rec, ['targetModel', 'model'], ''),
    tierOrder: pickString(rec, ['tierOrder', 'fallbackOrder'], DEFAULT_FALLBACK_ORDER),
    rtkEnabled: pickBoolean(rec, ['rtkEnabled'], true),
    updatedAt: pickString(rec, ['updatedAt']),
  };
}

function normalizeCatalogEntry(rawEntry: unknown, index: number): FreeModelCatalogEntry {
  const rec = asRecord(rawEntry);
  const providerSlug = pickString(rec, ['providerSlug', 'provider'], 'unknown');
  const modelId = pickString(rec, ['modelId', 'model'], `model-${index}`);
  return {
    id: pickString(rec, ['id'], `${providerSlug}:${modelId}`),
    providerSlug,
    providerName: pickString(rec, ['providerName'], providerSlug),
    baseUrl: pickString(rec, ['baseUrl']),
    modelId,
    contextWindow: pickNumber(rec, ['contextWindow'], 128000),
    freeTier: pickBoolean(rec, ['freeTier'], true),
    source: pickString(rec, ['source'], 'seed'),
    updatedAt: pickString(rec, ['updatedAt']),
  };
}

export async function fetchAiStats(): Promise<AiGatewayStats> {
  const rawPayload = await requestJson<unknown>('/api/ai/stats');
  return normalizeAiStats(rawPayload);
}

export async function updateAiSettings(settings: Partial<AiSettings>): Promise<AiSettings> {
  const rawPayload = await requestJson<unknown>('/api/ai/settings', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      rtkEnabled: settings.rtkEnabled,
      cacheEnabled: settings.cacheEnabled,
      fallbackOrder: settings.fallbackOrder,
      upstreamRepo: settings.upstreamRepo,
    }),
  });
  return normalizeAiSettings(rawPayload);
}

export async function clearAiCache(): Promise<{ purged: number }> {
  const rawPayload = await requestJson<unknown>('/api/ai/cache', {
    method: 'DELETE',
  });
  const rec = asRecord(rawPayload);
  return {
    purged: pickNumber(rec, ['purged', 'cleared', 'deleted'], 0),
  };
}

export async function fetchAiProviders(): Promise<AiProviderRecord[]> {
  const rawPayload = await requestJson<unknown>('/api/ai/providers');
  const rec = asRecord(rawPayload);
  const list = Array.isArray(rawPayload)
    ? rawPayload
    : Array.isArray(rec.providers)
      ? rec.providers
      : [];
  return list.map(normalizeAiProvider);
}

export async function createAiProvider(payload: AiProviderCreateInput): Promise<AiProviderRecord> {
  const rawPayload = await requestJson<unknown>('/api/ai/providers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  return normalizeAiProvider(rawPayload);
}

export function deleteAiProvider(providerId: string): Promise<{ deleted: boolean }> {
  return requestJson<{ deleted: boolean }>(`/api/ai/providers/${encodeURIComponent(providerId)}`, {
    method: 'DELETE',
  });
}

export async function fetchAiAgents(): Promise<AiAgentRoute[]> {
  const rawPayload = await requestJson<unknown>('/api/ai/agents');
  const rec = asRecord(rawPayload);
  const list = Array.isArray(rawPayload)
    ? rawPayload
    : Array.isArray(rec.agents)
      ? rec.agents
      : [];
  const normalized = list.map(normalizeAiAgent);
  if (normalized.length > 0) {
    return normalized;
  }
  return DEFAULT_AGENT_IDS.map((agentId) => ({
    agentId,
    targetModel: '',
    tierOrder: DEFAULT_FALLBACK_ORDER,
    rtkEnabled: true,
  }));
}

export async function updateAiAgent(agentId: string, payload: AiAgentUpdateInput): Promise<AiAgentRoute> {
  const rawPayload = await requestJson<unknown>(`/api/ai/agents/${encodeURIComponent(agentId)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      targetModel: payload.targetModel,
      tierOrder: payload.tierOrder,
      rtkEnabled: payload.rtkEnabled,
    }),
  });
  return normalizeAiAgent(rawPayload);
}

export async function fetchFreeModelCatalog(
  query = '',
  freeOnly = false,
): Promise<FreeModelCatalogListResponse> {
  const params = new URLSearchParams();
  if (query.trim()) {
    params.set('q', query.trim());
  }
  if (freeOnly) {
    params.set('freeOnly', 'true');
  }
  const suffix = params.toString() ? `?${params.toString()}` : '';
  const rawPayload = await requestJson<unknown>(`/api/ai/catalog${suffix}`);
  const rec = asRecord(rawPayload);
  const rawList = Array.isArray(rawPayload)
    ? rawPayload
    : Array.isArray(rec.entries)
      ? rec.entries
      : Array.isArray(rec.models)
        ? rec.models
        : [];
  const entries = rawList.map((entry, idx) => normalizeCatalogEntry(entry, idx));
  return {
    entries,
    count: pickNumber(rec, ['count'], entries.length),
  };
}

export function sniffFreeModelCatalog(): Promise<SniffUpstreamResult> {
  return requestJson<SniffUpstreamResult>('/api/ai/catalog/sniff', {
    method: 'POST',
  });
}

export async function sendChatCompletionTest(
  params: ChatCompletionTestRequest,
): Promise<ChatCompletionTestResult> {
  const startedAt = performance.now();
  const response = await fetch('/v1/chat/completions', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-MW-Agent': params.agentId,
    },
    body: JSON.stringify({
      model: params.model.trim() || 'auto',
      messages: params.messages,
      temperature: params.temperature ?? 0.2,
      max_tokens: params.maxTokens ?? 512,
      stream: false,
    }),
  });

  const elapsedMs = Math.round(performance.now() - startedAt);
  const rawHeaders: Record<string, string> = {};
  response.headers.forEach((headerVal, headerKey) => {
    rawHeaders[headerKey.toLowerCase()] = headerVal;
  });

  const rawText = await response.text();
  let parsed: unknown = {};
  if (rawText.trim().length > 0) {
    try {
      parsed = JSON.parse(rawText);
    } catch {
      if (!response.ok) {
        throw new ApiError(rawText || `HTTP ${response.status}`, response.status);
      }
      throw new ApiError('Invalid JSON response from /v1/chat/completions', response.status);
    }
  }

  const bodyRecord = asRecord(parsed);
  if (!response.ok) {
    const nestedErr = asRecord(bodyRecord.error);
    const errMsg =
      pickString(nestedErr, ['message', 'error']) ||
      pickString(bodyRecord, ['error', 'message'], `HTTP ${response.status}`);
    throw new ApiError(errMsg, response.status);
  }

  const choices = Array.isArray(bodyRecord.choices) ? bodyRecord.choices : [];
  const firstChoice = asRecord(choices[0]);
  const choiceMessage = asRecord(firstChoice.message);
  const usageRecord = asRecord(bodyRecord.usage);

  return {
    content: pickString(choiceMessage, ['content'], pickString(firstChoice, ['text'], '')),
    modelUsed:
      rawHeaders['x-mw-echo-model'] ||
      pickString(bodyRecord, ['model'], params.model || 'unknown'),
    providerUsed: rawHeaders['x-mw-echo-provider'] || 'gateway',
    agentUsed: rawHeaders['x-mw-agent'] || params.agentId,
    finishReason: pickString(firstChoice, ['finish_reason'], 'stop'),
    latencyMs: elapsedMs,
    promptTokens: pickNumber(usageRecord, ['prompt_tokens'], 0),
    completionTokens: pickNumber(usageRecord, ['completion_tokens'], 0),
    totalTokens: pickNumber(usageRecord, ['total_tokens'], 0),
    cacheStatus: rawHeaders['x-mw-echo-cache'] || 'MISS',
    rtkSavedTokens: rawHeaders['x-mw-echo-rtk-saved'] || '0',
    rawHeaders,
    rawPayload: bodyRecord,
  };
}
