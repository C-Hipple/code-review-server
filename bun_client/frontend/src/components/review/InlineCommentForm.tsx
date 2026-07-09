import type { ReactNode } from 'react';
import { Button } from '../../design';

interface InlineCommentFormProps {
    // Context label parts
    file: string | null;
    pos: number | null;
    editingCommentId: number | null;
    replyToId: number | null;
    // Form state
    commentBody: string;
    editingCommentBody: string;
    isAddingComment: boolean;
    // Pin the editor to the visible left edge while the code scrolls horizontally on mobile.
    stickyOnMobile?: boolean;
    // Optional content rendered between the label and the textarea (e.g. an LSP popover).
    children?: ReactNode;
    onChangeCommentBody: (body: string) => void;
    onChangeEditingCommentBody: (body: string) => void;
    onCancel: () => void;
    onSubmit: () => void;
}

// The inline comment editor shown under a diff line or file header when the
// user is adding, replying to, or editing a comment.
export default function InlineCommentForm({
    file,
    pos,
    editingCommentId,
    replyToId,
    commentBody,
    editingCommentBody,
    isAddingComment,
    stickyOnMobile,
    children,
    onChangeCommentBody,
    onChangeEditingCommentBody,
    onCancel,
    onSubmit,
}: InlineCommentFormProps) {
    const isEditing = editingCommentId !== null;

    return (
        <div
            style={{
                padding: '10px 20px',
                background: 'var(--bg-primary)',
                borderBottom: '1px solid var(--border)',
                borderTop: '1px solid var(--border)',
                marginBottom: '10px',
                // Keep the comment editor in view while the code scrolls
                // horizontally on mobile.
                ...(stickyOnMobile
                    ? {
                          position: 'sticky',
                          left: 0,
                          maxWidth: 'calc(100vw - 16px)',
                      }
                    : {}),
            }}
        >
            <div
                style={{
                    marginBottom: '5px',
                    fontSize: '12px',
                    color: 'var(--text-secondary)',
                }}
            >
                {isEditing
                    ? `Editing local comment #${editingCommentId}`
                    : replyToId !== null
                      ? `Replying to comment #${replyToId}`
                      : `Commenting on ${file}:${pos}`}
            </div>
            {children}
            <textarea
                autoFocus
                placeholder={
                    isEditing
                        ? 'Edit your comment...'
                        : replyToId !== null
                          ? 'Write a reply...'
                          : 'Write a comment...'
                }
                value={isEditing ? editingCommentBody : commentBody}
                onChange={e =>
                    isEditing
                        ? onChangeEditingCommentBody(e.target.value)
                        : onChangeCommentBody(e.target.value)
                }
                disabled={isAddingComment}
                style={{
                    width: '100%',
                    padding: '8px',
                    marginBottom: '10px',
                    background: 'var(--bg-primary)',
                    border: '1px solid var(--border)',
                    color: 'var(--text-primary)',
                    borderRadius: '4px',
                    height: '80px',
                    fontFamily: 'inherit',
                    resize: 'vertical',
                }}
            />
            <div
                style={{
                    display: 'flex',
                    gap: '12px',
                    justifyContent: 'flex-end',
                }}
            >
                <Button onClick={onCancel} variant="secondary" size="sm" disabled={isAddingComment}>
                    Cancel
                </Button>
                <Button onClick={onSubmit} size="sm" loading={isAddingComment}>
                    {isEditing ? 'Save Edit' : replyToId !== null ? 'Reply' : 'Add Comment'}
                </Button>
            </div>
        </div>
    );
}
