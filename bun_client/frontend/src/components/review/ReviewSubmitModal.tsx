import { Button, Modal, TextArea, colors } from '../../design';
import type { PendingCommentPreview } from '../../review_preview_utils';
import ReviewPreview from './ReviewPreview';
import type { DiffTheme } from './diff_theme';

interface ReviewSubmitModalProps {
    isOpen: boolean;
    reviewEvent: string;
    reviewBody: string;
    isSubmittingReview: boolean;
    // Unsubmitted comments this review will post, in diff order, each with the
    // diff rows it was left on. Empty when the reviewer left none.
    pendingPreviews: PendingCommentPreview[];
    // Who the review posts as, for the preview's verdict line.
    username?: string;
    diffTheme: DiffTheme;
    onChangeReviewEvent: (event: string) => void;
    onChangeReviewBody: (body: string) => void;
    onClose: () => void;
    onSubmit: () => void;
}

// Modal for submitting a review: pick Comment / Approve / Request Changes and
// an optional body, with a live preview of everything the submit will post.
export default function ReviewSubmitModal({
    isOpen,
    reviewEvent,
    reviewBody,
    isSubmittingReview,
    pendingPreviews,
    username,
    diffTheme,
    onChangeReviewEvent,
    onChangeReviewBody,
    onClose,
    onSubmit,
}: ReviewSubmitModalProps) {
    // A review carrying comments needs the room to show them; a bare
    // approve/comment keeps the compact modal it has always had.
    const hasComments = pendingPreviews.length > 0;

    return (
        <Modal
            isOpen={isOpen}
            onClose={onClose}
            title="Submit Review"
            size={hasComments ? 'lg' : 'sm'}
        >
            <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                <div style={{ display: 'flex', gap: '8px' }}>
                    {[
                        { value: 'COMMENT', label: 'Comment' },
                        { value: 'APPROVE', label: 'Approve' },
                        { value: 'REQUEST_CHANGES', label: 'Request Changes' },
                    ].map(option => {
                        const selected = reviewEvent === option.value;
                        let bgColor = 'var(--bg-primary)';
                        let borderColor = 'var(--border)';
                        let textColor = 'var(--text-primary)';

                        if (selected) {
                            if (option.value === 'APPROVE') {
                                bgColor = colors.success;
                                borderColor = colors.success;
                                textColor = 'white';
                            } else if (option.value === 'REQUEST_CHANGES') {
                                bgColor = colors.danger;
                                borderColor = colors.danger;
                                textColor = 'white';
                            } else {
                                bgColor = 'var(--accent)';
                                borderColor = 'var(--accent)';
                                textColor = 'white';
                            }
                        }

                        return (
                            <button
                                key={option.value}
                                type="button"
                                onClick={() => onChangeReviewEvent(option.value)}
                                disabled={isSubmittingReview}
                                style={{
                                    flex: 1,
                                    padding: '8px',
                                    background: bgColor,
                                    border: `1px solid ${borderColor}`,
                                    color: textColor,
                                    borderRadius: '4px',
                                    cursor: isSubmittingReview ? 'default' : 'pointer',
                                    fontFamily: 'inherit',
                                    fontSize: '14px',
                                    fontWeight: 500,
                                }}
                            >
                                {option.label}
                            </button>
                        );
                    })}
                </div>
                <TextArea
                    placeholder="Review Body (Optional)"
                    value={reviewBody}
                    onChange={e => onChangeReviewBody(e.target.value)}
                    rows={5}
                    disabled={isSubmittingReview}
                />
                <ReviewPreview
                    reviewEvent={reviewEvent}
                    reviewBody={reviewBody}
                    username={username}
                    previews={pendingPreviews}
                    diffTheme={diffTheme}
                />
                <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                    <Button onClick={onClose} variant="secondary" disabled={isSubmittingReview}>
                        Cancel
                    </Button>
                    <Button
                        onClick={onSubmit}
                        style={{ background: 'var(--success)' }}
                        loading={isSubmittingReview}
                    >
                        Submit
                    </Button>
                </div>
            </div>
        </Modal>
    );
}
