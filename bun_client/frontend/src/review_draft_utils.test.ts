import { expect, test, describe } from 'bun:test';
import {
    clearReviewDraft,
    loadReviewDraft,
    reviewDraftKey,
    saveReviewDraft,
} from './review_draft_utils';

const memoryStorage = (): Storage => {
    const data = new Map<string, string>();
    return {
        get length() {
            return data.size;
        },
        clear: () => data.clear(),
        getItem: k => data.get(k) ?? null,
        key: i => [...data.keys()][i] ?? null,
        removeItem: k => void data.delete(k),
        setItem: (k, v) => void data.set(k, v),
    };
};

const PR = 'owner/repo#12';

describe('review drafts', () => {
    test('round-trips a draft per PR', () => {
        const s = memoryStorage();
        saveReviewDraft(PR, { body: 'looks good', event: 'APPROVE' }, s);
        expect(loadReviewDraft(PR, s)).toEqual({ body: 'looks good', event: 'APPROVE' });
        expect(loadReviewDraft('owner/repo#13', s)).toBeNull();
    });

    test('an empty body removes the draft', () => {
        const s = memoryStorage();
        saveReviewDraft(PR, { body: 'x', event: 'COMMENT' }, s);
        saveReviewDraft(PR, { body: '  ', event: 'COMMENT' }, s);
        expect(s.getItem(reviewDraftKey(PR))).toBeNull();
    });

    test('clear removes the draft', () => {
        const s = memoryStorage();
        saveReviewDraft(PR, { body: 'x', event: 'COMMENT' }, s);
        clearReviewDraft(PR, s);
        expect(loadReviewDraft(PR, s)).toBeNull();
    });

    test('a malformed entry reads as no draft', () => {
        const s = memoryStorage();
        s.setItem(reviewDraftKey(PR), 'not json');
        expect(loadReviewDraft(PR, s)).toBeNull();
        s.setItem(reviewDraftKey(PR), JSON.stringify({ body: 1 }));
        expect(loadReviewDraft(PR, s)).toBeNull();
    });

    test('storage that throws is ignored', () => {
        const s = memoryStorage();
        s.setItem = () => {
            throw new Error('quota');
        };
        s.getItem = () => {
            throw new Error('blocked');
        };
        expect(() => saveReviewDraft(PR, { body: 'x', event: 'COMMENT' }, s)).not.toThrow();
        expect(loadReviewDraft(PR, s)).toBeNull();
    });

    test('no storage is a no-op', () => {
        saveReviewDraft(PR, { body: 'x', event: 'COMMENT' }, null);
        expect(loadReviewDraft(PR, null)).toBeNull();
    });
});
