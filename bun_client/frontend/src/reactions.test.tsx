import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import Reactions, { reactionTooltip } from './components/review/Reactions';
import type { Comment, Reaction, ReviewData } from './components/review/types';
import { buildDiscussionTimeline } from './discussion_utils';

const reaction = (over: Partial<Reaction> = {}): Reaction => ({
    content: '+1',
    emoji: '👍',
    users: ['alice'],
    count: 1,
    viewer_reacted: false,
    ...over,
});

const comment = (over: Partial<Comment> & { id: string }): Comment => ({
    author: 'alice',
    body: 'looks good',
    path: '',
    position: '',
    in_reply_to: 0,
    created_at: '2026-01-01T00:00:00Z',
    outdated: false,
    diff_hunk: '',
    ...over,
});

const review = (over: Partial<ReviewData> & { id: number }): ReviewData => ({
    user: 'bob',
    body: 'nice work',
    state: 'APPROVED',
    submitted_at: '2026-01-02T00:00:00Z',
    html_url: '',
    ...over,
});

describe('reactionTooltip', () => {
    test('names everyone who reacted', () => {
        expect(reactionTooltip(reaction({ users: ['alice', 'bob'], count: 2 }))).toBe(
            'alice, bob reacted with :+1:'
        );
    });

    // The server caps how many logins it asks GitHub for, so the count can run
    // ahead of the names. Saying so beats silently showing a short list.
    test('reports the reactors it does not have names for', () => {
        expect(reactionTooltip(reaction({ users: ['alice'], count: 4 }))).toBe(
            'alice and 3 more reacted with :+1:'
        );
    });

    test('falls back to a count when no names came back', () => {
        expect(reactionTooltip(reaction({ users: [], count: 2 }))).toBe('2 reacted with :+1:');
    });
});

describe('Reactions', () => {
    test('renders the emoji, the count, and the reactors on hover', () => {
        const html = renderToStaticMarkup(
            <Reactions reactions={[reaction({ users: ['alice', 'bob'], count: 2 })]} />
        );
        expect(html).toContain('👍');
        expect(html).toContain('>2<');
        expect(html).toContain('alice, bob reacted with :+1:');
    });

    // Every comment card renders this unconditionally, so "no reactions" has to
    // cost nothing on screen.
    test('renders nothing without reactions', () => {
        expect(renderToStaticMarkup(<Reactions reactions={[]} />)).toBe('');
        expect(renderToStaticMarkup(<Reactions />)).toBe('');
    });

    test('shows a name for an emoji it does not know', () => {
        const html = renderToStaticMarkup(
            <Reactions reactions={[reaction({ content: 'party_popper', emoji: '' })]} />
        );
        expect(html).toContain(':party_popper:');
    });
});

describe('buildDiscussionTimeline reactions', () => {
    test('carries a review body reactions onto its timeline entry', () => {
        const entries = buildDiscussionTimeline(
            [review({ id: 900, reactions: [reaction({ content: 'hooray', emoji: '🎉' })] })],
            [],
            []
        );
        expect(entries).toHaveLength(1);
        expect(entries[0].reactions?.[0].content).toBe('hooray');
    });

    test('carries a standalone comment reactions onto its entry', () => {
        const entries = buildDiscussionTimeline(
            [],
            [comment({ id: '1', reactions: [reaction({ users: ['bob'] })] })],
            []
        );
        expect(entries).toHaveLength(1);
        expect(entries[0].reactions?.[0].users).toEqual(['bob']);
    });

    // An inline thread renders its comments individually, each with its own
    // chips, so the entry wrapping it must not repeat the root's.
    test('leaves an inline thread entry without entry-level reactions', () => {
        const entries = buildDiscussionTimeline(
            [],
            [
                comment({
                    id: '1',
                    path: 'src/a.ts',
                    position: '3',
                    reactions: [reaction()],
                }),
            ],
            []
        );
        expect(entries).toHaveLength(1);
        expect(entries[0].reactions).toBeUndefined();
        expect(entries[0].threads[0].comments[0].reactions?.[0].content).toBe('+1');
    });
});
