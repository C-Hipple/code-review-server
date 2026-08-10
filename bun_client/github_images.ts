/**
 * The bridge half of the GitHub image proxy.
 *
 * Comments and reviews embed screenshots as `<img>` tags pointing at
 * github.com/user-attachments or *.githubusercontent.com. Those URLs redirect
 * to a signed CDN URL, and on a private repository the redirect is only issued
 * to an authenticated request — which a browser loading the image cross-site
 * cannot make, since its github.com cookie is not sent. The bridge already has
 * `CRS_GITHUB_TOKEN`, so it fetches the bytes and hands them to the page.
 *
 * `frontend/src/github_images.ts` decides which URLs to send here; this module
 * is the trust boundary. It re-checks the requested URL and every redirect
 * target, so the endpoint can't be turned into an open proxy by a crafted
 * comment body.
 */

/** How many redirects to follow before giving up. */
export const MAX_REDIRECTS = 5;

/** True for the hosts GitHub serves attachments and repository content from. */
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
 * Validate the `?url=` parameter. Returns the parsed URL, or null when it is
 * missing, unparseable, not http(s), or not on a GitHub host.
 */
export function parseGitHubImageUrl(raw: string | null | undefined): URL | null {
    if (!raw) return null;
    let url: URL;
    try {
        url = new URL(raw);
    } catch {
        return null;
    }
    if (url.protocol !== 'https:' && url.protocol !== 'http:') return null;
    if (!isGitHubImageHost(url.hostname)) return null;
    return url;
}

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

/**
 * Fetch a GitHub-hosted image, following redirects by hand.
 *
 * Two reasons not to let `fetch` follow them: every hop has to be re-checked
 * against the host allowlist, and the token must be dropped after the first
 * one. GitHub's redirect target carries its own signature in the query string,
 * and rejects the request outright if an `Authorization` header rides along
 * with it.
 */
export async function fetchGitHubImage(
    target: URL,
    token: string,
    fetchImpl: FetchLike = fetch
): Promise<Response> {
    let current = target;
    let authorization = token ? `Bearer ${token}` : '';

    for (let hop = 0; hop <= MAX_REDIRECTS; hop++) {
        const headers: Record<string, string> = {
            Accept: 'image/*',
            'User-Agent': 'code-review-server',
        };
        if (authorization) headers.Authorization = authorization;

        const response = await fetchImpl(current.href, { redirect: 'manual', headers });
        if (response.status < 300 || response.status >= 400) return response;

        const location = response.headers.get('location');
        if (!location) return response;

        let next: URL;
        try {
            next = new URL(location, current);
        } catch {
            throw new Error(`unparseable redirect from ${current.hostname}`);
        }
        if (!isGitHubImageHost(next.hostname)) {
            throw new Error(`redirect off GitHub to ${next.hostname}`);
        }
        current = next;
        authorization = '';
    }

    throw new Error(`too many redirects fetching ${target.href}`);
}
