import { useCallback, useEffect, useState, type CSSProperties, type ReactNode } from 'react';
import {
    getGitHubLink,
    rpcErrorMessage,
    unlinkGitHub,
    type BridgeSession,
    type GetGitHubLinkReply,
} from '../api';
import {
    Badge,
    Button,
    colors,
    fontSize,
    fontWeight,
    getStatusColor,
    spacing,
    type StatusVariant,
} from '../design';
import { displayUri, linkOutcome } from '../github_link_utils';
import { useBridgeSession } from '../hooks/useBridgeSession';
import { bridgeSession } from '../session';
import LinkGitHubDialog from './LinkGitHubDialog';

/**
 * The Account tab of Preferences, on a bridge connected to a shared server:
 * who it is signed in as there, and whether their GitHub account is linked —
 * which is what their reviews go to GitHub as — with Link and Unlink.
 */
export default function AccountPanel() {
    const session = useBridgeSession();
    // The link as the server reports it now: a session read can be a minute
    // old, and only this says which account, or why a link failed.
    const [link, setLink] = useState<GetGitHubLinkReply | null>(null);
    const [loadError, setLoadError] = useState('');
    const [linking, setLinking] = useState(false);
    const [confirmUnlink, setConfirmUnlink] = useState(false);
    const [unlinking, setUnlinking] = useState(false);
    const [notice, setNotice] = useState('');

    const load = useCallback(async () => {
        try {
            setLink(await getGitHubLink());
            setLoadError('');
        } catch (e) {
            setLoadError(rpcErrorMessage(e));
        }
    }, []);

    useEffect(() => {
        void load();
    }, [load]);

    const handleUnlink = async () => {
        setUnlinking(true);
        try {
            const reply = await unlinkGitHub();
            setNotice(reply.message);
        } catch (e) {
            setNotice(`Could not unlink: ${rpcErrorMessage(e)}`);
        } finally {
            setUnlinking(false);
            setConfirmUnlink(false);
            void bridgeSession.refresh();
            void load();
        }
    };

    if (!session) {
        return <div style={{ color: colors.textSecondary }}>Loading…</div>;
    }
    if (!session.remote) {
        return (
            <div style={{ color: colors.textSecondary }}>
                This client runs its own server, which posts to GitHub with your own token.
            </div>
        );
    }

    return (
        <div
            style={{ display: 'flex', flexDirection: 'column', gap: spacing.xl, padding: '10px 0' }}
        >
            <section>
                <div style={heading}>Signed in</div>
                <div style={{ display: 'flex', alignItems: 'center', gap: spacing.sm }}>
                    <span style={{ fontWeight: fontWeight.semibold }}>
                        {session.login || "The server's own identity"}
                    </span>
                    {session.is_admin && (
                        <Badge variant="info" size="sm">
                            Administrator
                        </Badge>
                    )}
                </div>
                <div style={hint}>
                    On the shared server at {session.address}
                    {session.error ? ` — can't reach it right now (${session.error})` : ''}
                </div>
            </section>

            <section>
                <div style={heading}>GitHub account</div>
                <GitHubLinkState
                    session={session}
                    link={link}
                    loadError={loadError}
                    confirmUnlink={confirmUnlink}
                    unlinking={unlinking}
                    onLink={() => {
                        setNotice('');
                        setLinking(true);
                    }}
                    onUnlink={() => setConfirmUnlink(true)}
                    onConfirmUnlink={handleUnlink}
                    onKeep={() => setConfirmUnlink(false)}
                />
                {notice && <div style={{ ...hint, marginTop: spacing.sm }}>{notice}</div>}
            </section>

            <LinkGitHubDialog
                isOpen={linking}
                onClose={() => {
                    setLinking(false);
                    void load();
                }}
                onLinked={() => void load()}
            />
        </div>
    );
}

interface GitHubLinkStateProps {
    session: BridgeSession;
    link: GetGitHubLinkReply | null;
    loadError: string;
    confirmUnlink: boolean;
    unlinking: boolean;
    onLink: () => void;
    onUnlink: () => void;
    onConfirmUnlink: () => void;
    onKeep: () => void;
}

/** The GitHub account section: where the link stands, and what can be done. */
export function GitHubLinkState({
    session,
    link,
    loadError,
    confirmUnlink,
    unlinking,
    onLink,
    onUnlink,
    onConfirmUnlink,
    onKeep,
}: GitHubLinkStateProps) {
    // Until the server's answer is in, the session's flags say enough.
    const status =
        link?.status ??
        (session.github_linked
            ? 'linked'
            : session.github_link_available
              ? 'unlinked'
              : 'unavailable');
    const githubLogin = link?.github_login || '';
    const linkButton = (label = 'Link GitHub account') => (
        <Button size="sm" onClick={onLink}>
            {label}
        </Button>
    );

    let line: string;
    let variant: StatusVariant = 'neutral';
    let actions: ReactNode = null;
    switch (status) {
        case 'linked':
            variant = 'success';
            line = `Linked as ${githubLogin || session.login}. Your reviews go to GitHub as you.`;
            actions = confirmUnlink ? (
                <>
                    <span style={hint}>Unlink it? Your reviews stop going to GitHub as you.</span>
                    <Button
                        size="sm"
                        variant="danger"
                        onClick={onConfirmUnlink}
                        loading={unlinking}
                    >
                        Unlink
                    </Button>
                    <Button size="sm" variant="secondary" onClick={onKeep}>
                        Keep it
                    </Button>
                </>
            ) : (
                <Button size="sm" variant="secondary" onClick={onUnlink}>
                    Unlink
                </Button>
            );
            break;
        case 'unlinked':
            variant = 'warning';
            line = 'Not linked. Link it so your reviews go to GitHub as you.';
            actions = linkButton();
            break;
        case 'pending':
            variant = 'info';
            line = `A link is waiting for code ${link?.user_code} at ${displayUri(link?.verification_uri || '')}.`;
            actions = linkButton('Continue linking');
            break;
        case 'unavailable':
            line = linkOutcome('unavailable', link?.message || '').title + '.';
            break;
        default: {
            // A link that ended without linking, in the last few minutes.
            const outcome = linkOutcome(status, link?.message || '');
            variant = outcome.variant;
            line = `${outcome.title}. ${outcome.detail}`;
            actions = linkButton();
        }
    }

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: spacing.sm }}>
            <div style={{ display: 'flex', alignItems: 'flex-start', gap: spacing.sm }}>
                <span
                    aria-hidden
                    style={{
                        width: '8px',
                        height: '8px',
                        borderRadius: '50%',
                        background: getStatusColor(variant),
                        marginTop: '7px',
                        flexShrink: 0,
                    }}
                />
                <span style={{ lineHeight: 1.5 }}>{line}</span>
            </div>
            {status === 'unavailable' && link?.message && <div style={hint}>{link.message}</div>}
            {loadError && (
                <div style={{ ...hint, color: colors.textDanger }}>
                    Could not read the link: {loadError}
                </div>
            )}
            {actions && (
                <div
                    style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: spacing.sm,
                        flexWrap: 'wrap',
                    }}
                >
                    {actions}
                </div>
            )}
        </div>
    );
}

const heading: CSSProperties = {
    fontSize: '13px',
    fontWeight: 600,
    color: 'var(--text-secondary)',
    marginBottom: '8px',
    textTransform: 'uppercase',
    letterSpacing: '0.5px',
};

const hint: CSSProperties = {
    fontSize: fontSize.sm,
    color: colors.textSecondary,
    marginTop: spacing.xs,
};

/**
 * The account indicator in the header, on a bridge connected to a shared
 * server: the login, with a dot for the GitHub link — green linked, amber
 * not linked yet, grey when the server cannot link. Opens the Account tab.
 */
export function AccountChip({
    session,
    compact,
    onOpen,
}: {
    session: BridgeSession;
    compact: boolean;
    onOpen: () => void;
}) {
    const who = session.login || 'administrator';
    const variant: StatusVariant = session.github_linked
        ? 'success'
        : session.github_link_available
          ? 'warning'
          : 'neutral';
    const state = session.github_linked
        ? 'GitHub account linked'
        : session.github_link_available
          ? 'GitHub account not linked'
          : 'GitHub accounts cannot be linked on this server';
    return (
        <button
            onClick={onOpen}
            title={`Signed in to ${session.address} as ${who} — ${state}`}
            aria-label={`Account: ${who}, ${state}`}
            style={{
                display: 'flex',
                alignItems: 'center',
                gap: '6px',
                background: 'transparent',
                border: '1px solid var(--border)',
                color: 'var(--text-secondary)',
                padding: '5px 10px',
                borderRadius: '999px',
                fontSize: '13px',
                cursor: 'pointer',
                maxWidth: compact ? '44px' : '180px',
            }}
        >
            <span
                aria-hidden
                style={{
                    width: '8px',
                    height: '8px',
                    borderRadius: '50%',
                    background: getStatusColor(variant),
                    flexShrink: 0,
                }}
            />
            {!compact && (
                <span
                    style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                >
                    {who}
                </span>
            )}
        </button>
    );
}
