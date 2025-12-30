import { useState, useEffect } from 'react';
import { rpcCall } from '../api';

interface PRListProps {
    onOpenReview: (owner: string, repo: string, number: number) => void;
}

export default function PRList({ onOpenReview }: PRListProps) {
    const [content, setContent] = useState<string>('');
    const [loading, setLoading] = useState(false);

    // Manual form state
    const [owner, setOwner] = useState('C-Hipple'); // Default based on user context
    const [repo, setRepo] = useState('code-review-server');
    const [prNumber, setPrNumber] = useState('');

    useEffect(() => {
        loadList();
    }, []);

    const loadList = async () => {
        setLoading(true);
        try {
            const res = await rpcCall<{ content: string }>('RPCHandler.GetAllReviews', [{}]);
            setContent(res.content || '');
        } catch (e) {
            console.error(e);
            setContent('Error loading list.');
        } finally {
            setLoading(false);
        }
    };

    const handleSubmit = (e: React.FormEvent) => {
        e.preventDefault();
        const num = parseInt(prNumber, 10);
        if (owner && repo && !isNaN(num)) {
            onOpenReview(owner, repo, num);
        }
    };

    return (
        <div className="pr-list">
            <div className="card" style={{
                background: 'var(--bg-secondary)',
                border: '1px solid var(--border)',
                borderRadius: '8px',
                padding: '20px',
                marginBottom: '20px'
            }}>
                <h2 style={{ marginTop: 0, fontSize: '18px' }}>Open Review Manually</h2>
                <form onSubmit={handleSubmit} style={{ display: 'flex', gap: '10px', alignItems: 'flex-end', flexWrap: 'wrap' }}>
                    <div>
                        <label style={{ display: 'block', marginBottom: '5px', fontSize: '12px', color: 'var(--text-secondary)' }}>Owner</label>
                        <input
                            type="text"
                            value={owner}
                            onChange={e => setOwner(e.target.value)}
                            style={inputStyle}
                        />
                    </div>
                    <div>
                        <label style={{ display: 'block', marginBottom: '5px', fontSize: '12px', color: 'var(--text-secondary)' }}>Repo</label>
                        <input
                            type="text"
                            value={repo}
                            onChange={e => setRepo(e.target.value)}
                            style={inputStyle}
                        />
                    </div>
                    <div>
                        <label style={{ display: 'block', marginBottom: '5px', fontSize: '12px', color: 'var(--text-secondary)' }}>PR #</label>
                        <input
                            type="number"
                            value={prNumber}
                            onChange={e => setPrNumber(e.target.value)}
                            style={inputStyle}
                        />
                    </div>
                    <button type="submit" style={buttonStyle}>Go</button>
                </form>
            </div>

            <div className="card" style={{
                background: 'var(--bg-secondary)',
                border: '1px solid var(--border)',
                borderRadius: '8px',
                padding: '20px'
            }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '15px' }}>
                    <h2 style={{ margin: 0, fontSize: '18px' }}>Your Reviews</h2>
                    <button onClick={loadList} style={{ ...buttonStyle, background: 'var(--bg-tertiary)' }}>Refresh</button>
                </div>

                {loading ? (
                    <div>Loading...</div>
                ) : (
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px' }}>
                        {parseOrgContent(content).map((section, sIdx) => (
                            <div key={sIdx}>
                                <h3 style={{
                                    fontSize: '13px',
                                    textTransform: 'uppercase',
                                    letterSpacing: '0.05em',
                                    color: 'var(--text-secondary)',
                                    marginBottom: '12px',
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '8px',
                                    borderBottom: '1px solid var(--border)',
                                    paddingBottom: '8px'
                                }}>
                                    {cleanSectionName(section.name)}
                                </h3>
                                <div style={{ display: 'flex', flexDirection: 'column', gap: '8px' }}>
                                    {section.items.length === 0 ? (
                                        <div style={{ fontSize: '14px', color: 'var(--text-secondary)', fontStyle: 'italic', paddingLeft: '10px' }}>
                                            No items in this section
                                        </div>
                                    ) : (
                                        section.items.map((item, iIdx) => (
                                            <div key={iIdx} style={{
                                                background: 'var(--bg-primary)',
                                                border: '1px solid var(--border)',
                                                borderRadius: '8px',
                                                padding: '12px 16px',
                                                display: 'flex',
                                                justifyContent: 'space-between',
                                                alignItems: 'center',
                                                transition: 'all 0.2s ease'
                                            }} className="pr-item-card">
                                                <div style={{ flex: 1 }}>
                                                    <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '4px' }}>
                                                        <span style={{
                                                            fontSize: '10px',
                                                            fontWeight: 700,
                                                            padding: '2px 6px',
                                                            borderRadius: '4px',
                                                            background: getStatusColor(item.status),
                                                            color: 'white'
                                                        }}>{item.status}</span>
                                                        <span style={{ fontWeight: 500, fontSize: '15px' }}>{cleanHeader(item.header)}</span>
                                                    </div>
                                                    {item.number ? (
                                                        <div style={{ fontSize: '13px', color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)' }}>
                                                            {item.owner}/{item.repo} <span style={{ color: 'var(--accent)' }}>#{item.number}</span>
                                                        </div>
                                                    ) : (
                                                        <div style={{ fontSize: '12px', color: 'var(--text-secondary)', fontStyle: 'italic' }}>
                                                            {item.details.join(' ')}
                                                        </div>
                                                    )}
                                                </div>
                                                {item.number && (
                                                    <button
                                                        onClick={() => onOpenReview(item.owner, item.repo, item.number)}
                                                        style={{
                                                            background: 'var(--accent)',
                                                            color: 'white',
                                                            border: 'none',
                                                            padding: '8px 16px',
                                                            borderRadius: '6px',
                                                            fontSize: '13px',
                                                            fontWeight: 600,
                                                            marginLeft: '15px',
                                                            transition: 'transform 0.1s active'
                                                        }}
                                                    >
                                                        Review
                                                    </button>
                                                )}
                                            </div>
                                        ))
                                    )}
                                </div>
                            </div>
                        ))}
                    </div>
                )}
            </div>
        </div>
    );
}

// Helper functions for parsing and styling
const parseOrgContent = (text: string) => {
    if (!text) return [];

    const sections: { name: string, items: any[] }[] = [];
    const lines = text.split('\n');
    let currentSection: { name: string, items: any[] } | null = null;
    let currentItem: any = null;

    for (const line of lines) {
        if (line.startsWith('* ')) {
            currentSection = { name: line.substring(2).trim(), items: [] };
            sections.push(currentSection);
            currentItem = null;
        } else if (line.startsWith('** ')) {
            currentItem = {
                header: line.substring(3).trim(),
                details: [],
                status: line.split(' ')[1]
            };
            if (currentSection) {
                currentSection.items.push(currentItem);
            } else {
                currentSection = { name: 'Tasks', items: [currentItem] };
                sections.push(currentSection);
            }
        } else if (currentItem && line.trim()) {
            currentItem.details.push(line.trim());
        }
    }

    return sections.map(s => ({
        ...s,
        items: s.items.map(item => {
            const prNumberLine = item.details.find((d: string) => /^\d+$/.test(d));
            const repoLine = item.details.find((d: string) => d.startsWith('Repo:'));

            if (prNumberLine && repoLine) {
                const repoPath = repoLine.replace('Repo:', '').trim();
                const parts = repoPath.split('/');

                if (parts.length >= 2) {
                    // Normal case: owner/repo
                    return {
                        ...item,
                        owner: parts[0],
                        repo: parts[1],
                        number: parseInt(prNumberLine, 10)
                    };
                } else if (parts.length === 1 && parts[0]) {
                    // Fallback for when owner is missing but we have a default
                    // Based on user context, default owner might be C-Hipple
                    return {
                        ...item,
                        owner: 'C-Hipple',
                        repo: parts[0],
                        number: parseInt(prNumberLine, 10)
                    };
                }
            }
            return item;
        })
    }));
};


const getStatusColor = (status: string) => {
    switch (status) {
        case 'DONE': return 'var(--success)';
        case 'TODO': return 'var(--accent)';
        case 'CANCELLED': return 'var(--text-secondary)';
        case 'PROGRESS': return 'var(--warning)';
        default: return 'var(--bg-tertiary)';
    }
};

const cleanHeader = (header: string) => {
    // Remove the status from the header if it's there
    const parts = header.split(' ');
    if (['TODO', 'DONE', 'CANCELLED', 'PROGRESS', 'WAITING', 'BLOCKED'].includes(parts[0])) {
        return parts.slice(1).join(' ').split(':')[0].trim();
    }
    return header.split(':')[0].trim();
};

const cleanSectionName = (name: string) => {
    // Remove the status from the section name if it's there
    const parts = name.split(' ');
    let cleaned = name;
    if (['TODO', 'DONE', 'CANCELLED', 'PROGRESS', 'WAITING', 'BLOCKED'].includes(parts[0])) {
        cleaned = parts.slice(1).join(' ');
    }
    // Remove the ratio suffix [n/m]
    return cleaned.replace(/\[\d+\/\d+\]$/, '').trim();
};



const inputStyle = {
    background: 'var(--bg-primary)',
    border: '1px solid var(--border)',
    color: 'var(--text-primary)',
    padding: '8px 12px',
    borderRadius: '6px',
    fontSize: '14px',
    outline: 'none',
    width: '150px'
};

const buttonStyle = {
    background: 'var(--accent)',
    color: 'white',
    border: 'none',
    padding: '8px 16px',
    borderRadius: '6px',
    fontSize: '14px',
    fontWeight: 500,
};
