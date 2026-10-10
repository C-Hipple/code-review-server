import { useId, useState, type ReactNode } from 'react';
import { ChevronRightIcon } from './icons';

const STORAGE_PREFIX = 'crs.expanded.';

function readExpanded(key: string, fallback: boolean): boolean {
    try {
        const stored = localStorage.getItem(STORAGE_PREFIX + key);
        return stored === null ? fallback : stored === '1';
    } catch {
        return fallback; // no storage (tests, a locked-down profile)
    }
}

function writeExpanded(key: string, expanded: boolean): void {
    try {
        localStorage.setItem(STORAGE_PREFIX + key, expanded ? '1' : '0');
    } catch {
        // Not remembered; fine.
    }
}

export interface ToolCardProps {
    /** Stable key, e.g. `ai:change-diagram`; remembers whether the card was left open. */
    storageKey: string;
    title: string;
    /** Tooltip on the title (a feature's description, a plugin's command). */
    titleHint?: string;
    icon?: ReactNode;
    /** Status chip and badges, shown beside the title. */
    chips?: ReactNode;
    /** Right-aligned small text, e.g. "Updated 5 min ago". */
    meta?: ReactNode;
    /** Buttons. */
    actions?: ReactNode;
    /** One line under the header while collapsed: a preview of the result, or what it does. */
    summary?: ReactNode;
    /** Shown under the header whether expanded or not: an action's error or note. */
    notice?: ReactNode;
    /** The card has a body to expand into. */
    expandable: boolean;
    defaultExpanded?: boolean;
    children?: ReactNode;
}

/** A plugin's or AI feature's card: a header row, and a body that expands. */
export function ToolCard({
    storageKey,
    title,
    titleHint,
    icon,
    chips,
    meta,
    actions,
    summary,
    notice,
    expandable,
    defaultExpanded = false,
    children,
}: ToolCardProps) {
    const bodyId = useId();
    const [open, setOpen] = useState(() => readExpanded(storageKey, defaultExpanded));
    const expanded = expandable && open;

    const toggle = () => {
        setOpen(!expanded);
        writeExpanded(storageKey, !expanded);
    };

    const heading = (
        <>
            {expandable && (
                <span className={`chevron${expanded ? ' chevron-open' : ''}`}>
                    <ChevronRightIcon />
                </span>
            )}
            {icon && <span className="card-icon">{icon}</span>}
            <span className="card-title" title={titleHint}>
                {title}
            </span>
            {chips && <span className="card-chips">{chips}</span>}
        </>
    );

    return (
        <article className={`card${expanded ? ' card-expanded' : ''}`}>
            <div className="card-header">
                <h3 className="card-heading">
                    {expandable ? (
                        <button
                            type="button"
                            className="card-toggle"
                            aria-expanded={expanded}
                            aria-controls={expanded ? bodyId : undefined}
                            onClick={toggle}
                        >
                            {heading}
                        </button>
                    ) : (
                        <span className="card-toggle card-static">{heading}</span>
                    )}
                </h3>
                {meta && <span className="card-meta">{meta}</span>}
                {actions && <span className="card-actions">{actions}</span>}
            </div>
            {summary && !expanded && <p className="card-summary">{summary}</p>}
            {notice}
            {expanded && (
                <div className="card-body" id={bodyId}>
                    {children}
                </div>
            )}
        </article>
    );
}

/** A note under a card's header: why its action failed, or what it did. */
export function CardNotice({ tone, children }: { tone: 'danger' | 'muted'; children: ReactNode }) {
    return (
        <p
            className={`card-notice card-notice-${tone}`}
            role={tone === 'danger' ? 'alert' : 'status'}
        >
            {children}
        </p>
    );
}
