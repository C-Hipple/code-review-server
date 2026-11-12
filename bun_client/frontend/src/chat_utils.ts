// The review chat: a conversation with a local agent (Claude Code, to start)
// about the PR under review, held by the server. See docs/protocol.md#review-chat
// for the RPCs these types mirror.

/** A model a backend offers; `id` empty means "whatever the agent defaults to". */
export interface ChatModel {
    id: string;
    name: string;
}

/** An agent the server can chat through. */
export interface ChatBackendInfo {
    id: string;
    name: string;
    /** False when it can't run on the server's machine; `reason` says why. */
    available: boolean;
    reason?: string;
    /** The backend's own default first. */
    models: ChatModel[];
    default_model: string;
}

export type ChatStatus = 'idle' | 'running';

/** A chat as the list of a PR's chats shows it. */
export interface ChatSummary {
    id: string;
    owner: string;
    repo: string;
    number: number;
    backend: string;
    /** The model asked for; empty for the backend's default. */
    model: string;
    title: string;
    /** The PR head whose description and diff the chat attached. */
    head_sha: string;
    cost_usd: number;
    status: ChatStatus;
    created_at: string;
    updated_at: string;
}

export type ChatRole = 'user' | 'assistant' | 'tool' | 'error' | 'notice';

export interface ChatMessage {
    id: number;
    role: ChatRole;
    /** Markdown for a reply; what a tool was used on for a tool. */
    content: string;
    created_at: string;
    tool?: string;
    tool_status?: 'running' | 'done' | 'error';
    /** What a reviewer's message carried besides their words: "description", "diff". */
    attached?: string[];
    diff_truncated?: boolean;
    head_sha?: string;
}

export interface Chat extends ChatSummary {
    messages: ChatMessage[];
    /** The reply being written right now; empty between replies. */
    draft: string;
    /** When the running turn started (RFC 3339). */
    started_at?: string;
    /** The PR's head now; differs from `head_sha` once the PR has moved on. */
    current_sha: string;
}

/** StartChat's and SendChatMessage's reply. */
export interface ChatReply {
    /** False when the message was refused; `message` says why. */
    okay: boolean;
    message: string;
    chat: Chat | null;
}

/** Sent as a chat changes: a message lands, the reply grows, a turn starts or ends. */
export const CHAT_CHANGED_EVENT = 'Server.ChatChanged';

export interface ChatChanged {
    owner: string;
    repo: string;
    number: number;
    chat_id: string;
    status: string;
}

export function parseChatChanged(data: unknown): ChatChanged | null {
    if (typeof data !== 'object' || data === null) return null;
    const { owner, repo, number, chat_id, status } = data as Record<string, unknown>;
    if (
        typeof owner !== 'string' ||
        typeof repo !== 'string' ||
        typeof number !== 'number' ||
        typeof chat_id !== 'string'
    ) {
        return null;
    }
    return { owner, repo, number, chat_id, status: typeof status === 'string' ? status : '' };
}

/**
 * The backend to preselect: the remembered one while it is still available,
 * else the first available, else the first listed (whose reason then shows).
 */
export function pickBackend(
    backends: ChatBackendInfo[],
    remembered: string | null
): ChatBackendInfo | null {
    const known = backends.find(b => b.id === remembered && b.available);
    return known ?? backends.find(b => b.available) ?? backends[0] ?? null;
}

/** The model to preselect for `backend`: the remembered one if it offers it, else its default. */
export function pickModel(backend: ChatBackendInfo | null, remembered: string | null): string {
    if (!backend) return '';
    if (remembered !== null && backend.models.some(m => m.id === remembered)) return remembered;
    return backend.default_model;
}

/** How a chat's model reads: its name in the backend's list, else the ID as asked for. */
export function modelLabel(backend: ChatBackendInfo | undefined, model: string): string {
    const found = backend?.models.find(m => m.id === model);
    if (found) return found.name;
    return model || 'Default';
}

/** The PR has moved on since the chat attached its diff. */
export function chatIsStale(chat: Pick<Chat, 'head_sha' | 'current_sha'>): boolean {
    return !!chat.head_sha && !!chat.current_sha && chat.head_sha !== chat.current_sha;
}

/** A cost worth showing, or null: nothing for a chat that cost nothing yet. */
export function formatCost(usd: number): string | null {
    if (!usd || usd <= 0) return null;
    if (usd < 0.01) return '<$0.01';
    return `$${usd.toFixed(2)}`;
}

/** "12s", "3m 05s": how long the agent has been at it. */
export function elapsedLabel(startedAt: string | undefined, now: number): string {
    if (!startedAt) return '';
    const started = Date.parse(startedAt);
    if (Number.isNaN(started)) return '';
    const seconds = Math.max(0, Math.floor((now - started) / 1000));
    if (seconds < 60) return `${seconds}s`;
    return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`;
}

/** What a reviewer's message attached, in words; null when it attached nothing. */
export function attachedLabel(message: ChatMessage): string | null {
    const attached = message.attached ?? [];
    if (attached.length === 0) return null;
    const parts: string[] = [];
    if (attached.includes('description')) parts.push('description');
    if (attached.includes('diff'))
        parts.push(message.diff_truncated ? 'diff (cut to fit)' : 'diff');
    const sha = message.head_sha ? ` at ${message.head_sha.slice(0, 7)}` : '';
    return `PR ${parts.join(' and ')} attached${sha}`;
}

/**
 * Whether the hover-opened panel should close now the pointer has left it.
 * It stays while the reviewer is working in it: pinned open, focus inside it,
 * or a message half written.
 */
export function shouldCloseOnLeave(state: {
    pinned: boolean;
    focusInside: boolean;
    draft: string;
}): boolean {
    return !state.pinned && !state.focusInside && state.draft.trim() === '';
}

/** The send keystroke: Enter alone sends; Shift+Enter (or an IME composing) is a newline. */
export function isSendKey(e: {
    key: string;
    shiftKey: boolean;
    isComposing?: boolean;
    nativeEvent?: { isComposing?: boolean };
}): boolean {
    return e.key === 'Enter' && !e.shiftKey && !e.isComposing && !e.nativeEvent?.isComposing;
}

const BACKEND_KEY = 'crs.chat.backend';
const MODEL_KEY = 'crs.chat.model';

/** The backend and model last chosen, remembered per browser. */
export function loadChatPrefs(): { backend: string | null; model: string | null } {
    try {
        return {
            backend: localStorage.getItem(BACKEND_KEY),
            model: localStorage.getItem(MODEL_KEY),
        };
    } catch {
        return { backend: null, model: null };
    }
}

export function saveChatPrefs(backend: string, model: string): void {
    try {
        localStorage.setItem(BACKEND_KEY, backend);
        localStorage.setItem(MODEL_KEY, model);
    } catch {
        // Private mode or storage off: the picker just starts at the default.
    }
}
