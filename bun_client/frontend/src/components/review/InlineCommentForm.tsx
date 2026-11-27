import type { ReactNode } from 'react';
import { Button } from '../../design';

interface InlineCommentFormProps {
    // Context label parts
    file: string | null;
    pos: number | null;
    // Form state
    commentBody: string;
    isAddingComment: boolean;
    // Pin the editor to the visible left edge while the code scrolls horizontally on mobile.
    stickyOnMobile?: boolean;
    // Optional content rendered between the label and the textarea (e.g. an LSP popover).
    children?: ReactNode;
    onChangeCommentBody: (body: string) => void;
    onCancel: () => void;
    onSubmit: () => void;
}

// The inline comment editor shown under a diff line or file header when the
// user starts a comment there. Replies and edits are written in the comment
// tray (CommentTray), beside the thread they belong to.
export default function InlineCommentForm({
    file,
    pos,
    commentBody,
    isAddingComment,
    stickyOnMobile,
    children,
    onChangeCommentBody,
    onCancel,
    onSubmit,
}: InlineCommentFormProps) {
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
                {`Commenting on ${file}:${pos}`}
            </div>
            {children}
            <textarea
                autoFocus
                placeholder="Write a comment..."
                value={commentBody}
                onChange={e => onChangeCommentBody(e.target.value)}
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
                    Add Comment
                </Button>
            </div>
        </div>
    );
}
