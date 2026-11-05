/**
 * The Submit Review modal's draft, kept in localStorage per PR so closing the
 * modal (or the tab) doesn't throw away a half-written review. Cleared once
 * the review is submitted.
 */

export interface ReviewDraft {
    body: string;
    event: string;
}

const KEY_PREFIX = 'reviewDraft:';

/** The storage key for a PR's draft, e.g. `reviewDraft:owner/repo#12`. */
export const reviewDraftKey = (prRef: string): string => `${KEY_PREFIX}${prRef}`;

const defaultStorage = (): Storage | null => {
    try {
        return typeof localStorage === 'undefined' ? null : localStorage;
    } catch {
        return null;
    }
};

/** The saved draft for a PR, or null when there is none (or it can't be read). */
export function loadReviewDraft(
    prRef: string,
    storage: Storage | null = defaultStorage()
): ReviewDraft | null {
    try {
        const raw = storage?.getItem(reviewDraftKey(prRef));
        if (!raw) return null;
        const parsed = JSON.parse(raw);
        if (typeof parsed?.body !== 'string' || typeof parsed?.event !== 'string') return null;
        return { body: parsed.body, event: parsed.event };
    } catch {
        return null;
    }
}

/**
 * Saves a PR's draft. An empty body removes it, so a cleared modal falls back
 * to the review feedback again on the next open.
 */
export function saveReviewDraft(
    prRef: string,
    draft: ReviewDraft,
    storage: Storage | null = defaultStorage()
): void {
    try {
        if (draft.body.trim() === '') {
            storage?.removeItem(reviewDraftKey(prRef));
        } else {
            storage?.setItem(reviewDraftKey(prRef), JSON.stringify(draft));
        }
    } catch {
        // Storage full or blocked: the draft just lives in memory.
    }
}

export function clearReviewDraft(prRef: string, storage: Storage | null = defaultStorage()): void {
    try {
        storage?.removeItem(reviewDraftKey(prRef));
    } catch {
        // Nothing to clear.
    }
}
