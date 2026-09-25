// Source text for the locations an LSP answer points at, so the web client can
// show what is on each referenced line instead of only a file and line number.

import { fileURLToPath } from 'node:url';

/**
 * The text of each requested line, as `{ [uri]: { [line]: text } }` with
 * 0-based lines, for showing what's on the lines an LSP location points at.
 * Only file:// URIs are read; unreadable files are left out. Lines are
 * truncated, since the client shows one row per location.
 */
export async function readLocationLines(
    locations: { uri: string; line: number }[],
    maxLineLength = 300
): Promise<Record<string, Record<number, string>>> {
    const wanted = new Map<string, Set<number>>();
    for (const { uri, line } of locations) {
        if (typeof uri !== 'string' || !uri.startsWith('file://')) continue;
        if (!Number.isInteger(line) || line < 0) continue;
        if (!wanted.has(uri)) wanted.set(uri, new Set());
        wanted.get(uri)?.add(line);
    }

    const result: Record<string, Record<number, string>> = {};
    await Promise.all(
        [...wanted].map(async ([uri, lines]) => {
            let text: string;
            try {
                text = await Bun.file(fileURLToPath(uri)).text();
            } catch {
                return;
            }
            const fileLines = text.split('\n');
            const found: Record<number, string> = {};
            for (const line of lines) {
                if (line >= fileLines.length) continue;
                found[line] = fileLines[line].replace(/\r$/, '').slice(0, maxLineLength);
            }
            result[uri] = found;
        })
    );
    return result;
}
