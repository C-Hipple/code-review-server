// Playwright fixtures shared by every spec. Each test gets its own Chromium
// profile with the built extension loaded, the real native host registered
// in the profile's NativeMessagingHosts, a fake server behind it, and fake
// github.com pages served by context.route — nothing reaches the network.
//
//   context  — the persistent context (one per test, so one service worker,
//              one host and one fake server per test)
//   sw       — the extension's service worker
//   crs      — the fake server's controls: scenario, call log, host log
//   github   — opens fake GitHub pages and toggles the modal on them
//
// Options (test.use): `hostInstall` registers the host, leaves it out
// ('missing') or registers it for another extension id ('forbidden');
// `hostEnv` adds or overrides lines in the host's env file; `browserEnv`
// adds variables to the browser's environment, which the host inherits.
//
// Import `test` and `expect` from here instead of @playwright/test.

import {
    existsSync,
    mkdirSync,
    mkdtempSync,
    readFileSync,
    renameSync,
    rmSync,
    writeFileSync,
} from 'node:fs';
import { join } from 'node:path';
import {
    test as base,
    chromium,
    expect,
    type BrowserContext,
    type Frame,
    type Locator,
    type Page,
    type Worker,
} from '@playwright/test';
import {
    DIST_DIR,
    E2E_ROOT,
    EXTENSION_ID,
    EXTENSION_ORIGIN,
    FAKE_CRS_BIN,
    HOST_BIN,
    HOST_NAME,
    PANEL_URL,
} from './paths';
import {
    CALLS_FILE,
    SCENARIO_FILE,
    STATE_DIR_VAR,
    type RecordedCall,
    type Scenario,
} from './scenario';

export { EXTENSION_ID, PANEL_URL };

export type HostInstall = 'installed' | 'missing' | 'forbidden';

/** The browser window; headless Chromium's frame takes about 140 px of its height. */
const WINDOW_SIZE = { width: 1280, height: 1040 };

/** The fake server's controls, through its state directory (see scenario.ts). */
export class FakeCrs {
    private current: Scenario = {};

    constructor(readonly stateDir: string) {
        mkdirSync(this.crsHome, { recursive: true });
        this.update({});
    }

    /** CRS_HOME for the host: its log is written here. */
    get crsHome(): string {
        return join(this.stateDir, 'crs_home');
    }

    /** What the host reports as its log, and where the fake's stderr goes. */
    get logPath(): string {
        return join(this.crsHome, 'native_host.log');
    }

    /**
     * Merges `changes` into the scenario; the fake reads it on its next
     * request. A field set to undefined is cleared.
     */
    update(changes: Scenario): void {
        this.current = { ...this.current, ...changes };
        const path = join(this.stateDir, SCENARIO_FILE);
        writeFileSync(`${path}.tmp`, JSON.stringify(this.current));
        renameSync(`${path}.tmp`, path);
    }

    /** Lets the named runs (AI feature ids, plugin names) land on their next read. */
    finishRuns(...names: string[]): void {
        this.update({ finishRuns: names });
    }

    /** Every request the fake received, oldest first; `method` without `RPCHandler.`. */
    calls(method?: string): RecordedCall[] {
        const path = join(this.stateDir, CALLS_FILE);
        if (!existsSync(path)) return [];
        const calls = readFileSync(path, 'utf-8')
            .split('\n')
            .filter(Boolean)
            .map(line => JSON.parse(line) as RecordedCall);
        return method ? calls.filter(c => c.method === `RPCHandler.${method}`) : calls;
    }

    /** Waits for a call to `method` whose params contain `params`, and returns it. */
    async waitForCall(method: string, params: Record<string, unknown> = {}): Promise<RecordedCall> {
        let found: RecordedCall | undefined;
        await expect
            .poll(
                () => {
                    found = this.calls(method).find(c => contains(c.params, params));
                    return found !== undefined;
                },
                { message: `no ${method} call with ${JSON.stringify(params)}` }
            )
            .toBe(true);
        return found!;
    }

    /** The native host's log, which also holds the fake's stderr. */
    hostLog(): string {
        return existsSync(this.logPath) ? readFileSync(this.logPath, 'utf-8') : '';
    }
}

function contains(actual: Record<string, unknown>, expected: Record<string, unknown>): boolean {
    return Object.entries(expected).every(
        ([k, v]) => JSON.stringify(actual[k]) === JSON.stringify(v)
    );
}

/** Fake GitHub pages, and the toolbar button. */
export class GitHub {
    constructor(
        private readonly context: BrowserContext,
        private readonly sw: Worker
    ) {}

    /**
     * Loads `https://github.com${path}` in the context's first tab (or `page`)
     * and waits for the extension's content script to be running in it.
     */
    async open(path: string, page?: Page): Promise<Page> {
        const tab = page ?? this.context.pages()[0] ?? (await this.context.newPage());
        await tab.goto(`https://github.com${path}`);
        await this.waitForContentScript(tab);
        return tab;
    }

    /**
     * The manifest injects content.js at document_idle, which can come after
     * `load`. Asks the content script's own (isolated) world whether it is
     * there, so a toggle never races its injection.
     */
    async waitForContentScript(page: Page): Promise<void> {
        await expect
            .poll(
                () =>
                    this.sw.evaluate(async url => {
                        const tab = (await chrome.tabs.query({})).find(t => t.url === url);
                        if (tab?.id === undefined) return false;
                        const [result] = await chrome.scripting.executeScript({
                            target: { tabId: tab.id },
                            func: () => '__crsContentScript' in window,
                        });
                        return result?.result === true;
                    }, page.url()),
                { message: `the content script never ran in ${page.url()}` }
            )
            .toBe(true);
    }

    /**
     * Clicks the extension's toolbar button for `page`'s tab. Playwright can't
     * click browser UI, and the button's shortcut (Alt+Shift+R) isn't
     * delivered to extensions in headless Chromium, so this dispatches
     * chrome.action.onClicked in the service worker with the tab: the
     * listener background.ts registered runs exactly as for a real click.
     */
    async clickToolbarButton(page: Page): Promise<void> {
        await this.sw.evaluate(async url => {
            const tabs = await chrome.tabs.query({});
            // The extension sees only github.com tabs' URLs; any other page
            // must be the one tab whose URL it can't see.
            const unseen = tabs.filter(t => t.url === undefined);
            const tab = tabs.find(t => t.url === url) ?? (unseen.length === 1 ? unseen[0] : null);
            if (!tab) throw new Error(`no tab for ${url}`);
            const onClicked = chrome.action.onClicked as unknown as {
                dispatch?: (tab: chrome.tabs.Tab) => void;
            };
            if (typeof onClicked.dispatch !== 'function') {
                throw new Error('chrome.action.onClicked.dispatch is not available');
            }
            onClicked.dispatch(tab);
        }, page.url());
    }

    /** Opens the modal on `page` with the toolbar button and returns the panel's frame. */
    async openPanel(page: Page): Promise<Frame> {
        await expect(modalHost(page)).toHaveCount(0);
        await this.clickToolbarButton(page);
        await expect(modalHost(page)).toBeAttached();
        return panelFrame(page);
    }
}

/** The modal's host element, which the content script adds to <html>. */
export function modalHost(page: Page): Locator {
    return page.locator('#crs-modal-host');
}

/**
 * The panel's iframe in the modal. It sits in a closed shadow root, out of
 * reach of locators, so it is found among the page's frames by its URL.
 */
export async function panelFrame(page: Page): Promise<Frame> {
    let frame: Frame | undefined;
    await expect
        .poll(
            () => {
                frame = page.frames().find(f => f.url().startsWith(PANEL_URL));
                return frame !== undefined;
            },
            { message: 'the panel iframe never loaded' }
        )
        .toBe(true);
    return frame!;
}

/** An AI feature's or plugin's card in the PR tools view, by its title. */
export function card(frame: Frame, title: string): Locator {
    return frame.locator(`article.card:has(.card-title:text-is(${JSON.stringify(title)}))`);
}

/** A card's status chip (Success, Running, Not run, ...). */
export function statusChip(cardLocator: Locator): Locator {
    return cardLocator.locator('.card-chips .chip');
}

// --- Fake github.com ------------------------------------------------------------

function escapeHtml(text: string): string {
    return text.replace(/[&<>"]/g, c => `&#${c.charCodeAt(0)};`);
}

/** A stand-in GitHub page: a PR page for /:owner/:repo/pull/:n/..., else a repo page. */
function githubPage(url: URL): string {
    const pr = url.pathname.match(/^\/([^/]+)\/([^/]+)\/pull\/(\d+)/);
    const title = pr ? `Pull request ${pr[1]}/${pr[2]}#${pr[3]}` : `GitHub ${url.pathname}`;
    const filler = '<p>Lorem ipsum dolor sit amet, consectetur adipiscing elit.</p>'.repeat(40);
    return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>${escapeHtml(title)}</title>
<style>
    :root { color-scheme: light dark; }
    body { margin: 0; font-family: sans-serif; background: #ffffff; color: #1f2328; }
    header { height: 56px; background: #24292f; color: #fff; display: flex; align-items: center; gap: 16px; padding: 0 24px; }
    main { padding: 24px 40px; }
    @media (prefers-color-scheme: dark) { body { background: #0d1117; color: #e6edf3; } }
</style>
</head>
<body>
<header>GitHub (e2e fake) <input id="gh-search" placeholder="Search"></header>
<main><h1 id="gh-title">${escapeHtml(title)}</h1>${filler}</main>
</body>
</html>`;
}

async function routeGitHub(context: BrowserContext): Promise<void> {
    // Nothing else may reach the network.
    await context.route(
        url => (url.protocol === 'http:' || url.protocol === 'https:') && url.host !== 'github.com',
        route => route.abort()
    );
    await context.route('https://github.com/**', route =>
        route.fulfill({
            contentType: 'text/html; charset=utf-8',
            body: githubPage(new URL(route.request().url())),
        })
    );
}

// --- Fixtures ---------------------------------------------------------------------

/** The browser's environment, without the developer's server config and tokens. */
function browserBaseEnv(): Record<string, string> {
    const env: Record<string, string> = {};
    for (const [key, value] of Object.entries(process.env)) {
        if (value === undefined) continue;
        if (key.startsWith('CRS_') || key === 'GEMINI_API_KEY' || key === 'OPENROUTER_API_KEY') {
            continue;
        }
        env[key] = value;
    }
    return env;
}

interface Options {
    hostInstall: HostInstall;
    hostEnv: Record<string, string>;
    browserEnv: Record<string, string>;
}

interface Fixtures {
    crs: FakeCrs;
    sw: Worker;
    github: GitHub;
}

export const test = base.extend<Options & Fixtures>({
    hostInstall: ['installed', { option: true }],
    hostEnv: [{}, { option: true }],
    browserEnv: [{}, { option: true }],

    // eslint-disable-next-line no-empty-pattern -- Playwright reads the dependencies from it
    crs: async ({}, use, testInfo) => {
        mkdirSync(E2E_ROOT, { recursive: true });
        const crs = new FakeCrs(mkdtempSync(join(E2E_ROOT, 'test-')));
        await use(crs);
        // The browser (and with it the host and the fake) is closed by now.
        if (testInfo.status !== testInfo.expectedStatus) {
            await testInfo.attach('native_host.log', {
                body: crs.hostLog(),
                contentType: 'text/plain',
            });
            await testInfo.attach('calls.jsonl', {
                body: JSON.stringify(crs.calls(), null, 2),
                contentType: 'application/json',
            });
        }
        try {
            rmSync(crs.stateDir, { recursive: true, force: true, maxRetries: 10 });
        } catch {
            // Chromium can still be flushing its cache; global setup empties E2E_ROOT.
        }
    },

    context: async ({ crs, hostInstall, hostEnv, browserEnv, colorScheme, headless }, use) => {
        const profile = join(crs.stateDir, 'profile');
        // Chromium on Linux reads per-profile host manifests from here.
        const manifests = join(profile, 'NativeMessagingHosts');
        mkdirSync(manifests, { recursive: true });
        if (hostInstall !== 'missing') {
            const id =
                hostInstall === 'forbidden' ? 'abcdefghijklmnopabcdefghijklmnop' : EXTENSION_ID;
            writeFileSync(
                join(manifests, `${HOST_NAME}.json`),
                JSON.stringify({
                    name: HOST_NAME,
                    description: 'Code Review Server bridge (e2e)',
                    path: HOST_BIN,
                    type: 'stdio',
                    allowed_origins: [`chrome-extension://${id}/`],
                })
            );
        }

        // The host merges this file into its environment before resolving
        // the server, as it does ~/.crs/native_host.env.
        const envFile = join(crs.stateDir, 'native_host.env');
        const hostVars: Record<string, string> = {
            CRS_SERVER_PATH: FAKE_CRS_BIN,
            CRS_HOME: crs.crsHome,
            [STATE_DIR_VAR]: crs.stateDir,
            ...hostEnv,
        };
        writeFileSync(
            envFile,
            Object.entries(hostVars)
                .map(([k, v]) => `${k}=${v}\n`)
                .join('')
        );

        const context = await chromium.launchPersistentContext(profile, {
            channel: 'chromium', // the full browser: the headless shell can't load extensions
            headless,
            colorScheme,
            // No viewport emulation: Chromium hit-tests the modal's
            // out-of-process iframe against the real window, so with an
            // emulated viewport taller than the window's content area, clicks
            // low in the panel never reach it. The window sets the size
            // instead: about 1280×900 inside.
            viewport: null,
            env: { ...browserBaseEnv(), CRS_NATIVE_HOST_ENV: envFile, ...browserEnv },
            args: [
                `--disable-extensions-except=${DIST_DIR}`,
                `--load-extension=${DIST_DIR}`,
                `--window-size=${WINDOW_SIZE.width},${WINDOW_SIZE.height}`,
            ],
        });
        await routeGitHub(context);
        await use(context);
        await context.close();
    },

    sw: async ({ context }, use) => {
        const ours = (w: Worker) => w.url().startsWith(`${EXTENSION_ORIGIN}/`);
        const sw =
            context.serviceWorkers().find(ours) ??
            (await context.waitForEvent('serviceworker', { predicate: ours }));
        // The worker can be reported before the extension APIs are bound in it.
        await expect
            .poll(() => sw.evaluate(() => !!chrome.tabs && !!chrome.action && !!chrome.scripting), {
                message: "the service worker's extension APIs never appeared",
            })
            .toBe(true);
        await use(sw);
    },

    github: async ({ context, sw }, use) => {
        await use(new GitHub(context, sw));
    },
});

export { expect };
