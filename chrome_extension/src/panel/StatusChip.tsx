import type { ReactNode } from 'react';
import type { Tone } from './list_utils';

/** A spinner; it stops turning under prefers-reduced-motion (see panel.css). */
export function Spinner({ label }: { label?: string }) {
    return (
        <span
            className="spinner"
            role={label ? 'status' : undefined}
            aria-label={label}
            aria-hidden={label ? undefined : true}
        />
    );
}

/** A run's status: a colored chip with a dot, or a spinner while it runs. */
export function StatusChip({
    tone,
    label,
    spinning = false,
    title,
}: {
    tone: Tone;
    label: string;
    spinning?: boolean;
    title?: string;
}) {
    return (
        <span className={`chip chip-${tone}`} title={title}>
            {spinning ? <Spinner /> : <span className="chip-dot" aria-hidden="true" />}
            {label}
        </span>
    );
}

/** A small outlined label: a tag on a list row, a badge on a card. */
export function Pill({
    tone = 'neutral',
    children,
    title,
}: {
    tone?: Tone;
    children: ReactNode;
    title?: string;
}) {
    return (
        <span className={`pill pill-${tone}`} title={title}>
            {children}
        </span>
    );
}
