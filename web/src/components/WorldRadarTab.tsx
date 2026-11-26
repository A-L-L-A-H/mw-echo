import { useCallback, useEffect, useState } from 'react';
import {
  AlertTriangle,
  Bell,
  Check,
  CheckCircle2,
  Clock,
  ExternalLink,
  FileText,
  Globe2,
  HeartPulse,
  Layers,
  Pause,
  Play,
  Plus,
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
  Target,
  ThumbsDown,
  ThumbsUp,
  X,
} from 'lucide-react';
import {
  acknowledgeRadarAlert,
  addRadarTarget,
  createRadarReport,
  fetchRadarAlerts,
  fetchRadarDomains,
  fetchRadarEventDetail,
  fetchRadarEvents,
  fetchRadarInstances,
  fetchRadarLenses,
  fetchRadarPacks,
  fetchRadarReports,
  fetchRadarSourcesHealth,
  installRadarPack,
  pauseRadarInstance,
  runRadarInstance,
  submitRadarFeedback,
  type RadarAlertRecord,
  type RadarConnectorHealth,
  type RadarDomain,
  type RadarEventDetailResponse,
  type RadarEventSummary,
  type RadarInstance,
  type RadarLens,
  type RadarPack,
  type RadarReportRecord,
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

interface WorldRadarTabProps {
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

function resolvePriorityTone(priorityTier: string): BadgeTone {
  const tier = (priorityTier || '').toUpperCase();
  if (tier === 'P0' || tier === 'CRITICAL') return 'err';
  if (tier === 'P1' || tier === 'HIGH') return 'magenta';
  if (tier === 'P2' || tier === 'MEDIUM') return 'yellow';
  return 'neutral';
}

function resolveConfidenceTone(confidenceLabel: string): BadgeTone {
  const label = (confidenceLabel || '').toLowerCase();
  if (label === 'verified_primary') return 'ok';
  if (label === 'corroborated') return 'cyan';
  if (label === 'single_source') return 'yellow';
  if (label === 'disputed') return 'err';
  return 'neutral';
}

function normalizeScorePct(scoreVal: number): number {
  if (!Number.isFinite(scoreVal) || scoreVal <= 0) return 0;
  if (scoreVal <= 1) return Math.min(100, Math.round(scoreVal * 100));
  if (scoreVal <= 10) return Math.min(100, Math.round(scoreVal * 10));
  return Math.min(100, Math.round(scoreVal));
}

export function WorldRadarTab({ onNotify }: WorldRadarTabProps) {
  const [taxonomyVersion, setTaxonomyVersion] = useState('');
  const [domains, setDomains] = useState<RadarDomain[]>([]);
  const [lenses, setLenses] = useState<RadarLens[]>([]);
  const [showLenses, setShowLenses] = useState(false);

  const [packs, setPacks] = useState<RadarPack[]>([]);
  const [instances, setInstances] = useState<RadarInstance[]>([]);
  const [connectors, setConnectors] = useState<RadarConnectorHealth[]>([]);

  const [selectedDomain, setSelectedDomain] = useState('');
  const [events, setEvents] = useState<RadarEventSummary[]>([]);
  const [selectedEventId, setSelectedEventId] = useState('');
  const [eventDetail, setEventDetail] = useState<RadarEventDetailResponse | null>(null);
  const [loadingDetail, setLoadingDetail] = useState(false);

  const [alerts, setAlerts] = useState<RadarAlertRecord[]>([]);
  const [reports, setReports] = useState<RadarReportRecord[]>([]);
  const [selectedReportId, setSelectedReportId] = useState('');

  const [loadingAll, setLoadingAll] = useState(false);
  const [installingPackId, setInstallingPackId] = useState<string | null>(null);
  const [busyInstanceId, setBusyInstanceId] = useState<string | null>(null);

  // Add Watch Target inline form state
  const [targetFormInstanceId, setTargetFormInstanceId] = useState<string | null>(null);
  const [targetType, setTargetType] = useState('repository');
  const [canonicalName, setCanonicalName] = useState('anthropics/claude-code');
  const [connectorId, setConnectorId] = useState('github');
  const [sourceUri, setSourceUri] = useState('https://github.com/anthropics/claude-code');
  const [independenceGroup, setIndependenceGroup] = useState('anthropic-primary');
  const [savingTarget, setSavingTarget] = useState(false);

  // Analyst Feedback state
  const [feedbackReason, setFeedbackReason] = useState('');
  const [submittingVote, setSubmittingVote] = useState<string | null>(null);

  // Brief Generator state
  const [briefInstanceId, setBriefInstanceId] = useState('');
  const [briefTitle, setBriefTitle] = useState('Daily World Radar Intelligence Brief');
  const [briefScope, setBriefScope] = useState('24h');
  const [generatingBrief, setGeneratingBrief] = useState(false);

  const loadEventsForDomain = useCallback(async (domainCode: string) => {
    try {
      const reply = await fetchRadarEvents(domainCode, 50);
      const list = reply.events ?? [];
      setEvents(list);
      setSelectedEventId((prevId) => {
        if (prevId && list.some((evt) => evt.id === prevId)) return prevId;
        return list[0]?.id ?? '';
      });
    } catch {
      // Soft fail while backend boots.
    }
  }, []);

  const loadAllRadar = useCallback(async () => {
    setLoadingAll(true);
    try {
      const [
        domainsOutcome,
        lensesOutcome,
        packsOutcome,
        instancesOutcome,
        alertsOutcome,
        reportsOutcome,
        healthOutcome,
      ] = await Promise.allSettled([
        fetchRadarDomains(),
        fetchRadarLenses(),
        fetchRadarPacks(),
        fetchRadarInstances(),
        fetchRadarAlerts(),
        fetchRadarReports(),
        fetchRadarSourcesHealth(),
      ]);

      if (domainsOutcome.status === 'fulfilled') {
        setTaxonomyVersion(domainsOutcome.value.taxonomyVersion || 'v1');
        setDomains(domainsOutcome.value.domains ?? []);
      }
      if (lensesOutcome.status === 'fulfilled') {
        setLenses(lensesOutcome.value.lenses ?? []);
      }
      if (packsOutcome.status === 'fulfilled') {
        setPacks(packsOutcome.value.packs ?? []);
      }
      if (instancesOutcome.status === 'fulfilled') {
        const instList = instancesOutcome.value.instances ?? [];
        setInstances(instList);
        setBriefInstanceId((prev) => prev || instList[0]?.id || '');
      }
      if (alertsOutcome.status === 'fulfilled') {
        setAlerts(alertsOutcome.value.alerts ?? []);
      }
      if (reportsOutcome.status === 'fulfilled') {
        const repList = reportsOutcome.value.reports ?? [];
        setReports(repList);
        setSelectedReportId((prev) => prev || repList[0]?.id || '');
      }
      if (healthOutcome.status === 'fulfilled') {
        setConnectors(healthOutcome.value.connectors ?? []);
      }

      await loadEventsForDomain(selectedDomain);
    } finally {
      setLoadingAll(false);
    }
  }, [loadEventsForDomain, selectedDomain]);

  useEffect(() => {
    void loadAllRadar();
  }, [loadAllRadar]);

  useEffect(() => {
    void loadEventsForDomain(selectedDomain);
  }, [selectedDomain, loadEventsForDomain]);

  useEffect(() => {
    if (!selectedEventId) {
      setEventDetail(null);
      return;
    }
    let cancelled = false;
    setLoadingDetail(true);
    void fetchRadarEventDetail(selectedEventId)
      .then((detail) => {
        if (!cancelled) setEventDetail(detail);
      })
      .catch(() => {
        if (!cancelled) setEventDetail(null);
      })
      .finally(() => {
        if (!cancelled) setLoadingDetail(false);
      });
    return () => {
      cancelled = true;
    };
  }, [selectedEventId]);

  const handleInstallPack = async (pack: RadarPack) => {
    setInstallingPackId(pack.id);
    try {
      const installed = await installRadarPack(pack.id, {
        name: pack.name,
        mode: 'monitor',
      });
      onNotify('ok', `Installed radar pack "${installed.name}" (${pack.id}).`);
      setBriefInstanceId(installed.id);
      await loadAllRadar();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Pack install failed';
      onNotify('err', message);
    } finally {
      setInstallingPackId(null);
    }
  };

  const handleRunInstance = async (instance: RadarInstance) => {
    setBusyInstanceId(instance.id);
    try {
      const runOut = await runRadarInstance(instance.id);
      onNotify(
        'ok',
        `Scan "${instance.name}": ${runOut.observationsCreated} obs, ${runOut.eventsUpdated} events, ${runOut.alertsCreated} alerts.`,
      );
      await loadAllRadar();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Radar scan failed';
      onNotify('err', message);
    } finally {
      setBusyInstanceId(null);
    }
  };

  const handlePauseInstance = async (instance: RadarInstance) => {
    setBusyInstanceId(instance.id);
    try {
      const reply = await pauseRadarInstance(instance.id);
      onNotify('info', `Instance "${instance.name}" status -> ${reply.status}.`);
      await loadAllRadar();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Pause failed';
      onNotify('err', message);
    } finally {
      setBusyInstanceId(null);
    }
  };

  const handleAddTarget = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!targetFormInstanceId || !sourceUri.trim() || !canonicalName.trim()) {
      onNotify('err', 'Canonical name and Source URI are required.');
      return;
    }
    setSavingTarget(true);
    try {
      await addRadarTarget(targetFormInstanceId, {
        targetType: targetType.trim() || 'feed',
        canonicalName: canonicalName.trim(),
        connectorId: connectorId.trim() || 'rss',
        sourceUri: sourceUri.trim(),
        independenceGroup: independenceGroup.trim() || undefined,
      });
      onNotify('ok', `Attached watch target "${canonicalName.trim()}".`);
      setTargetFormInstanceId(null);
      await loadAllRadar();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to add target';
      onNotify('err', message);
    } finally {
      setSavingTarget(false);
    }
  };

  const handleAcknowledgeAlert = async (alert: RadarAlertRecord) => {
    try {
      await acknowledgeRadarAlert(alert.id);
      onNotify('ok', `Acknowledged alert "${alert.title}".`);
      await loadAllRadar();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Acknowledge failed';
      onNotify('err', message);
    }
  };

  const handleSubmitFeedback = async (vote: 'accurate' | 'disputed' | 'false_positive') => {
    if (!eventDetail) return;
    setSubmittingVote(vote);
    try {
      await submitRadarFeedback({
        targetKind: 'event',
        targetId: eventDetail.event.id,
        vote,
        reason: feedbackReason.trim() || `Analyst marked ${vote}`,
      });
      setFeedbackReason('');
      onNotify('ok', `Recorded analyst vote "${vote}" on event.`);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Feedback failed';
      onNotify('err', message);
    } finally {
      setSubmittingVote(null);
    }
  };

  const handleGenerateBrief = async (event: React.FormEvent) => {
    event.preventDefault();
    setGeneratingBrief(true);
    try {
      const createdReport = await createRadarReport({
        instanceId: briefInstanceId || instances[0]?.id || '',
        title: briefTitle.trim() || 'World Radar Intelligence Brief',
        scope: briefScope.trim() || '24h',
      });
      onNotify('ok', `Generated Section 11.1 brief "${createdReport.title}".`);
      setSelectedReportId(createdReport.id);
      await loadAllRadar();
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Brief generation failed';
      onNotify('err', message);
    } finally {
      setGeneratingBrief(false);
    }
  };

  const activeDomainMeta = domains.find((dom) => dom.id === selectedDomain) ?? null;
  const openAlerts = alerts.filter((alert) => alert.status !== 'acknowledged');
  const activeReport =
    reports.find((rep) => rep.id === selectedReportId) ?? reports[0] ?? null;

  const assessmentDimensions = eventDetail?.assessment
    ? [
        { label: 'IMPACT', value: eventDetail.assessment.impact, color: '#ff334b' },
        { label: 'URGENCY', value: eventDetail.assessment.urgency, color: '#ff2e88' },
        { label: 'RELEVANCE', value: eventDetail.assessment.relevance, color: '#ffe031' },
        { label: 'VELOCITY', value: eventDetail.assessment.velocity, color: '#00f0ff' },
        { label: 'NOVELTY', value: eventDetail.assessment.novelty, color: '#a855f7' },
        { label: 'ACTIONABILITY', value: eventDetail.assessment.actionability, color: '#a3ff12' },
        { label: 'EXPOSURE', value: eventDetail.assessment.exposure, color: '#ff6b00' },
      ]
    : [];

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 12"
        sticker="WORLD RADAR"
        title="R!OTS WORLD RADAR NETWORK"
        subtitle="23-DOMAIN INTELLIGENCE TAXONOMY · STRICT PRIORITY VS CONFIDENCE SEPARATION · 4-TIMESTAMP EVIDENCE DOSSIERS & SECTION 11.1 BRIEFS"
        tone="cyan"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {openAlerts.length > 0 ? (
              <MonoBadge tone="err">
                <Bell className="h-3 w-3" />
                {openAlerts.length} OPEN ALERT(S)
              </MonoBadge>
            ) : (
              <MonoBadge tone="ok">ALERTS CLEAR</MonoBadge>
            )}
            <button
              type="button"
              onClick={() => setShowLenses((prev) => !prev)}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#1b1b2b] px-3 py-2 font-mono text-xs font-extrabold text-[#00f0ff]"
            >
              <Layers className="h-3.5 w-3.5" />
              {showLenses ? 'HIDE' : '20'} LENSES
            </button>
            <button
              type="button"
              onClick={() => void loadAllRadar()}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingAll ? 'animate-spin' : ''}`} />
              SYNC RADAR
            </button>
          </div>
        }
      />

      {/* Top Radar Telemetry Stamps */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="TAXONOMY DOMAINS"
          value={domains.length || 23}
          sublabel={`Taxonomy ${taxonomyVersion || 'v1'} · ${lenses.length || 20} analytical lenses`}
          icon={<Globe2 className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="ACTIVE INSTANCES"
          value={instances.filter((inst) => inst.status === 'active').length}
          sublabel={`${instances.length} total installed · ${packs.length} Alpha packs`}
          icon={<Target className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="TRACKED EVENTS"
          value={events.length}
          sublabel={selectedDomain ? `Filtered by ${selectedDomain}` : 'Across all 23 domains'}
          icon={<ShieldCheck className="h-4 w-4" />}
          tone="magenta"
        />
        <StatTile
          label="CONNECTOR HEALTH"
          value={`${connectors.filter((conn) => conn.lastStatus !== 'error').length}/${connectors.length}`}
          sublabel={`${reports.length} Section 11.1 brief(s) filed`}
          icon={<HeartPulse className="h-4 w-4" />}
          tone="ok"
        />
      </div>

      {/* 1. Top 23-Domain Universe Ribbon (D01..D23) */}
      <SectionCard
        title="23-DOMAIN UNIVERSE RIBBON (D01..D23)"
        subtitle="Click any domain chip to filter the live Events & Priority/Confidence board."
        icon={<Globe2 className="h-4 w-4" />}
        sticker={selectedDomain || 'ALL 23'}
        stickerTone="cyan"
        accent="cyan"
      >
        <div className="flex flex-wrap gap-1.5">
          <button
            type="button"
            onClick={() => setSelectedDomain('')}
            className={`border-2 border-black px-2.5 py-1 font-mono text-[11px] font-extrabold uppercase transition-colors ${
              selectedDomain === ''
                ? 'bg-[#ffe031] text-[#08080c] shadow-[3px_3px_0_#ff2e88]'
                : 'bg-[#08080c] text-[#fff6de] hover:bg-[#1b1b2b]'
            }`}
          >
            ALL DOMAINS
          </button>
          {domains.map((domain) => {
            const isSelected = selectedDomain === domain.id;
            return (
              <button
                key={domain.id}
                type="button"
                onClick={() => setSelectedDomain(isSelected ? '' : domain.id)}
                title={domain.summary}
                className={`border-2 border-black px-2.5 py-1 font-mono text-[11px] font-extrabold transition-colors ${
                  isSelected
                    ? 'bg-[#00f0ff] text-[#08080c] shadow-[3px_3px_0_#ffe031]'
                    : 'bg-[#08080c] text-[#fff6de] hover:bg-[#1b1b2b] hover:text-[#00f0ff]'
                }`}
              >
                <span className={isSelected ? 'text-[#08080c]' : 'text-[#ffe031]'}>
                  {domain.id}
                </span>{' '}
                {domain.name}
              </button>
            );
          })}
        </div>

        {activeDomainMeta ? (
          <div className="mt-3 border-2 border-black bg-[#08080c] p-3">
            <div className="flex flex-wrap items-center gap-2">
              <MonoBadge tone="cyan">{activeDomainMeta.id}</MonoBadge>
              <span className="font-display text-xs text-[#ffe031] uppercase">
                {activeDomainMeta.name}
              </span>
              <span className="font-mono text-xs text-[#a6a2b8]">{activeDomainMeta.summary}</span>
            </div>
            {(activeDomainMeta.subdomains ?? []).length > 0 ? (
              <div className="mt-2 flex flex-wrap gap-1">
                {activeDomainMeta.subdomains.map((subName) => (
                  <MonoBadge key={subName} tone="neutral">
                    {subName}
                  </MonoBadge>
                ))}
              </div>
            ) : null}
          </div>
        ) : null}

        {showLenses && lenses.length > 0 ? (
          <div className="mt-3 grid grid-cols-1 gap-2 border-t-2 border-black pt-3 md:grid-cols-2 xl:grid-cols-4">
            {lenses.map((lens) => (
              <div key={lens.id} className="border-2 border-black bg-[#08080c] p-2.5">
                <div className="flex items-center gap-1.5">
                  <MonoBadge tone="magenta">{lens.id}</MonoBadge>
                  <span className="font-mono text-xs font-extrabold text-[#fff6de]">
                    {lens.name}
                  </span>
                </div>
                <p className="mt-1 font-mono text-[10px] text-[#a6a2b8]">{lens.question}</p>
              </div>
            ))}
          </div>
        ) : null}
      </SectionCard>

      {/* 2. Radar Packs, Active Instances & Connector Health */}
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-12">
        {/* 6 Alpha Packs */}
        <SectionCard
          title="ALPHA RADAR PACKS"
          subtitle="Deploy curated multi-domain intelligence packs with 1 click."
          icon={<Layers className="h-4 w-4" />}
          sticker={`${packs.length} PACKS`}
          stickerTone="yellow"
          accent="yellow"
          className="xl:col-span-5"
        >
          <div className="space-y-3">
            {packs.length === 0 ? (
              <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] py-8 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
                LOADING ALPHA PACKS...
              </div>
            ) : (
              packs.map((pack) => (
                <div
                  key={pack.id}
                  className="border-2 border-black bg-[#08080c] p-3 shadow-[4px_4px_0_#000]"
                >
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="font-display text-xs text-[#fff6de] uppercase">
                          {pack.name}
                        </span>
                        <MonoBadge tone="neutral">v{pack.version}</MonoBadge>
                      </div>
                      <p className="mt-1 font-mono text-[11px] text-[#a6a2b8]">
                        {pack.description}
                      </p>
                    </div>
                    <button
                      type="button"
                      disabled={installingPackId === pack.id}
                      onClick={() => void handleInstallPack(pack)}
                      className="riot-btn shrink-0 bg-[#ffe031] px-3 py-1 font-mono text-[10px] font-extrabold text-[#08080c] uppercase disabled:opacity-50"
                    >
                      {installingPackId === pack.id ? '...' : 'INSTALL'}
                    </button>
                  </div>
                  <div className="mt-2 flex flex-wrap gap-1">
                    {(pack.domains ?? []).slice(0, 5).map((domCode) => (
                      <MonoBadge key={domCode} tone="cyan">
                        {domCode}
                      </MonoBadge>
                    ))}
                    {(pack.requiredConnectors ?? []).slice(0, 3).map((connName) => (
                      <MonoBadge key={connName} tone="neutral">
                        {connName}
                      </MonoBadge>
                    ))}
                  </div>
                </div>
              ))
            )}
          </div>
        </SectionCard>

        {/* Active Instances + Watch Target Attachment + Connector Health */}
        <SectionCard
          title="ACTIVE RADAR INSTANCES & CONNECTOR HEALTH"
          subtitle="Attach RSS/Atom, GitHub Releases, or Web JSON targets, trigger live scans, and monitor connector health."
          icon={<Target className="h-4 w-4" />}
          sticker={`${instances.length} LIVE`}
          stickerTone="ok"
          accent="ok"
          className="xl:col-span-7"
        >
          {/* Active Instances List */}
          <div className="space-y-3">
            {instances.length === 0 ? (
              <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] py-8 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
                NO INSTANCES DEPLOYED YET. INSTALL ANY ALPHA PACK ON THE LEFT!
              </div>
            ) : (
              instances.map((instance) => {
                const isBusy = busyInstanceId === instance.id;
                const isTargetFormOpen = targetFormInstanceId === instance.id;
                return (
                  <div
                    key={instance.id}
                    className="border-2 border-black bg-[#08080c] p-3.5 shadow-[4px_4px_0_#000]"
                  >
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <div>
                        <div className="flex flex-wrap items-center gap-1.5">
                          <span className="font-display text-sm text-[#fff6de] uppercase">
                            {instance.name}
                          </span>
                          <MonoBadge tone="cream">{instance.packId}</MonoBadge>
                          <MonoBadge tone={instance.status === 'active' ? 'ok' : 'yellow'}>
                            {instance.status}
                          </MonoBadge>
                          <MonoBadge tone="cyan">{instance.targetCount} TARGETS</MonoBadge>
                        </div>
                        <div className="mt-1 font-mono text-[10px] font-bold text-[#a6a2b8]">
                          LAST RUN: {instance.lastRunStatus || 'never'} ·{' '}
                          {formatShortTime(instance.lastRunAt)}
                        </div>
                      </div>

                      <div className="flex flex-wrap items-center gap-1.5">
                        <button
                          type="button"
                          disabled={isBusy}
                          onClick={() => void handleRunInstance(instance)}
                          className="riot-btn inline-flex items-center gap-1 bg-[#a3ff12] px-3 py-1 font-mono text-[10px] font-extrabold text-[#08080c] uppercase disabled:opacity-50"
                        >
                          <Play className="h-3 w-3" />
                          {isBusy ? 'SCANNING...' : 'RUN SCAN'}
                        </button>
                        <button
                          type="button"
                          disabled={isBusy}
                          onClick={() => void handlePauseInstance(instance)}
                          className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-2.5 py-1 font-mono text-[10px] font-extrabold text-[#ffe031] uppercase disabled:opacity-50"
                        >
                          <Pause className="h-3 w-3" />
                          PAUSE
                        </button>
                        <button
                          type="button"
                          onClick={() =>
                            setTargetFormInstanceId(isTargetFormOpen ? null : instance.id)
                          }
                          className="riot-btn inline-flex items-center gap-1 bg-[#00f0ff] px-2.5 py-1 font-mono text-[10px] font-extrabold text-[#08080c] uppercase"
                        >
                          <Plus className="h-3 w-3" />
                          TARGET
                        </button>
                      </div>
                    </div>

                    {/* Inline Watch Target Form */}
                    {isTargetFormOpen ? (
                      <form
                        onSubmit={handleAddTarget}
                        className="mt-3 space-y-2.5 border-2 border-[#00f0ff] bg-[#13131d] p-3"
                      >
                        <div className="flex items-center justify-between">
                          <ComicSticker tone="cyan">ATTACH WATCH TARGET</ComicSticker>
                          <button
                            type="button"
                            onClick={() => setTargetFormInstanceId(null)}
                            className="text-[#a6a2b8] hover:text-[#ffe031]"
                          >
                            <X className="h-4 w-4" />
                          </button>
                        </div>

                        <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                          <select
                            value={connectorId}
                            onChange={(ev) => {
                              const nextConn = ev.target.value;
                              setConnectorId(nextConn);
                              if (nextConn === 'github') {
                                setTargetType('repository');
                                setCanonicalName('anthropics/claude-code');
                                setSourceUri('https://github.com/anthropics/claude-code');
                                setIndependenceGroup('anthropic-primary');
                              } else if (nextConn === 'rss') {
                                setTargetType('feed');
                                setCanonicalName('Hacker News Frontpage');
                                setSourceUri('https://hnrss.org/frontpage');
                                setIndependenceGroup('ycombinator-rss');
                              } else {
                                setTargetType('feed');
                              }
                            }}
                            className="riot-input px-2.5 py-1.5 font-mono text-xs font-bold"
                          >
                            <option value="github">github (GitHub Releases / Commits)</option>
                            <option value="rss">rss (RSS 2.0 / Atom Feed)</option>
                            <option value="web_feed">web_feed (JSON / Web Feed)</option>
                          </select>
                          <input
                            type="text"
                            value={canonicalName}
                            onChange={(ev) => setCanonicalName(ev.target.value)}
                            placeholder="Canonical Name"
                            className="riot-input px-2.5 py-1.5 font-mono text-xs"
                          />
                          <input
                            type="text"
                            value={independenceGroup}
                            onChange={(ev) => setIndependenceGroup(ev.target.value)}
                            placeholder="Independence group"
                            className="riot-input px-2.5 py-1.5 font-mono text-xs"
                          />
                        </div>

                        <div className="flex flex-wrap gap-2">
                          <input
                            type="text"
                            value={sourceUri}
                            onChange={(ev) => setSourceUri(ev.target.value)}
                            placeholder="https://github.com/anthropics/claude-code or RSS/JSON URL"
                            className="riot-input min-w-[220px] flex-1 px-2.5 py-1.5 font-mono text-xs"
                          />
                          <button
                            type="submit"
                            disabled={savingTarget}
                            className="riot-btn bg-[#ffe031] px-4 py-1.5 font-mono text-xs font-extrabold text-[#08080c] uppercase disabled:opacity-50"
                          >
                            {savingTarget ? '...' : 'ATTACH'}
                          </button>
                        </div>
                      </form>
                    ) : null}
                  </div>
                );
              })
            )}
          </div>

          {/* Connector Health Indicators */}
          {connectors.length > 0 ? (
            <div className="mt-4 border-t-2 border-black pt-4">
              <div className="mb-2 font-mono text-[10px] font-extrabold uppercase tracking-wider text-[#00f0ff]">
                CONNECTOR HEALTH TELEMETRY
              </div>
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
                {connectors.map((conn) => {
                  const hasError =
                    conn.lastStatus === 'connector_failed' ||
                    conn.lastStatus === 'rate_limited' ||
                    conn.lastStatus === 'error' ||
                    Boolean(conn.lastError);
                  return (
                    <div
                      key={conn.connectorId}
                      className="border-2 border-black bg-[#08080c] px-2.5 py-2"
                    >
                      <div className="flex items-center justify-between gap-1.5">
                        <span className="truncate font-mono text-[11px] font-extrabold text-[#fff6de]">
                          {conn.connectorId}
                        </span>
                        <MonoBadge tone={hasError ? 'err' : 'ok'}>
                          {conn.lastStatus || 'ready'}
                        </MonoBadge>
                      </div>
                      <div className="mt-1 font-mono text-[10px] text-[#a6a2b8]">
                        RUNS {conn.totalRuns} · FAIL {conn.failedRuns} ·{' '}
                        {formatShortTime(conn.lastSuccessAt)}
                      </div>
                      {conn.lastError ? (
                        <div className="mt-1 truncate font-mono text-[10px] text-[#ff334b]">
                          {conn.lastError}
                        </div>
                      ) : null}
                    </div>
                  );
                })}
              </div>
            </div>
          ) : null}
        </SectionCard>
      </div>

      {/* Live Alerts Strip (Only shown if alerts exist — clean, zero flashing!) */}
      {openAlerts.length > 0 ? (
        <SectionCard
          title="VERIFICATION & PRIORITY ALERTS"
          subtitle="Acknowledge triaged alerts. Verification-required alerts explicitly highlight single-source or unverified high-impact signals."
          icon={<Bell className="h-4 w-4" />}
          sticker={`${openAlerts.length} OPEN`}
          stickerTone="err"
          accent="err"
        >
          <div className="space-y-2">
            {openAlerts.map((alert) => (
              <div
                key={alert.id}
                className="flex flex-wrap items-center justify-between gap-2 border-2 border-black bg-[#08080c] px-3 py-2.5"
              >
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <MonoBadge tone={resolvePriorityTone(alert.priorityTier)}>
                    PRIORITY: {alert.priorityTier}
                  </MonoBadge>
                  <MonoBadge tone={resolveConfidenceTone(alert.confidenceLabel)}>
                    CONF: {alert.confidenceLabel}
                  </MonoBadge>
                  {alert.alertKind === 'verification_required' ? (
                    <MonoBadge tone="magenta">
                      <AlertTriangle className="h-3 w-3" />
                      VERIFICATION REQUIRED
                    </MonoBadge>
                  ) : (
                    <MonoBadge tone="neutral">{alert.alertKind}</MonoBadge>
                  )}
                  <button
                    type="button"
                    onClick={() => setSelectedEventId(alert.eventId)}
                    className="truncate text-left font-mono text-xs font-bold text-[#fff6de] underline hover:text-[#ffe031]"
                  >
                    {alert.title}
                  </button>
                </div>

                <button
                  type="button"
                  onClick={() => void handleAcknowledgeAlert(alert)}
                  className="riot-btn inline-flex shrink-0 items-center gap-1 bg-[#a3ff12] px-2.5 py-1 font-mono text-[10px] font-extrabold text-[#08080c] uppercase"
                >
                  <Check className="h-3 w-3" />
                  ACK
                </button>
              </div>
            ))}
          </div>
        </SectionCard>
      ) : null}

      {/* 3. Events & Priority vs. Confidence Board + Evidence Dossier */}
      <SectionCard
        title="EVENTS BOARD // PRIORITY VS. CONFIDENCE SEPARATION"
        subtitle="Priority tier (P0..P3) and Epistemic Confidence (verified_primary -> disputed) are ALWAYS rendered separately."
        icon={<ShieldAlert className="h-4 w-4" />}
        sticker="EVIDENCE FIRST"
        stickerTone="magenta"
        accent="magenta"
      >
        <div className="grid grid-cols-1 gap-5 lg:grid-cols-12">
          {/* Left: Events List */}
          <div className="max-h-[72vh] space-y-2.5 overflow-y-auto pr-1 lg:col-span-5">
            {events.length === 0 ? (
              <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] py-10 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
                NO EVENTS IN THIS DOMAIN YET. INSTALL A PACK &amp; HIT &quot;RUN SCAN&quot;.
              </div>
            ) : (
              events.map((evt) => {
                const isSelected = evt.id === selectedEventId;
                const needsVerification = evt.alertKind === 'verification_required';
                return (
                  <button
                    key={evt.id}
                    type="button"
                    onClick={() => setSelectedEventId(evt.id)}
                    className={`block w-full border-2 border-black p-3 text-left transition-all ${
                      isSelected
                        ? 'bg-[#1b1b2b] shadow-[5px_5px_0_#ffe031]'
                        : 'bg-[#08080c] shadow-[3px_3px_0_#000] hover:bg-[#151522]'
                    }`}
                  >
                    {/* Strictly separated Priority vs Confidence badges */}
                    <div className="mb-2 flex flex-wrap items-center gap-1.5">
                      <MonoBadge tone={resolvePriorityTone(evt.priorityTier)}>
                        PRIORITY {evt.priorityTier} ({Math.round(evt.priorityScore)})
                      </MonoBadge>
                      <MonoBadge tone={resolveConfidenceTone(evt.confidenceLabel)}>
                        CONF: {evt.confidenceLabel}
                      </MonoBadge>
                      {needsVerification ? (
                        <MonoBadge tone="magenta">
                          <AlertTriangle className="h-3 w-3" />
                          VERIFY
                        </MonoBadge>
                      ) : null}
                    </div>

                    <div className="font-display text-xs leading-snug text-[#fff6de] uppercase">
                      {evt.title}
                    </div>
                    {evt.summary ? (
                      <p className="mt-1 line-clamp-2 font-mono text-[11px] text-[#a6a2b8]">
                        {evt.summary}
                      </p>
                    ) : null}

                    <div className="mt-2 flex flex-wrap items-center justify-between gap-2 font-mono text-[10px] font-bold text-[#a6a2b8]">
                      <div className="flex flex-wrap gap-1">
                        <MonoBadge tone="cyan">{evt.primaryDomain}</MonoBadge>
                        <MonoBadge tone="neutral">{evt.independentSources} IND. SRC</MonoBadge>
                      </div>
                      <span>OBS {formatShortTime(evt.observedAt)}</span>
                    </div>
                  </button>
                );
              })
            )}
          </div>

          {/* Right: Event Evidence Dossier */}
          <div className="border-2 border-black bg-[#08080c] p-4 lg:col-span-7">
            {loadingDetail ? (
              <div className="font-mono text-xs text-[#a6a2b8]">Loading evidence dossier...</div>
            ) : !eventDetail ? (
              <div className="flex h-full min-h-[320px] flex-col items-center justify-center gap-3 text-center">
                <ComicSticker tone="cream">SELECT AN EVENT</ComicSticker>
                <p className="max-w-sm font-mono text-xs text-[#a6a2b8]">
                  Inspect all 4 timestamps, the 7-dimension Priority breakdown, raw evidence links,
                  and record analyst verification votes.
                </p>
              </div>
            ) : (
              <div className="space-y-4">
                {/* Header & Separated Priority / Confidence */}
                <div className="border-b-2 border-black pb-3">
                  <div className="mb-2 flex flex-wrap items-center gap-1.5">
                    <MonoBadge tone={resolvePriorityTone(eventDetail.event.priorityTier)}>
                      PRIORITY {eventDetail.event.priorityTier} · SCORE{' '}
                      {Math.round(eventDetail.event.priorityScore)}
                    </MonoBadge>
                    <MonoBadge tone={resolveConfidenceTone(eventDetail.event.confidenceLabel)}>
                      CONFIDENCE: {eventDetail.event.confidenceLabel}
                    </MonoBadge>
                    <MonoBadge tone="cyan">{eventDetail.event.primaryDomain}</MonoBadge>
                    <MonoBadge tone="neutral">
                      {eventDetail.event.independentSources} INDEPENDENT SOURCE(S)
                    </MonoBadge>
                    {eventDetail.event.alertKind === 'verification_required' ? (
                      <ComicSticker tone="magenta">VERIFICATION REQUIRED</ComicSticker>
                    ) : null}
                  </div>

                  <h3 className="font-display text-base text-[#ffe031] uppercase">
                    {eventDetail.event.title}
                  </h3>
                  {eventDetail.event.summary ? (
                    <p className="mt-1 text-xs leading-relaxed text-[#fff6de]/90">
                      {eventDetail.event.summary}
                    </p>
                  ) : null}
                </div>

                {/* All 4 Explicit Timestamps */}
                <div className="border-2 border-black bg-[#13131d] p-3">
                  <div className="mb-2 flex items-center justify-between">
                    <span className="inline-flex items-center gap-1.5 font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                      <Clock className="h-3.5 w-3.5" />
                      4-TIMESTAMP TEMPORAL CHAIN
                    </span>
                    <MonoBadge tone="neutral">
                      PRECISION: {eventDetail.event.timePrecision || 'exact'}
                    </MonoBadge>
                  </div>
                  <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                    {[
                      { label: 'OCCURRED', value: eventDetail.event.occurredAt },
                      { label: 'PUBLISHED', value: eventDetail.event.publishedAt },
                      { label: 'OBSERVED', value: eventDetail.event.observedAt },
                      { label: 'INGESTED', value: eventDetail.event.ingestedAt },
                    ].map((tsEntry) => (
                      <div key={tsEntry.label} className="border border-black bg-[#08080c] p-2">
                        <div className="font-mono text-[9px] font-extrabold text-[#ffe031]">
                          {tsEntry.label}
                        </div>
                        <div className="mt-0.5 font-mono text-[11px] font-bold text-[#fff6de]">
                          {formatShortTime(tsEntry.value)}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>

                {/* 7-Dimension Priority Breakdown */}
                {assessmentDimensions.length > 0 ? (
                  <div className="border-2 border-black bg-[#13131d] p-3">
                    <div className="mb-2 font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                      7-DIMENSION PRIORITY BREAKDOWN
                    </div>
                    <div className="space-y-1.5">
                      {assessmentDimensions.map((dim) => {
                        const pct = normalizeScorePct(dim.value);
                        return (
                          <div key={dim.label} className="flex items-center gap-2 font-mono text-[10px]">
                            <span className="w-28 shrink-0 font-extrabold text-[#fff6de]">
                              {dim.label}
                            </span>
                            <div className="h-2.5 flex-1 overflow-hidden border border-black bg-[#08080c]">
                              <div
                                className="h-full"
                                style={{ width: `${pct}%`, backgroundColor: dim.color }}
                              />
                            </div>
                            <span className="w-10 text-right font-extrabold text-[#a6a2b8]">
                              {typeof dim.value === 'number' ? dim.value.toFixed(1) : '0'}
                            </span>
                          </div>
                        );
                      })}
                    </div>

                    {eventDetail.assessment.rationale ? (
                      <p className="mt-2.5 border-t border-[#252538] pt-2 font-mono text-[11px] text-[#a6a2b8]">
                        {eventDetail.assessment.rationale}
                      </p>
                    ) : null}

                    {(eventDetail.assessment.uncertaintyFlags ?? []).length > 0 ? (
                      <div className="mt-2 flex flex-wrap gap-1">
                        {eventDetail.assessment.uncertaintyFlags.map((flagText) => (
                          <MonoBadge key={flagText} tone="err">
                            UNCERTAINTY: {flagText}
                          </MonoBadge>
                        ))}
                      </div>
                    ) : null}
                  </div>
                ) : null}

                {/* Raw Source Evidence Links */}
                <div className="space-y-2">
                  <div className="font-mono text-[10px] font-extrabold uppercase text-[#a3ff12]">
                    RAW EVIDENCE ARTIFACTS ({(eventDetail.evidence ?? []).length})
                  </div>
                  {(eventDetail.evidence ?? []).length === 0 ? (
                    <div className="border border-dashed border-[#3a3a58] p-3 font-mono text-xs text-[#a6a2b8]">
                      No raw evidence artifacts attached.
                    </div>
                  ) : (
                    (eventDetail.evidence ?? []).map((evItem) => (
                      <div
                        key={evItem.artifactId}
                        className="border-2 border-black bg-[#13131d] p-3"
                      >
                        <div className="flex flex-wrap items-start justify-between gap-2">
                          <div className="min-w-0">
                            <div className="font-mono text-xs font-extrabold text-[#fff6de]">
                              {evItem.title || evItem.artifactId}
                            </div>
                            <div className="mt-1 flex flex-wrap gap-1">
                              <MonoBadge tone="cyan">{evItem.independenceGroup || 'primary'}</MonoBadge>
                              {evItem.language ? (
                                <MonoBadge tone="neutral">{evItem.language}</MonoBadge>
                              ) : null}
                              <span className="font-mono text-[10px] text-[#a6a2b8]">
                                Captured {formatShortTime(evItem.capturedAt)}
                              </span>
                            </div>
                          </div>
                          {evItem.sourceUri ? (
                            <a
                              href={evItem.sourceUri}
                              target="_blank"
                              rel="noreferrer"
                              className="riot-btn inline-flex shrink-0 items-center gap-1 bg-[#00f0ff] px-2.5 py-1 font-mono text-[10px] font-extrabold text-[#08080c]"
                            >
                              SOURCE
                              <ExternalLink className="h-3 w-3" />
                            </a>
                          ) : null}
                        </div>
                        {evItem.excerpt ? (
                          <p className="mt-2 border-l-2 border-[#ffe031] pl-2.5 font-mono text-[11px] leading-relaxed text-[#fff6de]/80">
                            {evItem.excerpt}
                          </p>
                        ) : null}
                      </div>
                    ))
                  )}
                </div>

                {/* Analyst Feedback Controls */}
                <div className="border-2 border-black bg-[#13131d] p-3">
                  <div className="mb-2 font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                    ANALYST VERIFICATION FEEDBACK
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <input
                      type="text"
                      value={feedbackReason}
                      onChange={(ev) => setFeedbackReason(ev.target.value)}
                      placeholder="Optional analyst rationale..."
                      className="riot-input min-w-[200px] flex-1 px-2.5 py-1.5 font-mono text-xs"
                    />
                    <button
                      type="button"
                      disabled={submittingVote !== null}
                      onClick={() => void handleSubmitFeedback('accurate')}
                      className="riot-btn inline-flex items-center gap-1 bg-[#a3ff12] px-2.5 py-1.5 font-mono text-[10px] font-extrabold text-[#08080c] uppercase disabled:opacity-50"
                    >
                      <ThumbsUp className="h-3 w-3" />
                      ACCURATE
                    </button>
                    <button
                      type="button"
                      disabled={submittingVote !== null}
                      onClick={() => void handleSubmitFeedback('disputed')}
                      className="riot-btn inline-flex items-center gap-1 bg-[#ffe031] px-2.5 py-1.5 font-mono text-[10px] font-extrabold text-[#08080c] uppercase disabled:opacity-50"
                    >
                      <AlertTriangle className="h-3 w-3" />
                      DISPUTED
                    </button>
                    <button
                      type="button"
                      disabled={submittingVote !== null}
                      onClick={() => void handleSubmitFeedback('false_positive')}
                      className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2.5 py-1.5 font-mono text-[10px] font-extrabold text-white uppercase disabled:opacity-50"
                    >
                      <ThumbsDown className="h-3 w-3" />
                      FALSE POSITIVE
                    </button>
                  </div>
                </div>
              </div>
            )}
          </div>
        </div>
      </SectionCard>

      {/* 4. Evidence-Linked Intelligence Brief Generator (Section 11.1) */}
      <SectionCard
        title="SECTION 11.1 // EVIDENCE-LINKED INTELLIGENCE BRIEF GENERATOR"
        subtitle="Generates structured briefs separating Observed Facts, Reported Claims, Inferences, Unknowns & Evidence Refs."
        icon={<FileText className="h-4 w-4" />}
        sticker="SEC 11.1"
        stickerTone="cream"
        accent="yellow"
      >
        <form
          onSubmit={handleGenerateBrief}
          className="mb-4 grid grid-cols-1 gap-2.5 border-2 border-black bg-[#08080c] p-3.5 md:grid-cols-12"
        >
          <select
            value={briefInstanceId}
            onChange={(ev) => setBriefInstanceId(ev.target.value)}
            className="riot-input px-2.5 py-2 font-mono text-xs font-bold md:col-span-3"
          >
            <option value="">All / Global Scope</option>
            {instances.map((inst) => (
              <option key={inst.id} value={inst.id}>
                {inst.name} ({inst.packId})
              </option>
            ))}
          </select>
          <input
            type="text"
            value={briefTitle}
            onChange={(ev) => setBriefTitle(ev.target.value)}
            placeholder="Brief title"
            className="riot-input px-2.5 py-2 font-mono text-xs md:col-span-5"
          />
          <input
            type="text"
            value={briefScope}
            onChange={(ev) => setBriefScope(ev.target.value)}
            placeholder="Scope (24h / 7d / D06)"
            className="riot-input px-2.5 py-2 font-mono text-xs md:col-span-2"
          />
          <button
            type="submit"
            disabled={generatingBrief}
            className="riot-btn inline-flex items-center justify-center gap-1.5 bg-[#ffe031] px-3 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50 md:col-span-2"
          >
            <Sparkles className="h-3.5 w-3.5" />
            {generatingBrief ? '...' : 'GENERATE'}
          </button>
        </form>

        {reports.length === 0 ? (
          <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] py-8 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
            NO INTELLIGENCE BRIEFS GENERATED YET. CLICK &quot;GENERATE&quot; ABOVE.
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
            {/* Brief selector list */}
            <div className="space-y-2 lg:col-span-4">
              {reports.map((report) => {
                const isSelected = activeReport?.id === report.id;
                return (
                  <button
                    key={report.id}
                    type="button"
                    onClick={() => setSelectedReportId(report.id)}
                    className={`block w-full border-2 border-black p-3 text-left ${
                      isSelected
                        ? 'bg-[#ffe031] text-[#08080c] shadow-[4px_4px_0_#ff2e88]'
                        : 'bg-[#08080c] text-[#fff6de] hover:bg-[#1b1b2b]'
                    }`}
                  >
                    <div className="font-display text-xs uppercase">{report.title}</div>
                    <div
                      className={`mt-1 font-mono text-[10px] font-bold ${
                        isSelected ? 'text-[#08080c]/75' : 'text-[#a6a2b8]'
                      }`}
                    >
                      SCOPE: {report.scope || 'all'} · {formatShortTime(report.createdAt)}
                    </div>
                  </button>
                );
              })}
            </div>

            {/* Structured Section 11.1 Dossier Renderer */}
            {activeReport ? (
              <div className="space-y-3 border-2 border-black bg-[#08080c] p-4 lg:col-span-8">
                <div className="flex flex-wrap items-center justify-between gap-2 border-b-2 border-black pb-2">
                  <h3 className="font-display text-base text-[#ffe031] uppercase">
                    {activeReport.title}
                  </h3>
                  <MonoBadge tone="cream">SCOPE: {activeReport.scope || 'all'}</MonoBadge>
                </div>

                <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                  <div className="border-2 border-black bg-[#13131d] p-3">
                    <div className="mb-2 flex items-center gap-1.5">
                      <CheckCircle2 className="h-3.5 w-3.5 text-[#a3ff12]" />
                      <MonoBadge tone="ok">OBSERVED FACTS</MonoBadge>
                    </div>
                    <ul className="list-disc space-y-1 pl-4 font-mono text-[11px] text-[#fff6de]">
                      {(activeReport.observedFacts ?? []).length > 0 ? (
                        activeReport.observedFacts.map((factLine, idx) => (
                          <li key={idx}>{factLine}</li>
                        ))
                      ) : (
                        <li className="text-[#a6a2b8]">None recorded.</li>
                      )}
                    </ul>
                  </div>

                  <div className="border-2 border-black bg-[#13131d] p-3">
                    <div className="mb-2 flex items-center gap-1.5">
                      <MonoBadge tone="yellow">REPORTED CLAIMS</MonoBadge>
                    </div>
                    <ul className="list-disc space-y-1 pl-4 font-mono text-[11px] text-[#fff6de]">
                      {(activeReport.reportedClaims ?? []).length > 0 ? (
                        activeReport.reportedClaims.map((claimLine, idx) => (
                          <li key={idx}>{claimLine}</li>
                        ))
                      ) : (
                        <li className="text-[#a6a2b8]">None recorded.</li>
                      )}
                    </ul>
                  </div>

                  <div className="border-2 border-black bg-[#13131d] p-3">
                    <div className="mb-2 flex items-center gap-1.5">
                      <MonoBadge tone="cyan">INFERENCES</MonoBadge>
                    </div>
                    <ul className="list-disc space-y-1 pl-4 font-mono text-[11px] text-[#fff6de]">
                      {(activeReport.inferences ?? []).length > 0 ? (
                        activeReport.inferences.map((infLine, idx) => (
                          <li key={idx}>{infLine}</li>
                        ))
                      ) : (
                        <li className="text-[#a6a2b8]">None recorded.</li>
                      )}
                    </ul>
                  </div>

                  <div className="border-2 border-black bg-[#13131d] p-3">
                    <div className="mb-2 flex items-center gap-1.5">
                      <MonoBadge tone="magenta">OPEN UNKNOWNS</MonoBadge>
                    </div>
                    <ul className="list-disc space-y-1 pl-4 font-mono text-[11px] text-[#fff6de]">
                      {(activeReport.unknowns ?? []).length > 0 ? (
                        activeReport.unknowns.map((unkLine, idx) => <li key={idx}>{unkLine}</li>)
                      ) : (
                        <li className="text-[#a6a2b8]">None recorded.</li>
                      )}
                    </ul>
                  </div>
                </div>

                {/* Evidence Refs */}
                <div className="border-2 border-black bg-[#13131d] p-3">
                  <div className="mb-2">
                    <MonoBadge tone="cream">
                      EVIDENCE REFS ({(activeReport.evidenceRefs ?? []).length})
                    </MonoBadge>
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {(activeReport.evidenceRefs ?? []).length > 0 ? (
                      activeReport.evidenceRefs.map((refStr, idx) =>
                        refStr.startsWith('http') ? (
                          <a
                            key={idx}
                            href={refStr}
                            target="_blank"
                            rel="noreferrer"
                            className="inline-flex items-center gap-1 border border-black bg-[#08080c] px-2 py-0.5 font-mono text-[10px] font-bold text-[#00f0ff] underline"
                          >
                            {refStr}
                            <ExternalLink className="h-2.5 w-2.5" />
                          </a>
                        ) : (
                          <MonoBadge key={idx} tone="neutral">
                            {refStr}
                          </MonoBadge>
                        ),
                      )
                    ) : (
                      <span className="font-mono text-[11px] text-[#a6a2b8]">
                        No evidence refs linked yet.
                      </span>
                    )}
                  </div>
                </div>
              </div>
            ) : null}
          </div>
        )}
      </SectionCard>
    </div>
  );
}
