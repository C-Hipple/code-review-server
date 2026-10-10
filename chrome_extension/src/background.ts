// Service worker: owns the connection to the server (through the native host,
// see native_client.ts), answers extension pages' RPC and status requests,
// and turns a toolbar click into the modal on github.com or a popup window
// anywhere else.

import { isGitHubUrl, parsePullUrl } from './github_url';
import { NativeClient } from './native_client';
import type { ErrorInfo } from './native_protocol';
import { isRpcMethod, type RpcReply, type ToggleMessage } from './types';

const HOST_NAME = 'com.c_hipple.crs';
const TITLE = 'Code Review Server';
const STANDALONE_PATH = 'panel.html?standalone=1';

const client = new NativeClient({
    connect: () => chrome.runtime.connectNative(HOST_NAME),
    lastError: () => chrome.runtime.lastError?.message,
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
    if (!isExtensionPage(sender) || typeof message !== 'object' || message === null) {
        return false;
    }
    const msg = message as Record<string, unknown>;

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
