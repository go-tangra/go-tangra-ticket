/**
 * Shared date/time display helpers for federated Tangra modules.
 *
 * These modules are Module Federation remotes built separately from the host,
 * so they cannot share the host's `@vben/utils` formatter state. Instead the
 * host publishes the user's resolved time-display preference (timezone +
 * 12h/24h) on a well-known `window` global, and these helpers read it. Uses the
 * native `Intl` API so no extra dependency (dayjs, etc.) is required.
 *
 * IMPORTANT: use these ONLY for display. Never use them to build values sent to
 * the backend — serialization must stay in UTC via `new Date(x).toISOString()`.
 */

const TIME_CONFIG_GLOBAL = '__TANGRA_TIME_CONFIG__';

interface TimeDisplayConfig {
  timeFormat: '12h' | '24h';
  timezone: string;
}

function readConfig(): TimeDisplayConfig {
  const raw = (globalThis as Record<string, any>)[TIME_CONFIG_GLOBAL] as
    | Partial<TimeDisplayConfig>
    | undefined;
  return {
    timeFormat: raw?.timeFormat === '12h' ? '12h' : '24h',
    timezone: typeof raw?.timezone === 'string' ? raw.timezone : '',
  };
}

function toDate(value: Date | number | string): Date | null {
  if (value === null || value === undefined || value === '') {
    return null;
  }
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

/** Format a timestamp as a localized date-time honouring the user preference. */
export function formatDateTime(value: Date | number | string): string {
  const date = toDate(value);
  if (!date) {
    return '';
  }
  const { timeFormat, timezone } = readConfig();
  try {
    const parts = new Intl.DateTimeFormat('en-CA', {
      timeZone: timezone || undefined,
      hour12: timeFormat === '12h',
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).format(date);
    // en-CA yields "YYYY-MM-DD, HH:mm:ss"; drop the comma for a clean look.
    return parts.replace(',', '');
  } catch {
    return date.toLocaleString();
  }
}

/** Format a timestamp as a date only (YYYY-MM-DD) honouring the timezone. */
export function formatDate(value: Date | number | string): string {
  const date = toDate(value);
  if (!date) {
    return '';
  }
  const { timezone } = readConfig();
  try {
    return new Intl.DateTimeFormat('en-CA', {
      timeZone: timezone || undefined,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
    }).format(date);
  } catch {
    return date.toLocaleDateString();
  }
}
