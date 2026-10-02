import { useState } from 'react';
import {
    attentionItems,
    canJumpTo,
    changeDiagram,
    commentsReport,
    isPending,
    itemLocation,
    itemVariant,
    sourceLabel,
    statusLabel,
    statusVariant,
    verdictLabel,
    verdictVariant,
    type AIFeatureOutput,
    type CommentsReport,
    type ReportItem,
} from '../ai_utils';
import { Badge, Button, colors } from '../design';
import { relativeTime } from '../discussion_utils';
import MermaidDiagram from './MermaidDiagram';
import PluginBodyView from './PluginBodyView';

interface AIReportViewProps {
    output: AIFeatureOutput | null;
    error?: string | null;
    notice?: string | null;
    gaveUp?: boolean;
    /** Takes the reviewer to an item's thread in the diff. Without it, locations are plain text. */
    onJumpToItem?: (item: ReportItem) => void;
    /** Grow to fill the parent, a flex column, as the change diagram does in its modal. */
    fill?: boolean;
    /** The name the change diagram's Download image saves it as, without the extension. */
    imageName?: string;
}

/**
 * One AI feature's result, as the review view's modal and the full-page AI
 * view both show it: what the run's state is, then the report itself.
 *
 * comments-addressed gets a structured view — what needs attention, with a
 * way to jump to each thread; change-diagram draws its Mermaid source; any
 * other feature renders its markdown body.
 */
export default function AIReportView({
    output,
    error,
    notice,
    gaveUp,
    onJumpToItem,
    fill,
    imageName,
}: AIReportViewProps) {
    const pending = isPending(output);
    const report = commentsReport(output);
    const diagram = changeDiagram(output);
    const hasResult = !!output && output.status !== 'not-run' && output.updated_at !== '';
    const grow: React.CSSProperties = fill ? { flex: 1, minHeight: 0 } : {};

    return (
        <div
            data-testid="ai-report"
            style={{ display: 'flex', flexDirection: 'column', gap: '14px', ...grow }}
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
                    The PR has changed since this report was made. Re-run it for a current answer.
                </Callout>
            )}

            {pending && !hasResult && (
                <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>
                    Working it out… this can take a little while when the model is consulted.
                </div>
            )}

            <div
                style={{
                    opacity: pending && hasResult ? 0.55 : 1,
                    ...(fill && { display: 'flex', flexDirection: 'column', ...grow }),
                }}
            >
                {report ? (
                    <CommentsReportView
                        report={report}
                        attention={attentionItems(output)}
                        onJump={onJumpToItem}
                    />
                ) : diagram ? (
                    <MermaidDiagram
                        source={diagram.mermaid}
                        legend={
                            diagram.diagram_type === 'flowchart' || diagram.diagram_type === 'graph'
                        }
                        fill={fill}
                        imageName={imageName}
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
    );
}

/** Forces a fresh run; reads "Running…" while one is in flight. */
export function AIRerunButton({
    pending,
    onRerun,
    size,
}: {
    pending: boolean;
    onRerun: () => void;
    size?: 'sm' | 'md';
}) {
    return (
        <Button
            variant="secondary"
            size={size}
            onClick={onRerun}
            loading={pending}
            disabled={pending}
            title="Run again, even if nothing changed"
        >
            {pending ? 'Running…' : '↻ Re-run'}
        </Button>
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
    onJump?: (item: ReportItem) => void;
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

function ItemList({ items, onJump }: { items: ReportItem[]; onJump?: (item: ReportItem) => void }) {
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
                        {onJump && canJumpTo(item) ? (
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
                    {item.code_context && <CodeContext code={item.code_context} />}
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

/** A diff line's background: added, removed, a hunk header, or plain context. */
function diffLineBackground(line: string): string {
    if (line.startsWith('@@')) return colors.diffHunkBg;
    if (line.startsWith('+')) return colors.diffAddBg;
    if (line.startsWith('-')) return colors.diffDelBg;
    return 'transparent';
}

/**
 * The code an open thread was left on, as GitHub shows it above a comment. The
 * context ends at the commented line, which gets an accent bar.
 */
function CodeContext({ code }: { code: string }) {
    const lines = code.split('\n');
    return (
        <pre
            data-testid="ai-item-code"
            aria-label="Code the comment was left on"
            style={{
                margin: '0 0 8px',
                padding: '4px 0',
                border: '1px solid var(--border)',
                borderRadius: '4px',
                background: 'var(--bg-secondary)',
                fontFamily: 'var(--font-mono)',
                fontSize: '12px',
                lineHeight: 1.5,
                color: 'var(--text-primary)',
                overflowX: 'auto',
            }}
        >
            {lines.map((line, i) => (
                <span
                    key={i}
                    style={{
                        display: 'block',
                        minWidth: 'fit-content',
                        padding: '0 8px',
                        whiteSpace: 'pre',
                        background: diffLineBackground(line),
                        boxShadow:
                            i === lines.length - 1 ? 'inset 3px 0 0 var(--accent)' : undefined,
                    }}
                >
                    {line || ' '}
                </span>
            ))}
        </pre>
    );
}
