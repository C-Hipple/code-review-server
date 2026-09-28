import { useEffect, useRef, useState } from 'react';
import { getAIOutput, runAIFeature } from '../api';
import {
    attentionItems,
    canJumpTo,
    commentsReport,
    isPending,
    itemLocation,
    itemVariant,
    MAX_POLL_MS,
    pollDelayMs,
    shouldRunOnOpen,
    sourceLabel,
    statusLabel,
    statusVariant,
    verdictLabel,
    verdictVariant,
    type AIFeatureInfo,
    type AIFeatureOutput,
    type CommentsReport,
    type ReportItem,
} from '../ai_utils';
import { Badge, Button, Modal } from '../design';
import { relativeTime } from '../discussion_utils';
import PluginBodyView from './PluginBodyView';

interface AIReportModalProps {
    feature: AIFeatureInfo;
    owner: string;
    repo: string;
    number: number;
    /** What the review view already has for the feature, shown until the modal's own load lands. */
    initialOutput?: AIFeatureOutput;
    onClose: () => void;
    /** Every output the modal loads, so the toolbar count stays current. */
    onOutput: (output: AIFeatureOutput) => void;
    /** Takes the reviewer to an item's thread in the diff. */
    onJumpToItem: (item: ReportItem) => void;
}

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

/**
 * One AI feature's result for the PR under review.
 *
 * Opening it asks the server for a run when there is no result yet or the
 * stored one no longer describes the PR (the server answers from its cache
 * when nothing changed), then polls while the run is pending. The previous
 * result stays on screen, marked as refreshing, until the new one lands.
 *
 * comments-addressed gets a structured view — what needs attention, with a
 * way to jump to each thread; any other feature renders its markdown body.
 */
export default function AIReportModal({
    feature,
    owner,
    repo,
    number,
    initialOutput,
    onClose,
    onOutput,
    onJumpToItem,
}: AIReportModalProps) {
    const [output, setOutput] = useState<AIFeatureOutput | null>(initialOutput ?? null);
    const [error, setError] = useState<string | null>(null);
    const [notice, setNotice] = useState<string | null>(null);
    const [gaveUp, setGaveUp] = useState(false);
    // Bumped by the Re-run button; each value is one forced run.
    const [rerunToken, setRerunToken] = useState(0);

    // Read through a ref so a parent re-render (a new callback identity)
    // doesn't restart the run.
    const onOutputRef = useRef(onOutput);
    useEffect(() => {
        onOutputRef.current = onOutput;
    }, [onOutput]);

    useEffect(() => {
        let cancelled = false;
        let timer: ReturnType<typeof setTimeout> | undefined;
        const pr = { Owner: owner, Repo: repo, Number: number, Feature: feature.id };
        const startedAt = Date.now();

        const publish = (o: AIFeatureOutput) => {
            setOutput(o);
            onOutputRef.current(o);
        };

        const poll = async (attempt: number) => {
            try {
                const o = (await getAIOutput(pr))[feature.id];
                if (cancelled || !o) return;
                publish(o);
                if (!isPending(o)) return;
                if (Date.now() - startedAt > MAX_POLL_MS) {
                    setGaveUp(true);
                    return;
                }
                timer = setTimeout(() => poll(attempt + 1), pollDelayMs(attempt));
            } catch (e) {
                if (!cancelled) setError(errorText(e));
            }
        };

        const start = async () => {
            setError(null);
            setNotice(null);
            setGaveUp(false);
            try {
                let current: AIFeatureOutput | undefined;
                if (rerunToken === 0) {
                    current = (await getAIOutput(pr))[feature.id];
                    if (cancelled) return;
                    if (current) publish(current);
                }
                if (rerunToken > 0 || shouldRunOnOpen(current)) {
                    const reply = await runAIFeature({ ...pr, Force: rerunToken > 0 });
                    if (cancelled) return;
                    if (reply.output) publish(reply.output);
                    if (!reply.okay) {
                        setNotice(reply.message);
                        return;
                    }
                    current = reply.output ?? current;
                }
                if (current && isPending(current)) {
                    timer = setTimeout(() => poll(0), pollDelayMs(0));
                }
            } catch (e) {
                if (!cancelled) setError(errorText(e));
            }
        };

        start();
        return () => {
            cancelled = true;
            if (timer) clearTimeout(timer);
        };
    }, [owner, repo, number, feature.id, rerunToken]);

    const pending = isPending(output);
    const report = commentsReport(output);
    const hasResult = !!output && output.status !== 'not-run' && output.updated_at !== '';

    return (
        <Modal
            isOpen={true}
            onClose={onClose}
            title={feature.name}
            size="xl"
            footer={
                <>
                    <Button
                        variant="secondary"
                        onClick={() => setRerunToken(t => t + 1)}
                        loading={pending}
                        disabled={pending}
                        title="Run again, even if nothing changed"
                    >
                        {pending ? 'Running…' : '↻ Re-run'}
                    </Button>
                    <Button variant="secondary" onClick={onClose}>
                        Close
                    </Button>
                </>
            }
        >
            <div
                data-testid="ai-report"
                style={{ display: 'flex', flexDirection: 'column', gap: '14px' }}
            >
                <StatusLine output={output} report={report} />

                {error && <Callout variant="danger">Could not load the report: {error}</Callout>}
                {notice && <Callout variant="warning">{notice}</Callout>}
                {gaveUp && (
                    <Callout variant="warning">
                        Still running after several minutes — close this and check back later.
                    </Callout>
                )}
                {output?.stale && !pending && (
                    <Callout variant="warning">
                        The PR has changed since this report was made (new commits, comments,
                        reviews or resolved threads). Re-run it for a current answer.
                    </Callout>
                )}

                {pending && !hasResult && (
                    <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>
                        Working it out… this can take a little while when the model is consulted.
                    </div>
                )}

                <div style={{ opacity: pending && hasResult ? 0.55 : 1 }}>
                    {report ? (
                        <CommentsReportView
                            report={report}
                            attention={attentionItems(output)}
                            onJump={onJumpToItem}
                        />
                    ) : (
                        hasResult &&
                        output && (
                            <div className="markdown-content">
                                <PluginBodyView body={output.body} emptyLabel="No output." />
                            </div>
                        )
                    )}
                </div>
            </div>
        </Modal>
    );
}

function StatusLine({
    output,
    report,
}: {
    output: AIFeatureOutput | null;
    report: CommentsReport | null;
}) {
    if (!output) {
        return <div style={{ color: 'var(--text-secondary)' }}>Loading…</div>;
    }
    const updated = relativeTime(output.updated_at);
    return (
        <div
            style={{
                display: 'flex',
                flexWrap: 'wrap',
                alignItems: 'center',
                gap: '8px',
                fontSize: '13px',
                color: 'var(--text-secondary)',
            }}
        >
            {/* A finished run speaks through its verdict; the run's own status
                only matters when it isn't a plain success. */}
            {output.status !== 'success' && (
                <Badge variant={statusVariant(output.status)}>{statusLabel(output.status)}</Badge>
            )}
            {report && (
                <Badge variant={verdictVariant(report.verdict)}>
                    {verdictLabel(report.verdict)}
                </Badge>
            )}
            {output.stale && <Badge variant="warning">Out of date</Badge>}
            {output.truncated && <Badge variant="neutral">Input truncated</Badge>}
            {updated && <span>Updated {updated}</span>}
            {isPending(output) && output.updated_at && <span>· refreshing…</span>}
        </div>
    );
}

function Callout({
    variant,
    children,
}: {
    variant: 'danger' | 'warning';
    children: React.ReactNode;
}) {
    return (
        <div
            role="status"
            style={{
                padding: '10px 12px',
                borderRadius: '6px',
                fontSize: '13px',
                background: variant === 'danger' ? 'var(--bg-danger-dim)' : 'var(--bg-warning-dim)',
                border: `1px solid ${
                    variant === 'danger' ? 'var(--border-danger-dim)' : 'var(--border-warning-dim)'
                }`,
                color: 'var(--text-primary)',
            }}
        >
            {children}
        </div>
    );
}

const sectionTitle: React.CSSProperties = {
    margin: '4px 0 8px',
    fontSize: '14px',
    fontWeight: 600,
    color: 'var(--text-primary)',
};

function CommentsReportView({
    report,
    attention,
    onJump,
}: {
    report: CommentsReport;
    attention: ReportItem[];
    onJump: (item: ReportItem) => void;
}) {
    const [showAddressed, setShowAddressed] = useState(false);
    const addressed = report.items.filter(i => i.status === 'addressed');
    const notes = [
        report.truncated &&
            "Some input was cut to fit the model's prompt; verdicts that depended on it were not trusted.",
        report.model.note,
        !report.model.note &&
            report.model.consulted &&
            `The model judged ${report.model.asked} item(s); every other status is GitHub's.`,
    ].filter(Boolean) as string[];

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
            <div style={{ fontSize: '15px', fontWeight: 600, color: 'var(--text-primary)' }}>
                {report.summary}
            </div>

            {attention.length > 0 && (
                <section>
                    <h3 style={sectionTitle}>Needs attention ({attention.length})</h3>
                    <ItemList items={attention} onJump={onJump} />
                </section>
            )}

            {report.change_requests.length > 0 && (
                <section>
                    <h3 style={sectionTitle}>
                        Changes requested ({report.change_requests.length})
                    </h3>
                    <ul style={{ margin: 0, paddingLeft: '18px' }}>
                        {report.change_requests.map(cr => (
                            <li
                                key={cr.review_id}
                                style={{ marginBottom: '6px', fontSize: '13px' }}
                            >
                                <strong>{cr.reviewer}</strong> still requests changes
                                {cr.excerpt && <>: “{cr.excerpt}”</>}{' '}
                                {cr.html_url && (
                                    <a href={cr.html_url} target="_blank" rel="noreferrer">
                                        review ↗
                                    </a>
                                )}
                            </li>
                        ))}
                    </ul>
                </section>
            )}

            {addressed.length > 0 && (
                <section>
                    <button
                        type="button"
                        onClick={() => setShowAddressed(s => !s)}
                        aria-expanded={showAddressed}
                        style={{
                            ...sectionTitle,
                            background: 'none',
                            border: 'none',
                            padding: 0,
                            cursor: 'pointer',
                        }}
                    >
                        {showAddressed ? '▼' : '▶'} Addressed ({addressed.length})
                    </button>
                    {showAddressed && <ItemList items={addressed} onJump={onJump} />}
                </section>
            )}

            {report.missing.length > 0 && (
                <Callout variant="warning">Missing input: {report.missing.join('; ')}.</Callout>
            )}

            {notes.map(n => (
                <div
                    key={n}
                    style={{
                        fontSize: '12px',
                        fontStyle: 'italic',
                        color: 'var(--text-secondary)',
                    }}
                >
                    {n}
                </div>
            ))}
        </div>
    );
}

function ItemList({ items, onJump }: { items: ReportItem[]; onJump: (item: ReportItem) => void }) {
    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
            {items.map(item => (
                <div
                    key={`${item.kind}:${item.root_comment_id}`}
                    className="ai-report-item"
                    style={{
                        border: '1px solid var(--border)',
                        borderRadius: '6px',
                        padding: '10px 12px',
                        background: 'var(--bg-primary)',
                    }}
                >
                    <div
                        style={{
                            display: 'flex',
                            flexWrap: 'wrap',
                            alignItems: 'center',
                            gap: '8px',
                            marginBottom: '6px',
                        }}
                    >
                        <Badge variant={itemVariant(item.status)} size="sm">
                            {item.status}
                        </Badge>
                        <Badge variant={item.source === 'model' ? 'info' : 'neutral'} size="sm">
                            {sourceLabel(item.source)}
                        </Badge>
                        {canJumpTo(item) ? (
                            <button
                                type="button"
                                onClick={() => onJump(item)}
                                title="Show this thread in the diff"
                                style={{
                                    background: 'none',
                                    border: 'none',
                                    padding: 0,
                                    color: 'var(--accent)',
                                    cursor: 'pointer',
                                    fontFamily: 'var(--font-mono)',
                                    fontSize: '12px',
                                }}
                            >
                                {itemLocation(item)}
                            </button>
                        ) : (
                            <span
                                style={{
                                    fontFamily: 'var(--font-mono)',
                                    fontSize: '12px',
                                    color: 'var(--text-secondary)',
                                }}
                            >
                                {itemLocation(item)}
                            </span>
                        )}
                        {item.html_url && (
                            <a
                                href={item.html_url}
                                target="_blank"
                                rel="noreferrer"
                                style={{ fontSize: '12px', marginLeft: 'auto' }}
                            >
                                GitHub ↗
                            </a>
                        )}
                    </div>
                    <div style={{ fontSize: '13px', color: 'var(--text-primary)' }}>
                        <strong>{item.author}</strong>: {item.excerpt}
                    </div>
                    <div
                        style={{
                            fontSize: '12px',
                            color: 'var(--text-secondary)',
                            marginTop: '4px',
                        }}
                    >
                        {item.rationale}
                    </div>
                    {item.model_note && (
                        <div
                            style={{
                                fontSize: '12px',
                                color: 'var(--text-tertiary)',
                                fontStyle: 'italic',
                                marginTop: '4px',
                            }}
                        >
                            {item.model_note}
                        </div>
                    )}
                </div>
            ))}
        </div>
    );
}
