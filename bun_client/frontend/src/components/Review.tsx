import { useState, useEffect } from 'react';
import { rpcCall } from '../api';

interface ReviewProps {
    owner: string;
    repo: string;
    number: number;
}

export default function Review({ owner, repo, number }: ReviewProps) {
    const [content, setContent] = useState<string>('');
    const [loading, setLoading] = useState(false);

    // UI State
    const [showCommentModal, setShowCommentModal] = useState(false); // For general comments
    const [activeLineIndex, setActiveLineIndex] = useState<number | null>(null); // For inline comments

    const [submitting, setSubmitting] = useState(false);

    // Comment form data
    const [filename, setFilename] = useState('');
    const [position, setPosition] = useState('');
    const [commentBody, setCommentBody] = useState('');

    // Submit form
    const [reviewBody, setReviewBody] = useState('');
    const [reviewEvent, setReviewEvent] = useState('COMMENT');

    useEffect(() => {
        loadPR();
    }, [owner, repo, number]);

    const loadPR = async () => {
        setLoading(true);
        try {
            const res = await rpcCall<{ Content: string }>('RPCHandler.GetPR', [{
                Owner: owner,
                Repo: repo,
                Number: number
            }]);
            setContent(res.Content);
        } catch (e) {
            console.error(e);
            setContent('Error loading PR.');
        } finally {
            setLoading(false);
        }
    };

    const handleSync = async () => {
        setLoading(true);
        try {
            const res = await rpcCall<{ Content: string }>('RPCHandler.SyncPR', [{
                Owner: owner,
                Repo: repo,
                Number: number
            }]);
            setContent(res.Content);
        } catch (e) {
            console.error(e);
        } finally {
            setLoading(false);
        }
    };

    const resetCommentForm = () => {
        setFilename('');
        setPosition('');
        setCommentBody('');
        setShowCommentModal(false);
        setActiveLineIndex(null);
    };

    const handleAddComment = async () => {
        if (!filename || !position || !commentBody) return;
        try {
            const res = await rpcCall<{ Content: string }>('RPCHandler.AddComment', [{
                Owner: owner,
                Repo: repo,
                Number: number,
                Filename: filename,
                Position: parseInt(position, 10),
                Body: commentBody
            }]);
            setContent(res.Content);
            resetCommentForm();
        } catch (e) {
            console.error(e);
            alert('Error adding comment');
        }
    };

    const handleSubmitReview = async () => {
        try {
            const res = await rpcCall<{ Content: string }>('RPCHandler.SubmitReview', [{
                Owner: owner,
                Repo: repo,
                Number: number,
                Event: reviewEvent,
                Body: reviewBody
            }]);
            setContent(res.Content);
            setSubmitting(false);
        } catch (e) {
            console.error(e);
            alert('Error submitting review');
        }
    };

    // Parse content to identify clickable lines
    const parseContent = () => {
        if (!content) return [];

        const lines = content.split('\n');
        const parsedLines: { text: string, file: string | null, pos: number | null, clickable: boolean }[] = [];

        let currentFile: string | null = null;
        let currentPos = 0;
        let inComment = false;
        let foundFirstHunkInFile = false;

        console.log("Parsing content, total lines:", lines.length);

        for (const rawLine of lines) {
            const line = rawLine.replace(/\r$/, '');
            let clickable = false;
            let pos: number | null = null;
            let file: string | null = null;

            // Detect comment blocks
            if (line.match(/^\s*┌─/)) {
                inComment = true;
            }

            if (!inComment) {
                // Improved file header detection
                const fileMatch = line.match(/^\s*(modified|deleted|new file|renamed)\s+(.+)$/);
                if (fileMatch) {
                    const matchedFile = fileMatch[2].trim();
                    currentFile = matchedFile;
                    currentPos = 0;
                    foundFirstHunkInFile = false;
                    console.log("Detected file header:", matchedFile);
                } else if (currentFile) {
                    // Check if it's a diff line
                    const isHunkHeader = line.startsWith('@@');
                    const isSourceLine = line.length > 0 && ['+', '-', ' '].includes(line[0]);

                    if (isHunkHeader) {
                        if (!foundFirstHunkInFile) {
                            currentPos = 0;
                            foundFirstHunkInFile = true;
                        } else {
                            currentPos++;
                        }
                        // Hunk header itself is a clickable position 
                        pos = currentPos;
                        file = currentFile;
                        clickable = true;
                    } else if (isSourceLine) {
                        currentPos++;
                        pos = currentPos;
                        file = currentFile;
                        clickable = true;
                    }
                }
            }

            // End of comment block
            if (line.match(/^\s*└─/)) {
                inComment = false;
            }

            parsedLines.push({ text: line, file, pos, clickable });
        }
        return parsedLines;
    };

    const handleLineClick = (idx: number, file: string, pos: number) => {
        if (activeLineIndex === idx) {
            // Toggle off if clicking same line
            setActiveLineIndex(null);
            setFilename('');
            setPosition('');
        } else {
            setFilename(file);
            setPosition(pos.toString());
            setActiveLineIndex(idx);
            setShowCommentModal(false); // Ensure modal is closed
            setCommentBody(''); // Clear previous comment body
        }
    };

    const renderContent = () => {
        const lines = parseContent();
        if (lines.length === 0) return null;

        return lines.map((item, idx) => {
            const line = item.text;
            let style: React.CSSProperties = { color: 'var(--text-primary)', padding: '0 5px', whiteSpace: 'pre' };

            // Stylize based on content
            if (line.startsWith('+') && !line.startsWith('+++')) style = { ...style, color: 'var(--success)', background: 'rgba(35, 134, 54, 0.15)' };
            else if (line.startsWith('-') && !line.startsWith('---')) style = { ...style, color: 'var(--danger)', background: 'rgba(218, 54, 51, 0.15)' };
            else if (line.startsWith('@@')) style = { ...style, color: 'var(--accent)' };
            else if (line.indexOf('REVIEW COMMENT') !== -1) style = { ...style, color: 'var(--warning)' };

            if (item.clickable) {
                style = { ...style, cursor: 'pointer' };
            }

            const isInlineActive = activeLineIndex === idx;

            return (
                <div key={idx}>
                    <div
                        style={{ ...style, borderLeft: isInlineActive ? '3px solid var(--accent)' : '3px solid transparent' }}
                        onClick={() => item.clickable && item.file && item.pos && handleLineClick(idx, item.file, item.pos)}
                        className={item.clickable ? 'hover-line' : ''}
                        title={item.clickable ? `Add comment to ${item.file}:${item.pos}` : undefined}
                    >
                        {line}
                    </div>
                    {isInlineActive && (
                        <div style={{
                            padding: '10px 20px',
                            background: 'var(--bg-primary)',
                            borderBottom: '1px solid var(--border)',
                            borderTop: '1px solid var(--border)',
                            marginBottom: '10px'
                        }}>
                            <div style={{ marginBottom: '5px', fontSize: '12px', color: 'var(--text-secondary)' }}>
                                Commenting on {item.file}:{item.pos}
                            </div>
                            <textarea
                                autoFocus
                                placeholder="Write a comment..."
                                value={commentBody}
                                onChange={e => setCommentBody(e.target.value)}
                                style={{ ...inputStyle, height: '80px', marginBottom: '10px' }}
                            />
                            <div style={{ display: 'flex', gap: '10px', justifyContent: 'flex-end' }}>
                                <button onClick={() => setActiveLineIndex(null)} style={btnSecondaryStyle}>Cancel</button>
                                <button onClick={handleAddComment} style={btnStyle}>Add Comment</button>
                            </div>
                        </div>
                    )}
                </div>
            );
        });
    };

    if (loading && !content) return <div style={{ padding: '20px' }}>Loading PR...</div>;

    return (
        <div className="review-container">
            <div className="toolbar" style={{
                display: 'flex',
                gap: '10px',
                marginBottom: '20px',
                padding: '10px',
                background: 'var(--bg-secondary)',
                borderRadius: '8px',
                border: '1px solid var(--border)',
                position: 'sticky',
                top: '10px',
                zIndex: 10
            }}>
                <button onClick={handleSync} style={btnStyle}>Sync</button>
                <button onClick={() => {
                    setFilename('');
                    setPosition('');
                    setShowCommentModal(true);
                }} style={btnStyle}>Add General Comment</button>
                <button onClick={() => setSubmitting(true)} style={{ ...btnStyle, background: 'var(--success)' }}>Submit Review</button>

                <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', color: 'var(--text-secondary)', fontSize: '13px' }}>
                    💡 Click on any line of code to add a comment
                </span>
            </div>

            <div className="content" style={{
                background: 'var(--bg-secondary)',
                padding: '20px',
                borderRadius: '8px',
                fontFamily: 'var(--font-mono)',
                fontSize: '13px',
                overflowX: 'auto',
                border: '1px solid var(--border)'
            }}>
                {renderContent()}
            </div>

            {showCommentModal && (
                <div style={modalOverlayStyle}>
                    <div style={modalStyle}>
                        <h3>Add Comment</h3>
                        <input
                            placeholder="Filename"
                            value={filename}
                            onChange={e => setFilename(e.target.value)}
                            style={inputStyle}
                        />
                        <input
                            type="number"
                            placeholder="Line Position in Diff"
                            value={position}
                            onChange={e => setPosition(e.target.value)}
                            style={inputStyle}
                        />
                        <textarea
                            placeholder="Comment"
                            value={commentBody}
                            onChange={e => setCommentBody(e.target.value)}
                            style={{ ...inputStyle, height: '100px' }}
                        />
                        <div style={{ display: 'flex', gap: '10px', justifyContent: 'flex-end', marginTop: '10px' }}>
                            <button onClick={resetCommentForm} style={btnSecondaryStyle}>Cancel</button>
                            <button onClick={handleAddComment} style={btnStyle}>Add</button>
                        </div>
                    </div>
                </div>
            )}

            {submitting && (
                <div style={modalOverlayStyle}>
                    <div style={modalStyle}>
                        <h3>Submit Review</h3>
                        <select
                            value={reviewEvent}
                            onChange={e => setReviewEvent(e.target.value)}
                            style={{ ...inputStyle, marginBottom: '10px' }}
                        >
                            <option value="COMMENT">Comment</option>
                            <option value="APPROVE">Approve</option>
                            <option value="REQUEST_CHANGES">Request Changes</option>
                        </select>
                        <textarea
                            placeholder="Review Body (Optional)"
                            value={reviewBody}
                            onChange={e => setReviewBody(e.target.value)}
                            style={{ ...inputStyle, height: '100px' }}
                        />
                        <div style={{ display: 'flex', gap: '10px', justifyContent: 'flex-end', marginTop: '10px' }}>
                            <button onClick={() => setSubmitting(false)} style={btnSecondaryStyle}>Cancel</button>
                            <button onClick={handleSubmitReview} style={{ ...btnStyle, background: 'var(--success)' }}>Submit</button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
}

const btnStyle = {
    background: 'var(--accent)',
    color: 'white',
    border: 'none',
    padding: '8px 16px',
    borderRadius: '6px',
    fontWeight: 500,
    cursor: 'pointer',
};

const btnSecondaryStyle = {
    background: 'transparent',
    color: 'var(--text-secondary)',
    border: '1px solid var(--border)',
    padding: '8px 16px',
    borderRadius: '6px',
    cursor: 'pointer',
};

const inputStyle = {
    display: 'block',
    width: '100%',
    padding: '8px',
    marginBottom: '10px',
    background: 'var(--bg-primary)',
    border: '1px solid var(--border)',
    color: 'var(--text-primary)',
    borderRadius: '4px',
    boxSizing: 'border-box' as const,
};

const modalOverlayStyle = {
    position: 'fixed' as const,
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    background: 'rgba(0,0,0,0.7)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    zIndex: 100,
};

const modalStyle = {
    background: 'var(--bg-secondary)',
    padding: '20px',
    borderRadius: '8px',
    width: '400px',
    border: '1px solid var(--border)',
};
