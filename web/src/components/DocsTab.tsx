import { useCallback, useEffect, useState } from 'react';
import {
  BookOpen,
  Code2,
  Edit3,
  Eye,
  FilePlus2,
  Save,
  Search,
  Trash2,
  X,
} from 'lucide-react';
import {
  deleteDoc,
  fetchDocById,
  fetchDocs,
  upsertDoc,
  type DocRecord,
} from '../api';
import { formatBytes, formatShortTime } from '../utils';
import { MonoBadge, SectionCard } from './UiPrimitives';

interface DocsTabProps {
  onDocsChanged: () => Promise<void>;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

function renderSnippetHighlights(snippet: string) {
  const parts = snippet.split(/(\[[^\]]+\])/g);
  return parts.map((segment, segIdx) => {
    if (segment.startsWith('[') && segment.endsWith(']') && segment.length > 2) {
      return (
        <mark
          key={`${segIdx}-${segment}`}
          className="rounded bg-[#ffe031]/25 px-0.5 font-semibold text-[#ffe031]"
        >
          {segment.slice(1, -1)}
        </mark>
      );
    }
    return <span key={`${segIdx}-${segment}`}>{segment}</span>;
  });
}

function renderMarkdownBlocks(markdownText: string) {
  const lines = markdownText.replace(/\r\n/g, '\n').split('\n');
  const blocks: Array<{ kind: string; content: string; lines?: string[]; lang?: string }> = [];
  let lineIndex = 0;

  while (lineIndex < lines.length) {
    const line = lines[lineIndex];
    const trimmed = line.trim();

    if (trimmed.startsWith('```')) {
      const lang = trimmed.slice(3).trim();
      const codeLines: string[] = [];
      lineIndex += 1;
      while (lineIndex < lines.length && !lines[lineIndex].trim().startsWith('```')) {
        codeLines.push(lines[lineIndex]);
        lineIndex += 1;
      }
      lineIndex += 1;
      blocks.push({ kind: 'code', content: codeLines.join('\n'), lang });
      continue;
    }

    if (trimmed === '') {
      lineIndex += 1;
      continue;
    }

    if (trimmed.startsWith('### ')) {
      blocks.push({ kind: 'h3', content: trimmed.slice(4) });
      lineIndex += 1;
      continue;
    }
    if (trimmed.startsWith('## ')) {
      blocks.push({ kind: 'h2', content: trimmed.slice(3) });
      lineIndex += 1;
      continue;
    }
    if (trimmed.startsWith('# ')) {
      blocks.push({ kind: 'h1', content: trimmed.slice(2) });
      lineIndex += 1;
      continue;
    }

    if (trimmed.startsWith('- ') || trimmed.startsWith('* ')) {
      const bulletLines: string[] = [];
      while (
        lineIndex < lines.length &&
        (lines[lineIndex].trim().startsWith('- ') || lines[lineIndex].trim().startsWith('* '))
      ) {
        bulletLines.push(lines[lineIndex].trim().slice(2));
        lineIndex += 1;
      }
      blocks.push({ kind: 'list', content: '', lines: bulletLines });
      continue;
    }

    const paraLines: string[] = [line];
    lineIndex += 1;
    while (
      lineIndex < lines.length &&
      lines[lineIndex].trim() !== '' &&
      !lines[lineIndex].trim().startsWith('#') &&
      !lines[lineIndex].trim().startsWith('```') &&
      !lines[lineIndex].trim().startsWith('- ') &&
      !lines[lineIndex].trim().startsWith('* ')
    ) {
      paraLines.push(lines[lineIndex]);
      lineIndex += 1;
    }
    blocks.push({ kind: 'paragraph', content: paraLines.join('\n') });
  }

  return blocks.map((block, blockIdx) => {
    if (block.kind === 'h1') {
      return (
        <h1
          key={blockIdx}
          className="mt-4 mb-2 border-b border-[#2a2a36] pb-1.5 text-xl font-extrabold text-[#ffe031] first:mt-0"
        >
          {block.content}
        </h1>
      );
    }
    if (block.kind === 'h2') {
      return (
        <h2 key={blockIdx} className="mt-4 mb-1.5 text-base font-bold text-[#00b8f0]">
          {block.content}
        </h2>
      );
    }
    if (block.kind === 'h3') {
      return (
        <h3 key={blockIdx} className="mt-3 mb-1 text-sm font-bold text-[#f3efe4]">
          {block.content}
        </h3>
      );
    }
    if (block.kind === 'code') {
      return (
        <div key={blockIdx} className="my-2.5 overflow-hidden rounded border border-[#2a2a36]">
          {block.lang ? (
            <div className="border-b border-[#2a2a36] bg-[#15151a] px-3 py-1 font-mono text-[10px] uppercase text-[#9c988c]">
              {block.lang}
            </div>
          ) : null}
          <pre className="overflow-x-auto bg-[#09090c] p-3 font-mono text-xs text-[#f3efe4]">
            {block.content}
          </pre>
        </div>
      );
    }
    if (block.kind === 'list' && block.lines) {
      return (
        <ul key={blockIdx} className="my-2 list-disc space-y-1 pl-5 text-sm text-[#f3efe4]/90">
          {block.lines.map((bullet, bulletIdx) => (
            <li key={bulletIdx}>{bullet}</li>
          ))}
        </ul>
      );
    }
    return (
      <p key={blockIdx} className="my-2 whitespace-pre-wrap text-sm leading-relaxed text-[#f3efe4]/90">
        {block.content}
      </p>
    );
  });
}

export function DocsTab({ onDocsChanged, onNotify }: DocsTabProps) {
  const [searchQuery, setSearchQuery] = useState('');
  const [docsList, setDocsList] = useState<DocRecord[]>([]);
  const [loadingList, setLoadingList] = useState(false);

  const [activeDoc, setActiveDoc] = useState<DocRecord | null>(null);
  const [loadingDoc, setLoadingDoc] = useState(false);
  const [editorMode, setEditorMode] = useState<'read' | 'edit' | 'create'>('read');
  const [rawView, setRawView] = useState(false);

  // Editor form state
  const [draftProject, setDraftProject] = useState('moonwitness');
  const [draftRelPath, setDraftRelPath] = useState('');
  const [draftTitle, setDraftTitle] = useState('');
  const [draftCategory, setDraftCategory] = useState('');
  const [draftContent, setDraftContent] = useState('');
  const [savingDoc, setSavingDoc] = useState(false);

  const loadDocsList = useCallback(
    async (queryText: string) => {
      setLoadingList(true);
      try {
        const reply = await fetchDocs(queryText);
        setDocsList(reply.docs ?? []);
      } catch (caughtError) {
        const message = caughtError instanceof Error ? caughtError.message : 'Failed to load docs';
        onNotify('err', message);
      } finally {
        setLoadingList(false);
      }
    },
    [onNotify],
  );

  useEffect(() => {
    const debounceHandle = window.setTimeout(() => {
      void loadDocsList(searchQuery);
    }, 180);
    return () => window.clearTimeout(debounceHandle);
  }, [searchQuery, loadDocsList]);

  const handleOpenDoc = async (docId: string) => {
    setLoadingDoc(true);
    try {
      const loaded = await fetchDocById(docId);
      setActiveDoc(loaded);
      setEditorMode('read');
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to load doc';
      onNotify('err', message);
    } finally {
      setLoadingDoc(false);
    }
  };

  const handleStartCreate = () => {
    setDraftProject('moonwitness');
    setDraftRelPath('');
    setDraftTitle('');
    setDraftCategory('');
    setDraftContent('# New Document\n\nWrite Markdown notes here.\n');
    setEditorMode('create');
  };

  const handleStartEdit = () => {
    if (!activeDoc) {
      return;
    }
    setDraftProject(activeDoc.project);
    setDraftRelPath(activeDoc.relPath);
    setDraftTitle(activeDoc.title);
    setDraftCategory(activeDoc.category);
    setDraftContent(activeDoc.content ?? '');
    setEditorMode('edit');
  };

  const handleSaveDoc = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!draftRelPath.trim()) {
      onNotify('err', 'Relative path (e.g. docs/architecture.md) is required.');
      return;
    }
    setSavingDoc(true);
    try {
      const saved = await upsertDoc({
        id: editorMode === 'edit' ? activeDoc?.id : undefined,
        project: draftProject.trim() || 'moonwitness',
        relPath: draftRelPath.trim(),
        title: draftTitle.trim() || undefined,
        category: draftCategory.trim() || undefined,
        content: draftContent,
      });
      setActiveDoc(saved);
      setEditorMode('read');
      onNotify('ok', `Saved document ${saved.relPath}.`);
      await loadDocsList(searchQuery);
      await onDocsChanged();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to save document';
      onNotify('err', message);
    } finally {
      setSavingDoc(false);
    }
  };

  const handleDeleteDoc = async () => {
    if (!activeDoc) {
      return;
    }
    try {
      await deleteDoc(activeDoc.id);
      onNotify('ok', `Deleted ${activeDoc.relPath}.`);
      setActiveDoc(null);
      setEditorMode('read');
      await loadDocsList(searchQuery);
      await onDocsChanged();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to delete document';
      onNotify('err', message);
    }
  };

  return (
    <SectionCard
      title="Docs Knowledge Base (SQLite FTS5)"
      subtitle="Instant full-text BM25 search with highlighted snippets, Markdown reader, and inline editor."
      icon={<BookOpen className="h-4 w-4" />}
      actions={
        <div className="flex items-center gap-2">
          <MonoBadge tone="yellow">{docsList.length} MATCHED</MonoBadge>
          <button
            type="button"
            onClick={handleStartCreate}
            className="inline-flex items-center gap-1.5 rounded bg-[#ffe031] px-3 py-1.5 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90"
          >
            <FilePlus2 className="h-3.5 w-3.5" />
            NEW DOC
          </button>
        </div>
      }
    >
      {/* Instant FTS5 Search Input */}
      <div className="relative mb-4">
        <Search className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-[#9c988c]" />
        <input
          type="search"
          value={searchQuery}
          onChange={(ev) => setSearchQuery(ev.target.value)}
          placeholder="Instant FTS5 search across titles, paths, and Markdown body..."
          className="w-full rounded-md border border-[#2a2a36] bg-[#0d0d10] py-2.5 pr-3 pl-9 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
        />
      </div>

      {/* Split Pane */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
        {/* Left List */}
        <div className="max-h-[68vh] overflow-y-auto rounded-lg border border-[#2a2a36] bg-[#0d0d10] lg:col-span-4">
          {loadingList && docsList.length === 0 ? (
            <div className="p-4 font-mono text-xs text-[#9c988c]">Searching FTS5 index...</div>
          ) : docsList.length === 0 ? (
            <div className="p-4 font-mono text-xs text-[#9c988c]">
              No matching documents. Import a repo from Overview or create a new doc.
            </div>
          ) : (
            <div className="divide-y divide-[#2a2a36]">
              {docsList.map((docEntry) => {
                const isSelected = activeDoc?.id === docEntry.id && editorMode !== 'create';
                return (
                  <button
                    key={docEntry.id}
                    type="button"
                    onClick={() => void handleOpenDoc(docEntry.id)}
                    className={`block w-full px-3.5 py-3 text-left transition-colors ${
                      isSelected
                        ? 'border-l-2 border-l-[#ffe031] bg-[#1c1c23]'
                        : 'hover:bg-[#15151a]'
                    }`}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <span className="line-clamp-1 text-xs font-bold text-[#f3efe4]">
                        {docEntry.title}
                      </span>
                      <MonoBadge tone="neutral">{docEntry.category}</MonoBadge>
                    </div>
                    <div className="mt-0.5 truncate font-mono text-[10px] text-[#9c988c]">
                      {docEntry.project} / {docEntry.relPath}
                    </div>
                    {docEntry.snippet ? (
                      <div className="mt-1.5 line-clamp-2 text-[11px] leading-snug text-[#f3efe4]/75">
                        {renderSnippetHighlights(docEntry.snippet)}
                      </div>
                    ) : null}
                  </button>
                );
              })}
            </div>
          )}
        </div>

        {/* Right Reader / Editor */}
        <div className="min-h-[55vh] rounded-lg border border-[#2a2a36] bg-[#0d0d10] p-4 lg:col-span-8">
          {editorMode === 'create' || editorMode === 'edit' ? (
            <form onSubmit={handleSaveDoc} className="flex h-full flex-col gap-3">
              <div className="flex items-center justify-between border-b border-[#2a2a36] pb-2.5">
                <MonoBadge tone="yellow">
                  {editorMode === 'create' ? 'CREATE NEW DOC' : 'EDITING DOC'}
                </MonoBadge>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={() => setEditorMode('read')}
                    className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1 font-mono text-[11px] text-[#9c988c] hover:text-[#f3efe4]"
                  >
                    <X className="h-3 w-3" />
                    CANCEL
                  </button>
                  <button
                    type="submit"
                    disabled={savingDoc}
                    className="inline-flex items-center gap-1 rounded bg-[#ffe031] px-3 py-1 font-mono text-[11px] font-extrabold text-[#0d0d10] hover:opacity-90 disabled:opacity-50"
                  >
                    <Save className="h-3 w-3" />
                    {savingDoc ? 'SAVING...' : 'SAVE DOC'}
                  </button>
                </div>
              </div>

              <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-4">
                <input
                  type="text"
                  value={draftProject}
                  onChange={(ev) => setDraftProject(ev.target.value)}
                  placeholder="project"
                  className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
                />
                <input
                  type="text"
                  value={draftRelPath}
                  onChange={(ev) => setDraftRelPath(ev.target.value)}
                  placeholder="docs/guide.md"
                  className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none sm:col-span-2"
                />
                <input
                  type="text"
                  value={draftCategory}
                  onChange={(ev) => setDraftCategory(ev.target.value)}
                  placeholder="category (auto)"
                  className="rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
                />
              </div>

              <input
                type="text"
                value={draftTitle}
                onChange={(ev) => setDraftTitle(ev.target.value)}
                placeholder="Document title (optional — derived from # Heading)"
                className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />

              <textarea
                rows={18}
                value={draftContent}
                onChange={(ev) => setDraftContent(ev.target.value)}
                placeholder="# Title&#10;&#10;Markdown body..."
                className="flex-1 rounded border border-[#2a2a36] bg-[#15151a] p-3 font-mono text-xs leading-relaxed text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
            </form>
          ) : loadingDoc ? (
            <div className="font-mono text-xs text-[#9c988c]">Loading document...</div>
          ) : activeDoc ? (
            <div className="space-y-3">
              <div className="flex flex-wrap items-start justify-between gap-2 border-b border-[#2a2a36] pb-3">
                <div>
                  <h3 className="text-base font-extrabold text-[#f3efe4]">{activeDoc.title}</h3>
                  <div className="mt-1 flex flex-wrap items-center gap-1.5">
                    <MonoBadge tone="cyan">{activeDoc.project}</MonoBadge>
                    <MonoBadge tone="neutral">{activeDoc.relPath}</MonoBadge>
                    <MonoBadge tone="neutral">{formatBytes(activeDoc.sizeBytes)}</MonoBadge>
                    <span className="font-mono text-[10px] text-[#9c988c]">
                      Updated {formatShortTime(activeDoc.updatedAt)}
                    </span>
                  </div>
                </div>
                <div className="flex items-center gap-1.5">
                  <button
                    type="button"
                    onClick={() => setRawView((prev) => !prev)}
                    className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1 font-mono text-[11px] text-[#f3efe4] hover:border-[#00b8f0]"
                  >
                    {rawView ? <Eye className="h-3 w-3" /> : <Code2 className="h-3 w-3" />}
                    {rawView ? 'READER' : 'RAW'}
                  </button>
                  <button
                    type="button"
                    onClick={handleStartEdit}
                    className="inline-flex items-center gap-1 rounded border border-[#ffe031]/50 bg-[#ffe031]/10 px-2.5 py-1 font-mono text-[11px] font-bold text-[#ffe031] hover:bg-[#ffe031]/20"
                  >
                    <Edit3 className="h-3 w-3" />
                    EDIT
                  </button>
                  <button
                    type="button"
                    onClick={() => void handleDeleteDoc()}
                    className="inline-flex items-center gap-1 rounded border border-[#ef4444]/40 bg-[#ef4444]/10 px-2.5 py-1 font-mono text-[11px] font-bold text-[#ef4444] hover:bg-[#ef4444]/20"
                  >
                    <Trash2 className="h-3 w-3" />
                    DEL
                  </button>
                </div>
              </div>

              <div className="max-h-[58vh] overflow-y-auto pr-1">
                {rawView ? (
                  <pre className="whitespace-pre-wrap break-words rounded border border-[#2a2a36] bg-[#15151a] p-3 font-mono text-xs leading-relaxed text-[#f3efe4]">
                    {activeDoc.content ?? ''}
                  </pre>
                ) : (
                  <article className="space-y-1">
                    {renderMarkdownBlocks(activeDoc.content ?? '')}
                  </article>
                )}
              </div>
            </div>
          ) : (
            <div className="flex h-full min-h-[300px] flex-col items-center justify-center gap-2 text-center font-mono text-xs text-[#9c988c]">
              <BookOpen className="h-8 w-8 text-[#2a2a36]" />
              <span>Select a document from the left pane to read or edit.</span>
            </div>
          )}
        </div>
      </div>
    </SectionCard>
  );
}
