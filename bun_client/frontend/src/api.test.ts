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
        expect(
            rpcRefusal(
                goError(
                    "refused: this server posts to GitHub as its own account, so it does not submit reviews or merge on users' behalf; an administrator can enable AllowServerIdentityActions"
                )
            )
        ).toEqual({
            kind: 'refused',
            detail: "this server posts to GitHub as its own account, so it does not submit reviews or merge on users' behalf; an administrator can enable AllowServerIdentityActions",
        });
    });

    test('tells a refusal that linking a GitHub account answers from a final one', () => {
        for (const detail of [
            'link your GitHub account to submit reviews as yourself',
            'your GitHub authorization has expired or was revoked; link your account again',
            'this server can no longer read your stored GitHub authorization; link your account again',
        ]) {
            expect(rpcRefusal(goError(`refused: link-github: ${detail}`))).toEqual({
                kind: 'link-github',
                detail,
            });
        }
        // Merging as the server's account is an administrator's, whatever links.
        expect(
            rpcRefusal(
                goError(
                    "permission denied: RPCHandler.MergePR requires an administrator to merge as this server's GitHub account"
                )
            )?.kind
        ).toBe('permission-denied');
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
