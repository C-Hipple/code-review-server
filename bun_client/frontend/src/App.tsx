import { useState, useEffect } from 'react';
import PRList from './components/PRList';
import Review from './components/Review';
import PluginOutput from './components/PluginOutput';
import ConfigManager from './components/ConfigManager';
import { rpcCall } from './api';
import { useIsMobile } from './hooks/useMediaQuery';
import {
    Modal,
    Select,
    Theme,
    resolveTheme,
    THEME_OPTIONS,
    ReviewLocation,
    REVIEW_LOCATION_OPTIONS,
    DiffFontSize,
    VALID_DIFF_FONT_SIZES,
    DEFAULT_DIFF_FONT_SIZE,
    DIFF_FONT_SIZE_OPTIONS,
    DIFF_FONT_SIZE_PX,
} from './design';

interface PRParams {
    owner: string;
    repo: string;
    number: number;
}

function App() {
    const [view, setView] = useState<'LIST' | 'REVIEW' | 'PLUGIN_OUTPUT'>('LIST');
    const [currentPR, setCurrentPR] = useState<PRParams | null>(null);
    const [showPrefs, setShowPrefs] = useState(false);
    const [prefsTab, setPrefsTab] = useState<'appearance' | 'server'>('appearance');
    const [navigating, setNavigating] = useState(false);
    const isMobile = useIsMobile();
    const [theme, setTheme] = useState<Theme>(() => {
        const saved = resolveTheme(localStorage.getItem('theme'));
        if (saved) return saved;
        return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    });
    const [reviewLocation, setReviewLocation] = useState<ReviewLocation>(() => {
        const saved = localStorage.getItem('reviewLocation') as ReviewLocation;
        if (saved === 'local' || saved === 'github') return saved;
        return 'local';
    });
    const [diffFontSize, setDiffFontSize] = useState<DiffFontSize>(() => {
        const saved = localStorage.getItem('diffFontSize') as DiffFontSize;
        if (saved && VALID_DIFF_FONT_SIZES.includes(saved)) return saved;
        return DEFAULT_DIFF_FONT_SIZE;
    });

    // Apply theme
    useEffect(() => {
        document.documentElement.setAttribute('data-theme', theme);
        localStorage.setItem('theme', theme);
    }, [theme]);

    // Save review location
    useEffect(() => {
        localStorage.setItem('reviewLocation', reviewLocation);
    }, [reviewLocation]);

    // Publish the review diff font size as a CSS variable. The diff containers
    // read `--diff-font-size` and everything inside the diff sizes off it in
    // `em`, so the whole diff scales from this one value.
    useEffect(() => {
        document.documentElement.style.setProperty(
            '--diff-font-size',
            `${DIFF_FONT_SIZE_PX[diffFontSize]}px`
        );
        localStorage.setItem('diffFontSize', diffFontSize);
    }, [diffFontSize]);

    // Dynamic Title
    useEffect(() => {
        if (view === 'LIST') {
            document.title = 'Code Review';
        } else if (currentPR) {
            const prefix = view === 'REVIEW' ? 'Review' : 'Plugins';
            document.title = `${prefix} ${currentPR.owner}/${currentPR.repo}::${currentPR.number}`;
        }
    }, [view, currentPR]);

    // Initial load from URL
    useEffect(() => {
        const params = new URLSearchParams(window.location.search);
        const owner = params.get('owner');
        const repo = params.get('repo');
        const number = params.get('number');
        const viewParam = params.get('view');

        if (owner && repo && number) {
            setCurrentPR({ owner, repo, number: parseInt(number, 10) });
            if (viewParam === 'plugins') {
                setView('PLUGIN_OUTPUT');
            } else {
                setView('REVIEW');
            }
        }

        const handlePopState = () => {
            const newParams = new URLSearchParams(window.location.search);
            const newOwner = newParams.get('owner');
            const newRepo = newParams.get('repo');
            const newNumber = newParams.get('number');
            const newViewParam = newParams.get('view');

            if (newOwner && newRepo && newNumber) {
                setCurrentPR({ owner: newOwner, repo: newRepo, number: parseInt(newNumber, 10) });
                if (newViewParam === 'plugins') {
                    setView('PLUGIN_OUTPUT');
                } else {
                    setView('REVIEW');
                }
            } else {
                setView('LIST');
                setCurrentPR(null);
            }
        };

        window.addEventListener('popstate', handlePopState);
        return () => window.removeEventListener('popstate', handlePopState);
    }, []);

    const handleOpenReview = (owner: string, repo: string, number: number) => {
        const params = new URLSearchParams();
        params.set('owner', owner);
        params.set('repo', repo);
        params.set('number', number.toString());

        window.history.pushState({}, '', `?${params.toString()}`);

        setCurrentPR({ owner, repo, number });
        setView('REVIEW');
    };

    const handleOpenPluginOutput = (owner: string, repo: string, number: number) => {
        const params = new URLSearchParams();
        params.set('owner', owner);
        params.set('repo', repo);
        params.set('number', number.toString());
        params.set('view', 'plugins');

        window.history.pushState({}, '', `?${params.toString()}`);

        setCurrentPR({ owner, repo, number });
        setView('PLUGIN_OUTPUT');
    };

    const handleBack = () => {
        window.history.pushState({}, '', window.location.pathname);
        setView('LIST');
        setCurrentPR(null);
    };

    const handleNavigatePR = async (previous: boolean) => {
        if (!currentPR) return;
        setNavigating(true);
        try {
            const res = await rpcCall<{
                adjacent_owner: string;
                adjacent_repo: string;
                adjacent_number: number;
            }>('RPCHandler.GetAdjacentPR', [
                {
                    Owner: currentPR.owner,
                    Repo: currentPR.repo,
                    Number: currentPR.number,
                    Previous: previous,
                },
            ]);
            handleOpenReview(
                res.adjacent_owner || currentPR.owner,
                res.adjacent_repo || currentPR.repo,
                res.adjacent_number
            );
        } catch (e: any) {
            const msg = e?.message || String(e);
            const parsed = (() => {
                try {
                    return JSON.parse(msg);
                } catch {
                    return null;
                }
            })();
            alert(parsed?.message ?? msg);
        } finally {
            setNavigating(false);
        }
    };

    return (
        <div
            className="app-container"
            style={{ minHeight: '100vh', display: 'flex', flexDirection: 'column' }}
        >
            <header
                className="app-topbar"
                // Only the list pins its bar: the review view's own sticky
                // toolbar measures itself against the viewport top, so a pinned
                // bar there would cover it.
                style={{ position: view === 'LIST' ? 'sticky' : 'static', top: 0 }}
            >
                <div style={{ display: 'flex', alignItems: 'center', gap: '16px', minWidth: 0 }}>
                    <a
                        href="/"
                        onClick={e => {
                            // Only handle standard left-click without modifiers for SPA navigation
                            if (
                                e.button === 0 &&
                                !e.ctrlKey &&
                                !e.metaKey &&
                                !e.shiftKey &&
                                !e.altKey
                            ) {
                                e.preventDefault();
                                handleBack();
                            }
                        }}
                        style={{ textDecoration: 'none', color: 'inherit' }}
                    >
                        <h1
                            style={{
                                margin: 0,
                                fontSize: '18px',
                                fontWeight: 600,
                                cursor: 'pointer',
                                whiteSpace: 'nowrap',
                            }}
                        >
                            <span style={{ color: 'var(--accent)' }}>Code</span>Review
                        </h1>
                    </a>
                    {view === 'LIST' && !isMobile && <span className="app-tab">Pull Requests</span>}
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                    {view === 'REVIEW' && (
                        <>
                            <button
                                onClick={() => handleNavigatePR(true)}
                                disabled={navigating}
                                style={{
                                    background: 'transparent',
                                    border: '1px solid var(--border)',
                                    color: 'var(--text-secondary)',
                                    padding: '6px 12px',
                                    borderRadius: '6px',
                                    fontSize: '14px',
                                    cursor: navigating ? 'not-allowed' : 'pointer',
                                    opacity: navigating ? 0.5 : 1,
                                }}
                                title="Previous PR"
                            >
                                {isMobile ? '←' : '← Prev PR'}
                            </button>
                            <button
                                onClick={() => handleNavigatePR(false)}
                                disabled={navigating}
                                style={{
                                    background: 'transparent',
                                    border: '1px solid var(--border)',
                                    color: 'var(--text-secondary)',
                                    padding: '6px 12px',
                                    borderRadius: '6px',
                                    fontSize: '14px',
                                    cursor: navigating ? 'not-allowed' : 'pointer',
                                    opacity: navigating ? 0.5 : 1,
                                }}
                                title="Next PR"
                            >
                                {isMobile ? '→' : 'Next PR →'}
                            </button>
                        </>
                    )}
                    {(view === 'REVIEW' || view === 'PLUGIN_OUTPUT') && (
                        <button
                            onClick={handleBack}
                            style={{
                                background: 'transparent',
                                border: '1px solid var(--border)',
                                color: 'var(--text-secondary)',
                                padding: '6px 12px',
                                borderRadius: '6px',
                                fontSize: '14px',
                                cursor: 'pointer',
                            }}
                            title="Back to list"
                        >
                            {isMobile ? '☰ List' : '← Back to List'}
                        </button>
                    )}
                    <button
                        onClick={() => setShowPrefs(true)}
                        style={{
                            background: 'transparent',
                            border: 'none',
                            fontSize: '18px',
                            cursor: 'pointer',
                            color: 'var(--text-secondary)',
                            display: 'flex',
                            alignItems: 'center',
                            padding: '4px',
                            borderRadius: '4px',
                        }}
                        title="Preferences"
                    >
                        ⚙️
                    </button>
                </div>
            </header>

            <main
                style={{
                    flex: 1,
                    minWidth: 0,
                    padding: view === 'LIST' ? 0 : isMobile ? '12px' : '20px',
                }}
            >
                {view === 'LIST' && (
                    <PRList
                        onOpenReview={handleOpenReview}
                        onOpenPluginOutput={handleOpenPluginOutput}
                        theme={theme}
                        reviewLocation={reviewLocation}
                        onThemeChange={setTheme}
                    />
                )}
                {view === 'REVIEW' && currentPR && (
                    <Review
                        owner={currentPR.owner}
                        repo={currentPR.repo}
                        number={currentPR.number}
                        theme={theme}
                        onThemeChange={setTheme}
                    />
                )}
                {view === 'PLUGIN_OUTPUT' && currentPR && (
                    <PluginOutput
                        owner={currentPR.owner}
                        repo={currentPR.repo}
                        number={currentPR.number}
                        theme={theme}
                        onThemeChange={setTheme}
                        onClose={handleBack}
                    />
                )}
            </main>

            <Modal
                isOpen={showPrefs}
                onClose={() => setShowPrefs(false)}
                title="Preferences"
                size="lg"
            >
                <div
                    style={{
                        display: 'flex',
                        gap: '4px',
                        borderBottom: '1px solid var(--border)',
                        marginBottom: '16px',
                    }}
                >
                    {(
                        [
                            ['appearance', 'Appearance'],
                            ['server', 'Server Configuration'],
                        ] as const
                    ).map(([tab, label]) => (
                        <button
                            key={tab}
                            onClick={() => setPrefsTab(tab)}
                            style={{
                                background: 'transparent',
                                border: 'none',
                                borderBottom: `2px solid ${
                                    prefsTab === tab ? 'var(--accent)' : 'transparent'
                                }`,
                                color:
                                    prefsTab === tab
                                        ? 'var(--text-primary)'
                                        : 'var(--text-secondary)',
                                padding: '8px 12px',
                                fontSize: '14px',
                                fontWeight: prefsTab === tab ? 600 : 400,
                                cursor: 'pointer',
                            }}
                        >
                            {label}
                        </button>
                    ))}
                </div>

                {prefsTab === 'server' && <ConfigManager />}

                <div
                    style={{
                        display: prefsTab === 'appearance' ? 'flex' : 'none',
                        flexDirection: 'column',
                        gap: '20px',
                        padding: '10px 0',
                    }}
                >
                    <div>
                        <div
                            style={{
                                fontSize: '13px',
                                fontWeight: 600,
                                color: 'var(--text-secondary)',
                                marginBottom: '8px',
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                            }}
                        >
                            Theme
                        </div>
                        <Select
                            value={theme}
                            onChange={e => setTheme(e.target.value as Theme)}
                            options={THEME_OPTIONS}
                        />
                    </div>
                    <div>
                        <div
                            style={{
                                fontSize: '13px',
                                fontWeight: 600,
                                color: 'var(--text-secondary)',
                                marginBottom: '8px',
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                            }}
                        >
                            Preferred Review Location
                        </div>
                        <Select
                            value={reviewLocation}
                            onChange={e => setReviewLocation(e.target.value as ReviewLocation)}
                            options={REVIEW_LOCATION_OPTIONS}
                        />
                        <div
                            style={{
                                fontSize: '11px',
                                color: 'var(--text-tertiary)',
                                marginTop: '6px',
                            }}
                        >
                            Determines where to open PRs when clicking their title in the list.
                        </div>
                    </div>
                    <div>
                        <div
                            style={{
                                fontSize: '13px',
                                fontWeight: 600,
                                color: 'var(--text-secondary)',
                                marginBottom: '8px',
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                            }}
                        >
                            Review Diff Font Size
                        </div>
                        <Select
                            value={diffFontSize}
                            onChange={e => setDiffFontSize(e.target.value as DiffFontSize)}
                            options={DIFF_FONT_SIZE_OPTIONS}
                        />
                        <div
                            style={{
                                fontFamily: 'var(--font-mono)',
                                fontSize: `${DIFF_FONT_SIZE_PX[diffFontSize]}px`,
                                color: 'var(--text-secondary)',
                                background: 'var(--bg-tertiary)',
                                border: '1px solid var(--border)',
                                borderRadius: '4px',
                                padding: '6px 8px',
                                marginTop: '8px',
                                whiteSpace: 'pre',
                                overflowX: 'auto',
                            }}
                        >
                            {'+ func preview(size int) error {'}
                        </div>
                        <div
                            style={{
                                fontSize: '11px',
                                color: 'var(--text-tertiary)',
                                marginTop: '6px',
                            }}
                        >
                            Size of the diff text (and its line numbers) when reviewing a PR.
                        </div>
                    </div>
                </div>
                <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '20px' }}>
                    <button
                        onClick={() => setShowPrefs(false)}
                        style={{
                            padding: '8px 16px',
                            background: 'var(--accent)',
                            color: 'white',
                            border: 'none',
                            borderRadius: '6px',
                            cursor: 'pointer',
                            fontWeight: 500,
                        }}
                    >
                        Close
                    </button>
                </div>
            </Modal>
        </div>
    );
}

export default App;
