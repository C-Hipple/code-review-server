import { describe, expect, test } from 'bun:test';
import { createRef } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import type { Chat, ChatBackendInfo } from './chat_utils';
import ChatWidget, { ChatPanel } from './components/ChatWidget';
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
        backend: 'claude',
        model: '',
        selectBackend: () => {},
        selectModel: () => {},
        chats: [],
        chat: null,
        error: null,
        sending: false,
        send: async () => true,
        stop: () => {},
        newChat: () => {},
        openChat: () => {},
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
    test('a new chat offers the agent and model, and says what goes with the message', () => {
        const html = panel();
        expect(html).toContain('>Claude Code</option>');
        expect(html).toContain('>Sonnet</option>');
        expect(html).toContain('Ask Claude Code about this PR');
        expect(html).toContain('description and diff');
        expect(html).toContain('Ask about this PR');
        // Nothing typed yet.
        expect(html).toMatch(/<button type="submit" class="crs-chat-send" disabled=""/);
    });

    test('an unavailable agent says why and can’t be written to', () => {
        const html = panel({
            backends: [{ ...claude, available: false, reason: 'claude isn’t installed' }],
        });
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
        expect(html).toContain('<option value="c1" selected="">Why the new field?</option>');
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
    });

    test('a chat about an older head says so', () => {
        const html = panel({ chat: { ...chat, current_sha: 'sha-2' } });
        expect(html).toContain('The PR has new commits since this chat started');
    });

    test('an error shows', () => {
        expect(panel({ error: 'the agent is still answering' })).toContain(
            'role="alert">the agent is still answering'
        );
    });
});
