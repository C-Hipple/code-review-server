// The service worker against a fake `chrome`: who may call the server, what
// reaches the native port, and what the toolbar button does.

import { beforeAll, beforeEach, describe, expect, mock, test } from 'bun:test';

const EXT_ID = 'acmghogknbbihjoejbkejhhikiapmiib';
const BASE = `chrome-extension://${EXT_ID}/`;

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

describe('toolbar button', () => {
    const click = (tab: object) => (fake.action.onClicked.listeners[0] as (t: object) => void)(tab);
    const settle = () => new Promise(r => setTimeout(r, 5));

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
