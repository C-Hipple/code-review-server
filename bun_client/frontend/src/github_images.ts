import { API_BASE } from './api';

/**
 * Rewrites GitHub-hosted image URLs to run through the bridge's image proxy.
 *
 * A screenshot pasted into a comment lands in the body as
 * `https://github.com/user-attachments/assets/<uuid>`, which redirects to a
 * signed CDN URL. On a private repository GitHub only issues that redirect to
 * an authenticated request, and the browser's github.com session cookie is not
 * sent on a cross-site image load — so the image renders broken no matter who
 * is looking at it. The bridge holds `CRS_GITHUB_TOKEN`, so it fetches the
 * bytes instead (see `bun_client/github_images.ts`, which re-checks every URL
 * this module hands it).
 *
 * Anything not on a GitHub host is left alone and loaded directly.
 */

const PROXY_PATH = '/api/github-image';

/**
 * True for the hosts GitHub serves attachments and repository content from.
 * Kept in sync with the bridge's copy in `bun_client/github_images.ts`, which
 * is the one that actually enforces it.
 */
export function isGitHubImageHost(host: string): boolean {
    const h = host.toLowerCase();
    return (
        h === 'github.com' ||
        h === 'www.github.com' ||
        h === 'githubusercontent.com' ||
        h.endsWith('.githubusercontent.com')
    );
}

/**
 * The URL an `<img>` in a GitHub body should actually load: the proxy for
 * GitHub-hosted images, the original for everything else (including relative
 * and malformed URLs, which we have no repository context to resolve).
 */
export function proxyImageUrl(src: string): string {
    if (!src) return src;
    let url: URL;
    try {
        url = new URL(src);
    } catch {
        return src;
    }
    if (url.protocol !== 'https:' && url.protocol !== 'http:') return src;
    if (!isGitHubImageHost(url.hostname)) return src;
    return `${API_BASE}${PROXY_PATH}?url=${encodeURIComponent(url.href)}`;
}

/**
 * Same rewrite across a `srcset`, for the `<picture>` markup GitHub emits for
 * light/dark image pairs. Each candidate is `<url> [descriptor]`.
 */
export function proxySrcSet(srcSet: string): string {
    return srcSet
        .split(',')
        .map(candidate => candidate.trim())
        .filter(candidate => candidate.length > 0)
        .map(candidate => {
            const [url, ...descriptor] = candidate.split(/\s+/);
            return [proxyImageUrl(url), ...descriptor].join(' ');
        })
        .join(', ');
}
