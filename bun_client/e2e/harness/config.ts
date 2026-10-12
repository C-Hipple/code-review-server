// Ports and paths shared by the Playwright config, the server launcher, and
// the specs. Everything is derived from the port so two suites on one machine
// (or a suite and a dev server) never share state.

import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

export const BUN_CLIENT_DIR = resolve(import.meta.dirname, '../..');

/** Bridge with the fake diff-lsp and file-mode language server on PATH. */
export const LSP_PORT = parseInt(process.env.CRS_E2E_PORT || '5190', 10);
/** Bridge with no language servers at all on PATH. */
export const NO_LSP_PORT = LSP_PORT + 1;
/**
 * Bridge using the real diff-lsp and typescript-language-server from the
 * caller's PATH. Opt-in with CRS_E2E_REAL_LSP=1; see lsp_real.e2e.ts.
 */
export const REAL_LSP_PORT = LSP_PORT + 2;
export const REAL_LSP = process.env.CRS_E2E_REAL_LSP === '1';
/**
 * Bridge in remote mode (CRS_REMOTE), connected over TCP to a fake crs
 * started with --listen, with no language servers; see remote.e2e.ts.
 */
export const REMOTE_PORT = LSP_PORT + 3;
/** Where the remote bridge's fake crs listens: the port after the bridge's. */
export const remoteCrsPortFor = (port: number) => port + 1;
/** The registration key the remote bridge and its fake crs share. */
export const REGISTRATION_KEY = 'e2e-registration-key';

export const urlFor = (port: number) => `http://127.0.0.1:${port}`;

/** Where the fakes write their logs (and the launcher its PATH shims). */
export const stateDirFor = (port: number) => join(tmpdir(), 'crs-e2e', String(port));

/**
 * The fixture repo's git copy served by the real-LSP bridge. The real diff-lsp
 * runs `git fetch origin` in its root, which must not touch this checkout.
 */
export const repoCopyFor = (port: number) => join(stateDirFor(port), 'repo');

/**
 * The remote bridge's HOME, so nothing of the machine's leaks in: its
 * codereviewserver.toml is under .config, and the token it registers for
 * lands in .crs/remote_tokens/127.0.0.1_<port of its fake crs>.
 */
export const remoteHomeFor = (port: number) => join(stateDirFor(port), 'home');
