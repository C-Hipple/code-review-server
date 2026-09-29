import { describe, expect, test } from 'bun:test';
import { rpcErrorMessage } from './api';

describe('rpcErrorMessage', () => {
    test('unwraps the JSON-encoded string the Go server sends', () => {
        expect(rpcErrorMessage(new Error(JSON.stringify('GitHub said no')))).toBe('GitHub said no');
    });

    test('reads the message off an error object', () => {
        expect(rpcErrorMessage(new Error(JSON.stringify({ message: 'PR not found' })))).toBe(
            'PR not found'
        );
    });

    test('passes anything else through as it is', () => {
        expect(rpcErrorMessage(new TypeError('Failed to fetch'))).toBe('Failed to fetch');
        expect(rpcErrorMessage(new Error(JSON.stringify({ code: 7 })))).toBe('{"code":7}');
        expect(rpcErrorMessage('plain')).toBe('plain');
    });
});
