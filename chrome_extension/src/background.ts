// Service worker: owns the connection to the server (through the native host,
// see native_client.ts), answers extension pages' RPC and status requests,
// relays the server's change events to them, and turns a toolbar click into
// the modal on github.com or a popup window anywhere else.

import { isGitHubUrl, parsePullUrl, type PullRef } from './github_url';
import { fileOrderingFiles } from './file_order';
import { NativeClient } from './native_client';
import type { ErrorInfo, JsonRpcNotification } from './native_protocol';
import { isChangeFor, toServerEvent } from './server_events';
import { chipsFor } from './tool_chips';
import {
    EVENTS_PORT,
    FILE_ORDERING,
    isRpcMethod,
    type AIFeature,
    type AIFeatureOutput,
    type AIOutputChanged,
    type FileOrderChangedMessage,
    type FileOrderReply,
    type PluginConfig,
    type RpcReply,
    type ServerEventMessage,
    type ToggleMessage,
    type ToolsReply,
} from './types';

const HOST_NAME = 'com.c_hipple.crs';
const TITLE = 'Code Review Server';
const STANDALONE_PATH = 'panel.html?standalone=1';

const client = new NativeClient({
    connect: () => chrome.runtime.connectNative(HOST_NAME),
    lastError: () => chrome.runtime.lastError?.message,
    onNotification,
    // A new server: pages read what they show again, since whatever it did
    // before it subscribed was never announced.
    onSubscribed: () => broadcast({ type: 'resync' }),
});

// --- Messages from extension pages -------------------------------------------

/**
 * Only the extension's own pages — the panel, in GitHub's iframe or the popup
 * window — may talk to the server. Content scripts share the extension id but
 * report the web page's URL, so github.com never gets RPC access.
 */
function isExtensionPage(sender: chrome.runtime.MessageSender): boolean {
    if (sender.id !== chrome.runtime.id) return false;
    const base = chrome.runtime.getURL('');
    if (typeof sender.url !== 'string' || !sender.url.startsWith(base)) return false;
    return sender.origin === undefined || sender.origin === base.replace(/\/$/, '');
}

chrome.runtime.onMessage.addListener((message: unknown, sender, sendResponse) => {
    if (typeof message !== 'object' || message === null) {
        return false;
    }
    const msg = message as Record<string, unknown>;

    // Content scripts may ask which chips to draw on their own PR page, and
    // nothing else: what they get back is names, never a report.
    if (msg.type === 'crs-tools') {
        if (sender.id !== chrome.runtime.id) return false;
        void toolsFor(sender).then(sendResponse);
        return true;
    }

    // A PR's Files changed page may also ask for the file order to lay over
    // it. Read-only too, and again only ever about the sender's own page.
    if (msg.type === 'crs-file-order') {
        if (sender.id !== chrome.runtime.id) return false;
        void fileOrderFor(sender).then(sendResponse);
        return true;
    }

    if (!isExtensionPage(sender)) {
        return false;
    }

    if (msg.type === 'crs-status') {
        sendResponse(client.status());
        return false;
    }

    if (msg.type === 'crs-rpc') {
        if (!isRpcMethod(msg.method)) {
            const error: ErrorInfo = {
                kind: 'extension',
                message: `${String(msg.method)} is not one of the methods the extension may call.`,
            };
            sendResponse({ ok: false, error } satisfies RpcReply);
            return false;
        }
        const params = typeof msg.params === 'object' && msg.params !== null ? msg.params : {};
        void client.call(msg.method, params).then(sendResponse);
        return true; // sendResponse is called asynchronously
    }

    return false;
});

// --- The server's change events -------------------------------------------------

/**
 * Extension pages listening for change events, each on a port of its own
 * (EVENTS_PORT). A page that goes away takes its port with it.
 */
const eventPorts = new Set<chrome.runtime.Port>();

chrome.runtime.onConnect.addListener(port => {
    if (port.name !== EVENTS_PORT) return;
    if (!port.sender || !isExtensionPage(port.sender)) {
        port.disconnect();
        return;
    }
    eventPorts.add(port);
    port.onDisconnect.addListener(() => eventPorts.delete(port));
});

function broadcast(message: ServerEventMessage): void {
    for (const port of eventPorts) {
        try {
            port.postMessage(message);
        } catch {
            // Closed without a disconnect event reaching us yet.
            eventPorts.delete(port);
        }
    }
}

function onNotification(notification: JsonRpcNotification): void {
    const event = toServerEvent(notification);
    if (!event) return;
    broadcast(event);
    if (
        event.type === 'ai-output-changed' &&
        event.change.feature === FILE_ORDERING &&
        event.change.status !== 'pending'
    ) {
        void tellFileOrderChanged(event.change);
    }
}

/**
 * A file order landed for a PR: tells the content script on each of its tabs,
 * which asks for it again (fileOrderFor) and lays it over a Files changed
 * view. The message says only that, never the order.
 */
async function tellFileOrderChanged(change: AIOutputChanged): Promise<void> {
    let tabs: chrome.tabs.Tab[];
    try {
        tabs = await chrome.tabs.query({ url: 'https://github.com/*' });
    } catch {
        return;
    }
    const message: FileOrderChangedMessage = { type: 'crs-file-order-changed' };
    for (const tab of tabs) {
        const pr = tab.url ? parsePullUrl(tab.url) : null;
        if (tab.id === undefined || !pr || !isChangeFor(change, pr)) continue;
        chrome.tabs.sendMessage(tab.id, message).catch(() => {
            // No content script there (yet): it asks when it loads.
        });
    }
}

// --- The chips a PR page draws ------------------------------------------------

/**
 * The PR a content script is on. Content scripts share the extension's id but
 * report the web page's URL, so the URL — not anything the page says — decides
 * which PR is asked about, and a content script can only ever ask about its
 * own page.
 */
function contentScriptPR(sender: chrome.runtime.MessageSender): PullRef | null {
    const url = sender.tab?.url ?? sender.url;
    return typeof url === 'string' ? parsePullUrl(url) : null;
}

/**
 * What the content script on `sender`'s tab should draw: the PR it is on, and
 * a chip for each enabled AI feature and configured plugin. Two read-only
 * calls, the same the panel makes; a failure — no server yet, say — leaves the
 * page with no chips rather than an error, and the next page load retries.
 */
async function toolsFor(sender: chrome.runtime.MessageSender): Promise<ToolsReply> {
    const pr = contentScriptPR(sender);
    if (!pr) return { pr: null, chips: [] };
    const [features, plugins] = await Promise.all([
        client.call('RPCHandler.ListAIFeatures', {}),
        client.call('RPCHandler.ListPlugins', {}),
    ]);
    const featureList = features.ok ? arrayAt<AIFeature>(features.result, 'features') : null;
    const pluginList = plugins.ok ? arrayAt<PluginConfig>(plugins.result, 'plugins') : null;
    return { pr, chips: chipsFor(featureList, pluginList) };
}

/** `result[key]` when it is an array, else null. */
function arrayAt<T>(result: unknown, key: string): T[] | null {
    if (typeof result !== 'object' || result === null) return null;
    const value = (result as Record<string, unknown>)[key];
    return Array.isArray(value) ? (value as T[]) : null;
}

// --- The file order a Files changed page applies -------------------------------

/**
 * The file order stored for `sender`'s own PR, or null when the server holds
 * none: one read-only GetAIOutput, the same call the panel makes, so a page
 * loading never starts a run of its own. A failure — no server yet, say —
 * leaves GitHub's own order alone, and the page asks again later.
 */
async function fileOrderFor(sender: chrome.runtime.MessageSender): Promise<FileOrderReply> {
    const pr = contentScriptPR(sender);
    if (!pr) return { pr: null, files: null };
    const reply = await client.call('RPCHandler.GetAIOutput', {
        Owner: pr.owner,
        Repo: pr.repo,
        Number: pr.number,
        Feature: FILE_ORDERING,
    });
    const outputs = reply.ok ? recordAt<AIFeatureOutput>(reply.result, 'output') : null;
    return { pr, files: fileOrderingFiles(outputs?.[FILE_ORDERING]) };
}

/** `result[key]` when it is an object, else null. */
function recordAt<T>(result: unknown, key: string): Record<string, T> | null {
    if (typeof result !== 'object' || result === null) return null;
    const value = (result as Record<string, unknown>)[key];
    return typeof value === 'object' && value !== null ? (value as Record<string, T>) : null;
}

// --- Toolbar button / Alt+Shift+R ----------------------------------------------

chrome.action.onClicked.addListener(tab => {
    if (tab.id !== undefined && tab.url && isGitHubUrl(tab.url)) {
        void toggleModal(tab.id);
    } else {
        void openStandalone();
    }
});

async function toggleModal(tabId: number): Promise<void> {
    const toggle: ToggleMessage = { type: 'crs-toggle' };
    try {
        await chrome.tabs.sendMessage(tabId, toggle);
        return;
    } catch {
        // No content script to answer: the tab was open before the extension
        // was installed or reloaded. Inject it and try again.
    }
    try {
        await chrome.scripting.executeScript({ target: { tabId }, files: ['content.js'] });
        await chrome.tabs.sendMessage(tabId, toggle);
    } catch (e) {
        console.error(`crs: could not open the panel in tab ${tabId}:`, e);
    }
}

/** Focuses the standalone window if one is open, otherwise opens one. */
async function openStandalone(): Promise<void> {
    try {
        const contexts = await chrome.runtime.getContexts({
            contextTypes: [chrome.runtime.ContextType.TAB],
        });
        const open = contexts.find(c => c.documentUrl?.includes('standalone=1'));
        if (open && open.windowId >= 0) {
            await chrome.windows.update(open.windowId, { focused: true });
            return;
        }
    } catch {
        // Fall through and open a new one.
    }
    await chrome.windows.create({
        url: chrome.runtime.getURL(STANDALONE_PATH),
        type: 'popup',
        width: 560,
        height: 760,
    });
}

// --- Per-tab button title --------------------------------------------------------

function titleFor(url: string | undefined): string {
    const pr = url ? parsePullUrl(url) : null;
    return pr ? `${TITLE} — ${pr.owner}/${pr.repo}#${pr.number}` : TITLE;
}

function setTitle(tabId: number, url: string | undefined): void {
    chrome.action.setTitle({ tabId, title: titleFor(url) }).catch(() => {
        // The tab closed in the meantime.
    });
}

// `url` is only reported for tabs the extension has host access to (github.com);
// leaving GitHub shows up as a status change with no URL, which resets the title.
chrome.tabs.onUpdated.addListener((tabId, change, tab) => {
    if (change.url !== undefined || change.status === 'complete') setTitle(tabId, tab.url);
});

async function titleExistingTabs(): Promise<void> {
    const tabs = await chrome.tabs.query({ url: 'https://github.com/*' });
    for (const tab of tabs) {
        if (tab.id !== undefined) setTitle(tab.id, tab.url);
    }
}

chrome.runtime.onInstalled.addListener(() => void titleExistingTabs());
chrome.runtime.onStartup.addListener(() => void titleExistingTabs());
