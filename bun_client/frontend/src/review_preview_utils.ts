import type { ParsedLine } from './diff_utils';
import { groupIntoThreads } from './discussion_utils';
import type { Comment } from './components/review/types';

/**
 * Pure helpers behind the review preview: what a "Submit Review" click is about
 * to post to GitHub, assembled from the local comments the reviewer has left
 * and the diff rows they were left on.
 *
 * Local comments carry no `diff_hunk` (the server has nothing to fill it with
 * until GitHub echoes the comment back), so context is recovered the same way
 * the diff anchors a comment in the first place: by (path, position) against
 * the parsed diff.
 */

/** How many diff rows of context a comment shows, the commented line included. */
export const DEFAULT_CONTEXT_LINES = 4;

export interface PendingCommentPreview {
    /** The unsubmitted comment, exactly as it will be posted. */
    comment: Comment;
    /** File the comment lands on. Empty when it cannot be resolved. */
    file: string;
    /** Diff rows leading up to and including the commented line. */
    context: ParsedLine[];
    /** Index into `context` of the commented line; -1 when it isn't in the diff. */
    anchorIndex: number;
    /** Head-side line number of the commented line, for the file:line label. */
    lineNo: number | null;
    /** True for a comment attached to the file rather than to one of its lines. */
    fileLevel: boolean;
    /** The comment this one answers, when it is a reply. */
    replyTo?: Comment;
}

/**
 * A comment's position as the diff means it: "0" and "" both say "not on a
 * line" — a comment left on the file header stores 0, and GitHub reports "" for
 * a comment whose line no longer maps into the diff.
 */
const normalizePosition = (position: string): string => (position === '0' ? '' : position);

/** The comments a submit would post: everything left locally, still unsent. */
export const pendingComments = (comments: Comment[]): Comment[] =>
    comments.filter(c => c.author === 'local');

/**
 * Diff rows ending at `position` in `file`: the commented line plus up to
 * `maxLines - 1` rows above it, stopping at a hunk or file boundary so context
 * never bleeds in from the hunk before.
 */
export function contextForPosition(
    parsedLines: ParsedLine[],
    file: string,
    position: string,
    maxLines: number = DEFAULT_CONTEXT_LINES
): { context: ParsedLine[]; anchorIndex: number; sourceIndex: number } {
    const empty = { context: [] as ParsedLine[], anchorIndex: -1, sourceIndex: -1 };
    if (!file || position === '' || maxLines < 1) return empty;

    const isCodeRow = (p: ParsedLine) =>
        p.lineType === 'addition' || p.lineType === 'deletion' || p.lineType === 'code';

    const anchor = parsedLines.findIndex(
        p => p.file === file && isCodeRow(p) && p.pos !== null && p.pos.toString() === position
    );
    if (anchor === -1) return empty;

    let start = anchor;
    while (start > 0 && anchor - start < maxLines - 1 && isCodeRow(parsedLines[start - 1])) {
        // Guard against a row from the file above sneaking in when a hunk
        // header is missing from the parse.
        if (parsedLines[start - 1].file !== file) break;
        start--;
    }

    return {
        context: parsedLines.slice(start, anchor + 1),
        anchorIndex: anchor - start,
        sourceIndex: anchor,
    };
}

/**
 * Build the preview rows for every unsubmitted comment, in the order they
 * appear in the diff so the preview reads top-to-bottom like the review does.
 * Comments that can't be placed (an outdated thread, a file that dropped out of
 * the diff) keep their input order at the end rather than being dropped: they
 * are still going out with the review.
 */
export function buildPendingPreviews(
    comments: Comment[],
    parsedLines: ParsedLine[],
    maxLines: number = DEFAULT_CONTEXT_LINES
): PendingCommentPreview[] {
    const byId = new Map<string, Comment>(comments.map(c => [c.id, c]));

    // A local reply carries no path or position of its own — it inherits both
    // from the thread it answers, so every comment needs its thread root.
    const rootById = new Map<string, Comment>();
    for (const thread of groupIntoThreads(comments)) {
        for (const c of thread) rootById.set(c.id, thread[0]);
    }

    const previews: { preview: PendingCommentPreview; sortKey: number; inputIndex: number }[] =
        pendingComments(comments).map((comment, inputIndex) => {
            const root = rootById.get(comment.id) ?? comment;
            const file = comment.path || root.path || '';
            const position =
                normalizePosition(comment.position) || normalizePosition(root.position);
            const { context, anchorIndex, sourceIndex } = contextForPosition(
                parsedLines,
                file,
                position,
                maxLines
            );
            const anchorLine = anchorIndex >= 0 ? context[anchorIndex] : null;
            const replyTo = comment.in_reply_to
                ? byId.get(comment.in_reply_to.toString())
                : undefined;

            return {
                preview: {
                    comment,
                    file,
                    context,
                    anchorIndex,
                    lineNo: anchorLine
                        ? (anchorLine.newLineNo ?? anchorLine.oldLineNo ?? null)
                        : null,
                    fileLevel: !!file && position === '',
                    replyTo,
                },
                // Diff order, with the unplaceable ones after everything placed.
                sortKey: sourceIndex >= 0 ? sourceIndex : Infinity,
                inputIndex,
            };
        });

    previews.sort((a, b) =>
        a.sortKey === b.sortKey ? a.inputIndex - b.inputIndex : a.sortKey - b.sortKey
    );
    return previews.map(p => p.preview);
}

/** How GitHub will label the review once it is submitted. */
export function reviewVerdictLabel(event: string, user?: string): string {
    const who = user?.trim();
    switch (event) {
        case 'APPROVE':
            return who ? `Approved by ${who}` : 'Approved';
        case 'REQUEST_CHANGES':
            return who ? `Changes requested by ${who}` : 'Changes requested';
        default:
            return who ? `Commented by ${who}` : 'Commented';
    }
}
