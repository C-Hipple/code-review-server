/**
 * The bridge's image route, GET /api/github-image.
 *
 * The downloading itself belongs to the Go server: it holds `CRS_GITHUB_TOKEN`,
 * which a private repository's attachments require, and caching there means the
 * Emacs client and anything else render the same images rather than each client
 * reimplementing the fetch. The bridge asks for one over `RPCHandler.GetImage`
 * and serves the file that comes back — or, connected to a shared server whose
 * disk it cannot see, the bytes the server sends instead.
 *
 * The URL check is only a fast rejection so an obviously wrong URL doesn't cost
 * a round trip; the Go server validates again, and it is the one that matters.
 * `frontend/src/github_images.ts` decides which URLs get routed here at all.
 */

/** The reply shape of the Go server's RPCHandler.GetImage. */
export interface GetImageReply {
    okay: boolean;
    url: string;
    /** Where the server cached it, on the server's machine. */
    path: string;
    content_type: string;
    size: number;
    cached: boolean;
    error: string;
    /** The bytes, base64, when the call set IncludeData. */
    data?: string;
}

/** The bridge's RPC call: resolves with the Go reply, rejects with the Go error. */
export type RpcCall = (method: string, params: unknown[]) => Promise<unknown>;

/**
 * The response to GET /api/github-image?url=`raw`: the image the Go server
 * cached for it.
 *
 * Spawning crs locally, the bridge serves the file the server names. Remote
 * (CRS_REMOTE), that path is on the server's machine, so it asks for the bytes
 * (`IncludeData`) and never opens a path at all — a file of the same name here
 * would be some other file.
 */
export async function githubImageResponse(
    call: RpcCall,
    raw: string | null,
    remote: boolean,
    headers: Record<string, string> = {},
    logError: (message: string) => void = console.error
): Promise<Response> {
    const target = parseGitHubImageUrl(raw);
    if (!target) {
        return new Response('Not a GitHub image URL', { status: 400, headers });
    }
    const unavailable = (why: string) => {
        logError(`[Image] ${why} for ${target.href}`);
        return new Response('Image unavailable', { status: 502, headers });
    };
    let image: GetImageReply;
    try {
        image = (await call('RPCHandler.GetImage', [
            remote ? { URL: target.href, IncludeData: true } : { URL: target.href },
        ])) as GetImageReply;
    } catch (err) {
        return unavailable(`could not load it: ${err instanceof Error ? err.message : err}`);
    }
    if (!image?.okay) return unavailable(image?.error || 'not served');

    let body: Blob | Buffer;
    if (remote) {
        if (!image.data) return unavailable('no bytes in the reply');
        body = Buffer.from(image.data, 'base64');
    } else {
        if (!image.path) return unavailable('no path');
        const file = Bun.file(image.path);
        if (!(await file.exists())) return unavailable(`cached file is gone: ${image.path}`);
        body = file;
    }
    return new Response(body, {
        headers: {
            ...headers,
            'Content-Type': image.content_type || 'application/octet-stream',
            // The same screenshot re-renders on every keystroke in a reply box.
            'Cache-Control': 'private, max-age=600',
        },
    });
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
