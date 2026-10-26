import { describe, expect, test } from 'bun:test';
import { changeFor, isChangeFor, parseServerEvent, toServerEvent } from './server_events';

const PR = { owner: 'acme', repo: 'widgets', number: 42 };

describe('toServerEvent', () => {
    test('a plugin output change', () => {
        expect(
            toServerEvent({
                method: 'Server.PluginOutputChanged',
                params: [{ ...PR, plugin: 'lint', status: 'success' }],
            })
        ).toEqual({
            type: 'plugin-output-changed',
            change: { ...PR, plugin: 'lint', status: 'success' },
        });
        // A rerun of them all clears every result at once.
        expect(
            toServerEvent({ method: 'Server.PluginOutputChanged', params: [{ ...PR }] })
        ).toEqual({ type: 'plugin-output-changed', change: { ...PR, plugin: '', status: '' } });
    });

    test('an AI output change', () => {
        expect(
            toServerEvent({
                method: 'Server.AIOutputChanged',
                params: [{ ...PR, feature: 'file-ordering', status: 'pending' }],
            })
        ).toEqual({
            type: 'ai-output-changed',
            change: { ...PR, feature: 'file-ordering', status: 'pending' },
        });
        expect(toServerEvent({ method: 'Server.AIOutputChanged', params: [PR] })).toBeNull();
    });

    test('nothing for the heartbeat, an unknown method or a malformed change', () => {
        expect(toServerEvent({ method: 'Server.Heartbeat', params: [{ seq: 1 }] })).toBeNull();
        expect(toServerEvent({ method: 'Server.Other', params: [PR] })).toBeNull();
        expect(toServerEvent({ method: 'Server.PluginOutputChanged', params: [] })).toBeNull();
        expect(
            toServerEvent({
                method: 'Server.PluginOutputChanged',
                params: [{ ...PR, number: '42' }],
            })
        ).toBeNull();
    });
});

describe('changeFor', () => {
    test("routes an event about the page's PR, whatever the case of its names", () => {
        const other = { ...PR, number: 43 };
        const plugins = { ...PR, owner: 'Acme', plugin: 'lint', status: 'success' };
        expect(changeFor({ type: 'plugin-output-changed', change: plugins }, PR)).toEqual({
            kind: 'plugins',
        });
        expect(
            changeFor(
                {
                    type: 'ai-output-changed',
                    change: { ...PR, feature: 'comments-addressed', status: 'success' },
                },
                PR
            )
        ).toEqual({ kind: 'ai', feature: 'comments-addressed' });
        expect(changeFor({ type: 'plugin-output-changed', change: plugins }, other)).toBeNull();
        expect(changeFor({ type: 'resync' }, other)).toEqual({ kind: 'resync' });
    });

    test('isChangeFor needs the same number, owner and repo', () => {
        expect(isChangeFor({ ...PR, repo: 'gadgets' }, PR)).toBe(false);
        expect(isChangeFor({ ...PR, owner: 'other' }, PR)).toBe(false);
        expect(isChangeFor({ owner: 'ACME', repo: 'Widgets', number: 42 }, PR)).toBe(true);
    });
});

describe('parseServerEvent', () => {
    test('reads what the service worker posts, and nothing else', () => {
        expect(parseServerEvent({ type: 'resync' })).toEqual({ type: 'resync' });
        const change = { ...PR, plugin: 'lint', status: 'pending' };
        expect(parseServerEvent({ type: 'plugin-output-changed', change })).toEqual({
            type: 'plugin-output-changed',
            change,
        });
        expect(parseServerEvent({ type: 'ai-output-changed' })).toBeNull();
        expect(parseServerEvent('resync')).toBeNull();
        expect(parseServerEvent(null)).toBeNull();
    });
});
