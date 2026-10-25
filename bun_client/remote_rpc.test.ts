import { afterEach, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Socket, TCPSocketListener } from 'bun';
import {
    CONNECTION_LOST,
    describeProblem,
    parseRemoteAddress,
    RemoteRpcClient,
    type RemoteRpcOptions,
    type RemoteSettings,
    readUserConfig,
    remoteSettingsFromEnv,
    tokenFileName,
} from './remote_rpc';

// biome-ignore lint/suspicious/noExplicitAny: decoded JSON-RPC arguments
type Args = any;

const TOKEN_A = `crs_${'a'.repeat(64)}`;
const TOKEN_B = `crs_${'b'.repeat(64)}`;

const USER_TOML = `GithubUsername = "octocat"
Repos = ["acme/widgets"]
SleepDuration = 5

[SectionPriority]
"Needs Review" = 1

[SectionSorting]
"Needs Review" = "oldest_first"

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Reviews"
SectionTitle = "Needs Review"
Filters = ["FilterNotDraft"]
`;

const USER_PAYLOAD = {
    Workflows: [
        {
            WorkflowType: 'SyncReviewRequestsWorkflow',
            Name: 'Reviews',
            SectionTitle: 'Needs Review',
            Filters: ['FilterNotDraft'],
        },
    ],
    Repos: ['acme/widgets'],
    SectionPriority: { 'Needs Review': 1 },
    SectionSorting: { 'Needs Review': 'oldest_first' },
};

interface Session {
    buffer: string;
    login: string | null;
}

type Handler = (args: Args, session: Session) => unknown;

/**
 * A stand-in for `crs --listen`: Authenticate / RegisterUser per the contract,
 * and `Test.Echo`, which needs an authenticated session. `handlers` overrides
 * or adds methods; one returning `undefined` leaves the call unanswered.
 */
function fakeServer(handlers: Record<string, Handler> = {}) {
    const users = new Map<string, { login: string; token: string }>();
    const received: { method: string; params: Args }[] = [];
    const sockets = new Set<Socket<Session>>();
    let minted = 0;

    const builtins: Record<string, Handler> = {
        'RPCHandler.Authenticate': (args, session) => {
            const user = [...users.values()].find(u => u.token === args.Token);
            if (!user)
                return { okay: false, known: false, login: '', is_admin: false, message: '' };
            session.login = user.login;
            return { okay: true, known: true, login: user.login, is_admin: false, message: '' };
        },
        'RPCHandler.RegisterUser': (args, session) => {
            const known = users.get(args.Login);
            if (known && known.token !== args.Token) {
                return { okay: false, message: 'login already registered', errors: [] };
            }
            const token = known?.token || args.Token || `crs_${String(++minted).padStart(64, '0')}`;
            users.set(args.Login, { login: args.Login, token });
            session.login = args.Login;
            return {
                okay: true,
                created: !known,
                login: args.Login,
                token,
                is_admin: false,
                message: '',
                errors: [],
            };
        },
        'Test.Echo': (args, session) => {
            if (session.login === null) throw 'not authenticated: Test.Echo needs a session';
            return { echo: args, login: session.login };
        },
    };
    const all = { ...builtins, ...handlers };

    const listener: TCPSocketListener<Session> = Bun.listen<Session>({
        hostname: '127.0.0.1',
        port: 0,
        socket: {
            open(socket) {
                socket.data = { buffer: '', login: null };
                sockets.add(socket);
            },
            data(socket, chunk) {
                socket.data.buffer += chunk.toString();
                let nl = socket.data.buffer.indexOf('\n');
                while (nl !== -1) {
                    const line = socket.data.buffer.slice(0, nl);
                    socket.data.buffer = socket.data.buffer.slice(nl + 1);
                    nl = socket.data.buffer.indexOf('\n');
                    const req = JSON.parse(line);
                    const args = req.params?.[0] ?? {};
                    received.push({ method: req.method, params: args });
                    const handler = all[req.method];
                    let reply: { result: unknown; error: unknown };
                    try {
                        if (!handler) throw `rpc: can't find method ${req.method}`;
                        const result = handler(args, socket.data);
                        if (result === undefined) continue;
                        reply = { result, error: null };
                    } catch (e) {
                        reply = { result: null, error: String(e) };
                    }
                    socket.write(`${JSON.stringify({ id: req.id, ...reply })}\n`);
                }
            },
            close(socket) {
                sockets.delete(socket);
            },
        },
    });

    return {
        port: listener.port,
        users,
        received,
        methods: () => received.map(r => r.method),
        /** Forget every session's authentication, as when the server drops the user. */
        deauthenticate() {
            for (const socket of sockets) socket.data.login = null;
        },
        /** Close every connection from the server's side. */
        drop() {
            for (const socket of sockets) socket.end();
        },
        stop() {
            listener.stop(true);
        },
    };
}

const cleanups: (() => void)[] = [];
afterEach(() => {
    for (const cleanup of cleanups.splice(0).reverse()) cleanup();
});

function setup(
    handlers: Record<string, Handler> = {},
    overrides: Partial<RemoteSettings> = {},
    toml: string | null = USER_TOML,
    options: RemoteRpcOptions = {}
) {
    const server = fakeServer(handlers);
    const dir = mkdtempSync(join(tmpdir(), 'crs-remote-test-'));
    const configPath = join(dir, 'codereviewserver.toml');
    if (toml !== null) writeFileSync(configPath, toml);
    const settings: RemoteSettings = {
        address: `127.0.0.1:${server.port}`,
        hostname: '127.0.0.1',
        port: server.port,
        tls: false,
        envToken: '',
        tokenFile: join(dir, 'crs', 'remote_tokens', `127.0.0.1_${server.port}`),
        login: '',
        registrationKey: 'sesame',
        configPath,
        ...overrides,
    };
    const logs: string[] = [];
    const connect = () => {
        const client = new RemoteRpcClient(settings, {
            initialBackoffMs: 20,
            maxBackoffMs: 100,
            connectTimeoutMs: 2000,
            log: m => logs.push(m),
            logError: m => logs.push(m),
            ...options,
        });
        cleanups.push(() => client.close());
        return client;
    };
    cleanups.push(() => {
        server.stop();
        rmSync(dir, { recursive: true, force: true });
    });
    return { server, settings, logs, connect, dir };
}

const readToken = (settings: RemoteSettings) => readFileSync(settings.tokenFile, 'utf-8').trim();

describe('RemoteRpcClient connect flow', () => {
    test('registers on the first connect with the local workflows and saves the token', async () => {
        const { server, settings, connect } = setup();
        const client = connect();

        expect(await client.call('Test.Echo', [{ n: 1 }])).toEqual({
            echo: { n: 1 },
            login: 'octocat',
        });
        // No token yet, so no Authenticate: straight to registering.
        expect(server.methods()).toEqual(['RPCHandler.RegisterUser', 'Test.Echo']);
        expect(server.received[0].params).toEqual({
            RegistrationKey: 'sesame',
            Login: 'octocat',
            Token: '',
            ...USER_PAYLOAD,
        });
        const token = server.users.get('octocat')?.token ?? '';
        expect(token).toStartWith('crs_');
        expect(readToken(settings)).toBe(token);
        expect(statSync(settings.tokenFile).mode & 0o777).toBe(0o600);
    });

    test('authenticates with a stored token without registering', async () => {
        const { server, settings, connect } = setup();
        server.users.set('octocat', { login: 'octocat', token: TOKEN_A });
        await Bun.write(settings.tokenFile, `${TOKEN_A}\n`);

        await connect().call('Test.Echo', [{}]);
        expect(server.methods()).toEqual(['RPCHandler.Authenticate', 'Test.Echo']);
        expect(server.received[0].params).toEqual({ Token: TOKEN_A });
    });

    test('registers again with the same token once the server has forgotten it', async () => {
        const { server, settings, connect } = setup();
        await Bun.write(settings.tokenFile, `${TOKEN_A}\n`);

        await connect().call('Test.Echo', [{}]);
        expect(server.methods()).toEqual([
            'RPCHandler.Authenticate',
            'RPCHandler.RegisterUser',
            'Test.Echo',
        ]);
        expect(server.received[1].params).toMatchObject({ Login: 'octocat', Token: TOKEN_A });
        expect(server.users.get('octocat')?.token).toBe(TOKEN_A);
        expect(readToken(settings)).toBe(TOKEN_A);
    });

    test('CRS_REMOTE_TOKEN and CRS_LOGIN win over the token file and the TOML', async () => {
        const { server, settings, connect } = setup({}, { envToken: TOKEN_B, login: 'hubot' });
        await Bun.write(settings.tokenFile, `${TOKEN_A}\n`);

        await connect().call('Test.Echo', [{}]);
        expect(server.received[0].params).toEqual({ Token: TOKEN_B });
        expect(server.received[1].params).toMatchObject({ Login: 'hubot', Token: TOKEN_B });
    });

    test('keeps a token per server, and never sends one server’s to another', async () => {
        const { server, settings, connect, dir } = setup();
        // Another server's token, kept the way this server's would be.
        const elsewhere = join(dir, 'crs', 'remote_tokens', 'crs.other_7700');
        await Bun.write(elsewhere, `${TOKEN_B}\n`);

        await connect().call('Test.Echo', [{}]);
        // This server had none of its own: it registered, with no token.
        expect(server.received[0]).toEqual({
            method: 'RPCHandler.RegisterUser',
            params: expect.objectContaining({ Token: '' }),
        });
        expect(readToken(settings)).toBe(server.users.get('octocat')?.token ?? '');
        expect(server.received.some(r => JSON.stringify(r.params).includes(TOKEN_B))).toBe(false);
        expect(readFileSync(elsewhere, 'utf-8').trim()).toBe(TOKEN_B);
    });

    test('says what the server said of the registration', async () => {
        const { connect, logs } = setup({
            'RPCHandler.RegisterUser': (args, session) => {
                session.login = args.Login;
                return {
                    okay: true,
                    created: true,
                    login: args.Login,
                    token: TOKEN_A,
                    is_admin: false,
                    message: `Registered ${args.Login} with 1 workflow; the dashboard follows on the next workflow cycle`,
                    errors: [],
                };
            },
        });
        await connect().call('Test.Echo', [{}]);
        expect(logs.find(l => l.startsWith('Registered with'))).toEndWith(
            '. Registered octocat with 1 workflow; the dashboard follows on the next workflow cycle'
        );
    });

    test('queues calls made while connecting and sends them in order', async () => {
        const { server, connect } = setup();
        const client = connect();
        const replies = await Promise.all([1, 2, 3].map(n => client.call('Test.Echo', [{ n }])));
        expect(replies.map(r => (r as { echo: { n: number } }).echo.n)).toEqual([1, 2, 3]);
        expect(server.methods()).toEqual([
            'RPCHandler.RegisterUser',
            'Test.Echo',
            'Test.Echo',
            'Test.Echo',
        ]);
    });
});

describe('RemoteRpcClient session', () => {
    test('describes who the connect flow signed in as, the GitHub link read fresh', async () => {
        let linked = false;
        const { server, settings, connect } = setup({
            'RPCHandler.Authenticate': (args, session) => {
                session.login = 'octocat';
                return {
                    okay: args.Token === TOKEN_A,
                    known: true,
                    login: 'octocat',
                    is_admin: true,
                    message: '',
                    github_linked: false,
                    github_link_available: true,
                };
            },
            'RPCHandler.GetMyWorkflows': () => ({
                okay: true,
                known: true,
                login: 'octocat',
                github_linked: linked,
                github_link_available: true,
            }),
        });
        await Bun.write(settings.tokenFile, `${TOKEN_A}\n`);
        const client = connect();

        expect(await client.describeSession()).toEqual({
            remote: true,
            address: settings.address,
            connected: true,
            login: 'octocat',
            is_admin: true,
            github_linked: false,
            github_link_available: true,
        });

        // Linked since the flow ran, from this UI or another client.
        linked = true;
        expect(await client.describeSession()).toMatchObject({ github_linked: true });
        expect(server.methods()).toEqual([
            'RPCHandler.Authenticate',
            'RPCHandler.GetMyWorkflows',
            'RPCHandler.GetMyWorkflows',
        ]);
    });

    test('a registration signs in as the login the server registered', async () => {
        const { connect } = setup({
            'RPCHandler.GetMyWorkflows': () => ({ okay: true, known: true }),
        });
        expect(await connect().describeSession()).toMatchObject({
            remote: true,
            login: 'octocat',
            is_admin: false,
            github_linked: false,
            github_link_available: false,
        });
    });

    test('says it is not connected until the connect flow has signed in', async () => {
        const { connect } = setup({}, { registrationKey: '' });
        const session = await connect().describeSession();
        expect(session).toMatchObject({ remote: true, connected: false });
        expect(session.login).toBeUndefined();
        expect(session.error).toContain('set CRS_REGISTRATION_KEY');
    });

    test('signs in as nobody while it cannot connect', async () => {
        const { server, connect } = setup();
        const client = connect();
        expect(await client.describeSession()).toMatchObject({ connected: true });

        server.stop();
        server.drop();
        // The reconnect fails, and with it who the bridge was signed in as.
        let session = await client.describeSession();
        for (let i = 0; i < 50 && session.connected; i++) {
            await Bun.sleep(20);
            session = await client.describeSession();
        }
        expect(session).toMatchObject({ remote: true, connected: false });
        expect(session.login).toBeUndefined();
        expect(session.error).toStartWith("Can't reach crs at");
    });

    test('says why it could not read the session, with what it last knew', async () => {
        const { connect } = setup({
            'RPCHandler.GetMyWorkflows': () => {
                throw 'could not check this connection’s user: database is locked';
            },
        });
        const session = await connect().describeSession();
        expect(session).toMatchObject({
            remote: true,
            login: 'octocat',
            error: 'could not check this connection’s user: database is locked',
        });
    });
});

describe('RemoteRpcClient recovery', () => {
    test('a call refused as not authenticated re-runs the flow and is retried once', async () => {
        const { server, connect } = setup();
        const client = connect();
        await client.call('Test.Echo', [{}]);

        // The server forgets the user and the session with it.
        server.users.clear();
        server.deauthenticate();
        const replies = await Promise.all([
            client.call('Test.Echo', [{ n: 1 }]),
            client.call('Test.Echo', [{ n: 2 }]),
        ]);
        expect(replies).toHaveLength(2);
        // Both refusals wait for one connect flow, which re-registers with
        // the token the first registration saved.
        expect(server.methods().slice(2)).toEqual([
            'Test.Echo',
            'Test.Echo',
            'RPCHandler.Authenticate',
            'RPCHandler.RegisterUser',
            'Test.Echo',
            'Test.Echo',
        ]);
    });

    test('a second not-authenticated refusal is the call’s error, not another loop', async () => {
        const { server, connect } = setup({
            'Test.Refuse': () => {
                throw 'not authenticated: still no';
            },
        });
        const client = connect();
        await expect(client.call('Test.Refuse', [{}])).rejects.toBe('not authenticated: still no');
        expect(server.methods().filter(m => m === 'Test.Refuse')).toHaveLength(2);
    });

    test('a dropped connection fails the calls in flight, then reconnects', async () => {
        const { server, settings, connect } = setup({ 'Test.Hang': () => undefined });
        const client = connect();
        await client.call('Test.Echo', [{}]);

        const hanging = client.call('Test.Hang', [{}]);
        await Bun.sleep(20);
        server.drop();
        // It reached the server, which may have acted on it: the caller
        // is told it doesn't know, not that it failed.
        await expect(hanging).rejects.toBe(
            `${CONNECTION_LOST} the connection to crs at ${settings.address} was lost before it answered`
        );

        expect(await client.call('Test.Echo', [{ n: 2 }])).toMatchObject({ echo: { n: 2 } });
        // The reconnect authenticates with the token the registration saved.
        expect(server.methods().filter(m => m.startsWith('RPCHandler.'))).toEqual([
            'RPCHandler.RegisterUser',
            'RPCHandler.Authenticate',
        ]);
    });

    test('an unreachable server fails calls with why, and keeps retrying', async () => {
        // A backoff long enough to call again before the next attempt.
        const { server, settings, connect } = setup({}, {}, USER_TOML, { initialBackoffMs: 5000 });
        server.stop();
        const client = connect();
        const err = await client.call('Test.Echo', [{}]).catch(e => e);
        expect(err).toStartWith(`Can't reach crs at ${settings.address}`);
        // Between attempts, calls fail at once rather than hang.
        const next = await client.call('Test.Echo', [{}]).catch(e => e);
        expect(next).toMatch(/^Can't reach crs at .* \(retrying in \d+s\)$/);
    });
});

describe('RemoteRpcClient configuration problems', () => {
    test('without a token or a registration key, says how to register', async () => {
        const { server, settings, connect } = setup({}, { registrationKey: '' });
        const err = await connect()
            .call('Test.Echo', [{}])
            .catch(e => e);
        expect(err).toContain('set CRS_REGISTRATION_KEY');
        expect(err).toContain(settings.tokenFile);
        expect(server.received).toEqual([]);
    });

    test('without a login, says where to set one', async () => {
        const { server, connect } = setup({}, {}, USER_TOML.replace(/^GithubUsername.*\n/, ''));
        const err = await connect()
            .call('Test.Echo', [{}])
            .catch(e => e);
        expect(err).toContain('no GitHub login. Set CRS_LOGIN, or GithubUsername in');
        expect(server.received).toEqual([]);
    });

    test('without a local config, says which file it needs', async () => {
        const { settings, connect } = setup({}, {}, null);
        const err = await connect()
            .call('Test.Echo', [{}])
            .catch(e => e);
        expect(err).toContain(`${settings.configPath} does not exist`);
    });

    test('a refused registration carries the server’s reasons', async () => {
        const { connect } = setup({
            'RPCHandler.RegisterUser': () => ({
                okay: false,
                message: 'invalid workflows',
                errors: [{ workflow: 0, field: 'WorkflowType', message: 'unknown type' }],
            }),
        });
        const err = await connect()
            .call('Test.Echo', [{}])
            .catch(e => e);
        expect(err).toContain(
            'refused to register octocat: invalid workflows (Workflows[0].WorkflowType: unknown type)'
        );
    });

    test('a token the server revoked says which one it is', async () => {
        const { settings, connect } = setup({
            'RPCHandler.Authenticate': () => ({ okay: false, known: false }),
            'RPCHandler.RegisterUser': () => ({
                okay: false,
                message:
                    'token revoked: an administrator deleted the user this token belonged to, so it cannot register again; remove the token your client stored to register afresh, or ask an administrator',
                errors: [],
            }),
        });
        await Bun.write(settings.tokenFile, `${TOKEN_A}\n`);
        const err = await connect()
            .call('Test.Echo', [{}])
            .catch(e => e);
        expect(err).toContain('refused to register octocat: token revoked: ');
        expect(err).toEndWith(
            `or ask an administrator. That token is the one in ${settings.tokenFile}.`
        );
    });

    test('a login registered under another token says how to recover', async () => {
        const { server, settings, connect } = setup();
        server.users.set('octocat', { login: 'octocat', token: TOKEN_B });
        await Bun.write(settings.tokenFile, `${TOKEN_A}\n`);
        const err = await connect()
            .call('Test.Echo', [{}])
            .catch(e => e);
        expect(err).toContain('login already registered');
        expect(err).toContain(
            `copy it from the machine that registered into ${settings.tokenFile}`
        );
    });
});

describe('remoteSettingsFromEnv', () => {
    test('is off without CRS_REMOTE', () => {
        expect(remoteSettingsFromEnv({ HOME: '/home/me' })).toBeNull();
        expect(remoteSettingsFromEnv({ CRS_REMOTE: '  ' })).toBeNull();
    });

    test('defaults its files under HOME', () => {
        expect(
            remoteSettingsFromEnv({ CRS_REMOTE: 'crs.internal:7700', HOME: '/home/me' })
        ).toEqual({
            address: 'crs.internal:7700',
            hostname: 'crs.internal',
            port: 7700,
            tls: false,
            envToken: '',
            tokenFile: '/home/me/.crs/remote_tokens/crs.internal_7700',
            login: '',
            registrationKey: '',
            configPath: '/home/me/.config/codereviewserver.toml',
        });
    });

    test('follows CRS_HOME, XDG_CONFIG_HOME and the other variables', () => {
        expect(
            remoteSettingsFromEnv({
                CRS_REMOTE: '[::1]:443',
                CRS_REMOTE_TLS: '1',
                CRS_REMOTE_TOKEN: ` ${TOKEN_A} `,
                CRS_LOGIN: 'octocat',
                CRS_REGISTRATION_KEY: 'sesame',
                CRS_HOME: '/srv/crs',
                XDG_CONFIG_HOME: '/etc/xdg',
                HOME: '/home/me',
            })
        ).toMatchObject({
            hostname: '::1',
            port: 443,
            tls: true,
            envToken: TOKEN_A,
            tokenFile: '/srv/crs/remote_tokens/__1_443',
            login: 'octocat',
            registrationKey: 'sesame',
            configPath: '/etc/xdg/codereviewserver.toml',
        });
    });

    test('keeps each server’s token in a file of its own', () => {
        const file = (address: string) =>
            remoteSettingsFromEnv({ CRS_REMOTE: address, HOME: '/home/me' })?.tokenFile;
        expect(file('crs.internal:7700')).not.toBe(file('crs.other:7700'));
        expect(file('crs.internal:7700')).not.toBe(file('crs.internal:7701'));
        // A host name is case-insensitive; the file is the same either way.
        expect(file('CRS.Internal:7700')).toBe(file('crs.internal:7700'));
        expect(tokenFileName('fe80::1%eth0', 7700)).toBe('fe80__1_eth0_7700');
        expect(tokenFileName('../../etc', 22)).toBe('.._.._etc_22');
    });

    test('rejects an address without a port', () => {
        expect(() => parseRemoteAddress('crs.internal')).toThrow('is not host:port');
        expect(() => parseRemoteAddress('crs.internal:0')).toThrow('is not host:port');
        expect(() => parseRemoteAddress('::1:80')).toThrow('is not host:port');
    });
});

describe('readUserConfig', () => {
    test('takes what a registration sends, plus the login', () => {
        const dir = mkdtempSync(join(tmpdir(), 'crs-remote-test-'));
        cleanups.push(() => rmSync(dir, { recursive: true, force: true }));
        const path = join(dir, 'c.toml');
        writeFileSync(path, USER_TOML);
        expect(readUserConfig(path)).toEqual({
            payload: USER_PAYLOAD,
            githubUsername: 'octocat',
        });

        writeFileSync(path, 'GithubUsername = "octocat"\n');
        expect(readUserConfig(path).payload).toEqual({
            Workflows: [],
            Repos: [],
            SectionPriority: {},
            SectionSorting: {},
        });
    });

    test('names a key of the wrong type', () => {
        const dir = mkdtempSync(join(tmpdir(), 'crs-remote-test-'));
        cleanups.push(() => rmSync(dir, { recursive: true, force: true }));
        const path = join(dir, 'c.toml');
        writeFileSync(path, 'Repos = "acme/widgets"\n');
        expect(() => readUserConfig(path)).toThrow('Repos in');
        writeFileSync(path, 'Workflows = 3\n');
        expect(() => readUserConfig(path)).toThrow('Workflows in');
        writeFileSync(path, 'not toml at all = = =\n');
        expect(() => readUserConfig(path)).toThrow('could not parse');
    });
});

describe('describeProblem', () => {
    test('words a validation error the way the server does', () => {
        expect(describeProblem({ workflow: 2, field: 'Filters', message: 'unknown filter' })).toBe(
            'Workflows[2].Filters: unknown filter'
        );
        expect(describeProblem({ workflow: -1, field: 'Repos', message: 'empty' })).toBe(
            'Repos: empty'
        );
        expect(describeProblem({ workflow: -1, field: '', message: 'too many' })).toBe('too many');
        expect(describeProblem('plain')).toBe('plain');
    });
});
