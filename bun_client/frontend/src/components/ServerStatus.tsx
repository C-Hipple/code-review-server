import type { CSSProperties } from 'react';
import { heartbeatDeadlineMs, type HeartbeatState } from '../server_events';

interface ServerStatusProps extends HeartbeatState {
    /** Just the dot, for a narrow top bar. */
    compact?: boolean;
}

function dot(color: string): CSSProperties {
    return {
        display: 'inline-block',
        width: '8px',
        height: '8px',
        borderRadius: '50%',
        background: color,
        flexShrink: 0,
    };
}

/**
 * The top bar's word on the server, from its heartbeats: a quiet dot while
 * they arrive, a warning once they stop. Nothing until the first, so a server
 * that sends none (one from before heartbeats) isn't reported as down. The
 * wrapper stays mounted so screen readers hear the change.
 */
export default function ServerStatus({ connection, heartbeat, compact }: ServerStatusProps) {
    let shown = null;
    if (connection === 'live') {
        const every = heartbeat ? ` (heartbeat every ${heartbeat.interval_seconds} s)` : '';
        shown = (
            <span
                role="img"
                aria-label="Server connected"
                title={`Connected to the crs server${every}`}
                style={{ display: 'inline-flex', alignItems: 'center', padding: '4px' }}
            >
                <span style={{ ...dot('var(--text-success)'), opacity: 0.8 }} />
            </span>
        );
    } else if (connection === 'lost') {
        const silentFor = heartbeat ? ` for over ${heartbeatDeadlineMs(heartbeat) / 1000} s` : '';
        shown = (
            <span
                role="img"
                aria-label="Server not responding"
                title={`No heartbeat from the crs server${silentFor}. It may have exited or hung; check the terminal running the bridge.`}
                style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: '6px',
                    padding: compact ? '4px' : '4px 10px',
                    borderRadius: '999px',
                    border: '1px solid var(--border-danger-dim)',
                    background: 'var(--bg-danger-dim)',
                    color: 'var(--text-danger)',
                    fontSize: '12px',
                    fontWeight: 500,
                    whiteSpace: 'nowrap',
                }}
            >
                <span style={dot('var(--text-danger)')} />
                {!compact && 'Server not responding'}
            </span>
        );
    }
    return (
        <span aria-live="polite" style={{ display: 'inline-flex' }}>
            {shown}
        </span>
    );
}
