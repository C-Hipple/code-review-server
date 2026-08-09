import { useMemo, useState } from 'react';
import Markdown from 'react-markdown';
import { colors } from '../../design';
import {
    buildDiscussionTimeline,
    buildReplyTree,
    flattenReplyTree,
    relativeTime,
    summarizeDiscussion,
    type ThreadSummary,
    type TimelineEntry,
} from '../../discussion_utils';
import ThreadStateBadges from './ThreadStateBadges';
import { type Comment, type ReviewData, stripHtmlComments } from './types';

interface PRDiscussionProps {
    reviews: ReviewData[];
    comments: Comment[];
    outdatedComments: Comment[];
    // Jump the diff to a thread's file. Threads on files that are no longer in
    // the diff simply won't scroll anywhere, so this is best-effort.
    onJumpToFile?: (file: string) => void;
}

const stateLabel = (state: string): string => {
    switch (state) {
        case 'APPROVED':
            return 'approved';
        case 'CHANGES_REQUESTED':
            return 'requested changes';
        case 'DISMISSED':
            return 'review dismissed';
        default:
            return 'commented';
    }
};

const stateChipStyle = (state: string) => {
    const base = {
        fontSize: '11px',
        padding: '1px 8px',
        borderRadius: '10px',
        fontWeight: 600,
        whiteSpace: 'nowrap' as const,
    };
    if (state === 'APPROVED') {
        return {
            ...base,
            background: colors.bgSuccessDim,
            color: colors.success,
            border: `1px solid ${colors.borderSuccessDim}`,
        };
    }
    if (state === 'CHANGES_REQUESTED') {
        return {
            ...base,
            background: colors.bgDangerDim,
            color: colors.danger,
            border: `1px solid ${colors.borderDangerDim}`,
        };
    }
    return {
        ...base,
        background: 'var(--bg-tertiary)',
        color: 'var(--text-secondary)',
        border: '1px solid var(--border)',
    };
};

// "4 comments · 3 resolved · 1 outdated" — the one-line verdict on a review,
// which is the thing you actually want when scanning back through a PR.
function threadCounts(threads: ThreadSummary[]): string {
    if (threads.length === 0) return '';
    const parts = [`${threads.length} ${threads.length === 1 ? 'comment' : 'comments'}`];
    const resolved = threads.filter(t => t.resolved).length;
    const outdated = threads.filter(t => t.outdated).length;
    if (resolved > 0) {
        parts.push(resolved === threads.length ? 'all resolved' : `${resolved} resolved`);
    }
    if (outdated > 0) parts.push(`${outdated} outdated`);
    return parts.join(' · ');
}

// One review comment thread inside the timeline: file location, state badges,
// and the conversation itself indented by reply depth.
function TimelineThread({
    thread,
    onJumpToFile,
}: {
    thread: ThreadSummary;
    onJumpToFile?: (file: string) => void;
}) {
    // Resolved conversations start folded — that's the point of resolving them.
    const [expanded, setExpanded] = useState(!thread.resolved);
    const nodes = useMemo(() => flattenReplyTree(buildReplyTree(thread.comments)), [thread]);
    const root = thread.comments[0];

    return (
        <div
            style={{
                border: '1px solid var(--border)',
                borderRadius: '6px',
                background: 'var(--bg-primary)',
                overflow: 'hidden',
                opacity: thread.resolved ? 0.75 : 1,
            }}
        >
            <div
                style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: '8px',
                    flexWrap: 'wrap',
                    padding: '6px 10px',
                    background: 'var(--bg-secondary)',
                    borderBottom: expanded ? '1px solid var(--border)' : 'none',
                }}
            >
                <button
                    type="button"
                    onClick={() => setExpanded(e => !e)}
                    aria-expanded={expanded}
                    style={{
                        background: 'none',
                        border: 'none',
                        cursor: 'pointer',
                        padding: 0,
                        fontSize: '10px',
                        color: 'var(--text-secondary)',
                        transform: expanded ? 'rotate(0deg)' : 'rotate(-90deg)',
                        transition: 'transform 0.15s ease',
                    }}
                    title={expanded ? 'Collapse this conversation' : 'Expand this conversation'}
                >
                    ▼
                </button>
                <button
                    type="button"
                    onClick={() => root.path && onJumpToFile?.(root.path)}
                    disabled={!root.path || !onJumpToFile}
                    style={{
                        background: 'none',
                        border: 'none',
                        padding: 0,
                        fontFamily: 'var(--font-mono, monospace)',
                        fontSize: '12px',
                        color:
                            root.path && onJumpToFile ? 'var(--accent)' : 'var(--text-secondary)',
                        cursor: root.path && onJumpToFile ? 'pointer' : 'default',
                        textAlign: 'left',
                    }}
                    title={root.path ? `Jump to ${root.path} in the diff` : undefined}
                >
                    {root.path || 'general comment'}
                </button>
                <ThreadStateBadges
                    resolved={thread.resolved}
                    resolvedBy={thread.resolvedBy}
                    outdated={thread.outdated}
                    replyCount={thread.comments.length - 1}
                />
                {!expanded && (
                    <span style={{ fontSize: '11px', color: 'var(--text-tertiary)' }}>
                        {thread.participants.join(', ')}
                    </span>
                )}
            </div>
            {expanded &&
                nodes.map(({ comment: c, depth }) => (
                    <div
                        key={c.id}
                        style={{
                            padding: '8px 10px',
                            marginLeft: `${Math.min(depth, 4) * 16}px`,
                            borderLeft: depth > 0 ? '2px solid var(--border)' : 'none',
                            borderTop: '1px solid var(--border)',
                        }}
                    >
                        <div
                            style={{
                                display: 'flex',
                                gap: '8px',
                                alignItems: 'baseline',
                                fontSize: '11px',
                                color: 'var(--text-secondary)',
                                marginBottom: '4px',
                            }}
                        >
                            {depth > 0 && <span style={{ opacity: 0.6 }}>↳</span>}
                            <span style={{ fontWeight: 600, color: 'var(--text-primary)' }}>
                                @{c.author}
                            </span>
                            <span>{relativeTime(c.created_at)}</span>
                        </div>
                        <div
                            className="markdown-content"
                            style={{ fontSize: '13px', lineHeight: 1.55 }}
                        >
                            <Markdown>{stripHtmlComments(c.body)}</Markdown>
                        </div>
                    </div>
                ))}
        </div>
    );
}

function TimelineRow({
    entry,
    onJumpToFile,
}: {
    entry: TimelineEntry;
    onJumpToFile?: (file: string) => void;
}) {
    const counts = threadCounts(entry.threads);
    return (
        <div
            style={{
                padding: '14px 16px',
                borderBottom: '1px solid var(--border)',
                display: 'flex',
                flexDirection: 'column',
                gap: '10px',
            }}
        >
            <div
                style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: '8px',
                    flexWrap: 'wrap',
                }}
            >
                <span style={{ fontWeight: 600, fontSize: '13px', color: 'var(--text-primary)' }}>
                    @{entry.author}
                </span>
                <span style={stateChipStyle(entry.state)}>{stateLabel(entry.state)}</span>
                {counts && (
                    <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
                        with {counts}
                    </span>
                )}
                <span
                    style={{
                        fontSize: '11px',
                        color: 'var(--text-secondary)',
                        marginLeft: 'auto',
                    }}
                    title={entry.timestamp ? new Date(entry.timestamp).toLocaleString() : undefined}
                >
                    {relativeTime(entry.timestamp)}
                </span>
                {entry.htmlUrl && (
                    <a
                        href={entry.htmlUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        style={{ fontSize: '11px', color: 'var(--accent)', textDecoration: 'none' }}
                    >
                        View
                    </a>
                )}
            </div>
            {entry.body.trim() && (
                <div
                    className="pr-description"
                    style={{ fontSize: '14px', lineHeight: 1.6, color: 'var(--text-primary)' }}
                >
                    <Markdown>{stripHtmlComments(entry.body)}</Markdown>
                </div>
            )}
            {entry.threads.length > 0 && (
                <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                    {entry.threads.map(t => (
                        <TimelineThread key={t.rootId} thread={t} onJumpToFile={onJumpToFile} />
                    ))}
                </div>
            )}
        </div>
    );
}

// The PR's conversation history: every review in chronological order with the
// comments it was submitted alongside, so "I requested changes yesterday with
// these four comments, three now resolved and one outdated" reads off the page.
// Collapsed by default so the diff stays near the top.
export default function PRDiscussion({
    reviews,
    comments,
    outdatedComments,
    onJumpToFile,
}: PRDiscussionProps) {
    const [collapsed, setCollapsed] = useState(true);
    const timeline = useMemo(
        () => buildDiscussionTimeline(reviews, comments, outdatedComments),
        [reviews, comments, outdatedComments]
    );
    const summary = useMemo(() => summarizeDiscussion(timeline), [timeline]);

    if (timeline.length === 0) return null;

    // The "N open" chip below already carries the unresolved count, so this line
    // stays to the totals.
    const headline = [
        `${summary.entries} ${summary.entries === 1 ? 'entry' : 'entries'}`,
        summary.threads > 0
            ? `${summary.threads} ${summary.threads === 1 ? 'thread' : 'threads'}`
            : '',
        summary.resolvedThreads > 0 ? `${summary.resolvedThreads} resolved` : '',
        summary.outdatedThreads > 0 ? `${summary.outdatedThreads} outdated` : '',
    ]
        .filter(Boolean)
        .join(' · ');

    return (
        <div
            style={{
                background: 'var(--bg-secondary)',
                borderRadius: '8px',
                border: '1px solid var(--border)',
                marginBottom: '16px',
                overflow: 'hidden',
            }}
        >
            <button
                type="button"
                onClick={() => setCollapsed(c => !c)}
                aria-expanded={!collapsed}
                style={{
                    width: '100%',
                    padding: '12px 16px',
                    borderTop: 'none',
                    borderLeft: 'none',
                    borderRight: 'none',
                    borderBottom: collapsed ? 'none' : '1px solid var(--border)',
                    background: 'var(--bg-primary)',
                    fontSize: '13px',
                    fontWeight: 500,
                    color: 'var(--text-secondary)',
                    display: 'flex',
                    alignItems: 'center',
                    gap: '8px',
                    flexWrap: 'wrap',
                    cursor: 'pointer',
                    textAlign: 'left',
                }}
            >
                <span
                    style={{
                        display: 'inline-block',
                        transform: collapsed ? 'rotate(-90deg)' : 'rotate(0deg)',
                        transition: 'transform 0.15s ease',
                        fontSize: '10px',
                    }}
                >
                    ▼
                </span>
                Discussion
                <span style={{ fontSize: '11px', color: 'var(--text-tertiary)', fontWeight: 400 }}>
                    {headline}
                </span>
                {summary.unresolvedThreads > 0 && (
                    <span
                        style={{
                            fontSize: '10px',
                            fontWeight: 600,
                            padding: '1px 6px',
                            borderRadius: '9px',
                            background: colors.bgWarningDim,
                            color: colors.warning,
                            border: `1px solid ${colors.borderWarningDim}`,
                        }}
                    >
                        {summary.unresolvedThreads} open
                    </span>
                )}
            </button>
            <div style={{ display: collapsed ? 'none' : 'flex', flexDirection: 'column' }}>
                {timeline.map(entry => (
                    <TimelineRow key={entry.key} entry={entry} onJumpToFile={onJumpToFile} />
                ))}
            </div>
        </div>
    );
}
