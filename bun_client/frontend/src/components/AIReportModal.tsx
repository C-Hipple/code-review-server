import {
    CHANGE_DIAGRAM,
    reportImageName,
    type AIFeatureInfo,
    type AIFeatureOutput,
    type ReportItem,
} from '../ai_utils';
import { Button, Modal } from '../design';
import { useAIReport } from '../hooks/useAIReport';
import AIReportView, { AIRerunButton } from './AIReportView';

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

/**
 * One AI feature's result for the PR under review.
 *
 * Opening it asks the server for a run when there is no result yet or the
 * stored one no longer describes the PR, then polls while the run is pending
 * (see useAIReport). The previous result stays on screen, marked as
 * refreshing, until the new one lands.
 *
 * The change diagram takes most of the screen, so a large diagram stays
 * legible; every other report opens at the usual width.
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
    const report = useAIReport({
        owner,
        repo,
        number,
        feature: feature.id,
        initialOutput,
        onOutput,
    });

    const diagram = feature.id === CHANGE_DIAGRAM;

    return (
        <Modal
            isOpen={true}
            onClose={onClose}
            title={feature.name}
            size={diagram ? 'full' : 'xl'}
            footer={
                <>
                    <AIRerunButton pending={report.pending} onRerun={report.rerun} />
                    <Button variant="secondary" onClick={onClose}>
                        Close
                    </Button>
                </>
            }
        >
            <AIReportView
                output={report.output}
                error={report.error}
                notice={report.notice}
                gaveUp={report.gaveUp}
                onJumpToItem={onJumpToItem}
                fill={diagram}
                imageName={reportImageName(owner, repo, number, feature.id)}
            />
        </Modal>
    );
}
