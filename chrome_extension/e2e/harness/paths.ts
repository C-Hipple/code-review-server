// Paths and identifiers shared by the Playwright config, global setup, the
// fixtures and the fake server. Everything the suite writes lives under
// E2E_ROOT, which global setup empties at the start of each run.

import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

export const EXTENSION_DIR = resolve(import.meta.dirname, '../..');
export const REPO_ROOT = resolve(EXTENSION_DIR, '..');
/** The unpacked extension `bun run build` writes. */
export const DIST_DIR = join(EXTENSION_DIR, 'dist');

/** Pinned by manifest.json's `key`. */
export const EXTENSION_ID = 'acmghogknbbihjoejbkejhhikiapmiib';
export const EXTENSION_ORIGIN = `chrome-extension://${EXTENSION_ID}`;
export const PANEL_URL = `${EXTENSION_ORIGIN}/panel.html`;
export const HOST_NAME = 'com.c_hipple.crs';

export const E2E_ROOT = process.env.CRS_EXT_E2E_ROOT || join(tmpdir(), 'crs-ext-e2e');
/** The real native host, built from crs_native_host/ by global setup. */
export const HOST_BIN = join(E2E_ROOT, 'bin', 'crs_native_host');
/**
 * The fake server's launcher, which CRS_SERVER_PATH names. Deliberately not
 * called `codereviewserver`: the host also looks next to its own binary.
 */
export const FAKE_CRS_BIN = join(E2E_ROOT, 'bin', 'fake_crs');
export const FAKE_CRS_SCRIPT = resolve(import.meta.dirname, 'fake_crs.ts');
