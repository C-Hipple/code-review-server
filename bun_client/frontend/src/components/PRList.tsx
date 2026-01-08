import { useState, useEffect } from 'react';
import { rpcCall } from '../api';

interface PRListProps {
    onOpenReview: (owner: string, repo: string, number: number) => void;
}

// Matches the ReviewItem struct from the Go backend
interface ReviewItem {
    section: string;
    status: string;
    title: string;
    owner: string;
    repo: string;
    number: number;
    author: string;
    url: string;
}

interface GetReviewsResponse {
    content: string;
    items: ReviewItem[];
}

// Group items by section
interface Section {
    name: string;
    items: ReviewItem[];
}

// Status options for the dropdown filter (static options)
const STATUS_OPTIONS = [
    { value: '', label: 'All Statuses' },
    { value: 'TODO', label: 'TODO' },
    { value: 'PROGRESS', label: 'In Progress' },
    { value: 'DONE', label: 'Done' },
    { value: 'CANCELLED', label: 'Cancelled' },
];

// Configuration for dynamic dropdown filters
// Each filter extracts unique values from the items based on a field
interface DynamicFilterConfig {
    key: keyof ReviewItem;  // Field to filter on
    label: string;          // Display label
    allLabel: string;       // "All X" label
}

const DYNAMIC_FILTERS: DynamicFilterConfig[] = [
    { key: 'author', label: 'Author', allLabel: 'All Authors' },
    { key: 'owner', label: 'Owner', allLabel: 'All Owners' },
    { key: 'repo', label: 'Repo', allLabel: 'All Repos' },
];

// Helper to extract unique sorted values for a field from items
function getUniqueValues(items: ReviewItem[], field: keyof ReviewItem): string[] {
    return Array.from(
        new Set(
            items
                .map(item => String(item[field] || ''))
                .filter(val => val.trim() !== '')
        )
    ).sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
}

export default function PRList({ onOpenReview }: PRListProps) {
    const [sections, setSections] = useState<Section[]>([]);
    const [loading, setLoading] = useState(false);
    const [filterText, setFilterText] = useState('');
    const [statusFilter, setStatusFilter] = useState('');
    
    // Dynamic filters state - one value per filter key
    const [dynamicFilters, setDynamicFilters] = useState<Record<string, string>>(() => {
        const initial: Record<string, string> = {};
        DYNAMIC_FILTERS.forEach(f => { initial[f.key] = ''; });
        return initial;
    });

    // Manual form state
    const [owner, setOwner] = useState('C-Hipple'); // Default based on user context
    const [repo, setRepo] = useState('code-review-server');
    const [prNumber, setPrNumber] = useState('');

    // Get all items flattened for computing unique values
    const allItems = sections.flatMap(s => s.items);

    // Check if any filters are active
    const hasActiveFilters = filterText.trim() !== '' || 
        statusFilter !== '' || 
        Object.values(dynamicFilters).some(v => v !== '');

    // Filter items based on all active filters
    const filterItem = (item: ReviewItem): boolean => {
        // Status filter
        if (statusFilter && item.status !== statusFilter) {
            return false;
        }
        
        // Dynamic filters
        for (const config of DYNAMIC_FILTERS) {
            const filterValue = dynamicFilters[config.key];
            if (filterValue && String(item[config.key]) !== filterValue) {
                return false;
            }
        }
        
        // Text search filter
        if (!filterText.trim()) return true;
        const search = filterText.toLowerCase();
        return (
            item.title.toLowerCase().includes(search) ||
            item.author.toLowerCase().includes(search) ||
            item.owner.toLowerCase().includes(search) ||
            item.repo.toLowerCase().includes(search)
        );
    };

    // Update a single dynamic filter
    const setDynamicFilter = (key: string, value: string) => {
        setDynamicFilters(prev => ({ ...prev, [key]: value }));
    };

    // Clear all filters
    const clearAllFilters = () => {
        setFilterText('');
        setStatusFilter('');
        setDynamicFilters(() => {
            const cleared: Record<string, string> = {};
            DYNAMIC_FILTERS.forEach(f => { cleared[f.key] = ''; });
            return cleared;
        });
    };

    // Get filtered sections (only sections with matching items)
    const filteredSections = sections
        .map(section => ({
            ...section,
            items: section.items.filter(filterItem)
        }))
        .filter(section => section.items.length > 0);

    useEffect(() => {
        loadList();
    }, []);

    const loadList = async () => {
        setLoading(true);
        try {
            const res = await rpcCall<GetReviewsResponse>('RPCHandler.GetAllReviews', [{}]);
            const items = res.items || [];
            
            // Group items by section
            const sectionMap = new Map<string, ReviewItem[]>();
            for (const item of items) {
                const sectionName = item.section || 'Other';
                if (!sectionMap.has(sectionName)) {
                    sectionMap.set(sectionName, []);
                }
                sectionMap.get(sectionName)!.push(item);
            }
            
            // Convert to array
            const sectionList: Section[] = [];
            for (const [name, items] of sectionMap) {
                sectionList.push({ name, items });
            }
            
            setSections(sectionList);
        } catch (e) {
            console.error(e);
            setSections([]);
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

                {/* Filter Bar */}
                <div style={{ marginBottom: '20px' }}>
                    {/* Search Input */}
                    <div style={{ position: 'relative' }}>
                        <input
                            type="text"
                            value={filterText}
                            onChange={e => setFilterText(e.target.value)}
                            placeholder="Filter by title, author, owner, or repo..."
                            style={{
                                ...filterInputStyle,
                                width: '100%',
                                paddingLeft: '36px'
                            }}
                        />
                        <span style={{
                            position: 'absolute',
                            left: '12px',
                            top: '50%',
                            transform: 'translateY(-50%)',
                            color: 'var(--text-secondary)',
                            fontSize: '14px',
                            pointerEvents: 'none'
                        }}>⌕</span>
                        {filterText && (
                            <button
                                onClick={() => setFilterText('')}
                                style={{
                                    position: 'absolute',
                                    right: '8px',
                                    top: '50%',
                                    transform: 'translateY(-50%)',
                                    background: 'var(--bg-tertiary)',
                                    border: 'none',
                                    borderRadius: '50%',
                                    width: '20px',
                                    height: '20px',
                                    display: 'flex',
                                    alignItems: 'center',
                                    justifyContent: 'center',
                                    cursor: 'pointer',
                                    color: 'var(--text-secondary)',
                                    fontSize: '12px',
                                    padding: 0
                                }}
                            >×</button>
                        )}
                    </div>

                    {/* Filter Dropdowns Row */}
                    <div style={{ 
                        display: 'flex', 
                        gap: '12px', 
                        marginTop: '12px',
                        flexWrap: 'wrap',
                        alignItems: 'center'
                    }}>
                        {/* Status Filter (static options) */}
                        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                            <label style={{ fontSize: '12px', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                                Status:
                            </label>
                            <select
                                value={statusFilter}
                                onChange={e => setStatusFilter(e.target.value)}
                                style={selectStyle}
                            >
                                {STATUS_OPTIONS.map(opt => (
                                    <option key={opt.value} value={opt.value}>{opt.label}</option>
                                ))}
                            </select>
                        </div>

                        {/* Dynamic Filters (generated from data) */}
                        {DYNAMIC_FILTERS.map(config => {
                            const options = getUniqueValues(allItems, config.key);
                            return (
                                <div key={config.key} style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <label style={{ fontSize: '12px', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                                        {config.label}:
                                    </label>
                                    <select
                                        value={dynamicFilters[config.key]}
                                        onChange={e => setDynamicFilter(config.key, e.target.value)}
                                        style={selectStyle}
                                    >
                                        <option value="">{config.allLabel}</option>
                                        {options.map(val => (
                                            <option key={val} value={val}>{val}</option>
                                        ))}
                                    </select>
                                </div>
                            );
                        })}

                        {/* Clear All Filters Button */}
                        {hasActiveFilters && (
                            <button
                                onClick={clearAllFilters}
                                style={{
                                    background: 'transparent',
                                    border: '1px solid var(--border)',
                                    color: 'var(--text-secondary)',
                                    padding: '6px 12px',
                                    borderRadius: '6px',
                                    fontSize: '12px',
                                    cursor: 'pointer',
                                    marginLeft: 'auto',
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '4px'
                                }}
                            >
                                <span>×</span> Clear filters
                            </button>
                        )}
                    </div>

                    {/* Results count */}
                    {hasActiveFilters && (
                        <div style={{ 
                            fontSize: '12px', 
                            color: 'var(--text-secondary)', 
                            marginTop: '12px' 
                        }}>
                            Showing {filteredSections.reduce((acc, s) => acc + s.items.length, 0)} of {sections.reduce((acc, s) => acc + s.items.length, 0)} items
                        </div>
                    )}
                </div>

                {loading ? (
                    <div>Loading...</div>
                ) : (
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px' }}>
                        {filteredSections.length === 0 && hasActiveFilters ? (
                            <div style={{ 
                                fontSize: '14px', 
                                color: 'var(--text-secondary)', 
                                fontStyle: 'italic',
                                textAlign: 'center',
                                padding: '40px 20px'
                            }}>
                                No items match the current filters
                            </div>
                        ) : filteredSections.map((section, sIdx) => (
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
                                    {section.name}
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
                                                        <span style={{ fontWeight: 500, fontSize: '15px' }}>{item.title}</span>
                                                    </div>
                                                    {item.number ? (
                                                        <div style={{ fontSize: '13px', color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)' }}>
                                                            {item.owner}/{item.repo} <span style={{ color: 'var(--accent)' }}>#{item.number}</span>
                                                            {item.author && <span style={{ marginLeft: '8px', color: 'var(--text-secondary)' }}>by {item.author}</span>}
                                                        </div>
                                                    ) : (
                                                        <div style={{ fontSize: '12px', color: 'var(--text-secondary)', fontStyle: 'italic' }}>
                                                            Non-PR item
                                                        </div>
                                                    )}
                                                </div>
                                                {item.number > 0 && (
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

const getStatusColor = (status: string) => {
    switch (status) {
        case 'DONE': return 'var(--success)';
        case 'TODO': return 'var(--accent)';
        case 'CANCELLED': return 'var(--text-secondary)';
        case 'PROGRESS': return 'var(--warning)';
        default: return 'var(--bg-tertiary)';
    }
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

const filterInputStyle = {
    background: 'var(--bg-primary)',
    border: '1px solid var(--border)',
    color: 'var(--text-primary)',
    padding: '10px 12px',
    borderRadius: '8px',
    fontSize: '14px',
    outline: 'none',
    transition: 'border-color 0.2s ease',
};

const selectStyle = {
    background: 'var(--bg-primary)',
    border: '1px solid var(--border)',
    color: 'var(--text-primary)',
    padding: '6px 10px',
    borderRadius: '6px',
    fontSize: '13px',
    outline: 'none',
    cursor: 'pointer',
    minWidth: '120px',
};
