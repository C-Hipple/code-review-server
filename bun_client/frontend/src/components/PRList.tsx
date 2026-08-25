import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { rpcCall } from '../api';
import { useIsMobile } from '../hooks/useMediaQuery';
import {
    Button,
    Input,
    Select,
    getStatusTone,
    mapReviewEase,
    Theme,
    ReviewLocation,
} from '../design';
import {
    PR_STATES,
    authorLogin,
    countByState,
    facetsByOpenCount,
    formatAbsoluteTime,
    formatRelativeTime,
    groupBySection,
    itemMatchesQuery,
    parseGitHubPRUrl,
    prState,
    uniqueValues,
} from '../pr_list_utils';
import type { Facet, PRState, ReviewItem } from '../pr_list_utils';
import { teamChips } from '../team_utils';

interface PRListProps {
    onOpenReview: (owner: string, repo: string, number: number) => void;
    onOpenPluginOutput: (owner: string, repo: string, number: number) => void;
    theme: Theme;
    reviewLocation: ReviewLocation;
    onThemeChange: (theme: Theme) => void;
}

interface GetReviewsResponse {
    content: string;
    items: ReviewItem[];
}

/** The sidebar's lifecycle buckets, plus the catch-all. */
type StateFilter = PRState | 'all';

const STATE_FILTER_KEY = 'prListStateFilter';

const STATE_LABELS: Record<StateFilter, string> = {
    open: 'Open',
    draft: 'Draft',
    merged: 'Merged',
    closed: 'Closed',
    all: 'Everything',
};

/**
 * Stand-in value for the dropdowns when the facet list below them has several
 * boxes checked — a single-value control has nothing truthful to show, so it
 * names the size of the selection and does nothing when picked.
 */
const MULTIPLE = '__multiple__';

function selectValue(selection: Set<string>): string {
    if (selection.size === 1) return Array.from(selection)[0];
    return selection.size > 1 ? MULTIPLE : '';
}

function selectionFor(value: string): (prev: Set<string>) => Set<string> {
    return prev => (value === MULTIPLE ? prev : new Set(value ? [value] : []));
}

function repoHeading(selection: Set<string>): string {
    if (selection.size === 1) return Array.from(selection)[0];
    return selection.size > 1 ? `${selection.size} repositories` : 'All repositories';
}

/** Dim tone per lifecycle state, borrowed from the shared status palette. */
function stateTone(state: PRState) {
    switch (state) {
        case 'open':
            return getStatusTone('success');
        case 'merged':
            return {
                fg: 'var(--text-merged)',
                bg: 'var(--bg-merged-dim)',
                border: 'var(--border-merged-dim)',
            };
        case 'closed':
            return getStatusTone('danger');
        default:
            return getStatusTone('neutral');
    }
}

function Icon({ children, size = 16 }: { children: ReactNode; size?: number }) {
    return (
        <svg
            width={size}
            height={size}
            viewBox="0 0 16 16"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.4"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
            style={{ flex: '0 0 auto' }}
        >
            {children}
        </svg>
    );
}

/** One glyph per sidebar bucket: open, draft, merged, closed, everything. */
const STATE_ICONS: Record<StateFilter, ReactNode> = {
    open: (
        <Icon>
            <circle cx="8" cy="8" r="5.25" />
            <circle cx="8" cy="8" r="1.6" fill="currentColor" stroke="none" />
        </Icon>
    ),
    draft: (
        <Icon>
            <circle cx="8" cy="8" r="5.25" strokeDasharray="2.2 2.2" />
            <circle cx="8" cy="8" r="1.6" fill="currentColor" stroke="none" />
        </Icon>
    ),
    merged: (
        <Icon>
            <circle cx="5" cy="3.6" r="1.7" />
            <circle cx="5" cy="12.4" r="1.7" />
            <circle cx="11.4" cy="5.4" r="1.7" />
            <path d="M5 5.3v5.4" />
            <path d="M10.4 7c-.7 1.9-2.5 3.1-5.4 3.4" />
        </Icon>
    ),
    closed: (
        <Icon>
            <circle cx="8" cy="8" r="5.25" />
            <path d="M6.2 6.2l3.6 3.6M9.8 6.2l-3.6 3.6" />
        </Icon>
    ),
    all: (
        <Icon>
            <path d="M2.8 4.5h10.4M2.8 8h10.4M2.8 11.5h10.4" />
        </Icon>
    ),
};

const REPO_ICON = (
    <Icon>
        <rect x="2.4" y="2.8" width="11.2" height="3.2" rx="1" />
        <path d="M3.6 6.4v6a1 1 0 0 0 1 1h6.8a1 1 0 0 0 1-1v-6" />
        <path d="M6.6 9.2h2.8" />
    </Icon>
);

const SEARCH_ICON = (
    <Icon>
        <circle cx="7" cy="7" r="4.3" />
        <path d="M10.2 10.2L14 14" />
    </Icon>
);

const REFRESH_ICON = (
    <Icon>
        <path d="M13.3 8a5.3 5.3 0 1 1-1.7-3.9" />
        <path d="M13.4 2.6v3.6H9.8" />
    </Icon>
);

export default function PRList({
    onOpenReview,
    onOpenPluginOutput,
    theme: _theme,
    reviewLocation,
    onThemeChange: _onThemeChange,
}: PRListProps) {
    const isMobile = useIsMobile();
    const [items, setItems] = useState<ReviewItem[]>([]);
    const [loading, setLoading] = useState(true);
    const [loadError, setLoadError] = useState(false);
    const [query, setQuery] = useState('');
    // Repo / author narrowing is a set: the checkable facet lists select several,
    // and the dropdowns above them are single-value shortcuts into the same set,
    // so the two controls can never disagree about what is filtered.
    const [repoFilters, setRepoFilters] = useState<Set<string>>(new Set());
    const [authorFilters, setAuthorFilters] = useState<Set<string>>(new Set());
    const [collapsedSections, setCollapsedSections] = useState<Set<string>>(new Set());

    // The list opens on the PRs that still need a review; the choice sticks so
    // someone who lives in "Everything" isn't reset on every visit.
    const [stateFilter, setStateFilter] = useState<StateFilter>(() => {
        const saved = localStorage.getItem(STATE_FILTER_KEY) as StateFilter | null;
        return saved && saved in STATE_LABELS ? saved : 'open';
    });

    // Manual "go to PR" form
    const [owner, setOwner] = useState('C-Hipple');
    const [repo, setRepo] = useState('code-review-server');
    const [prNumber, setPrNumber] = useState('');

    useEffect(() => {
        localStorage.setItem(STATE_FILTER_KEY, stateFilter);
    }, [stateFilter]);

    useEffect(() => {
        loadList();
    }, []);

    const loadList = async () => {
        setLoading(true);
        try {
            const res = await rpcCall<GetReviewsResponse>('RPCHandler.GetAllReviews', [{}]);
            const loaded = res.items || [];
            setItems(loaded);
            setLoadError(false);

            // Warm up plugin output for every visible PR so execution is already
            // underway by the time the user opens a PR or its plugin panel.
            for (const item of loaded) {
                if (item.owner && item.repo && item.number) {
                    rpcCall('RPCHandler.GetPluginOutput', [
                        { Owner: item.owner, Repo: item.repo, Number: item.number },
                    ]).catch(() => {});
                }
            }
        } catch (e) {
            console.error(e);
            setItems([]);
            setLoadError(true);
        } finally {
            setLoading(false);
        }
    };

    const repoOptions = useMemo(() => uniqueValues(items, 'repo'), [items]);
    const authorOptions = useMemo(
        () =>
            Array.from(new Set(items.map(i => authorLogin(i.author)).filter(Boolean))).sort(
                (a, b) => a.toLowerCase().localeCompare(b.toLowerCase())
            ),
        [items]
    );
    const repoFacets = useMemo(() => facetsByOpenCount(items, i => i.repo), [items]);
    const authorFacets = useMemo(
        () => facetsByOpenCount(items, i => authorLogin(i.author)),
        [items]
    );

    // Narrowing (repo / author / text) applies first, so the sidebar counts
    // describe what the current search would actually turn up. An empty set
    // means "no constraint" rather than "nothing matches".
    const narrowed = useMemo(
        () =>
            items.filter(
                item =>
                    (repoFilters.size === 0 || repoFilters.has(item.repo)) &&
                    (authorFilters.size === 0 || authorFilters.has(authorLogin(item.author))) &&
                    itemMatchesQuery(item, query)
            ),
        [items, repoFilters, authorFilters, query]
    );

    const counts = useMemo(() => countByState(narrowed), [narrowed]);

    const visible = useMemo(
        () =>
            stateFilter === 'all'
                ? narrowed
                : narrowed.filter(item => prState(item) === stateFilter),
        [narrowed, stateFilter]
    );

    const sections = useMemo(() => groupBySection(visible), [visible]);

    const hasNarrowFilters = query.trim() !== '' || repoFilters.size > 0 || authorFilters.size > 0;
    const allCollapsed = sections.length > 0 && collapsedSections.size >= sections.length;

    const clearFilters = () => {
        setQuery('');
        setRepoFilters(new Set());
        setAuthorFilters(new Set());
    };

    const toggleFacet = (
        setFilters: React.Dispatch<React.SetStateAction<Set<string>>>,
        value: string
    ) => {
        setFilters(prev => {
            const next = new Set(prev);
            if (next.has(value)) next.delete(value);
            else next.add(value);
            return next;
        });
    };

    const toggleSection = (name: string) => {
        setCollapsedSections(prev => {
            const next = new Set(prev);
            if (next.has(name)) next.delete(name);
            else next.add(name);
            return next;
        });
    };

    const toggleAllSections = () => {
        setCollapsedSections(allCollapsed ? new Set() : new Set(sections.map(s => s.name)));
    };

    const handleSubmit = (e: React.FormEvent) => {
        e.preventDefault();
        const num = parseInt(prNumber, 10);
        if (owner && repo && !isNaN(num)) {
            onOpenReview(owner, repo, num);
        }
    };

    const navItem = (filter: StateFilter, count: number) => (
        <button
            key={filter}
            type="button"
            className="crs-nav-item"
            aria-current={stateFilter === filter}
            onClick={() => setStateFilter(filter)}
        >
            {STATE_ICONS[filter]}
            <span>{STATE_LABELS[filter]}</span>
            <span className="crs-nav-count">{count}</span>
        </button>
    );

    const gotoForm = (
        <form onSubmit={handleSubmit} className="crs-goto-form">
            <Input
                type="text"
                value={owner}
                onChange={e => setOwner(e.target.value)}
                placeholder="owner"
                aria-label="Owner"
                style={{ padding: '6px 8px', fontSize: '13px' }}
            />
            <Input
                type="text"
                value={repo}
                onChange={e => setRepo(e.target.value)}
                placeholder="repo"
                aria-label="Repo"
                style={{ padding: '6px 8px', fontSize: '13px' }}
            />
            <Input
                type="number"
                value={prNumber}
                onChange={e => setPrNumber(e.target.value)}
                placeholder="number"
                aria-label="PR number"
                style={{ padding: '6px 8px', fontSize: '13px' }}
            />
            <Button type="submit" size="sm" variant="secondary" style={{ width: '100%' }}>
                Open PR
            </Button>
        </form>
    );

    return (
        <div className="crs-list">
            <aside className="crs-sidebar">
                <div className="crs-sidebar-head">
                    <span style={{ color: 'var(--accent)' }}>{REPO_ICON}</span>
                    <div style={{ minWidth: 0 }}>
                        <div className="crs-sidebar-title">{repoHeading(repoFilters)}</div>
                        <div className="crs-sidebar-subtitle">
                            {loading ? 'Loading…' : `${items.length} reviews tracked`}
                        </div>
                    </div>
                </div>

                <nav className="crs-nav" aria-label="Filter by state">
                    {PR_STATES.map(state => navItem(state, counts[state]))}
                    {navItem('all', narrowed.length)}
                </nav>

                <div className="crs-sidebar-section">
                    <div className="crs-sidebar-label">
                        Narrow
                        {hasNarrowFilters && (
                            <button type="button" className="crs-link-btn" onClick={clearFilters}>
                                clear
                            </button>
                        )}
                    </div>
                    <div className="crs-narrow">
                        <Select
                            aria-label="Filter by repo"
                            value={selectValue(repoFilters)}
                            onChange={e => setRepoFilters(selectionFor(e.target.value))}
                        >
                            <option value="">All repos</option>
                            {repoFilters.size > 1 && (
                                <option value={MULTIPLE}>{repoFilters.size} repos selected</option>
                            )}
                            {repoOptions.map(value => (
                                <option key={value} value={value}>
                                    {value}
                                </option>
                            ))}
                        </Select>
                        <Select
                            aria-label="Filter by author"
                            value={selectValue(authorFilters)}
                            onChange={e => setAuthorFilters(selectionFor(e.target.value))}
                        >
                            <option value="">All authors</option>
                            {authorFilters.size > 1 && (
                                <option value={MULTIPLE}>
                                    {authorFilters.size} authors selected
                                </option>
                            )}
                            {authorOptions.map(value => (
                                <option key={value} value={value}>
                                    {value}
                                </option>
                            ))}
                        </Select>
                    </div>
                </div>

                <FacetList
                    label="Repos"
                    facets={repoFacets}
                    selected={repoFilters}
                    onToggle={value => toggleFacet(setRepoFilters, value)}
                    onClear={() => setRepoFilters(new Set())}
                    collapsible={isMobile}
                />
                <FacetList
                    label="Authors"
                    facets={authorFacets}
                    selected={authorFilters}
                    onToggle={value => toggleFacet(setAuthorFilters, value)}
                    onClear={() => setAuthorFilters(new Set())}
                    collapsible={isMobile}
                />

                {/* Collapsed by default: reaching a PR no workflow tracks is the
                    exception, and the filter box already opens pasted PR URLs. */}
                <details className="crs-sidebar-section">
                    <summary className="crs-sidebar-label">Open a PR by number</summary>
                    {gotoForm}
                </details>
            </aside>

            <div className="crs-main">
                <div className="crs-toolbar">
                    <div style={{ flex: 1, minWidth: 0 }}>
                        <Input
                            type="text"
                            value={query}
                            onChange={e => {
                                const value = e.target.value;
                                const pasted = parseGitHubPRUrl(value);
                                if (pasted) {
                                    onOpenReview(pasted.owner, pasted.repo, pasted.number);
                                    return;
                                }
                                setQuery(value);
                            }}
                            placeholder="Filter by title, repo, author, number… (or paste a GitHub PR URL)"
                            aria-label="Filter reviews"
                            icon={SEARCH_ICON}
                            clearable
                            onClear={() => setQuery('')}
                        />
                    </div>
                    <span className="crs-count">
                        {visible.length} of {items.length}
                    </span>
                    <button
                        type="button"
                        className="crs-icon-btn"
                        onClick={toggleAllSections}
                        disabled={sections.length === 0}
                        title={allCollapsed ? 'Expand all sections' : 'Collapse all sections'}
                        aria-label={allCollapsed ? 'Expand all sections' : 'Collapse all sections'}
                    >
                        <Icon>
                            {allCollapsed ? (
                                <path d="M4.5 6.2L8 9.8l3.5-3.6" />
                            ) : (
                                <path d="M4.5 9.8L8 6.2l3.5 3.6" />
                            )}
                        </Icon>
                    </button>
                    <button
                        type="button"
                        className="crs-icon-btn"
                        onClick={loadList}
                        disabled={loading}
                        title="Refresh"
                        aria-label="Refresh"
                    >
                        <span className={loading ? 'crs-spin' : undefined}>{REFRESH_ICON}</span>
                    </button>
                </div>

                {loading && items.length === 0 ? (
                    <div className="crs-empty">Loading reviews…</div>
                ) : loadError ? (
                    <div className="crs-empty">
                        <div>Couldn&apos;t load reviews.</div>
                        <div className="crs-empty-hint">
                            The server may not be running — check the bridge and try again.
                        </div>
                        <div className="crs-empty-actions">
                            <Button size="sm" variant="secondary" onClick={loadList}>
                                Retry
                            </Button>
                        </div>
                    </div>
                ) : sections.length === 0 ? (
                    <div className="crs-empty">
                        {items.length === 0 ? (
                            <>
                                <div>No reviews tracked yet.</div>
                                <div className="crs-empty-hint">
                                    Workflows populate this list on their next run.
                                </div>
                            </>
                        ) : (
                            <>
                                <div>
                                    No {stateFilter === 'all' ? '' : STATE_LABELS[stateFilter]} PRs
                                    match{hasNarrowFilters ? ' the current filters' : ''}.
                                </div>
                                <div className="crs-empty-actions">
                                    {hasNarrowFilters && (
                                        <Button
                                            size="sm"
                                            variant="secondary"
                                            onClick={clearFilters}
                                        >
                                            Clear filters
                                        </Button>
                                    )}
                                    {stateFilter !== 'all' && narrowed.length > visible.length && (
                                        <Button
                                            size="sm"
                                            variant="secondary"
                                            onClick={() => setStateFilter('all')}
                                        >
                                            Show everything ({narrowed.length})
                                        </Button>
                                    )}
                                </div>
                            </>
                        )}
                    </div>
                ) : (
                    sections.map(section => {
                        const isCollapsed = collapsedSections.has(section.name);
                        return (
                            <section key={section.name} className="crs-section">
                                <button
                                    type="button"
                                    className="crs-section-header"
                                    onClick={() => toggleSection(section.name)}
                                    aria-expanded={!isCollapsed}
                                >
                                    <span
                                        className="crs-chevron"
                                        style={{
                                            transform: isCollapsed
                                                ? 'rotate(-90deg)'
                                                : 'rotate(0deg)',
                                        }}
                                    >
                                        <Icon size={12}>
                                            <path d="M4 6l4 4 4-4" />
                                        </Icon>
                                    </span>
                                    <span>{section.name}</span>
                                    <span className="crs-section-count">
                                        {section.items.length}
                                    </span>
                                </button>
                                {!isCollapsed &&
                                    section.items.map(item => (
                                        <PRRow
                                            key={`${item.section}-${item.repo}-${item.number}-${item.title}`}
                                            item={item}
                                            reviewLocation={reviewLocation}
                                            onOpenReview={onOpenReview}
                                            onOpenPluginOutput={onOpenPluginOutput}
                                        />
                                    ))}
                            </section>
                        );
                    })
                )}
            </div>
        </div>
    );
}

interface FacetListProps {
    label: string;
    facets: Facet[];
    selected: Set<string>;
    onToggle: (value: string) => void;
    onClear: () => void;
    /** Fold into a disclosure — on phones the lists would bury the reviews. */
    collapsible: boolean;
}

/**
 * A checkable list of values with their open-PR counts. Ten rows are visible
 * (see `--facet-row-height` in App.css); the rest scroll.
 */
function FacetList({ label, facets, selected, onToggle, onClear, collapsible }: FacetListProps) {
    if (facets.length === 0) return null;

    const header = (
        <>
            {label}
            {selected.size > 0 &&
                (collapsible ? (
                    // A button inside a <summary> would toggle the disclosure.
                    <span className="crs-facet-selected">{selected.size} selected</span>
                ) : (
                    <button type="button" className="crs-link-btn" onClick={onClear}>
                        clear
                    </button>
                ))}
        </>
    );

    const list = (
        <div className="crs-facet">
            {facets.map(facet => (
                <label className="crs-facet-item" key={facet.value}>
                    <input
                        type="checkbox"
                        checked={selected.has(facet.value)}
                        onChange={() => onToggle(facet.value)}
                    />
                    <span className="crs-facet-name" title={facet.value}>
                        {facet.value}
                    </span>
                    <span
                        className="crs-facet-count"
                        title={`${facet.openCount} open ${facet.openCount === 1 ? 'PR' : 'PRs'}`}
                    >
                        {facet.openCount}
                    </span>
                </label>
            ))}
        </div>
    );

    return collapsible ? (
        <details className="crs-sidebar-section">
            <summary className="crs-sidebar-label">{header}</summary>
            {list}
        </details>
    ) : (
        <div className="crs-sidebar-section">
            <div className="crs-sidebar-label">{header}</div>
            {list}
        </div>
    );
}

interface PRRowProps {
    item: ReviewItem;
    reviewLocation: ReviewLocation;
    onOpenReview: (owner: string, repo: string, number: number) => void;
    onOpenPluginOutput: (owner: string, repo: string, number: number) => void;
}

function PRRow({ item, reviewLocation, onOpenReview, onOpenPluginOutput }: PRRowProps) {
    const isPR = item.number > 0;
    const state = prState(item);
    const tone = stateTone(state);
    const ease = mapReviewEase(item.review_ease);
    const easeTone = ease ? getStatusTone(ease.variant) : null;
    const relative = formatRelativeTime(item.created_at);
    const teams = teamChips(item.required_teams);
    // Rows open wherever the preference points; the action buttons cover the
    // other destination so both are always one click away.
    const rowOpensGitHub = reviewLocation === 'github';

    const openRow = () => {
        if (!isPR) return;
        if (rowOpensGitHub) window.open(item.url, '_blank');
        else onOpenReview(item.owner, item.repo, item.number);
    };

    return (
        <div className="crs-row">
            <a
                className="crs-row-link"
                href={
                    rowOpensGitHub
                        ? item.url
                        : `/?owner=${item.owner}&repo=${item.repo}&number=${item.number}`
                }
                onClick={e => {
                    if (!isPR) {
                        e.preventDefault();
                        return;
                    }
                    // Let ctrl/cmd/shift/alt clicks behave like normal links.
                    if (e.button === 0 && !e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey) {
                        e.preventDefault();
                        openRow();
                    }
                }}
            >
                <span
                    className="crs-row-state"
                    style={{ color: tone.fg, background: tone.bg, borderColor: tone.border }}
                >
                    {isPR ? state : 'item'}
                </span>
                <span className="crs-row-body">
                    <span className="crs-row-title-line">
                        <span className="crs-row-title">{item.title}</span>
                        {ease && easeTone && (
                            <span
                                className="crs-row-ease"
                                style={{
                                    color: easeTone.fg,
                                    background: easeTone.bg,
                                    borderColor: easeTone.border,
                                }}
                                title={`Review ease: ${ease.label.toLowerCase()}`}
                            >
                                {ease.label}
                            </span>
                        )}
                    </span>
                    <span className="crs-row-meta">
                        {isPR ? (
                            <>
                                <span className="crs-row-repo" title={`${item.owner}/${item.repo}`}>
                                    {item.repo}
                                </span>
                                <span className="crs-row-number">#{item.number}</span>
                                {item.author && (
                                    <span title={item.author}>{authorLogin(item.author)}</span>
                                )}
                                {relative && (
                                    <span title={formatAbsoluteTime(item.created_at)}>
                                        {relative}
                                    </span>
                                )}
                                {teams.length > 0 && (
                                    <span className="crs-row-teams">
                                        {teams.map(team => {
                                            const tone = getStatusTone(team.variant);
                                            return (
                                                <span
                                                    key={team.key}
                                                    className={`crs-row-team crs-row-team--${team.relationship}`}
                                                    style={{
                                                        color: tone.fg,
                                                        background: tone.bg,
                                                        borderColor: tone.border,
                                                    }}
                                                    title={team.title}
                                                >
                                                    {team.label}
                                                </span>
                                            );
                                        })}
                                    </span>
                                )}
                            </>
                        ) : (
                            <span style={{ fontStyle: 'italic' }}>Non-PR item</span>
                        )}
                    </span>
                </span>
            </a>
            {isPR && (
                <div className="crs-row-actions">
                    <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => onOpenPluginOutput(item.owner, item.repo, item.number)}
                    >
                        Plugins
                    </Button>
                    {rowOpensGitHub ? (
                        <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => onOpenReview(item.owner, item.repo, item.number)}
                        >
                            Review
                        </Button>
                    ) : (
                        <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => window.open(item.url, '_blank')}
                        >
                            GitHub
                        </Button>
                    )}
                </div>
            )}
        </div>
    );
}
