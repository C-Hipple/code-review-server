// GitHub pull request URLs: recognising the PR a tab is on, and building links
// back into GitHub's own pages. Shared by the content script, the service
// worker and the panel, so it stays dependency-free.

/** A pull request, as GitHub's URLs name it. */
export interface PullRef {
    owner: string;
    repo: string;
    number: number;
}

const GITHUB_ORIGIN = 'https://github.com';

// GitHub's own rules: a login is letters, digits and single hyphens; a repo
// name adds `.` and `_`. Matching them keeps `/orgs/...`-style pages and
// anything odd out without listing GitHub's reserved paths.
const OWNER_RE = /^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$/;
const REPO_RE = /^[A-Za-z0-9._-]+$/;
const NUMBER_RE = /^[1-9][0-9]*$/;

/** Whether `url` is a page on https://github.com (not gist., api. or another host). */
export function isGitHubUrl(url: string): boolean {
    const parsed = tryParse(url);
    return parsed !== null && parsed.origin === GITHUB_ORIGIN;
}

/**
 * The PR a github.com URL points at — `/:owner/:repo/pull/:number` plus any
 * tab under it (`/files`, `/commits`, `/checks`, ...), query or hash — or null
 * for every other page, including `.diff` / `.patch` views and the `/pulls` list.
 */
export function parsePullUrl(url: string): PullRef | null {
    const parsed = tryParse(url);
    if (!parsed || parsed.origin !== GITHUB_ORIGIN) return null;
    const [owner, repo, kind, number] = parsed.pathname.split('/').slice(1);
    if (kind !== 'pull' || !owner || !repo || !number) return null;
    if (!OWNER_RE.test(owner) || !REPO_RE.test(repo) || repo === '.' || repo === '..') {
        return null;
    }
    if (!NUMBER_RE.test(number)) return null;
    const n = Number(number);
    return Number.isSafeInteger(n) ? { owner, repo, number: n } : null;
}

/** Whether two refs name the same PR; GitHub treats owner and repo case-insensitively. */
export function samePull(a: PullRef | null, b: PullRef | null): boolean {
    if (!a || !b) return a === b;
    return (
        a.number === b.number &&
        a.owner.toLowerCase() === b.owner.toLowerCase() &&
        a.repo.toLowerCase() === b.repo.toLowerCase()
    );
}

/** `https://github.com/owner/repo/pull/N` */
export function pullUrl(pr: PullRef): string {
    return `${GITHUB_ORIGIN}/${encodeURIComponent(pr.owner)}/${encodeURIComponent(pr.repo)}/pull/${pr.number}`;
}

/** Lowercase hex SHA-256 of `text`'s UTF-8 bytes. */
export async function sha256Hex(text: string): Promise<string> {
    const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
    return Array.from(new Uint8Array(digest), b => b.toString(16).padStart(2, '0')).join('');
}

/**
 * The fragment GitHub gives a file in a PR's Files changed tab: `diff-` plus
 * the SHA-256 of its path (the new path, for a rename).
 */
export async function diffAnchor(path: string): Promise<string> {
    return `diff-${await sha256Hex(path)}`;
}

/** Link to a file's diff: `.../pull/N/files#diff-<sha256(path)>`. */
export async function diffFileUrl(pr: PullRef, path: string): Promise<string> {
    return `${pullUrl(pr)}/files#${await diffAnchor(path)}`;
}

/**
 * Link to one line of a file's diff: `...#diff-<sha256(path)>R<line>`, `R`
 * being the new side (where annotations point) and `L` the old one.
 */
export async function diffLineUrl(
    pr: PullRef,
    path: string,
    line: number,
    side: 'R' | 'L' = 'R'
): Promise<string> {
    return `${await diffFileUrl(pr, path)}${side}${line}`;
}

function tryParse(url: string): URL | null {
    try {
        return new URL(url);
    } catch {
        return null;
    }
}
