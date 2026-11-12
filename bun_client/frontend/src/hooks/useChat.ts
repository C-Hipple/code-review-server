import { useCallback, useEffect, useRef, useState } from 'react';
import {
    deleteChat as deleteChatRpc,
    getChat,
    listChatBackends,
    listChats,
    rpcErrorMessage,
    sendChatMessage,
    startChat,
    stopChat,
} from '../api';
import {
    CHAT_CHANGED_EVENT,
    loadChatPrefs,
    parseChatChanged,
    pickBackend,
    pickModel,
    saveChatPrefs,
    type Chat,
    type ChatBackendInfo,
    type ChatSummary,
} from '../chat_utils';
import { isChangeFor, serverEvents, type ServerEvents } from '../server_events';
import { useServerChanges } from './useServerChanges';

interface UseChatOptions {
    owner: string;
    repo: string;
    number: number;
    events?: ServerEvents;
}

export interface ChatState {
    /** Null until the server has said which backends it has. */
    backends: ChatBackendInfo[] | null;
    /** The backend and model the next new chat starts with. */
    backend: string;
    model: string;
    selectBackend: (id: string) => void;
    selectModel: (id: string) => void;
    /** The PR's chats, the most recently active first. */
    chats: ChatSummary[];
    /** The chat on screen; null for a new one not yet started. */
    chat: Chat | null;
    /** A request failed, or the server refused a message. */
    error: string | null;
    /** A message is on its way to the server. */
    sending: boolean;
    /** Sends a message: starts a chat when none is open, else continues it. Resolves true once the server took it. */
    send: (message: string) => Promise<boolean>;
    stop: () => void;
    /** Leaves the open chat for a new one. */
    newChat: () => void;
    openChat: (id: string) => void;
    deleteChat: (id: string) => void;
}

/**
 * The review chat for a PR. Opening the PR lists its chats and reopens the
 * most recent; the chat is read again whenever the server says it changed —
 * a message landing, the reply growing, a turn ending — so an answer streams
 * in without polling.
 */
export function useChat({ owner, repo, number, events = serverEvents }: UseChatOptions): ChatState {
    const [backends, setBackends] = useState<ChatBackendInfo[] | null>(null);
    const [backend, setBackend] = useState('');
    const [model, setModel] = useState('');
    const [chats, setChats] = useState<ChatSummary[]>([]);
    const [chat, setChat] = useState<Chat | null>(null);
    const [error, setError] = useState<string | null>(null);
    const [sending, setSending] = useState(false);
    const prKey = `${owner}/${repo}#${number}`;

    // The open chat's ID, read by the change handler and by replies landing
    // after the reviewer switched chats.
    const chatIdRef = useRef<string | null>(null);
    // Reads overlap while a reply streams in, and can come back out of order:
    // each takes a ticket, and only one newer than the last shown is shown.
    const lastTicket = useRef(0);
    const lastShown = useRef(0);
    const show = (ticket: number, next: Chat | null) => {
        if (ticket <= lastShown.current) return;
        if (next && next.id !== chatIdRef.current) return;
        lastShown.current = ticket;
        setChat(next);
    };
    const prKeyRef = useRef(prKey);

    const refreshList = useCallback(async () => {
        const key = prKey;
        const list = await listChats({ Owner: owner, Repo: repo, Number: number });
        if (prKeyRef.current === key) setChats(list);
        return list;
    }, [owner, repo, number, prKey]);

    const load = useCallback(async (id: string) => {
        const ticket = ++lastTicket.current;
        const next = await getChat(id);
        show(ticket, next);
    }, []);

    useEffect(() => {
        prKeyRef.current = prKey;
        chatIdRef.current = null;
        setChat(null);
        setChats([]);
        setError(null);
        let cancelled = false;
        (async () => {
            try {
                const [found, list] = await Promise.all([
                    listChatBackends(),
                    listChats({ Owner: owner, Repo: repo, Number: number }),
                ]);
                if (cancelled) return;
                setBackends(found);
                const prefs = loadChatPrefs();
                const picked = pickBackend(found, prefs.backend);
                setBackend(picked?.id ?? '');
                setModel(pickModel(picked, prefs.model));
                setChats(list);
                if (list.length > 0) {
                    chatIdRef.current = list[0].id;
                    await load(list[0].id);
                }
            } catch (e) {
                if (!cancelled) {
                    setBackends(prev => prev ?? []);
                    setError(rpcErrorMessage(e));
                }
            }
        })();
        return () => {
            cancelled = true;
        };
    }, [owner, repo, number]);

    useServerChanges(
        CHAT_CHANGED_EVENT,
        data => {
            const change = parseChatChanged(data);
            return !!change && isChangeFor(change, { owner, repo, number });
        },
        async () => {
            const id = chatIdRef.current;
            const list = await refreshList();
            if (!id) return;
            if (!list.some(c => c.id === id)) {
                // Deleted elsewhere.
                chatIdRef.current = null;
                setChat(null);
                return;
            }
            await load(id);
        },
        { key: prKey, events }
    );

    const send = async (message: string): Promise<boolean> => {
        const text = message.trim();
        if (!text || sending) return false;
        setSending(true);
        setError(null);
        try {
            const reply = chatIdRef.current
                ? await sendChatMessage(chatIdRef.current, text)
                : await startChat({
                      Owner: owner,
                      Repo: repo,
                      Number: number,
                      Backend: backend,
                      Model: model,
                      Message: text,
                  });
            if (reply.chat) {
                chatIdRef.current = reply.chat.id;
                show(++lastTicket.current, reply.chat);
                void refreshList().catch(() => {});
            }
            if (!reply.okay) {
                setError(reply.message || 'The server refused the message.');
                return false;
            }
            if (backend) saveChatPrefs(backend, model);
            return true;
        } catch (e) {
            setError(rpcErrorMessage(e));
            return false;
        } finally {
            setSending(false);
        }
    };

    const selectBackend = (id: string) => {
        setBackend(id);
        const picked = backends?.find(b => b.id === id) ?? null;
        setModel(pickModel(picked, null));
    };

    return {
        backends,
        backend,
        model,
        selectBackend,
        selectModel: setModel,
        chats,
        chat,
        error,
        sending,
        send,
        stop: () => {
            const id = chatIdRef.current;
            if (id) stopChat(id).catch(e => setError(rpcErrorMessage(e)));
        },
        newChat: () => {
            chatIdRef.current = null;
            lastShown.current = ++lastTicket.current;
            setChat(null);
            setError(null);
        },
        openChat: (id: string) => {
            chatIdRef.current = id;
            setError(null);
            load(id).catch(e => setError(rpcErrorMessage(e)));
        },
        deleteChat: (id: string) => {
            deleteChatRpc(id)
                .then(() => {
                    if (chatIdRef.current === id) {
                        chatIdRef.current = null;
                        setChat(null);
                    }
                    return refreshList();
                })
                .catch(e => setError(rpcErrorMessage(e)));
        },
    };
}
