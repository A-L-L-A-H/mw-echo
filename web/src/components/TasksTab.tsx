import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  CheckSquare,
  Download,
  ExternalLink,
  Kanban,
  Plus,
  RefreshCw,
  Search,
  Table2,
  Trash2,
  X,
} from 'lucide-react';
import {
  createTask,
  deleteTask,
  fetchTasks,
  fetchTasksExportJson,
  patchTask,
  type ProjectSummary,
  type TaskRecord,
} from '../api';
import {
  ComicSticker,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  type BadgeTone,
} from './UiPrimitives';

interface TasksTabProps {
  projects: ProjectSummary[];
  selectedProject: string;
  onSelectProject: (projectSlug: string) => void;
  onTasksChanged: () => Promise<void>;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

type TaskViewMode = 'board' | 'table';
type KanbanColumnId = 'todo' | 'doing' | 'blocked' | 'done';

interface KanbanColumnDef {
  id: KanbanColumnId;
  title: string;
  targetStatus: string;
  headerBg: string;
  headerText: string;
  shadowColor: string;
  badgeTone: BadgeTone;
}

const KANBAN_COLUMNS: KanbanColumnDef[] = [
  {
    id: 'todo',
    title: 'QUEUED',
    targetStatus: 'todo',
    headerBg: '#00f0ff',
    headerText: '#08080c',
    shadowColor: '#00f0ff',
    badgeTone: 'cyan',
  },
  {
    id: 'doing',
    title: 'IN COMBAT',
    targetStatus: 'doing',
    headerBg: '#ffe031',
    headerText: '#08080c',
    shadowColor: '#ffe031',
    badgeTone: 'yellow',
  },
  {
    id: 'blocked',
    title: 'JAMMED',
    targetStatus: 'blocked',
    headerBg: '#ff334b',
    headerText: '#ffffff',
    shadowColor: '#ff334b',
    badgeTone: 'err',
  },
  {
    id: 'done',
    title: 'CLEARED',
    targetStatus: 'done',
    headerBg: '#a3ff12',
    headerText: '#08080c',
    shadowColor: '#a3ff12',
    badgeTone: 'ok',
  },
];

const PRIORITY_TONES: Record<string, BadgeTone> = {
  p0: 'err',
  p1: 'magenta',
  p2: 'yellow',
  p3: 'neutral',
};

function resolveKanbanColumn(rawStatus: string): KanbanColumnId {
  const normalized = (rawStatus || 'todo').trim().toLowerCase();
  if (
    normalized === 'doing' ||
    normalized === 'in_progress' ||
    normalized === 'in-progress' ||
    normalized === 'active' ||
    normalized === 'review'
  ) {
    return 'doing';
  }
  if (normalized === 'blocked' || normalized === 'on_hold') {
    return 'blocked';
  }
  if (normalized === 'done' || normalized === 'completed' || normalized === 'closed') {
    return 'done';
  }
  return 'todo';
}

export function TasksTab({
  projects,
  selectedProject,
  onSelectProject,
  onTasksChanged,
  onNotify,
}: TasksTabProps) {
  const [viewMode, setViewMode] = useState<TaskViewMode>('board');
  const [tasksList, setTasksList] = useState<TaskRecord[]>([]);
  const [loadingTasks, setLoadingTasks] = useState(false);

  // Filters
  const [searchQuery, setSearchQuery] = useState('');
  const [priorityFilter, setPriorityFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [milestoneFilter, setMilestoneFilter] = useState('');

  // Create task form
  const [createOpen, setCreateOpen] = useState(false);
  const [newCode, setNewCode] = useState('');
  const [newProject, setNewProject] = useState(selectedProject || 'moonwitness');
  const [newSeries, setNewSeries] = useState('');
  const [newMilestone, setNewMilestone] = useState('');
  const [newTitle, setNewTitle] = useState('');
  const [newSummary, setNewSummary] = useState('');
  const [newPriority, setNewPriority] = useState('p2');
  const [newStatus, setNewStatus] = useState('todo');
  const [newTagsInput, setNewTagsInput] = useState('');
  const [creatingTask, setCreatingTask] = useState(false);
  const [exportingJson, setExportingJson] = useState(false);
  const [patchingCode, setPatchingCode] = useState<string | null>(null);

  useEffect(() => {
    if (selectedProject) {
      setNewProject(selectedProject);
    }
  }, [selectedProject]);

  const loadTasks = useCallback(async () => {
    setLoadingTasks(true);
    try {
      const reply = await fetchTasks({
        project: selectedProject || undefined,
        priority: priorityFilter || undefined,
        status: statusFilter || undefined,
        query: searchQuery || undefined,
        limit: 500,
      });
      setTasksList(reply.tasks ?? []);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to load tasks';
      onNotify('err', message);
    } finally {
      setLoadingTasks(false);
    }
  }, [selectedProject, priorityFilter, statusFilter, searchQuery, onNotify]);

  useEffect(() => {
    const debounceHandle = window.setTimeout(() => {
      void loadTasks();
    }, 150);
    return () => window.clearTimeout(debounceHandle);
  }, [loadTasks]);

  const projectOptions = useMemo(() => {
    const slugSet = new Set<string>();
    for (const proj of projects) {
      if (proj.slug) slugSet.add(proj.slug);
    }
    for (const taskRecord of tasksList) {
      if (taskRecord.project) slugSet.add(taskRecord.project);
    }
    return Array.from(slugSet).sort((left, right) => left.localeCompare(right));
  }, [projects, tasksList]);

  const milestoneOptions = useMemo(() => {
    const msSet = new Set<string>();
    for (const proj of projects) {
      if (!selectedProject || proj.slug === selectedProject) {
        for (const ms of proj.milestones ?? []) {
          if (ms.name) msSet.add(ms.name);
        }
      }
    }
    for (const taskRecord of tasksList) {
      if (taskRecord.milestone) msSet.add(taskRecord.milestone);
    }
    return Array.from(msSet).sort((left, right) => left.localeCompare(right));
  }, [projects, selectedProject, tasksList]);

  const filteredTasks = useMemo(() => {
    if (!milestoneFilter) {
      return tasksList;
    }
    return tasksList.filter((taskRecord) => taskRecord.milestone === milestoneFilter);
  }, [tasksList, milestoneFilter]);

  const tasksByColumn = useMemo(() => {
    const grouped: Record<KanbanColumnId, TaskRecord[]> = {
      todo: [],
      doing: [],
      blocked: [],
      done: [],
    };
    for (const taskRecord of filteredTasks) {
      grouped[resolveKanbanColumn(taskRecord.status)].push(taskRecord);
    }
    return grouped;
  }, [filteredTasks]);

  const handlePatchTask = async (
    taskCode: string,
    patch: { status?: string; priority?: string },
  ) => {
    setPatchingCode(taskCode);
    try {
      const updated = await patchTask(taskCode, patch);
      setTasksList((prev) =>
        prev.map((taskRecord) => (taskRecord.code === taskCode ? updated : taskRecord)),
      );
      onNotify(
        'ok',
        `${taskCode} updated${patch.status ? ` -> ${patch.status}` : ''}${patch.priority ? ` (${patch.priority})` : ''}.`,
      );
      await onTasksChanged();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to update task';
      onNotify('err', message);
    } finally {
      setPatchingCode(null);
    }
  };

  const handleDeleteTask = async (taskCode: string) => {
    try {
      await deleteTask(taskCode);
      setTasksList((prev) => prev.filter((taskRecord) => taskRecord.code !== taskCode));
      onNotify('ok', `Eliminated bounty ${taskCode}.`);
      await onTasksChanged();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to delete task';
      onNotify('err', message);
    }
  };

  const handleCreateTask = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!newCode.trim() || !newTitle.trim()) {
      onNotify('err', 'Task code and title are required.');
      return;
    }
    const parsedTags = newTagsInput
      .split(',')
      .map((tag) => tag.trim())
      .filter(Boolean);
    setCreatingTask(true);
    try {
      await createTask({
        code: newCode.trim().toUpperCase(),
        project: newProject.trim() || 'moonwitness',
        series: newSeries.trim(),
        milestone: newMilestone.trim(),
        title: newTitle.trim(),
        summary: newSummary.trim(),
        priority: newPriority,
        status: newStatus,
        tags: parsedTags,
      });
      setNewCode('');
      setNewTitle('');
      setNewSummary('');
      setNewTagsInput('');
      setCreateOpen(false);
      onNotify('ok', `Posted bounty ${newCode.trim().toUpperCase()}.`);
      await loadTasks();
      await onTasksChanged();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to create task';
      onNotify('err', message);
    } finally {
      setCreatingTask(false);
    }
  };

  const handleExportTasksJson = async () => {
    setExportingJson(true);
    try {
      const exported = await fetchTasksExportJson();
      const jsonBlob = new Blob([JSON.stringify(exported, null, 2)], {
        type: 'application/json',
      });
      const downloadUrl = URL.createObjectURL(jsonBlob);
      const anchor = document.createElement('a');
      anchor.href = downloadUrl;
      anchor.download = 'tasks.json';
      document.body.appendChild(anchor);
      anchor.click();
      document.body.removeChild(anchor);
      URL.revokeObjectURL(downloadUrl);
      onNotify('ok', 'Exported TaskOS v2 tasks.json.');
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to export tasks';
      onNotify('err', message);
    } finally {
      setExportingJson(false);
    }
  };

  return (
    <div className="space-y-5">
      <StageHeroBanner
        sceneCode="SCENE 03"
        sticker="BOUNTY BOARD"
        title="WANTED MISSIONS & TASKOS V2"
        subtitle="QUEUED // IN COMBAT // JAMMED // CLEARED — KANBAN STRIKE LANES OR DENSE INTEL TABLE"
        tone="magenta"
        actions={
          <>
            <div className="inline-flex border-[2.5px] border-black bg-[#08080c] shadow-[4px_4px_0_#000]">
              <button
                type="button"
                onClick={() => setViewMode('board')}
                className={`inline-flex items-center gap-1.5 px-3 py-1.5 font-mono text-xs font-extrabold uppercase ${
                  viewMode === 'board'
                    ? 'bg-[#ffe031] text-[#08080c]'
                    : 'text-[#a6a2b8] hover:text-[#fff6de]'
                }`}
              >
                <Kanban className="h-3.5 w-3.5" />
                BOUNTY BOARD
              </button>
              <button
                type="button"
                onClick={() => setViewMode('table')}
                className={`inline-flex items-center gap-1.5 border-l-2 border-black px-3 py-1.5 font-mono text-xs font-extrabold uppercase ${
                  viewMode === 'table'
                    ? 'bg-[#ffe031] text-[#08080c]'
                    : 'text-[#a6a2b8] hover:text-[#fff6de]'
                }`}
              >
                <Table2 className="h-3.5 w-3.5" />
                TABLE
              </button>
            </div>

            <button
              type="button"
              disabled={exportingJson}
              onClick={() => void handleExportTasksJson()}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#00f0ff] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c] disabled:opacity-50"
            >
              <Download className="h-3.5 w-3.5" />
              EXPORT JSON
            </button>

            <a
              href="/api/export/tasks.json"
              target="_blank"
              rel="noreferrer"
              className="riot-btn inline-flex items-center bg-[#1b1b2b] px-2.5 py-1.5 text-[#fff6de]"
              title="Open raw tasks.json"
            >
              <ExternalLink className="h-3.5 w-3.5" />
            </a>

            <button
              type="button"
              onClick={() => setCreateOpen((prev) => !prev)}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ff2e88] px-4 py-1.5 font-display text-xs text-white uppercase"
            >
              <Plus className="h-4 w-4" />
              POST BOUNTY
            </button>
          </>
        }
      />

      <SectionCard
        title="MISSION FILTER DECK"
        icon={<CheckSquare className="h-4 w-4" />}
        sticker={`${filteredTasks.length} ACTIVE`}
        stickerTone="yellow"
      >
        {/* Filter Bar */}
        <div className="mb-4 grid grid-cols-1 gap-2.5 md:grid-cols-12">
          <div className="relative md:col-span-4">
            <Search className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-[#ffe031]" />
            <input
              type="search"
              value={searchQuery}
              onChange={(ev) => setSearchQuery(ev.target.value)}
              placeholder="Search bounty code, title, summary..."
              className="riot-input w-full py-2 pr-3 pl-9 font-mono text-xs"
            />
          </div>

          <select
            value={selectedProject}
            onChange={(ev) => {
              onSelectProject(ev.target.value);
              setMilestoneFilter('');
            }}
            className="riot-input px-2.5 py-2 font-mono text-xs font-bold md:col-span-2"
          >
            <option value="">All Arcs</option>
            {projectOptions.map((slug) => (
              <option key={slug} value={slug}>
                {slug}
              </option>
            ))}
          </select>

          <select
            value={milestoneFilter}
            onChange={(ev) => setMilestoneFilter(ev.target.value)}
            className="riot-input px-2.5 py-2 font-mono text-xs font-bold md:col-span-2"
          >
            <option value="">All Chapters</option>
            {milestoneOptions.map((msName) => (
              <option key={msName} value={msName}>
                {msName}
              </option>
            ))}
          </select>

          <select
            value={priorityFilter}
            onChange={(ev) => setPriorityFilter(ev.target.value)}
            className="riot-input px-2.5 py-2 font-mono text-xs font-bold md:col-span-2"
          >
            <option value="">All Threat Tiers</option>
            <option value="p0">P0 (BOSS)</option>
            <option value="p1">P1 (HIGH)</option>
            <option value="p2">P2 (STD)</option>
            <option value="p3">P3 (LOW)</option>
          </select>

          <div className="flex gap-2 md:col-span-2">
            <select
              value={statusFilter}
              onChange={(ev) => setStatusFilter(ev.target.value)}
              className="riot-input min-w-0 flex-1 px-2.5 py-2 font-mono text-xs font-bold"
            >
              <option value="">All Status</option>
              <option value="todo">todo</option>
              <option value="backlog">backlog</option>
              <option value="doing">doing</option>
              <option value="in_progress">in_progress</option>
              <option value="blocked">blocked</option>
              <option value="done">done</option>
            </select>
            <button
              type="button"
              onClick={() => void loadTasks()}
              className="riot-btn bg-[#1b1b2b] px-2.5 py-2 text-[#ffe031]"
              title="Refresh bounties"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingTasks ? 'animate-spin' : ''}`} />
            </button>
          </div>
        </div>

        {/* + Post Bounty Inline Drawer */}
        {createOpen ? (
          <form
            onSubmit={handleCreateTask}
            className="mb-5 space-y-3 border-[2.5px] border-black bg-[#08080c] p-4 shadow-[5px_5px_0_#ff2e88]"
          >
            <div className="flex items-center justify-between border-b-2 border-black pb-2">
              <ComicSticker tone="magenta">NEW BOUNTY SLIP</ComicSticker>
              <button
                type="button"
                onClick={() => setCreateOpen(false)}
                className="text-[#a6a2b8] hover:text-[#ffe031]"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-6">
              <input
                type="text"
                value={newCode}
                onChange={(ev) => setNewCode(ev.target.value)}
                placeholder="Code (ECHO-042)"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
              <input
                type="text"
                value={newProject}
                onChange={(ev) => setNewProject(ev.target.value)}
                placeholder="project"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
              <input
                type="text"
                value={newSeries}
                onChange={(ev) => setNewSeries(ev.target.value)}
                placeholder="Series (optional)"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
              <input
                type="text"
                value={newMilestone}
                onChange={(ev) => setNewMilestone(ev.target.value)}
                placeholder="Milestone (M1)"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
              <select
                value={newPriority}
                onChange={(ev) => setNewPriority(ev.target.value)}
                className="riot-input px-2.5 py-2 font-mono text-xs font-bold"
              >
                <option value="p0">p0</option>
                <option value="p1">p1</option>
                <option value="p2">p2</option>
                <option value="p3">p3</option>
              </select>
              <select
                value={newStatus}
                onChange={(ev) => setNewStatus(ev.target.value)}
                className="riot-input px-2.5 py-2 font-mono text-xs font-bold"
              >
                <option value="todo">todo</option>
                <option value="backlog">backlog</option>
                <option value="doing">doing</option>
                <option value="blocked">blocked</option>
                <option value="done">done</option>
              </select>
            </div>

            <div className="grid grid-cols-1 gap-2.5 md:grid-cols-3">
              <input
                type="text"
                value={newTitle}
                onChange={(ev) => setNewTitle(ev.target.value)}
                placeholder="Mission title"
                className="riot-input px-2.5 py-2 font-mono text-xs md:col-span-2"
              />
              <input
                type="text"
                value={newTagsInput}
                onChange={(ev) => setNewTagsInput(ev.target.value)}
                placeholder="Tags (ui,api)"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
            </div>

            <div className="flex flex-wrap items-center gap-2.5">
              <input
                type="text"
                value={newSummary}
                onChange={(ev) => setNewSummary(ev.target.value)}
                placeholder="Mission brief / acceptance criteria (optional)"
                className="riot-input min-w-[240px] flex-1 px-2.5 py-2 font-mono text-xs"
              />
              <button
                type="submit"
                disabled={creatingTask}
                className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
              >
                <Plus className="h-3.5 w-3.5" />
                {creatingTask ? 'POSTING...' : 'DEPLOY BOUNTY'}
              </button>
            </div>
          </form>
        ) : null}

        {/* Active filter chips */}
        {selectedProject || milestoneFilter ? (
          <div className="mb-4 flex flex-wrap items-center gap-2">
            {selectedProject ? (
              <button
                type="button"
                onClick={() => onSelectProject('')}
                className="riot-btn inline-flex items-center gap-1 bg-[#ffe031] px-2.5 py-0.5 font-mono text-[11px] font-extrabold text-[#08080c]"
              >
                ARC: {selectedProject}
                <X className="h-3 w-3" />
              </button>
            ) : null}
            {milestoneFilter ? (
              <button
                type="button"
                onClick={() => setMilestoneFilter('')}
                className="riot-btn inline-flex items-center gap-1 bg-[#00f0ff] px-2.5 py-0.5 font-mono text-[11px] font-extrabold text-[#08080c]"
              >
                CHAPTER: {milestoneFilter}
                <X className="h-3 w-3" />
              </button>
            ) : null}
          </div>
        ) : null}

        {/* KANBAN BOUNTY BOARD */}
        {viewMode === 'board' ? (
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2 2xl:grid-cols-4">
            {KANBAN_COLUMNS.map((column) => {
              const columnTasks = tasksByColumn[column.id];
              return (
                <div
                  key={column.id}
                  className="flex flex-col border-[2.5px] border-black bg-[#08080c]"
                  style={{ boxShadow: `5px 5px 0 ${column.shadowColor}` }}
                >
                  <div
                    className="flex items-center justify-between border-b-[2.5px] border-black px-3 py-2"
                    style={{ backgroundColor: column.headerBg, color: column.headerText }}
                  >
                    <span className="font-display text-sm tracking-wide uppercase">
                      {column.title}
                    </span>
                    <span className="border-2 border-black bg-[#08080c] px-2 py-0.5 font-mono text-xs font-extrabold text-[#fff6de]">
                      {columnTasks.length}
                    </span>
                  </div>

                  <div className="max-h-[68vh] space-y-3 overflow-y-auto p-3">
                    {columnTasks.length === 0 ? (
                      <div className="border-2 border-dashed border-[#2f2f46] py-8 text-center font-mono text-[11px] font-bold uppercase text-[#a6a2b8]">
                        NO BOUNTIES
                      </div>
                    ) : (
                      columnTasks.map((taskRecord) => {
                        const priorityTone = PRIORITY_TONES[taskRecord.priority] ?? 'neutral';
                        const isBusy = patchingCode === taskRecord.code;
                        return (
                          <div
                            key={taskRecord.code}
                            className="space-y-2 border-2 border-black bg-[#13131d] p-3 shadow-[4px_4px_0_#000] transition-transform hover:-translate-x-0.5 hover:-translate-y-0.5"
                          >
                            <div className="flex items-center justify-between gap-1.5">
                              <div className="flex flex-wrap items-center gap-1.5">
                                <MonoBadge tone="cream">{taskRecord.code}</MonoBadge>
                                <select
                                  aria-label={`Priority for ${taskRecord.code}`}
                                  value={taskRecord.priority}
                                  disabled={isBusy}
                                  onChange={(ev) =>
                                    void handlePatchTask(taskRecord.code, {
                                      priority: ev.target.value,
                                    })
                                  }
                                  className="cursor-pointer border-2 border-black bg-[#ffe031] px-1.5 py-0.5 font-mono text-[10px] font-extrabold uppercase text-[#08080c] focus:outline-none"
                                >
                                  <option value="p0">p0</option>
                                  <option value="p1">p1</option>
                                  <option value="p2">p2</option>
                                  <option value="p3">p3</option>
                                </select>
                                {taskRecord.status === 'backlog' ? (
                                  <MonoBadge tone="neutral">backlog</MonoBadge>
                                ) : null}
                                {taskRecord.status === 'in_progress' ? (
                                  <MonoBadge tone={priorityTone}>in_progress</MonoBadge>
                                ) : null}
                              </div>

                              <button
                                type="button"
                                onClick={() => void handleDeleteTask(taskRecord.code)}
                                className="border border-black bg-[#1b1b2b] p-1 text-[#a6a2b8] hover:bg-[#ff334b] hover:text-white"
                                title="Delete bounty"
                              >
                                <Trash2 className="h-3 w-3" />
                              </button>
                            </div>

                            <div className="text-xs font-bold leading-snug text-[#fff6de]">
                              {taskRecord.title}
                            </div>

                            {taskRecord.summary ? (
                              <p className="line-clamp-2 text-[11px] text-[#a6a2b8]">
                                {taskRecord.summary}
                              </p>
                            ) : null}

                            <div className="flex flex-wrap items-center gap-1">
                              <MonoBadge tone="cyan">{taskRecord.project}</MonoBadge>
                              {taskRecord.milestone ? (
                                <MonoBadge tone="magenta">{taskRecord.milestone}</MonoBadge>
                              ) : null}
                              {(taskRecord.tags ?? []).slice(0, 3).map((tagLabel) => (
                                <MonoBadge key={tagLabel} tone="neutral">
                                  #{tagLabel}
                                </MonoBadge>
                              ))}
                            </div>

                            {/* One-click Strike Lane Move Triggers */}
                            <div className="flex flex-wrap gap-1 border-t-2 border-black pt-2">
                              {KANBAN_COLUMNS.filter((targetCol) => targetCol.id !== column.id).map(
                                (targetCol) => (
                                  <button
                                    key={targetCol.id}
                                    type="button"
                                    disabled={isBusy}
                                    onClick={() =>
                                      void handlePatchTask(taskRecord.code, {
                                        status: targetCol.targetStatus,
                                      })
                                    }
                                    className="border-2 border-black bg-[#08080c] px-2 py-0.5 font-mono text-[10px] font-extrabold uppercase text-[#fff6de] transition-colors hover:bg-[#ffe031] hover:text-[#08080c] disabled:opacity-50"
                                  >
                                    -&gt; {targetCol.title}
                                  </button>
                                ),
                              )}
                            </div>
                          </div>
                        );
                      })
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        ) : (
          /* TABLE INTEL VIEW */
          <div className="overflow-x-auto border-2 border-black bg-[#08080c]">
            <table className="w-full border-collapse text-left text-xs">
              <thead>
                <tr className="border-b-2 border-black bg-[#ffe031] font-mono text-[10px] font-extrabold uppercase tracking-wider text-[#08080c]">
                  <th className="px-3 py-2">Code</th>
                  <th className="px-3 py-2">Mission Title &amp; Brief</th>
                  <th className="px-3 py-2">Arc / Chapter</th>
                  <th className="px-3 py-2">Priority</th>
                  <th className="px-3 py-2">Status</th>
                  <th className="px-3 py-2 text-right">Action</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[#252538]">
                {filteredTasks.length > 0 ? (
                  filteredTasks.map((taskRecord) => {
                    const isBusy = patchingCode === taskRecord.code;
                    return (
                      <tr key={taskRecord.code} className="hover:bg-[#1b1b2b]">
                        <td className="px-3 py-2.5">
                          <MonoBadge tone="cream">{taskRecord.code}</MonoBadge>
                        </td>
                        <td className="px-3 py-2.5">
                          <div className="font-bold text-[#fff6de]">{taskRecord.title}</div>
                          {taskRecord.summary ? (
                            <div className="mt-0.5 max-w-md truncate text-[11px] text-[#a6a2b8]">
                              {taskRecord.summary}
                            </div>
                          ) : null}
                        </td>
                        <td className="px-3 py-2.5">
                          <div className="flex flex-wrap gap-1">
                            <MonoBadge tone="cyan">{taskRecord.project}</MonoBadge>
                            {taskRecord.milestone ? (
                              <MonoBadge tone="magenta">{taskRecord.milestone}</MonoBadge>
                            ) : null}
                          </div>
                        </td>
                        <td className="px-3 py-2.5">
                          <select
                            value={taskRecord.priority}
                            disabled={isBusy}
                            onChange={(ev) =>
                              void handlePatchTask(taskRecord.code, { priority: ev.target.value })
                            }
                            className="riot-input px-2 py-1 font-mono text-xs font-extrabold text-[#ffe031]"
                          >
                            <option value="p0">p0</option>
                            <option value="p1">p1</option>
                            <option value="p2">p2</option>
                            <option value="p3">p3</option>
                          </select>
                        </td>
                        <td className="px-3 py-2.5">
                          <select
                            value={taskRecord.status}
                            disabled={isBusy}
                            onChange={(ev) =>
                              void handlePatchTask(taskRecord.code, { status: ev.target.value })
                            }
                            className="riot-input px-2 py-1 font-mono text-xs font-bold"
                          >
                            <option value="todo">todo</option>
                            <option value="backlog">backlog</option>
                            <option value="doing">doing</option>
                            <option value="in_progress">in_progress</option>
                            <option value="blocked">blocked</option>
                            <option value="done">done</option>
                          </select>
                        </td>
                        <td className="px-3 py-2.5 text-right">
                          <button
                            type="button"
                            onClick={() => void handleDeleteTask(taskRecord.code)}
                            className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2 py-1 font-mono text-[10px] font-extrabold text-white"
                          >
                            <Trash2 className="h-3 w-3" />
                            DEL
                          </button>
                        </td>
                      </tr>
                    );
                  })
                ) : (
                  <tr>
                    <td
                      colSpan={6}
                      className="px-3 py-8 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]"
                    >
                      No bounties match your current filters.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        )}
      </SectionCard>
    </div>
  );
}
