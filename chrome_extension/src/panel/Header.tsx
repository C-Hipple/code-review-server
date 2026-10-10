import type { PullRef } from '../github_url';
import type { HostStatus } from '../types';
import { ArrowLeftIcon, CloseIcon } from './icons';
import { prLabel } from './list_utils';

export type Connection = 'connected' | 'error' | 'unknown';

/** The dot's state and its tooltip, from the service worker's status. */
export function connectionOf(status: HostStatus | null): { state: Connection; text: string } {
    if (status?.connected) {
        const host = status.host;
        return {
            state: 'connected',
            text: host
                ? `Connected to ${host.server_path}${host.version ? ` (${host.version})` : ''}`
                : 'Connected',
        };
    }
    if (status?.lastError) return { state: 'error', text: status.lastError.message };
    return { state: 'unknown', text: 'Not connected yet' };
}

const CONNECTION_LABEL: Record<Connection, string> = {
    connected: 'Connected',
    error: 'Disconnected',
    unknown: 'Connecting',
};

export interface HeaderProps {
    /** The PR being shown, or null on the list. */
    pr: PullRef | null;
    status: HostStatus | null;
    standalone: boolean;
    onBack: () => void;
    onClose: () => void;
}

export function Header({ pr, status, standalone, onBack, onClose }: HeaderProps) {
    const connection = connectionOf(status);
    return (
        <header className="header">
            {pr && (
                <button
                    type="button"
                    className="header-back"
                    onClick={onBack}
                    aria-label="Back to the review list"
                    title="Back to the review list"
                >
                    <ArrowLeftIcon />
                    <span className="header-back-text">Back</span>
                </button>
            )}
            <span className="brand">
                <Logo />
                <span className="brand-name">Code Review Server</span>
            </span>
            <nav className="crumb" aria-label="Location">
                <span className="crumb-sep" aria-hidden="true">
                    /
                </span>
                {pr ? (
                    <span className="crumb-current" aria-current="page">
                        {prLabel(pr)}
                    </span>
                ) : (
                    <span className="crumb-current" aria-current="page">
                        Review list
                    </span>
                )}
            </nav>
            <span className="spacer" />
            <span className={`connection connection-${connection.state}`} title={connection.text}>
                <span className="dot" aria-hidden="true" />
                <span className="connection-label">{CONNECTION_LABEL[connection.state]}</span>
            </span>
            {!standalone && (
                <button
                    type="button"
                    className="icon-button"
                    aria-label="Close"
                    title="Close (Esc)"
                    onClick={onClose}
                >
                    <CloseIcon />
                </button>
            )}
        </header>
    );
}

function Logo() {
    return (
        <svg
            className="logo"
            width="20"
            height="20"
            viewBox="0 0 20 20"
            aria-hidden="true"
            focusable="false"
        >
            <rect width="20" height="20" rx="5" fill="var(--accent)" />
            <path
                d="M6.5 6.5 4 10l2.5 3.5M13.5 6.5 16 10l-2.5 3.5M11.25 5.5l-2.5 9"
                fill="none"
                stroke="var(--accent-fg)"
                strokeWidth="1.6"
                strokeLinecap="round"
                strokeLinejoin="round"
            />
        </svg>
    );
}
