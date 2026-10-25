import { describe, expect, test } from 'bun:test';
import {
    adminOnlyMessage,
    rpcConnectionLost,
    rpcDraftsChanged,
    rpcErrorMessage,
    rpcRefusal,
} from './api';

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

describe('rpcConnectionLost', () => {
    const goError = (message: string) => new Error(JSON.stringify(message));

    test('is a call the bridge lost with its connection, not one it never sent', () => {
        expect(
            rpcConnectionLost(
                goError(
                    'connection lost: the connection to crs at crs.test:7700 was lost before it answered'
                )
            )
        ).toBe(true);
        expect(
            rpcConnectionLost(
                goError('The connection to crs at crs.test:7700 closed before the call was sent')
            )
        ).toBe(false);
        expect(rpcConnectionLost(goError("Can't reach crs at crs.test:7700: refused"))).toBe(false);
        expect(rpcConnectionLost(new TypeError('Failed to fetch'))).toBe(false);
    });
});

describe('rpcDraftsChanged', () => {
    test('is SubmitReview refusing a review whose previewed drafts are gone', () => {
        expect(
            rpcDraftsChanged(
                new Error(
                    JSON.stringify(
                        'drafts changed: some of the pending comments this review was to carry no longer exist'
                    )
                )
            )
        ).toBe(true);
        expect(rpcDraftsChanged(new Error(JSON.stringify('GitHub is down')))).toBe(false);
    });
});

describe('adminOnlyMessage', () => {
    test('names the action', () => {
        expect(adminOnlyMessage('rerun plugins')).toBe(
            'Only an administrator of this server can rerun plugins.'
        );
    });
});
