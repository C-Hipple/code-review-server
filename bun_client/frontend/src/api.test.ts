import { describe, expect, test } from 'bun:test';
import { adminOnlyMessage, rpcErrorMessage, rpcRefusal } from './api';

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

describe('rpcRefusal', () => {
    const goError = (message: string) => new Error(JSON.stringify(message));

    test('reads a shared server’s refusals off their prefix', () => {
        expect(
            rpcRefusal(goError('permission denied: RPCHandler.GetConfig requires an administrator'))
        ).toEqual({
            kind: 'permission-denied',
            detail: 'RPCHandler.GetConfig requires an administrator',
        });
        expect(rpcRefusal(goError('refused: SubmitReview would post as the server'))).toEqual({
            kind: 'refused',
            detail: 'SubmitReview would post as the server',
        });
    });

    test('is null for any other error', () => {
        expect(rpcRefusal(goError('GitHub is down'))).toBeNull();
        expect(rpcRefusal(goError('not authenticated: no session'))).toBeNull();
        expect(rpcRefusal(goError('the reviewer refused: nothing to see'))).toBeNull();
        expect(rpcRefusal(new TypeError('Failed to fetch'))).toBeNull();
    });
});

describe('adminOnlyMessage', () => {
    test('names the action', () => {
        expect(adminOnlyMessage('rerun plugins')).toBe(
            'Only an administrator of this server can rerun plugins.'
        );
    });
});
