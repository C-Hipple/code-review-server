import { useState, useEffect } from 'react';
import { rpcCall } from './api';
import PRList from './components/PRList';
import Review from './components/Review';

interface PRParams {
    owner: string;
    repo: string;
    number: number;
}

function App() {
    const [view, setView] = useState<'LIST' | 'REVIEW'>('LIST');
    const [currentPR, setCurrentPR] = useState<PRParams | null>(null);

    const handleOpenReview = (owner: string, repo: string, number: number) => {
        setCurrentPR({ owner, repo, number });
        setView('REVIEW');
    };

    const handleBack = () => {
        setView('LIST');
        setCurrentPR(null);
    };

    return (
        <div className="app-container" style={{ minHeight: '100vh', padding: '20px' }}>
            <header style={{ marginBottom: '30px', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <h1 style={{ margin: 0, fontSize: '24px', fontWeight: 600 }}>
                    <span style={{ color: 'var(--accent)' }}>Code</span>Review
                </h1>
                {view === 'REVIEW' && (
                    <button
                        onClick={handleBack}
                        style={{
                            background: 'transparent',
                            border: '1px solid var(--border)',
                            color: 'var(--text-secondary)',
                            padding: '8px 16px',
                            borderRadius: '6px',
                            fontSize: '14px'
                        }}
                    >
                        ← Back to List
                    </button>
                )}
            </header>

            <main>
                {view === 'LIST' && <PRList onOpenReview={handleOpenReview} />}
                {view === 'REVIEW' && currentPR && (
                    <Review owner={currentPR.owner} repo={currentPR.repo} number={currentPR.number} />
                )}
            </main>
        </div>
    );
}

export default App;
