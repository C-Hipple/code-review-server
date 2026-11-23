import { describe, expect, test } from 'bun:test';
import {
    agentBackend,
    attachedLabel,
    chatIsStale,
    elapsedLabel,
    formatCost,
    isSendKey,
    modelLabel,
    parseChatChanged,
    pickModel,
    shouldCloseOnLeave,
    type ChatBackendInfo,
    type ChatMessage,
} from './chat_utils';

const claude: ChatBackendInfo = {
    id: 'claude',
    name: 'Claude Code',
    available: true,
    models: [
        { id: '', name: 'Default' },
        { id: 'opus', name: 'Opus' },
        { id: 'sonnet', name: 'Sonnet' },
    ],
    default_model: 'sonnet',
};
const codex: ChatBackendInfo = {
    id: 'codex',
    name: 'Codex',
    available: false,
    reason: 'not installed',
    models: [{ id: '', name: 'Default' }],
    default_model: '',
};

describe('parseChatChanged', () => {
    test('reads a notification', () => {
        expect(
            parseChatChanged({
                owner: 'acme',
                repo: 'widgets',
                number: 42,
                chat_id: 'c1',
                status: 'running',
            })
        ).toEqual({ owner: 'acme', repo: 'widgets', number: 42, chat_id: 'c1', status: 'running' });
    });

    test('rejects one that names no chat', () => {
        expect(parseChatChanged({ owner: 'acme', repo: 'widgets', number: 42 })).toBeNull();
        expect(parseChatChanged(null)).toBeNull();
    });
});

describe('agentBackend and pickModel', () => {
    test('take the agent the server names, available or not', () => {
        expect(agentBackend({ backends: [codex, claude], agent: 'claude' })?.id).toBe('claude');
        expect(agentBackend({ backends: [codex, claude], agent: 'codex' })?.id).toBe('codex');
    });

    test('stand in for an agent the server names but does not list', () => {
        const missing = agentBackend({ backends: [claude], agent: 'gemini' });
        expect(missing?.id).toBe('gemini');
        expect(missing?.available).toBe(false);
        expect(missing?.reason).toContain("doesn't define it");
    });

    test('fall back, for a server that names none, to the first available, else the first', () => {
        expect(agentBackend({ backends: [codex, claude], agent: '' })?.id).toBe('claude');
        expect(agentBackend({ backends: [codex], agent: '' })?.id).toBe('codex');
        expect(agentBackend({ backends: [], agent: '' })).toBeNull();
    });

    test('keep a remembered model the backend offers, else its default', () => {
        expect(pickModel(claude, 'opus')).toBe('opus');
        expect(pickModel(claude, '')).toBe('');
        expect(pickModel(claude, 'gpt')).toBe('sonnet');
        expect(pickModel(claude, null)).toBe('sonnet');
        expect(pickModel(null, 'opus')).toBe('');
    });

    test('label a model by the backend’s name for it', () => {
        expect(modelLabel(claude, 'opus')).toBe('Opus');
        expect(modelLabel(claude, '')).toBe('Default');
        expect(modelLabel(claude, 'claude-x-1')).toBe('claude-x-1');
        expect(modelLabel(undefined, '')).toBe('Default');
    });
});

describe('chat details', () => {
    test('a chat goes stale when the PR moves on', () => {
        expect(chatIsStale({ head_sha: 'a', current_sha: 'b' })).toBe(true);
        expect(chatIsStale({ head_sha: 'a', current_sha: 'a' })).toBe(false);
        expect(chatIsStale({ head_sha: '', current_sha: 'b' })).toBe(false);
    });

    test('costs', () => {
        expect(formatCost(0)).toBeNull();
        expect(formatCost(0.004)).toBe('<$0.01');
        expect(formatCost(0.256)).toBe('$0.26');
    });

    test('elapsed time', () => {
        const start = '2026-11-12T10:00:00Z';
        const at = (s: number) => Date.parse(start) + s * 1000;
        expect(elapsedLabel(start, at(12))).toBe('12s');
        expect(elapsedLabel(start, at(185))).toBe('3m 05s');
        expect(elapsedLabel(undefined, at(1))).toBe('');
        expect(elapsedLabel('nonsense', at(1))).toBe('');
    });

    test('what a message attached', () => {
        const message: ChatMessage = {
            id: 1,
            role: 'user',
            content: 'hi',
            created_at: '',
            attached: ['description', 'diff'],
            head_sha: '0123456789abcdef',
        };
        expect(attachedLabel(message)).toBe('PR description and diff attached at 0123456');
        expect(attachedLabel({ ...message, diff_truncated: true, head_sha: '' })).toBe(
            'PR description and diff (cut to fit) attached'
        );
        expect(attachedLabel({ ...message, attached: undefined })).toBeNull();
    });
});

describe('panel behavior', () => {
    test('a hover-opened panel closes when the reviewer is not working in it', () => {
        expect(shouldCloseOnLeave({ pinned: false, focusInside: false, draft: '' })).toBe(true);
        expect(shouldCloseOnLeave({ pinned: false, focusInside: false, draft: '  ' })).toBe(true);
        expect(shouldCloseOnLeave({ pinned: true, focusInside: false, draft: '' })).toBe(false);
        expect(shouldCloseOnLeave({ pinned: false, focusInside: true, draft: '' })).toBe(false);
        expect(shouldCloseOnLeave({ pinned: false, focusInside: false, draft: 'Why' })).toBe(false);
    });

    test('Enter sends; Shift+Enter and composing do not', () => {
        expect(isSendKey({ key: 'Enter', shiftKey: false })).toBe(true);
        expect(isSendKey({ key: 'Enter', shiftKey: true })).toBe(false);
        expect(
            isSendKey({ key: 'Enter', shiftKey: false, nativeEvent: { isComposing: true } })
        ).toBe(false);
        expect(isSendKey({ key: 'a', shiftKey: false })).toBe(false);
    });
});
