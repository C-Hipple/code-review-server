import { useState, useEffect, useMemo } from 'react';
import Markdown from 'react-markdown';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism';
import { rpcCall } from '../api';

// Strip HTML comments from text (e.g., <!-- comment -->)
const stripHtmlComments = (text: string): string => {
    return text.replace(/<!--[\s\S]*?-->/g, '');
};

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

interface PRMetadata {
    number: number;
    title: string;
    author: string;
    base_ref: string;
    head_ref: string;
    state: string;
    milestone: string;
    labels: string[];
    assignees: string[];
    reviewers: string[];
    draft: boolean;
    ci_status: string;
    ci_failures: string[];
    description: string;
    url: string;
}

interface PRResponse {
    content: string;
    diff: string;
    comments: Comment[];
    metadata: PRMetadata;
}

// Map file extensions to Prism language identifiers
const getLanguageFromFilename = (filename: string): string => {
    const ext = filename.split('.').pop()?.toLowerCase() || '';
    const languageMap: Record<string, string> = {
        // JavaScript/TypeScript
        'js': 'javascript',
        'jsx': 'jsx',
        'ts': 'typescript',
        'tsx': 'tsx',
        'mjs': 'javascript',
        'cjs': 'javascript',
        // Web
        'html': 'html',
        'htm': 'html',
        'css': 'css',
        'scss': 'scss',
        'sass': 'sass',
        'less': 'less',
        'json': 'json',
        'xml': 'xml',
        'svg': 'xml',
        // Backend
        'py': 'python',
        'rb': 'ruby',
        'go': 'go',
        'rs': 'rust',
        'java': 'java',
        'kt': 'kotlin',
        'scala': 'scala',
        'php': 'php',
        'c': 'c',
        'h': 'c',
        'cpp': 'cpp',
        'cc': 'cpp',
        'cxx': 'cpp',
        'hpp': 'cpp',
        'cs': 'csharp',
        'swift': 'swift',
        // Shell/Config
        'sh': 'bash',
        'bash': 'bash',
        'zsh': 'bash',
        'fish': 'bash',
        'ps1': 'powershell',
        'yaml': 'yaml',
        'yml': 'yaml',
        'toml': 'toml',
        'ini': 'ini',
        'conf': 'ini',
        // Data/Docs
        'md': 'markdown',
        'mdx': 'markdown',
        'sql': 'sql',
        'graphql': 'graphql',
        'gql': 'graphql',
        // Other
        'dockerfile': 'docker',
        'makefile': 'makefile',
        'lua': 'lua',
        'r': 'r',
        'dart': 'dart',
        'ex': 'elixir',
        'exs': 'elixir',
        'erl': 'erlang',
        'hrl': 'erlang',
        'clj': 'clojure',
        'vim': 'vim',
        'el': 'lisp',
        'lisp': 'lisp',
        'hs': 'haskell',
        'ml': 'ocaml',
        'proto': 'protobuf',
    };
    
    // Handle special filenames
    const basename = filename.split('/').pop()?.toLowerCase() || '';
    if (basename === 'dockerfile') return 'docker';
    if (basename === 'makefile' || basename === 'gnumakefile') return 'makefile';
    if (basename.endsWith('.d.ts')) return 'typescript';
    
    return languageMap[ext] || 'text';
};

export default function Review({ owner, repo, number }: ReviewProps) {
    const [content, setContent] = useState<string>('');
    const [diff, setDiff] = useState<string>('');
    const [comments, setComments] = useState<Comment[]>([]);
    const [metadata, setMetadata] = useState<PRMetadata | null>(null);
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
            setMetadata(res.metadata || null);
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
            setMetadata(res.metadata || null);
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

    // Line types for rendering
    type LineType = 'code' | 'addition' | 'deletion' | 'hunk' | 'file-header' | 'skip';

    interface ParsedLine {
        text: string;
        file: string | null;
        pos: number | null;
        clickable: boolean;
        lineType: LineType;
        fileStatus?: 'modified' | 'new' | 'deleted' | 'renamed';
    }

    // Parse diff to identify clickable lines (metadata is now rendered separately)
    const parseDiff = (): ParsedLine[] => {
        const parsedLines: ParsedLine[] = [];

        // Add Diff lines and track positions for comments
        if (diff) {
            const lines = diff.split('\n');
            let currentFile: string | null = null;
            let currentPos = 0;
            let foundFirstHunkInFile = false;
            let pendingFileStatus: 'modified' | 'new' | 'deleted' | 'renamed' = 'modified';

            for (const rawLine of lines) {
                const line = rawLine.replace(/\r$/, '');
                let clickable = false;
                let pos: number | null = null;
                let file: string | null = null;
                let lineType: LineType = 'code';

                // Check for diff --git header to determine file status
                if (line.startsWith('diff --git')) {
                    // Check if next lines indicate new/deleted file
                    pendingFileStatus = 'modified';
                    lineType = 'skip';
                } else if (line.startsWith('new file mode')) {
                    pendingFileStatus = 'new';
                    lineType = 'skip';
                } else if (line.startsWith('deleted file mode')) {
                    pendingFileStatus = 'deleted';
                    lineType = 'skip';
                } else if (line.startsWith('rename from') || line.startsWith('rename to') || line.startsWith('similarity index')) {
                    pendingFileStatus = 'renamed';
                    lineType = 'skip';
                } else if (line.startsWith('index ') || line.startsWith('---')) {
                    // Skip these git metadata lines
                    lineType = 'skip';
                } else {
                    // Match +++ b/filename as the file header
                    const fileMatch = line.match(/^\+\+\+\s+b\/(.+)$/) ||
                        line.match(/^\+\+\+\s+(.+)$/) ||
                        line.match(/^\s*(modified|deleted|new file|renamed)\s+(.+)$/);

                    if (fileMatch) {
                        currentFile = (fileMatch[1] || fileMatch[2]).trim();
                        currentPos = 0;
                        foundFirstHunkInFile = false;

                        // Allow comments on the file header itself (general file comments)
                        pos = 0;
                        file = currentFile;
                        clickable = true;
                        lineType = 'file-header';

                        parsedLines.push({ 
                            text: currentFile, 
                            file, 
                            pos, 
                            clickable, 
                            lineType,
                            fileStatus: pendingFileStatus 
                        });
                        pendingFileStatus = 'modified'; // Reset for next file
                        continue;
                    } else if (currentFile) {
                        const isHunkHeader = line.startsWith('@@');
                        const isAddition = line.startsWith('+') && !line.startsWith('+++');
                        const isDeletion = line.startsWith('-') && !line.startsWith('---');
                        const isContextLine = line.length > 0 && line[0] === ' ';

                        if (isHunkHeader) {
                            // For the first hunk in a file, the backend resets diffPosCount to 0.
                            // Subsequent hunks consume a position but aren't valid comment targets.
                            if (!foundFirstHunkInFile) {
                                foundFirstHunkInFile = true;
                                // Don't increment for first hunk - mimics backend reset to 0
                            } else {
                                // Subsequent hunks consume a position in the diff
                                currentPos++;
                            }
                            pos = currentPos;
                            file = currentFile;
                            // Hunk headers are not valid GitHub comment positions
                            clickable = false;
                            lineType = 'hunk';
                        } else if (isAddition || isDeletion || isContextLine) {
                            currentPos++;
                            pos = currentPos;
                            file = currentFile;
                            clickable = true;
                            lineType = isAddition ? 'addition' : isDeletion ? 'deletion' : 'code';
                        }
                    }
                }

                if (lineType !== 'skip') {
                    parsedLines.push({ text: line, file, pos, clickable, lineType });
                }
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

    // Custom theme based on oneDark but adjusted for diff context
    const customDiffTheme = useMemo(() => ({
        ...oneDark,
        'pre[class*="language-"]': {
            ...oneDark['pre[class*="language-"]'],
            background: 'transparent',
            margin: 0,
            padding: 0,
            overflow: 'visible',
        },
        'code[class*="language-"]': {
            ...oneDark['code[class*="language-"]'],
            background: 'transparent',
        },
    }), []);

    // Pre-compute highlighted lines for each file section
    const highlightedLines = useMemo(() => {
        const lines = parseDiff();
        const result: Map<number, React.ReactNode> = new Map();
        
        // Group lines by file and collect code for bulk highlighting
        let currentFile: string | null = null;
        let fileLines: { idx: number; code: string; prefix: string }[] = [];
        
        const processFileSection = (file: string, linesData: typeof fileLines) => {
            if (linesData.length === 0) return;
            
            const language = getLanguageFromFilename(file);
            
            // We'll use SyntaxHighlighter's output directly for each line
            linesData.forEach((lineData) => {
                const lineCode = lineData.code || ' '; // Use space for empty lines
                result.set(lineData.idx, (
                    <SyntaxHighlighter
                        language={language}
                        style={customDiffTheme}
                        customStyle={{
                            background: 'transparent',
                            margin: 0,
                            padding: 0,
                            display: 'inline',
                            fontSize: 'inherit',
                            fontFamily: 'inherit',
                        }}
                        codeTagProps={{
                            style: {
                                background: 'transparent',
                                fontFamily: 'inherit',
                            }
                        }}
                        PreTag="span"
                        CodeTag="span"
                    >
                        {lineCode}
                    </SyntaxHighlighter>
                ));
            });
        };
        
        lines.forEach((item, idx) => {
            if (item.lineType === 'file-header') {
                // Process previous file's lines
                if (currentFile && fileLines.length > 0) {
                    processFileSection(currentFile, fileLines);
                }
                currentFile = item.text;
                fileLines = [];
            } else if (currentFile && (item.lineType === 'addition' || item.lineType === 'deletion' || item.lineType === 'code')) {
                // Extract the code without the +/- prefix
                const code = item.text.slice(1);
                fileLines.push({ idx, code, prefix: item.text[0] });
            }
        });
        
        // Process the last file section
        if (currentFile && fileLines.length > 0) {
            processFileSection(currentFile, fileLines);
        }
        
        return result;
    }, [diff, customDiffTheme]);

    // Get icon and color for file status
    const getFileStatusInfo = (status?: 'modified' | 'new' | 'deleted' | 'renamed') => {
        switch (status) {
            case 'new':
                return { icon: '+', label: 'new file', color: 'var(--success)', bg: 'rgba(35, 134, 54, 0.15)' };
            case 'deleted':
                return { icon: '−', label: 'deleted', color: 'var(--danger)', bg: 'rgba(218, 54, 51, 0.15)' };
            case 'renamed':
                return { icon: '→', label: 'renamed', color: 'var(--warning)', bg: 'rgba(210, 153, 34, 0.15)' };
            default:
                return { icon: '●', label: 'modified', color: 'var(--accent)', bg: 'rgba(56, 139, 253, 0.15)' };
        }
    };

    const renderDiff = () => {
        const lines = parseDiff();
        if (lines.length === 0) return null;

        return lines.map((item, idx) => {
            const line = item.text;
            const isAddition = item.lineType === 'addition';
            const isDeletion = item.lineType === 'deletion';
            const isHunkHeader = item.lineType === 'hunk';
            const isFileHeader = item.lineType === 'file-header';
            const isCodeLine = isAddition || isDeletion || item.lineType === 'code';
            
            // Render file header with clean styling
            if (isFileHeader) {
                const statusInfo = getFileStatusInfo(item.fileStatus);
                const isInlineActive = activeLineIndex === idx;
                const lineComments = item.file ? comments.filter(c => c.path === item.file && (c.position === item.pos?.toString() || (c.position === "" && item.pos === 0))) : [];
                const rootComments = lineComments.filter(c => !c.in_reply_to);

                return (
                    <div key={idx}>
                        <div
                            style={{
                                display: 'flex',
                                alignItems: 'center',
                                gap: '10px',
                                padding: '10px 12px',
                                background: 'linear-gradient(135deg, var(--bg-tertiary) 0%, var(--bg-secondary) 100%)',
                                borderTop: idx > 0 ? '2px solid var(--border)' : 'none',
                                borderBottom: '1px solid var(--border)',
                                marginTop: idx > 0 ? '8px' : '0',
                                cursor: 'pointer',
                                borderLeft: isInlineActive ? '3px solid var(--accent)' : '3px solid transparent',
                            }}
                            onClick={() => item.file && item.pos !== null && handleLineClick(idx, item.file, item.pos)}
                            className="hover-line"
                            title={`Add comment to ${item.file}`}
                        >
                            <span style={{
                                display: 'inline-flex',
                                alignItems: 'center',
                                justifyContent: 'center',
                                width: '22px',
                                height: '22px',
                                borderRadius: '4px',
                                background: statusInfo.bg,
                                color: statusInfo.color,
                                fontSize: '14px',
                                fontWeight: 600,
                            }}>
                                {statusInfo.icon}
                            </span>
                            <span style={{
                                fontSize: '11px',
                                fontWeight: 500,
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                                color: statusInfo.color,
                                minWidth: '60px',
                            }}>
                                {statusInfo.label}
                            </span>
                            <span style={{
                                fontFamily: 'var(--font-mono)',
                                fontSize: '13px',
                                fontWeight: 500,
                                color: 'var(--text-primary)',
                            }}>
                                {item.text}
                            </span>
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
            }

            let containerStyle: React.CSSProperties = { 
                display: 'flex',
                alignItems: 'stretch',
                minHeight: '20px',
            };
            
            let prefixStyle: React.CSSProperties = {
                width: '20px',
                minWidth: '20px',
                textAlign: 'center',
                userSelect: 'none',
                color: 'var(--text-tertiary)',
                borderRight: '1px solid var(--border)',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontSize: '12px',
            };
            
            let lineStyle: React.CSSProperties = { 
                flex: 1,
                padding: '0 8px', 
                whiteSpace: 'pre',
                display: 'flex',
                alignItems: 'center',
            };

            if (isAddition) {
                containerStyle = { ...containerStyle, background: 'rgba(35, 134, 54, 0.12)' };
                prefixStyle = { ...prefixStyle, color: 'var(--success)', background: 'rgba(35, 134, 54, 0.2)' };
            } else if (isDeletion) {
                containerStyle = { ...containerStyle, background: 'rgba(218, 54, 51, 0.12)' };
                prefixStyle = { ...prefixStyle, color: 'var(--danger)', background: 'rgba(218, 54, 51, 0.2)' };
            } else if (isHunkHeader) {
                containerStyle = { ...containerStyle, background: 'rgba(56, 139, 253, 0.1)' };
                lineStyle = { ...lineStyle, color: 'var(--accent)', fontStyle: 'italic', fontSize: '12px' };
            }

            if (item.clickable) {
                containerStyle = { ...containerStyle, cursor: 'pointer' };
            }

            const isInlineActive = activeLineIndex === idx;
            const lineComments = item.file ? comments.filter(c => c.path === item.file && (c.position === item.pos?.toString() || (c.position === "" && item.pos === 0))) : [];
            const rootComments = lineComments.filter(c => !c.in_reply_to);

            // Determine what to render for the line content
            let lineContent: React.ReactNode;
            const highlightedContent = highlightedLines.get(idx);
            
            if (highlightedContent && isCodeLine && !isHunkHeader) {
                // Use syntax-highlighted content
                lineContent = highlightedContent;
            } else {
                // Use plain text for headers and non-code lines
                lineContent = isCodeLine ? line.slice(1) : line;
            }

            return (
                <div key={idx}>
                    <div
                        style={{ 
                            ...containerStyle, 
                            borderLeft: isInlineActive ? '3px solid var(--accent)' : '3px solid transparent',
                            marginLeft: isInlineActive ? '-3px' : '0',
                        }}
                        onClick={() => item.clickable && item.file && item.pos !== null && handleLineClick(idx, item.file, item.pos)}
                        className={item.clickable ? 'hover-line' : ''}
                        title={item.clickable ? `Add comment to ${item.file}:${item.pos}` : undefined}
                    >
                        {isCodeLine && !isHunkHeader && (
                            <span style={prefixStyle}>
                                {isAddition ? '+' : isDeletion ? '-' : ''}
                            </span>
                        )}
                        <span style={lineStyle}>
                            {lineContent}
                        </span>
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

    const getCIStatusStyle = () => {
        if (!metadata?.ci_status) return {};
        const status = metadata.ci_status.toLowerCase();
        if (status === 'success' || status === 'passed') {
            return { background: 'rgba(35, 134, 54, 0.15)', color: '#3fb950', borderColor: 'rgba(63, 185, 80, 0.4)' };
        } else if (status === 'pending' || status === 'running') {
            return { background: 'rgba(158, 106, 3, 0.15)', color: '#d29922', borderColor: 'rgba(210, 153, 34, 0.4)' };
        } else if (status === 'failure' || status === 'failed') {
            return { background: 'rgba(218, 54, 51, 0.15)', color: '#f85149', borderColor: 'rgba(248, 81, 73, 0.4)' };
        }
        return { background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', borderColor: 'var(--border)' };
    };

    const getStateStyle = () => {
        if (!metadata?.state) return {};
        const state = metadata.state.toLowerCase();
        if (state === 'open') {
            return { background: 'rgba(35, 134, 54, 0.15)', color: '#3fb950' };
        } else if (state === 'closed') {
            return { background: 'rgba(218, 54, 51, 0.15)', color: '#f85149' };
        } else if (state === 'merged') {
            return { background: 'rgba(130, 80, 223, 0.15)', color: '#a371f7' };
        }
        return {};
    };

    return (
        <div className="review-container">
            {/* PR Header Section */}
            {metadata && (
                <div className="pr-header" style={{
                    background: 'linear-gradient(135deg, var(--bg-secondary) 0%, var(--bg-primary) 100%)',
                    borderRadius: '12px',
                    border: '1px solid var(--border)',
                    marginBottom: '16px',
                    overflow: 'hidden'
                }}>
                    {/* Title Bar */}
                    <div style={{
                        padding: '20px 24px',
                        borderBottom: '1px solid var(--border)',
                        background: 'var(--bg-secondary)'
                    }}>
                        <div style={{ display: 'flex', alignItems: 'flex-start', gap: '16px' }}>
                            <div style={{ flex: 1 }}>
                                <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '8px', flexWrap: 'wrap' }}>
                                    <span style={{
                                        ...getStateStyle(),
                                        padding: '4px 12px',
                                        borderRadius: '16px',
                                        fontSize: '12px',
                                        fontWeight: 600,
                                        textTransform: 'uppercase',
                                        letterSpacing: '0.5px'
                                    }}>
                                        {metadata.draft ? '📝 Draft' : metadata.state}
                                    </span>
                                    <span style={{ color: 'var(--text-secondary)', fontSize: '14px' }}>
                                        #{metadata.number}
                                    </span>
                                </div>
                                <h1 style={{
                                    margin: 0,
                                    fontSize: '22px',
                                    fontWeight: 600,
                                    color: 'var(--text-primary)',
                                    lineHeight: 1.3
                                }}>
                                    {metadata.title}
                                </h1>
                            </div>
                            <a
                                href={metadata.url}
                                target="_blank"
                                rel="noopener noreferrer"
                                style={{
                                    ...btnSecondaryStyle,
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '6px',
                                    textDecoration: 'none',
                                    fontSize: '13px'
                                }}
                            >
                                <span>↗</span> GitHub
                            </a>
                        </div>
                    </div>

                    {/* Info Grid */}
                    <div style={{
                        display: 'grid',
                        gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
                        gap: '1px',
                        background: 'var(--border)'
                    }}>
                        {/* Branch Info */}
                        <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                            <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                Branch
                            </div>
                            <div style={{ fontFamily: 'var(--font-mono)', fontSize: '13px', color: 'var(--accent)' }}>
                                <span style={{ color: 'var(--text-secondary)' }}>{metadata.base_ref}</span>
                                <span style={{ margin: '0 8px', color: 'var(--text-tertiary)' }}>←</span>
                                <span>{metadata.head_ref}</span>
                            </div>
                        </div>

                        {/* Author */}
                        <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                            <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                Author
                            </div>
                            <div style={{ fontSize: '14px', fontWeight: 500, color: 'var(--text-primary)' }}>
                                @{metadata.author}
                            </div>
                        </div>

                        {/* CI Status */}
                        {metadata.ci_status && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                    CI Status
                                </div>
                                <div style={{
                                    display: 'inline-flex',
                                    alignItems: 'center',
                                    gap: '6px',
                                    padding: '4px 10px',
                                    borderRadius: '6px',
                                    fontSize: '13px',
                                    fontWeight: 500,
                                    border: '1px solid',
                                    ...getCIStatusStyle()
                                }}>
                                    {metadata.ci_status.toLowerCase() === 'success' && '✓'}
                                    {metadata.ci_status.toLowerCase() === 'pending' && '○'}
                                    {metadata.ci_status.toLowerCase() === 'failure' && '✗'}
                                    {metadata.ci_status}
                                </div>
                            </div>
                        )}

                        {/* Reviewers */}
                        {metadata.reviewers && metadata.reviewers.length > 0 && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                    Reviewers
                                </div>
                                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                                    {metadata.reviewers.map((r, i) => (
                                        <span key={i} style={{
                                            background: 'var(--bg-tertiary)',
                                            padding: '2px 8px',
                                            borderRadius: '4px',
                                            fontSize: '12px',
                                            color: 'var(--text-primary)'
                                        }}>
                                            @{r}
                                        </span>
                                    ))}
                                </div>
                            </div>
                        )}
                    </div>

                    {/* Labels, Assignees, Milestone Row */}
                    {(metadata.labels?.length > 0 || metadata.assignees?.length > 0 || metadata.milestone) && (
                        <div style={{
                            padding: '14px 20px',
                            display: 'flex',
                            flexWrap: 'wrap',
                            gap: '20px',
                            borderTop: '1px solid var(--border)',
                            background: 'var(--bg-primary)'
                        }}>
                            {metadata.labels && metadata.labels.length > 0 && (
                                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <span style={{ fontSize: '11px', color: 'var(--text-secondary)', textTransform: 'uppercase' }}>Labels:</span>
                                    <div style={{ display: 'flex', gap: '6px', flexWrap: 'wrap' }}>
                                        {metadata.labels.map((label, i) => (
                                            <span key={i} style={{
                                                background: 'linear-gradient(135deg, rgba(130, 80, 223, 0.2), rgba(56, 139, 253, 0.2))',
                                                color: '#a371f7',
                                                padding: '2px 10px',
                                                borderRadius: '12px',
                                                fontSize: '11px',
                                                fontWeight: 500,
                                                border: '1px solid rgba(130, 80, 223, 0.3)'
                                            }}>
                                                {label}
                                            </span>
                                        ))}
                                    </div>
                                </div>
                            )}
                            {metadata.assignees && metadata.assignees.length > 0 && (
                                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <span style={{ fontSize: '11px', color: 'var(--text-secondary)', textTransform: 'uppercase' }}>Assignees:</span>
                                    <span style={{ fontSize: '13px', color: 'var(--text-primary)' }}>
                                        {metadata.assignees.map(a => `@${a}`).join(', ')}
                                    </span>
                                </div>
                            )}
                            {metadata.milestone && (
                                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <span style={{ fontSize: '11px', color: 'var(--text-secondary)', textTransform: 'uppercase' }}>Milestone:</span>
                                    <span style={{ fontSize: '13px', color: 'var(--accent)' }}>{metadata.milestone}</span>
                                </div>
                            )}
                        </div>
                    )}

                    {/* Description */}
                    {metadata.description && (
                        <div style={{
                            padding: '16px 20px',
                            borderTop: '1px solid var(--border)',
                            background: 'var(--bg-primary)'
                        }}>
                            <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '10px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                Description
                            </div>
                            <div
                                className="pr-description"
                                style={{
                                    fontSize: '14px',
                                    lineHeight: 1.6,
                                    color: 'var(--text-primary)',
                                    maxHeight: '300px',
                                    overflow: 'auto'
                                }}
                            >
                                <Markdown>{stripHtmlComments(metadata.description)}</Markdown>
                            </div>
                        </div>
                    )}

                    {/* CI Failures */}
                    {metadata.ci_failures && metadata.ci_failures.length > 0 && (
                        <div style={{
                            padding: '14px 20px',
                            borderTop: '1px solid var(--border)',
                            background: 'rgba(218, 54, 51, 0.05)'
                        }}>
                            <div style={{ fontSize: '11px', color: '#f85149', marginBottom: '8px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                ✗ CI Failures
                            </div>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
                                {metadata.ci_failures.map((failure, i) => (
                                    <span key={i} style={{
                                        fontFamily: 'var(--font-mono)',
                                        fontSize: '12px',
                                        color: '#f85149'
                                    }}>
                                        • {failure}
                                    </span>
                                ))}
                            </div>
                        </div>
                    )}
                </div>
            )}

            {/* Toolbar */}
            <div className="toolbar" style={{
                display: 'flex',
                gap: '10px',
                marginBottom: '16px',
                padding: '12px 16px',
                background: 'var(--bg-secondary)',
                borderRadius: '8px',
                border: '1px solid var(--border)',
                position: 'sticky',
                top: '10px',
                zIndex: 10
            }}>
                <button onClick={handleSync} style={btnStyle}>
                    {loading ? '↻ Syncing...' : '↻ Sync'}
                </button>
                <button onClick={() => {
                    setFilename('');
                    setPosition('');
                    setShowCommentModal(true);
                }} style={btnStyle}>+ Comment</button>
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

            {/* Diff Section */}
            <div className="diff-section" style={{
                background: 'var(--bg-secondary)',
                borderRadius: '8px',
                border: '1px solid var(--border)',
                overflow: 'hidden'
            }}>
                <div style={{
                    padding: '12px 16px',
                    borderBottom: '1px solid var(--border)',
                    background: 'var(--bg-primary)',
                    fontSize: '13px',
                    fontWeight: 500,
                    color: 'var(--text-secondary)',
                    display: 'flex',
                    alignItems: 'center',
                    gap: '8px'
                }}>
                    <span style={{ color: 'var(--accent)' }}>◈</span>
                    Changes
                </div>
                <div style={{
                    padding: '16px',
                    fontFamily: 'var(--font-mono)',
                    fontSize: '13px',
                    overflowX: 'auto'
                }}>
                    {renderDiff()}
                </div>
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
                                            <div className="plugin-output" style={{ padding: '15px', fontSize: '13px', lineHeight: '1.5', color: 'var(--text-primary)', background: 'var(--bg-primary)' }}>
                                                {data.result ? (
                                                    <Markdown>{data.result}</Markdown>
                                                ) : (
                                                    <span style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>No output produced.</span>
                                                )}
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
