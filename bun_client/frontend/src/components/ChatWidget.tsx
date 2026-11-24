import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react';
import {
    anySessionRunning,
    attachedLabel,
    chatIsStale,
    elapsedLabel,
    formatCost,
    isSendKey,
    modelLabel,
    sessionDetail,
    shouldCloseOnLeave,
    type ChatBackendInfo,
    type ChatMessage,
    type ChatSummary,
} from '../chat_utils';
import { useChat, type ChatState } from '../hooks/useChat';
import GitHubMarkdown from './GitHubMarkdown';

interface ChatWidgetProps {
    owner: string;
    repo: string;
    number: number;
    /** Lifts the button above a bar fixed to the bottom of the screen (the mobile review bar). */
    raised?: boolean;
}

/** How long the pointer rests on the button before the panel opens, and leaves before it closes. */
const OPEN_DELAY_MS = 120;
const CLOSE_DELAY_MS = 400;

/**
 * The review chat: a small floating button in the bottom-left corner that
 * opens a chat with a local agent about the PR when hovered (or clicked, which
 * also keeps it open). The panel stays open while the reviewer is working in
 * it — focus inside, or a message half written — and closes once the pointer
 * leaves otherwise. The conversation itself lives on the server (useChat).
 *
 * A PR can hold several sessions, each a context of its own: the header names
 * the open one and lists them all, to switch to, rename or delete; New session
 * clears the context and Fork carries the open one into a fresh context.
 */
export default function ChatWidget({ owner, repo, number, raised = false }: ChatWidgetProps) {
    const state = useChat({ owner, repo, number });
    const [open, setOpen] = useState(false);
    const [pinned, setPinned] = useState(false);
    const [input, setInput] = useState('');
    const rootRef = useRef<HTMLDivElement>(null);
    const textareaRef = useRef<HTMLTextAreaElement>(null);
    const hovering = useRef(false);
    const openTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
    const closeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

    const clearTimers = () => {
        if (openTimer.current) clearTimeout(openTimer.current);
        if (closeTimer.current) clearTimeout(closeTimer.current);
        openTimer.current = closeTimer.current = null;
    };
    useEffect(() => clearTimers, []);

    // Read when a close timer fires, so it sees the state as it is then.
    const closeState = useRef({ pinned, input });
    closeState.current = { pinned, input };
    const maybeClose = () => {
        const focusInside = !!rootRef.current?.contains(document.activeElement);
        if (
            !hovering.current &&
            shouldCloseOnLeave({
                ...closeState.current,
                draft: closeState.current.input,
                focusInside,
            })
        ) {
            setOpen(false);
        }
    };

    const onPointerEnter = () => {
        hovering.current = true;
        if (closeTimer.current) clearTimeout(closeTimer.current);
        closeTimer.current = null;
        if (!open && !openTimer.current) {
            openTimer.current = setTimeout(() => {
                openTimer.current = null;
                if (hovering.current) setOpen(true);
            }, OPEN_DELAY_MS);
        }
    };
    const onPointerLeave = () => {
        hovering.current = false;
        if (openTimer.current) clearTimeout(openTimer.current);
        openTimer.current = null;
        if (open) {
            if (closeTimer.current) clearTimeout(closeTimer.current);
            closeTimer.current = setTimeout(maybeClose, CLOSE_DELAY_MS);
        }
    };

    const close = () => {
        clearTimers();
        setOpen(false);
        setPinned(false);
    };
    const toggleFromButton = () => {
        clearTimers();
        if (open && pinned) {
            close();
            return;
        }
        setOpen(true);
        setPinned(true);
        // The panel renders on this click; focus once it has.
        setTimeout(() => textareaRef.current?.focus(), 0);
    };

    const submit = async () => {
        const text = input;
        if (!text.trim()) return;
        if (await state.send(text)) setInput('');
    };

    const running = anySessionRunning(state.chats, state.chat);

    return (
        <div
            ref={rootRef}
            className={`crs-chat${raised ? ' crs-chat--raised' : ''}`}
            onMouseEnter={onPointerEnter}
            onMouseLeave={onPointerLeave}
            onBlur={() => {
                // Focus leaving the panel with the pointer already gone
                // closes it, as the pointer leaving would have.
                setTimeout(maybeClose, 0);
            }}
            onKeyDown={e => {
                if (e.key === 'Escape' && open) {
                    e.stopPropagation();
                    close();
                }
            }}
        >
            {open && (
                <ChatPanel
                    state={state}
                    input={input}
                    onInput={setInput}
                    onSubmit={submit}
                    pinned={pinned}
                    onTogglePin={() => setPinned(p => !p)}
                    onClose={close}
                    textareaRef={textareaRef}
                />
            )}
            <button
                type="button"
                className={`crs-chat-button${open ? ' crs-chat-button--open' : ''}`}
                aria-label="Chat with an agent about this PR"
                aria-expanded={open}
                aria-haspopup="dialog"
                title="Chat about this PR"
                onClick={toggleFromButton}
            >
                <ChatIcon />
                {running && <span className="crs-chat-button-dot" aria-hidden="true" />}
            </button>
        </div>
    );
}

interface ChatPanelProps {
    state: ChatState;
    input: string;
    onInput: (value: string) => void;
    onSubmit: () => void;
    pinned: boolean;
    onTogglePin: () => void;
    onClose: () => void;
    textareaRef: React.RefObject<HTMLTextAreaElement | null>;
}

/** The chat itself: its header, the conversation, and the box to write in. */
export function ChatPanel({
    state,
    input,
    onInput,
    onSubmit,
    pinned,
    onTogglePin,
    onClose,
    textareaRef,
}: ChatPanelProps) {
    const { chat, backends, chats } = state;
    const running = chat?.status === 'running';
    const selected = state.agent ?? undefined;
    const chatBackend = chat ? backends?.find(b => b.id === chat.backend) : selected;
    const agentName = chatBackend?.name ?? 'the agent';
    const unavailable = !chat && selected && !selected.available ? selected : null;
    const noBackends = backends !== null && !selected;

    // While the agent works, tick once a second for the elapsed time.
    const [now, setNow] = useState(() => Date.now());
    useEffect(() => {
        if (!running) return;
        const timer = setInterval(() => setNow(Date.now()), 1000);
        return () => clearInterval(timer);
    }, [running]);

    const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
        if (isSendKey(e)) {
            e.preventDefault();
            if (!running) onSubmit();
        }
    };

    const cost = chat ? formatCost(chat.cost_usd) : null;
    const disabled = !!unavailable || noBackends || backends === null;

    // The list of the PR's sessions, shown in place of the conversation.
    const [listOpen, setListOpen] = useState(false);
    const newSession = () => {
        setListOpen(false);
        state.newChat();
        textareaRef.current?.focus();
    };
    const fork = async () => {
        setListOpen(false);
        if (await state.forkChat()) textareaRef.current?.focus();
    };
    const canFork = !!chat && !running && !state.sending;

    return (
        <div className="crs-chat-panel" role="dialog" aria-label="Review chat">
            <div className="crs-chat-header">
                <span className="crs-chat-title">Chat</span>
                <button
                    type="button"
                    className={`crs-chat-sessions-toggle${listOpen ? ' crs-chat-sessions-toggle--open' : ''}`}
                    aria-label={`Sessions: ${chat ? chat.title || 'Untitled session' : 'New session'}`}
                    aria-expanded={listOpen}
                    title="The PR's sessions: switch to one, rename or delete it"
                    onClick={() => setListOpen(o => !o)}
                >
                    <span className="crs-chat-sessions-current">
                        {chat ? chat.title || 'Untitled session' : 'New session'}
                    </span>
                    {chats.length > 0 && (
                        <span className="crs-chat-sessions-count">{chats.length}</span>
                    )}
                    <span aria-hidden="true">▾</span>
                </button>
                <span className="crs-chat-header-spacer" />
                <button
                    type="button"
                    className="crs-chat-text-button"
                    title="Clear the context: start a new session, the agent getting the PR's latest diff. This one stays under Sessions."
                    aria-label="New session"
                    disabled={!chat}
                    onClick={newSession}
                >
                    + New<span className="crs-chat-wide-only"> session</span>
                </button>
                <button
                    type="button"
                    className="crs-chat-text-button"
                    title="Continue in a new session: the agent starts afresh with this conversation and the PR's latest diff. This one stays under Sessions."
                    aria-label="Fork session"
                    disabled={!canFork}
                    onClick={fork}
                >
                    Fork
                </button>
                <button
                    type="button"
                    className={`crs-chat-icon-button${pinned ? ' crs-chat-icon-button--on' : ''}`}
                    title={pinned ? 'Unpin: close when the pointer leaves' : 'Keep open'}
                    aria-label={pinned ? 'Unpin chat' : 'Pin chat open'}
                    aria-pressed={pinned}
                    onClick={onTogglePin}
                >
                    📌
                </button>
                <button
                    type="button"
                    className="crs-chat-icon-button"
                    title="Close"
                    aria-label="Close chat"
                    onClick={onClose}
                >
                    ×
                </button>
            </div>

            {listOpen ? (
                <SessionList
                    state={state}
                    onOpen={id => {
                        setListOpen(false);
                        if (id !== chat?.id) state.openChat(id);
                    }}
                    onNew={newSession}
                    onClose={() => setListOpen(false)}
                />
            ) : (
                <>
                    {chat ? (
                        <div className="crs-chat-meta">
                            <span>
                                {agentName} · {modelLabel(chatBackend, chat.model)}
                                {cost && ` · ${cost}`}
                            </span>
                            <button
                                type="button"
                                className="crs-chat-link-button crs-chat-link-button--danger"
                                title="Delete this session"
                                onClick={() => {
                                    if (window.confirm('Delete this session?'))
                                        state.deleteChat(chat.id);
                                }}
                            >
                                Delete
                            </button>
                        </div>
                    ) : (
                        <AgentLine state={state} />
                    )}
                    <Conversation
                        state={state}
                        agentName={agentName}
                        selected={selected}
                        unavailable={unavailable}
                        noBackends={noBackends}
                        now={now}
                        canFork={canFork}
                        onFork={fork}
                        onNew={newSession}
                    />
                </>
            )}

            {state.error && (
                <div className="crs-chat-error" role="alert">
                    {state.error}
                </div>
            )}

            <form
                className="crs-chat-compose"
                onSubmit={e => {
                    e.preventDefault();
                    if (!running) onSubmit();
                }}
            >
                <textarea
                    ref={textareaRef}
                    className="crs-chat-input"
                    aria-label="Message"
                    placeholder={
                        chat ? 'Reply… (Enter to send)' : 'Ask about this PR… (Enter to send)'
                    }
                    rows={2}
                    value={input}
                    disabled={disabled}
                    onChange={e => onInput(e.target.value)}
                    onKeyDown={onKeyDown}
                />
                {running ? (
                    <button
                        type="button"
                        className="crs-chat-send crs-chat-send--stop"
                        onClick={state.stop}
                    >
                        Stop
                    </button>
                ) : (
                    <button
                        type="submit"
                        className="crs-chat-send"
                        disabled={disabled || state.sending || !input.trim()}
                    >
                        {state.sending ? '…' : 'Send'}
                    </button>
                )}
            </form>
        </div>
    );
}

interface ConversationProps {
    state: ChatState;
    agentName: string;
    selected: ChatBackendInfo | undefined;
    unavailable: ChatBackendInfo | null | undefined;
    noBackends: boolean;
    now: number;
    canFork: boolean;
    onFork: () => void;
    onNew: () => void;
}

/** The open session's messages, or what a new one says before it starts. */
function Conversation({
    state,
    agentName,
    selected,
    unavailable,
    noBackends,
    now,
    canFork,
    onFork,
    onNew,
}: ConversationProps) {
    const { chat } = state;
    const running = chat?.status === 'running';

    // Follow the conversation as it grows, unless the reviewer scrolled up to read.
    const listRef = useRef<HTMLDivElement>(null);
    const stick = useRef(true);
    useLayoutEffect(() => {
        const el = listRef.current;
        if (el && stick.current) el.scrollTop = el.scrollHeight;
    }, [chat?.id, chat?.messages.length, chat?.draft, running]);
    useEffect(() => {
        stick.current = true;
    }, [chat?.id]);

    return (
        <div
            className="crs-chat-messages"
            ref={listRef}
            onScroll={e => {
                const el = e.currentTarget;
                stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
            }}
            aria-live="polite"
        >
            {chat && chatIsStale(chat) && (
                <div className="crs-chat-banner">
                    The PR has new commits since this session attached its diff.{' '}
                    {canFork ? (
                        <>
                            <button
                                type="button"
                                className="crs-chat-inline-button"
                                onClick={onFork}
                            >
                                Fork it
                            </button>{' '}
                            to carry this conversation to the latest diff, or{' '}
                        </>
                    ) : (
                        'Fork it once the agent is done, or '
                    )}
                    <button type="button" className="crs-chat-inline-button" onClick={onNew}>
                        start a new session
                    </button>
                    .
                </div>
            )}
            {!chat && (
                <div className="crs-chat-empty">
                    {noBackends ? (
                        'The server offers no chat agents.'
                    ) : unavailable ? (
                        <>
                            <strong>{unavailable.name} isn't available.</strong>{' '}
                            {unavailable.reason}
                        </>
                    ) : (
                        <>
                            Ask {selected?.name ?? 'an agent'} about this PR. Your first message
                            goes with the PR's description and diff, and it can read the repository,
                            but not change anything.
                        </>
                    )}
                </div>
            )}
            {chat?.messages.map(m => (
                <MessageView key={m.id} message={m} />
            ))}
            {running &&
                (chat?.draft ? (
                    <div className="crs-chat-message crs-chat-message--assistant crs-chat-message--streaming">
                        <div className="markdown-content">
                            <GitHubMarkdown>{chat.draft}</GitHubMarkdown>
                        </div>
                    </div>
                ) : (
                    <div className="crs-chat-thinking">
                        <span className="crs-chat-spinner" aria-hidden="true" />
                        {agentName} is working… {elapsedLabel(chat?.started_at, now)}
                    </div>
                ))}
        </div>
    );
}

interface SessionListProps {
    state: ChatState;
    onOpen: (id: string) => void;
    onNew: () => void;
    onClose: () => void;
}

/**
 * The PR's sessions, the most recently active first: a new one to start, and
 * each to open, rename or delete. Escape closes the list, not the panel.
 */
export function SessionList({ state, onOpen, onNew, onClose }: SessionListProps) {
    const { chats, chat, backends } = state;
    const now = Date.now();
    return (
        <div
            className="crs-chat-sessions"
            role="region"
            aria-label="Sessions on this PR"
            onKeyDown={e => {
                if (e.key === 'Escape') {
                    e.stopPropagation();
                    onClose();
                }
            }}
        >
            <button
                type="button"
                className={`crs-chat-session-open crs-chat-session-new${chat ? '' : ' crs-chat-session-open--current'}`}
                onClick={onNew}
            >
                <span className="crs-chat-session-title">+ New session</span>
                <span className="crs-chat-session-preview">
                    A fresh context: the agent starts over with the PR's latest diff. The sessions
                    below stay, to come back to.
                </span>
            </button>
            {chats.length === 0 ? (
                <div className="crs-chat-empty">No sessions on this PR yet.</div>
            ) : (
                <ul className="crs-chat-session-list">
                    {chats.map(c => (
                        <SessionRow
                            key={c.id}
                            session={c}
                            current={c.id === chat?.id}
                            detail={sessionDetail(c, backends, now)}
                            onOpen={() => onOpen(c.id)}
                            onRename={title => state.renameChat(c.id, title)}
                            onDelete={() => state.deleteChat(c.id)}
                        />
                    ))}
                </ul>
            )}
        </div>
    );
}

interface SessionRowProps {
    session: ChatSummary;
    current: boolean;
    detail: string;
    onOpen: () => void;
    onRename: (title: string) => Promise<boolean>;
    onDelete: () => void;
}

function SessionRow({ session, current, detail, onOpen, onRename, onDelete }: SessionRowProps) {
    const [editing, setEditing] = useState(false);
    const title = session.title || 'Untitled session';
    const save = async (value: string) => {
        setEditing(false);
        const next = value.trim();
        if (next && next !== session.title) await onRename(next);
    };
    return (
        <li className={`crs-chat-session${current ? ' crs-chat-session--current' : ''}`}>
            {editing ? (
                <input
                    className="crs-chat-rename"
                    aria-label="Session title"
                    defaultValue={session.title}
                    autoFocus
                    onFocus={e => e.currentTarget.select()}
                    onKeyDown={e => {
                        if (e.key === 'Enter') {
                            e.preventDefault();
                            void save(e.currentTarget.value);
                        } else if (e.key === 'Escape') {
                            e.stopPropagation();
                            setEditing(false);
                        }
                    }}
                    onBlur={e => void save(e.currentTarget.value)}
                />
            ) : (
                <button
                    type="button"
                    className={`crs-chat-session-open${current ? ' crs-chat-session-open--current' : ''}`}
                    aria-current={current ? 'true' : undefined}
                    onClick={onOpen}
                >
                    <span className="crs-chat-session-title">
                        {session.status === 'running' && (
                            <span
                                className="crs-chat-session-dot"
                                title="The agent is answering"
                                aria-label="answering"
                            />
                        )}
                        <span className="crs-chat-session-name">{title}</span>
                        {session.forked_from && <span className="crs-chat-session-tag">fork</span>}
                    </span>
                    <span className="crs-chat-session-detail">{detail}</span>
                    {session.preview && (
                        <span className="crs-chat-session-preview">{session.preview}</span>
                    )}
                </button>
            )}
            <span className="crs-chat-session-actions">
                <button
                    type="button"
                    className="crs-chat-link-button"
                    aria-label={`Rename ${title}`}
                    onClick={() => setEditing(true)}
                >
                    Rename
                </button>
                <button
                    type="button"
                    className="crs-chat-link-button crs-chat-link-button--danger"
                    aria-label={`Delete ${title}`}
                    onClick={() => {
                        if (window.confirm(`Delete the session "${title}"?`)) onDelete();
                    }}
                >
                    Delete
                </button>
            </span>
        </li>
    );
}

/**
 * The agent a new chat talks to — the server config's choice, not the
 * page's — and the model it asks for, when the agent offers more than its
 * default.
 */
function AgentLine({ state }: { state: ChatState }) {
    const agent: ChatBackendInfo | null = state.agent;
    if (!agent) return null;
    return (
        <div className="crs-chat-picker">
            <span className="crs-chat-agent" title="The server's config picks the chat agent">
                {agent.name}
            </span>
            {agent.models.length > 1 && (
                <label>
                    <span>Model</span>
                    <select
                        className="crs-chat-select"
                        aria-label="Model"
                        value={state.model}
                        disabled={!agent.available}
                        onChange={e => state.selectModel(e.target.value)}
                    >
                        {agent.models.map(m => (
                            <option key={m.id} value={m.id}>
                                {m.name}
                            </option>
                        ))}
                    </select>
                </label>
            )}
        </div>
    );
}

function MessageView({ message }: { message: ChatMessage }) {
    switch (message.role) {
        case 'user': {
            const attached = attachedLabel(message);
            return (
                <div className="crs-chat-message crs-chat-message--user">
                    <div className="crs-chat-user-text">{message.content}</div>
                    {attached && <div className="crs-chat-attached">📎 {attached}</div>}
                </div>
            );
        }
        case 'assistant':
            return (
                <div className="crs-chat-message crs-chat-message--assistant">
                    <div className="markdown-content">
                        <GitHubMarkdown>{message.content}</GitHubMarkdown>
                    </div>
                </div>
            );
        case 'tool': {
            const status = message.tool_status ?? 'done';
            const mark = status === 'running' ? '…' : status === 'error' ? '✗' : '✓';
            return (
                <div className={`crs-chat-tool crs-chat-tool--${status}`}>
                    <span className="crs-chat-tool-mark" aria-label={status}>
                        {mark}
                    </span>
                    <span className="crs-chat-tool-name">{message.tool}</span>
                    {message.content && (
                        <span className="crs-chat-tool-detail">{message.content}</span>
                    )}
                </div>
            );
        }
        case 'error':
            return (
                <div className="crs-chat-message crs-chat-message--error">{message.content}</div>
            );
        default:
            return <div className="crs-chat-notice">{message.content}</div>;
    }
}

function ChatIcon() {
    return (
        <svg width="22" height="22" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <path
                d="M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v8a2.5 2.5 0 0 1-2.5 2.5H10l-4.2 3.6c-.5.4-1.3.1-1.3-.6V16A2.5 2.5 0 0 1 4 13.5v-8Z"
                stroke="currentColor"
                strokeWidth="1.8"
                strokeLinejoin="round"
            />
            <circle cx="8.5" cy="9.5" r="1.1" fill="currentColor" />
            <circle cx="12" cy="9.5" r="1.1" fill="currentColor" />
            <circle cx="15.5" cy="9.5" r="1.1" fill="currentColor" />
        </svg>
    );
}
