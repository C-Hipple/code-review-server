// Applying file-ordering to GitHub's Files changed view: the file tree on the
// left and the per-file diffs on the right, in the order the server stored for
// the PR.
//
// The page is GitHub's, and its markup isn't a contract, so everything here
// keys off the two things it has kept stable — a diff block per file
// (`div.file.js-file`, the path on the file's header) and the file tree's
// ActionList — and leaves whatever it doesn't recognise exactly where GitHub
// put it. Ranking and sorting are pure, so a page is only needed to move
// elements; a file the report doesn't name keeps GitHub's order, after the
// ones it does.

import { FILE_ORDERING, type AIFeatureOutput } from './types';

/** file-ordering's suggested order, or null when the output carries none. */
export function fileOrderingFiles(output: AIFeatureOutput | null | undefined): string[] | null {
    if (!output || output.feature !== FILE_ORDERING) return null;
    const report = output.report as { files?: unknown } | null;
    const files = report?.files;
    if (!Array.isArray(files)) return null;
    const paths = files.filter((f): f is string => typeof f === 'string' && f !== '');
    return paths.length > 0 ? paths : null;
}

/** Where each path sits in `files`. An unnamed path has no rank. */
export function pathRanks(files: readonly string[]): Map<string, number> {
    return new Map(files.map((path, index) => [path, index]));
}

/** A path's rank, or after everything ranked when the report doesn't name it. */
export function rankOf(ranks: ReadonlyMap<string, number>, path: string | null): number {
    return path === null ? Number.POSITIVE_INFINITY : (ranks.get(path) ?? Number.POSITIVE_INFINITY);
}

/** `items` by rank; equal ranks keep their order (Array#sort is stable). */
export function byRank<T>(items: readonly T[], rank: (item: T) => number): T[] {
    return items.slice().sort((a, b) => rank(a) - rank(b));
}

/**
 * Orders `items` among themselves in their parent, by `rank`, leaving every
 * other child where it is. Returns false when they are already in order: a
 * page that has been applied is then left untouched, which matters because
 * GitHub mutates its pages constantly and the user may be reading them.
 */
export function reorderInPlace<T extends Element>(
    items: readonly T[],
    rank: (item: T) => number
): boolean {
    const parent = items[0]?.parentElement;
    if (!parent || items.length < 2) return false;
    const sorted = byRank(items, rank);
    if (sorted.every((item, index) => item === items[index])) return false;
    // Each item is inserted in order in front of a marker that holds the first
    // one's place, so the block moves as a whole.
    const marker = document.createComment('crs-file-order');
    parent.insertBefore(marker, items[0]);
    for (const item of sorted) parent.insertBefore(item, marker);
    marker.remove();
    return true;
}

/** The class GitHub gives each file's diff block. */
const DIFF_FILE = 'div.file.js-file';
/** The element GitHub appends each batch of files to, as they load. */
const DIFF_LIST = '.js-diff-progressive-container';
/** The file tree's root list. */
const FILE_TREE = 'ul[aria-label="File Tree"]';

/** The path a diff block is for: on the block itself, or failing that its header. */
export function diffPath(block: Element): string | null {
    const header = block.matches('[data-path]') ? block : block.querySelector('[data-path]');
    return header?.getAttribute('data-path') ?? block.getAttribute('data-file-path');
}

/** Whether `el` is, or wraps, a file's diff block. */
function holdsDiff(el: Element): boolean {
    return el.matches(DIFF_FILE) || el.querySelector(DIFF_FILE) !== null;
}

/**
 * The page's diff blocks, grouped as GitHub holds them: one group per
 * progressive container, in the order the containers appear. GitHub loads the
 * files it hasn't got yet as the reader scrolls, appending each batch to its
 * own container, so the groups are the runs the page is made of.
 */
function diffGroups(root: Document): { container: Element; blocks: Element[] }[] {
    return Array.from(root.querySelectorAll(DIFF_LIST)).map(container => ({
        container,
        blocks: Array.from(container.children).filter(holdsDiff),
    }));
}

/** The path a tree entry stands for, or null when it names none. */
function treePath(root: Document, entry: Element): string | null {
    const href = entry.querySelector('a[href^="#diff-"]')?.getAttribute('href');
    const block = href ? root.getElementById(href.slice(1)) : null;
    const path = block ? diffPath(block) : null;
    if (path) return path;
    // The diff may not have loaded yet. GitHub writes the whole path in the
    // label; one without a slash is a name this can't place anywhere.
    const label = entry.querySelector('.ActionList-item-label')?.textContent?.trim() ?? '';
    return label.includes('/') ? label : null;
}

/** A tree entry's rank: a file's own, a directory's as its earliest file's. */
function treeRank(root: Document, entry: Element, rank: (path: string | null) => number): number {
    const sub = entry.querySelector(':scope > ul');
    if (sub === null) return rank(treePath(root, entry));
    let best = Number.POSITIVE_INFINITY;
    for (const child of Array.from(sub.children)) {
        if (child.tagName === 'LI') best = Math.min(best, treeRank(root, child, rank));
    }
    return best;
}

/**
 * Reorders GitHub's Files changed view to match `files` — the diff blocks
 * first, then every list the file tree is made of — and reports whether
 * anything moved. A page with neither is left alone.
 */
export function applyFileOrder(root: Document, files: readonly string[]): boolean {
    const ranks = pathRanks(files);
    const rank = (path: string | null) => rankOf(ranks, path);
    let moved = false;

    const groups = diffGroups(root).filter(group => group.blocks.length > 0);
    const blocks = groups.flatMap(group => group.blocks);
    if (blocks.length > 1) {
        const sorted = byRank(blocks, block => rank(diffPath(block)));
        if (!sorted.every((block, index) => block === blocks[index])) {
            // Every container keeps as many files as it had; the files
            // themselves follow the report, so the page reads top to bottom
            // whichever batch GitHub loaded them in.
            const markers = groups.map(group => {
                const marker = document.createComment('crs-file-order');
                group.container.insertBefore(marker, group.blocks[0]);
                return marker;
            });
            let next = 0;
            groups.forEach((group, index) => {
                for (let taken = 0; taken < group.blocks.length; taken++) {
                    group.container.insertBefore(sorted[next++], markers[index]);
                }
            });
            for (const marker of markers) marker.remove();
            moved = true;
        }
    }

    const tree = root.querySelector(FILE_TREE);
    if (tree) {
        // The root's list first, then the ones inside it: a directory's own
        // place follows the earliest file it holds.
        for (const list of [tree, ...Array.from(tree.querySelectorAll('ul'))]) {
            const entries = Array.from(list.children).filter(child => child.tagName === 'LI');
            moved = reorderInPlace(entries, entry => treeRank(root, entry, rank)) || moved;
        }
    }
    return moved;
}
