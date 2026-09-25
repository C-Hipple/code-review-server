import type { LocationLines, LspHover, LspLocation } from './lsp';

export function hasHoverContent(hover: LspHover | null | undefined): boolean {
    if (!hover || !hover.contents) return false;
    if (typeof hover.contents === 'string' || Array.isArray(hover.contents)) {
        return hover.contents.length > 0;
    }
    return !!hover.contents.value;
}

/** A definition or references answer as a list, or null when there's nothing in it. */
export function toLocationList(
    res: LspLocation | LspLocation[] | null | undefined
): LspLocation[] | null {
    if (!res) return null;
    const list = Array.isArray(res) ? res : [res];
    return list.length > 0 ? list : null;
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
