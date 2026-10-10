// Failures, by kind (ErrorKind in native_protocol.ts). The connection-level
// ones — no host, a host that refuses the extension, a server that won't
// start — take over the panel with the steps to fix them; anything else
// shows inline in the section that failed, with a Retry.

import type { RpcError } from '../rpc';
import { Button } from './Button';
import { RefreshIcon } from './icons';

const INSTALL_SCRIPT = 'chrome_extension/crs_native_host/install.sh';

export interface ErrorCardProps {
    error: RpcError;
    onRetry?: () => void;
    /** `panel` fills the view; `inline` sits in a section. */
    variant?: 'panel' | 'inline';
    /** chrome.runtime.id, for the install steps. */
    extensionId?: string;
    /** What was being loaded, for an inline error's title: "Couldn't load {what}". */
    what?: string;
}

/** The heading for an error of `kind`. */
export function errorTitle(error: RpcError, what?: string): string {
    switch (error.kind) {
        case 'host-missing':
            return 'Connect the extension to code-review-server';
        case 'host-forbidden':
            return "The native host doesn't allow this extension";
        case 'server':
            return "The server isn't running";
        case 'disconnected':
            return 'Lost the connection to the server';
        case 'timeout':
            return "The server didn't answer in time";
        default:
            return what ? `Couldn't load ${what}` : 'Something went wrong';
    }
}

export function ErrorCard({
    error,
    onRetry,
    variant = 'inline',
    extensionId,
    what,
}: ErrorCardProps) {
    const retry = onRetry && (
        <Button size={variant === 'panel' ? 'md' : 'sm'} icon={<RefreshIcon />} onClick={onRetry}>
            Retry
        </Button>
    );

    if (variant === 'inline') {
        return (
            <div className="error-inline" role="alert">
                <div className="error-inline-text">
                    <strong>{errorTitle(error, what)}</strong>
                    <span className="error-message">{error.message}</span>
                    {error.logPath && (
                        <span className="muted small">
                            Log: <code>{error.logPath}</code>
                        </span>
                    )}
                </div>
                {retry}
            </div>
        );
    }

    return (
        <div className={`error-panel error-${error.kind}`} role="alert">
            <h2 className="error-title">{errorTitle(error, what)}</h2>
            <ErrorDetails error={error} extensionId={extensionId} />
            {retry && <div className="error-actions">{retry}</div>}
        </div>
    );
}

function ErrorDetails({ error, extensionId }: { error: RpcError; extensionId?: string }) {
    switch (error.kind) {
        case 'host-missing':
            return (
                <>
                    <p>
                        The extension reaches the server through a small native host program, which
                        isn't installed for this browser yet.
                    </p>
                    <ol className="steps">
                        <li>
                            In your code-review-server checkout, run <code>{INSTALL_SCRIPT}</code>.
                            Add <code>--capture-env</code> to hand the server your shell&apos;s{' '}
                            <code>PATH</code> and tokens (<code>CRS_GITHUB_TOKEN</code>, …).
                        </li>
                        <li>
                            Reload the extension on <code>chrome://extensions</code> (or restart the
                            browser), then press Retry.
                        </li>
                    </ol>
                    {extensionId && <ExtensionId id={extensionId} />}
                </>
            );
        case 'host-forbidden':
            return (
                <>
                    <p>
                        The native host is installed, but its manifest doesn&apos;t list this
                        extension&apos;s id.
                    </p>
                    <ol className="steps">
                        <li>
                            Run{' '}
                            <code>
                                {INSTALL_SCRIPT}
                                {extensionId ? ` --extension-id ${extensionId}` : ''}
                            </code>
                            .
                        </li>
                        <li>Reload the extension, then press Retry.</li>
                    </ol>
                    {extensionId && <ExtensionId id={extensionId} />}
                </>
            );
        case 'server':
            return (
                <>
                    <p className="error-message">{error.message}</p>
                    {error.logPath && (
                        <p className="muted">
                            The native host&apos;s log has the details: <code>{error.logPath}</code>
                        </p>
                    )}
                    <p className="muted">
                        The host starts <code>codereviewserver --server</code> with the variables in{' '}
                        <code>~/.crs/native_host.env</code> — check that it is on the host&apos;s{' '}
                        <code>PATH</code> and that <code>CRS_GITHUB_TOKEN</code> is set.
                    </p>
                </>
            );
        default:
            return (
                <>
                    <p className="error-message">{error.message}</p>
                    {error.logPath && (
                        <p className="muted">
                            Log: <code>{error.logPath}</code>
                        </p>
                    )}
                </>
            );
    }
}

function ExtensionId({ id }: { id: string }) {
    return (
        <p className="muted small">
            Extension id: <code>{id}</code>
        </p>
    );
}
