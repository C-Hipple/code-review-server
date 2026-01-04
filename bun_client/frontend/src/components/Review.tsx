import { useState, useEffect } from 'react';
import { rpcCall } from '../api';

interface ReviewProps {
    owner: string;
    repo: string;
    number: number;
}

interface Comment {
    id: string;
    author: string;
    body: string;
    path: string;
    position: string;
    in_reply_to: number;
    created_at: string;
    outdated: boolean;
}

interface PluginResult {
    result: string;
    status: string;
}

interface PRResponse {
    content: string;
    diff: string;
    comments: Comment[];
    metadata: any;
}

export default function Review({ owner, repo, number }: ReviewProps) {
    const [content, setContent] = useState<string>('');
    const [diff, setDiff] = useState<string>('');
    const [comments, setComments] = useState<Comment[]>([]);
    const [pluginOutputs, setPluginOutputs] = useState<Record<string, PluginResult>>({});
    const [loading, setLoading] = useState(false);

    // UI State
    const [showCommentModal, setShowCommentModal] = useState(false); // For general comments
    const [activeLineIndex, setActiveLineIndex] = useState<number | null>(null); // For inline comments
    const [showPlugins, setShowPlugins] = useState(false);

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
        loadPluginOutputs();
    }, [owner, repo, number]);

    useEffect(() => {
        const handleKeyDown = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                setShowPlugins(false);
                setShowCommentModal(false);
                setSubmitting(false);
                setActiveLineIndex(null);
            }
        };
        window.addEventListener('keydown', handleKeyDown);
        return () => window.removeEventListener('keydown', handleKeyDown);
    }, []);

    const loadPluginOutputs = async () => {
        try {
            const res = await rpcCall<{ output: Record<string, PluginResult> }>('RPCHandler.GetPluginOutput', [{
                Owner: owner,
                Repo: repo,
                Number: number
            }]);
            setPluginOutputs(res.output || {});
        } catch (e) {
            console.error("Failed to load plugin outputs:", e);
        }
    };

    const loadPR = async () => {
        setLoading(true);
        try {
            const res = await rpcCall<PRResponse>('RPCHandler.GetPR', [{
                Owner: owner,
                Repo: repo,
                Number: number
            }]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
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
            const res = await rpcCall<PRResponse>('RPCHandler.SyncPR', [{
                Owner: owner,
                Repo: repo,
                Number: number
            }]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            loadPluginOutputs();
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
        if (!filename || !commentBody) return;
        try {
            const res = await rpcCall<PRResponse>('RPCHandler.AddComment', [{
                Owner: owner,
                Repo: repo,
                Number: number,
                Filename: filename,
                Position: parseInt(position, 10) || 0,
                Body: commentBody
            }]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            resetCommentForm();
        } catch (e) {
            console.error(e);
            alert('Error adding comment');
        }
    };

    const handleSubmitReview = async () => {
        try {
            const res = await rpcCall<PRResponse>('RPCHandler.SubmitReview', [{
                Owner: owner,
                Repo: repo,
                Number: number,
                Event: reviewEvent,
                Body: reviewBody
            }]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            setSubmitting(false);
        } catch (e) {
            console.error(e);
            alert('Error submitting review');
        }
    };

    // Parse content to identify clickable lines
    const parseContent = () => {
        const parsedLines: { text: string, file: string | null, pos: number | null, clickable: boolean }[] = [];

        // 1. Add Preamble from content (header, description, conversation)
        if (content) {
            const preambleMatch = content.match(/^(?:.*?\n)*?Files changed \(.*?\)\n\n/);
            const preamble = preambleMatch ? preambleMatch[0] : content;
            const lines = preamble.split('\n');
            for (const line of lines) {
                parsedLines.push({ text: line, file: null, pos: null, clickable: false });
            }
        }

        // 2. Add Diff lines and track positions for comments
        if (diff) {
            const lines = diff.split('\n');
            let currentFile: string | null = null;
            let currentPos = 0;
            let foundFirstHunkInFile = false;

            for (const rawLine of lines) {
                const line = rawLine.replace(/\r$/, '');
                let clickable = false;
                let pos: number | null = null;
                let file: string | null = null;

                // Match standard git diff headers or simplified format
                // Look for '+++ b/filename' as the primary source of the new filename
                const fileMatch = line.match(/^\+\+\+\s+b\/(.+)$/) ||
                    line.match(/^\s*(modified|deleted|new file|renamed)\s+(.+)$/);

                if (fileMatch) {
                    currentFile = (fileMatch[1] || fileMatch[2]).trim();
                    currentPos = 0;
                    foundFirstHunkInFile = false;

                    // Allow comments on the file header itself (general file comments)
                    pos = 0;
                    file = currentFile;
                    clickable = true;
                } else if (currentFile) {
                    const isHunkHeader = line.startsWith('@@');
                    const isSourceLine = line.length > 0 && ['+', '-', ' '].includes(line[0]) && !line.startsWith('---') && !line.startsWith('+++');

                    if (isHunkHeader || isSourceLine) {
                        currentPos++;
                        if (!foundFirstHunkInFile && isHunkHeader) {
                            foundFirstHunkInFile = true;
                            // GitHub position 1 is the first @@ line
                        }
                        pos = currentPos;
                        file = currentFile;
                        clickable = true;
                    }
                }
                parsedLines.push({ text: line, file, pos, clickable });
            }
        }

        return parsedLines;
    };

    const handleLineClick = (idx: number, file: string, pos: number) => {
        if (activeLineIndex === idx) {
            setActiveLineIndex(null);
            setFilename('');
            setPosition('');
        } else {
            setFilename(file);
            setPosition(pos.toString());
            setActiveLineIndex(idx);
            setShowCommentModal(false);
            setCommentBody('');
        }
    };

    const renderContent = () => {
        const lines = parseContent();
        if (lines.length === 0) return null;

        return lines.map((item, idx) => {
            const line = item.text;
            let style: React.CSSProperties = { color: 'var(--text-primary)', padding: '0 5px', whiteSpace: 'pre' };

            if (line.startsWith('+') && !line.startsWith('+++')) style = { ...style, color: 'var(--success)', background: 'rgba(35, 134, 54, 0.15)' };
            else if (line.startsWith('-') && !line.startsWith('---')) style = { ...style, color: 'var(--danger)', background: 'rgba(218, 54, 51, 0.15)' };
            else if (line.startsWith('@@')) style = { ...style, color: 'var(--accent)' };

            if (item.clickable) {
                style = { ...style, cursor: 'pointer' };
            }

            const isInlineActive = activeLineIndex === idx;
            const lineComments = item.file ? comments.filter(c => c.path === item.file && (c.position === item.pos?.toString() || (c.position === "" && item.pos === 0))) : [];
            const rootComments = lineComments.filter(c => !c.in_reply_to);

            return (
                <div key={idx}>
                    <div
                        style={{ ...style, borderLeft: isInlineActive ? '3px solid var(--accent)' : '3px solid transparent' }}
                        onClick={() => item.clickable && item.file && item.pos !== null && handleLineClick(idx, item.file, item.pos)}
                        className={item.clickable ? 'hover-line' : ''}
                        title={item.clickable ? `Add comment to ${item.file}:${item.pos}` : undefined}
                    >
                        {line}
                    </div>
                    {rootComments.map(rc => {
                        const thread = [rc, ...lineComments.filter(c => c.in_reply_to === parseInt(rc.id, 10))];
                        return (
                            <div key={rc.id} style={{
                                margin: '10px 20px',
                                border: '1px solid var(--border)',
                                borderRadius: '6px',
                                background: 'var(--bg-primary)',
                                overflow: 'hidden'
                            }}>
                                <div style={{ background: 'var(--bg-secondary)', padding: '5px 10px', fontSize: '11px', borderBottom: '1px solid var(--border)', color: 'var(--text-secondary)', display: 'flex', justifyContent: 'space-between' }}>
                                    <span>{rc.author} commented</span>
                                    <span>ID: {rc.id}</span>
                                </div>
                                {thread.map(c => (
                                    <div key={c.id} style={{ padding: '10px', borderBottom: c.id !== thread[thread.length - 1].id ? '1px solid var(--border)' : 'none' }}>
                                        {c !== rc && <div style={{ fontSize: '11px', color: 'var(--accent)', marginBottom: '5px' }}>Reply by {c.author}:</div>}
                                        <div style={{ whiteSpace: 'pre-wrap' }}>{c.body}</div>
                                    </div>
                                ))}
                            </div>
                        );
                    })}
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
                <button
                    onClick={() => setShowPlugins(!showPlugins)}
                    style={{ ...btnStyle, background: showPlugins ? 'var(--accent)' : 'transparent', border: '1px solid var(--border)', color: showPlugins ? 'white' : 'var(--text-primary)' }}
                >
                    Plugins {Object.keys(pluginOutputs).length > 0 ? `(${Object.keys(pluginOutputs).length})` : ''}
                </button>

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
            {showPlugins && (
                <div style={{
                    position: 'fixed' as const,
                    top: 0,
                    left: 0,
                    right: 0,
                    bottom: 0,
                    background: 'rgba(0,0,0,0.2)',
                    display: 'flex',
                    justifyContent: 'flex-end',
                    zIndex: 100,
                }} onClick={() => setShowPlugins(false)}>
                    <div style={{
                        background: 'var(--bg-secondary)',
                        width: '500px',
                        maxWidth: '80vw',
                        height: '100vh',
                        borderLeft: '1px solid var(--border)',
                        boxShadow: '-5px 0 20px rgba(0,0,0,0.3)',
                        display: 'flex',
                        flexDirection: 'column',
                        overflowY: 'auto',
                        padding: '20px',
                    }} onClick={e => e.stopPropagation()}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px', position: 'sticky', top: 0, background: 'var(--bg-secondary)', zIndex: 1, paddingBottom: '10px', borderBottom: '1px solid var(--border)' }}>
                            <h2 style={{ fontSize: '18px', margin: 0, color: 'var(--accent)' }}>Plugin Analysis</h2>
                            <button onClick={() => setShowPlugins(false)} style={btnSecondaryStyle}>Close (Esc)</button>
                        </div>
                        <div style={{ flex: 1 }}>
                            {Object.keys(pluginOutputs).length === 0 ? (
                                <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic', padding: '20px', textAlign: 'center' }}>No plugin output found.</div>
                            ) : (
                                <div style={{ display: 'flex', flexDirection: 'column', gap: '20px' }}>
                                    {Object.entries(pluginOutputs).map(([name, data]) => (
                                        <div key={name} style={{ background: 'var(--bg-primary)', border: '1px solid var(--border)', borderRadius: '8px', overflow: 'hidden' }}>
                                            <div style={{
                                                padding: '10px 15px',
                                                background: 'var(--bg-secondary)',
                                                borderBottom: '1px solid var(--border)',
                                                display: 'flex',
                                                justifyContent: 'space-between',
                                                alignItems: 'center'
                                            }}>
                                                <span style={{ fontWeight: 600, fontSize: '14px' }}>{name}</span>
                                                <span style={{
                                                    fontSize: '11px',
                                                    padding: '2px 10px',
                                                    borderRadius: '12px',
                                                    fontWeight: 600,
                                                    background: data.status === 'success' ? 'rgba(35, 134, 54, 0.2)' : data.status === 'pending' ? 'rgba(158, 106, 3, 0.2)' : 'rgba(218, 54, 51, 0.2)',
                                                    color: data.status === 'success' ? '#3fb950' : data.status === 'pending' ? '#d29922' : '#f85149',
                                                    border: `1px solid ${data.status === 'success' ? 'rgba(63, 185, 80, 0.3)' : data.status === 'pending' ? 'rgba(210, 153, 34, 0.3)' : 'rgba(248, 81, 73, 0.3)'}`
                                                }}>
                                                    {data.status.toUpperCase()}
                                                </span>
                                            </div>
                                            <div style={{ padding: '15px', whiteSpace: 'pre-wrap', fontSize: '13px', lineHeight: '1.5', color: 'var(--text-primary)', background: 'var(--bg-primary)' }}>
                                                {data.result || <span style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>No output produced.</span>}
                                            </div>
                                        </div>
                                    ))}
                                </div>
                            )}
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
