import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react';
import {
    attachedLabel,
    chatIsStale,
    elapsedLabel,
    formatCost,
    isSendKey,
    modelLabel,
    shouldCloseOnLeave,
    type ChatBackendInfo,
    type ChatMessage,
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

    const running = state.chat?.status === 'running';

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
    const selected = backends?.find(b => b.id === state.backend);
    const chatBackend = chat ? backends?.find(b => b.id === chat.backend) : selected;
    const agentName = chatBackend?.name ?? 'the agent';
    const unavailable = !chat && selected && !selected.available ? selected : null;
    const noBackends = backends !== null && backends.length === 0;

    // While the agent works, tick once a second for the elapsed time.
    const [now, setNow] = useState(() => Date.now());
    useEffect(() => {
        if (!running) return;
        const timer = setInterval(() => setNow(Date.now()), 1000);
        return () => clearInterval(timer);
    }, [running]);

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

    const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
        if (isSendKey(e)) {
            e.preventDefault();
            if (!running) onSubmit();
        }
    };

    const cost = chat ? formatCost(chat.cost_usd) : null;
    const disabled = !!unavailable || noBackends || backends === null;

    return (
        <div className="crs-chat-panel" role="dialog" aria-label="Review chat">
            <div className="crs-chat-header">
                <span className="crs-chat-title">Chat</span>
                {chats.length > 0 && (
                    <select
                        className="crs-chat-select crs-chat-history"
                        aria-label="Chats on this PR"
                        value={chat?.id ?? ''}
                        onChange={e =>
                            e.target.value ? state.openChat(e.target.value) : state.newChat()
                        }
                    >
                        <option value="">New chat</option>
                        {chats.map(c => (
                            <option key={c.id} value={c.id}>
                                {c.title || 'Untitled chat'}
                            </option>
                        ))}
                    </select>
                )}
                <span className="crs-chat-header-spacer" />
                {chat && (
                    <button
                        type="button"
                        className="crs-chat-icon-button"
                        title="Start a new chat"
                        aria-label="New chat"
                        onClick={state.newChat}
                    >
                        +
                    </button>
                )}
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

            {chat ? (
                <div className="crs-chat-meta">
                    <span>
                        {agentName} · {modelLabel(chatBackend, chat.model)}
                        {cost && ` · ${cost}`}
                    </span>
                    <button
                        type="button"
                        className="crs-chat-link-button"
                        title="Delete this chat"
                        onClick={() => {
                            if (window.confirm('Delete this chat?')) state.deleteChat(chat.id);
                        }}
                    >
                        Delete
                    </button>
                </div>
            ) : (
                <BackendPicker state={state} />
            )}

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
                        The PR has new commits since this chat started. Start a new chat to give{' '}
                        {agentName} the latest diff.
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
                                goes with the PR's description and diff, and it can read the
                                repository, but not change anything.
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

/** The agent and model a new chat starts with. */
function BackendPicker({ state }: { state: ChatState }) {
    const { backends } = state;
    if (!backends || backends.length === 0) return null;
    const selected: ChatBackendInfo | undefined = backends.find(b => b.id === state.backend);
    return (
        <div className="crs-chat-picker">
            <label>
                <span>Agent</span>
                <select
                    className="crs-chat-select"
                    aria-label="Agent"
                    value={state.backend}
                    onChange={e => state.selectBackend(e.target.value)}
                >
                    {backends.map(b => (
                        <option key={b.id} value={b.id}>
                            {b.name}
                            {b.available ? '' : ' (unavailable)'}
                        </option>
                    ))}
                </select>
            </label>
            <label>
                <span>Model</span>
                <select
                    className="crs-chat-select"
                    aria-label="Model"
                    value={state.model}
                    disabled={!selected?.available}
                    onChange={e => state.selectModel(e.target.value)}
                >
                    {(selected?.models ?? []).map(m => (
                        <option key={m.id} value={m.id}>
                            {m.name}
                        </option>
                    ))}
                </select>
            </label>
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
