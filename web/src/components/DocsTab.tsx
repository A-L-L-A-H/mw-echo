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
import { ComicSticker, MonoBadge, SectionCard, StageHeroBanner } from './UiPrimitives';

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
          className="border border-black bg-[#ffe031] px-0.5 font-extrabold text-[#08080c]"
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
          className="mt-4 mb-2 border-b-2 border-[#ffe031] pb-1.5 font-display text-xl text-[#ffe031] uppercase first:mt-0"
        >
          {block.content}
        </h1>
      );
    }
    if (block.kind === 'h2') {
      return (
        <h2 key={blockIdx} className="mt-4 mb-1.5 font-display text-base text-[#00f0ff] uppercase">
          {block.content}
        </h2>
      );
    }
    if (block.kind === 'h3') {
      return (
        <h3 key={blockIdx} className="mt-3 mb-1 font-display text-sm text-[#ff2e88] uppercase">
          {block.content}
        </h3>
      );
    }
    if (block.kind === 'code') {
      return (
        <div key={blockIdx} className="my-3 border-2 border-black shadow-[4px_4px_0_#000]">
          {block.lang ? (
            <div className="border-b-2 border-black bg-[#ffe031] px-3 py-1 font-mono text-[10px] font-extrabold uppercase text-[#08080c]">
              {block.lang}
            </div>
          ) : null}
          <pre className="overflow-x-auto bg-[#050508] p-3 font-mono text-xs text-[#fff6de]">
            {block.content}
          </pre>
        </div>
      );
    }
    if (block.kind === 'list' && block.lines) {
      return (
        <ul key={blockIdx} className="my-2 list-disc space-y-1 pl-5 text-sm text-[#fff6de]/90">
          {block.lines.map((bullet, bulletIdx) => (
            <li key={bulletIdx}>{bullet}</li>
          ))}
        </ul>
      );
    }
    return (
      <p key={blockIdx} className="my-2 whitespace-pre-wrap text-sm leading-relaxed text-[#fff6de]/90">
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
    setDraftContent('# New Dossier\n\nWrite Markdown field intel here.\n');
    setEditorMode('create');
  };

  const handleStartEdit = () => {
    if (!activeDoc) return;
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
      onNotify('ok', `Filed dossier ${saved.relPath}.`);
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
    if (!activeDoc) return;
    try {
      await deleteDoc(activeDoc.id);
      onNotify('ok', `Shredded ${activeDoc.relPath}.`);
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
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 09"
        sticker="CODEX ARCHIVE"
        title="DOSSIER VAULT // SQLITE FTS5"
        subtitle="INSTANT BM25 FULL-TEXT SEARCH WITH HIGHLIGHTED SNIPPETS, COMIC DOSSIER READER & LIVE MARKDOWN EDITOR"
        tone="cream"
        actions={
          <>
            <MonoBadge tone="yellow">{docsList.length} DOSSIERS</MonoBadge>
            <button
              type="button"
              onClick={handleStartCreate}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-display text-xs text-[#08080c] uppercase"
            >
              <FilePlus2 className="h-4 w-4" />
              NEW DOSSIER
            </button>
          </>
        }
      />

      <SectionCard
        title="FULL-TEXT ARCHIVE CONSOLE"
        icon={<BookOpen className="h-4 w-4" />}
        sticker="FTS5 BM25"
        stickerTone="yellow"
      >
        <div className="relative mb-4">
          <Search className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-[#ffe031]" />
          <input
            type="search"
            value={searchQuery}
            onChange={(ev) => setSearchQuery(ev.target.value)}
            placeholder="Instant BM25 full-text query across titles, paths, and Markdown intel..."
            className="riot-input w-full py-2.5 pr-3 pl-10 font-mono text-xs"
          />
        </div>

        <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
          {/* Left Archive List */}
          <div className="max-h-[68vh] overflow-y-auto border-2 border-black bg-[#08080c] lg:col-span-4">
            {loadingList && docsList.length === 0 ? (
              <div className="p-4 font-mono text-xs text-[#a6a2b8]">Scanning FTS5 index...</div>
            ) : docsList.length === 0 ? (
              <div className="p-4 font-mono text-xs font-bold uppercase text-[#a6a2b8]">
                NO DOSSIERS MATCHED.
              </div>
            ) : (
              <div className="divide-y divide-[#252538]">
                {docsList.map((docEntry) => {
                  const isSelected = activeDoc?.id === docEntry.id && editorMode !== 'create';
                  return (
                    <button
                      key={docEntry.id}
                      type="button"
                      onClick={() => void handleOpenDoc(docEntry.id)}
                      className={`block w-full px-3.5 py-3 text-left transition-colors ${
                        isSelected
                          ? 'bg-[#ffe031] text-[#08080c]'
                          : 'text-[#fff6de] hover:bg-[#1b1b2b]'
                      }`}
                    >
                      <div className="flex items-start justify-between gap-2">
                        <span className="line-clamp-1 font-display text-xs uppercase">
                          {docEntry.title}
                        </span>
                        <MonoBadge tone={isSelected ? 'cream' : 'neutral'}>
                          {docEntry.category}
                        </MonoBadge>
                      </div>
                      <div
                        className={`mt-0.5 truncate font-mono text-[10px] font-bold ${
                          isSelected ? 'text-[#08080c]/75' : 'text-[#00f0ff]'
                        }`}
                      >
                        {docEntry.project} / {docEntry.relPath}
                      </div>
                      {docEntry.snippet ? (
                        <div
                          className={`mt-1.5 line-clamp-2 text-[11px] leading-snug ${
                            isSelected ? 'text-[#08080c]' : 'text-[#fff6de]/80'
                          }`}
                        >
                          {renderSnippetHighlights(docEntry.snippet)}
                        </div>
                      ) : null}
                    </button>
                  );
                })}
              </div>
            )}
          </div>

          {/* Right Dossier Reader / Editor */}
          <div className="min-h-[55vh] border-2 border-black bg-[#08080c] p-4 lg:col-span-8">
            {editorMode === 'create' || editorMode === 'edit' ? (
              <form onSubmit={handleSaveDoc} className="flex h-full flex-col gap-3">
                <div className="flex items-center justify-between border-b-2 border-black pb-2.5">
                  <ComicSticker tone="yellow">
                    {editorMode === 'create' ? 'NEW DOSSIER' : 'EDIT DOSSIER'}
                  </ComicSticker>
                  <div className="flex items-center gap-2">
                    <button
                      type="button"
                      onClick={() => setEditorMode('read')}
                      className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-3 py-1 font-mono text-[11px] font-extrabold text-[#fff6de]"
                    >
                      <X className="h-3 w-3" />
                      CANCEL
                    </button>
                    <button
                      type="submit"
                      disabled={savingDoc}
                      className="riot-btn inline-flex items-center gap-1 bg-[#a3ff12] px-3.5 py-1 font-display text-[11px] text-[#08080c] uppercase disabled:opacity-50"
                    >
                      <Save className="h-3 w-3" />
                      {savingDoc ? 'SAVING...' : 'FILE DOSSIER'}
                    </button>
                  </div>
                </div>

                <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-4">
                  <input
                    type="text"
                    value={draftProject}
                    onChange={(ev) => setDraftProject(ev.target.value)}
                    placeholder="project"
                    className="riot-input px-2.5 py-1.5 font-mono text-xs"
                  />
                  <input
                    type="text"
                    value={draftRelPath}
                    onChange={(ev) => setDraftRelPath(ev.target.value)}
                    placeholder="docs/guide.md"
                    className="riot-input px-2.5 py-1.5 font-mono text-xs sm:col-span-2"
                  />
                  <input
                    type="text"
                    value={draftCategory}
                    onChange={(ev) => setDraftCategory(ev.target.value)}
                    placeholder="category (auto)"
                    className="riot-input px-2.5 py-1.5 font-mono text-xs"
                  />
                </div>

                <input
                  type="text"
                  value={draftTitle}
                  onChange={(ev) => setDraftTitle(ev.target.value)}
                  placeholder="Dossier title (optional)"
                  className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
                />

                <textarea
                  rows={18}
                  value={draftContent}
                  onChange={(ev) => setDraftContent(ev.target.value)}
                  placeholder="# Title&#10;&#10;Markdown body..."
                  className="riot-input flex-1 p-3 font-mono text-xs leading-relaxed"
                />
              </form>
            ) : loadingDoc ? (
              <div className="font-mono text-xs text-[#a6a2b8]">Loading dossier...</div>
            ) : activeDoc ? (
              <div className="space-y-3">
                <div className="flex flex-wrap items-start justify-between gap-2 border-b-2 border-black pb-3">
                  <div>
                    <h3 className="font-display text-lg text-[#ffe031] uppercase">
                      {activeDoc.title}
                    </h3>
                    <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                      <MonoBadge tone="cyan">{activeDoc.project}</MonoBadge>
                      <MonoBadge tone="cream">{activeDoc.relPath}</MonoBadge>
                      <MonoBadge tone="neutral">{formatBytes(activeDoc.sizeBytes)}</MonoBadge>
                      <span className="font-mono text-[10px] font-bold text-[#a6a2b8]">
                        UPDATED {formatShortTime(activeDoc.updatedAt)}
                      </span>
                    </div>
                  </div>
                  <div className="flex items-center gap-1.5">
                    <button
                      type="button"
                      onClick={() => setRawView((prev) => !prev)}
                      className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#00f0ff]"
                    >
                      {rawView ? <Eye className="h-3 w-3" /> : <Code2 className="h-3 w-3" />}
                      {rawView ? 'READER' : 'RAW'}
                    </button>
                    <button
                      type="button"
                      onClick={handleStartEdit}
                      className="riot-btn inline-flex items-center gap-1 bg-[#ffe031] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#08080c]"
                    >
                      <Edit3 className="h-3 w-3" />
                      EDIT
                    </button>
                    <button
                      type="button"
                      onClick={() => void handleDeleteDoc()}
                      className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2.5 py-1 font-mono text-[11px] font-extrabold text-white"
                    >
                      <Trash2 className="h-3 w-3" />
                      DEL
                    </button>
                  </div>
                </div>

                <div className="max-h-[58vh] overflow-y-auto pr-1">
                  {rawView ? (
                    <pre className="whitespace-pre-wrap break-words border-2 border-black bg-[#13131d] p-3 font-mono text-xs leading-relaxed text-[#fff6de]">
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
              <div className="flex h-full min-h-[300px] flex-col items-center justify-center gap-3 text-center">
                <ComicSticker tone="cream">OPEN A DOSSIER</ComicSticker>
                <span className="font-mono text-xs text-[#a6a2b8]">
                  Pick any indexed document on the left to inspect or rewrite.
                </span>
              </div>
            )}
          </div>
        </div>
      </SectionCard>
    </div>
  );
}
