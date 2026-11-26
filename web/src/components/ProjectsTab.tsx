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
import { MonoBadge, SectionCard, StatTile, type BadgeTone } from './UiPrimitives';

interface ProjectsTabProps {
  projects: ProjectSummary[];
  isLoading: boolean;
  onReloadProjects: () => Promise<void>;
  onOpenProjectBoard: (projectSlug: string) => void;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

const COLOR_SWATCHES = ['#ffe031', '#00b8f0', '#ff2e88', '#22c55e', '#a855f7', '#f97316'];

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
        onNotify('ok', `Updated project "${payload.name}".`);
      } else {
        await createProject(payload);
        onNotify('ok', `Created project "${payload.name}".`);
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
      onNotify('ok', `Deleted project "${project.slug}".`);
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
    <div className="space-y-5">
      {/* Top Portfolio Telemetry */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="Tracked Projects"
          value={projects.length}
          sublabel={`${activeCount} active portfolio workspace(s)`}
          icon={<FolderKanban className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="Total Tasks"
          value={totalTasksAcrossProjects}
          sublabel={`${totalTasksAcrossProjects - doneTasksAcrossProjects} open across all projects`}
          icon={<Layers className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="Tasks Completed"
          value={doneTasksAcrossProjects}
          sublabel="Synced with TaskOS v2"
          icon={<BookOpen className="h-4 w-4" />}
          tone="ok"
        />
        <StatTile
          label="Portfolio Completion"
          value={`${overallProgress}%`}
          sublabel="Weighted across all milestones"
          icon={<Bot className="h-4 w-4" />}
          tone="magenta"
        />
      </div>

      {/* Header Controls + Create/Edit Form */}
      <SectionCard
        title="Project Management Control Plane"
        subtitle="Track workspaces, milestones, docs, storage footprint, default AI agent, and jump straight into each Kanban board."
        icon={<FolderKanban className="h-4 w-4" />}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <select
              value={statusFilter}
              onChange={(ev) => setStatusFilter(ev.target.value)}
              className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            >
              <option value="">All Statuses</option>
              <option value="active">active</option>
              <option value="paused">paused</option>
              <option value="archived">archived</option>
            </select>
            <button
              type="button"
              onClick={() => void onReloadProjects()}
              className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#1c1c23] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] hover:border-[#ffe031]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${isLoading ? 'animate-spin' : ''}`} />
              REFRESH
            </button>
            <button
              type="button"
              onClick={openCreateForm}
              className="inline-flex items-center gap-1.5 rounded bg-[#ffe031] px-3.5 py-1.5 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90"
            >
              <Plus className="h-3.5 w-3.5" />
              NEW PROJECT
            </button>
          </div>
        }
      >
        {formOpen ? (
          <form
            onSubmit={handleSubmitForm}
            className="mb-5 space-y-3 rounded-lg border border-[#ffe031]/50 bg-[#0d0d10] p-4"
          >
            <div className="flex items-center justify-between border-b border-[#2a2a36] pb-2">
              <MonoBadge tone="yellow">
                {editingSlug ? `EDIT PROJECT: ${editingSlug}` : 'CREATE NEW PROJECT'}
              </MonoBadge>
              <button
                type="button"
                onClick={() => {
                  setFormOpen(false);
                  setEditingSlug(null);
                }}
                className="text-[#9c988c] hover:text-[#f3efe4]"
                aria-label="Close project form"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
              <div>
                <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
                  Slug
                </label>
                <input
                  type="text"
                  disabled={Boolean(editingSlug)}
                  value={draft.slug}
                  onChange={(ev) => setDraft((prev) => ({ ...prev, slug: ev.target.value }))}
                  placeholder="moonwitness"
                  className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none disabled:opacity-60"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
                  Display Name
                </label>
                <input
                  type="text"
                  value={draft.name}
                  onChange={(ev) => setDraft((prev) => ({ ...prev, name: ev.target.value }))}
                  placeholder="MoonWitness Core"
                  className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
                  Status
                </label>
                <select
                  value={draft.status}
                  onChange={(ev) =>
                    setDraft((prev) => ({ ...prev, status: ev.target.value as ProjectStatus }))
                  }
                  className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
                >
                  <option value="active">active</option>
                  <option value="paused">paused</option>
                  <option value="archived">archived</option>
                </select>
              </div>
            </div>

            <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
              <div className="md:col-span-2">
                <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
                  Repository Path
                </label>
                <input
                  type="text"
                  value={draft.repoPath}
                  onChange={(ev) => setDraft((prev) => ({ ...prev, repoPath: ev.target.value }))}
                  placeholder="Z:\repo\mw-xv"
                  className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
                />
              </div>
              <div>
                <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
                  Default Magpie Agent
                </label>
                <select
                  value={draft.defaultAgent}
                  onChange={(ev) =>
                    setDraft((prev) => ({ ...prev, defaultAgent: ev.target.value }))
                  }
                  className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
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
              <label className="mb-1 block font-mono text-[10px] uppercase text-[#9c988c]">
                Description
              </label>
              <textarea
                rows={2}
                value={draft.description}
                onChange={(ev) => setDraft((prev) => ({ ...prev, description: ev.target.value }))}
                placeholder="Scope, goals, and key deliverables..."
                className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
            </div>

            <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
              <div className="flex items-center gap-2">
                <span className="font-mono text-[10px] uppercase text-[#9c988c]">Accent Color:</span>
                {COLOR_SWATCHES.map((swatchHex) => (
                  <button
                    key={swatchHex}
                    type="button"
                    onClick={() => setDraft((prev) => ({ ...prev, color: swatchHex }))}
                    className={`h-5 w-5 rounded border ${
                      draft.color === swatchHex ? 'scale-110 border-white' : 'border-transparent'
                    }`}
                    style={{ backgroundColor: swatchHex }}
                    aria-label={`Select color ${swatchHex}`}
                  />
                ))}
                <input
                  type="text"
                  value={draft.color}
                  onChange={(ev) => setDraft((prev) => ({ ...prev, color: ev.target.value }))}
                  className="w-24 rounded border border-[#2a2a36] bg-[#15151a] px-2 py-1 font-mono text-[11px] text-[#f3efe4]"
                />
              </div>

              <button
                type="submit"
                disabled={saving}
                className="inline-flex items-center gap-1.5 rounded bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90 disabled:opacity-50"
              >
                <Save className="h-3.5 w-3.5" />
                {saving ? 'SAVING...' : editingSlug ? 'UPDATE PROJECT' : 'CREATE PROJECT'}
              </button>
            </div>
          </form>
        ) : null}

        {/* Project Cards Grid */}
        {visibleProjects.length === 0 ? (
          <div className="rounded-lg border border-dashed border-[#2a2a36] bg-[#0d0d10] py-12 text-center font-mono text-xs text-[#9c988c]">
            No projects found. Import a repository from Overview or click &quot;NEW PROJECT&quot;.
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            {visibleProjects.map((project) => {
              const accentColor = project.color || '#ffe031';
              const clampedProgress = Math.min(100, Math.max(0, Math.round(project.progressPct || 0)));
              const statusTone = STATUS_BADGE_TONES[project.status] ?? 'neutral';
              const milestones = project.milestones ?? [];
              const isExpanded = Boolean(expandedMilestones[project.slug]);
              const displayedMilestones = isExpanded ? milestones : milestones.slice(0, 3);

              return (
                <article
                  key={project.slug}
                  className="relative flex flex-col justify-between overflow-hidden rounded-lg border border-[#2a2a36] bg-[#0d0d10] p-4 transition-colors hover:border-[#3d3d4f]"
                >
                  <div
                    className="absolute inset-x-0 top-0 h-[3px]"
                    style={{ backgroundColor: accentColor }}
                  />

                  <div className="space-y-3">
                    {/* Card Header */}
                    <div className="flex flex-wrap items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="flex flex-wrap items-center gap-2">
                          <h3 className="text-base font-extrabold text-[#f3efe4]">
                            {project.name}
                          </h3>
                          <MonoBadge tone="neutral">{project.slug}</MonoBadge>
                          <MonoBadge tone={statusTone}>{project.status || 'active'}</MonoBadge>
                        </div>
                        {project.description ? (
                          <p className="mt-1 text-xs text-[#9c988c]">{project.description}</p>
                        ) : null}
                        {project.repoPath ? (
                          <div className="mt-1 truncate font-mono text-[11px] text-[#00b8f0]">
                            {project.repoPath}
                          </div>
                        ) : null}
                      </div>

                      {project.defaultAgent ? (
                        <MonoBadge tone="magenta">
                          <Bot className="h-3 w-3" />
                          {project.defaultAgent}
                        </MonoBadge>
                      ) : null}
                    </div>

                    {/* Progress Bar */}
                    <div className="rounded-md border border-[#2a2a36] bg-[#15151a] p-3">
                      <div className="mb-1.5 flex items-center justify-between font-mono text-xs">
                        <span className="text-[#9c988c]">DELIVERY PROGRESS</span>
                        <span className="font-extrabold" style={{ color: accentColor }}>
                          {clampedProgress}% ({project.doneTasks}/{project.totalTasks} DONE)
                        </span>
                      </div>
                      <div className="h-2 w-full overflow-hidden rounded-full bg-[#0d0d10]">
                        <div
                          className="h-full rounded-full transition-all"
                          style={{
                            width: `${clampedProgress}%`,
                            backgroundColor: accentColor,
                          }}
                        />
                      </div>

                      {/* Task Status Breakdown + Docs/Storage */}
                      <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
                        <MonoBadge tone="cyan">TODO: {project.todoTasks}</MonoBadge>
                        <MonoBadge tone="yellow">DOING: {project.inProgressTasks}</MonoBadge>
                        <MonoBadge tone={project.blockedTasks > 0 ? 'err' : 'neutral'}>
                          BLOCKED: {project.blockedTasks}
                        </MonoBadge>
                        <MonoBadge tone="ok">DONE: {project.doneTasks}</MonoBadge>
                        <span className="ml-auto flex items-center gap-2 font-mono text-[11px] text-[#9c988c]">
                          <span className="inline-flex items-center gap-1">
                            <BookOpen className="h-3 w-3" />
                            {project.docsCount} docs
                          </span>
                          <span className="inline-flex items-center gap-1">
                            <HardDrive className="h-3 w-3" />
                            {project.storageCount} ({formatBytes(project.storageBytes)})
                          </span>
                        </span>
                      </div>
                    </div>

                    {/* Milestones */}
                    {milestones.length > 0 ? (
                      <div className="space-y-1.5">
                        <div className="flex items-center justify-between">
                          <span className="font-mono text-[10px] font-bold uppercase tracking-wider text-[#9c988c]">
                            Milestones ({milestones.length})
                          </span>
                          {milestones.length > 3 ? (
                            <button
                              type="button"
                              onClick={() => toggleMilestones(project.slug)}
                              className="inline-flex items-center gap-1 font-mono text-[10px] text-[#00b8f0] hover:underline"
                            >
                              {isExpanded ? (
                                <>
                                  COLLAPSE <ChevronUp className="h-3 w-3" />
                                </>
                              ) : (
                                <>
                                  SHOW ALL ({milestones.length}) <ChevronDown className="h-3 w-3" />
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
                                className="rounded border border-[#2a2a36]/80 bg-[#15151a] px-2.5 py-1.5"
                              >
                                <div className="mb-1 flex items-center justify-between gap-2 font-mono text-[11px]">
                                  <span className="truncate font-semibold text-[#f3efe4]">
                                    {milestone.name || '(unassigned)'}
                                  </span>
                                  <span className="shrink-0 text-[#9c988c]">
                                    {milestone.doneTasks}/{milestone.totalTasks} ·{' '}
                                    <span className="text-[#ffe031]">{msPct}%</span>
                                  </span>
                                </div>
                                <div className="h-1.5 w-full overflow-hidden rounded-full bg-[#0d0d10]">
                                  <div
                                    className="h-full rounded-full bg-[#00b8f0]"
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
                  <div className="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-[#2a2a36] pt-3">
                    <span className="font-mono text-[10px] text-[#9c988c]">
                      Updated {formatShortTime(project.updatedAt)}
                    </span>
                    <div className="flex items-center gap-1.5">
                      <button
                        type="button"
                        onClick={() => openEditForm(project)}
                        className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1 font-mono text-[11px] text-[#f3efe4] hover:border-[#ffe031]"
                      >
                        <Edit3 className="h-3 w-3" />
                        EDIT
                      </button>
                      <button
                        type="button"
                        onClick={() => void handleDeleteProject(project)}
                        className="inline-flex items-center gap-1 rounded border border-[#ef4444]/40 bg-[#ef4444]/10 px-2.5 py-1 font-mono text-[11px] text-[#ef4444] hover:bg-[#ef4444]/20"
                      >
                        <Trash2 className="h-3 w-3" />
                        DEL
                      </button>
                      <button
                        type="button"
                        onClick={() => onOpenProjectBoard(project.slug)}
                        className="inline-flex items-center gap-1.5 rounded bg-[#ffe031] px-3 py-1 font-mono text-[11px] font-extrabold text-[#0d0d10] hover:opacity-90"
                      >
                        OPEN BOARD
                        <ArrowRight className="h-3 w-3" />
                      </button>
                    </div>
                  </div>
                </article>
              );
            })}
          </div>
        )}
      </SectionCard>
    </div>
  );
}
