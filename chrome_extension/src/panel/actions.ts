// Per-button state in the PR tools view: which runs are being requested,
// and what the last request for each said.

import type { RpcError } from '../rpc';

export interface ActionState {
    /** Requests in flight, by key. */
    busy: Readonly<Record<string, boolean>>;
    /** The last request's failure, by key. */
    errors: Readonly<Record<string, RpcError>>;
    /** The last request's outcome, when worth saying, by key. */
    notes: Readonly<Record<string, string>>;
}

export const NO_ACTIONS: ActionState = { busy: {}, errors: {}, notes: {} };

export const aiKey = (feature: string) => `ai:${feature}`;
export const pluginKey = (name: string) => `plugin:${name}`;
export const ALL_PLUGINS = 'plugins:all';
export const SYNC = 'sync';
