// Placeholder panel: exercises the plumbing (service worker → native host →
// server) with GetAllReviews and the connection status. The PR list and PR
// tools views replace it.

import { StrictMode, useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { getAllReviews, getStatus, RpcError } from '../rpc';
import type { HostStatus, ReviewItem } from '../types';
import { loadMermaid } from './mermaid_loader';
import { linkTarget, readPanelParams, requestClose } from './params';

const params = readPanelParams(window.location.search);

type Load<T> =
    { state: 'loading' } | { state: 'done'; value: T } | { state: 'error'; error: RpcError };

function toRpcError(e: unknown): RpcError {
    return e instanceof RpcError
        ? e
        : new RpcError('extension', e instanceof Error ? e.message : String(e));
}

function App() {
    const [reviews, setReviews] = useState<Load<ReviewItem[]>>({ state: 'loading' });
    const [status, setStatus] = useState<HostStatus | null>(null);
    const [attempt, setAttempt] = useState(0);

    useEffect(() => {
        let live = true;
        getAllReviews()
            .then(
                value => live && setReviews({ state: 'done', value }),
                e => live && setReviews({ state: 'error', error: toRpcError(e) })
            )
            .then(() => getStatus())
            .then(
                s => live && setStatus(s),
                () => {}
            );
        return () => {
            live = false;
        };
    }, [attempt]);

    // Esc inside the iframe closes the modal, like Esc on the page does.
    useEffect(() => {
        if (params.standalone) return;
        const onKey = (e: KeyboardEvent) => {
            if (e.key === 'Escape') requestClose();
        };
        window.addEventListener('keydown', onKey);
        return () => window.removeEventListener('keydown', onKey);
    }, []);

    const retry = () => {
        setReviews({ state: 'loading' });
        setAttempt(a => a + 1);
    };

    const pr = params.pr;
    return (
        <div className="panel">
            <header className="header">
                <span className="product">Code Review Server</span>
                <span className="crumb">
                    {pr ? `${pr.owner}/${pr.repo}#${pr.number}` : 'Review list'}
                </span>
                <span
                    className={`dot ${status?.connected ? 'dot-ok' : reviews.state === 'error' ? 'dot-error' : ''}`}
                    title={status?.connected ? 'Connected' : 'Not connected'}
                />
                {!params.standalone && (
                    <button className="icon-button" aria-label="Close" onClick={requestClose}>
                        ✕
                    </button>
                )}
            </header>
            <main className="content">
                {reviews.state === 'loading' && <p className="muted">Loading…</p>}
                {reviews.state === 'error' && <ErrorCard error={reviews.error} onRetry={retry} />}
                {reviews.state === 'done' && <ReviewList items={reviews.value} />}
                {status?.host && (
                    <p className="muted small">
                        Server {status.host.server_path} ({status.host.version}) · log{' '}
                        {status.host.log_path}
                    </p>
                )}
                <MermaidCheck />
            </main>
        </div>
    );
}

function ReviewList({ items }: { items: ReviewItem[] }) {
    if (items.length === 0) {
        return (
            <p className="muted">
                No PRs in your review list — configure workflows in the server config.
            </p>
        );
    }
    const target = linkTarget(params);
    return (
        <ul className="list">
            {items.map(item => (
                <li key={`${item.owner}/${item.repo}#${item.number}`}>
                    <a href={item.url} target={target} rel="noreferrer">
                        {item.title}
                    </a>{' '}
                    <span className="muted">
                        {item.owner}/{item.repo}#{item.number} · {item.section}
                    </span>
                </li>
            ))}
        </ul>
    );
}

function ErrorCard({ error, onRetry }: { error: RpcError; onRetry: () => void }) {
    return (
        <div className="error-card" role="alert">
            {error.kind === 'host-missing' ? (
                <>
                    <strong>The native host isn't installed.</strong>
                    <ol>
                        <li>
                            In your code-review-server checkout, run{' '}
                            <code>chrome_extension/crs_native_host/install.sh</code>.
                        </li>
                        <li>Reload the extension, then retry.</li>
                    </ol>
                    <p className="muted small">Extension id: {chrome.runtime.id}</p>
                </>
            ) : (
                <>
                    <strong>
                        {error.kind === 'server' ? 'The server is not running.' : 'Request failed.'}
                    </strong>
                    <p>{error.message}</p>
                    {error.logPath && <p className="muted small">Log: {error.logPath}</p>}
                </>
            )}
            <button onClick={onRetry}>Retry</button>
        </div>
    );
}

/** Loads the mermaid chunk and draws a fixed diagram, to check it works under the extension's CSP. */
function MermaidCheck() {
    const [svg, setSvg] = useState('');
    const [error, setError] = useState('');
    const render = async () => {
        try {
            const mermaid = await loadMermaid();
            const result = await mermaid.render(
                'crs-mermaid-check',
                'flowchart LR\n  A[Panel] --> B[Service worker] --> C[Native host] --> D[Server]'
            );
            setSvg(result.svg);
            setError('');
        } catch (e) {
            setError(e instanceof Error ? e.message : String(e));
        }
    };
    return (
        <details className="small">
            <summary>Diagram check</summary>
            <button onClick={() => void render()}>Render test diagram</button>
            {error && <p className="error-text">{error}</p>}
            {/* mermaid's strict mode sanitizes the SVG it returns. */}
            {svg && <div className="diagram" dangerouslySetInnerHTML={{ __html: svg }} />}
        </details>
    );
}

const root = document.getElementById('root');
if (root) {
    createRoot(root).render(
        <StrictMode>
            <App />
        </StrictMode>
    );
}
