import { useCallback, useEffect, useState } from 'react';
import {
  Activity,
  CheckCircle2,
  KeyRound,
  Link2,
  Play,
  Plus,
  RefreshCw,
  Save,
  ShieldAlert,
  ShieldCheck,
  Trash2,
  XCircle,
} from 'lucide-react';
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  StatTile,
} from './UiPrimitives';

interface FleetServiceConfig {
  key: string;
  name: string;
  envVar: string;
  url: string;
  source: string;
  updatedAt?: string;
}

interface FleetServiceProbe {
  key: string;
  name: string;
  envVar: string;
  url: string;
  online: boolean;
  statusCode: number;
  latencyMs: number;
  error?: string;
  checkedAt: string;
}

interface FleetProbeReport {
  services: FleetServiceProbe[];
  onlineCount: number;
  totalCount: number;
  checkedAt: string;
}

interface ArsyGrant {
  id: string;
  subject: string;
  capability: string;
  riskTier: string;
  budgetLimit: number;
  budgetUsed: number;
  status: string;
  expiresAt: string;
  createdAt: string;
}

interface ArsyAuditEntry {
  seq: number;
  id: string;
  actor: string;
  action: string;
  resource: string;
  riskTier: string;
  decision: string;
  prevHash: string;
  entryHash: string;
  createdAt: string;
}

interface ArsyEvalOutcome {
  allowed: boolean;
  decision: string;
  reason: string;
  grantId?: string;
  budgetRemaining: number;
  auditEntry: ArsyAuditEntry;
}

interface ArsyChainVerification {
  valid: boolean;
  count: number;
  headHash: string;
  brokenSeq: number;
}

async function fleetRequest<T>(path: string, init?: RequestInit): Promise<T> {
  const token = typeof window !== 'undefined' ? window.localStorage.getItem('mw_echo_token') : null;
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  };
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const response = await fetch(path, {
    ...init,
    headers: {
      ...headers,
      ...(init?.headers as Record<string, string> | undefined),
    },
  });
  if (!response.ok) {
    const errText = await response.text().catch(() => '');
    throw new Error(errText || `HTTP ${response.status}`);
  }
  return (await response.json()) as T;
}

export function FleetCockpitTab() {
  const [services, setServices] = useState<FleetServiceConfig[]>([]);
  const [probeReport, setProbeReport] = useState<FleetProbeReport | null>(null);
  const [urlDrafts, setUrlDrafts] = useState<Record<string, string>>({});
  const [grants, setGrants] = useState<ArsyGrant[]>([]);
  const [auditEntries, setAuditEntries] = useState<ArsyAuditEntry[]>([]);
  const [chainStatus, setChainStatus] = useState<ArsyChainVerification | null>(null);
  const [evalOutcome, setEvalOutcome] = useState<ArsyEvalOutcome | null>(null);

  const [loading, setLoading] = useState(false);
  const [probing, setProbing] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);

  // Grant form state
  const [grantSubject, setGrantSubject] = useState('agent-ops');
  const [grantCapability, setGrantCapability] = useState('fleet.deploy');
  const [grantRiskTier, setGrantRiskTier] = useState('R2');
  const [grantBudgetLimit, setGrantBudgetLimit] = useState(10);

  // Risk evaluator form state
  const [evalSubject, setEvalSubject] = useState('agent-ops');
  const [evalCapability, setEvalCapability] = useState('fleet.deploy');
  const [evalRiskTier, setEvalRiskTier] = useState('R2');
  const [evalResource, setEvalResource] = useState('mw-lab/prod');
  const [evalCostUnits, setEvalCostUnits] = useState(1);

  const refreshAll = useCallback(async () => {
    setLoading(true);
    setErrorMessage(null);
    try {
      const [servicesPayload, grantsPayload, auditPayload] = await Promise.all([
        fleetRequest<{ services: FleetServiceConfig[] }>('/api/fleet/services'),
        fleetRequest<{ grants: ArsyGrant[] }>('/api/fleet/arsy/grants'),
        fleetRequest<{ entries: ArsyAuditEntry[] }>('/api/fleet/arsy/audit?limit=25'),
      ]);
      const loadedServices = servicesPayload.services ?? [];
      setServices(loadedServices);
      setUrlDrafts((prev) => {
        const next: Record<string, string> = { ...prev };
        for (const svc of loadedServices) {
          if (next[svc.key] === undefined) {
            next[svc.key] = svc.url;
          }
        }
        return next;
      });
      setGrants(grantsPayload.grants ?? []);
      setAuditEntries(auditPayload.entries ?? []);
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : 'Failed to load fleet cockpit');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refreshAll();
  }, [refreshAll]);

  const handleProbeFleet = async () => {
    setProbing(true);
    setErrorMessage(null);
    try {
      const report = await fleetRequest<FleetProbeReport>('/api/fleet/probe', { method: 'POST' });
      setProbeReport(report);
      setStatusMessage(`Probed ${report.totalCount} services (${report.onlineCount} online)`);
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : 'Fleet probe failed');
    } finally {
      setProbing(false);
    }
  };

  const handleSaveServiceUrl = async (svc: FleetServiceConfig) => {
    setErrorMessage(null);
    try {
      const draftUrl = urlDrafts[svc.key] ?? svc.url;
      await fleetRequest<FleetServiceConfig>('/api/fleet/services', {
        method: 'PUT',
        body: JSON.stringify({ key: svc.key, name: svc.name, url: draftUrl }),
      });
      setStatusMessage(`Saved URL override for ${svc.key}`);
      await refreshAll();
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : 'Failed to save service URL');
    }
  };

  const handleIssueGrant = async () => {
    setErrorMessage(null);
    try {
      await fleetRequest<ArsyGrant>('/api/fleet/arsy/grants', {
        method: 'POST',
        body: JSON.stringify({
          subject: grantSubject,
          capability: grantCapability,
          riskTier: grantRiskTier,
          budgetLimit: Number(grantBudgetLimit),
        }),
      });
      setStatusMessage(`Issued ${grantRiskTier} grant for ${grantSubject}`);
      await refreshAll();
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : 'Failed to issue grant');
    }
  };

  const handleRevokeGrant = async (grantId: string) => {
    setErrorMessage(null);
    try {
      await fleetRequest<{ revoked: boolean }>(`/api/fleet/arsy/grants/${encodeURIComponent(grantId)}`, {
        method: 'DELETE',
      });
      setStatusMessage(`Revoked grant ${grantId}`);
      await refreshAll();
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : 'Failed to revoke grant');
    }
  };

  const handleEvaluateRisk = async () => {
    setErrorMessage(null);
    try {
      const outcome = await fleetRequest<ArsyEvalOutcome>('/api/fleet/arsy/evaluate', {
        method: 'POST',
        body: JSON.stringify({
          subject: evalSubject,
          capability: evalCapability,
          riskTier: evalRiskTier,
          resource: evalResource,
          costUnits: Number(evalCostUnits),
        }),
      });
      setEvalOutcome(outcome);
      await refreshAll();
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : 'Risk evaluation failed');
    }
  };

  const handleVerifyChain = async () => {
    setVerifying(true);
    setErrorMessage(null);
    try {
      const verification = await fleetRequest<ArsyChainVerification>('/api/fleet/arsy/audit/verify');
      setChainStatus(verification);
      setStatusMessage(
        verification.valid
          ? `Merkle chain verified (${verification.count} entries)`
          : `Merkle chain TAMPERED at seq #${verification.brokenSeq}`,
      );
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : 'Merkle chain verification failed');
    } finally {
      setVerifying(false);
    }
  };

  const probeByKey = new Map<string, FleetServiceProbe>(
    (probeReport?.services ?? []).map((probe) => [probe.key, probe]),
  );

  const activeGrantsCount = grants.filter((grant) => grant.status === 'active').length;

  return (
    <div className="space-y-5">
      <StageHeroBanner
        sceneCode="STAGE 19"
        sticker="FLEET + ARSY"
        title="SOVEREIGN FLEET COCKPIT"
        subtitle="Concurrent /health probing, SQLite endpoint overrides, ARSY constitutional grants, and SHA-256 Merkle audit verification."
        tone="cyan"
        actions={
          <>
            <button
              type="button"
              onClick={handleProbeFleet}
              disabled={probing}
              className="riot-btn inline-flex items-center gap-2 bg-[#a3ff12] px-3.5 py-2 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#ffe031] disabled:opacity-60"
            >
              <Activity className="h-4 w-4" />
              {probing ? 'PROBING...' : 'PROBE FLEET'}
            </button>
            <button
              type="button"
              onClick={handleVerifyChain}
              disabled={verifying}
              className="riot-btn inline-flex items-center gap-2 bg-[#00f0ff] px-3.5 py-2 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#ffe031] disabled:opacity-60"
            >
              <ShieldCheck className="h-4 w-4" />
              {verifying ? 'VERIFYING...' : 'VERIFY SHA-256 MERKLE CHAIN'}
            </button>
            <button
              type="button"
              onClick={() => void refreshAll()}
              disabled={loading}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#fff6de] px-3 py-2 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#ffe031]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
              SYNC
            </button>
          </>
        }
      />

      {errorMessage ? (
        <div className="border-[2.5px] border-black bg-[#ff334b] px-4 py-2.5 font-mono text-xs font-extrabold text-white shadow-[4px_4px_0_#000]">
          {errorMessage}
        </div>
      ) : null}

      {statusMessage ? (
        <div className="border-[2.5px] border-black bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c] shadow-[4px_4px_0_#000]">
          {statusMessage}
        </div>
      ) : null}

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatTile
          label="FLEET ONLINE"
          value={probeReport ? `${probeReport.onlineCount}/${probeReport.totalCount}` : `0/${services.length}`}
          sublabel={probeReport ? `Checked ${probeReport.checkedAt}` : 'Click PROBE FLEET'}
          icon={<Activity className="h-4 w-4" />}
          tone={probeReport && probeReport.onlineCount > 0 ? 'ok' : 'yellow'}
        />
        <StatTile
          label="ACTIVE GRANTS"
          value={activeGrantsCount}
          sublabel={`${grants.length} total ARSY grants`}
          icon={<KeyRound className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="AUDIT ENTRIES"
          value={auditEntries.length}
          sublabel="SHA-256 hash-chained"
          icon={<Link2 className="h-4 w-4" />}
          tone="magenta"
        />
        <StatTile
          label="MERKLE INTEGRITY"
          value={chainStatus ? (chainStatus.valid ? 'VALID' : `BROKEN #${chainStatus.brokenSeq}`) : 'UNCHECKED'}
          sublabel={chainStatus ? `Head ${chainStatus.headHash.slice(0, 12)}...` : 'Click Verify Chain'}
          icon={<ShieldCheck className="h-4 w-4" />}
          tone={chainStatus ? (chainStatus.valid ? 'ok' : 'err') : 'cream'}
        />
      </div>

      {/* Sovereign Fleet Matrix + URL Editor */}
      <SectionCard
        title="Sovereign Fleet Matrix"
        subtitle="11 canonical MW_* endpoints with live /health telemetry and persistent SQLite URL overrides."
        icon={<Activity className="h-4 w-4" />}
        sticker="11 NODES"
        stickerTone="ok"
        accent="cyan"
      >
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
          {services.map((svc) => {
            const probe = probeByKey.get(svc.key);
            const isOnline = probe?.online ?? false;
            return (
              <div
                key={svc.key}
                className="border-2 border-black bg-[#0d0d16] p-3 shadow-[3px_3px_0_#000]"
              >
                <div className="mb-2 flex items-center justify-between gap-2">
                  <div className="min-w-0">
                    <div className="truncate font-display text-xs tracking-wide text-[#fff6de] uppercase">
                      {svc.name}
                    </div>
                    <div className="font-mono text-[10px] text-[#a6a2b8]">{svc.envVar || svc.key}</div>
                  </div>
                  <MonoBadge tone={probe ? (isOnline ? 'ok' : 'err') : 'neutral'}>
                    {probe ? (isOnline ? `ONLINE ${probe.latencyMs}ms` : probe.error || 'OFFLINE') : 'IDLE'}
                  </MonoBadge>
                </div>

                <div className="flex items-center gap-1.5">
                  <input
                    type="text"
                    value={urlDrafts[svc.key] ?? svc.url}
                    onChange={(event) =>
                      setUrlDrafts((prev) => ({ ...prev, [svc.key]: event.target.value }))
                    }
                    placeholder="http://127.0.0.1:8080"
                    className="min-w-0 flex-1 border-2 border-[#3a3a58] bg-[#13131d] px-2 py-1 font-mono text-[11px] text-[#fff6de] focus:border-[#00f0ff] focus:outline-none"
                  />
                  <button
                    type="button"
                    onClick={() => void handleSaveServiceUrl(svc)}
                    className="riot-btn inline-flex items-center gap-1 bg-[#ffe031] px-2 py-1 font-mono text-[10px] font-extrabold text-[#08080c] hover:bg-[#a3ff12]"
                    title="Save URL override"
                  >
                    <Save className="h-3 w-3" />
                    SAVE
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      </SectionCard>

      {/* ARSY Constitutional Grants + Risk Evaluator */}
      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <SectionCard
          title="ARSY Constitutional Grants"
          subtitle="Issue, inspect, and revoke explicit capability grants with budget limits."
          icon={<KeyRound className="h-4 w-4" />}
          sticker="GOVERNOR"
          stickerTone="yellow"
          accent="yellow"
        >
          <div className="mb-4 grid grid-cols-2 gap-2 border-2 border-black bg-[#0d0d16] p-3">
            <input
              type="text"
              value={grantSubject}
              onChange={(event) => setGrantSubject(event.target.value)}
              placeholder="subject (e.g. agent-ops)"
              className="border-2 border-[#3a3a58] bg-[#13131d] px-2 py-1.5 font-mono text-xs text-[#fff6de]"
            />
            <input
              type="text"
              value={grantCapability}
              onChange={(event) => setGrantCapability(event.target.value)}
              placeholder="capability (e.g. fleet.deploy)"
              className="border-2 border-[#3a3a58] bg-[#13131d] px-2 py-1.5 font-mono text-xs text-[#fff6de]"
            />
            <select
              value={grantRiskTier}
              onChange={(event) => setGrantRiskTier(event.target.value)}
              className="border-2 border-[#3a3a58] bg-[#13131d] px-2 py-1.5 font-mono text-xs text-[#fff6de]"
            >
              <option value="R0">R0 (Read-Only)</option>
              <option value="R1">R1 (Standard)</option>
              <option value="R2">R2 (Elevated)</option>
              <option value="R3">R3 (High Risk)</option>
              <option value="R4">R4 (Critical)</option>
            </select>
            <input
              type="number"
              min={1}
              value={grantBudgetLimit}
              onChange={(event) => setGrantBudgetLimit(Number(event.target.value))}
              placeholder="budget limit"
              className="border-2 border-[#3a3a58] bg-[#13131d] px-2 py-1.5 font-mono text-xs text-[#fff6de]"
            />
            <button
              type="button"
              onClick={() => void handleIssueGrant()}
              className="riot-btn col-span-2 inline-flex items-center justify-center gap-1.5 bg-[#a3ff12] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#ffe031]"
            >
              <Plus className="h-3.5 w-3.5" />
              ISSUE ARSY GRANT
            </button>
          </div>

          <div className="max-h-72 space-y-2 overflow-y-auto pr-1">
            {grants.length === 0 ? (
              <div className="font-mono text-xs text-[#a6a2b8]">No ARSY grants issued yet.</div>
            ) : (
              grants.map((grant) => (
                <div
                  key={grant.id}
                  className="flex flex-wrap items-center justify-between gap-2 border-2 border-black bg-[#0d0d16] px-3 py-2"
                >
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <MonoBadge tone={grant.status === 'active' ? 'ok' : 'err'}>
                        {grant.status}
                      </MonoBadge>
                      <MonoBadge tone={grant.riskTier === 'R3' || grant.riskTier === 'R4' ? 'magenta' : 'cyan'}>
                        {grant.riskTier}
                      </MonoBadge>
                      <span className="font-mono text-xs font-bold text-[#fff6de]">
                        {grant.subject} :: {grant.capability}
                      </span>
                    </div>
                    <div className="mt-1 font-mono text-[10px] text-[#a6a2b8]">
                      Budget {grant.budgetUsed}/{grant.budgetLimit} • ID {grant.id}
                    </div>
                  </div>
                  {grant.status === 'active' ? (
                    <button
                      type="button"
                      onClick={() => void handleRevokeGrant(grant.id)}
                      className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2 py-1 font-mono text-[10px] font-extrabold text-white"
                    >
                      <Trash2 className="h-3 w-3" />
                      REVOKE
                    </button>
                  ) : null}
                </div>
              ))
            )}
          </div>
        </SectionCard>

        <SectionCard
          title="ARSY Risk Evaluator & Merkle Audit"
          subtitle="Test CheckAndConsume against live grants and inspect the SHA-256 hash chain."
          icon={<ShieldAlert className="h-4 w-4" />}
          sticker="SHA-256"
          stickerTone="magenta"
          accent="magenta"
        >
          <div className="mb-4 grid grid-cols-2 gap-2 border-2 border-black bg-[#0d0d16] p-3">
            <input
              type="text"
              value={evalSubject}
              onChange={(event) => setEvalSubject(event.target.value)}
              placeholder="subject"
              className="border-2 border-[#3a3a58] bg-[#13131d] px-2 py-1.5 font-mono text-xs text-[#fff6de]"
            />
            <input
              type="text"
              value={evalCapability}
              onChange={(event) => setEvalCapability(event.target.value)}
              placeholder="capability"
              className="border-2 border-[#3a3a58] bg-[#13131d] px-2 py-1.5 font-mono text-xs text-[#fff6de]"
            />
            <select
              value={evalRiskTier}
              onChange={(event) => setEvalRiskTier(event.target.value)}
              className="border-2 border-[#3a3a58] bg-[#13131d] px-2 py-1.5 font-mono text-xs text-[#fff6de]"
            >
              <option value="R0">R0</option>
              <option value="R1">R1</option>
              <option value="R2">R2</option>
              <option value="R3">R3</option>
              <option value="R4">R4</option>
            </select>
            <input
              type="number"
              min={0}
              value={evalCostUnits}
              onChange={(event) => setEvalCostUnits(Number(event.target.value))}
              placeholder="cost units"
              className="border-2 border-[#3a3a58] bg-[#13131d] px-2 py-1.5 font-mono text-xs text-[#fff6de]"
            />
            <input
              type="text"
              value={evalResource}
              onChange={(event) => setEvalResource(event.target.value)}
              placeholder="resource (e.g. mw-lab/prod)"
              className="col-span-2 border-2 border-[#3a3a58] bg-[#13131d] px-2 py-1.5 font-mono text-xs text-[#fff6de]"
            />
            <button
              type="button"
              onClick={() => void handleEvaluateRisk()}
              className="riot-btn col-span-2 inline-flex items-center justify-center gap-1.5 bg-[#ffe031] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c] hover:bg-[#00f0ff]"
            >
              <Play className="h-3.5 w-3.5" />
              EVALUATE &amp; APPEND MERKLE AUDIT
            </button>
          </div>

          {evalOutcome ? (
            <div
              className={`mb-3 flex items-start gap-2 border-2 border-black px-3 py-2 font-mono text-xs font-bold ${
                evalOutcome.allowed ? 'bg-[#a3ff12] text-[#08080c]' : 'bg-[#ff334b] text-white'
              }`}
            >
              {evalOutcome.allowed ? (
                <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0" />
              ) : (
                <XCircle className="mt-0.5 h-4 w-4 shrink-0" />
              )}
              <div>
                <div>
                  {evalOutcome.decision.toUpperCase()} — {evalOutcome.reason}
                </div>
                <div className="text-[10px] opacity-85">
                  Seq #{evalOutcome.auditEntry.seq} • Hash {evalOutcome.auditEntry.entryHash.slice(0, 16)}...
                </div>
              </div>
            </div>
          ) : null}

          <div className="max-h-56 space-y-1.5 overflow-y-auto pr-1">
            {auditEntries.length === 0 ? (
              <div className="font-mono text-xs text-[#a6a2b8]">No Merkle audit entries recorded yet.</div>
            ) : (
              auditEntries.map((entry) => (
                <div
                  key={entry.id}
                  className="border border-[#2a2a42] bg-[#08080c] px-2.5 py-1.5 font-mono text-[11px]"
                >
                  <div className="flex flex-wrap items-center justify-between gap-1">
                    <span className="font-bold text-[#fff6de]">
                      #{entry.seq} {entry.actor} → {entry.action}
                    </span>
                    <div className="flex items-center gap-1">
                      <MonoBadge tone={entry.decision === 'allow' ? 'ok' : 'err'}>
                        {entry.decision}
                      </MonoBadge>
                      <ComicSticker tone="cream" tilt="rotate-0">
                        {entry.riskTier}
                      </ComicSticker>
                    </div>
                  </div>
                  <div className="mt-0.5 truncate text-[10px] text-[#a6a2b8]">
                    hash: {entry.entryHash.slice(0, 20)}... ← prev: {entry.prevHash.slice(0, 12)}...
                  </div>
                </div>
              ))
            )}
          </div>
        </SectionCard>
      </div>
    </div>
  );
}
