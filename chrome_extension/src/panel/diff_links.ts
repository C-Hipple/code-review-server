// Links into a PR's Files changed tab. GitHub anchors each file at
// `#diff-<sha256(path)>`, and crypto.subtle only hashes asynchronously, so
// anchors are computed once per path, kept in a module cache, and links fall
// back to the Files changed tab itself until their anchor is known.

import { useEffect, useState } from 'react';
import { diffAnchor, pullUrl, type PullRef } from '../github_url';

const anchors = new Map<string, string>();

/** The cached anchors for `paths` (those already computed). */
function cached(paths: readonly string[]): Map<string, string> {
    const out = new Map<string, string>();
    for (const p of paths) {
        const a = anchors.get(p);
        if (a) out.set(p, a);
    }
    return out;
}

/** Computes and caches the anchors for `paths`; resolves with all of them. */
export async function primeDiffAnchors(paths: readonly string[]): Promise<Map<string, string>> {
    await Promise.all(
        [...new Set(paths)]
            .filter(p => !anchors.has(p))
            .map(async p => {
                anchors.set(p, await diffAnchor(p));
            })
    );
    return cached(paths);
}

/** Link to a file's diff, or to the Files changed tab while its anchor is unknown. */
export function fileHref(pr: PullRef, path: string, known: ReadonlyMap<string, string>): string {
    const anchor = known.get(path);
    return `${pullUrl(pr)}/files${anchor ? `#${anchor}` : ''}`;
}

/** Link to a line on the new side of a file's diff (`…#diff-<sha>R<line>`). */
export function lineHref(
    pr: PullRef,
    path: string,
    line: number,
    known: ReadonlyMap<string, string>
): string {
    const anchor = known.get(path);
    if (!anchor) return `${pullUrl(pr)}/files`;
    return line > 0 ? `${pullUrl(pr)}/files#${anchor}R${line}` : `${pullUrl(pr)}/files#${anchor}`;
}

/**
 * The anchors for `paths`: what the cache already holds at once, the rest as
 * soon as they're hashed.
 */
export function useDiffAnchors(paths: readonly string[]): ReadonlyMap<string, string> {
    const key = paths.join('\n');
    const [state, setState] = useState(() => ({ key, map: cached(paths) }));

    useEffect(() => {
        let live = true;
        const list = key ? key.split('\n') : [];
        primeDiffAnchors(list).then(
            map => live && setState({ key, map }),
            () => {} // no crypto.subtle: links stay on the Files changed tab
        );
        return () => {
            live = false;
        };
    }, [key]);

    return state.key === key ? state.map : cached(paths);
}
