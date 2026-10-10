import { describe, expect, test } from 'bun:test';
import {
    diffAnchor,
    diffFileUrl,
    diffLineUrl,
    isGitHubUrl,
    parsePullUrl,
    pullUrl,
    samePull,
    sha256Hex,
} from './github_url';

const PR = { owner: 'C-hipple', repo: 'code-review-server', number: 265 };

function hex(text: string): string {
    return new Bun.CryptoHasher('sha256').update(text).digest('hex');
}

describe('parsePullUrl', () => {
    test.each([
        'https://github.com/C-hipple/code-review-server/pull/265',
        'https://github.com/C-hipple/code-review-server/pull/265/',
        'https://github.com/C-hipple/code-review-server/pull/265/files',
        'https://github.com/C-hipple/code-review-server/pull/265/commits',
        'https://github.com/C-hipple/code-review-server/pull/265/checks',
        'https://github.com/C-hipple/code-review-server/pull/265/files/abc123..def456',
        'https://github.com/C-hipple/code-review-server/pull/265/commits/abc123',
        'https://github.com/C-hipple/code-review-server/pull/265?w=1',
        'https://github.com/C-hipple/code-review-server/pull/265/files?diff=split#diff-abc',
        'https://github.com/C-hipple/code-review-server/pull/265#issuecomment-1',
        'https://GITHUB.com/C-hipple/code-review-server/pull/265',
    ])('%s is PR 265', url => {
        expect(parsePullUrl(url)).toEqual(PR);
    });

    test('accepts dots and underscores in repo names', () => {
        expect(parsePullUrl('https://github.com/a-b/my_repo.js/pull/1')).toEqual({
            owner: 'a-b',
            repo: 'my_repo.js',
            number: 1,
        });
    });

    test.each([
        ['the PR list', 'https://github.com/C-hipple/code-review-server/pulls'],
        ['an issue', 'https://github.com/C-hipple/code-review-server/issues/12'],
        ['a repo root', 'https://github.com/C-hipple/code-review-server'],
        ['the home page', 'https://github.com/'],
        ['a non-numeric number', 'https://github.com/o/r/pull/abc'],
        ['PR 0', 'https://github.com/o/r/pull/0'],
        ['a leading zero', 'https://github.com/o/r/pull/012'],
        ['a .diff view', 'https://github.com/o/r/pull/12.diff'],
        ['a .patch view', 'https://github.com/o/r/pull/12.patch'],
        ['a number past 2^53', 'https://github.com/o/r/pull/99999999999999999999'],
        ['http', 'http://github.com/o/r/pull/1'],
        ['gist', 'https://gist.github.com/o/r/pull/1'],
        ['the API host', 'https://api.github.com/o/r/pull/1'],
        ['another host', 'https://example.com/o/r/pull/1'],
        ['github.com in a path', 'https://evil.example/github.com/o/r/pull/1'],
        ['a bad owner', 'https://github.com/-o/r/pull/1'],
        ['an owner with a dot', 'https://github.com/o.x/r/pull/1'],
        ['repo ..', 'https://github.com/o/../pull/1'],
        ['garbage', 'not a url'],
        ['empty', ''],
    ])('%s is not a PR', (_, url) => {
        expect(parsePullUrl(url)).toBeNull();
    });
});

describe('isGitHubUrl', () => {
    test('github.com pages only', () => {
        expect(isGitHubUrl('https://github.com/')).toBe(true);
        expect(isGitHubUrl('https://github.com/o/r/pull/1/files')).toBe(true);
        expect(isGitHubUrl('https://gist.github.com/')).toBe(false);
        expect(isGitHubUrl('http://github.com/')).toBe(false);
        expect(isGitHubUrl('chrome://extensions')).toBe(false);
        expect(isGitHubUrl('')).toBe(false);
    });
});

describe('samePull', () => {
    test('ignores owner and repo case', () => {
        expect(samePull(PR, { owner: 'c-HIPPLE', repo: 'Code-Review-Server', number: 265 })).toBe(
            true
        );
    });
    test('differs by number', () => {
        expect(samePull(PR, { ...PR, number: 266 })).toBe(false);
    });
    test('null only equals null', () => {
        expect(samePull(null, null)).toBe(true);
        expect(samePull(PR, null)).toBe(false);
        expect(samePull(null, PR)).toBe(false);
    });
});

describe('building URLs', () => {
    test('pullUrl', () => {
        expect(pullUrl(PR)).toBe('https://github.com/C-hipple/code-review-server/pull/265');
    });

    test('sha256Hex matches the FIPS test vector', async () => {
        expect(await sha256Hex('abc')).toBe(
            'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad'
        );
    });

    test('sha256Hex hashes UTF-8 bytes', async () => {
        expect(await sha256Hex('docs/ünïcode ✓.md')).toBe(hex('docs/ünïcode ✓.md'));
    });

    test('diffAnchor is diff- plus the path hash', async () => {
        expect(await diffAnchor('server/server.go')).toBe(`diff-${hex('server/server.go')}`);
    });

    test('diffFileUrl links the file in the Files changed tab', async () => {
        expect(await diffFileUrl(PR, 'ai/runner.go')).toBe(
            `https://github.com/C-hipple/code-review-server/pull/265/files#diff-${hex('ai/runner.go')}`
        );
    });

    test('diffLineUrl links a line on the new side by default', async () => {
        const base = `https://github.com/C-hipple/code-review-server/pull/265/files#diff-${hex('a.ts')}`;
        expect(await diffLineUrl(PR, 'a.ts', 42)).toBe(`${base}R42`);
        expect(await diffLineUrl(PR, 'a.ts', 7, 'L')).toBe(`${base}L7`);
    });
});
