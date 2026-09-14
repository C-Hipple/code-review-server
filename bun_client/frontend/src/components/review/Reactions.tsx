import { colors } from '../../design';
import type { Reaction } from './types';

interface ReactionsProps {
    reactions?: Reaction[];
    /** Smaller chips, for the dense comment cards pinned inside the diff. */
    compact?: boolean;
}

/**
 * The hover text for one reaction: who left it, and how many names GitHub
 * didn't hand over. Exported for tests — the list is the whole point of
 * showing reactions at all, so it is worth pinning down.
 */
export function reactionTooltip(reaction: Reaction): string {
    const names = reaction.users.join(', ');
    const hidden = reaction.count - reaction.users.length;
    if (!names) return `${reaction.count} reacted with :${reaction.content}:`;
    const suffix = hidden > 0 ? ` and ${hidden} more` : '';
    return `${names}${suffix} reacted with :${reaction.content}:`;
}

/**
 * The emoji reactions on a comment or review, as chips that name their
 * reactors on hover. Reading a thread, this is what answers "did anyone
 * actually take my comment on board?" — a 👍 from the author is an
 * acknowledgement that never shows up as a reply.
 *
 * Renders nothing when there are no reactions, so callers can drop it in
 * unconditionally.
 */
export default function Reactions({ reactions, compact = false }: ReactionsProps) {
    if (!reactions || reactions.length === 0) return null;

    return (
        <div
            style={{
                display: 'flex',
                flexWrap: 'wrap',
                gap: '4px',
                marginTop: compact ? '5px' : '8px',
            }}
        >
            {reactions.map(r => (
                <span
                    key={r.content}
                    title={reactionTooltip(r)}
                    style={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: '4px',
                        fontSize: compact ? '11px' : '12px',
                        lineHeight: 1.4,
                        padding: compact ? '0 6px' : '1px 8px',
                        borderRadius: '10px',
                        // Your own reaction is picked out the way GitHub picks
                        // it out, so "did I already acknowledge this?" reads off
                        // the chip rather than off the tooltip.
                        background: r.viewer_reacted ? colors.bgInfoDim : 'var(--bg-tertiary)',
                        border: `1px solid ${r.viewer_reacted ? colors.accent : 'var(--border)'}`,
                        color: r.viewer_reacted ? colors.accent : 'var(--text-secondary)',
                        cursor: 'default',
                        userSelect: 'none' as const,
                    }}
                >
                    <span>{r.emoji || `:${r.content}:`}</span>
                    <span style={{ fontWeight: 600 }}>{r.count}</span>
                </span>
            ))}
        </div>
    );
}
