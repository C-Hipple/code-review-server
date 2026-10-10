// Times as the panel shows them: "5 min ago", with the full date on hover.

const MINUTE = 60;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

function plural(n: number, unit: string): string {
    return `${n} ${unit}${n === 1 ? '' : 's'} ago`;
}

/**
 * How long before `now` (ms since the epoch) the RFC 3339 time `iso` was,
 * in words; empty when `iso` is empty or unreadable. A time slightly in the
 * future (clock skew) reads as "just now".
 */
export function relativeTime(iso: string, now: number): string {
    if (!iso) return '';
    const then = Date.parse(iso);
    if (!Number.isFinite(then)) return '';
    const seconds = Math.floor((now - then) / 1000);
    if (seconds < 45) return 'just now';
    if (seconds < HOUR) return `${Math.max(1, Math.floor(seconds / MINUTE))} min ago`;
    if (seconds < DAY) return plural(Math.floor(seconds / HOUR), 'hour');
    const days = Math.floor(seconds / DAY);
    if (days === 1) return 'yesterday';
    if (days < 30) return `${days} days ago`;
    if (days < 365) return plural(Math.floor(days / 30), 'month');
    return plural(Math.floor(days / 365), 'year');
}

/** The full local date and time, for a tooltip; empty when unreadable. */
export function absoluteTime(iso: string): string {
    const then = Date.parse(iso);
    return Number.isFinite(then) ? new Date(then).toLocaleString() : '';
}
