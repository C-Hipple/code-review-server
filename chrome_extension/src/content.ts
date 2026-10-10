// Content script for github.com: the modal the toolbar button toggles. It runs
// on every GitHub page, so it stays small — no React, no RPC (the panel in the
// iframe talks to the service worker itself). Bundled as a classic IIFE.

import { parsePullUrl, samePull, type PullRef } from './github_url';
import type { PanelToPageMessage } from './types';

const HOST_ID = 'crs-modal-host';
// Set on the content script's (isolated) window so a re-injected copy — the
// service worker injects one into tabs that predate the extension — replaces
// this one instead of running beside it.
const INSTANCE_KEY = '__crsContentScript';
const EXTENSION_ORIGIN = chrome.runtime.getURL('').replace(/\/$/, '');

interface Modal {
    host: HTMLElement;
    iframe: HTMLIFrameElement;
    /** The PR the iframe was opened for, to notice soft navigation to another. */
    pr: PullRef | null;
    href: string;
    poll: ReturnType<typeof setInterval>;
    rootOverflow: string;
}

let modal: Modal | null = null;

const CSS = `
:host { all: initial; }
.backdrop { position: fixed; inset: 0; background: rgba(1, 4, 9, 0.55); }
.panel {
    position: fixed; top: 50%; left: 50%; transform: translate(-50%, -50%);
    width: min(960px, 96vw); height: min(90vh, 960px);
    border-radius: 12px; overflow: hidden; background: #ffffff;
    box-shadow: 0 16px 48px rgba(1, 4, 9, 0.4);
}
@media (prefers-color-scheme: dark) { .panel { background: #0d1117; } }
iframe { display: block; width: 100%; height: 100%; border: 0; }
`;

function panelUrl(pr: PullRef | null): string {
    const url = chrome.runtime.getURL('panel.html');
    if (!pr) return url;
    const query = new URLSearchParams({
        owner: pr.owner,
        repo: pr.repo,
        number: String(pr.number),
    });
    return `${url}?${query}`;
}

function open(): void {
    if (modal) return;
    const pr = parsePullUrl(location.href);

    // The shadow root keeps GitHub's CSS out of the modal and the modal's out of GitHub.
    const host = document.createElement('div');
    host.id = HOST_ID;
    host.style.cssText = 'all: initial; position: fixed; inset: 0; z-index: 2147483647;';
    const root = host.attachShadow({ mode: 'closed' });

    const style = document.createElement('style');
    style.textContent = CSS;
    const backdrop = document.createElement('div');
    backdrop.className = 'backdrop';
    backdrop.addEventListener('click', close);
    const panel = document.createElement('div');
    panel.className = 'panel';
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-modal', 'true');
    panel.setAttribute('aria-label', 'Code Review Server');
    const iframe = document.createElement('iframe');
    iframe.title = 'Code Review Server';
    // Lets the panel's Copy buttons use the clipboard from inside the iframe.
    iframe.allow = 'clipboard-write';
    iframe.src = panelUrl(pr);
    panel.append(iframe);
    root.append(style, backdrop, panel);

    // On <html>, not <body>: Turbo swaps <body> on soft navigation.
    document.documentElement.append(host);

    modal = {
        host,
        iframe,
        pr,
        href: location.href,
        poll: setInterval(followNavigation, 1000),
        rootOverflow: document.documentElement.style.overflow,
    };
    document.documentElement.style.overflow = 'hidden';
    document.addEventListener('keydown', onKeyDown, true);
    document.addEventListener('turbo:load', followNavigation);
    window.addEventListener('popstate', followNavigation);
    window.addEventListener('message', onPanelMessage);
    iframe.focus();
}

function close(): void {
    if (!modal) return;
    const { host, poll, rootOverflow } = modal;
    modal = null;
    clearInterval(poll);
    document.removeEventListener('keydown', onKeyDown, true);
    document.removeEventListener('turbo:load', followNavigation);
    window.removeEventListener('popstate', followNavigation);
    window.removeEventListener('message', onPanelMessage);
    document.documentElement.style.overflow = rootOverflow;
    host.remove();
}

function toggle(): void {
    if (modal) close();
    else open();
}

// Esc while focus is on the page; the panel handles Esc inside the iframe by
// posting `close`.
function onKeyDown(event: KeyboardEvent): void {
    if (event.key !== 'Escape' || !modal) return;
    event.preventDefault();
    event.stopPropagation();
    close();
}

// GitHub navigates without reloading (Turbo, pushState). If that lands on a
// different PR — or off PRs — while the modal is open, reopen the panel on it.
function followNavigation(): void {
    if (!modal || location.href === modal.href) return;
    modal.href = location.href;
    const pr = parsePullUrl(location.href);
    if (samePull(pr, modal.pr)) return;
    modal.pr = pr;
    modal.iframe.src = panelUrl(pr);
}

function onPanelMessage(event: MessageEvent): void {
    // Only the panel itself: the right origin and our own iframe's window.
    if (!modal || event.origin !== EXTENSION_ORIGIN) return;
    if (event.source !== modal.iframe.contentWindow) return;
    const data = event.data as Partial<PanelToPageMessage> | null;
    if (data?.source === 'crs-panel' && data.type === 'close') close();
}

function onRuntimeMessage(
    message: unknown,
    sender: chrome.runtime.MessageSender,
    sendResponse: (response: unknown) => void
): boolean {
    if (sender.id !== chrome.runtime.id) return false;
    if ((message as { type?: unknown } | null)?.type === 'crs-toggle') {
        toggle();
        // Answer so the service worker's sendMessage resolves rather than
        // treating the tab as having no content script.
        sendResponse({ ok: true });
    }
    return false;
}

// TODO(file-ordering): apply the file-ordering AI feature to GitHub's Files
// changed tab here. When the page is a PR's /files view, ask the service
// worker for the PR's stored file-ordering report (a new message it answers
// read-only for the sender tab's own PR — content scripts get no general RPC
// access), then reorder GitHub's per-file diff containers to match
// `report.files`, leaving files the report doesn't name at the end in GitHub's
// order. Re-apply on turbo:load / popstate and whenever GitHub lazily renders
// more files (a MutationObserver on the diff list), and never fight a user who
// has picked a file from GitHub's own file tree.

const previous = (window as unknown as Record<string, { teardown(): void } | undefined>)[
    INSTANCE_KEY
];
try {
    previous?.teardown();
} catch {
    // A copy from before an extension reload can't reach chrome.* any more.
}
document.getElementById(HOST_ID)?.remove();

chrome.runtime.onMessage.addListener(onRuntimeMessage);
(window as unknown as Record<string, { teardown(): void }>)[INSTANCE_KEY] = {
    teardown() {
        close();
        chrome.runtime.onMessage.removeListener(onRuntimeMessage);
    },
};
