// The service worker against a fake `chrome`: who may call the server, what
// reaches the native port, and what the toolbar button does.

import { beforeAll, beforeEach, describe, expect, mock, test } from 'bun:test';
import type { AIFeature, PluginConfig } from './types';

const EXT_ID = 'acmghogknbbihjoejbkejhhikiapmiib';
const BASE = `chrome-extension://${EXT_ID}/`;

const settle = () => new Promise(r => setTimeout(r, 5));

function feature(id: string, name: string, enabled = true): AIFeature {
    return {
        id,
        name,
        description: '',
        enabled,
        automatic: false,
        mode: 'oneshot',
        modes: ['oneshot'],
        provider: 'gemini',
        applied: false,
    };
}

function plugin(name: string): PluginConfig {
    return {
        Name: name,
        Command: name,
        IncludeDiff: true,
        IncludeHeaders: true,
        IncludeComments: false,
        IncludeBranch: false,
        OnlyOnDemand: false,
        Provider: '',
        Model: '',
    };
}

type Listener = (...args: never[]) => unknown;

function event() {
    const listeners: Listener[] = [];
    return {
        listeners,
        addListener: (cb: Listener) => void listeners.push(cb),
        removeListener: () => {},
    };
}

class FakePort {
    sent: unknown[] = [];
    onMessage = event();
    onDisconnect = event();
    postMessage(m: unknown) {
        this.sent.push(m);
    }
    receive(m: unknown) {
        for (const cb of this.onMessage.listeners) (cb as (m: unknown) => void)(m);
    }
}

const ports: FakePort[] = [];
const fake = {
    runtime: {
        id: EXT_ID,
        lastError: undefined as { message: string } | undefined,
        getURL: (p: string) => BASE + p,
        connectNative: mock((_name: string) => {
            const port = new FakePort();
            ports.push(port);
            return port;
        }),
        getContexts: mock(async (_filter: unknown): Promise<unknown[]> => []),
        ContextType: { TAB: 'TAB' },
        onMessage: event(),
        onInstalled: event(),
        onStartup: event(),
    },
    action: {
        onClicked: event(),
        setTitle: mock(async (_details: unknown) => {}),
    },
    tabs: {
        sendMessage: mock(async (_tabId: number, _message: unknown): Promise<unknown> => ({
            ok: true,
        })),
        onUpdated: event(),
        query: mock(async (_q: unknown): Promise<unknown[]> => []),
    },
    scripting: { executeScript: mock(async (_i: unknown) => []) },
    windows: {
        create: mock(async (_d: unknown) => ({})),
        update: mock(async (_id: number, _d: unknown) => ({})),
    },
};

beforeAll(async () => {
    (globalThis as { chrome?: unknown }).chrome = fake;
    await import('./background');
});

beforeEach(() => {
    (globalThis as { chrome?: unknown }).chrome = fake;
    for (const group of Object.values(fake)) {
        for (const value of Object.values(group)) {
            if (typeof value === 'function' && 'mockClear' in value) value.mockClear();
        }
    }
});

const EXTENSION_PAGE = {
    id: EXT_ID,
    url: `${BASE}panel.html?owner=o&repo=r&number=1`,
    origin: BASE.slice(0, -1),
};
const CONTENT_SCRIPT = {
    id: EXT_ID,
    url: 'https://github.com/o/r/pull/1',
    origin: 'https://github.com',
};

/** Sends a runtime message as `sender`; resolves with the response, or 'no response'. */
function sendAs(sender: object, message: unknown): Promise<unknown> {
    const [listener] = fake.runtime.onMessage.listeners as ((
        m: unknown,
        s: object,
        r: (x: unknown) => void
    ) => boolean)[];
    return new Promise(resolve => {
        const async = listener(message, sender, resolve);
        if (!async) setTimeout(() => resolve('no response'), 5);
    });
}

describe('messages', () => {
    test('content scripts get no RPC access', async () => {
        const msg = { type: 'crs-rpc', method: 'RPCHandler.GetAllReviews', params: {} };
        expect(await sendAs(CONTENT_SCRIPT, msg)).toBe('no response');
        expect(await sendAs(CONTENT_SCRIPT, { type: 'crs-status' })).toBe('no response');
        expect(fake.runtime.connectNative).not.toHaveBeenCalled();
    });

    test('other extensions are ignored', async () => {
        const sender = { ...EXTENSION_PAGE, id: 'someoneelse' };
        expect(await sendAs(sender, { type: 'crs-status' })).toBe('no response');
    });

    test('a page claiming an extension URL from another origin is ignored', async () => {
        const sender = { ...EXTENSION_PAGE, origin: 'https://github.com' };
        expect(await sendAs(sender, { type: 'crs-status' })).toBe('no response');
    });

    test('status before any call', async () => {
        expect(await sendAs(EXTENSION_PAGE, { type: 'crs-status' })).toEqual({ connected: false });
    });

    test('an extension page calls the server through the native host', async () => {
        const reply = sendAs(EXTENSION_PAGE, {
            type: 'crs-rpc',
            method: 'RPCHandler.GetPR',
            params: { Owner: 'o', Repo: 'r', Number: 1 },
        });
        expect(fake.runtime.connectNative).toHaveBeenCalledWith('com.c_hipple.crs');
        const port = ports[ports.length - 1];
        const request = port.sent[port.sent.length - 1] as { id: number; params: unknown };
        expect(request).toMatchObject({
            method: 'RPCHandler.GetPR',
            params: [{ Owner: 'o', Repo: 'r', Number: 1 }],
        });
        port.receive({ id: request.id, result: { okay: true }, error: null });
        expect(await reply).toEqual({ ok: true, result: { okay: true } });
    });

    test('methods outside the allowlist are refused', async () => {
        const reply = await sendAs(EXTENSION_PAGE, {
            type: 'crs-rpc',
            method: 'RPCHandler.MergePR',
            params: {},
        });
        expect(reply).toMatchObject({ ok: false, error: { kind: 'extension' } });
    });

    test('unknown message types get no answer', async () => {
        expect(await sendAs(EXTENSION_PAGE, { type: 'nope' })).toBe('no response');
        expect(await sendAs(EXTENSION_PAGE, 'crs-status')).toBe('no response');
    });
});

describe('the chips a PR page draws', () => {
    const TOOLS = { type: 'crs-tools' };

    /** Sends crs-tools and reports the reply and whatever reached the server. */
    async function ask(sender: object = CONTENT_SCRIPT) {
        const before = ports.length > 0 ? ports[ports.length - 1].sent.length : 0;
        const reply = sendAs(sender, TOOLS);
        await settle();
        const port = ports[ports.length - 1];
        const requests =
            (port?.sent as { id: number; method: string }[] | undefined)?.slice(before) ?? [];
        const lists = requests.filter(r => r.method.startsWith('RPCHandler.List'));
        return { reply, port, lists };
    }

    test('a content script on a PR is offered its features and plugins', async () => {
        const { reply, port, lists } = await ask();
        expect(lists.map(r => r.method).sort()).toEqual([
            'RPCHandler.ListAIFeatures',
            'RPCHandler.ListPlugins',
        ]);
        for (const request of lists) {
            port!.receive({
                id: request.id,
                result:
                    request.method === 'RPCHandler.ListAIFeatures'
                        ? {
                              features: [
                                  feature('change-diagram', 'Change diagram'),
                                  feature('test-gaps', 'Test gaps', false),
                              ],
                          }
                        : { plugins: [plugin('security_check')] },
                error: null,
            });
        }
        expect(await reply).toEqual({
            pr: { owner: 'o', repo: 'r', number: 1 },
            chips: [
                { kind: 'ai', id: 'change-diagram', name: 'Change diagram' },
                { kind: 'plugin', id: 'security_check', name: 'security_check' },
            ],
        });
    });

    test('off a PR page the content script gets nothing, and reaches no server', async () => {
        const sender = { ...CONTENT_SCRIPT, url: 'https://github.com/o/r/pulls' };
        const { reply, lists } = await ask(sender);
        expect(await reply).toEqual({ pr: null, chips: [] });
        expect(lists).toEqual([]);
        expect(fake.runtime.connectNative).not.toHaveBeenCalled();
    });

    test('a list that fails leaves the page with no chips', async () => {
        const { reply, port, lists } = await ask();
        expect(lists).toHaveLength(2);
        for (const request of lists) {
            port!.receive({ id: request.id, result: null, error: 'the server is not running' });
        }
        expect(await reply).toEqual({ pr: { owner: 'o', repo: 'r', number: 1 }, chips: [] });
    });

    test('other extensions may not ask', async () => {
        expect(await sendAs({ ...CONTENT_SCRIPT, id: 'someoneelse' }, TOOLS)).toBe('no response');
    });
});

describe('the file order a Files changed page applies', () => {
    const ASK = { type: 'crs-file-order' };

    /** Sends crs-file-order as `sender` and reports the reply and what reached the server. */
    async function ask(sender: object = CONTENT_SCRIPT) {
        const before = ports.length > 0 ? ports[ports.length - 1].sent.length : 0;
        const reply = sendAs(sender, ASK);
        await settle();
        const port = ports[ports.length - 1];
        const requests =
            (port?.sent as { id: number; method: string }[] | undefined)?.slice(before) ?? [];
        return { reply, port, requests };
    }

    test('a content script on a PR is answered with the order the server holds', async () => {
        const { reply, port, requests } = await ask();
        expect(requests.map(r => r.method)).toEqual(['RPCHandler.GetAIOutput']);
        expect(requests[0]).toMatchObject({
            params: [{ Owner: 'o', Repo: 'r', Number: 1, Feature: 'file-ordering' }],
        });
        port!.receive({
            id: requests[0].id,
            result: {
                output: {
                    'file-ordering': {
                        feature: 'file-ordering',
                        report: { files: ['internal/api/router.go', 'docs/api.md'] },
                    },
                },
            },
            error: null,
        });
        expect(await reply).toEqual({
            pr: { owner: 'o', repo: 'r', number: 1 },
            files: ['internal/api/router.go', 'docs/api.md'],
        });
    });

    test('a PR with no stored order is answered with none, so GitHub’s stands', async () => {
        const { reply, port, requests } = await ask();
        port!.receive({ id: requests[0].id, result: { output: {} }, error: null });
        expect(await reply).toEqual({ pr: { owner: 'o', repo: 'r', number: 1 }, files: null });
    });

    test('a read that fails leaves the page with GitHub’s order', async () => {
        const { reply, port, requests } = await ask();
        port!.receive({ id: requests[0].id, result: null, error: 'the server is not running' });
        expect(await reply).toEqual({ pr: { owner: 'o', repo: 'r', number: 1 }, files: null });
    });

    test('off a PR page nothing is asked for', async () => {
        const sender = { ...CONTENT_SCRIPT, url: 'https://github.com/o/r/pulls' };
        const { reply, requests } = await ask(sender);
        expect(await reply).toEqual({ pr: null, files: null });
        expect(requests).toEqual([]);
        expect(fake.runtime.connectNative).not.toHaveBeenCalled();
    });

    test('other extensions may not ask', async () => {
        expect(await sendAs({ ...CONTENT_SCRIPT, id: 'someoneelse' }, ASK)).toBe('no response');
    });
});

describe('toolbar button', () => {
    const click = (tab: object) => (fake.action.onClicked.listeners[0] as (t: object) => void)(tab);

    test('toggles the modal on a GitHub tab', async () => {
        click({ id: 3, url: 'https://github.com/o/r/pull/1/files' });
        await settle();
        expect(fake.tabs.sendMessage).toHaveBeenCalledWith(3, { type: 'crs-toggle' });
        expect(fake.scripting.executeScript).not.toHaveBeenCalled();
    });

    test('injects the content script when the tab has none, then retries', async () => {
        fake.tabs.sendMessage.mockImplementationOnce(async () => {
            throw new Error('Could not establish connection. Receiving end does not exist.');
        });
        click({ id: 4, url: 'https://github.com/' });
        await settle();
        expect(fake.scripting.executeScript).toHaveBeenCalledWith({
            target: { tabId: 4 },
            files: ['content.js'],
        });
        expect(fake.tabs.sendMessage).toHaveBeenCalledTimes(2);
    });

    test('opens the standalone window anywhere else', async () => {
        click({ id: 5, url: undefined });
        await settle();
        expect(fake.windows.create).toHaveBeenCalledWith({
            url: `${BASE}panel.html?standalone=1`,
            type: 'popup',
            width: 560,
            height: 760,
        });
    });

    test('focuses the standalone window if it is already open', async () => {
        fake.runtime.getContexts.mockImplementationOnce(async () => [
            { documentUrl: `${BASE}panel.html?owner=o&repo=r&number=1`, windowId: 1 },
            { documentUrl: `${BASE}panel.html?standalone=1`, windowId: 9 },
        ]);
        click({ id: 6, url: 'chrome://newtab/' });
        await settle();
        expect(fake.windows.update).toHaveBeenCalledWith(9, { focused: true });
        expect(fake.windows.create).not.toHaveBeenCalled();
    });
});

describe('button title', () => {
    const update = (tabId: number, change: object, tab: object) =>
        (fake.tabs.onUpdated.listeners[0] as (i: number, c: object, t: object) => void)(
            tabId,
            change,
            tab
        );

    test('names the PR on a PR page', () => {
        const url = 'https://github.com/o/r/pull/12/files';
        update(1, { url }, { url });
        expect(fake.action.setTitle).toHaveBeenCalledWith({
            tabId: 1,
            title: 'Code Review Server — o/r#12',
        });
    });

    test('resets elsewhere', () => {
        update(1, { status: 'complete' }, {});
        expect(fake.action.setTitle).toHaveBeenCalledWith({
            tabId: 1,
            title: 'Code Review Server',
        });
    });

    test('ignores updates that change neither URL nor load state', () => {
        update(1, { title: 'x' }, { url: 'https://github.com/o/r/pull/1' });
        expect(fake.action.setTitle).not.toHaveBeenCalled();
    });
});
