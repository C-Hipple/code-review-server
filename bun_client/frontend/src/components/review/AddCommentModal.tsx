import { Button, Modal, TextArea } from '../../design';

interface AddCommentModalProps {
    isOpen: boolean;
    filename: string;
    position: string;
    commentBody: string;
    isAddingComment: boolean;
    onChangeFilename: (v: string) => void;
    onChangePosition: (v: string) => void;
    onChangeCommentBody: (v: string) => void;
    onClose: () => void;
    onSubmit: () => void;
}

// Modal for adding a general comment by filename + diff position.
export default function AddCommentModal({
    isOpen,
    filename,
    position,
    commentBody,
    isAddingComment,
    onChangeFilename,
    onChangePosition,
    onChangeCommentBody,
    onClose,
    onSubmit,
}: AddCommentModalProps) {
    return (
        <Modal isOpen={isOpen} onClose={onClose} title="Add Comment" size="sm">
            <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                <input
                    placeholder="Filename"
                    value={filename}
                    onChange={e => onChangeFilename(e.target.value)}
                    disabled={isAddingComment}
                    style={{
                        padding: '8px',
                        background: 'var(--bg-primary)',
                        border: '1px solid var(--border)',
                        color: 'var(--text-primary)',
                        borderRadius: '4px',
                    }}
                />
                <input
                    type="number"
                    placeholder="Line Position in Diff"
                    value={position}
                    onChange={e => onChangePosition(e.target.value)}
                    disabled={isAddingComment}
                    style={{
                        padding: '8px',
                        background: 'var(--bg-primary)',
                        border: '1px solid var(--border)',
                        color: 'var(--text-primary)',
                        borderRadius: '4px',
                    }}
                />
                <TextArea
                    placeholder="Comment"
                    value={commentBody}
                    onChange={e => onChangeCommentBody(e.target.value)}
                    rows={5}
                    disabled={isAddingComment}
                />
                <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                    <Button onClick={onClose} variant="secondary" disabled={isAddingComment}>
                        Cancel
                    </Button>
                    <Button onClick={onSubmit} loading={isAddingComment}>
                        Add
                    </Button>
                </div>
            </div>
        </Modal>
    );
}
