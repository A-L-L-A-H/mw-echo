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
import { MonoBadge, SectionCard, type BadgeTone } from './UiPrimitives';

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
  accentHex: string;
  badgeTone: BadgeTone;
}

const KANBAN_COLUMNS: KanbanColumnDef[] = [
  {
    id: 'todo',
    title: 'TODO / BACKLOG',
    targetStatus: 'todo',
    accentHex: '#00b8f0',
    badgeTone: 'cyan',
  },
  {
    id: 'doing',
    title: 'DOING / IN PROGRESS',
    targetStatus: 'doing',
    accentHex: '#ffe031',
    badgeTone: 'yellow',
  },
  {
    id: 'blocked',
    title: 'BLOCKED',
    targetStatus: 'blocked',
    accentHex: '#ef4444',
    badgeTone: 'err',
  },
  {
    id: 'done',
    title: 'DONE',
    targetStatus: 'done',
    accentHex: '#22c55e',
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

  // Create task drawer/form
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
      onNotify('ok', `Deleted task ${taskCode}.`);
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
      onNotify('ok', `Created task ${newCode.trim().toUpperCase()}.`);
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
    <div className="space-y-4">
      <SectionCard
        title="Task Management (TaskOS v2)"
        subtitle="Switch between Kanban Board and Table View, filter by project/milestone/priority, and move work across stages."
        icon={<CheckSquare className="h-4 w-4" />}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {/* Kanban vs Table toggle */}
            <div className="inline-flex overflow-hidden rounded border border-[#2a2a36] bg-[#0d0d10]">
              <button
                type="button"
                onClick={() => setViewMode('board')}
                className={`inline-flex items-center gap-1.5 px-3 py-1.5 font-mono text-xs font-bold transition-colors ${
                  viewMode === 'board'
                    ? 'bg-[#ffe031] text-[#0d0d10]'
                    : 'text-[#9c988c] hover:text-[#f3efe4]'
                }`}
              >
                <Kanban className="h-3.5 w-3.5" />
                KANBAN
              </button>
              <button
                type="button"
                onClick={() => setViewMode('table')}
                className={`inline-flex items-center gap-1.5 px-3 py-1.5 font-mono text-xs font-bold transition-colors ${
                  viewMode === 'table'
                    ? 'bg-[#ffe031] text-[#0d0d10]'
                    : 'text-[#9c988c] hover:text-[#f3efe4]'
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
              className="inline-flex items-center gap-1.5 rounded border border-[#2a2a36] bg-[#1c1c23] px-3 py-1.5 font-mono text-xs font-semibold text-[#00b8f0] hover:border-[#00b8f0] disabled:opacity-50"
            >
              <Download className="h-3.5 w-3.5" />
              EXPORT tasks.json
            </button>

            <a
              href="/api/export/tasks.json"
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#1c1c23] px-2 py-1.5 font-mono text-xs text-[#9c988c] hover:text-[#f3efe4]"
              title="Open raw tasks.json in new tab"
            >
              <ExternalLink className="h-3.5 w-3.5" />
            </a>

            <button
              type="button"
              onClick={() => setCreateOpen((prev) => !prev)}
              className="inline-flex items-center gap-1.5 rounded bg-[#ffe031] px-3.5 py-1.5 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90"
            >
              <Plus className="h-3.5 w-3.5" />
              NEW TASK
            </button>
          </div>
        }
      >
        {/* Filter Toolbar */}
        <div className="mb-4 grid grid-cols-1 gap-2.5 md:grid-cols-12">
          <div className="relative md:col-span-4">
            <Search className="pointer-events-none absolute top-1/2 left-3 h-3.5 w-3.5 -translate-y-1/2 text-[#9c988c]" />
            <input
              type="search"
              value={searchQuery}
              onChange={(ev) => setSearchQuery(ev.target.value)}
              placeholder="Search code, title, summary..."
              className="w-full rounded border border-[#2a2a36] bg-[#0d0d10] py-2 pr-3 pl-9 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>

          <select
            value={selectedProject}
            onChange={(ev) => {
              onSelectProject(ev.target.value);
              setMilestoneFilter('');
            }}
            className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none md:col-span-2"
          >
            <option value="">All Projects</option>
            {projectOptions.map((slug) => (
              <option key={slug} value={slug}>
                {slug}
              </option>
            ))}
          </select>

          <select
            value={milestoneFilter}
            onChange={(ev) => setMilestoneFilter(ev.target.value)}
            className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none md:col-span-2"
          >
            <option value="">All Milestones</option>
            {milestoneOptions.map((msName) => (
              <option key={msName} value={msName}>
                {msName}
              </option>
            ))}
          </select>

          <select
            value={priorityFilter}
            onChange={(ev) => setPriorityFilter(ev.target.value)}
            className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none md:col-span-2"
          >
            <option value="">All Priorities</option>
            <option value="p0">p0 (Critical)</option>
            <option value="p1">p1 (High)</option>
            <option value="p2">p2 (Normal)</option>
            <option value="p3">p3 (Low)</option>
          </select>

          <div className="flex gap-2 md:col-span-2">
            <select
              value={statusFilter}
              onChange={(ev) => setStatusFilter(ev.target.value)}
              className="min-w-0 flex-1 rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
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
              className="rounded border border-[#2a2a36] bg-[#1c1c23] px-2.5 py-2 text-[#f3efe4] hover:border-[#ffe031]"
              title="Refresh tasks"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingTasks ? 'animate-spin' : ''}`} />
            </button>
          </div>
        </div>

        {/* + New Task Inline Drawer */}
        {createOpen ? (
          <form
            onSubmit={handleCreateTask}
            className="mb-4 space-y-3 rounded-lg border border-[#ffe031]/50 bg-[#0d0d10] p-4"
          >
            <div className="flex items-center justify-between border-b border-[#2a2a36] pb-2">
              <MonoBadge tone="yellow">CREATE TASKOS V2 TASK</MonoBadge>
              <button
                type="button"
                onClick={() => setCreateOpen(false)}
                className="text-[#9c988c] hover:text-[#f3efe4]"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-6">
              <input
                type="text"
                value={newCode}
                onChange={(ev) => setNewCode(ev.target.value)}
                placeholder="Code (e.g. ECHO-042)"
                className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
              <input
                type="text"
                value={newProject}
                onChange={(ev) => setNewProject(ev.target.value)}
                placeholder="project"
                className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
              <input
                type="text"
                value={newSeries}
                onChange={(ev) => setNewSeries(ev.target.value)}
                placeholder="Series (optional)"
                className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
              <input
                type="text"
                value={newMilestone}
                onChange={(ev) => setNewMilestone(ev.target.value)}
                placeholder="Milestone (e.g. M1)"
                className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
              <select
                value={newPriority}
                onChange={(ev) => setNewPriority(ev.target.value)}
                className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              >
                <option value="p0">p0</option>
                <option value="p1">p1</option>
                <option value="p2">p2</option>
                <option value="p3">p3</option>
              </select>
              <select
                value={newStatus}
                onChange={(ev) => setNewStatus(ev.target.value)}
                className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
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
                placeholder="Task title"
                className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none md:col-span-2"
              />
              <input
                type="text"
                value={newTagsInput}
                onChange={(ev) => setNewTagsInput(ev.target.value)}
                placeholder="Tags (comma-separated: ui,api)"
                className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
            </div>

            <div className="flex flex-wrap items-center gap-2.5">
              <input
                type="text"
                value={newSummary}
                onChange={(ev) => setNewSummary(ev.target.value)}
                placeholder="Short summary / acceptance criteria (optional)"
                className="min-w-[240px] flex-1 rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
              <button
                type="submit"
                disabled={creatingTask}
                className="inline-flex items-center gap-1.5 rounded bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90 disabled:opacity-50"
              >
                <Plus className="h-3.5 w-3.5" />
                {creatingTask ? 'CREATING...' : 'CREATE TASK'}
              </button>
            </div>
          </form>
        ) : null}

        {/* Active filter chip strip */}
        <div className="mb-3 flex flex-wrap items-center gap-2 font-mono text-[11px] text-[#9c988c]">
          <MonoBadge tone="neutral">{filteredTasks.length} TASKS</MonoBadge>
          {selectedProject ? (
            <button
              type="button"
              onClick={() => onSelectProject('')}
              className="inline-flex items-center gap-1 rounded border border-[#ffe031]/50 bg-[#ffe031]/10 px-2 py-0.5 text-[#ffe031]"
            >
              PROJECT: {selectedProject}
              <X className="h-3 w-3" />
            </button>
          ) : null}
          {milestoneFilter ? (
            <button
              type="button"
              onClick={() => setMilestoneFilter('')}
              className="inline-flex items-center gap-1 rounded border border-[#00b8f0]/50 bg-[#00b8f0]/10 px-2 py-0.5 text-[#00b8f0]"
            >
              MILESTONE: {milestoneFilter}
              <X className="h-3 w-3" />
            </button>
          ) : null}
        </div>

        {/* KANBAN BOARD VIEW */}
        {viewMode === 'board' ? (
          <div className="grid grid-cols-1 gap-3 lg:grid-cols-2 2xl:grid-cols-4">
            {KANBAN_COLUMNS.map((column) => {
              const columnTasks = tasksByColumn[column.id];
              return (
                <div
                  key={column.id}
                  className="flex flex-col rounded-lg border border-[#2a2a36] bg-[#0d0d10]"
                >
                  <div
                    className="flex items-center justify-between border-b border-[#2a2a36] px-3 py-2.5"
                    style={{ borderTop: `3px solid ${column.accentHex}` }}
                  >
                    <span className="font-mono text-xs font-extrabold tracking-wide text-[#f3efe4]">
                      {column.title}
                    </span>
                    <MonoBadge tone={column.badgeTone}>{columnTasks.length}</MonoBadge>
                  </div>

                  <div className="max-h-[68vh] space-y-2.5 overflow-y-auto p-2.5">
                    {columnTasks.length === 0 ? (
                      <div className="rounded border border-dashed border-[#2a2a36] py-6 text-center font-mono text-[11px] text-[#9c988c]">
                        Empty column
                      </div>
                    ) : (
                      columnTasks.map((taskRecord) => {
                        const priorityTone = PRIORITY_TONES[taskRecord.priority] ?? 'neutral';
                        const isBusy = patchingCode === taskRecord.code;
                        return (
                          <div
                            key={taskRecord.code}
                            className="space-y-2 rounded-md border border-[#2a2a36] bg-[#15151a] p-3 transition-colors hover:border-[#3d3d4f]"
                          >
                            <div className="flex items-center justify-between gap-1.5">
                              <div className="flex flex-wrap items-center gap-1.5">
                                <MonoBadge tone="neutral">{taskRecord.code}</MonoBadge>
                                <select
                                  aria-label={`Priority for ${taskRecord.code}`}
                                  value={taskRecord.priority}
                                  disabled={isBusy}
                                  onChange={(ev) =>
                                    void handlePatchTask(taskRecord.code, {
                                      priority: ev.target.value,
                                    })
                                  }
                                  className="cursor-pointer rounded border border-[#2a2a36] bg-[#0d0d10] px-1.5 py-0.5 font-mono text-[10px] font-bold uppercase text-[#ffe031] focus:outline-none"
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
                                className="text-[#9c988c] hover:text-[#ef4444]"
                                title="Delete task"
                              >
                                <Trash2 className="h-3.5 w-3.5" />
                              </button>
                            </div>

                            <div className="text-xs font-bold leading-snug text-[#f3efe4]">
                              {taskRecord.title}
                            </div>

                            {taskRecord.summary ? (
                              <p className="line-clamp-2 text-[11px] text-[#9c988c]">
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

                            {/* One-click Move buttons */}
                            <div className="flex flex-wrap gap-1 border-t border-[#2a2a36] pt-2">
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
                                    className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2 py-0.5 font-mono text-[10px] font-semibold text-[#9c988c] transition-colors hover:border-[#ffe031] hover:text-[#ffe031] disabled:opacity-50"
                                  >
                                    -&gt; {targetCol.targetStatus}
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
          /* TABLE VIEW */
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-left text-xs">
              <thead>
                <tr className="border-b border-[#2a2a36] font-mono text-[10px] uppercase tracking-wider text-[#9c988c]">
                  <th className="px-3 py-2">Code</th>
                  <th className="px-3 py-2">Title &amp; Summary</th>
                  <th className="px-3 py-2">Project / Milestone</th>
                  <th className="px-3 py-2">Priority</th>
                  <th className="px-3 py-2">Status</th>
                  <th className="px-3 py-2 text-right">Action</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[#2a2a36]/70">
                {filteredTasks.length > 0 ? (
                  filteredTasks.map((taskRecord) => {
                    const isBusy = patchingCode === taskRecord.code;
                    return (
                      <tr key={taskRecord.code} className="hover:bg-[#1c1c23]/60">
                        <td className="px-3 py-2.5">
                          <MonoBadge tone="neutral">{taskRecord.code}</MonoBadge>
                        </td>
                        <td className="px-3 py-2.5">
                          <div className="font-semibold text-[#f3efe4]">{taskRecord.title}</div>
                          {taskRecord.summary ? (
                            <div className="mt-0.5 max-w-md truncate text-[11px] text-[#9c988c]">
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
                            className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2 py-1 font-mono text-xs font-bold text-[#ffe031] focus:border-[#ffe031] focus:outline-none"
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
                            className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2 py-1 font-mono text-xs font-semibold text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
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
                            className="inline-flex items-center gap-1 rounded border border-[#ef4444]/40 bg-[#ef4444]/10 px-2 py-1 font-mono text-[10px] font-bold text-[#ef4444] hover:bg-[#ef4444]/20"
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
                    <td colSpan={6} className="px-3 py-8 text-center font-mono text-xs text-[#9c988c]">
                      No tasks match your current filters.
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
