import type { ReviewItem } from '../types';
import { usePanel } from './context';
import { ChevronRightIcon, CommentIcon, PullStateIcon } from './icons';
import { itemState, prLabel, rowTags } from './list_utils';
import { Pill } from './StatusChip';
import { absoluteTime, relativeTime } from './time_utils';

export interface ReviewRowProps {
    item: ReviewItem;
    /** The PR the GitHub tab is on. */
    current: boolean;
    now: number;
    /** Opens the PR tools view for the item, in the panel. */
    onOpenTools: (item: ReviewItem) => void;
}

/**
 * One PR in the review list. The row's link goes to the PR on GitHub (the
 * GitHub tab itself when embedded, a new tab from the popup window); the
 * button beside it opens the PR's AI features and plugins here.
 */
export function ReviewRow({ item, current, now, onOpenTools }: ReviewRowProps) {
    const { target } = usePanel();
    const state = itemState(item);
    const tags = rowTags(item);
    const created = relativeTime(item.created_at, now);
    const label = prLabel(item);

    return (
        <li className={`row${current ? ' row-current' : ''}`}>
            <a
                className="row-link"
                href={item.url}
                target={target}
                rel="noreferrer"
                aria-current={current ? 'page' : undefined}
            >
                <span className={`row-state state-fg-${state}`} title={state}>
                    <PullStateIcon state={state} />
                </span>
                <span className="row-main">
                    <span className="row-title-line">
                        <span className="row-title">{item.title}</span>
                        {tags.map(t => (
                            <Pill key={t.key} tone={t.tone} title={t.title}>
                                {t.label}
                            </Pill>
                        ))}
                        {current && <Pill tone="accent">This tab</Pill>}
                    </span>
                    <span className="row-meta">
                        <span className="row-ref">{label}</span>
                        {item.author && <span> · {item.author}</span>}
                        {created && (
                            <span title={absoluteTime(item.created_at)}> · opened {created}</span>
                        )}
                    </span>
                </span>
                {item.comment_count > 0 && (
                    <span
                        className="row-comments"
                        title={`${item.comment_count} ${item.comment_count === 1 ? 'comment' : 'comments'}`}
                    >
                        <CommentIcon />
                        {item.comment_count}
                    </span>
                )}
            </a>
            <button
                type="button"
                className="row-tools"
                onClick={() => onOpenTools(item)}
                aria-label={`AI features and plugins for ${label}`}
                title="AI features and plugins"
            >
                <span className="row-tools-text">Tools</span>
                <ChevronRightIcon />
            </button>
        </li>
    );
}
