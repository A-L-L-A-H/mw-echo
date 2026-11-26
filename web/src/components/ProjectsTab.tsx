import { useState } from 'react';
import {
  ArrowRight,
  BookOpen,
  Bot,
  ChevronDown,
  ChevronUp,
  Edit3,
  FolderKanban,
  HardDrive,
  Layers,
  Plus,
  RefreshCw,
  Save,
  Trash2,
  X,
} from 'lucide-react';
import {
  createProject,
  DEFAULT_AGENT_IDS,
  deleteProject,
  updateProject,
  type ProjectStatus,
  type ProjectSummary,
  type ProjectUpsertInput,
} from '../api';
import { formatBytes, formatShortTime } from '../utils';
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  StatTile,
  type BadgeTone,
} from './UiPrimitives';

interface ProjectsTabProps {
  projects: ProjectSummary[];
  isLoading: boolean;
  onReloadProjects: () => Promise<void>;
  onOpenProjectBoard: (projectSlug: string) => void;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

const COLOR_SWATCHES = ['#ffe031', '#00f0ff', '#ff2e88', '#a3ff12', '#a855f7', '#ff6b00'];

const STATUS_BADGE_TONES: Record<string, BadgeTone> = {
  active: 'ok',
  paused: 'yellow',
  archived: 'neutral',
};

const EMPTY_PROJECT_DRAFT: ProjectUpsertInput = {
  slug: '',
  name: '',
  description: '',
  repoPath: '',
  status: 'active',
  color: '#ffe031',
  defaultAgent: 'claude-code',
};

export function ProjectsTab({
  projects,
  isLoading,
  onReloadProjects,
  onOpenProjectBoard,
  onNotify,
}: ProjectsTabProps) {
  const [formOpen, setFormOpen] = useState(false);
  const [editingSlug, setEditingSlug] = useState<string | null>(null);
  const [draft, setDraft] = useState<ProjectUpsertInput>(EMPTY_PROJECT_DRAFT);
  const [saving, setSaving] = useState(false);
  const [expandedMilestones, setExpandedMilestones] = useState<Record<string, boolean>>({});
  const [statusFilter, setStatusFilter] = useState<string>('');

  const visibleProjects = statusFilter
    ? projects.filter((proj) => proj.status === statusFilter)
    : projects;

  const activeCount = projects.filter((proj) => proj.status === 'active').length;
  const totalTasksAcrossProjects = projects.reduce((sum, proj) => sum + proj.totalTasks, 0);
  const doneTasksAcrossProjects = projects.reduce((sum, proj) => sum + proj.doneTasks, 0);
  const overallProgress =
    totalTasksAcrossProjects > 0
      ? Math.round((doneTasksAcrossProjects / totalTasksAcrossProjects) * 100)
      : 0;

  const openCreateForm = () => {
    setEditingSlug(null);
    setDraft(EMPTY_PROJECT_DRAFT);
    setFormOpen(true);
  };

  const openEditForm = (project: ProjectSummary) => {
    setEditingSlug(project.slug);
    setDraft({
      slug: project.slug,
      name: project.name,
      description: project.description || '',
      repoPath: project.repoPath || '',
      status: project.status || 'active',
      color: project.color || '#ffe031',
      defaultAgent: project.defaultAgent || 'claude-code',
    });
    setFormOpen(true);
  };

  const handleSubmitForm = async (event: React.FormEvent) => {
    event.preventDefault();
    const cleanSlug = draft.slug.trim().toLowerCase().replace(/[^a-z0-9-_]+/g, '-');
    const cleanName = draft.name.trim();
    if (!cleanSlug || !cleanName) {
      onNotify('err', 'Project slug and display name are required.');
      return;
    }
    const payload: ProjectUpsertInput = {
      ...draft,
      slug: cleanSlug,
      name: cleanName,
      description: draft.description.trim(),
      repoPath: draft.repoPath.trim(),
      color: draft.color.trim() || '#ffe031',
      defaultAgent: draft.defaultAgent.trim() || 'default',
    };
    setSaving(true);
    try {
      if (editingSlug) {
        await updateProject(editingSlug, payload);
        onNotify('ok', `Updated arc "${payload.name}".`);
      } else {
        await createProject(payload);
        onNotify('ok', `Launched arc "${payload.name}".`);
      }
      setFormOpen(false);
      setEditingSlug(null);
      setDraft(EMPTY_PROJECT_DRAFT);
      await onReloadProjects();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to save project';
      onNotify('err', message);
    } finally {
      setSaving(false);
    }
  };

  const handleDeleteProject = async (project: ProjectSummary) => {
    try {
      await deleteProject(project.slug);
      onNotify('ok', `Deleted arc "${project.slug}".`);
      await onReloadProjects();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to delete project';
      onNotify('err', message);
    }
  };

  const toggleMilestones = (slug: string) => {
    setExpandedMilestones((prev) => ({
      ...prev,
      [slug]: !prev[slug],
    }));
  };

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 02"
        sticker="STORY ARCS"
        title="CAMPAIGN ARCS & MILESTONES"
        subtitle="ANIME-POSTER PROJECT DECK — TRACK XP PROGRESS, MILESTONE CHAPTERS, AGENTS & JUMP INTO COMBAT BOARDS"
        tone="cyan"
        actions={
          <>
            <select
              value={statusFilter}
              onChange={(ev) => setStatusFilter(ev.target.value)}
              className="riot-input px-3 py-2 font-mono text-xs font-bold uppercase"
            >
              <option value="">All Arcs</option>
              <option value="active">active</option>
              <option value="paused">paused</option>
              <option value="archived">archived</option>
            </select>
            <button
              type="button"
              onClick={() => void onReloadProjects()}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#1b1b2b] px-3 py-2 font-mono text-xs font-extrabold text-[#fff6de]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${isLoading ? 'animate-spin' : ''}`} />
              SYNC
            </button>
            <button
              type="button"
              onClick={openCreateForm}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-display text-xs text-[#08080c] uppercase"
            >
              <Plus className="h-4 w-4" />
              NEW ARC
            </button>
          </>
        }
      />

      {/* Portfolio Telemetry Stamps */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="STORY ARCS"
          value={projects.length}
          sublabel={`${activeCount} active in rotation`}
          icon={<FolderKanban className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="TOTAL BOUNTIES"
          value={totalTasksAcrossProjects}
          sublabel={`${totalTasksAcrossProjects - doneTasksAcrossProjects} still in play`}
          icon={<Layers className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="CLEARED"
          value={doneTasksAcrossProjects}
          sublabel="Synced with TaskOS v2"
          icon={<BookOpen className="h-4 w-4" />}
          tone="ok"
        />
        <StatTile
          label="GLOBAL XP"
          value={`${overallProgress}%`}
          sublabel="Across all milestone chapters"
          icon={<Bot className="h-4 w-4" />}
          tone="magenta"
        />
      </div>

      {/* Create / Edit Arc Form */}
      {formOpen ? (
        <SectionCard
          title={editingSlug ? `REWRITE ARC // ${editingSlug}` : 'FORGE NEW STORY ARC'}
          icon={<FolderKanban className="h-4 w-4" />}
          sticker="EDITOR"
          stickerTone="magenta"
          accent="yellow"
          actions={
            <button
              type="button"
              onClick={() => {
                setFormOpen(false);
                setEditingSlug(null);
              }}
              className="riot-btn bg-[#1b1b2b] p-1.5 text-[#fff6de]"
              aria-label="Close project form"
            >
              <X className="h-4 w-4" />
            </button>
          }
        >
          <form onSubmit={handleSubmitForm} className="space-y-3">
            <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                  Slug
                </label>
                <input
                  type="text"
                  disabled={Boolean(editingSlug)}
                  value={draft.slug}
                  onChange={(ev) => setDraft((prev) => ({ ...prev, slug: ev.target.value }))}
                  placeholder="moonwitness"
                  className="riot-input w-full px-3 py-2 font-mono text-xs disabled:opacity-60"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                  Arc Display Name
                </label>
                <input
                  type="text"
                  value={draft.name}
                  onChange={(ev) => setDraft((prev) => ({ ...prev, name: ev.target.value }))}
                  placeholder="MoonWitness Core"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ff2e88]">
                  Status
                </label>
                <select
                  value={draft.status}
                  onChange={(ev) =>
                    setDraft((prev) => ({ ...prev, status: ev.target.value as ProjectStatus }))
                  }
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                >
                  <option value="active">active</option>
                  <option value="paused">paused</option>
                  <option value="archived">archived</option>
                </select>
              </div>
            </div>

            <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
              <div className="md:col-span-2">
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                  Repository Path
                </label>
                <input
                  type="text"
                  value={draft.repoPath}
                  onChange={(ev) => setDraft((prev) => ({ ...prev, repoPath: ev.target.value }))}
                  placeholder="Z:\repo\mw-xv"
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a3ff12]">
                  Default Magpie Pilot
                </label>
                <select
                  value={draft.defaultAgent}
                  onChange={(ev) =>
                    setDraft((prev) => ({ ...prev, defaultAgent: ev.target.value }))
                  }
                  className="riot-input w-full px-3 py-2 font-mono text-xs"
                >
                  {DEFAULT_AGENT_IDS.map((agentId) => (
                    <option key={agentId} value={agentId}>
                      {agentId}
                    </option>
                  ))}
                </select>
              </div>
            </div>

            <div>
              <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                Arc Synopsis
              </label>
              <textarea
                rows={2}
                value={draft.description}
                onChange={(ev) => setDraft((prev) => ({ ...prev, description: ev.target.value }))}
                placeholder="Mission scope, boss fights, and deliverables..."
                className="riot-input w-full px-3 py-2 font-mono text-xs"
              />
            </div>

            <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                  Poster Tint:
                </span>
                {COLOR_SWATCHES.map((swatchHex) => (
                  <button
                    key={swatchHex}
                    type="button"
                    onClick={() => setDraft((prev) => ({ ...prev, color: swatchHex }))}
                    className={`h-6 w-6 border-2 border-black shadow-[2px_2px_0_#000] transition-transform ${
                      draft.color === swatchHex ? 'scale-115 ring-2 ring-white' : ''
                    }`}
                    style={{ backgroundColor: swatchHex }}
                    aria-label={`Select color ${swatchHex}`}
                  />
                ))}
                <input
                  type="text"
                  value={draft.color}
                  onChange={(ev) => setDraft((prev) => ({ ...prev, color: ev.target.value }))}
                  className="riot-input w-24 px-2 py-1 font-mono text-[11px]"
                />
              </div>

              <button
                type="submit"
                disabled={saving}
                className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
              >
                <Save className="h-3.5 w-3.5" />
                {saving ? 'SAVING...' : editingSlug ? 'UPDATE ARC' : 'CREATE ARC'}
              </button>
            </div>
          </form>
        </SectionCard>
      ) : null}

      {/* Anime/Comic Arc Poster Grid */}
      {visibleProjects.length === 0 ? (
        <div className="border-[2.5px] border-dashed border-[#3a3a58] bg-[#13131d] py-14 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
          NO ARCS FOUND. IMPORT A REPO ON HQ STAGE OR CLICK &quot;NEW ARC&quot;.
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
          {visibleProjects.map((project, arcIdx) => {
            const accentColor = project.color || '#ffe031';
            const clampedProgress = Math.min(100, Math.max(0, Math.round(project.progressPct || 0)));
            const statusTone = STATUS_BADGE_TONES[project.status] ?? 'neutral';
            const milestones = project.milestones ?? [];
            const isExpanded = Boolean(expandedMilestones[project.slug]);
            const displayedMilestones = isExpanded ? milestones : milestones.slice(0, 3);

            return (
              <article
                key={project.slug}
                className="relative flex flex-col justify-between border-[2.5px] border-black bg-[#13131d] shadow-[7px_7px_0_#000] transition-transform duration-100 hover:-translate-x-0.5 hover:-translate-y-0.5"
              >
                {/* Explosive Top Poster Strip */}
                <div
                  className="flex flex-wrap items-center justify-between gap-2 border-b-[2.5px] border-black px-4 py-2.5 text-[#08080c]"
                  style={{ backgroundColor: accentColor }}
                >
                  <div className="flex items-center gap-2">
                    <span className="border-2 border-black bg-[#08080c] px-1.5 py-0.5 font-comic text-sm text-[#ffe031]">
                      ARC #{String(arcIdx + 1).padStart(2, '0')}
                    </span>
                    <h3 className="font-display text-base tracking-tight uppercase">
                      {project.name}
                    </h3>
                  </div>
                  <div className="flex items-center gap-1.5">
                    <ComicSticker tone={statusTone} tilt="rotate-2">
                      {project.status || 'active'}
                    </ComicSticker>
                    {project.defaultAgent ? (
                      <MonoBadge tone="cream">
                        <Bot className="h-3 w-3" />
                        {project.defaultAgent}
                      </MonoBadge>
                    ) : null}
                  </div>
                </div>

                <div className="space-y-3.5 p-4">
                  {/* Slug, synopsis & repo path */}
                  <div className="flex flex-wrap items-center gap-2">
                    <MonoBadge tone="neutral">{project.slug}</MonoBadge>
                    {project.repoPath ? (
                      <span className="truncate font-mono text-[11px] font-bold text-[#00f0ff]">
                        {project.repoPath}
                      </span>
                    ) : null}
                  </div>
                  {project.description ? (
                    <p className="text-xs font-medium text-[#fff6de]/85">{project.description}</p>
                  ) : null}

                  {/* Diagonal Striped Anime XP Progress Gauge */}
                  <div className="border-2 border-black bg-[#08080c] p-3 shadow-[4px_4px_0_#000]">
                    <div className="mb-1.5 flex items-center justify-between font-mono text-xs font-extrabold">
                      <span className="text-[#a6a2b8]">ARC XP PROGRESS</span>
                      <span style={{ color: accentColor }}>
                        {clampedProgress}% ({project.doneTasks}/{project.totalTasks} CLEARED)
                      </span>
                    </div>
                    <div className="h-4 w-full overflow-hidden border-2 border-black bg-[#1b1b2b]">
                      <div
                        className="riot-xp-stripe h-full border-r-2 border-black transition-all"
                        style={{
                          width: `${clampedProgress}%`,
                          backgroundColor: accentColor,
                        }}
                      />
                    </div>

                    <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
                      <MonoBadge tone="cyan">QUEUED {project.todoTasks}</MonoBadge>
                      <MonoBadge tone="yellow">COMBAT {project.inProgressTasks}</MonoBadge>
                      <MonoBadge tone={project.blockedTasks > 0 ? 'err' : 'neutral'}>
                        JAMMED {project.blockedTasks}
                      </MonoBadge>
                      <MonoBadge tone="ok">CLEARED {project.doneTasks}</MonoBadge>
                      <span className="ml-auto flex items-center gap-2.5 font-mono text-[11px] font-bold text-[#a6a2b8]">
                        <span className="inline-flex items-center gap-1">
                          <BookOpen className="h-3 w-3 text-[#ffe031]" />
                          {project.docsCount}
                        </span>
                        <span className="inline-flex items-center gap-1">
                          <HardDrive className="h-3 w-3 text-[#00f0ff]" />
                          {project.storageCount} ({formatBytes(project.storageBytes)})
                        </span>
                      </span>
                    </div>
                  </div>

                  {/* Milestone Chapters */}
                  {milestones.length > 0 ? (
                    <div className="space-y-2">
                      <div className="flex items-center justify-between">
                        <span className="font-mono text-[10px] font-extrabold uppercase tracking-wider text-[#ffe031]">
                          MILESTONE CHAPTERS ({milestones.length})
                        </span>
                        {milestones.length > 3 ? (
                          <button
                            type="button"
                            onClick={() => toggleMilestones(project.slug)}
                            className="inline-flex items-center gap-1 font-mono text-[10px] font-extrabold text-[#00f0ff] uppercase hover:underline"
                          >
                            {isExpanded ? (
                              <>
                                COLLAPSE <ChevronUp className="h-3 w-3" />
                              </>
                            ) : (
                              <>
                                ALL {milestones.length} CHAPTERS <ChevronDown className="h-3 w-3" />
                              </>
                            )}
                          </button>
                        ) : null}
                      </div>

                      <div className="space-y-1.5">
                        {displayedMilestones.map((milestone) => {
                          const msPct = Math.min(
                            100,
                            Math.max(0, Math.round(milestone.progressPct || 0)),
                          );
                          return (
                            <div
                              key={`${project.slug}:${milestone.name}`}
                              className="border-2 border-black bg-[#08080c] px-2.5 py-1.5"
                            >
                              <div className="mb-1 flex items-center justify-between gap-2 font-mono text-[11px] font-bold">
                                <span className="truncate text-[#fff6de]">
                                  {milestone.name || 'General'}
                                </span>
                                <span className="shrink-0 text-[#a6a2b8]">
                                  {milestone.doneTasks}/{milestone.totalTasks} ·{' '}
                                  <span className="text-[#ffe031]">{msPct}%</span>
                                </span>
                              </div>
                              <div className="h-2 w-full overflow-hidden border border-black bg-[#1b1b2b]">
                                <div
                                  className="riot-xp-stripe h-full bg-[#00f0ff]"
                                  style={{ width: `${msPct}%` }}
                                />
                              </div>
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  ) : null}
                </div>

                {/* Card Footer */}
                <div className="flex flex-wrap items-center justify-between gap-2 border-t-[2.5px] border-black bg-[#1b1b2b] px-4 py-2.5">
                  <span className="font-mono text-[10px] font-bold text-[#a6a2b8]">
                    UPDATED {formatShortTime(project.updatedAt)}
                  </span>
                  <div className="flex items-center gap-2">
                    <button
                      type="button"
                      onClick={() => openEditForm(project)}
                      className="riot-btn inline-flex items-center gap-1 bg-[#13131d] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#fff6de]"
                    >
                      <Edit3 className="h-3 w-3" />
                      EDIT
                    </button>
                    <button
                      type="button"
                      onClick={() => void handleDeleteProject(project)}
                      className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2.5 py-1 font-mono text-[11px] font-extrabold text-white"
                    >
                      <Trash2 className="h-3 w-3" />
                      DEL
                    </button>
                    <button
                      type="button"
                      onClick={() => onOpenProjectBoard(project.slug)}
                      className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-3.5 py-1 font-display text-[11px] text-[#08080c] uppercase"
                    >
                      OPEN BOARD
                      <ArrowRight className="h-3.5 w-3.5" />
                    </button>
                  </div>
                </div>
              </article>
            );
          })}
        </div>
      )}
    </div>
  );
}
