/**
 * URL checking for the bridge's image route.
 *
 * The downloading itself belongs to the Go server: it holds `CRS_GITHUB_TOKEN`,
 * which a private repository's attachments require, and caching there means the
 * Emacs client and anything else render the same images rather than each client
 * reimplementing the fetch. The bridge asks for one over `RPCHandler.GetImage`
 * and serves the file that comes back.
 *
 * This check is only a fast rejection so an obviously wrong URL doesn't cost a
 * round trip; the Go server validates again, and it is the one that matters.
 * `frontend/src/github_images.ts` decides which URLs get routed here at all.
 */

/** The reply shape of the Go server's RPCHandler.GetImage. */
export interface GetImageReply {
    okay: boolean;
    url: string;
    path: string;
    content_type: string;
    size: number;
    cached: boolean;
    error: string;
}

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
