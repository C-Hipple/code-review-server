import type {
    LocationLines,
    LspDocumentSymbol,
    LspHover,
    LspLocation,
    LspLocationLink,
    LspSymbolInformation,
} from './lsp';

export function hasHoverContent(hover: LspHover | null | undefined): boolean {
    if (!hover || !hover.contents) return false;
    if (typeof hover.contents === 'string' || Array.isArray(hover.contents)) {
        return hover.contents.length > 0;
    }
    return !!hover.contents.value;
}

/**
 * A definition or references answer as a list, or null when there's nothing
 * in it. A LocationLink (rust-analyzer answers with them) becomes the
 * location of its target's name.
 */
export function toLocationList(
    res: LspLocation | LspLocationLink | (LspLocation | LspLocationLink)[] | null | undefined
): LspLocation[] | null {
    if (!res) return null;
    const list = (Array.isArray(res) ? res : [res]).flatMap((l): LspLocation[] => {
        if ('targetUri' in l) {
            const range = l.targetSelectionRange ?? l.targetRange;
            return l.targetUri && range ? [{ uri: l.targetUri, range }] : [];
        }
        return l?.uri && l.range ? [l] : [];
    });
    return list.length > 0 ? list : null;
}

/** A file:// URI's path, decoded. */
export function uriToPath(uri: string): string {
    const path = uri.replace(/^file:\/\//, '');
    try {
        return decodeURIComponent(path);
    } catch {
        return path;
    }
}

/** Whether `path` is `dir` or inside it. */
export function isUnder(dir: string, path: string): boolean {
    if (!dir) return false;
    const base = dir.endsWith('/') ? dir.slice(0, -1) : dir;
    return path === base || path.startsWith(`${base}/`);
}

/** `path` relative to `dir` when it's inside it, else unchanged (absolute). */
export function pathInCheckout(dir: string, path: string): string {
    if (!isUnder(dir, path)) return path;
    const base = dir.endsWith('/') ? dir.slice(0, -1) : dir;
    return path.slice(base.length + 1);
}

// LSP SymbolKind, as the short label the symbol list shows.
const SYMBOL_KINDS: Record<number, string> = {
    1: 'file',
    2: 'module',
    3: 'namespace',
    4: 'package',
    5: 'class',
    6: 'method',
    7: 'property',
    8: 'field',
    9: 'constructor',
    10: 'enum',
    11: 'interface',
    12: 'function',
    13: 'variable',
    14: 'constant',
    15: 'string',
    16: 'number',
    17: 'boolean',
    18: 'array',
    19: 'object',
    20: 'key',
    21: 'null',
    22: 'enum member',
    23: 'struct',
    24: 'event',
    25: 'operator',
    26: 'type parameter',
};

export function symbolKindName(kind: number): string {
    return SYMBOL_KINDS[kind] ?? 'symbol';
}

/** One row of a file's symbol outline. */
export interface OutlineSymbol {
    name: string;
    detail?: string;
    kind: number;
    /** 0-based position of the symbol's name. */
    line: number;
    character: number;
    /** Nesting: 0 for top-level declarations. */
    depth: number;
}

/**
 * A documentSymbol answer as an outline in file order: hierarchical symbols
 * flattened depth-first, flat ones (SymbolInformation) nested by their
 * ranges, since that's all a flat answer says about nesting.
 */
export function outlineSymbols(
    res: LspDocumentSymbol[] | LspSymbolInformation[] | null | undefined
): OutlineSymbol[] {
    if (!Array.isArray(res) || res.length === 0) return [];
    const out: OutlineSymbol[] = [];
    if ('location' in res[0]) {
        const flat = [...(res as LspSymbolInformation[])].sort(
            (a, b) =>
                a.location.range.start.line - b.location.range.start.line ||
                a.location.range.start.character - b.location.range.start.character
        );
        const open: LspLocation['range'][] = [];
        for (const sym of flat) {
            const { start, end } = sym.location.range;
            while (open.length && open[open.length - 1].end.line < start.line) open.pop();
            out.push({
                name: sym.name,
                detail: sym.containerName,
                kind: sym.kind,
                line: start.line,
                character: start.character,
                depth: open.length,
            });
            if (end.line > start.line) open.push(sym.location.range);
        }
        return out;
    }
    const walk = (symbols: LspDocumentSymbol[], depth: number) => {
        const sorted = [...symbols].sort(
            (a, b) =>
                a.range.start.line - b.range.start.line ||
                a.range.start.character - b.range.start.character
        );
        for (const sym of sorted) {
            const at = (sym.selectionRange ?? sym.range).start;
            out.push({
                name: sym.name,
                detail: sym.detail,
                kind: sym.kind,
                line: at.line,
                character: at.character,
                depth,
            });
            if (sym.children?.length) walk(sym.children, depth + 1);
        }
    };
    walk(res as LspDocumentSymbol[], 0);
    return out;
}

/** The outline rows whose name contains `query`, ignoring case. */
export function filterOutline(symbols: OutlineSymbol[], query: string): OutlineSymbol[] {
    const q = query.trim().toLowerCase();
    if (!q) return symbols;
    return symbols.filter(s => s.name.toLowerCase().includes(q));
}

/** A work-done progress a server reported and hasn't ended. */
export interface LspProgress {
    title: string;
    message?: string;
    percentage?: number;
}

/**
 * Folds a `$/progress` notification into the running set, keyed by token.
 * Returns the same map when nothing changed.
 */
export function applyProgress(
    running: Map<string | number, LspProgress>,
    params: any
): Map<string | number, LspProgress> {
    const token = params?.token;
    const value = params?.value;
    if (token === undefined || !value || typeof value !== 'object') return running;
    const next = new Map(running);
    if (value.kind === 'begin') {
        next.set(token, {
            title: String(value.title ?? ''),
            message: value.message,
            percentage: value.percentage,
        });
    } else if (value.kind === 'report') {
        const current = next.get(token);
        if (!current) return running;
        next.set(token, {
            ...current,
            ...(value.message !== undefined ? { message: value.message } : {}),
            ...(value.percentage !== undefined ? { percentage: value.percentage } : {}),
        });
    } else if (value.kind === 'end') {
        if (!next.delete(token)) return running;
    } else {
        return running;
    }
    return next;
}

/**
 * What the server is busy with, e.g. "Indexing 40%", for its status badge:
 * the latest work begun that's still running. Null when it's idle.
 */
export function progressSummary(running: Map<string | number, LspProgress>): string | null {
    const all = [...running.values()];
    const latest = all[all.length - 1];
    if (!latest) return null;
    const parts = [latest.title || 'Working'];
    if (latest.message && latest.message !== latest.title) parts.push(latest.message);
    let text = parts.join(': ');
    if (typeof latest.percentage === 'number') text += ` ${Math.round(latest.percentage)}%`;
    return running.size > 1 ? `${text} (+${running.size - 1} more)` : text;
}

/** JSON-RPC errors an LSP server answers with when asking again later will do. */
export function isRetryableLspError(err: unknown): boolean {
    const code = (err as { code?: unknown } | null)?.code;
    // ContentModified (rust-analyzer while it indexes) and ServerCancelled.
    return code === -32801 || code === -32802;
}

/** Where the code viewer is: a file, relative to the checkout or absolute, and a 1-based line. */
export interface NavEntry {
    path: string;
    line: number | null;
}

/** The code viewer's back/forward history, like an editor's. */
export interface NavHistory {
    entries: NavEntry[];
    index: number;
}

const MAX_HISTORY = 100;

export function startHistory(entry: NavEntry): NavHistory {
    return { entries: [entry], index: 0 };
}

export function currentEntry(history: NavHistory): NavEntry {
    return history.entries[history.index];
}

/**
 * Moves to `to`, dropping whatever was forward of here. `fromLine` is where
 * the jump was made from, kept on the entry being left so Back returns to
 * that line rather than to wherever the file was first opened at.
 */
export function navigate(history: NavHistory, to: NavEntry, fromLine?: number | null): NavHistory {
    const here = currentEntry(history);
    const entries = history.entries.slice(0, history.index + 1);
    if (fromLine) entries[entries.length - 1] = { ...here, line: fromLine };
    const last = entries[entries.length - 1];
    if (last.path === to.path && last.line === to.line)
        return { entries, index: entries.length - 1 };
    entries.push(to);
    const dropped = Math.max(0, entries.length - MAX_HISTORY);
    return { entries: entries.slice(dropped), index: entries.length - 1 - dropped };
}

export function canGoBack(history: NavHistory): boolean {
    return history.index > 0;
}

export function canGoForward(history: NavHistory): boolean {
    return history.index < history.entries.length - 1;
}

export function goBack(history: NavHistory): NavHistory {
    return canGoBack(history) ? { ...history, index: history.index - 1 } : history;
}

export function goForward(history: NavHistory): NavHistory {
    return canGoForward(history) ? { ...history, index: history.index + 1 } : history;
}

/** The language server the code viewer is connected to. */
export interface LspSessionInfo {
    lang: string;
    root: string;
}

/**
 * Whether a file the viewer moves to belongs to the server it already has.
 * One in the same language does when it's under that server's root (a
 * nearer project marker, like a dependency's package.json in node_modules,
 * doesn't make it another project), or outside the checkout altogether: the
 * standard library or a dependency a definition led to, which only the
 * server it came from knows how to read.
 */
export function keepsLspSession(
    current: LspSessionInfo | null,
    next: { lang: string; outside: boolean },
    path: string
): boolean {
    if (!current || !next.lang || current.lang !== next.lang) return false;
    return next.outside || isUnder(current.root, path);
}

/** Both sets of line text, combined per file. */
export function mergeLocationLines(a: LocationLines, b: LocationLines): LocationLines {
    const merged = { ...a };
    for (const [uri, lines] of Object.entries(b)) {
        merged[uri] = { ...merged[uri], ...lines };
    }
    return merged;
}

/**
 * Locations grouped by file: files in the order the server listed them,
 * lines ascending within each file.
 */
export function groupLocationsByFile(
    locations: LspLocation[]
): { uri: string; locations: LspLocation[] }[] {
    const groups = new Map<string, LspLocation[]>();
    for (const location of locations) {
        const group = groups.get(location.uri);
        if (group) group.push(location);
        else groups.set(location.uri, [location]);
    }
    return [...groups].map(([uri, group]) => ({
        uri,
        locations: [...group].sort(
            (a, b) =>
                a.range.start.line - b.range.start.line ||
                a.range.start.character - b.range.start.character
        ),
    }));
}

/**
 * A location's path for display: relative to the most specific root it's
 * under (a PR's worktree over the repo it was made from), else absolute.
 */
export function displayPath(uri: string, roots: string[]): string {
    let path = uri.replace(/^file:\/\//, '');
    try {
        path = decodeURIComponent(path);
    } catch {
        // Not percent-encoded after all; show it as it came.
    }
    const root = roots
        .filter(r => r && path.startsWith(r.endsWith('/') ? r : `${r}/`))
        .sort((a, b) => b.length - a.length)[0];
    return root ? path.slice(root.replace(/\/$/, '').length + 1) : path;
}

/**
 * A location's line split around the range it points at, so the symbol can
 * be highlighted. Leading indentation is dropped: the line is shown on its own.
 */
export function splitLocationLine(
    text: string,
    range: LspLocation['range']
): { before: string; match: string; after: string } {
    const indent = text.length - text.trimStart().length;
    const start = Math.max(range.start.character, indent);
    const end =
        range.end.line === range.start.line ? Math.max(start, range.end.character) : text.length;
    return {
        before: text.slice(indent, start),
        match: text.slice(start, end),
        after: text.slice(end).trimEnd(),
    };
}
