// Runs once before the suite: checks the extension is built, builds the real
// native host from crs_native_host/ and writes the launcher the host is
// pointed at in place of codereviewserver (CRS_SERVER_PATH).

import { execFileSync } from 'node:child_process';
import { accessSync, chmodSync, constants, existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { delimiter, dirname, join } from 'node:path';
import {
    DIST_DIR,
    E2E_ROOT,
    FAKE_CRS_BIN,
    FAKE_CRS_SCRIPT,
    HOST_BIN,
    REPO_ROOT,
} from './harness/paths';

function isExecutable(path: string): boolean {
    try {
        accessSync(path, constants.X_OK);
        return true;
    } catch {
        return false;
    }
}

/** Bun, to run the fake server: Playwright itself usually runs under Node. */
function findBun(): string {
    if (process.versions.bun) return process.execPath;
    const candidates = (process.env.PATH ?? '')
        .split(delimiter)
        .filter(Boolean)
        .map(dir => join(dir, 'bun'));
    candidates.push(join(process.env.BUN_INSTALL ?? join(homedir(), '.bun'), 'bin', 'bun'));
    const bun = candidates.find(isExecutable);
    if (!bun) throw new Error('[e2e] bun not found on PATH; the fake server runs under Bun');
    return bun;
}

export default function globalSetup(): void {
    if (!existsSync(join(DIST_DIR, 'manifest.json'))) {
        throw new Error(
            '[e2e] chrome_extension/dist is missing — run `bun run build` (or `bun run test:e2e`, which builds first).'
        );
    }

    rmSync(E2E_ROOT, { recursive: true, force: true });
    mkdirSync(dirname(HOST_BIN), { recursive: true });

    // The host Chrome launches is the real one, built fresh from this checkout.
    try {
        execFileSync('go', ['build', '-o', HOST_BIN, './chrome_extension/crs_native_host'], {
            cwd: REPO_ROOT,
            stdio: ['ignore', 'inherit', 'inherit'],
        });
    } catch (e) {
        throw new Error(
            `[e2e] building crs_native_host failed (is Go installed?): ${e instanceof Error ? e.message : String(e)}`
        );
    }

    const bun = findBun();
    writeFileSync(FAKE_CRS_BIN, `#!/bin/sh\nexec '${bun}' '${FAKE_CRS_SCRIPT}' "$@"\n`);
    chmodSync(FAKE_CRS_BIN, 0o755);
}
