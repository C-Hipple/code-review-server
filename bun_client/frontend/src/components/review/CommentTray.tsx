import { useEffect, useMemo, useRef, useState } from 'react';
import {
    authorHue,
    authorInitials,
    clampTrayWidth,
    countTrayThreads,
    displayAuthor,
    filterTrayGroups,
    stepThread,
    threadPreview,
    trayOrder,
    type TrayFilter,
    type TrayGroup,
    type TrayThread,
} from '../../comment_tray_utils';
import { Button } from '../../design';
import { relativeTime } from '../../discussion_utils';
import GitHubMarkdown from '../GitHubMarkdown';
import Reactions from './Reactions';
import { type Comment, stripHtmlComments } from './types';

interface CommentTrayProps {
    groups: TrayGroup[];
    selectedId: string | null;
    isMobile: boolean;
    width: number;
    // A reply, edit or delete is in flight.
    busy: boolean;
    onResize: (width: number) => void;
    onResizeEnd: (width: number) => void;
    // Select a thread; `reveal` also scrolls the diff to its line.
    onSelect: (thread: TrayThread, reveal: boolean) => void;
    // Resolve to true once the comment is saved, so the composer can close.
    onReply: (thread: TrayThread, body: string) => Promise<boolean>;
    onEdit: (comment: Comment, body: string) => Promise<boolean>;
    onDelete: (comment: Comment) => void;
    onJumpToFile: (path: string) => void;
    onClose: () => void;
}

// The one reply or edit being written in the tray.
interface Composer {
    rootId: string;
    mode: 'reply' | 'edit';
    comment?: Comment;
    body: string;
}

const FILTERS: { id: TrayFilter; label: string }[] = [
    { id: 'all', label: 'All' },
    { id: 'open', label: 'Open' },
    { id: 'resolved', label: 'Resolved' },
];

const isTyping = (target: EventTarget | null): boolean => {
    const el = target as HTMLElement | null;
    if (!el) return false;
    return (
        el.isContentEditable ||
        el.tagName === 'INPUT' ||
        el.tagName === 'TEXTAREA' ||
        el.tagName === 'SELECT'
    );
};

const fullTime = (iso: string): string | undefined =>
    iso && !iso.startsWith('0001') ? new Date(iso).toLocaleString() : undefined;

function Avatar({ author }: { author: string }) {
    const local = author === 'local';
    return (
        <span
            className={'ct-avatar' + (local ? ' is-you' : '')}
            style={local ? undefined : ({ '--ct-hue': authorHue(author) } as React.CSSProperties)}
            aria-hidden="true"
        >
            {authorInitials(author)}
        </span>
    );
}

function LineStrip({ thread }: { thread: TrayThread }) {
    const { line } = thread;
    if (!line) {
        return (
            <div className="ct-line ct-line-file">
                <span className="ct-line-no">file</span>
                <span className="ct-line-code">Comment on the whole file</span>
            </div>
        );
    }
    return (
        <div className={`ct-line ct-line-${line.kind}`}>
            <span className="ct-line-no">
                {line.lineNo !== null ? `${line.side}${line.lineNo}` : ''}
            </span>
            <span className="ct-line-sign" aria-hidden="true">
                {line.kind === 'addition' ? '+' : line.kind === 'deletion' ? '−' : ''}
            </span>
            {/* Indentation only pushes the code out of the strip. */}
            <code className="ct-line-code">{line.text.trim() || ' '}</code>
        </div>
    );
}

function StateChips({ thread }: { thread: TrayThread }) {
    if (!thread.pending && !thread.outdated && !thread.resolved) return null;
    return (
        <span className="ct-chips">
            {thread.pending && (
                <span className="ct-chip ct-chip-pending" title="Goes out with your next review">
                    Pending
                </span>
            )}
            {thread.outdated && (
                <span
                    className="ct-chip ct-chip-outdated"
                    title="These lines are no longer in the current diff"
                >
                    Outdated
                </span>
            )}
            {thread.resolved && (
                <span
                    className="ct-chip ct-chip-resolved"
                    title={
                        thread.resolvedBy
                            ? `Conversation resolved by ${thread.resolvedBy}`
                            : 'Conversation resolved'
                    }
                >
                    ✓ Resolved
                </span>
            )}
        </span>
    );
}

// The comment tray: every conversation on the PR in a panel down the right
// edge, grouped by file in diff order, beside the diff rather than inside it.
// Selecting a thread scrolls the diff to its line and marks it; ↑/↓ (or k/j)
// step through them. Replies and edits to your pending comments are written
// here, without leaving the code you're reading.
export default function CommentTray({
    groups,
    selectedId,
    isMobile,
    width,
    busy,
    onResize,
    onResizeEnd,
    onSelect,
    onReply,
    onEdit,
    onDelete,
    onJumpToFile,
    onClose,
}: CommentTrayProps) {
    const [filter, setFilter] = useState<TrayFilter>('all');
    // Resolved threads start folded to one line; these are the ones opened.
    const [unfolded, setUnfolded] = useState<Set<string>>(new Set());
    const [composer, setComposer] = useState<Composer | null>(null);
    const listRef = useRef<HTMLDivElement | null>(null);

    const counts = useMemo(() => countTrayThreads(groups), [groups]);
    const shown = useMemo(() => filterTrayGroups(groups, filter), [groups, filter]);
    const order = useMemo(() => trayOrder(shown), [shown]);
    const byId = useMemo(() => {
        const map = new Map<string, TrayThread>();
        for (const g of groups) for (const t of g.threads) map.set(t.rootId, t);
        return map;
    }, [groups]);

    const step = (dir: 1 | -1) => {
        const next = stepThread(order, selectedId, dir);
        const thread = next ? byId.get(next) : undefined;
        if (thread) onSelect(thread, !isMobile);
    };
    const stepRef = useRef(step);
    stepRef.current = step;

    // j / k step through the threads while the tray is open, as in a mail
    // reader — but never while something is being typed.
    useEffect(() => {
        const onKey = (e: KeyboardEvent) => {
            if (e.metaKey || e.ctrlKey || e.altKey || isTyping(e.target)) return;
            if (e.key === 'j') stepRef.current(1);
            else if (e.key === 'k') stepRef.current(-1);
            else return;
            e.preventDefault();
        };
        window.addEventListener('keydown', onKey);
        return () => window.removeEventListener('keydown', onKey);
    }, []);

    // Keep the selected thread in view inside the tray. The list is scrolled
    // directly rather than with scrollIntoView, which would also move the
    // page while the diff is scrolling to the same thread.
    useEffect(() => {
        const list = listRef.current;
        if (!list || !selectedId) return;
        const card = list.querySelector<HTMLElement>(
            `[data-tray-thread="${CSS.escape(selectedId)}"]`
        );
        if (!card) return;
        const listBox = list.getBoundingClientRect();
        const cardBox = card.getBoundingClientRect();
        const margin = 48; // clear the sticky file header
        if (cardBox.top < listBox.top + margin || cardBox.bottom > listBox.bottom) {
            list.scrollTo({
                top: list.scrollTop + cardBox.top - listBox.top - margin,
                behavior: 'smooth',
            });
        }
    }, [selectedId, filter]);

    // Drag the left edge to resize the tray (desktop only).
    const startResize = (e: React.PointerEvent<HTMLDivElement>) => {
        e.preventDefault();
        const handle = e.currentTarget;
        handle.setPointerCapture(e.pointerId);
        let latest = width;
        const move = (ev: PointerEvent) => {
            latest = clampTrayWidth(window.innerWidth - ev.clientX, window.innerWidth);
            onResize(latest);
        };
        const up = () => {
            handle.removeEventListener('pointermove', move);
            handle.removeEventListener('pointerup', up);
            handle.removeEventListener('pointercancel', up);
            onResizeEnd(latest);
        };
        handle.addEventListener('pointermove', move);
        handle.addEventListener('pointerup', up);
        handle.addEventListener('pointercancel', up);
    };

    const submitComposer = async (thread: TrayThread) => {
        if (!composer || !composer.body.trim()) return;
        const ok =
            composer.mode === 'edit' && composer.comment
                ? await onEdit(composer.comment, composer.body)
                : await onReply(thread, composer.body);
        if (ok) setComposer(null);
    };

    const renderComment = (c: Comment) => (
        <div key={c.id} className="ct-comment">
            <Avatar author={c.author} />
            <div className="ct-comment-main">
                <div className="ct-comment-meta">
                    <span className="ct-author">{displayAuthor(c.author)}</span>
                    <span className="ct-time" title={fullTime(c.created_at)}>
                        {relativeTime(c.created_at)}
                    </span>
                    {c.author === 'local' && (
                        <span className="ct-comment-actions">
                            <button
                                type="button"
                                className="ct-link-btn"
                                disabled={busy}
                                onClick={e => {
                                    e.stopPropagation();
                                    const thread = [...byId.values()].find(t =>
                                        t.comments.some(x => x.id === c.id)
                                    );
                                    if (!thread) return;
                                    setComposer({
                                        rootId: thread.rootId,
                                        mode: 'edit',
                                        comment: c,
                                        body: c.body,
                                    });
                                }}
                            >
                                Edit
                            </button>
                            <button
                                type="button"
                                className="ct-link-btn ct-danger"
                                disabled={busy}
                                title="Delete local comment"
                                onClick={e => {
                                    e.stopPropagation();
                                    onDelete(c);
                                }}
                            >
                                Delete
                            </button>
                        </span>
                    )}
                </div>
                <div className="ct-comment-body markdown-content">
                    <GitHubMarkdown>{stripHtmlComments(c.body)}</GitHubMarkdown>
                </div>
                <Reactions reactions={c.reactions} compact />
            </div>
        </div>
    );

    const renderComposer = (thread: TrayThread) => {
        if (!composer || composer.rootId !== thread.rootId) return null;
        const editing = composer.mode === 'edit';
        return (
            <div className="ct-composer" onClick={e => e.stopPropagation()}>
                <textarea
                    autoFocus
                    aria-label={editing ? 'Edit your comment' : 'Write a reply'}
                    placeholder={editing ? 'Edit your comment…' : 'Write a reply…'}
                    value={composer.body}
                    disabled={busy}
                    onChange={e => setComposer({ ...composer, body: e.target.value })}
                    onKeyDown={e => {
                        if (e.key === 'Escape') {
                            e.stopPropagation();
                            setComposer(null);
                        } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                            e.preventDefault();
                            submitComposer(thread);
                        }
                    }}
                />
                <div className="ct-composer-actions">
                    <span className="ct-hint">⌘/Ctrl+Enter to send</span>
                    <Button
                        variant="secondary"
                        size="sm"
                        disabled={busy}
                        onClick={() => setComposer(null)}
                    >
                        Cancel
                    </Button>
                    <Button
                        size="sm"
                        loading={busy}
                        disabled={!composer.body.trim()}
                        onClick={() => submitComposer(thread)}
                    >
                        {editing ? 'Save' : 'Reply'}
                    </Button>
                </div>
            </div>
        );
    };

    const renderThread = (thread: TrayThread) => {
        const selected = thread.rootId === selectedId;
        const folded =
            thread.resolved &&
            !selected &&
            !unfolded.has(thread.rootId) &&
            composer?.rootId !== thread.rootId;
        const onFile = thread.path !== '';
        return (
            <article
                key={thread.rootId}
                data-tray-thread={thread.rootId}
                className={
                    'ct-thread' +
                    (selected ? ' is-selected' : '') +
                    (thread.resolved ? ' is-resolved' : '') +
                    (folded ? ' is-folded' : '')
                }
                tabIndex={0}
                aria-current={selected ? 'true' : undefined}
                onClick={() => {
                    // Selecting text to copy shouldn't jump the diff.
                    if (window.getSelection()?.toString()) return;
                    onSelect(thread, !isMobile);
                }}
                onKeyDown={e => {
                    if (e.key === 'Enter' && e.target === e.currentTarget) {
                        onSelect(thread, !isMobile);
                    }
                }}
            >
                {onFile && (
                    <div className="ct-thread-head">
                        <LineStrip thread={thread} />
                        <button
                            type="button"
                            className="ct-icon-btn"
                            title="Show in diff"
                            aria-label="Show in diff"
                            onClick={e => {
                                e.stopPropagation();
                                onSelect(thread, true);
                            }}
                        >
                            ⌖
                        </button>
                    </div>
                )}
                {folded ? (
                    <button
                        type="button"
                        className="ct-folded"
                        onClick={e => {
                            e.stopPropagation();
                            setUnfolded(prev => new Set(prev).add(thread.rootId));
                        }}
                        title="Show this conversation"
                    >
                        <StateChips thread={thread} />
                        <span className="ct-folded-who">
                            {thread.participants.map(displayAuthor).join(', ')}
                        </span>
                        <span className="ct-folded-text">{threadPreview(thread)}</span>
                    </button>
                ) : (
                    <>
                        <StateChips thread={thread} />
                        <div className="ct-comments">{thread.comments.map(renderComment)}</div>
                        {thread.resolved && !selected && unfolded.has(thread.rootId) && (
                            <button
                                type="button"
                                className="ct-link-btn ct-fold-btn"
                                onClick={e => {
                                    e.stopPropagation();
                                    setUnfolded(prev => {
                                        const next = new Set(prev);
                                        next.delete(thread.rootId);
                                        return next;
                                    });
                                }}
                            >
                                Fold
                            </button>
                        )}
                        {onFile &&
                            (composer?.rootId === thread.rootId ? (
                                renderComposer(thread)
                            ) : (
                                <button
                                    type="button"
                                    className="ct-reply-prompt"
                                    disabled={busy}
                                    onClick={e => {
                                        e.stopPropagation();
                                        onSelect(thread, false);
                                        setComposer({
                                            rootId: thread.rootId,
                                            mode: 'reply',
                                            body: '',
                                        });
                                    }}
                                >
                                    Reply…
                                </button>
                            ))}
                    </>
                )}
            </article>
        );
    };

    const renderGroupHeader = (group: TrayGroup) => {
        if (!group.path) {
            return (
                <div className="ct-group-head">
                    <span className="ct-group-name">Conversation</span>
                    <span className="ct-group-count">{group.threads.length}</span>
                </div>
            );
        }
        const slash = group.path.lastIndexOf('/');
        const dir = slash >= 0 ? group.path.slice(0, slash + 1) : '';
        const base = slash >= 0 ? group.path.slice(slash + 1) : group.path;
        return (
            <button
                type="button"
                className="ct-group-head ct-group-file"
                title={`Jump to ${group.path} in the diff`}
                onClick={() => onJumpToFile(group.path)}
            >
                <span className="ct-group-name">
                    {dir && <span className="ct-group-dir">{dir}</span>}
                    <span className="ct-group-base">{base}</span>
                </span>
                <span className="ct-group-count">{group.threads.length}</span>
            </button>
        );
    };

    const position = order.indexOf(selectedId ?? '');

    return (
        <aside
            className={'comment-tray' + (isMobile ? ' is-mobile' : '')}
            style={isMobile ? undefined : { width: `${width}px` }}
            aria-label="Comments"
        >
            {!isMobile && (
                <div
                    className="ct-resize"
                    role="separator"
                    aria-orientation="vertical"
                    aria-label="Resize the comment tray"
                    onPointerDown={startResize}
                />
            )}
            <header className="ct-head">
                <div className="ct-title-row">
                    <h2 className="ct-title">Comments</h2>
                    <span className="ct-subtitle">
                        {counts.all === 0
                            ? 'none yet'
                            : `${counts.all} ${counts.all === 1 ? 'thread' : 'threads'} · ${counts.open} open`}
                    </span>
                    <span className="ct-head-actions">
                        {order.length > 0 && (
                            <span className="ct-position" aria-live="polite">
                                {position >= 0 ? `${position + 1}/${order.length}` : ''}
                            </span>
                        )}
                        <button
                            type="button"
                            className="ct-icon-btn"
                            title="Previous thread (k)"
                            aria-label="Previous thread"
                            disabled={order.length === 0}
                            onClick={() => step(-1)}
                        >
                            ↑
                        </button>
                        <button
                            type="button"
                            className="ct-icon-btn"
                            title="Next thread (j)"
                            aria-label="Next thread"
                            disabled={order.length === 0}
                            onClick={() => step(1)}
                        >
                            ↓
                        </button>
                        <button
                            type="button"
                            className="ct-icon-btn"
                            title="Close the comment tray"
                            aria-label="Close comments"
                            onClick={onClose}
                        >
                            ×
                        </button>
                    </span>
                </div>
                <div className="ct-filters" role="tablist" aria-label="Which threads to show">
                    {FILTERS.map(f => (
                        <button
                            key={f.id}
                            type="button"
                            role="tab"
                            aria-selected={filter === f.id}
                            className={'ct-filter' + (filter === f.id ? ' is-active' : '')}
                            onClick={() => setFilter(f.id)}
                        >
                            {f.label}
                            <span className="ct-filter-count">{counts[f.id]}</span>
                        </button>
                    ))}
                </div>
            </header>
            <div className="ct-list" ref={listRef}>
                {counts.all === 0 ? (
                    <div className="ct-empty">
                        No comments on this PR yet. Click the <strong>+</strong> beside a line to
                        start one.
                    </div>
                ) : shown.length === 0 ? (
                    <div className="ct-empty">
                        No {filter} threads.{' '}
                        <button
                            type="button"
                            className="ct-link-btn"
                            onClick={() => setFilter('all')}
                        >
                            Show all
                        </button>
                    </div>
                ) : (
                    shown.map(group => (
                        <section key={group.key} className="ct-group">
                            {renderGroupHeader(group)}
                            <div className="ct-group-threads">
                                {group.threads.map(renderThread)}
                            </div>
                        </section>
                    ))
                )}
            </div>
            {!isMobile && counts.all > 1 && (
                <footer className="ct-foot">
                    <kbd>j</kbd>
                    <kbd>k</kbd> to step through threads
                </footer>
            )}
        </aside>
    );
}
