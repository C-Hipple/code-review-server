import { useEffect, useRef, useState, type ReactNode } from 'react';
import { listAIFeatures } from '../api';
import type { AIFeatureInfo, AIFeatureOutput } from '../ai_utils';
import { Button, Card } from '../design';
import { useAIReport } from '../hooks/useAIReport';
import AIReportView, { AIRerunButton } from './AIReportView';

interface AIOutputProps {
    owner: string;
    repo: string;
    number: number;
    onOpenReview: (owner: string, repo: string, number: number) => void;
    onClose?: () => void;
}

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

/**
 * Every enabled AI feature's report for one PR, on a page of its own — the
 * counterpart of the plugin view, reached from the list's AI button or
 * `?view=ai`.
 *
 * Each feature's card behaves like the review view's report modal: opening
 * the page runs a feature that never ran for the PR or went stale (the server
 * answers from its cache when nothing changed), the card polls while the run
 * is pending, and ↻ Re-run forces a fresh one. There is no diff here to jump
 * into, so item locations are plain text; Open review goes to the PR.
 */
export default function AIOutput({ owner, repo, number, onOpenReview, onClose }: AIOutputProps) {
    // Null until the first ListAIFeatures reply.
    const [features, setFeatures] = useState<AIFeatureInfo[] | null>(null);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);
    // Refresh remounts the cards, so each reloads its output and runs again
    // if it went stale. The last output a card showed seeds its replacement,
    // so the page doesn't blank while that happens.
    const [generation, setGeneration] = useState(0);
    const [outputs, setOutputs] = useState<Record<string, AIFeatureOutput>>({});
    const loadSeq = useRef(0);

    const loadFeatures = async () => {
        const seq = ++loadSeq.current;
        setLoading(true);
        try {
            const enabled = (await listAIFeatures()).filter(f => f.enabled);
            if (seq !== loadSeq.current) return;
            setFeatures(enabled);
            setError(null);
        } catch (e) {
            console.error('Failed to load AI features:', e);
            if (seq === loadSeq.current) setError(errorText(e));
        } finally {
            if (seq === loadSeq.current) setLoading(false);
        }
    };

    useEffect(() => {
        loadFeatures();
    }, []);

    useEffect(() => {
        if (!onClose) return;
        const handleKeyDown = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                onClose();
            }
        };
        window.addEventListener('keydown', handleKeyDown);
        return () => window.removeEventListener('keydown', handleKeyDown);
    }, [onClose]);

    const refresh = () => {
        setGeneration(g => g + 1);
        loadFeatures();
    };

    // Cards are keyed by PR as well as feature: back/forward can move this
    // page to another PR without remounting it, and a card must not carry
    // one PR's result (or a pending forced re-run) over to the next.
    const pr = `${owner}/${repo}#${number}`;

    // Once the features are listed, a failed refresh keeps them: the cards
    // remount regardless and report their own load errors.
    let body: ReactNode;
    if (features === null) {
        body = (
            <Message>
                {error ? `Could not load the AI features: ${error}` : 'Loading AI features…'}
            </Message>
        );
    } else if (features.length === 0) {
        body = (
            <Message italic>
                No AI features are enabled. Turn one on with an [[AIFeatures]] entry in the
                server&apos;s config.
            </Message>
        );
    } else {
        body = (
            <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
                {features.map(feature => {
                    const seed = `${pr}:${feature.id}`;
                    return (
                        <AIFeatureCard
                            key={`${seed}:${generation}`}
                            feature={feature}
                            owner={owner}
                            repo={repo}
                            number={number}
                            initialOutput={outputs[seed]}
                            onOutput={output => setOutputs(prev => ({ ...prev, [seed]: output }))}
                        />
                    );
                })}
            </div>
        );
    }

    return (
        <div className="ai-output">
            <Card padding="lg" style={{ marginBottom: '20px' }}>
                <div
                    style={{
                        display: 'flex',
                        flexWrap: 'wrap',
                        justifyContent: 'space-between',
                        alignItems: 'center',
                        gap: '12px',
                        marginBottom: '15px',
                    }}
                >
                    <h2 style={{ margin: 0, fontSize: '18px' }}>
                        AI Reports for {owner}/{repo} #{number}
                    </h2>
                    <div style={{ display: 'flex', gap: '12px', alignItems: 'center' }}>
                        <Button onClick={refresh} loading={loading} variant="secondary">
                            Refresh
                        </Button>
                        <Button
                            onClick={() => onOpenReview(owner, repo, number)}
                            variant="secondary"
                        >
                            Open review
                        </Button>
                        {onClose && (
                            <Button onClick={onClose} variant="secondary">
                                Close (Esc)
                            </Button>
                        )}
                    </div>
                </div>

                {body}
            </Card>
        </div>
    );
}

interface AIFeatureCardProps {
    feature: AIFeatureInfo;
    owner: string;
    repo: string;
    number: number;
    /** Shown until the card's own load lands. */
    initialOutput?: AIFeatureOutput;
    onOutput?: (output: AIFeatureOutput) => void;
}

/** One feature's report on the AI page, run on mount when it never ran or went stale. */
export function AIFeatureCard({
    feature,
    owner,
    repo,
    number,
    initialOutput,
    onOutput,
}: AIFeatureCardProps) {
    const report = useAIReport({
        owner,
        repo,
        number,
        feature: feature.id,
        initialOutput,
        onOutput,
    });

    return (
        <Card variant="outlined" padding="none" style={{ overflow: 'hidden' }}>
            <div
                style={{
                    padding: '12px 16px',
                    background: 'var(--bg-tertiary)',
                    borderBottom: '1px solid var(--border)',
                    display: 'flex',
                    alignItems: 'center',
                    gap: '10px',
                }}
            >
                <AIRerunButton pending={report.pending} onRerun={report.rerun} size="sm" />
                <div style={{ flex: 1, minWidth: 0 }}>
                    <h3 style={{ margin: 0, fontSize: '15px', fontWeight: 600 }}>
                        <span aria-hidden="true">✦ </span>
                        {feature.name}
                    </h3>
                    {feature.description && (
                        <div
                            style={{
                                marginTop: '2px',
                                fontSize: '12px',
                                color: 'var(--text-secondary)',
                            }}
                        >
                            {feature.description}
                        </div>
                    )}
                </div>
            </div>
            <div style={{ padding: '16px' }}>
                <AIReportView
                    output={report.output}
                    error={report.error}
                    notice={report.notice}
                    gaveUp={report.gaveUp}
                />
            </div>
        </Card>
    );
}

function Message({ italic, children }: { italic?: boolean; children: ReactNode }) {
    return (
        <div
            style={{
                padding: '40px',
                textAlign: 'center',
                color: 'var(--text-secondary)',
                fontStyle: italic ? 'italic' : 'normal',
            }}
        >
            {children}
        </div>
    );
}
