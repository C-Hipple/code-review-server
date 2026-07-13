import { useState } from 'react';

export default function CopyUrlButton({ url }: { url: string }) {
    const [copied, setCopied] = useState(false);

    const handleCopy = () => {
        navigator.clipboard.writeText(url).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
        });
    };

    return (
        <button
            onClick={handleCopy}
            style={{
                background: 'transparent',
                color: copied ? 'var(--accent)' : 'var(--text-secondary)',
                border: '1px solid var(--border)',
                padding: '8px 16px',
                borderRadius: '6px',
                cursor: 'pointer',
                display: 'flex',
                alignItems: 'center',
                gap: '6px',
                fontSize: '13px',
            }}
        >
            <span>{copied ? '✓' : '⎘'}</span> {copied ? 'Copied!' : 'Copy URL'}
        </button>
    );
}
