// LSP integration: the bridge's WebSocket <-> stdio relay, the diff-lsp
// tempfile handshake, and the review UI's use of both language server modes
// (diff-lsp for the diff, a per-language server for the code viewer).

import { readFileSync } from 'node:fs';
import type { APIRequestContext, Locator, Page } from '@playwright/test';
import { FIXTURE_REPO, WIDGETS_DIFF } from './fixtures/prs';
import { NO_LSP_PORT, urlFor } from './harness/config';
import {
    clickWord,
    diffRow,
    expect,
    openReview,
    test,
    type LspLog,
    type LspMessage,
} from './harness/test';

const DIFF_LINES = WIDGETS_DIFF.split('\n');
const lineIndex = (prefix: string) => DIFF_LINES.findIndex(l => l.startsWith(prefix));

/** Minimal LSP-over-WebSocket client, the same protocol the frontend speaks. */
async function connect(url: string) {
    const ws = new WebSocket(url);
    const pending = new Map<number, (msg: any) => void>();
    const notifications: LspMessage[] = [];
    ws.onmessage = event => {
        const msg = JSON.parse(String(event.data));
        if (msg.id !== undefined && pending.has(msg.id)) {
            pending.get(msg.id)!(msg);
            pending.delete(msg.id);
        } else {
            notifications.push(msg);
        }
    };
    await new Promise<void>((resolve, reject) => {
        ws.onopen = () => resolve();
        ws.onerror = () => reject(new Error(`could not connect to ${url}`));
    });
    let nextId = 1;
    return {
        ws,
        notifications,
        request(method: string, params: unknown = {}): Promise<any> {
            const id = nextId++;
            return new Promise(resolve => {
                pending.set(id, resolve);
                ws.send(JSON.stringify({ jsonrpc: '2.0', id, method, params }));
            });
        },
        notify(method: string, params: unknown = {}) {
            ws.send(JSON.stringify({ jsonrpc: '2.0', method, params }));
        },
        close() {
            ws.close();
        },
    };
}

function isAlive(pid: number) {
    try {
        process.kill(pid, 0);
        return true;
    } catch {
        return false;
    }
}

async function prepareTempfile(request: APIRequestContext) {
    const res = await request.post('/api/prepare-diff-lsp', {
        data: {
            project: 'widgets',
            root: FIXTURE_REPO,
            buffer: 'PR #42',
            type: 'code-review',
            content: WIDGETS_DIFF,
        },
    });
    expect(res.ok()).toBe(true);
    return (await res.json()).path as string;
}

/** The floating LSP popover anchored to a diff row or code viewer line. */
function popover(page: Page) {
    return page.getByRole('button', { name: 'Close', exact: true }).locator('..');
}

/** One titled list in an LSP popover ("Definition:", "References (3):", ...). */
function section(container: Locator, title: string) {
    return container.getByText(title, { exact: true }).locator('..');
}

test.describe('LSP bridge', () => {
    test('reports which language servers are on PATH', async ({ request }) => {
        expect(await (await request.get('/api/check-lsp')).json()).toEqual({ available: true });
        for (const lang of ['typescript', 'tsx', 'javascript']) {
            const res = await request.get(`/api/check-lsp-file?lang=${lang}`);
            expect(await res.json(), lang).toEqual({ available: true });
        }
        for (const lang of ['go', 'rust', 'python', '']) {
            const res = await request.get(`/api/check-lsp-file?lang=${lang}`);
            expect(await res.json(), lang).toEqual({ available: false });
        }
        const refused = await request.get('/api/lsp-file?lang=go');
        expect(refused.status()).toBe(404);
    });

    test('writes the 5-line tempfile header diff-lsp parses', async ({ request }) => {
        const path = await prepareTempfile(request);
        expect(path).toMatch(/^\/tmp\/diff_lsp_[A-Za-z0-9-]+$/);

        const lines = readFileSync(path, 'utf-8').split('\n');
        expect(lines.slice(0, 5)).toEqual([
            'Project: widgets',
            `Root: ${FIXTURE_REPO}`,
            // No worktree: an empty value, never the string "undefined".
            'Worktree: ',
            'Buffer: PR #42',
            'Type: code-review',
        ]);
        expect(lines.slice(5).join('\n')).toBe(WIDGETS_DIFF);
    });

    test('relays framed LSP messages both ways over the WebSocket', async ({
        request,
        lsp,
        baseURL,
    }) => {
        const tempfile = await prepareTempfile(request);
        const client = await connect(
            `${baseURL!.replace(/^http/, 'ws')}/api/lsp?tempfile=${encodeURIComponent(tempfile)}`
        );

        const init = await client.request('initialize', { processId: null, rootUri: null });
        expect(init.result.capabilities).toMatchObject({
            hoverProvider: true,
            definitionProvider: true,
            referencesProvider: true,
        });
        client.notify('initialized');
        await expect
            .poll(() => client.notifications.map(n => n.method))
            .toContain('window/logMessage');

        // The bridge pinned diff-lsp to the tempfile it was handed.
        const [spawn] = lsp.spawns('diff');
        expect(spawn.argv).toEqual(['diff', tempfile]);
        expect(spawn.header).toMatchObject({ Root: FIXTURE_REPO, Type: 'code-review' });

        // Content-Length counts bytes: multi-byte text must survive both
        // directions, including a reply that arrives split mid-character.
        const payload = { text: 'héllo ✓ 世界 🚀', big: 'x'.repeat(200_000) };
        const echo = await client.request('e2e/echo', payload);
        expect(echo.result).toEqual({ echo: payload, split: true });

        // Several requests in flight come back matched to their ids. Positions
        // are 1-indexed tempfile lines (5 header lines, then the diff) and raw
        // diff columns.
        const i = lineIndex('+const message');
        const at = {
            textDocument: { uri: `file://${tempfile}` },
            position: { line: i + 6, character: DIFF_LINES[i].indexOf('formatGreeting') + 3 },
        };
        const [hover, defs, refs] = await Promise.all([
            client.request('textDocument/hover', at),
            client.request('textDocument/definition', at),
            client.request('textDocument/references', {
                ...at,
                context: { includeDeclaration: true },
            }),
        ]);
        expect(hover.result.contents.value).toContain('hovered `formatGreeting` at src/main.ts:3');
        expect(defs.result[0].uri).toBe(`file://${FIXTURE_REPO}/src/greet.ts`);
        expect(refs.result).toHaveLength(3);

        client.close();
    });

    test('kills the language server when the socket closes', async ({ request, lsp, baseURL }) => {
        const tempfile = await prepareTempfile(request);
        const client = await connect(
            `${baseURL!.replace(/^http/, 'ws')}/api/lsp?tempfile=${encodeURIComponent(tempfile)}`
        );
        await client.request('initialize');
        const { pid } = lsp.spawns('diff')[0];
        expect(isAlive(pid)).toBe(true);

        client.close();
        await expect.poll(() => isAlive(pid)).toBe(false);
    });

    test('does not pass diff-lsp a tempfile outside /tmp/diff_lsp_*', async ({ lsp, baseURL }) => {
        const client = await connect(
            `${baseURL!.replace(/^http/, 'ws')}/api/lsp?tempfile=${encodeURIComponent('/etc/passwd')}`
        );
        await expect.poll(() => lsp.spawns('diff').length).toBe(1);
        expect(lsp.spawns('diff')[0].argv).toEqual(['diff']);
        client.close();
    });

    test('spawns the per-language server for the code viewer', async ({ lsp, baseURL }) => {
        const client = await connect(
            `${baseURL!.replace(/^http/, 'ws')}/api/lsp-file?lang=typescript`
        );
        const init = await client.request('initialize', {
            processId: null,
            rootUri: `file://${FIXTURE_REPO}`,
        });
        expect(init.result.serverInfo.name).toBe('crs-e2e-fake-file');
        expect(lsp.spawns('file')).toHaveLength(1);
        client.close();
    });
});

test.describe('LSP in the review diff', () => {
    async function openAndConnect(page: Page, lsp: LspLog) {
        await openReview(page);
        return lsp.waitForDidOpen('diff');
    }

    test('hands diff-lsp the PR diff it is showing', async ({ page, lsp }) => {
        const open = await openAndConnect(page, lsp);

        const [spawn] = lsp.spawns('diff');
        expect(spawn.header).toEqual({
            Project: 'widgets',
            Root: FIXTURE_REPO,
            Worktree: '',
            Buffer: 'PR #42',
            Type: 'code-review',
        });
        const [, tempfile] = spawn.argv!;
        expect(tempfile).toMatch(/^\/tmp\/diff_lsp_/);
        expect(open.params.textDocument).toMatchObject({
            uri: `file://${tempfile}`,
            languageId: 'diff',
            text: WIDGETS_DIFF,
        });
        expect(lsp.received('diff', 'initialize')).toHaveLength(1);
        expect(lsp.received('diff', 'initialized')).toHaveLength(1);
    });

    test('shows hover, definition, type and references for a clicked symbol', async ({
        page,
        lsp,
    }) => {
        await openAndConnect(page, lsp);

        await clickWord(diffRow(page, 'const message = formatGreeting('), 'formatGreeting');

        const info = popover(page);
        await expect(info.locator('code').first()).toHaveText(
            'export function formatGreeting(greeting: Greeting): string'
        );
        await expect(info).toContainText('hovered formatGreeting at src/main.ts:3');
        await expect(section(info, 'Definition:').locator('li')).toHaveText(['greet.ts : 8']);
        await expect(section(info, 'Type Definition:').locator('li')).toHaveText(['greet.ts : 3']);
        await expect(section(info, 'References (3):').locator('li')).toHaveText([
            'greet.ts : 8',
            'main.ts : 1',
            'main.ts : 3',
        ]);

        // All four queries went out for the same position: the diff line's
        // index + 6 (5 header lines, 1-indexed) and the column + 1 (the +/-
        // prefix), landing inside the word that was clicked.
        const hover = lsp.received('diff', 'textDocument/hover').at(-1)!;
        const { line, character } = hover.params.position;
        expect(line).toBe(lineIndex('+const message') + 6);
        const raw = DIFF_LINES[line - 6];
        const start = raw.indexOf('formatGreeting');
        expect(character).toBeGreaterThanOrEqual(start);
        expect(character).toBeLessThan(start + 'formatGreeting'.length);
        for (const method of ['references', 'definition', 'typeDefinition']) {
            const req = lsp.received('diff', `textDocument/${method}`).at(-1)!;
            expect(req.params.position, method).toEqual(hover.params.position);
        }
    });

    test('maps context lines in other files too', async ({ page, lsp }) => {
        await openAndConnect(page, lsp);

        await clickWord(diffRow(page, 'export interface Greeting {'), 'Greeting');
        const info = popover(page);
        await expect(info.locator('code').first()).toHaveText('export interface Greeting');
        await expect(info).toContainText('hovered Greeting at src/greet.ts:3');
        await expect(info.getByText('References (3):')).toBeVisible();
    });

    test('shows nothing for a word the server knows nothing about', async ({ page, lsp }) => {
        await openAndConnect(page, lsp);

        await clickWord(diffRow(page, 'console.log(message);'), 'console');
        await expect.poll(() => lsp.received('diff', 'textDocument/hover').length).toBe(1);
        await expect(page.getByText(/^References \(/)).toHaveCount(0);
        await expect(page.getByRole('button', { name: 'Close', exact: true })).toHaveCount(0);
    });

    test('clicking the symbol again or pressing Escape dismisses the popover', async ({
        page,
        lsp,
    }) => {
        await openAndConnect(page, lsp);
        const row = diffRow(page, 'const message = formatGreeting(');
        const refs = page.getByText('References (3):');

        await clickWord(row, 'formatGreeting');
        await expect(refs).toBeVisible();
        await clickWord(row, 'formatGreeting');
        await expect(refs).toHaveCount(0);

        await clickWord(row, 'formatGreeting');
        await expect(refs).toBeVisible();
        await page.keyboard.press('Escape');
        await expect(refs).toHaveCount(0);

        // Dismissing only clears the display; the next click queries afresh.
        await clickWord(row, 'formatGreeting');
        await expect(refs).toBeVisible();
        expect(lsp.received('diff', 'textDocument/hover')).toHaveLength(3);
    });

    // Known bug: `.hover-line:hover { filter: brightness(1.2) }` (App.css)
    // makes the popover's row a stacking context as soon as the pointer is
    // over the popover, trapping its z-index under the rows that follow. The
    // x button sits over the next row, so the click lands on that row
    // instead. Remove test.fail() once the popover stays on top.
    test('the close button dismisses the popover', async ({ page, lsp }) => {
        test.fail();
        await openAndConnect(page, lsp);

        await clickWord(diffRow(page, 'const message = formatGreeting('), 'formatGreeting');
        await expect(page.getByText('References (3):')).toBeVisible();
        await page.getByRole('button', { name: 'Close', exact: true }).click({ timeout: 3_000 });
        await expect(page.getByText('References (3):')).toHaveCount(0, { timeout: 3_000 });
    });

    test('keeps LSP info open while a comment is written', async ({ page, lsp }) => {
        await openAndConnect(page, lsp);

        await clickWord(diffRow(page, 'const message = formatGreeting('), 'formatGreeting');
        const refs = page.getByText('References (3):');
        await expect(refs).toBeVisible();

        // A line comment leaves the floating popover up beside the form...
        await page.getByTitle('Add comment to src/main.ts:4').click();
        await expect(page.getByText('Commenting on src/main.ts:4')).toBeVisible();
        await expect(refs).toBeVisible();
        await page.getByRole('button', { name: 'Cancel' }).click();

        // ...and a file-level comment shows it inline in the form itself.
        await page.locator('.diff-file-row').filter({ hasText: 'src/main.ts' }).click();
        const form = page.getByText('Commenting on src/main.ts:0').locator('..');
        await expect(form.getByText('LSP Info')).toBeVisible();
        await expect(form.getByText('Hover:')).toBeVisible();
        await expect(form).toContainText('export function formatGreeting(greeting: Greeting)');
        await expect(section(form, 'Definition:').locator('li')).toHaveText([
            `file://${FIXTURE_REPO}/src/greet.ts : 8`,
        ]);
    });

    test('opens a definition in the code viewer, backed by its own language server', async ({
        page,
        lsp,
    }) => {
        await openAndConnect(page, lsp);

        await clickWord(diffRow(page, 'const message = formatGreeting('), 'formatGreeting');
        await section(popover(page), 'Definition:').getByText('greet.ts : 8').click();

        // The viewer reads the file from the checkout and starts the
        // TypeScript server rooted at the repo.
        await expect(page.getByText('(line 8)')).toBeVisible();
        await expect(page.getByTitle('Language server connected')).toBeVisible();
        await expect(page.locator('[data-line="8"]')).toContainText(
            'export function formatGreeting(greeting: Greeting): string {'
        );

        const [init] = lsp.received('file', 'initialize');
        expect(init.params.rootUri).toBe(`file://${FIXTURE_REPO}`);
        const open = await lsp.waitForDidOpen('file');
        expect(open.params.textDocument).toMatchObject({
            uri: `file://${FIXTURE_REPO}/src/greet.ts`,
            languageId: 'typescript',
            text: readFileSync(`${FIXTURE_REPO}/src/greet.ts`, 'utf-8'),
        });

        // Clicking in the viewer queries the file server with plain 0-indexed
        // positions.
        await clickWord(page.locator('[data-line="8"]'), 'Greeting', 1);
        await expect(page.getByText('hovered Greeting at src/greet.ts:8')).toBeVisible();
        const hover = lsp.received('file', 'textDocument/hover').at(-1)!;
        expect(hover.params.textDocument.uri).toBe(`file://${FIXTURE_REPO}/src/greet.ts`);
        expect(hover.params.position.line).toBe(7);
    });

    test('follows a reference to another file inside the code viewer', async ({ page, lsp }) => {
        await openAndConnect(page, lsp);

        await clickWord(diffRow(page, 'const message = formatGreeting('), 'formatGreeting');
        await section(popover(page), 'Definition:').getByText('greet.ts : 8').click();
        await expect(page.getByTitle('Language server connected')).toBeVisible();
        await lsp.waitForDidOpen('file');

        // The diff's popover stays open underneath; the viewer's renders last.
        await clickWord(page.locator('[data-line="8"]'), 'formatGreeting');
        await expect(page.getByText('hovered formatGreeting at src/greet.ts:8')).toBeVisible();
        const viewerRefs = section(page.locator('body'), 'References (3):').last();
        await viewerRefs.getByText('main.ts : 3').click();

        // The viewer switches files and opens the new one with its server.
        await expect(page.locator('[data-line="3"]')).toContainText(
            "const message = formatGreeting({ name: 'world', punctuation: '!' });"
        );
        await expect
            .poll(() =>
                lsp
                    .received('file', 'textDocument/didOpen')
                    .map(m => m.params.textDocument.uri as string)
            )
            .toContain(`file://${FIXTURE_REPO}/src/main.ts`);
    });
});

test.describe('LSP disabled', () => {
    test('a PR whose repo is not cloned never starts diff-lsp', async ({ page, lsp }) => {
        await openReview(
            page,
            { owner: 'acme', repo: 'gadgets', number: 7 },
            'Fix gadget overflow'
        );

        await expect(page.getByText('Repo not found locally. LSP disabled.')).toBeVisible();
        await clickWord(diffRow(page, 'const MaxWidth = 120'), 'MaxWidth');
        await expect(page.getByRole('button', { name: 'Close', exact: true })).toHaveCount(0);
        expect(lsp.spawns('diff')).toHaveLength(0);
    });

    test.describe('without diff-lsp installed', () => {
        test.use({ baseURL: urlFor(NO_LSP_PORT) });

        test('the bridge reports nothing available', async ({ request }) => {
            expect(await (await request.get('/api/check-lsp')).json()).toEqual({
                available: false,
            });
            expect(await (await request.get('/api/check-lsp-file?lang=typescript')).json()).toEqual(
                { available: false }
            );
        });

        test('the review warns and code clicks do nothing', async ({ page, lsp }) => {
            await openReview(page);

            await expect(page.getByText('LSP not active')).toBeVisible();
            await clickWord(diffRow(page, 'const message = formatGreeting('), 'formatGreeting');
            await expect(page.getByRole('button', { name: 'Close', exact: true })).toHaveCount(0);
            expect(lsp.spawns('diff')).toHaveLength(0);

            // Commenting still works without LSP.
            await page.getByTitle('Add comment to src/main.ts:4').click();
            await expect(page.getByText('Commenting on src/main.ts:4')).toBeVisible();
            await expect(page.getByText('LSP Info')).toHaveCount(0);
        });

        test('a socket opened anyway is closed immediately', async ({ baseURL }) => {
            const ws = new WebSocket(`${baseURL!.replace(/^http/, 'ws')}/api/lsp`);
            const code = await new Promise<number>(resolve => {
                ws.onclose = e => resolve(e.code);
            });
            expect(code).toBeGreaterThan(0);
        });
    });
});
