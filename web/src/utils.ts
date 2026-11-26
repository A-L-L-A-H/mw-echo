export function formatBytes(byteCount: number): string {
  const safeBytes = Number.isFinite(byteCount) && byteCount > 0 ? byteCount : 0;
  if (safeBytes < 1024) {
    return `${Math.round(safeBytes)} B`;
  }
  const units = ['KB', 'MB', 'GB', 'TB'];
  let scaled = safeBytes / 1024;
  let unitIndex = 0;
  while (scaled >= 1024 && unitIndex < units.length - 1) {
    scaled /= 1024;
    unitIndex += 1;
  }
  return `${scaled.toFixed(scaled >= 100 ? 1 : 2)} ${units[unitIndex]}`;
}

export function formatCompactNumber(value: number): string {
  const safeVal = Number.isFinite(value) ? value : 0;
  if (safeVal >= 1_000_000) {
    return `${(safeVal / 1_000_000).toFixed(2)}M`;
  }
  if (safeVal >= 1_000) {
    return `${(safeVal / 1_000).toFixed(1)}K`;
  }
  return String(Math.round(safeVal));
}

export function formatShortTime(isoTimestamp?: string): string {
  if (!isoTimestamp) {
    return '—';
  }
  const parsed = new Date(isoTimestamp);
  if (Number.isNaN(parsed.getTime())) {
    return isoTimestamp;
  }
  return parsed.toLocaleString(undefined, {
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}

export function computeUsagePercent(usedBytes: number, quotaBytes: number): number {
  if (!Number.isFinite(quotaBytes) || quotaBytes <= 0) {
    return 0;
  }
  const ratio = (Math.max(0, usedBytes) / quotaBytes) * 100;
  return Math.min(100, Math.max(0, Math.round(ratio * 10) / 10));
}

export function isCooldownActive(cooldownUntil?: string): boolean {
  if (!cooldownUntil) {
    return false;
  }
  const expiresMs = Date.parse(cooldownUntil);
  return !Number.isNaN(expiresMs) && expiresMs > Date.now();
}

export function formatCooldownRemaining(cooldownUntil?: string): string {
  if (!cooldownUntil) {
    return '';
  }
  const remainingSec = Math.ceil((Date.parse(cooldownUntil) - Date.now()) / 1000);
  if (Number.isNaN(remainingSec) || remainingSec <= 0) {
    return '';
  }
  if (remainingSec < 60) {
    return `${remainingSec}s`;
  }
  const minutes = Math.floor(remainingSec / 60);
  const seconds = remainingSec % 60;
  return `${minutes}m ${seconds}s`;
}

export async function copyTextToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const textarea = document.createElement('textarea');
    textarea.value = text;
    textarea.style.position = 'fixed';
    textarea.style.opacity = '0';
    document.body.appendChild(textarea);
    textarea.select();
    const copied = document.execCommand('copy');
    document.body.removeChild(textarea);
    return copied;
  }
}
