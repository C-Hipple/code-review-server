// The server's change events, as the service worker relays them to pages.
// Pure — no chrome.* — so it is unit tested directly.
//
// Once subscribed (native_client.ts), the server sends a notification each
// time a PR's plugin output or AI results change. Only those two reach pages;
// the heartbeat and any method this extension doesn't know stay in the
// service worker.

import type { PullRef } from './github_url';
import type { JsonRpcNotification } from './native_protocol';
import type { AIOutputChanged, PluginOutputChanged, ServerEventMessage } from './types';

export const PLUGIN_OUTPUT_CHANGED = 'Server.PluginOutputChanged';
export const AI_OUTPUT_CHANGED = 'Server.AIOutputChanged';

/** The page message for a notification, or null for one pages don't hear. */
export function toServerEvent(notification: JsonRpcNotification): ServerEventMessage | null {
    const data = notification.params[0];
    if (typeof data !== 'object' || data === null) return null;
    const fields = data as Record<string, unknown>;
    const { owner, repo, number } = fields;
    if (typeof owner !== 'string' || typeof repo !== 'string' || typeof number !== 'number') {
        return null;
    }
    const status = typeof fields.status === 'string' ? fields.status : '';
    switch (notification.method) {
        case PLUGIN_OUTPUT_CHANGED: {
            const plugin = typeof fields.plugin === 'string' ? fields.plugin : '';
            const change: PluginOutputChanged = { owner, repo, number, plugin, status };
            return { type: 'plugin-output-changed', change };
        }
        case AI_OUTPUT_CHANGED: {
            if (typeof fields.feature !== 'string') return null;
            const change: AIOutputChanged = {
                owner,
                repo,
                number,
                feature: fields.feature,
                status,
            };
            return { type: 'ai-output-changed', change };
        }
        default:
            return null;
    }
}

/** Whether `change` is about `pr`. GitHub names ignore case. */
export function isChangeFor(change: PullRef, pr: PullRef): boolean {
    return (
        change.number === pr.number &&
        change.owner.toLowerCase() === pr.owner.toLowerCase() &&
        change.repo.toLowerCase() === pr.repo.toLowerCase()
    );
}

/** Reads one message off an events port, or null for anything else. */
export function parseServerEvent(message: unknown): ServerEventMessage | null {
    if (typeof message !== 'object' || message === null) return null;
    const msg = message as Record<string, unknown>;
    if (msg.type === 'resync') return { type: 'resync' };
    if (
        (msg.type === 'plugin-output-changed' || msg.type === 'ai-output-changed') &&
        typeof msg.change === 'object' &&
        msg.change !== null
    ) {
        return msg as unknown as ServerEventMessage;
    }
    return null;
}

/** What a page showing `pr` does with one event: read which output again, if any. */
export type PRChange = { kind: 'plugins' } | { kind: 'ai'; feature: string } | { kind: 'resync' };

export function changeFor(event: ServerEventMessage, pr: PullRef): PRChange | null {
    switch (event.type) {
        case 'resync':
            return { kind: 'resync' };
        case 'plugin-output-changed':
            return isChangeFor(event.change, pr) ? { kind: 'plugins' } : null;
        case 'ai-output-changed':
            return isChangeFor(event.change, pr)
                ? { kind: 'ai', feature: event.change.feature }
                : null;
    }
}
