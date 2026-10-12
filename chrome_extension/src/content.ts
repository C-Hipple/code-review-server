// Content script for github.com: the modal the toolbar button toggles, and the
// chips beside a pull request's tabs that open it. It runs on every GitHub
// page, so it stays small — no React, no RPC (the panel in the iframe talks to
// the service worker itself, and the chips only ask it for names). Bundled as a
// classic IIFE.

import { parsePullUrl, samePull, type PullRef } from './github_url';
import type { PanelToPageMessage, ToolChip, ToolsRequestMessage, ToolsReply } from './types';

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

// The chips are Primer-coloured, like the panel, so they sit beside GitHub's
// own tabs without looking foreign.
const CHIP_CSS = `
:host {
    all: initial; display: inline-flex; align-items: center; gap: 6px;
    margin-left: 12px; color-scheme: light dark;
    font: 12px/1.5 -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans', Helvetica, Arial, sans-serif;
}
.chip {
    font: inherit; white-space: nowrap; cursor: pointer;
    color: #1f2328; background: #f6f8fa; border: 1px solid #d1d9e0b3;
    border-radius: 999px; padding: 2px 10px;
}
.chip:hover { background: #eff2f5; border-color: #d1d9e0; }
.chip:focus-visible { outline: 2px solid #0969da; outline-offset: 1px; }
@media (prefers-color-scheme: dark) {
    .chip { color: #e6edf3; background: #21262d; border-color: #3d444db3; }
    .chip:hover { background: #30363d; border-color: #3d444d; }
    .chip:focus-visible { outline-color: #4493f8; }
}
`;

/**
 * @param focus the AI feature id or plugin name to bring into view, when the
 * panel was opened from a chip.
 */
function panelUrl(pr: PullRef | null, focus: string | null = null): string {
    const url = chrome.runtime.getURL('panel.html');
    if (!pr) return url;
    const query = new URLSearchParams({
        owner: pr.owner,
        repo: pr.repo,
        number: String(pr.number),
    });
    if (focus) query.set('focus', focus);
    return `${url}?${query}`;
}

function open(focus: string | null = null): void {
    // Already open — from a chip, the toolbar button or a shortcut: point it at
    // the page as it is now, with what was asked for in view.
    if (modal) {
        const url = panelUrl(modal.pr, focus);
        if (modal.iframe.src !== url) modal.iframe.src = url;
        return;
    }

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
    iframe.src = panelUrl(pr, focus);
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

// --- The chips beside GitHub's tabs -------------------------------------------

const TOOLS_ID = 'crs-tools';

interface Tools {
    /** The PR the chips were fetched for. */
    pr: PullRef | null;
    chips: ToolChip[];
    /** That PR's answer has arrived, even if it was "no chips". */
    loaded: boolean;
}

let tools: Tools = { pr: null, chips: [], loaded: false };
/** Bumped by each refresh, so a slow answer for a page left behind is dropped. */
let toolsSeq = 0;
let toolsTimer: ReturnType<typeof setTimeout> | null = null;
/** Watches for GitHub re-rendering, which can take the chips with it. */
let toolsObserver: MutationObserver | null = null;

/**
 * The row of tabs a pull request's pages carry ("Conversation", "Commits",
 * "Files changed", ...), found by the tab it holds rather than by class names
 * or `nav` markup, which GitHub changes.
 */
function findTabsRow(): Element | null {
    const links = Array.from(document.querySelectorAll<HTMLAnchorElement>('a'));
    // GitHub streams each tab's counts in after the page renders — "Files
    // changed" becomes "Files changed4 (4)" — and re-renders the row when they
    // land, so the label is matched with or without them. A diffstat ("4 files
    // changed") doesn't start with the label, and the nav check below drops
    // menu items.
    const filesTabs = links.filter(a =>
        /^files changed(\s|\d|$)/i.test((a.textContent ?? '').trim())
    );
    const link = filesTabs.find(a => a.closest('nav, [role="tablist"]')) ?? filesTabs[0];
    if (!link) return null;
    // The tabs are list items in GitHub's older markup and plain children of
    // the tab list in the React one.
    const item = link.closest('li');
    return (item ?? link).parentElement;
}

function chipButton(chip: ToolChip): HTMLButtonElement {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'chip';
    button.textContent = chip.name;
    button.title = `Show ${chip.name} in the Code Review Server panel`;
    button.addEventListener('click', () => open(chip.id));
    return button;
}

/** Draws the chips in the tab row, unless there are none or GitHub dropped them. */
function ensureTools(): void {
    if (tools.chips.length === 0) {
        document.getElementById(TOOLS_ID)?.remove();
        return;
    }
    if (document.getElementById(TOOLS_ID)) return;
    const row = findTabsRow();
    if (!row) return;

    // A tab row is a list, so the chips go in a list item; anything else gets
    // the host itself. An open shadow root, unlike the modal's closed one:
    // nothing here is private — feature and plugin names only, never a report
    // — and an open one stays visible to tests and to the page's own tooling.
    const host = document.createElement(
        row.tagName === 'UL' || row.tagName === 'OL' ? 'li' : 'div'
    );
    host.id = TOOLS_ID;
    const root = host.attachShadow({ mode: 'open' });
    const style = document.createElement('style');
    style.textContent = CHIP_CSS;
    root.append(style);
    for (const chip of tools.chips) root.append(chipButton(chip));
    row.append(host);
}

/** Asks the service worker what this page's PR has to offer. */
async function requestTools(): Promise<ToolsReply | null> {
    try {
        const message: ToolsRequestMessage = { type: 'crs-tools' };
        const reply = (await chrome.runtime.sendMessage(message)) as ToolsReply | null;
        return reply && typeof reply === 'object' ? reply : null;
    } catch {
        // The extension was reloaded under this page, or has no service worker
        // to wake; the toolbar button still opens the panel.
        return null;
    }
}

/** Brings the chips in line with the page: the PR's chips, or none at all. */
async function refreshTools(): Promise<void> {
    const pr = parsePullUrl(location.href);
    if (!pr) {
        tools = { pr: null, chips: [], loaded: false };
        document.getElementById(TOOLS_ID)?.remove();
        // Only a pull request page has a tab row to hang chips from.
        observeTools(false);
        return;
    }
    observeTools(true);
    if (tools.loaded && samePull(tools.pr, pr)) {
        // GitHub re-rendered the tab row, or a chip's card was opened: the
        // chips are known, so put them back rather than ask again.
        ensureTools();
        return;
    }

    const seq = ++toolsSeq;
    document.getElementById(TOOLS_ID)?.remove();
    tools = { pr, chips: [], loaded: false };
    const reply = await requestTools();
    if (seq !== toolsSeq || !reply?.pr || !samePull(reply.pr, pr)) return;
    tools = { pr, chips: reply.chips, loaded: true };
    ensureTools();
}

/** Watches the page for the tab row being re-rendered, while on a PR page. */
function observeTools(on: boolean): void {
    if (on === (toolsObserver !== null)) return;
    if (!on) {
        toolsObserver?.disconnect();
        toolsObserver = null;
        return;
    }
    toolsObserver = new MutationObserver(scheduleTools);
    toolsObserver.observe(document.documentElement, { childList: true, subtree: true });
}

/** A burst of mutations is one refresh; GitHub mutates its pages constantly. */
function scheduleTools(): void {
    if (toolsTimer !== null) return;
    toolsTimer = setTimeout(() => {
        toolsTimer = null;
        void refreshTools();
    }, 200);
}

// TODO(file-ordering): apply the file-ordering AI feature to GitHub's Files
// changed tab here. When the page is a PR's /files view, ask the service
// worker for the PR's stored file-ordering report (a new message it answers
// read-only for the sender tab's own PR — content scripts get no general RPC
// access), then reorder GitHub's per-file diff containers to match
// `report.files`, leaving files the report doesn't name at the end in GitHub's
// order. Re-apply on turbo:load / popstate and whenever GitHub lazily renders
// more files (a MutationObserver on the diff list), and never fight a user who
// has picked a file from GitHub's file tree.

const previous = (window as unknown as Record<string, { teardown(): void } | undefined>)[
    INSTANCE_KEY
];
try {
    previous?.teardown();
} catch {
    // A copy from before an extension reload can't reach chrome.* any more.
}
document.getElementById(HOST_ID)?.remove();
document.getElementById(TOOLS_ID)?.remove();

chrome.runtime.onMessage.addListener(onRuntimeMessage);
document.addEventListener('turbo:load', scheduleTools);
window.addEventListener('popstate', scheduleTools);
void refreshTools();

(window as unknown as Record<string, { teardown(): void }>)[INSTANCE_KEY] = {
    teardown() {
        close();
        chrome.runtime.onMessage.removeListener(onRuntimeMessage);
        document.removeEventListener('turbo:load', scheduleTools);
        window.removeEventListener('popstate', scheduleTools);
        if (toolsTimer !== null) clearTimeout(toolsTimer);
        toolsTimer = null;
        observeTools(false);
        document.getElementById(TOOLS_ID)?.remove();
    },
};
