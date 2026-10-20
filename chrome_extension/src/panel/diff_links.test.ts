import { describe, expect, test } from 'bun:test';
import { diffLineUrl } from '../github_url';
import { fileHref, lineHref, primeDiffAnchors } from './diff_links';

const PR = { owner: 'acme', repo: 'widgets', number: 42 };
// Any anchor will do here; the last test checks real ones.
const README = `diff-${'ab'.repeat(32)}`;

describe('annotation and file links', () => {
    test('before the anchor is known, links go to the Files changed tab', () => {
        expect(lineHref(PR, 'src/a.go', 12, new Map())).toBe(
            'https://github.com/acme/widgets/pull/42/files'
        );
        expect(fileHref(PR, 'src/a.go', new Map())).toBe(
            'https://github.com/acme/widgets/pull/42/files'
        );
    });

    test('with the anchor: the file, and the line on the new side', () => {
        const anchors = new Map([['README.md', README]]);
        expect(fileHref(PR, 'README.md', anchors)).toBe(
            `https://github.com/acme/widgets/pull/42/files#${README}`
        );
        expect(lineHref(PR, 'README.md', 7, anchors)).toBe(
            `https://github.com/acme/widgets/pull/42/files#${README}R7`
        );
        expect(lineHref(PR, 'README.md', 0, anchors)).toBe(
            `https://github.com/acme/widgets/pull/42/files#${README}`
        );
    });

    test('primed anchors are the SHA-256 of the path, as github_url builds them', async () => {
        const paths = [
            'internal/ratelimit/limiter.go',
            'docs/ünïcode.md',
            'internal/ratelimit/limiter.go',
        ];
        const anchors = await primeDiffAnchors(paths);
        expect(anchors.size).toBe(2);
        for (const p of paths) {
            expect(lineHref(PR, p, 88, anchors)).toBe(await diffLineUrl(PR, p, 88));
        }
        const expected = new Bun.CryptoHasher('sha256').update('docs/ünïcode.md').digest('hex');
        expect(anchors.get('docs/ünïcode.md')).toBe(`diff-${expected}`);
    });
});
