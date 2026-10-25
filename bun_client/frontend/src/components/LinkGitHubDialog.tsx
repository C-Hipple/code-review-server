import {
    useCallback,
    useEffect,
    useRef,
    useState,
    type CSSProperties,
    type ReactNode,
} from 'react';
import { getGitHubLink, rpcErrorMessage, startGitHubLink, type StartGitHubLinkReply } from '../api';
import {
    Button,
    Modal,
    borderRadius,
    colors,
    fontSize,
    fontWeight,
    getStatusTone,
    spacing,
    type StatusVariant,
} from '../design';
import {
    displayUri,
    formatLinkExpiry,
    linkOutcome,
    pollGitHubLink,
    type LinkPhase,
} from '../github_link_utils';
import { useBridgeSession } from '../hooks/useBridgeSession';
import { bridgeSession } from '../session';

interface LinkGitHubDialogProps {
    isOpen: boolean;
    onClose: () => void;
    /**
     * Why a link is needed — the refusal that stopped a write. With one, the
     * dialog explains and offers to link; without, it starts straight away.
     */
    reason?: string;
    /** What to do once linked, e.g. submit the refused review again. */
    retryLabel?: string;
    onRetry?: () => void;
    /** Called once the account is linked. */
    onLinked?: () => void;
}

/**
 * Links the user's GitHub account on a shared server, so their reviews go to
 * GitHub as them: shows the code GitHub issued with a copy button and a link
 * to github.com/login/device, polls the server at the interval it gives until
 * the link ends, and says how it ended. Closing it, or unmounting it, stops
 * the polling; the server keeps the link going, and opening it again picks
 * the same code back up.
 */
export default function LinkGitHubDialog({
    isOpen,
    onClose,
    reason,
    retryLabel,
    onRetry,
    onLinked,
}: LinkGitHubDialogProps) {
    const session = useBridgeSession();
    const [phase, setPhase] = useState<LinkPhase>({ kind: 'offer' });
    // Bumped on every start and close, so an answer for an earlier one is
    // dropped rather than painted over the current one.
    const generation = useRef(0);
    const stopPolling = useRef<(() => void) | null>(null);
    const onLinkedRef = useRef(onLinked);
    onLinkedRef.current = onLinked;

    const halt = useCallback(() => {
        generation.current++;
        stopPolling.current?.();
        stopPolling.current = null;
    }, []);

    const start = useCallback(async () => {
        halt();
        const gen = generation.current;
        setPhase({ kind: 'starting' });
        const finish = (status: Extract<LinkPhase, { kind: 'done' }>) => {
            if (gen !== generation.current) return;
            setPhase(status);
            if (status.status === 'linked') {
                void bridgeSession.refresh();
                onLinkedRef.current?.();
            }
        };
        let started: StartGitHubLinkReply;
        try {
            started = await startGitHubLink();
        } catch (e) {
            finish({ kind: 'done', status: 'error', message: rpcErrorMessage(e), githubLogin: '' });
            return;
        }
        if (gen !== generation.current) return;
        if (started.status !== 'pending') {
            finish({
                kind: 'done',
                status: started.status,
                message: started.message,
                githubLogin: '',
            });
            return;
        }
        const pending = {
            kind: 'pending' as const,
            userCode: started.user_code,
            verificationUri: started.verification_uri,
            expiresAt: started.expires_at,
            interval: started.interval,
        };
        setPhase(pending);
        stopPolling.current = pollGitHubLink({
            intervalSeconds: started.interval,
            getLink: getGitHubLink,
            onReply: reply => {
                if (reply.status === 'pending') {
                    if (gen === generation.current) setPhase(pending);
                    return;
                }
                finish({
                    kind: 'done',
                    status: reply.status,
                    message: reply.message,
                    githubLogin: reply.github_login,
                });
            },
            onError: e => {
                if (gen !== generation.current) return;
                setPhase({ ...pending, trouble: rpcErrorMessage(e) });
            },
        });
    }, [halt]);

    // Opening shows the offer, or starts straight away; closing stops polling.
    useEffect(() => {
        if (!isOpen) return;
        if (reason) {
            setPhase({ kind: 'offer' });
        } else {
            void start();
        }
        return halt;
    }, [isOpen, reason, start, halt]);

    return (
        <LinkGitHubDialogView
            isOpen={isOpen}
            phase={phase}
            reason={reason}
            login={session?.login || ''}
            retryLabel={retryLabel}
            onStart={start}
            onRetry={onRetry}
            onClose={onClose}
        />
    );
}

interface LinkGitHubDialogViewProps {
    isOpen: boolean;
    phase: LinkPhase;
    reason?: string;
    /** The login the user is registered as, which GitHub has to agree with. */
    login: string;
    retryLabel?: string;
    onStart: () => void;
    onRetry?: () => void;
    onClose: () => void;
}

/** The dialog as it looks in each phase; LinkGitHubDialog drives it. */
export function LinkGitHubDialogView({
    isOpen,
    phase,
    reason,
    login,
    retryLabel,
    onStart,
    onRetry,
    onClose,
}: LinkGitHubDialogViewProps) {
    let body: ReactNode;
    let actions: ReactNode;

    switch (phase.kind) {
        case 'offer':
            body = (
                <>
                    {reason && <Callout variant="warning">{reason}</Callout>}
                    <p style={paragraph}>
                        This server sends reviews to GitHub as your own GitHub account, once you
                        link it. GitHub gives you a one-time code to confirm it is you.
                    </p>
                </>
            );
            actions = (
                <>
                    <Button variant="secondary" onClick={onClose}>
                        Cancel
                    </Button>
                    <Button onClick={onStart}>Link GitHub account</Button>
                </>
            );
            break;
        case 'starting':
            body = <p style={paragraph}>Asking GitHub for a code…</p>;
            actions = (
                <Button variant="secondary" onClick={onClose}>
                    Cancel
                </Button>
            );
            break;
        case 'pending': {
            const expires = formatLinkExpiry(phase.expiresAt);
            body = (
                <>
                    <p style={paragraph}>Enter this code on GitHub:</p>
                    <UserCode code={phase.userCode} />
                    <a
                        href={phase.verificationUri}
                        target="_blank"
                        rel="noopener noreferrer"
                        style={openLink}
                    >
                        Open {displayUri(phase.verificationUri)} ↗
                    </a>
                    <p style={{ ...paragraph, color: colors.textSecondary, fontSize: fontSize.sm }}>
                        Sign in to GitHub{login ? ` as ${login}` : ''} if you aren&apos;t already,
                        enter the code and authorize the app.
                        {expires && ` The code expires at ${expires}.`}
                    </p>
                    <p
                        aria-live="polite"
                        style={{ ...paragraph, color: colors.textSecondary, fontSize: fontSize.sm }}
                    >
                        {phase.trouble
                            ? `Can't reach the server right now (${phase.trouble}); still trying…`
                            : 'Waiting for GitHub…'}
                    </p>
                </>
            );
            actions = (
                <Button variant="secondary" onClick={onClose}>
                    Cancel
                </Button>
            );
            break;
        }
        case 'done': {
            const outcome = linkOutcome(phase.status, phase.message, phase.githubLogin);
            const linked = phase.status === 'linked';
            body = (
                <Callout variant={outcome.variant} title={outcome.title}>
                    {outcome.detail}
                </Callout>
            );
            actions = (
                <>
                    <Button variant="secondary" onClick={onClose}>
                        {linked && !onRetry ? 'Done' : 'Close'}
                    </Button>
                    {linked && onRetry && retryLabel && (
                        <Button onClick={onRetry}>{retryLabel}</Button>
                    )}
                    {!linked && outcome.canRetry && <Button onClick={onStart}>Try again</Button>}
                </>
            );
            break;
        }
    }

    return (
        <Modal isOpen={isOpen} onClose={onClose} title="Link your GitHub account" size="sm">
            <div style={{ display: 'flex', flexDirection: 'column', gap: spacing.md }}>
                {body}
                <div
                    style={{
                        display: 'flex',
                        gap: spacing.md,
                        justifyContent: 'flex-end',
                        marginTop: spacing.sm,
                    }}
                >
                    {actions}
                </div>
            </div>
        </Modal>
    );
}

const paragraph: CSSProperties = { margin: 0, lineHeight: 1.5, color: colors.textPrimary };

const openLink: CSSProperties = {
    alignSelf: 'flex-start',
    color: colors.accent,
    fontWeight: fontWeight.medium,
    textDecoration: 'none',
};

/** A callout in a status tone, with an optional bold first line. */
function Callout({
    variant,
    title,
    children,
}: {
    variant: StatusVariant;
    title?: string;
    children: ReactNode;
}) {
    const tone = getStatusTone(variant);
    return (
        <div
            style={{
                background: tone.bg,
                border: `1px solid ${tone.border}`,
                borderRadius: borderRadius.md,
                padding: `${spacing.sm} ${spacing.md}`,
                lineHeight: 1.5,
            }}
        >
            {title && (
                <div style={{ color: tone.fg, fontWeight: fontWeight.semibold }}>{title}</div>
            )}
            <div style={{ color: colors.textPrimary, fontSize: fontSize.sm }}>{children}</div>
        </div>
    );
}

/** The user code, large enough to read across a room, with a copy button. */
function UserCode({ code }: { code: string }) {
    const [copy, setCopy] = useState<'idle' | 'copied' | 'failed'>('idle');
    useEffect(() => setCopy('idle'), [code]);

    const handleCopy = () => {
        // The clipboard needs a secure context: localhost, or https.
        const write = navigator.clipboard?.writeText(code);
        if (!write) {
            setCopy('failed');
            return;
        }
        write.then(
            () => setCopy('copied'),
            () => setCopy('failed')
        );
    };

    return (
        <div style={{ display: 'flex', alignItems: 'center', gap: spacing.md }}>
            <code
                aria-label="GitHub user code"
                style={{
                    fontFamily: 'var(--font-mono)',
                    fontSize: '28px',
                    fontWeight: fontWeight.bold,
                    letterSpacing: '3px',
                    padding: `${spacing.sm} ${spacing.lg}`,
                    background: colors.bgTertiary,
                    border: `1px solid ${colors.border}`,
                    borderRadius: borderRadius.md,
                    userSelect: 'all',
                }}
            >
                {code}
            </code>
            <Button variant="secondary" size="sm" onClick={handleCopy}>
                {copy === 'copied' ? '✓ Copied' : copy === 'failed' ? 'Select it to copy' : 'Copy'}
            </Button>
        </div>
    );
}
