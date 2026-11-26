import type { ReactNode } from 'react';
import { useState } from 'react';
import { AlertCircle, Check, CheckCircle2, Copy, Info, X } from 'lucide-react';
import { copyTextToClipboard } from '../utils';

export type BadgeTone = 'yellow' | 'magenta' | 'cyan' | 'ok' | 'err' | 'cream' | 'neutral';

export interface ToastNotice {
  id: number;
  tone: 'ok' | 'err' | 'info';
  message: string;
}

const BADGE_CLASSES: Record<BadgeTone, string> = {
  yellow: 'bg-[#ffe031] text-[#08080c] border-black shadow-[2px_2px_0_#000]',
  magenta: 'bg-[#ff2e88] text-white border-black shadow-[2px_2px_0_#000]',
  cyan: 'bg-[#00f0ff] text-[#08080c] border-black shadow-[2px_2px_0_#000]',
  ok: 'bg-[#a3ff12] text-[#08080c] border-black shadow-[2px_2px_0_#000]',
  err: 'bg-[#ff334b] text-white border-black shadow-[2px_2px_0_#000]',
  cream: 'bg-[#fff6de] text-[#08080c] border-black shadow-[2px_2px_0_#000]',
  neutral: 'bg-[#1b1b2b] text-[#fff6de] border-[#3a3a58] shadow-[2px_2px_0_#000]',
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
      className={`inline-flex items-center gap-1 border-2 px-2 py-0.5 font-mono text-[11px] font-extrabold uppercase tracking-tight ${BADGE_CLASSES[tone]} ${className}`}
    >
      {children}
    </span>
  );
}

export function ComicSticker({
  children,
  tone = 'cream',
  tilt = '-rotate-2',
}: {
  children: ReactNode;
  tone?: BadgeTone;
  tilt?: string;
}) {
  return (
    <span
      className={`inline-flex items-center gap-1 border-2 px-2.5 py-0.5 font-comic text-sm tracking-wider uppercase ${tilt} ${BADGE_CLASSES[tone]}`}
    >
      {children}
    </span>
  );
}

const STAT_SHADOW_CLASSES: Record<BadgeTone, string> = {
  yellow: 'shadow-[5px_5px_0_#ffe031]',
  magenta: 'shadow-[5px_5px_0_#ff2e88]',
  cyan: 'shadow-[5px_5px_0_#00f0ff]',
  ok: 'shadow-[5px_5px_0_#a3ff12]',
  err: 'shadow-[5px_5px_0_#ff334b]',
  cream: 'shadow-[5px_5px_0_#fff6de]',
  neutral: 'shadow-[5px_5px_0_#000000]',
};

const STAT_TOP_BAR_CLASSES: Record<BadgeTone, string> = {
  yellow: 'bg-[#ffe031] text-[#08080c]',
  magenta: 'bg-[#ff2e88] text-white',
  cyan: 'bg-[#00f0ff] text-[#08080c]',
  ok: 'bg-[#a3ff12] text-[#08080c]',
  err: 'bg-[#ff334b] text-white',
  cream: 'bg-[#fff6de] text-[#08080c]',
  neutral: 'bg-[#2a2a42] text-[#fff6de]',
};

const STAT_VALUE_CLASSES: Record<BadgeTone, string> = {
  yellow: 'text-[#ffe031]',
  magenta: 'text-[#ff2e88]',
  cyan: 'text-[#00f0ff]',
  ok: 'text-[#a3ff12]',
  err: 'text-[#ff334b]',
  cream: 'text-[#fff6de]',
  neutral: 'text-[#fff6de]',
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
    <div
      className={`group relative overflow-hidden border-[2.5px] border-black bg-[#13131d] transition-transform duration-100 hover:-translate-x-0.5 hover:-translate-y-0.5 ${STAT_SHADOW_CLASSES[tone]}`}
    >
      {/* Top comic label tape */}
      <div
        className={`flex items-center justify-between gap-2 border-b-2 border-black px-3 py-1 ${STAT_TOP_BAR_CLASSES[tone]}`}
      >
        <span className="truncate font-mono text-[10px] font-extrabold uppercase tracking-wider">
          {label}
        </span>
        <span className="shrink-0">{icon}</span>
      </div>

      {/* Body with subtle halftone corner */}
      <div className="relative px-3.5 py-3">
        <div className="riot-halftone-corner pointer-events-none absolute right-0 bottom-0 h-12 w-20 opacity-40" />
        <div
          className={`relative font-display text-2xl leading-none tracking-tight ${STAT_VALUE_CLASSES[tone]} riot-title-ink`}
        >
          {value}
        </div>
        {sublabel ? (
          <div className="relative mt-1.5 truncate font-mono text-[10px] font-bold uppercase text-[#a6a2b8]">
            {sublabel}
          </div>
        ) : null}
      </div>
    </div>
  );
}

const SECTION_ACCENT_BORDERS: Record<BadgeTone, string> = {
  yellow: 'shadow-[6px_6px_0_#ffe031]',
  magenta: 'shadow-[6px_6px_0_#ff2e88]',
  cyan: 'shadow-[6px_6px_0_#00f0ff]',
  ok: 'shadow-[6px_6px_0_#a3ff12]',
  err: 'shadow-[6px_6px_0_#ff334b]',
  cream: 'shadow-[6px_6px_0_#fff6de]',
  neutral: 'shadow-[6px_6px_0_#000000]',
};

export function StageHeroBanner({
  sceneCode,
  sticker,
  title,
  subtitle,
  tone = 'yellow',
  actions,
}: {
  sceneCode: string;
  sticker: string;
  title: string;
  subtitle: string;
  tone?: BadgeTone;
  actions?: ReactNode;
}) {
  return (
    <div className="relative overflow-hidden border-[2.5px] border-black bg-[#13131d] shadow-[7px_7px_0_#000]">
      <div className="riot-halftone-corner pointer-events-none absolute inset-y-0 right-0 w-1/3 opacity-45" />
      <div className="relative flex flex-wrap items-center justify-between gap-4 px-5 py-4">
        <div className="min-w-0">
          <div className="mb-1.5 flex flex-wrap items-center gap-2">
            <MonoBadge tone="cream">{sceneCode}</MonoBadge>
            <ComicSticker tone={tone} tilt="-rotate-2">
              {sticker}
            </ComicSticker>
          </div>
          <h1 className="font-display text-2xl tracking-tight text-[#fff6de] uppercase sm:text-3xl riot-title-rgb">
            {title}
          </h1>
          <p className="mt-1 font-mono text-xs font-medium text-[#a6a2b8]">{subtitle}</p>
        </div>
        {actions ? <div className="relative flex flex-wrap items-center gap-2">{actions}</div> : null}
      </div>
      <div className="riot-hazard-tape h-2 border-t-2 border-black" />
    </div>
  );
}

export function SectionCard({
  title,
  subtitle,
  icon,
  sticker,
  stickerTone = 'yellow',
  accent = 'neutral',
  actions,
  children,
  className = '',
}: {
  title: string;
  subtitle?: string;
  icon?: ReactNode;
  sticker?: string;
  stickerTone?: BadgeTone;
  accent?: BadgeTone;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section
      className={`relative border-[2.5px] border-black bg-[#13131d] ${SECTION_ACCENT_BORDERS[accent]} ${className}`}
    >
      {/* Tactical film frame crosshairs */}
      <span className="pointer-events-none absolute top-1 left-1 font-mono text-[9px] leading-none text-[#ffe031]/60">
        ┌
      </span>
      <span className="pointer-events-none absolute top-1 right-1 font-mono text-[9px] leading-none text-[#ffe031]/60">
        ┐
      </span>
      <span className="pointer-events-none absolute bottom-1 left-1 font-mono text-[9px] leading-none text-[#ff2e88]/60">
        └
      </span>
      <span className="pointer-events-none absolute right-1 bottom-1 font-mono text-[9px] leading-none text-[#ff2e88]/60">
        ┘
      </span>

      <div className="flex flex-wrap items-center justify-between gap-3 border-b-[2.5px] border-black bg-[#1b1b2b] px-4 py-3">
        <div className="flex items-center gap-3">
          {icon ? (
            <span className="flex h-8 w-8 shrink-0 items-center justify-center border-2 border-black bg-[#ffe031] text-[#08080c] shadow-[2px_2px_0_#000]">
              {icon}
            </span>
          ) : null}
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="font-display text-sm tracking-wide text-[#fff6de] uppercase sm:text-base">
                {title}
              </h2>
              {sticker ? (
                <ComicSticker tone={stickerTone} tilt="-rotate-2">
                  {sticker}
                </ComicSticker>
              ) : null}
            </div>
            {subtitle ? (
              <p className="mt-0.5 font-mono text-[11px] text-[#a6a2b8]">{subtitle}</p>
            ) : null}
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
    <div className="flex items-center justify-between gap-2 border-2 border-black bg-[#08080c] px-3 py-2 shadow-[3px_3px_0_#000]">
      <div className="min-w-0 flex-1">
        <div className="mb-1 flex items-center gap-2">
          <MonoBadge tone={tone}>{label}</MonoBadge>
        </div>
        <code className="block truncate font-mono text-xs font-bold text-[#fff6de]">{value}</code>
      </div>
      <button
        type="button"
        onClick={handleCopy}
        className="riot-btn inline-flex shrink-0 items-center gap-1.5 bg-[#fff6de] px-2.5 py-1.5 font-mono text-[11px] font-extrabold text-[#08080c] hover:bg-[#ffe031]"
      >
        {copied ? <Check className="h-3.5 w-3.5 text-[#08080c]" /> : <Copy className="h-3.5 w-3.5" />}
        {copied ? 'GOT IT!' : 'COPY'}
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
    <div className="fixed right-4 bottom-4 z-50 flex max-w-md flex-col gap-2.5">
      {notices.map((notice) => {
        const bgClass =
          notice.tone === 'err'
            ? 'bg-[#ff334b] text-white shadow-[5px_5px_0_#000]'
            : notice.tone === 'ok'
              ? 'bg-[#a3ff12] text-[#08080c] shadow-[5px_5px_0_#000]'
              : 'bg-[#ffe031] text-[#08080c] shadow-[5px_5px_0_#ff2e88]';
        const iconEl =
          notice.tone === 'err' ? (
            <AlertCircle className="h-4 w-4 shrink-0" />
          ) : notice.tone === 'ok' ? (
            <CheckCircle2 className="h-4 w-4 shrink-0" />
          ) : (
            <Info className="h-4 w-4 shrink-0" />
          );

        return (
          <div
            key={notice.id}
            className={`-rotate-1 flex items-start gap-2.5 border-[2.5px] border-black px-3.5 py-2.5 font-mono text-xs font-extrabold ${bgClass}`}
          >
            {iconEl}
            <span className="flex-1 leading-snug">{notice.message}</span>
            <button
              type="button"
              onClick={() => onDismiss(notice.id)}
              className="opacity-70 hover:opacity-100"
              aria-label="Dismiss notification"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
        );
      })}
    </div>
  );
}
