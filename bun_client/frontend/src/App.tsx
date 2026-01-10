import { useState, useEffect } from 'react';
import { rpcCall } from './api';
import PRList from './components/PRList';
import Review from './components/Review';
import PluginOutput from './components/PluginOutput';

interface PRParams {
    owner: string;
    repo: string;
    number: number;
}

function App() {
    const [view, setView] = useState<'LIST' | 'REVIEW' | 'PLUGIN_OUTPUT'>('LIST');
    const [currentPR, setCurrentPR] = useState<PRParams | null>(null);

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
                <h1 style={{ margin: 0, fontSize: '24px', fontWeight: 600 }}>
                    <span style={{ color: 'var(--accent)' }}>Code</span>Review
                </h1>
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
                {view === 'LIST' && <PRList onOpenReview={handleOpenReview} onOpenPluginOutput={handleOpenPluginOutput} />}
                {view === 'REVIEW' && currentPR && (
                    <Review owner={currentPR.owner} repo={currentPR.repo} number={currentPR.number} />
                )}
                {view === 'PLUGIN_OUTPUT' && currentPR && (
                    <PluginOutput owner={currentPR.owner} repo={currentPR.repo} number={currentPR.number} />
                )}
            </main>
        </div>
    );
}

export default App;

