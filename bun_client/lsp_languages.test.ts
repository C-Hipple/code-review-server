import { afterAll, describe, expect, test } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import {
    isWithin,
    LANGUAGE_SERVERS,
    LanguageServerResolver,
    languageForPath,
    lspWorkspaceFor,
    serverLanguage,
    workspaceRoot,
} from './lsp_languages';

describe('languageForPath', () => {
    test('picks the server and the LSP language id from the extension', () => {
        expect(languageForPath('/r/server/server.go')).toEqual({ lang: 'go', languageId: 'go' });
        expect(languageForPath('/r/src/lib.rs')).toEqual({ lang: 'rust', languageId: 'rust' });
        expect(languageForPath('/r/app/main.py')).toEqual({
            lang: 'python',
            languageId: 'python',
        });
        expect(languageForPath('/r/typings/x.pyi')?.lang).toBe('python');
        expect(languageForPath('/r/src/App.tsx')).toEqual({
            lang: 'typescript',
            languageId: 'typescriptreact',
        });
        expect(languageForPath('/r/src/index.JSX')).toEqual({
            lang: 'typescript',
            languageId: 'javascriptreact',
        });
        expect(languageForPath('/r/vite.config.mjs')?.languageId).toBe('javascript');
    });

    test('nothing for a file no server handles', () => {
        expect(languageForPath('/r/README.md')).toBeNull();
        expect(languageForPath('/r/Makefile')).toBeNull();
        expect(languageForPath('/r/.gitignore')).toBeNull();
        expect(languageForPath('/r/x.constructor')).toBeNull();
    });

    test('covers Python, Go, Rust and TypeScript', () => {
        expect(Object.keys(LANGUAGE_SERVERS).sort()).toEqual([
            'go',
            'python',
            'rust',
            'typescript',
        ]);
    });
});

describe('serverLanguage', () => {
    test("maps an older client's Prism language onto its server", () => {
        expect(serverLanguage('go')).toBe('go');
        expect(serverLanguage('tsx')).toBe('typescript');
        expect(serverLanguage('javascript')).toBe('typescript');
        expect(serverLanguage('jsx')).toBe('typescript');
        expect(serverLanguage('ruby')).toBeNull();
        expect(serverLanguage('toString')).toBeNull();
        expect(serverLanguage('')).toBeNull();
        expect(serverLanguage(null)).toBeNull();
    });
});

describe('isWithin', () => {
    test('a path inside a directory, or the directory itself', () => {
        expect(isWithin('/r', '/r')).toBe(true);
        expect(isWithin('/r', '/r/a/b.go')).toBe(true);
        expect(isWithin('/r', '/rr/a.go')).toBe(false);
        expect(isWithin('/r', '/usr/lib/go/src/fmt/print.go')).toBe(false);
    });
});

describe('workspaceRoot', () => {
    const dir = mkdtempSync(join(tmpdir(), 'lsp-roots-'));
    afterAll(() => rmSync(dir, { recursive: true, force: true }));
    const touch = (...parts: string[]) => {
        const path = join(dir, ...parts);
        mkdirSync(dirname(path), { recursive: true });
        writeFileSync(path, '');
        return path;
    };

    test('the checkout when it has a marker at the top, so one server covers the repo', async () => {
        touch('mono', 'go.mod');
        touch('mono', 'tools', 'go.mod');
        const file = touch('mono', 'tools', 'gen', 'main.go');
        expect(await workspaceRoot(join(dir, 'mono'), file, ['go.work', 'go.mod'])).toBe(
            join(dir, 'mono')
        );
    });

    test('else the nearest project above the file', async () => {
        touch('split', 'backend', 'pyproject.toml');
        const file = touch('split', 'backend', 'app', 'api', 'views.py');
        const markers = LANGUAGE_SERVERS.python.rootMarkers;
        expect(await workspaceRoot(join(dir, 'split'), file, markers)).toBe(
            join(dir, 'split', 'backend')
        );
        // Relative paths are relative to the checkout.
        expect(await workspaceRoot(join(dir, 'split'), 'backend/app/api/views.py', markers)).toBe(
            join(dir, 'split', 'backend')
        );
    });

    test('else the checkout', async () => {
        const file = touch('plain', 'scripts', 'build.ts');
        expect(
            await workspaceRoot(join(dir, 'plain'), file, LANGUAGE_SERVERS.typescript.rootMarkers)
        ).toBe(join(dir, 'plain'));
    });

    test('a marker that is a directory does not count', async () => {
        mkdirSync(join(dir, 'odd', 'Cargo.toml'), { recursive: true });
        const file = touch('odd', 'crate', 'Cargo.toml');
        touch('odd', 'crate', 'src', 'lib.rs');
        expect(
            await workspaceRoot(join(dir, 'odd'), join(dir, 'odd', 'crate', 'src', 'lib.rs'), [
                'Cargo.toml',
            ])
        ).toBe(dirname(file));
    });

    test('a file outside the checkout is rooted at the checkout', async () => {
        touch('outside', 'go.mod');
        expect(
            await workspaceRoot(join(dir, 'outside'), '/usr/lib/go/src/fmt/print.go', ['go.mod'])
        ).toBe(join(dir, 'outside'));
    });
});

function fakeResolver(installed: Record<string, string>, opts: { failing?: string[] } = {}) {
    let now = 0;
    const which: string[] = [];
    const resolver = new LanguageServerResolver({
        which: cmd => {
            which.push(cmd);
            return installed[cmd] ?? null;
        },
        runs: async argv => !(opts.failing ?? []).includes(argv[0]),
        now: () => now,
        retryMissingMs: 1000,
    });
    return {
        resolver,
        which,
        tick: (ms: number) => {
            now += ms;
        },
    };
}

describe('LanguageServerResolver', () => {
    test('uses the first installed command, with its arguments', async () => {
        const { resolver } = fakeResolver({
            gopls: '/home/me/go/bin/gopls',
            'basedpyright-langserver': '/usr/bin/basedpyright-langserver',
        });
        expect(await resolver.resolve('go')).toEqual({
            lang: 'go',
            argv: ['/home/me/go/bin/gopls', 'serve'],
            name: 'gopls',
        });
        expect(await resolver.resolve('python')).toEqual({
            lang: 'python',
            argv: ['/usr/bin/basedpyright-langserver', '--stdio'],
            name: 'basedpyright-langserver',
        });
    });

    test("skips a command whose check fails, like rustup's proxy without the component", async () => {
        const { resolver } = fakeResolver(
            { 'rust-analyzer': '/home/me/.cargo/bin/rust-analyzer' },
            { failing: ['/home/me/.cargo/bin/rust-analyzer'] }
        );
        expect(await resolver.resolve('rust')).toBeNull();
    });

    test('asks again about a missing language after a while', async () => {
        const installed: Record<string, string> = {};
        const { resolver, which, tick } = fakeResolver(installed);
        expect(await resolver.resolve('go')).toBeNull();
        installed.gopls = '/bin/gopls';
        expect(await resolver.resolve('go')).toBeNull();
        expect(which).toEqual(['gopls']);
        tick(1000);
        expect((await resolver.resolve('go'))?.argv).toEqual(['/bin/gopls', 'serve']);
        // Found stays found without looking again.
        await resolver.resolve('go');
        expect(which).toEqual(['gopls', 'gopls']);
    });

    test('an unknown language has no server', async () => {
        const { resolver } = fakeResolver({ gopls: '/bin/gopls' });
        expect(await resolver.resolve('cobol')).toBeNull();
        expect(await resolver.resolve('constructor')).toBeNull();
    });
});

describe('lspWorkspaceFor', () => {
    const dir = mkdtempSync(join(tmpdir(), 'lsp-workspace-'));
    afterAll(() => rmSync(dir, { recursive: true, force: true }));
    mkdirSync(join(dir, 'web', 'src'), { recursive: true });
    writeFileSync(join(dir, 'go.mod'), 'module x\n');
    writeFileSync(join(dir, 'web', 'tsconfig.json'), '{}');

    test('names the server, its language id and root for a file', async () => {
        const { resolver } = fakeResolver({
            gopls: '/bin/gopls',
            'typescript-language-server': '/bin/tsls',
        });
        expect(await lspWorkspaceFor(resolver, dir, 'main.go')).toEqual({
            available: true,
            lang: 'go',
            languageId: 'go',
            root: dir,
            server: 'gopls',
            outside: false,
        });
        expect(await lspWorkspaceFor(resolver, dir, join(dir, 'web', 'src', 'App.tsx'))).toEqual({
            available: true,
            lang: 'typescript',
            languageId: 'typescriptreact',
            root: join(dir, 'web'),
            server: 'typescript-language-server',
            outside: false,
        });
    });

    test('says when a file is outside the checkout or has no server', async () => {
        const { resolver } = fakeResolver({ gopls: '/bin/gopls' });
        const stdlib = await lspWorkspaceFor(resolver, dir, '/usr/lib/go/src/fmt/print.go');
        expect(stdlib).toMatchObject({ available: true, lang: 'go', outside: true, root: dir });
        expect(await lspWorkspaceFor(resolver, dir, 'lib.rs')).toMatchObject({
            available: false,
            lang: 'rust',
            server: 'rust-analyzer',
        });
        expect(await lspWorkspaceFor(resolver, dir, 'README.md')).toMatchObject({
            available: false,
            lang: '',
        });
    });
});
