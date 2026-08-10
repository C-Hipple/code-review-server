import { describe, expect, test } from 'bun:test';
import { fetchGitHubImage, MAX_REDIRECTS, parseGitHubImageUrl } from './github_images';

describe('parseGitHubImageUrl', () => {
    test('accepts the hosts GitHub serves attachments from', () => {
        const accepted = [
            'https://github.com/user-attachments/assets/9b506449-0b9b-4c14-90f7-775beb9e68af',
            'https://www.github.com/user-attachments/assets/abc',
            'https://user-images.githubusercontent.com/1234/screenshot.png',
            'https://private-user-images.githubusercontent.com/1234/x.png?jwt=token',
            'https://raw.githubusercontent.com/owner/repo/main/docs/img/diagram.png',
            'https://githubusercontent.com/x.png',
        ];
        for (const href of accepted) {
            expect(parseGitHubImageUrl(href)?.href).toBe(href);
        }
    });

    test('rejects anything not on a GitHub host', () => {
        const rejected = [
            'https://example.com/pixel.png',
            // Suffix matching has to be on a dot boundary, or this passes.
            'https://githubusercontent.com.evil.test/x.png',
            'https://evil.test/?q=githubusercontent.com',
            'https://github.com.evil.test/x.png',
        ];
        for (const href of rejected) {
            expect(parseGitHubImageUrl(href)).toBeNull();
        }
    });

    test('rejects non-http protocols and unparseable input', () => {
        expect(parseGitHubImageUrl('file:///etc/passwd')).toBeNull();
        expect(parseGitHubImageUrl('data:image/png;base64,AAAA')).toBeNull();
        expect(parseGitHubImageUrl('/user-attachments/assets/abc')).toBeNull();
        expect(parseGitHubImageUrl('')).toBeNull();
        expect(parseGitHubImageUrl(null)).toBeNull();
    });

    test('matches hosts case-insensitively', () => {
        expect(parseGitHubImageUrl('https://GitHub.com/user-attachments/assets/a')).not.toBeNull();
    });
});

interface Call {
    url: string;
    authorization: string | undefined;
}

// A fetch stand-in that replays a scripted sequence of responses and records
// what it was called with.
function scriptedFetch(responses: Response[]): {
    calls: Call[];
    impl: (input: string, init?: RequestInit) => Promise<Response>;
} {
    const calls: Call[] = [];
    let i = 0;
    return {
        calls,
        impl: async (input, init) => {
            const headers = new Headers(init?.headers);
            calls.push({ url: input, authorization: headers.get('authorization') ?? undefined });
            const response = responses[i++];
            if (!response) throw new Error(`unexpected fetch of ${input}`);
            return response;
        },
    };
}

const redirectTo = (location: string) => new Response(null, { status: 302, headers: { location } });

const imageResponse = () =>
    new Response('png-bytes', { status: 200, headers: { 'content-type': 'image/png' } });

describe('fetchGitHubImage', () => {
    const attachment = new URL('https://github.com/user-attachments/assets/abc');

    test('sends the token on the first request', async () => {
        const { calls, impl } = scriptedFetch([imageResponse()]);
        const response = await fetchGitHubImage(attachment, 'ghp_token', impl);
        expect(response.status).toBe(200);
        expect(calls).toEqual([{ url: attachment.href, authorization: 'Bearer ghp_token' }]);
    });

    test('omits the header entirely when no token is configured', async () => {
        const { calls, impl } = scriptedFetch([imageResponse()]);
        await fetchGitHubImage(attachment, '', impl);
        expect(calls[0].authorization).toBeUndefined();
    });

    test('drops the token on the redirect to the signed CDN url', async () => {
        // GitHub rejects a request that carries both its own query signature
        // and an Authorization header, so following this hop with the token
        // attached fails where an anonymous request succeeds.
        const signed = 'https://private-user-images.githubusercontent.com/1/x.png?jwt=abc';
        const { calls, impl } = scriptedFetch([redirectTo(signed), imageResponse()]);
        const response = await fetchGitHubImage(attachment, 'ghp_token', impl);
        expect(response.status).toBe(200);
        expect(calls).toEqual([
            { url: attachment.href, authorization: 'Bearer ghp_token' },
            { url: signed, authorization: undefined },
        ]);
    });

    test('resolves a relative redirect against the current url', async () => {
        const { calls, impl } = scriptedFetch([redirectTo('/other/asset.png'), imageResponse()]);
        await fetchGitHubImage(attachment, '', impl);
        expect(calls[1].url).toBe('https://github.com/other/asset.png');
    });

    test('refuses to follow a redirect off GitHub', async () => {
        const { impl } = scriptedFetch([redirectTo('https://evil.test/pixel.png')]);
        await expect(fetchGitHubImage(attachment, 'ghp_token', impl)).rejects.toThrow(
            'redirect off GitHub to evil.test'
        );
    });

    test('gives up on a redirect loop', async () => {
        const loop = Array.from({ length: MAX_REDIRECTS + 1 }, () =>
            redirectTo('https://github.com/user-attachments/assets/abc')
        );
        const { impl } = scriptedFetch(loop);
        await expect(fetchGitHubImage(attachment, '', impl)).rejects.toThrow('too many redirects');
    });

    test('returns an error response rather than following it', async () => {
        const { impl } = scriptedFetch([new Response('nope', { status: 404 })]);
        const response = await fetchGitHubImage(attachment, '', impl);
        expect(response.status).toBe(404);
    });
});
