import { afterEach, describe, expect, test } from 'bun:test';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
    type GetImageReply,
    githubImageResponse,
    isGitHubImageHost,
    parseGitHubImageUrl,
} from './github_images';

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

describe('isGitHubImageHost', () => {
    test('only matches on a dot boundary', () => {
        expect(isGitHubImageHost('user-images.githubusercontent.com')).toBe(true);
        expect(isGitHubImageHost('githubusercontent.com.evil.test')).toBe(false);
        expect(isGitHubImageHost('notgithub.com')).toBe(false);
    });
});

describe('githubImageResponse', () => {
    const URL_ = 'https://github.com/user-attachments/assets/9b506449';
    const PNG = Buffer.from('89504e470d0a1a0a', 'hex');
    const dirs: string[] = [];
    afterEach(() => {
        for (const dir of dirs.splice(0)) rmSync(dir, { recursive: true, force: true });
    });

    /** A file standing in for the one the server cached. */
    function cachedFile(contents: Buffer | string) {
        const dir = mkdtempSync(join(tmpdir(), 'crs-image-test-'));
        dirs.push(dir);
        const path = join(dir, 'cached.png');
        writeFileSync(path, contents);
        return path;
    }

    /** A GetImage that answers `reply`, recording what it was asked. */
    function server(reply: Partial<GetImageReply> | Error) {
        const calls: { method: string; params: unknown[] }[] = [];
        const call = async (method: string, params: unknown[]) => {
            calls.push({ method, params });
            if (reply instanceof Error) throw reply;
            return { okay: true, url: URL_, cached: true, error: '', ...reply };
        };
        return { call, calls };
    }

    const quiet = () => {};
    const bytesOf = async (res: Response) => Buffer.from(await res.arrayBuffer());

    test('locally, serves the file the server cached', async () => {
        const path = cachedFile(PNG);
        const { call, calls } = server({ path, content_type: 'image/png', size: PNG.length });

        const res = await githubImageResponse(call, URL_, false, { 'X-Test': '1' }, quiet);

        expect(calls).toEqual([{ method: 'RPCHandler.GetImage', params: [{ URL: URL_ }] }]);
        expect(res.status).toBe(200);
        expect(res.headers.get('Content-Type')).toBe('image/png');
        expect(res.headers.get('X-Test')).toBe('1');
        expect(await bytesOf(res)).toEqual(PNG);
    });

    test('remote, asks for the bytes and serves them, never the path', async () => {
        // A file at the path the server names, here, is some other file.
        const path = cachedFile('not the image');
        const { call, calls } = server({
            path,
            content_type: 'image/png',
            size: PNG.length,
            data: PNG.toString('base64'),
        });

        const res = await githubImageResponse(call, URL_, true, {}, quiet);

        expect(calls).toEqual([
            { method: 'RPCHandler.GetImage', params: [{ URL: URL_, IncludeData: true }] },
        ]);
        expect(res.status).toBe(200);
        expect(res.headers.get('Content-Type')).toBe('image/png');
        expect(res.headers.get('Cache-Control')).toBe('private, max-age=600');
        expect(await bytesOf(res)).toEqual(PNG);
    });

    test('remote, a reply without bytes is unavailable', async () => {
        const logged: string[] = [];
        const { call } = server({ path: cachedFile(PNG), content_type: 'image/png' });

        const res = await githubImageResponse(call, URL_, true, {}, m => logged.push(m));

        expect(res.status).toBe(502);
        expect(logged).toEqual([`[Image] no bytes in the reply for ${URL_}`]);
    });

    test('an image the server could not serve is unavailable, with its reason logged', async () => {
        const logged: string[] = [];
        const tooBig = 'the image is over the 10 MiB GetImage sends inline';
        const { call } = server({ okay: false, error: tooBig });

        const res = await githubImageResponse(call, URL_, true, {}, m => logged.push(m));

        expect(res.status).toBe(502);
        expect(logged).toEqual([`[Image] ${tooBig} for ${URL_}`]);
    });

    test('locally, a cached file that has gone is unavailable', async () => {
        const { call } = server({
            path: '/nonexistent/crs/images/x.png',
            content_type: 'image/png',
        });
        const res = await githubImageResponse(call, URL_, false, {}, quiet);
        expect(res.status).toBe(502);
    });

    test('a failed call is unavailable', async () => {
        const { call } = server(new Error('The connection to crs at crs.test:7700 was lost'));
        const res = await githubImageResponse(call, URL_, true, {}, quiet);
        expect(res.status).toBe(502);
    });

    test('a URL off GitHub is refused without asking the server', async () => {
        const { call, calls } = server({});
        const res = await githubImageResponse(call, 'https://evil.test/x.png', true, {}, quiet);
        expect(res.status).toBe(400);
        expect(calls).toEqual([]);
    });
});
