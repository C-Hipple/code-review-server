import { useEffect, useState } from 'react';
import { QRCodeSVG } from 'qrcode.react';
import { API_BASE } from '../api';
import { Select } from '../design';
import { deviceLinks, type DeviceLink, type NetworkInfo } from '../network_utils';

/**
 * Preferences' "Other Devices" tab: a QR code that opens this page on a phone
 * on the same Wi-Fi, at an address the bridge says it can be reached at.
 */
export default function DeviceQRCode() {
    const [info, setInfo] = useState<NetworkInfo | null>(null);
    const [failed, setFailed] = useState(false);

    useEffect(() => {
        let cancelled = false;
        fetch(`${API_BASE}/api/network`)
            .then(res => {
                if (!res.ok) throw new Error(`HTTP ${res.status}`);
                return res.json() as Promise<NetworkInfo>;
            })
            .then(reply => {
                if (!cancelled) setInfo(reply);
            })
            .catch(() => {
                // A bridge from before /api/network answers with index.html.
                if (!cancelled) setFailed(true);
            });
        return () => {
            cancelled = true;
        };
    }, []);

    return (
        <DeviceQRCodeView
            links={deviceLinks(window.location, info)}
            loading={!info && !failed}
            failed={failed}
        />
    );
}

const sectionLabel = {
    fontSize: '13px',
    fontWeight: 600,
    color: 'var(--text-secondary)',
    marginBottom: '8px',
    textTransform: 'uppercase',
    letterSpacing: '0.5px',
} as const;

export function DeviceQRCodeView({
    links,
    loading,
    failed,
}: {
    links: DeviceLink[];
    loading: boolean;
    failed: boolean;
}) {
    const [selectedUrl, setSelectedUrl] = useState<string | null>(null);
    // By URL rather than index, so the choice survives the list growing once
    // the bridge's addresses arrive.
    const link = links.find(l => l.url === selectedUrl) ?? links[0];

    if (loading && !link) {
        return (
            <div style={{ padding: '10px 0', color: 'var(--text-secondary)' }}>
                Looking up this machine's network address…
            </div>
        );
    }
    if (!link) {
        return (
            <div style={{ padding: '10px 0', color: 'var(--text-secondary)', fontSize: '13px' }}>
                {failed
                    ? "Couldn't ask the server for its network address."
                    : "No network address to offer: this machine isn't on a network, or the server is bound to this machine only (CRS_HOST)."}
            </div>
        );
    }

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '16px', padding: '10px 0' }}>
            <div style={{ fontSize: '13px', color: 'var(--text-secondary)' }}>
                Scan with a phone or tablet on the same Wi-Fi network to open this page on it.
            </div>
            <div
                style={{ display: 'flex', flexWrap: 'wrap', gap: '20px', alignItems: 'flex-start' }}
            >
                {/* Dark on white whatever the theme: scanners want the contrast. */}
                <div
                    style={{
                        background: '#ffffff',
                        padding: '12px',
                        borderRadius: '8px',
                        border: '1px solid var(--border)',
                        lineHeight: 0,
                    }}
                >
                    <QRCodeSVG
                        value={link.url}
                        size={192}
                        level="M"
                        bgColor="#ffffff"
                        fgColor="#000000"
                        title={`QR code for ${link.url}`}
                    />
                </div>
                <div
                    style={{
                        flex: '1 1 220px',
                        minWidth: 0,
                        display: 'flex',
                        flexDirection: 'column',
                        gap: '16px',
                    }}
                >
                    {links.length > 1 && (
                        <Select
                            label="Address"
                            value={link.url}
                            onChange={e => setSelectedUrl(e.target.value)}
                            options={links.map(l => ({ value: l.url, label: l.label }))}
                        />
                    )}
                    <div>
                        <div style={sectionLabel}>Link</div>
                        <a
                            href={link.url}
                            style={{
                                fontFamily: 'var(--font-mono)',
                                fontSize: '13px',
                                color: 'var(--accent)',
                                wordBreak: 'break-all',
                            }}
                        >
                            {link.url}
                        </a>
                    </div>
                    <div style={{ fontSize: '11px', color: 'var(--text-tertiary)' }}>
                        The server has no login, so anyone on this network can use it at this
                        address. Start it with CRS_HOST=127.0.0.1 to keep it to this machine.
                    </div>
                </div>
            </div>
        </div>
    );
}
