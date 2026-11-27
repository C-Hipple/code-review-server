import {
    CHANGE_DIAGRAM,
    reportImageName,
    type AIFeatureOutput,
    type ReportItem,
} from '../ai_utils';
import { useAIReport } from '../hooks/useAIReport';
import AIReportView, { AIRerunButton } from './AIReportView';

interface AIReportPaneProps {
    featureId: string;
    /** The feature's name, for the header; the id stands in until the features load. */
    name?: string;
    owner: string;
    repo: string;
    number: number;
    /** What the review view already has for the feature, shown until the pane's own load lands. */
    initialOutput?: AIFeatureOutput;
    /** Every output the pane loads, so the toolbar count stays current. */
    onOutput: (output: AIFeatureOutput) => void;
    /** Takes the reviewer to an item's thread in the diff, which the pane stays beside. */
    onJumpToItem: (item: ReportItem) => void;
    /** Moves the report back into its modal. */
    onUndock: () => void;
}

/**
 * One AI feature's report docked as a tab of the review, the way a code
 * viewer docks: in split view it sits in the right panel, sticky beside the
 * diff the left one scrolls, so the reviewer can read the report and the code
 * together.
 *
 * It shows what AIReportModal does and keeps itself current the same way
 * (useAIReport), filling the panel's height: the report scrolls within the
 * pane, and the change diagram is fitted to it.
 */
export default function AIReportPane({
    featureId,
    name,
    owner,
    repo,
    number,
    initialOutput,
    onOutput,
    onJumpToItem,
    onUndock,
}: AIReportPaneProps) {
    const report = useAIReport({
        owner,
        repo,
        number,
        feature: featureId,
        initialOutput,
        onOutput,
    });
    const diagram = featureId === CHANGE_DIAGRAM;

    return (
        <div
            data-testid="ai-report-pane"
            style={{ display: 'flex', flexDirection: 'column', height: '100%' }}
        >
            <div
                style={{
                    padding: '8px 12px',
                    background: 'var(--bg-tertiary)',
                    borderBottom: '1px solid var(--border)',
                    display: 'flex',
                    alignItems: 'center',
                    gap: '8px',
                }}
            >
                <span style={{ color: 'var(--accent)' }}>✦</span>
                <span
                    style={{
                        fontSize: '13px',
                        fontWeight: 600,
                        color: 'var(--text-primary)',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                    }}
                >
                    {name ?? featureId}
                </span>
                <span style={{ marginLeft: 'auto', display: 'flex', gap: '6px' }}>
                    <AIRerunButton pending={report.pending} onRerun={report.rerun} size="sm" />
                    <button
                        onClick={onUndock}
                        title="Open in a dialog"
                        aria-label="Open in a dialog"
                        style={{
                            background: 'transparent',
                            border: 'none',
                            color: 'var(--text-secondary)',
                            fontSize: '14px',
                            cursor: 'pointer',
                            padding: '2px 6px',
                            lineHeight: 1,
                            borderRadius: '4px',
                        }}
                    >
                        ↗
                    </button>
                </span>
            </div>
            <div
                className="custom-scrollbar"
                style={{
                    flex: 1,
                    minHeight: 0,
                    overflowY: 'auto',
                    padding: '16px',
                    // The diagram fits itself to the pane rather than scrolling it.
                    ...(diagram ? { display: 'flex', flexDirection: 'column' } : {}),
                }}
            >
                <AIReportView
                    output={report.output}
                    error={report.error}
                    notice={report.notice}
                    onJumpToItem={onJumpToItem}
                    fill={diagram}
                    imageName={reportImageName(owner, repo, number, featureId)}
                />
            </div>
        </div>
    );
}
