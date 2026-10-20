// Small stroke icons, drawn inline so the panel needs no icon font or
// library. Decorative: each is aria-hidden, and whatever holds it carries
// the accessible name.

import type { ReactNode } from 'react';

function Svg({ children, size = 16 }: { children: ReactNode; size?: number }) {
    return (
        <svg
            className="icon"
            width={size}
            height={size}
            viewBox="0 0 16 16"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
            focusable="false"
        >
            {children}
        </svg>
    );
}

export const ArrowLeftIcon = () => (
    <Svg>
        <path d="M13 8H3M7.5 3.5 3 8l4.5 4.5" />
    </Svg>
);

export const CloseIcon = () => (
    <Svg>
        <path d="M4 4l8 8M12 4l-8 8" />
    </Svg>
);

export const ChevronRightIcon = () => (
    <Svg>
        <path d="M6 3.5 10.5 8 6 12.5" />
    </Svg>
);

export const RefreshIcon = () => (
    <Svg>
        <path d="M13.25 8a5.25 5.25 0 1 1-1.54-3.71" />
        <path d="M13.25 2.5v3h-3" />
    </Svg>
);

export const SearchIcon = () => (
    <Svg>
        <circle cx="7" cy="7" r="4.25" />
        <path d="m10.25 10.25 3.25 3.25" />
    </Svg>
);

export const ExternalIcon = () => (
    <Svg>
        <path d="M9.5 2.5h4v4M13.5 2.5 7.5 8.5M12 9.5v3.25a.75.75 0 0 1-.75.75h-8a.75.75 0 0 1-.75-.75v-8a.75.75 0 0 1 .75-.75H6.5" />
    </Svg>
);

export const CopyIcon = () => (
    <Svg>
        <rect x="5.5" y="5.5" width="8" height="8" rx="1.25" />
        <path d="M10.5 3.5V3.25a.75.75 0 0 0-.75-.75H3.25a.75.75 0 0 0-.75.75v6.5c0 .41.34.75.75.75h.25" />
    </Svg>
);

export const CheckIcon = () => (
    <Svg>
        <path d="m3 8.5 3.25 3.25L13 5" />
    </Svg>
);

export const PlayIcon = () => (
    <Svg>
        <path d="M5 3.25v9.5L12.5 8z" fill="currentColor" strokeWidth="1" />
    </Svg>
);

export const CommentIcon = () => (
    <Svg>
        <path d="M2.75 3.25h10.5a.75.75 0 0 1 .75.75v6.5a.75.75 0 0 1-.75.75H8L5 13.5v-2.25H2.75A.75.75 0 0 1 2 10.5V4a.75.75 0 0 1 .75-.75Z" />
    </Svg>
);

export const SparkleIcon = () => (
    <Svg>
        <path d="M8 1.75 9.3 6.2 13.75 7.5 9.3 8.8 8 13.25 6.7 8.8 2.25 7.5 6.7 6.2Z" />
    </Svg>
);

export const PlugIcon = () => (
    <Svg>
        <path d="M6 1.75v3M10 1.75v3M4 4.75h8v2.5a4 4 0 0 1-8 0zM8 11.25v3" />
    </Svg>
);

export const FileIcon = () => (
    <Svg>
        <path d="M9 1.75H4.25a.75.75 0 0 0-.75.75v11c0 .41.34.75.75.75h7.5a.75.75 0 0 0 .75-.75V5.25z" />
        <path d="M9 1.75v3.5h3.5" />
    </Svg>
);

export const ListOrderIcon = () => (
    <Svg>
        <path d="M6.5 4h7M6.5 8h7M6.5 12h7M2.5 3l1-.5V5.5M2.25 10.25c.2-.5.65-.75 1.1-.75.6 0 1.05.4 1.05.95 0 .85-2.15 1.5-2.15 2.8h2.25" />
    </Svg>
);

/** A PR's state, drawn the way GitHub draws it. */
export function PullStateIcon({ state }: { state: 'open' | 'draft' | 'merged' | 'closed' }) {
    if (state === 'merged') {
        return (
            <Svg>
                <circle cx="4.25" cy="3.25" r="1.5" />
                <circle cx="4.25" cy="12.75" r="1.5" />
                <circle cx="11.75" cy="9" r="1.5" />
                <path d="M4.25 4.75v6.5M4.25 4.75c0 2.75 2 4.25 6 4.25" />
            </Svg>
        );
    }
    return (
        <Svg>
            <circle cx="4.25" cy="3.25" r="1.5" />
            <circle cx="4.25" cy="12.75" r="1.5" />
            <circle cx="11.75" cy="12.75" r="1.5" />
            <path d="M4.25 4.75v6.5" />
            {state === 'closed' ? (
                <path d="M10.25 3.25l3 3M13.25 3.25l-3 3M11.75 8.5v2.75" />
            ) : state === 'draft' ? (
                <path d="M11.75 4v.01M11.75 7.25v.01M11.75 10v.5" />
            ) : (
                <path d="M11.75 11.25V5.5a1.5 1.5 0 0 0-1.5-1.5H7.5M9 2.5 7.5 4 9 5.5" />
            )}
        </Svg>
    );
}
