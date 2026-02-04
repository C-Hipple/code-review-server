import { useState, useEffect } from 'react';
import { rpcCall } from './api';
import PRList from './components/PRList';
import Review from './components/Review';
import PluginOutput from './components/PluginOutput';
import { Modal, Select, Theme, VALID_THEMES, THEME_OPTIONS, ReviewLocation, REVIEW_LOCATION_OPTIONS } from './design';

interface PRParams {
    owner: string;
    repo: string;
    number: number;
}

function App() {
    const [view, setView] = useState<'LIST' | 'REVIEW' | 'PLUGIN_OUTPUT'>('LIST');
    const [currentPR, setCurrentPR] = useState<PRParams | null>(null);
    const [showPrefs, setShowPrefs] = useState(false);
    const [theme, setTheme] = useState<Theme>(() => {
        const saved = localStorage.getItem('theme') as Theme;
        if (saved && VALID_THEMES.includes(saved)) return saved;
        return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    });
    const [reviewLocation, setReviewLocation] = useState<ReviewLocation>(() => {
        const saved = localStorage.getItem('reviewLocation') as ReviewLocation;
        if (saved === 'local' || saved === 'github') return saved;
        return 'local';
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

    return (
        <div className="app-container" style={{ minHeight: '100vh', padding: '20px' }}>
            <header style={{ marginBottom: '30px', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
                    <h1
                        onClick={handleBack}
                        style={{ margin: 0, fontSize: '24px', fontWeight: 600, cursor: 'pointer' }}
                    >
                        <span style={{ color: 'var(--accent)' }}>Code</span>Review
                    </h1>
                    <button
                        onClick={() => setShowPrefs(true)}
                        style={{
                            background: 'transparent',
                            border: 'none',
                            fontSize: '20px',
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
                {(view === 'REVIEW' || view === 'PLUGIN_OUTPUT') && (
                    <button
                        onClick={handleBack}
                        style={{
                            background: 'transparent',
                            border: '1px solid var(--border)',
                            color: 'var(--text-secondary)',
                            padding: '8px 16px',
                            borderRadius: '6px',
                            fontSize: '14px',
                            cursor: 'pointer'
                        }}
                    >
                        ← Back to List
                    </button>
                )}
            </header>

            <main>
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
                size="sm"
            >
                <div style={{ display: 'flex', flexDirection: 'column', gap: '20px', padding: '10px 0' }}>
                    <div>
                        <div style={{ fontSize: '13px', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '8px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                            Theme
                        </div>
                        <Select
                            value={theme}
                            onChange={e => setTheme(e.target.value as Theme)}
                            options={THEME_OPTIONS}
                        />
                    </div>
                    <div>
                        <div style={{ fontSize: '13px', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '8px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                            Preferred Review Location
                        </div>
                        <Select
                            value={reviewLocation}
                            onChange={e => setReviewLocation(e.target.value as ReviewLocation)}
                            options={REVIEW_LOCATION_OPTIONS}
                        />
                        <div style={{ fontSize: '11px', color: 'var(--text-tertiary)', marginTop: '6px' }}>
                            Determines where to open PRs when clicking their title in the list.
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
                            fontWeight: 500
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

