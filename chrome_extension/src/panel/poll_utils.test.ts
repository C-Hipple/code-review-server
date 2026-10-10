import { describe, expect, test } from 'bun:test';
import type { AIFeatureOutput, PluginOutput } from '../types';
import { anythingPending, shouldPoll } from './poll_utils';

const ai = (status: string) => ({ status }) as AIFeatureOutput;
const plugin = (status: string) => ({ status }) as PluginOutput;

describe('polling decision', () => {
    test('nothing loaded, nothing requested: no polling', () => {
        expect(shouldPoll({ aiOutputs: null, pluginOutputs: null, justRequested: false })).toBe(
            false
        );
    });

    test('everything settled: no polling', () => {
        const aiOutputs = { a: ai('success'), b: ai('not-run'), c: ai('error') };
        const pluginOutputs = { p: plugin('success'), q: plugin('deferred') };
        expect(anythingPending(aiOutputs, pluginOutputs)).toBe(false);
        expect(shouldPoll({ aiOutputs, pluginOutputs, justRequested: false })).toBe(false);
    });

    test('a pending AI feature polls', () => {
        expect(anythingPending({ a: ai('success'), b: ai('pending') }, {})).toBe(true);
    });

    test('a pending plugin polls', () => {
        expect(anythingPending(null, { p: plugin('pending') })).toBe(true);
        expect(anythingPending(null, { p: plugin('PENDING') })).toBe(true);
    });

    test('a run just requested polls before anything reads pending', () => {
        expect(shouldPoll({ aiOutputs: {}, pluginOutputs: {}, justRequested: true })).toBe(true);
    });
});
