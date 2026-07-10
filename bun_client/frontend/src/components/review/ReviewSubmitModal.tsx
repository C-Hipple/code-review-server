import { Button, Modal, TextArea, colors } from '../../design';

interface ReviewSubmitModalProps {
    isOpen: boolean;
    reviewEvent: string;
    reviewBody: string;
    isSubmittingReview: boolean;
    onChangeReviewEvent: (event: string) => void;
    onChangeReviewBody: (body: string) => void;
    onClose: () => void;
    onSubmit: () => void;
}

// Modal for submitting a review: pick Comment / Approve / Request Changes and
// an optional body.
export default function ReviewSubmitModal({
    isOpen,
    reviewEvent,
    reviewBody,
    isSubmittingReview,
    onChangeReviewEvent,
    onChangeReviewBody,
    onClose,
    onSubmit,
}: ReviewSubmitModalProps) {
    return (
        <Modal isOpen={isOpen} onClose={onClose} title="Submit Review" size="sm">
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
