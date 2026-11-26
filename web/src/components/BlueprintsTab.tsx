import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Boxes,
  Check,
  Copy,
  Download,
  ExternalLink,
  FileCode2,
  GitCommitHorizontal,
  Package,
  Radar,
  RefreshCw,
  Search,
  Server,
  Sparkles,
  X,
} from 'lucide-react';
import {
  fetchBlueprintById,
  fetchBlueprints,
  sniffBlueprintsCatalogs,
  type BlueprintRecord,
  type BlueprintSourceFilter,
  type BlueprintSourceStatus,
} from '../api';
import { copyTextToClipboard, formatShortTime } from '../utils';
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  StatTile,
  type BadgeTone,
} from './UiPrimitives';

interface BlueprintsTabProps {
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

const SOURCE_FILTERS: Array<{ value: BlueprintSourceFilter; label: string }> = [
  { value: 'all', label: 'ALL SOURCES' },
  { value: 'curated', label: 'CURATED' },
  { value: 'dokploy', label: 'DOKPLOY' },
  { value: 'coolify', label: 'COOLIFY' },
  { value: 'bigbear', label: 'BIGBEAR' },
];

function resolveSourceTone(source: string): BadgeTone {
  switch ((source || '').toLowerCase()) {
    case 'curated':
      return 'yellow';
    case 'dokploy':
      return 'cyan';
    case 'coolify':
      return 'magenta';
    case 'bigbear':
      return 'ok';
    default:
      return 'neutral';
  }
}

export function BlueprintsTab({ onNotify }: BlueprintsTabProps) {
  const [blueprints, setBlueprints] = useState<BlueprintRecord[]>([]);
  const [sources, setSources] = useState<BlueprintSourceStatus[]>([]);
  const [totalCount, setTotalCount] = useState(0);

  const [searchQuery, setSearchQuery] = useState('');
  const [sourceFilter, setSourceFilter] = useState<BlueprintSourceFilter>('all');
  const [categoryFilter, setCategoryFilter] = useState('');
  const [loadingBlueprints, setLoadingBlueprints] = useState(false);
  const [sniffingUpstream, setSniffingUpstream] = useState(false);

  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [previewBlueprint, setPreviewBlueprint] = useState<BlueprintRecord | null>(null);
  const [loadingPreviewId, setLoadingPreviewId] = useState<string | null>(null);

  const loadBlueprints = useCallback(
    async (queryText: string, sourceVal: string, categoryVal: string) => {
      setLoadingBlueprints(true);
      try {
        const reply = await fetchBlueprints({
          query: queryText,
          source: sourceVal,
          category: categoryVal,
          limit: 100,
        });
        setBlueprints(reply.blueprints ?? []);
        setSources(reply.sources ?? []);
        setTotalCount(reply.count ?? (reply.blueprints ?? []).length);
      } catch (caughtError) {
        const message =
          caughtError instanceof Error ? caughtError.message : 'Failed to load PaaS blueprints';
        onNotify('err', message);
      } finally {
        setLoadingBlueprints(false);
      }
    },
    [onNotify],
  );

  useEffect(() => {
    const debounceHandle = window.setTimeout(() => {
      void loadBlueprints(searchQuery, sourceFilter, categoryFilter);
    }, 180);
    return () => window.clearTimeout(debounceHandle);
  }, [categoryFilter, loadBlueprints, searchQuery, sourceFilter]);

  useEffect(() => {
    if (!previewBlueprint) return;
    const handleEsc = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') {
        setPreviewBlueprint(null);
      }
    };
    window.addEventListener('keydown', handleEsc);
    return () => window.removeEventListener('keydown', handleEsc);
  }, [previewBlueprint]);

  const handleSniffUpstream = async () => {
    setSniffingUpstream(true);
    try {
      const sniffSummary = await sniffBlueprintsCatalogs();
      const syncedSources = sniffSummary.results
        .filter((sourceResult) => !sourceResult.error)
        .map((sourceResult) => `${sourceResult.source}:${sourceResult.indexed ?? 0}`)
        .join(', ');
      onNotify(
        'ok',
        `Sniffed upstream PaaS catalogs (${sniffSummary.totalBlueprints} total blueprints${syncedSources ? ` · ${syncedSources}` : ''}).`,
      );
      await loadBlueprints(searchQuery, sourceFilter, categoryFilter);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Upstream blueprint sniff failed';
      onNotify('err', message);
    } finally {
      setSniffingUpstream(false);
    }
  };

  const resolveFullBlueprint = async (blueprint: BlueprintRecord): Promise<BlueprintRecord> => {
    if (blueprint.composeYaml && blueprint.composeYaml.trim().length > 0) {
      return blueprint;
    }
    return fetchBlueprintById(blueprint.id);
  };

  const handleOpenPreview = async (blueprint: BlueprintRecord) => {
    setLoadingPreviewId(blueprint.id);
    try {
      const fullBlueprint = await resolveFullBlueprint(blueprint);
      setPreviewBlueprint(fullBlueprint);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to load blueprint YAML';
      onNotify('err', message);
    } finally {
      setLoadingPreviewId(null);
    }
  };

  const handleCopyCompose = async (blueprint: BlueprintRecord) => {
    try {
      const fullBlueprint = await resolveFullBlueprint(blueprint);
      const yamlText = fullBlueprint.composeYaml || '';
      if (!yamlText.trim()) {
        onNotify('err', `No docker-compose.yml payload available for ${blueprint.name}.`);
        return;
      }
      const succeeded = await copyTextToClipboard(yamlText);
      if (succeeded) {
        setCopiedId(blueprint.id);
        onNotify('ok', `Copied docker-compose.yml for "${blueprint.name}".`);
        window.setTimeout(() => {
          setCopiedId((prev) => (prev === blueprint.id ? null : prev));
        }, 1800);
      }
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to copy compose YAML';
      onNotify('err', message);
    }
  };

  const uniqueCategories = useMemo(() => {
    const catSet = new Set(
      blueprints.map((bp) => bp.category).filter((cat) => Boolean(cat && cat.trim())),
    );
    return Array.from(catSet).sort((left, right) => left.localeCompare(right));
  }, [blueprints]);

  const totalCatalogItems = useMemo(() => {
    const sumFromSources = sources.reduce((acc, src) => acc + (src.itemsCount || 0), 0);
    return Math.max(totalCount, sumFromSources, blueprints.length);
  }, [blueprints.length, sources, totalCount]);

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 14"
        sticker="PAAS ARMORY"
        title="PACKS // SELF-HOSTED DOCKER BLUEPRINTS"
        subtitle="CURATED PRODUCTION STACKS + LIVE UPSTREAM SNIFFER (DOKPLOY · COOLIFY · BIGBEAR) WITH FTS5 FULL-TEXT SEARCH"
        tone="cyan"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={sniffingUpstream}
              onClick={() => void handleSniffUpstream()}
              className="riot-btn inline-flex items-center gap-2 bg-[#ffe031] px-4 py-2 font-display text-xs text-[#08080c] uppercase hover:bg-[#a3ff12] disabled:opacity-50"
            >
              <Radar className={`h-4 w-4 ${sniffingUpstream ? 'animate-spin' : ''}`} />
              {sniffingUpstream ? 'SNIFFING UPSTREAMS...' : 'SNIFF UPSTREAM CATALOGS'}
            </button>
            <button
              type="button"
              onClick={() => void loadBlueprints(searchQuery, sourceFilter, categoryFilter)}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#00f0ff] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingBlueprints ? 'animate-spin' : ''}`} />
              REFRESH
            </button>
          </div>
        }
      />

      {/* Armory Telemetry Tiles */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="INDEXED PACKS"
          value={totalCatalogItems}
          sublabel={`${blueprints.length} matching current query`}
          icon={<Package className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="CATALOG SOURCES"
          value={sources.length || 4}
          sublabel="Curated · Dokploy · Coolify · BigBear"
          icon={<Server className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="CATEGORIES"
          value={uniqueCategories.length}
          sublabel="Databases · S3 · Automation · AI"
          icon={<Boxes className="h-4 w-4" />}
          tone="magenta"
        />
        <StatTile
          label="FTS5 SEARCH"
          value="READY"
          sublabel="Zero-repo-bloat dynamic indexing"
          icon={<Sparkles className="h-4 w-4" />}
          tone="ok"
        />
      </div>

      {/* Main Blueprints Armory Card */}
      <SectionCard
        title="SELF-HOSTED PAAS BLUEPRINT ARMORY"
        subtitle="FTS5 search across curated and sniffed Docker Compose blueprints. Preview or copy YAML in one click."
        icon={<Package className="h-4 w-4" />}
        sticker="COMPOSE READY"
        stickerTone="cyan"
        accent="cyan"
      >
        {/* Upstream Source SHA Strip */}
        {sources.length > 0 ? (
          <div className="mb-4 flex flex-wrap items-center gap-2 border-2 border-black bg-[#08080c] px-3.5 py-2.5 shadow-[4px_4px_0_#000]">
            {sources.map((srcStatus) => (
              <MonoBadge key={srcStatus.source} tone={resolveSourceTone(srcStatus.source)}>
                <GitCommitHorizontal className="h-3 w-3" />
                {srcStatus.source}: {srcStatus.itemsCount}
                {srcStatus.lastSha ? ` (${srcStatus.lastSha.slice(0, 7)})` : ''}
              </MonoBadge>
            ))}
            <span className="ml-auto font-mono text-[10px] font-bold text-[#a6a2b8]">
              LAST SNIFF:{' '}
              {formatShortTime(sources.find((src) => Boolean(src.lastSniffAt))?.lastSniffAt)}
            </span>
          </div>
        ) : null}

        {/* FTS5 Search + Source Filter Pills + Category Select */}
        <div className="mb-4 space-y-3">
          <div className="flex flex-wrap items-center gap-2.5">
            <div className="relative min-w-[240px] flex-1">
              <Search className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-[#00f0ff]" />
              <input
                type="search"
                value={searchQuery}
                onChange={(ev) => setSearchQuery(ev.target.value)}
                onKeyDown={(ev) => {
                  if (ev.key === 'Enter') {
                    void loadBlueprints(searchQuery, sourceFilter, categoryFilter);
                  }
                }}
                placeholder="FTS5 search: postgres, garage, n8n, ollama, cloudflared, mw-echo..."
                className="riot-input w-full py-2 pr-3 pl-9 font-mono text-xs"
              />
            </div>

            <select
              value={categoryFilter}
              onChange={(ev) => setCategoryFilter(ev.target.value)}
              className="riot-input px-3 py-2 font-mono text-xs font-bold text-[#ffe031]"
            >
              <option value="">All Categories</option>
              {uniqueCategories.map((catName) => (
                <option key={catName} value={catName}>
                  {catName}
                </option>
              ))}
            </select>
          </div>

          <div className="flex flex-wrap items-center gap-1.5">
            {SOURCE_FILTERS.map((srcOption) => {
              const isSelected = sourceFilter === srcOption.value;
              return (
                <button
                  key={srcOption.value}
                  type="button"
                  onClick={() => setSourceFilter(srcOption.value)}
                  className={`border-2 border-black px-3 py-1 font-mono text-[11px] font-extrabold uppercase transition-all ${
                    isSelected
                      ? 'bg-[#ffe031] text-[#08080c] shadow-[3px_3px_0_#ff2e88]'
                      : 'bg-[#08080c] text-[#a6a2b8] hover:border-[#00f0ff] hover:text-[#fff6de]'
                  }`}
                >
                  {srcOption.label}
                </button>
              );
            })}
          </div>
        </div>

        {/* Blueprint Cards Grid */}
        {blueprints.length === 0 ? (
          <div className="border-2 border-black bg-[#08080c] px-4 py-8 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
            {loadingBlueprints
              ? 'QUERYING FTS5 BLUEPRINT ARMORY...'
              : 'NO BLUEPRINTS FOUND FOR THIS QUERY. TRY ANOTHER SEARCH OR CLICK "SNIFF UPSTREAM CATALOGS".'}
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            {blueprints.map((blueprint) => {
              const isCopied = copiedId === blueprint.id;
              const isLoadingYaml = loadingPreviewId === blueprint.id;
              return (
                <article
                  key={blueprint.id}
                  className="flex flex-col justify-between border-[2.5px] border-black bg-[#08080c] p-4 shadow-[5px_5px_0_#000] transition-transform duration-100 hover:-translate-x-0.5 hover:-translate-y-0.5"
                >
                  <div>
                    <div className="mb-2 flex flex-wrap items-center justify-between gap-1.5">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <MonoBadge tone={resolveSourceTone(blueprint.source)}>
                          {blueprint.source}
                        </MonoBadge>
                        <MonoBadge tone="neutral">{blueprint.category}</MonoBadge>
                      </div>
                      {blueprint.version ? (
                        <MonoBadge tone="cream">v{blueprint.version.replace(/^v/i, '')}</MonoBadge>
                      ) : null}
                    </div>

                    <h3 className="font-display text-base text-[#fff6de] uppercase">
                      {blueprint.name}
                    </h3>
                    <div className="mt-0.5 font-mono text-[11px] font-bold text-[#00f0ff]">
                      {blueprint.slug}
                    </div>

                    <p className="mt-2 line-clamp-3 font-mono text-xs leading-relaxed text-[#a6a2b8]">
                      {blueprint.description || 'Self-hosted Docker Compose blueprint.'}
                    </p>

                    {blueprint.tags.length > 0 ? (
                      <div className="mt-3 flex flex-wrap gap-1">
                        {blueprint.tags.slice(0, 6).map((tagLabel) => (
                          <span
                            key={`${blueprint.id}-${tagLabel}`}
                            className="border border-[#353552] bg-[#13131d] px-1.5 py-0.5 font-mono text-[10px] font-bold text-[#ffe031]"
                          >
                            #{tagLabel}
                          </span>
                        ))}
                      </div>
                    ) : null}
                  </div>

                  <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-[#252538] pt-3">
                    <button
                      type="button"
                      disabled={isLoadingYaml}
                      onClick={() => void handleOpenPreview(blueprint)}
                      className="riot-btn inline-flex flex-1 items-center justify-center gap-1.5 bg-[#00f0ff] px-3 py-1.5 font-mono text-[11px] font-extrabold text-[#08080c] disabled:opacity-50"
                    >
                      <FileCode2 className="h-3.5 w-3.5" />
                      {isLoadingYaml ? 'LOADING...' : 'PREVIEW YAML'}
                    </button>

                    <button
                      type="button"
                      onClick={() => void handleCopyCompose(blueprint)}
                      className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-3 py-1.5 font-mono text-[11px] font-extrabold text-[#08080c]"
                    >
                      {isCopied ? (
                        <Check className="h-3.5 w-3.5" />
                      ) : (
                        <Copy className="h-3.5 w-3.5" />
                      )}
                      {isCopied ? 'COPIED!' : 'COPY'}
                    </button>

                    <a
                      href={`/api/blueprints/${encodeURIComponent(blueprint.id)}/compose`}
                      target="_blank"
                      rel="noreferrer"
                      className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-2.5 py-1.5 font-mono text-[11px] font-extrabold text-[#fff6de]"
                      title="Download docker-compose.yml"
                    >
                      <Download className="h-3.5 w-3.5" />
                    </a>

                    {blueprint.sourceUrl ? (
                      <a
                        href={blueprint.sourceUrl}
                        target="_blank"
                        rel="noreferrer"
                        className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-2.5 py-1.5 font-mono text-[11px] font-extrabold text-[#ff2e88]"
                        title="Open upstream source"
                      >
                        <ExternalLink className="h-3.5 w-3.5" />
                      </a>
                    ) : null}
                  </div>
                </article>
              );
            })}
          </div>
        )}
      </SectionCard>

      {/* docker-compose.yml Preview Modal */}
      {previewBlueprint ? (
        <div
          role="dialog"
          aria-modal="true"
          aria-label={`Compose YAML preview for ${previewBlueprint.name}`}
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4 backdrop-blur-xs"
          onClick={() => setPreviewBlueprint(null)}
        >
          <div
            className="flex max-h-[90vh] w-full max-w-4xl flex-col border-[3px] border-black bg-[#13131d] shadow-[10px_10px_0_#ffe031]"
            onClick={(ev) => ev.stopPropagation()}
          >
            <div className="flex flex-wrap items-center justify-between gap-3 border-b-[2.5px] border-black bg-[#1b1b2b] px-4 py-3">
              <div className="flex flex-wrap items-center gap-2">
                <ComicSticker tone={resolveSourceTone(previewBlueprint.source)} tilt="-rotate-2">
                  {previewBlueprint.source}
                </ComicSticker>
                <h3 className="font-display text-base text-[#fff6de] uppercase">
                  {previewBlueprint.name}
                </h3>
                <MonoBadge tone="cyan">docker-compose.yml</MonoBadge>
                {previewBlueprint.version ? (
                  <MonoBadge tone="cream">v{previewBlueprint.version.replace(/^v/i, '')}</MonoBadge>
                ) : null}
              </div>

              <div className="flex flex-wrap items-center gap-2">
                <button
                  type="button"
                  onClick={() => void handleCopyCompose(previewBlueprint)}
                  className="riot-btn inline-flex items-center gap-1.5 bg-[#a3ff12] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
                >
                  {copiedId === previewBlueprint.id ? (
                    <Check className="h-3.5 w-3.5" />
                  ) : (
                    <Copy className="h-3.5 w-3.5" />
                  )}
                  {copiedId === previewBlueprint.id ? 'COPIED!' : 'COPY COMPOSE YAML'}
                </button>
                <a
                  href={`/api/blueprints/${encodeURIComponent(previewBlueprint.id)}/compose`}
                  target="_blank"
                  rel="noreferrer"
                  className="riot-btn inline-flex items-center gap-1.5 bg-[#00f0ff] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
                >
                  <Download className="h-3.5 w-3.5" />
                  RAW .YML
                </a>
                <button
                  type="button"
                  onClick={() => setPreviewBlueprint(null)}
                  className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2.5 py-1.5 font-mono text-xs font-extrabold text-white"
                  aria-label="Close YAML preview"
                >
                  <X className="h-4 w-4" />
                </button>
              </div>
            </div>

            <div className="flex-1 overflow-auto p-4">
              {previewBlueprint.description ? (
                <p className="mb-3 font-mono text-xs text-[#a6a2b8]">
                  {previewBlueprint.description}
                </p>
              ) : null}
              <pre className="max-h-[58vh] overflow-auto border-2 border-black bg-[#08080c] p-4 font-mono text-xs leading-relaxed text-[#a3ff12]">
                <code>
                  {previewBlueprint.composeYaml || '# No compose YAML returned for this blueprint.'}
                </code>
              </pre>
            </div>

            <div className="flex flex-wrap items-center justify-between gap-2 border-t-2 border-black bg-[#08080c] px-4 py-2.5 font-mono text-[11px] text-[#a6a2b8]">
              <span>SLUG: {previewBlueprint.slug}</span>
              {previewBlueprint.sourceUrl ? (
                <a
                  href={previewBlueprint.sourceUrl}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 font-bold text-[#00f0ff] hover:underline"
                >
                  UPSTREAM SOURCE
                  <ExternalLink className="h-3 w-3" />
                </a>
              ) : null}
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}
