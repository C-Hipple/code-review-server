import { describe, expect, test } from 'bun:test';
import { createRef } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import type { Chat, ChatBackendInfo, ChatSummary } from './chat_utils';
import ChatWidget, { ChatPanel, SessionList } from './components/ChatWidget';
import type { ChatState } from './hooks/useChat';

const claude: ChatBackendInfo = {
    id: 'claude',
    name: 'Claude Code',
    available: true,
    models: [
        { id: '', name: 'Default' },
        { id: 'sonnet', name: 'Sonnet' },
    ],
    default_model: '',
};

const chat: Chat = {
    id: 'c1',
    owner: 'acme',
    repo: 'widgets',
    number: 42,
    backend: 'claude',
    model: 'sonnet',
    title: 'Why the new field?',
    head_sha: 'sha-1',
    current_sha: 'sha-1',
    cost_usd: 0.04,
    status: 'idle',
    created_at: '',
    updated_at: '',
    draft: '',
    messages: [
        {
            id: 1,
            role: 'user',
            content: 'Why the new field?',
            created_at: '',
            attached: ['description', 'diff'],
            head_sha: 'sha-1',
        },
        {
            id: 2,
            role: 'tool',
            content: 'pr.diff',
            tool: 'Read',
            tool_status: 'done',
            created_at: '',
        },
        { id: 3, role: 'assistant', content: 'It adds **punctuation**.', created_at: '' },
        { id: 4, role: 'error', content: 'claude failed: boom', created_at: '' },
        { id: 5, role: 'notice', content: 'Stopped.', created_at: '' },
    ],
};

function state(fields: Partial<ChatState> = {}): ChatState {
    return {
        backends: [claude],
        agent: claude,
        model: '',
        selectModel: () => {},
        chats: [],
        chat: null,
        error: null,
        sending: false,
        send: async () => true,
        stop: () => {},
        newChat: () => {},
        openChat: () => {},
        forkChat: async () => true,
        renameChat: async () => true,
        deleteChat: () => {},
        ...fields,
    };
}

function panel(fields: Partial<ChatState> = {}, input = '') {
    return renderToStaticMarkup(
        <ChatPanel
            state={state(fields)}
            input={input}
            onInput={() => {}}
            onSubmit={() => {}}
            pinned={false}
            onTogglePin={() => {}}
            onClose={() => {}}
            textareaRef={createRef()}
        />
    );
}

describe('ChatWidget', () => {
    test('starts as just the button', () => {
        const html = renderToStaticMarkup(<ChatWidget owner="acme" repo="widgets" number={42} />);
        expect(html).toContain('aria-label="Chat with an agent about this PR"');
        expect(html).toContain('aria-expanded="false"');
        expect(html).not.toContain('crs-chat-panel');
        expect(html).not.toContain('crs-chat--raised');
    });

    test('sits above the mobile review bar when raised', () => {
        const html = renderToStaticMarkup(
            <ChatWidget owner="acme" repo="widgets" number={42} raised />
        );
        expect(html).toContain('crs-chat--raised');
    });
});

describe('ChatPanel', () => {
    test('a new chat names the agent, offers its models, and says what goes with the message', () => {
        const html = panel();
        // The server's config picks the agent: it is named, not offered.
        expect(html).toContain('<span class="crs-chat-agent"');
        expect(html).toContain('>Claude Code</span>');
        expect(html).not.toContain('aria-label="Agent"');
        expect(html).toContain('>Sonnet</option>');
        expect(html).toContain('Ask Claude Code about this PR');
        expect(html).toContain('description and diff');
        expect(html).toContain('Ask about this PR');
        // Nothing typed yet.
        expect(html).toMatch(/<button type="submit" class="crs-chat-send" disabled=""/);
        // The header names the new session; there is nothing to fork or clear yet.
        expect(html).toContain('aria-label="Sessions: New session"');
        expect(html).not.toContain('crs-chat-sessions-count');
        expect(html).toMatch(/aria-label="New session" disabled=""/);
        expect(html).toMatch(/aria-label="Fork session" disabled=""/);
    });

    test('an agent with only its default model offers no model to pick', () => {
        const gemini: ChatBackendInfo = {
            id: 'gemini',
            name: 'Gemini CLI',
            available: true,
            models: [{ id: '', name: 'Default' }],
            default_model: '',
        };
        const html = panel({ backends: [claude, gemini], agent: gemini });
        expect(html).toContain('>Gemini CLI</span>');
        expect(html).not.toContain('aria-label="Model"');
        expect(html).toContain('Ask Gemini CLI about this PR');
    });

    test('an unavailable agent says why and can’t be written to', () => {
        const unavailable = { ...claude, available: false, reason: 'claude isn’t installed' };
        const html = panel({ backends: [unavailable], agent: unavailable });
        expect(html).toContain('Claude Code isn&#x27;t available.');
        expect(html).toContain('claude isn’t installed');
        expect(html).toMatch(/<textarea[^>]*disabled=""/);
    });

    test('a chat shows its messages', () => {
        const html = panel({ chat, chats: [chat] }, 'next question');
        expect(html).toContain('Claude Code · Sonnet · $0.04');
        expect(html).toContain('Why the new field?');
        expect(html).toContain('PR description and diff attached at sha-1');
        expect(html).toContain('crs-chat-tool--done');
        expect(html).toContain('pr.diff');
        expect(html).toContain('<strong>punctuation</strong>');
        expect(html).toContain('claude failed: boom');
        expect(html).toContain('Stopped.');
        // The header names the open session and counts the PR's sessions.
        expect(html).toContain('aria-label="Sessions: Why the new field?"');
        expect(html).toContain('<span class="crs-chat-sessions-count">1</span>');
        expect(html).not.toMatch(/aria-label="New session" disabled=""/);
        expect(html).not.toMatch(/aria-label="Fork session" disabled=""/);
        expect(html).not.toContain('crs-chat-picker');
        expect(html).toContain('>Send</button>');
    });

    test('a running chat streams its draft and offers Stop', () => {
        const html = panel({ chat: { ...chat, status: 'running', draft: 'Looking at `greet`' } });
        expect(html).toContain('<code>greet</code>');
        expect(html).toContain('>Stop</button>');
        expect(html).not.toContain('>Send</button>');

        const thinking = panel({ chat: { ...chat, status: 'running', draft: '' } });
        expect(thinking).toContain('Claude Code is working…');
        // A session can't be forked while its agent is answering.
        expect(html).toMatch(/aria-label="Fork session" disabled=""/);
    });

    test('a chat about an older head offers to fork it or start afresh', () => {
        const html = panel({ chat: { ...chat, current_sha: 'sha-2' } });
        expect(html).toContain('The PR has new commits since this session attached its diff');
        expect(html).toContain('>Fork it</button>');
        expect(html).toContain('>start a new session</button>');

        const running = panel({ chat: { ...chat, current_sha: 'sha-2', status: 'running' } });
        expect(running).not.toContain('>Fork it</button>');
        expect(running).toContain('Fork it once the agent is done');
    });

    test('an error shows', () => {
        expect(panel({ error: 'the agent is still answering' })).toContain(
            'role="alert">the agent is still answering'
        );
    });
});

describe('SessionList', () => {
    const summary = (fields: Partial<ChatSummary>): ChatSummary => ({
        id: 'c1',
        owner: 'acme',
        repo: 'widgets',
        number: 42,
        backend: 'claude',
        model: '',
        title: 'Why the new field?',
        head_sha: 'sha-1',
        cost_usd: 0,
        status: 'idle',
        created_at: '',
        updated_at: '',
        ...fields,
    });
    const sessions = [
        summary({
            id: 'c2',
            title: 'Fork: Why the new field?',
            forked_from: 'c1',
            status: 'running',
            turns: 1,
            model: 'sonnet',
            preview: 'Reading the diff again',
        }),
        summary({ turns: 2, cost_usd: 0.04, preview: 'It adds **punctuation**.' }),
        summary({ id: 'c0', title: '', turns: 0 }),
    ];
    const list = (fields: Partial<ChatState> = {}) =>
        renderToStaticMarkup(
            <SessionList
                state={state({ chats: sessions, ...fields })}
                onOpen={() => {}}
                onNew={() => {}}
                onClose={() => {}}
            />
        );

    test('lists every session to open, rename or delete, after a new one', () => {
        const html = list({ chat: { ...chat, id: 'c1' } });
        expect(html).toContain('aria-label="Sessions on this PR"');
        expect(html.indexOf('+ New session')).toBeLessThan(
            html.indexOf('Fork: Why the new field?')
        );
        expect(html).toContain('<span class="crs-chat-session-name">Why the new field?</span>');
        expect(html).toContain('<span class="crs-chat-session-name">Untitled session</span>');
        // The running fork is marked and tagged; the details and preview read.
        expect(html).toContain('class="crs-chat-session-dot"');
        expect(html).toContain('<span class="crs-chat-session-tag">fork</span>');
        expect(html).toContain('1 turn · Sonnet');
        expect(html).toContain('2 turns · Default · $0.04');
        expect(html).toContain('0 turns');
        expect(html).toContain('Reading the diff again');
        // The open session is marked as current.
        expect(html.match(/aria-current="true"/g)).toHaveLength(1);
        expect(html).toMatch(/aria-current="true">.*?Why the new field\?<\/span>/);
        expect(html).toContain('aria-label="Rename Why the new field?"');
        expect(html).toContain('aria-label="Delete Untitled session"');
    });

    test('a new session is the current one when none is open', () => {
        const html = list();
        expect(html).toContain('crs-chat-session-new crs-chat-session-open--current');
        expect(html).not.toContain('aria-current="true"');
    });

    test('a PR with no sessions says so', () => {
        expect(list({ chats: [] })).toContain('No sessions on this PR yet.');
    });
});
