// Boots the real Bun bridge (server.ts) against the fakes, for Playwright's
// webServer to run:
//
//   bun e2e/harness/start_server.ts <port> <lsp: fake|none|real|remote>
//
// server.ts finds `crs`, `diff-lsp` and the per-language servers with
// Bun.which, so the launcher builds a bin directory of shims for the fakes and
// makes it the *entire* PATH. Nothing installed on the machine (a real
// diff-lsp, gopls, ...) can leak into the run, and "none" really means no
// language server is available.
//
// "real" is the exception: the fake crs shim comes first, then the caller's
// PATH, so the real diff-lsp and typescript-language-server are used.
//
// "remote" runs the bridge in remote mode instead: no language servers and no
// crs on PATH at all, but a fake crs listening on TCP (the port after the
// bridge's) that the bridge connects to with CRS_REMOTE, registering with the
// fixture TOML in a HOME of its own.

import {
    chmodSync,
    cpSync,
    existsSync,
    mkdirSync,
    realpathSync,
    rmSync,
    symlinkSync,
    writeFileSync,
} from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { REMOTE_USER_TOML } from '../fixtures/remote_user';
import {
    BUN_CLIENT_DIR,
    REGISTRATION_KEY,
    remoteCrsPortFor,
    remoteHomeFor,
    repoCopyFor,
    stateDirFor,
} from './config';

const port = parseInt(process.argv[2] || '', 10);
const lsp = process.argv[3] || 'fake';
if (!port || !['fake', 'none', 'real', 'remote'].includes(lsp)) {
    console.error('usage: bun e2e/harness/start_server.ts <port> <fake|none|real|remote>');
    process.exit(2);
}

if (!existsSync(join(BUN_CLIENT_DIR, 'frontend/dist/index.html'))) {
    console.error(
        '[e2e] frontend/dist is missing — run `bun --filter frontend build` (or `bun run test:e2e`, which builds first).'
    );
    process.exit(1);
}

const stateDir = stateDirFor(port);
rmSync(stateDir, { recursive: true, force: true });
const binDir = join(stateDir, 'bin');
mkdirSync(binDir, { recursive: true });

function shim(name: string, script: string, ...args: string[]) {
    const path = join(binDir, name);
    const target = resolve(import.meta.dirname, script);
    const quoted = [target, ...args].map(a => `'${a}'`).join(' ');
    writeFileSync(path, `#!/bin/sh\nexec '${process.execPath}' ${quoted} "$@"\n`);
    chmodSync(path, 0o755);
}

// Remote mode must never spawn a crs, so it gets none to find.
if (lsp !== 'remote') shim('crs', 'fake_crs.ts');
if (lsp === 'fake') {
    shim('diff-lsp', 'fake_lsp.ts', 'diff');
    shim('typescript-language-server', 'fake_lsp.ts', 'file');
}

const extraEnv: Record<string, string> = {};
let path = binDir;
if (lsp === 'real') {
    for (const bin of ['diff-lsp', 'typescript-language-server', 'git']) {
        if (!Bun.which(bin)) {
            console.error(`[e2e] lsp=real needs \`${bin}\` on PATH`);
            process.exit(1);
        }
    }
    path = `${binDir}:${process.env.PATH}`;
    // A throwaway git checkout of the fixture repo for diff-lsp to root at.
    const repo = repoCopyFor(port);
    cpSync(resolve(import.meta.dirname, '../fixtures/repo'), repo, { recursive: true });
    const git = (...args: string[]) =>
        Bun.spawnSync(['git', '-c', 'user.name=e2e', '-c', 'user.email=e2e@example.com', ...args], {
            cwd: repo,
        });
    git('init', '-q');
    git('add', '.');
    git('commit', '-qm', 'fixture');
    extraEnv.CRS_E2E_REPO = repo;

    // typescript-language-server refuses to start unless `typescript` resolves
    // from the workspace, and the fixture has no node_modules. Borrow the
    // package installed alongside the server (npm puts them side by side).
    let dir = dirname(realpathSync(Bun.which('typescript-language-server')!));
    while (dir !== '/' && !existsSync(join(dir, 'node_modules/typescript'))) dir = dirname(dir);
    const typescript = join(dir, 'node_modules/typescript');
    if (existsSync(typescript)) {
        mkdirSync(join(repo, 'node_modules'));
        symlinkSync(typescript, join(repo, 'node_modules/typescript'));
    } else {
        console.warn('[e2e] no `typescript` package next to typescript-language-server');
    }
}

// Remote mode's settings, which only the remote bridge may see: a developer's
// own CRS_REMOTE would otherwise turn every bridge remote.
const REMOTE_ENV = [
    'CRS_REMOTE',
    'CRS_REMOTE_TLS',
    'CRS_REMOTE_TOKEN',
    'CRS_LOGIN',
    'CRS_REGISTRATION_KEY',
    'CRS_ADMIN_TOKEN',
];
const inherited = { ...(process.env as Record<string, string>) };
for (const name of REMOTE_ENV) delete inherited[name];

let fakeCrs: ReturnType<typeof Bun.spawn> | null = null;
if (lsp === 'remote') {
    const home = remoteHomeFor(port);
    mkdirSync(join(home, '.config'), { recursive: true });
    writeFileSync(join(home, '.config/codereviewserver.toml'), REMOTE_USER_TOML);
    const crsPort = remoteCrsPortFor(port);
    const address = `127.0.0.1:${crsPort}`;
    fakeCrs = Bun.spawn(
        [process.execPath, resolve(import.meta.dirname, 'fake_crs.ts'), '--listen', address],
        {
            env: { ...inherited, CRS_REGISTRATION_KEY: REGISTRATION_KEY },
            stdio: ['ignore', 'inherit', 'inherit'],
        }
    );
    // The bridge connects as it starts; a refused first attempt would only
    // put it in backoff.
    await waitForListener(crsPort);
    Object.assign(extraEnv, {
        CRS_REMOTE: address,
        CRS_REGISTRATION_KEY: REGISTRATION_KEY,
        HOME: home,
        XDG_CONFIG_HOME: join(home, '.config'),
        CRS_HOME: join(home, '.crs'),
    });
}

console.log(`[e2e] bridge on :${port} (lsp=${lsp}), state in ${stateDir}`);

// A child process rather than an import: Bun.which resolves against the PATH
// the process started with, not a PATH assigned to process.env later.
const env: Record<string, string> = {
    ...inherited,
    ...extraEnv,
    PATH: path,
    CRS_PORT: String(port),
    CRS_E2E_STATE_DIR: stateDir,
};
// The dev-mode Vite proxy would shadow the built frontend.
delete env.NODE_ENV;

const server = Bun.spawn([process.execPath, 'server.ts'], {
    cwd: BUN_CLIENT_DIR,
    env,
    stdio: ['ignore', 'inherit', 'inherit'],
});

for (const signal of ['SIGINT', 'SIGTERM'] as const) {
    process.on(signal, () => {
        server.kill(signal);
        fakeCrs?.kill(signal);
        process.exit(0);
    });
}
const code = await server.exited;
fakeCrs?.kill();
process.exit(code);

async function waitForListener(crsPort: number) {
    for (let attempt = 0; attempt < 100; attempt++) {
        try {
            const socket = await Bun.connect({
                hostname: '127.0.0.1',
                port: crsPort,
                socket: { data() {} },
            });
            socket.end();
            return;
        } catch {
            await Bun.sleep(100);
        }
    }
    console.error(`[e2e] the fake crs never listened on :${crsPort}`);
    fakeCrs?.kill();
    process.exit(1);
}
