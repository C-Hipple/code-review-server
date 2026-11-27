import type { Comment } from './components/review/types';
import type { ParsedLine } from './diff_utils';
import { groupIntoThreads, summarizeThread } from './discussion_utils';

// Pure helpers behind the comment tray (components/review/CommentTray.tsx):
// every conversation on the PR in one list, grouped the way the diff reads —
// the PR-level conversation first, then each file in diff order — with the
// line each thread is on, and stepping from one thread to the next.

/** Which threads the tray lists. */
export type TrayFilter = 'all' | 'open' | 'resolved';

/** The line a thread is on, for the context strip at the top of its card. */
export interface TrayLine {
    /** The line's code, without the diff's +/-/space prefix. */
    text: string;
    kind: 'addition' | 'deletion' | 'code';
    /** Its number in the file: the base side's for a removed line, else the head's. */
    lineNo: number | null;
    /** L for the base side (a removed line), R for the head. */
    side: 'L' | 'R';
}

export interface TrayThread {
    rootId: string;
    /** The file the thread is on; '' for a PR-level conversation comment. */
    path: string;
    /** The root's position in the PR's full diff ('' when it has none). */
    position: string;
    /** Root first, then replies in posting order. */
    comments: Comment[];
    resolved: boolean;
    resolvedBy: string;
    /** The thread's lines are no longer in the current diff. */
    outdated: boolean;
    /** Holds a comment left here that hasn't gone out with a review yet. */
    pending: boolean;
    participants: string[];
    line: TrayLine | null;
}

export interface TrayGroup {
    /** Stable React key: 'conversation', or 'file:<path>'. */
    key: string;
    /** '' for the conversation group. */
    path: string;
    threads: TrayThread[];
}

export interface TrayCounts {
    all: number;
    open: number;
    resolved: number;
    pending: number;
}

export const CONVERSATION_KEY = 'conversation';

const HUNK_HEADER = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

/**
 * The last line of a review comment's `diff_hunk` — GitHub cuts the hunk off
 * at the line the comment is on — with its number in the file. Used for a
 * thread whose line the diff on screen no longer shows (an outdated one).
 */
export function hunkTail(diffHunk: string): TrayLine | null {
    if (!diffHunk) return null;
    const lines = diffHunk.split('\n');
    let oldNo: number | null = null;
    let newNo: number | null = null;
    let last: TrayLine | null = null;
    for (const raw of lines) {
        const header = HUNK_HEADER.exec(raw);
        if (header) {
            oldNo = Number(header[1]);
            newNo = Number(header[2]);
            continue;
        }
        if (raw.startsWith('\\')) continue; // "\ No newline at end of file"
        const prefix = raw[0];
        const text = raw.slice(1);
        if (prefix === '+') {
            last = { text, kind: 'addition', lineNo: newNo, side: 'R' };
            if (newNo !== null) newNo++;
        } else if (prefix === '-') {
            last = { text, kind: 'deletion', lineNo: oldNo, side: 'L' };
            if (oldNo !== null) oldNo++;
        } else {
            last = { text, kind: 'code', lineNo: newNo, side: 'R' };
            if (oldNo !== null) oldNo++;
            if (newNo !== null) newNo++;
        }
    }
    return last;
}

/** A diff row as a tray line; null for anything that isn't a line of code. */
function rowLine(row: ParsedLine): TrayLine | null {
    if (row.lineType !== 'addition' && row.lineType !== 'deletion' && row.lineType !== 'code') {
        return null;
    }
    const deletion = row.lineType === 'deletion';
    return {
        text: row.text.slice(1),
        kind: row.lineType,
        lineNo: (deletion ? row.oldLineNo : row.newLineNo) ?? null,
        side: deletion ? 'L' : 'R',
    };
}

const rowKey = (file: string, pos: number | string) => `${file}\u0000${pos}`;

const positionValue = (position: string): number => {
    const n = parseInt(position, 10);
    return Number.isNaN(n) ? 0 : n;
};

const timeValue = (iso: string): number => {
    const t = iso ? new Date(iso).getTime() : NaN;
    return Number.isNaN(t) ? Number.MAX_SAFE_INTEGER : t;
};

/**
 * Every conversation on the PR, grouped for the tray.
 *
 * `rows` are the PR's full diff, parsed: comment positions count its rows,
 * so that's where a thread's line is looked up. `fileOrder` is the order the
 * diff view shows its files in; files only outdated threads still mention
 * come after them, alphabetically.
 *
 * Within a file, threads follow the diff top to bottom, outdated ones (which
 * have no row any more) last, oldest first.
 */
export function buildTrayGroups(
    comments: Comment[],
    outdatedComments: Comment[],
    rows: ParsedLine[],
    fileOrder: string[]
): TrayGroup[] {
    // The server can list a comment under both; the tray shows it once.
    const seen = new Set<string>();
    const all: Comment[] = [];
    for (const c of [...comments, ...outdatedComments]) {
        if (seen.has(c.id)) continue;
        seen.add(c.id);
        all.push(c);
    }

    const rowsByPos = new Map<string, ParsedLine>();
    for (const row of rows) {
        if (row.file && row.pos !== null) rowsByPos.set(rowKey(row.file, row.pos), row);
    }

    const threads: TrayThread[] = groupIntoThreads(all).map(thread => {
        const root = thread[0];
        const summary = summarizeThread(thread);
        let line: TrayLine | null = null;
        if (root.path && !summary.outdated) {
            const row = rowsByPos.get(rowKey(root.path, positionValue(root.position)));
            line = row ? rowLine(row) : null;
        }
        if (!line && root.path) line = hunkTail(root.diff_hunk);
        return {
            rootId: root.id,
            path: root.path || '',
            position: root.position || '',
            comments: thread,
            resolved: summary.resolved,
            resolvedBy: summary.resolvedBy,
            outdated: summary.outdated,
            pending: thread.some(c => c.author === 'local'),
            participants: summary.participants,
            line,
        };
    });

    const byPath = new Map<string, TrayThread[]>();
    for (const t of threads) {
        const bucket = byPath.get(t.path);
        if (bucket) bucket.push(t);
        else byPath.set(t.path, [t]);
    }

    const groups: TrayGroup[] = [];
    const conversation = byPath.get('');
    if (conversation) {
        conversation.sort(
            (a, b) => timeValue(a.comments[0].created_at) - timeValue(b.comments[0].created_at)
        );
        groups.push({ key: CONVERSATION_KEY, path: '', threads: conversation });
        byPath.delete('');
    }

    const order = [
        ...fileOrder.filter(f => byPath.has(f)),
        ...[...byPath.keys()].filter(f => !fileOrder.includes(f)).sort(),
    ];
    for (const path of order) {
        const list = byPath.get(path)!;
        list.sort((a, b) => {
            if (a.outdated !== b.outdated) return a.outdated ? 1 : -1;
            if (!a.outdated) {
                const byPos = positionValue(a.position) - positionValue(b.position);
                if (byPos !== 0) return byPos;
            }
            return timeValue(a.comments[0].created_at) - timeValue(b.comments[0].created_at);
        });
        groups.push({ key: `file:${path}`, path, threads: list });
    }
    return groups;
}

export function matchesFilter(thread: TrayThread, filter: TrayFilter): boolean {
    if (filter === 'open') return !thread.resolved;
    if (filter === 'resolved') return thread.resolved;
    return true;
}

/** The groups with only the threads `filter` lets through; empty groups go. */
export function filterTrayGroups(groups: TrayGroup[], filter: TrayFilter): TrayGroup[] {
    if (filter === 'all') return groups;
    return groups
        .map(g => ({ ...g, threads: g.threads.filter(t => matchesFilter(t, filter)) }))
        .filter(g => g.threads.length > 0);
}

export function countTrayThreads(groups: TrayGroup[]): TrayCounts {
    const counts: TrayCounts = { all: 0, open: 0, resolved: 0, pending: 0 };
    for (const g of groups) {
        for (const t of g.threads) {
            counts.all++;
            if (t.resolved) counts.resolved++;
            else counts.open++;
            if (t.pending) counts.pending++;
        }
    }
    return counts;
}

/** Every thread's root id, in the order the tray lists them. */
export function trayOrder(groups: TrayGroup[]): string[] {
    return groups.flatMap(g => g.threads.map(t => t.rootId));
}

/**
 * The thread after (`step` 1) or before (-1) `current` in `order`, wrapping
 * round at either end. With nothing selected, or a selection the list no
 * longer holds, stepping forward starts at the top and back at the bottom.
 */
export function stepThread(order: string[], current: string | null, step: 1 | -1): string | null {
    if (order.length === 0) return null;
    const at = current === null ? -1 : order.indexOf(current);
    if (at === -1) return step === 1 ? order[0] : order[order.length - 1];
    return order[(at + step + order.length) % order.length];
}

/** Who a comment is from, as the tray shows it: a pending local comment is yours. */
export function displayAuthor(author: string): string {
    return author === 'local' ? 'You' : author || 'unknown';
}

/** One or two letters for an author's avatar. */
export function authorInitials(author: string): string {
    if (author === 'local') return 'You';
    const name = author.replace(/\[bot\]$/, '').replace(/^@/, '');
    const parts = name.split(/[-_.\s]+/).filter(Boolean);
    if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
    return (name[0] || '?').toUpperCase();
}

/** A stable hue for an author's avatar, so each person keeps one colour. */
export function authorHue(author: string): number {
    let h = 0;
    for (let i = 0; i < author.length; i++) h = (h * 31 + author.charCodeAt(i)) >>> 0;
    return h % 360;
}

/** The thread's first line of text, for a folded card's one-line summary. */
export function threadPreview(thread: TrayThread, max = 90): string {
    const body = (thread.comments[0]?.body || '')
        .replace(/<!--[\s\S]*?-->/g, '')
        .replace(/```[\s\S]*?```/g, ' ')
        .replace(/[#>*_`~]/g, '')
        .replace(/\s+/g, ' ')
        .trim();
    return body.length > max ? `${body.slice(0, max - 1).trimEnd()}…` : body;
}

/** Order of file names the diff view shows, as file headers come. */
export function fileOrderOf(rows: ParsedLine[]): string[] {
    const out: string[] = [];
    for (const row of rows) {
        if (row.lineType === 'file-header' && row.file && !out.includes(row.file)) {
            out.push(row.file);
        }
    }
    return out;
}

const TRAY_WIDTH_KEY = 'crs.commentTray.width';
export const TRAY_DEFAULT_WIDTH = 420;
export const TRAY_MIN_WIDTH = 320;

/** The tray width to use for `requested` on a window `viewport` wide. */
export function clampTrayWidth(requested: number, viewport: number): number {
    const max = Math.max(TRAY_MIN_WIDTH, Math.floor(viewport * 0.6));
    return Math.round(Math.min(Math.max(requested, TRAY_MIN_WIDTH), max));
}

/** The width the reviewer last dragged the tray to, else the default. */
export function loadTrayWidth(): number {
    try {
        const saved = Number(localStorage.getItem(TRAY_WIDTH_KEY));
        return Number.isFinite(saved) && saved > 0 ? saved : TRAY_DEFAULT_WIDTH;
    } catch {
        return TRAY_DEFAULT_WIDTH;
    }
}

export function saveTrayWidth(width: number): void {
    try {
        localStorage.setItem(TRAY_WIDTH_KEY, String(Math.round(width)));
    } catch {
        // A private window or blocked storage just forgets the width.
    }
}
