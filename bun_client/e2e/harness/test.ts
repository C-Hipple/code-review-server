// Playwright fixtures shared by every spec:
//
//   backend — drives the fake `crs` through the bridge's generic /api/rpc
//             endpoint: reset state, inspect the RPCs the UI sent, inject
//             failures. Auto-used, so every test starts from the fixtures.
//   lsp     — reads what the fake language servers received.
//
// Import `test` and `expect` from here instead of @playwright/test.

import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import {
    test as base,
    expect,
    type APIRequestContext,
    type Locator,
    type Page,
} from '@playwright/test';
import { stateDirFor } from './config';

export interface RpcCall {
    method: string;
    params: Record<string, any>;
}

class Backend {
    constructor(private request: APIRequestContext) {}

    async rpc<T = any>(method: string, params: Record<string, unknown> = {}): Promise<T> {
        const res = await this.request.post('/api/rpc', { data: { method, params: [params] } });
        const body = await res.json();
        if (body.error) throw new Error(`${method}: ${JSON.stringify(body.error)}`);
        return body.result as T;
    }

    reset() {
        return this.rpc('E2E.Reset');
    }

    /** Every non-E2E RPC the bridge forwarded since the last reset, oldest first. */
    async calls(method?: string): Promise<RpcCall[]> {
        const { calls } = await this.rpc<{ calls: RpcCall[] }>('E2E.Calls');
        const full = method && !method.includes('.') ? `RPCHandler.${method}` : method;
        return full ? calls.filter(c => c.method === full) : calls;
    }

    /** Fail the next call to `method` (e.g. "GetAllReviews") with `message`. */
    failNext(method: string, message = 'injected failure') {
        return this.rpc('E2E.FailNext', { method: `RPCHandler.${method}`, message });
    }

    setSyncUpdated(updated: boolean) {
        return this.rpc('E2E.SetSyncUpdated', { updated });
    }
}

export interface LspMessage {
    jsonrpc: string;
    id?: number;
    method?: string;
    params?: any;
}

export interface LspLogEntry {
    pid: number;
    event: 'spawn' | 'recv';
    argv?: string[];
    header?: Record<string, string>;
    tempfile?: string;
    error?: string;
    msg?: LspMessage;
}

type Role = 'diff' | 'file';

export class LspLog {
    constructor(private stateDir: string) {}

    private path(role: Role) {
        return join(this.stateDir, `lsp-${role}.jsonl`);
    }

    clear() {
        for (const role of ['diff', 'file'] as const) writeFileSync(this.path(role), '');
    }

    entries(role: Role): LspLogEntry[] {
        const path = this.path(role);
        if (!existsSync(path)) return [];
        return readFileSync(path, 'utf-8')
            .split('\n')
            .filter(Boolean)
            .map(line => JSON.parse(line));
    }

    spawns(role: Role) {
        return this.entries(role).filter(e => e.event === 'spawn');
    }

    /** Messages with `method` received by the server, oldest first. */
    received(role: Role, method: string): LspMessage[] {
        return this.entries(role)
            .filter(e => e.event === 'recv' && e.msg?.method === method)
            .map(e => e.msg!);
    }

    /**
     * Resolves once a server has received textDocument/didOpen — the point
     * where the frontend's useLsp marks itself connected and starts querying.
     */
    async waitForDidOpen(role: Role): Promise<LspMessage> {
        let open: LspMessage | undefined;
        await expect
            .poll(
                () => {
                    open = this.received(role, 'textDocument/didOpen').at(-1);
                    return !!open;
                },
                { message: `${role} language server never received didOpen` }
            )
            .toBe(true);
        return open!;
    }
}

export const test = base.extend<{ backend: Backend; lsp: LspLog }>({
    backend: [
        async ({ request }, use) => {
            const backend = new Backend(request);
            await backend.reset();
            await use(backend);
        },
        { auto: true },
    ],
    lsp: [
        async ({ baseURL }, use) => {
            const port = parseInt(new URL(baseURL!).port, 10);
            const log = new LspLog(stateDirFor(port));
            log.clear();
            await use(log);
        },
        { auto: true },
    ],
});

export { expect };

export const PR42 = { owner: 'acme', repo: 'widgets', number: 42 } as const;

/** Open the review view for a PR straight from its deep link. */
export async function openReview(
    page: Page,
    pr: { owner: string; repo: string; number: number } = PR42,
    title = 'Add greeting helper'
) {
    await page.goto(`/?owner=${pr.owner}&repo=${pr.repo}&number=${pr.number}`);
    await expect(page.getByRole('heading', { name: title })).toBeVisible();
}

/** A clickable diff row (file header or code line of the PR's diff) by its text. */
export function diffRow(page: Page, text: string) {
    return page.locator('.hover-line').filter({ hasText: text });
}

/**
 * The design system's Modal by its title. It renders no dialog role, so this
 * walks up from the title heading: <modal><header><h2>.
 */
export function modal(page: Page, title: string) {
    return page.getByRole('heading', { name: title, level: 2, exact: true }).locator('xpath=../..');
}

/**
 * Click the middle of the `nth` occurrence of `word` in `container`'s text,
 * wherever syntax highlighting happened to split it into spans. This is how
 * a reviewer asks for LSP info: the click position becomes the column.
 */
export async function clickWord(container: Locator, word: string, nth = 0) {
    await container.scrollIntoViewIfNeeded();
    const point = await container.evaluate(
        (el, { word, nth }) => {
            const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
            const nodes: Text[] = [];
            let text = '';
            while (walker.nextNode()) {
                nodes.push(walker.currentNode as Text);
                text += walker.currentNode.textContent;
            }
            let at = -1;
            for (let i = 0; i <= nth; i++) at = text.indexOf(word, at + 1);
            if (at === -1) return null;
            // Middle character of the word, as a 1-char range.
            let target = at + Math.floor(word.length / 2);
            for (const node of nodes) {
                const len = node.textContent!.length;
                if (target < len) {
                    const range = document.createRange();
                    range.setStart(node, target);
                    range.setEnd(node, target + 1);
                    const r = range.getBoundingClientRect();
                    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
                }
                target -= len;
            }
            return null;
        },
        { word, nth }
    );
    if (!point) throw new Error(`"${word}" not found in ${container}`);
    await container.page().mouse.click(point.x, point.y);
}
