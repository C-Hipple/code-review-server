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

/** A request for `key` has started: busy, with its last error and note cleared. */
export function begin(state: ActionState, key: string): ActionState {
    const { [key]: _error, ...errors } = state.errors;
    const { [key]: _note, ...notes } = state.notes;
    return { busy: { ...state.busy, [key]: true }, errors, notes };
}

/** The request for `key` was accepted; `note` says what it did, if worth saying. */
export function finish(state: ActionState, key: string, note?: string): ActionState {
    const { [key]: _busy, ...busy } = state.busy;
    const notes = note ? { ...state.notes, [key]: note } : state.notes;
    return { ...state, busy, notes };
}

/** The request for `key` failed. */
export function fail(state: ActionState, key: string, error: RpcError): ActionState {
    const { [key]: _busy, ...busy } = state.busy;
    return { ...state, busy, errors: { ...state.errors, [key]: error } };
}
