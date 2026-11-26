import type { ReactNode } from 'react';
import { useState } from 'react';
import { Check, Copy, AlertCircle, CheckCircle2, Info, X } from 'lucide-react';
import { copyTextToClipboard } from '../utils';

export type BadgeTone = 'yellow' | 'magenta' | 'cyan' | 'ok' | 'err' | 'neutral';

export interface ToastNotice {
  id: number;
  tone: 'ok' | 'err' | 'info';
  message: string;
}

const BADGE_CLASSES: Record<BadgeTone, string> = {
  yellow: 'bg-[#ffe031]/15 text-[#ffe031] border-[#ffe031]/40',
  magenta: 'bg-[#ff2e88]/15 text-[#ff2e88] border-[#ff2e88]/40',
  cyan: 'bg-[#00b8f0]/15 text-[#00b8f0] border-[#00b8f0]/40',
  ok: 'bg-[#22c55e]/15 text-[#22c55e] border-[#22c55e]/40',
  err: 'bg-[#ef4444]/15 text-[#ef4444] border-[#ef4444]/40',
  neutral: 'bg-[#1c1c23] text-[#9c988c] border-[#2a2a36]',
};

export function MonoBadge({
  children,
  tone = 'neutral',
  className = '',
}: {
  children: ReactNode;
  tone?: BadgeTone;
  className?: string;
}) {
  return (
    <span
      className={`inline-flex items-center gap-1 rounded border px-2 py-0.5 font-mono text-[11px] font-semibold tracking-tight ${BADGE_CLASSES[tone]} ${className}`}
    >
      {children}
    </span>
  );
}

const ACCENT_BAR_CLASSES: Record<BadgeTone, string> = {
  yellow: 'bg-[#ffe031]',
  magenta: 'bg-[#ff2e88]',
  cyan: 'bg-[#00b8f0]',
  ok: 'bg-[#22c55e]',
  err: 'bg-[#ef4444]',
  neutral: 'bg-[#9c988c]',
};

const VALUE_COLOR_CLASSES: Record<BadgeTone, string> = {
  yellow: 'text-[#ffe031]',
  magenta: 'text-[#ff2e88]',
  cyan: 'text-[#00b8f0]',
  ok: 'text-[#22c55e]',
  err: 'text-[#ef4444]',
  neutral: 'text-[#f3efe4]',
};

export function StatTile({
  label,
  value,
  sublabel,
  icon,
  tone = 'yellow',
}: {
  label: string;
  value: string | number;
  sublabel?: string;
  icon: ReactNode;
  tone?: BadgeTone;
}) {
  return (
    <div className="relative overflow-hidden rounded-lg border border-[#2a2a36] bg-[#15151a] p-4 transition-colors hover:border-[#3d3d4f]">
      <div className={`absolute inset-x-0 top-0 h-[2px] ${ACCENT_BAR_CLASSES[tone]}`} />
      <div className="flex items-start justify-between gap-2">
        <span className="font-mono text-[11px] font-semibold uppercase tracking-wider text-[#9c988c]">
          {label}
        </span>
        <span className="text-[#9c988c]">{icon}</span>
      </div>
      <div className={`mt-2 font-mono text-2xl font-extrabold tracking-tight ${VALUE_COLOR_CLASSES[tone]}`}>
        {value}
      </div>
      {sublabel ? (
        <div className="mt-1 truncate font-mono text-[11px] text-[#9c988c]">{sublabel}</div>
      ) : null}
    </div>
  );
}

export function SectionCard({
  title,
  subtitle,
  icon,
  actions,
  children,
  className = '',
}: {
  title: string;
  subtitle?: string;
  icon?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`rounded-lg border border-[#2a2a36] bg-[#15151a] ${className}`}>
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[#2a2a36] px-4 py-3">
        <div className="flex items-center gap-2.5">
          {icon ? <span className="text-[#ffe031]">{icon}</span> : null}
          <div>
            <h2 className="text-sm font-bold tracking-wide text-[#f3efe4]">{title}</h2>
            {subtitle ? <p className="text-xs text-[#9c988c]">{subtitle}</p> : null}
          </div>
        </div>
        {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
      </div>
      <div className="p-4">{children}</div>
    </section>
  );
}

export function CopyableSnippet({
  label,
  value,
  tone = 'cyan',
}: {
  label: string;
  value: string;
  tone?: BadgeTone;
}) {
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    const succeeded = await copyTextToClipboard(value);
    if (succeeded) {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1800);
    }
  };

  return (
    <div className="flex items-center justify-between gap-2 rounded-md border border-[#2a2a36] bg-[#0d0d10] px-3 py-2">
      <div className="min-w-0 flex-1">
        <div className="mb-0.5 flex items-center gap-2">
          <MonoBadge tone={tone}>{label}</MonoBadge>
        </div>
        <code className="block truncate font-mono text-xs text-[#f3efe4]">{value}</code>
      </div>
      <button
        type="button"
        onClick={handleCopy}
        className="inline-flex shrink-0 items-center gap-1.5 rounded border border-[#2a2a36] bg-[#1c1c23] px-2.5 py-1.5 font-mono text-[11px] font-semibold text-[#f3efe4] transition-colors hover:border-[#ffe031] hover:text-[#ffe031]"
      >
        {copied ? <Check className="h-3.5 w-3.5 text-[#22c55e]" /> : <Copy className="h-3.5 w-3.5" />}
        {copied ? 'COPIED' : 'COPY'}
      </button>
    </div>
  );
}

export function ToastStack({
  notices,
  onDismiss,
}: {
  notices: ToastNotice[];
  onDismiss: (noticeId: number) => void;
}) {
  if (notices.length === 0) {
    return null;
  }
  return (
    <div className="fixed right-4 bottom-4 z-50 flex max-w-md flex-col gap-2">
      {notices.map((notice) => {
        const borderClass =
          notice.tone === 'err'
            ? 'border-[#ef4444]'
            : notice.tone === 'ok'
              ? 'border-[#22c55e]'
              : 'border-[#ffe031]';
        const iconEl =
          notice.tone === 'err' ? (
            <AlertCircle className="h-4 w-4 shrink-0 text-[#ef4444]" />
          ) : notice.tone === 'ok' ? (
            <CheckCircle2 className="h-4 w-4 shrink-0 text-[#22c55e]" />
          ) : (
            <Info className="h-4 w-4 shrink-0 text-[#ffe031]" />
          );

        return (
          <div
            key={notice.id}
            className={`flex items-start gap-2.5 rounded-lg border ${borderClass} bg-[#15151a]/95 px-3.5 py-2.5 text-xs text-[#f3efe4] shadow-xl backdrop-blur`}
          >
            {iconEl}
            <span className="flex-1 leading-relaxed">{notice.message}</span>
            <button
              type="button"
              onClick={() => onDismiss(notice.id)}
              className="text-[#9c988c] hover:text-[#f3efe4]"
              aria-label="Dismiss notification"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>
        );
      })}
    </div>
  );
}
