// Remote mode: the bridge talks to a hosted `crs --listen` over TCP (TLS with
// CRS_REMOTE_TLS=1) instead of spawning `crs --server` — see docs/clients.md.
//
// The wire is the same newline-delimited JSON-RPC 1.0 as stdio, so replies are
// framed by the same JsonRpcLineParser. What remote mode adds is the session:
// a connection starts unauthenticated, and each one opens with the connect
// flow (docs/protocol.md):
//
//   1. RPCHandler.Authenticate {Token} with the stored token, if there is one.
//   2. When the server doesn't know the token (never registered there, or the
//      user was swept by its retention) or there is none, RPCHandler.RegisterUser
//      with the registration key and the workflows from the local TOML. The
//      stored token goes along, so a swept user gets the same one back, and
//      the token in the reply is saved for next time.
//
// Calls made while that runs wait for it. A dropped connection fails the calls
// in flight and reconnects with capped exponential backoff; a call refused
// with `not authenticated:` re-runs the flow on its connection and is retried
// once. Errors are rejected as strings, the shape a Go error string arrives in,
// so the frontend shows them the same way.
//
// Who the flow signed in as — the login, whether it is an administrator, and
// whether its GitHub account is linked for writes — is kept for the web UI's
// /api/session (describeSession).

import { chmodSync, mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import type { Socket } from 'bun';
import { JsonRpcLineParser } from './rpc_framing';

export interface RemoteSettings {
    /** CRS_REMOTE as given (`host:port`), for messages. */
    address: string;
    hostname: string;
    port: number;
    /** CRS_REMOTE_TLS=1: speak TLS to the server, or to the proxy in front of it. */
    tls: boolean;
    /** CRS_REMOTE_TOKEN, which wins over the token file when set. */
    envToken: string;
    /** Where a registration's token is kept: $CRS_HOME/remote_token. */
    tokenFile: string;
    /** CRS_LOGIN, which wins over the TOML's GithubUsername when set. */
    login: string;
    /** CRS_REGISTRATION_KEY, needed only to register. */
    registrationKey: string;
    /** The local codereviewserver.toml whose workflows a registration sends. */
    configPath: string;
}

/** Splits `host:port` (or `[v6addr]:port`), throwing on anything else. */
export function parseRemoteAddress(value: string): { hostname: string; port: number } {
    const match = /^(?:\[([^\]]+)\]|([^:\s[\]]+)):(\d+)$/.exec(value.trim());
    const port = match ? parseInt(match[3], 10) : 0;
    if (!match || port < 1 || port > 65535) {
        throw new Error(`CRS_REMOTE=${value} is not host:port (e.g. crs.example.com:7700)`);
    }
    return { hostname: match[1] ?? match[2], port };
}

/** Remote mode's settings, or null when CRS_REMOTE is unset (spawn crs locally). */
export function remoteSettingsFromEnv(
    env: Record<string, string | undefined> = process.env
): RemoteSettings | null {
    const address = env.CRS_REMOTE?.trim();
    if (!address) return null;
    const { hostname, port } = parseRemoteAddress(address);
    const home = env.HOME || homedir();
    const crsHome = env.CRS_HOME || join(home, '.crs');
    const configHome = env.XDG_CONFIG_HOME || join(home, '.config');
    return {
        address,
        hostname,
        port,
        tls: ['1', 'true'].includes((env.CRS_REMOTE_TLS ?? '').trim().toLowerCase()),
        envToken: env.CRS_REMOTE_TOKEN?.trim() ?? '',
        tokenFile: join(crsHome, 'remote_token'),
        login: env.CRS_LOGIN?.trim() ?? '',
        registrationKey: env.CRS_REGISTRATION_KEY ?? '',
        configPath: join(configHome, 'codereviewserver.toml'),
    };
}

const isMissing = (e: unknown) => (e as NodeJS.ErrnoException)?.code === 'ENOENT';

/** The saved token, or '' when there is none yet. */
export function readTokenFile(path: string): string {
    try {
        return readFileSync(path, 'utf-8').trim();
    } catch (e) {
        if (isMissing(e)) return '';
        throw new Error(`could not read the token in ${path}: ${errorText(e)}`);
    }
}

/**
 * Saves a token readable by its owner only. It goes to a temporary file that
 * is renamed into place, so a crash never leaves half a token behind.
 */
export function writeTokenFile(path: string, token: string) {
    mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
    const tmp = `${path}.${process.pid}.tmp`;
    writeFileSync(tmp, `${token}\n`, { mode: 0o600 });
    // The mode above only applies to a file it creates.
    chmodSync(tmp, 0o600);
    renameSync(tmp, path);
}

/** What RegisterUser sends from the local TOML, under the contract's keys. */
export interface UserConfigPayload {
    Workflows: Record<string, unknown>[];
    Repos: string[];
    SectionPriority: Record<string, number>;
    SectionSorting: Record<string, string>;
}

/**
 * Reads the parts of the local codereviewserver.toml a registration sends —
 * the workflows (in their TOML keys, which are also their JSON keys on the
 * server), the fallback repos and the section settings — plus the root
 * GithubUsername, the login to register when CRS_LOGIN is unset.
 */
export function readUserConfig(path: string): {
    payload: UserConfigPayload;
    githubUsername: string;
} {
    let text: string;
    try {
        text = readFileSync(path, 'utf-8');
    } catch (e) {
        if (isMissing(e)) {
            throw new Error(
                `${path} does not exist; the server builds your dashboard from the [[Workflows]] in it`
            );
        }
        throw new Error(`could not read ${path}: ${errorText(e)}`);
    }
    let parsed: Record<string, unknown>;
    try {
        parsed = Bun.TOML.parse(text) as Record<string, unknown>;
    } catch (e) {
        throw new Error(`could not parse ${path}: ${errorText(e)}`);
    }

    const isTable = (v: unknown): v is Record<string, unknown> =>
        typeof v === 'object' && v !== null && !Array.isArray(v);
    const wrongType = (key: string, want: string) => new Error(`${key} in ${path} must be ${want}`);

    const workflows = parsed.Workflows ?? [];
    if (!Array.isArray(workflows) || !workflows.every(isTable)) {
        throw wrongType('Workflows', 'an array of tables ([[Workflows]])');
    }
    const repos = parsed.Repos ?? [];
    if (!Array.isArray(repos) || !repos.every(r => typeof r === 'string')) {
        throw wrongType('Repos', 'an array of strings');
    }
    const priority = parsed.SectionPriority ?? {};
    if (!isTable(priority) || !Object.values(priority).every(v => typeof v === 'number')) {
        throw wrongType('SectionPriority', 'a table of numbers');
    }
    const sorting = parsed.SectionSorting ?? {};
    if (!isTable(sorting) || !Object.values(sorting).every(v => typeof v === 'string')) {
        throw wrongType('SectionSorting', 'a table of strings');
    }
    const login = parsed.GithubUsername;
    return {
        payload: {
            Workflows: workflows,
            Repos: repos,
            SectionPriority: priority as Record<string, number>,
            SectionSorting: sorting as Record<string, string>,
        },
        githubUsername: typeof login === 'string' ? login.trim() : '',
    };
}

/** The flags Authenticate, RegisterUser and GetMyWorkflows carry about the user's GitHub link. */
interface GitHubLinkFlags {
    /** Their own GitHub account is linked, so their reviews go out as them. */
    github_linked?: boolean;
    /** The server can link GitHub accounts (StartGitHubLink). */
    github_link_available?: boolean;
}

interface AuthenticateReply extends GitHubLinkFlags {
    okay: boolean;
    known: boolean;
    login: string;
    is_admin: boolean;
    message: string;
}

interface RegisterUserReply extends GitHubLinkFlags {
    okay: boolean;
    created: boolean;
    login: string;
    token: string;
    is_admin: boolean;
    message: string;
    errors: unknown[] | null;
}

/**
 * What GET /api/session tells the web UI about the bridge's connection.
 * Spawning crs locally it is `{ remote: false }`: the local connection is the
 * token owner's own, and links nothing.
 */
export interface BridgeSessionInfo {
    /** Connected to a shared server (CRS_REMOTE) rather than a crs of its own. */
    remote: boolean;
    /** CRS_REMOTE, for display. */
    address?: string;
    /** The login the connect flow signed in as; '' for the administrator token. */
    login?: string;
    is_admin?: boolean;
    github_linked?: boolean;
    github_link_available?: boolean;
    /** Why the session could not be read just now, e.g. the server is unreachable. */
    error?: string;
}

/** Who the connect flow signed in as. */
type RemoteSession = Required<Omit<BridgeSessionInfo, 'remote' | 'address' | 'error'>>;

function sessionFrom(reply: AuthenticateReply | RegisterUserReply): RemoteSession {
    return {
        login: reply.login ?? '',
        is_admin: !!reply.is_admin,
        github_linked: !!reply.github_linked,
        github_link_available: !!reply.github_link_available,
    };
}

interface JsonRpcResponse {
    id?: number | string;
    result?: unknown;
    error?: unknown;
}

interface Settle {
    resolve: (value: unknown) => void;
    reject: (reason: unknown) => void;
}

interface Call extends Settle {
    method: string;
    params: unknown[];
    /** Already sent again once after a `not authenticated:` refusal. */
    retried: boolean;
}

/** The text of an error, whether it's an Error or a Go error string. */
function errorText(e: unknown): string {
    if (e instanceof Error) return e.message;
    return typeof e === 'string' ? e : JSON.stringify(e);
}

const NOT_AUTHENTICATED = 'not authenticated:';

const isNotAuthenticated = (e: unknown) => typeof e === 'string' && e.startsWith(NOT_AUTHENTICATED);

/** One TCP (or TLS) connection to the server, and the requests in flight on it. */
class Connection {
    socket: Socket<Connection> | null = null;
    /** Bumped each time the connect flow succeeds on this connection. */
    authEpoch = 0;
    readonly closed: Promise<void>;
    private resolveClosed!: () => void;
    private isClosed = false;
    private inFlight = new Map<number, Settle>();
    private parser = new JsonRpcLineParser(msg => this.onResponse(msg as JsonRpcResponse));
    private decoder = new TextDecoder();
    // Bytes the socket didn't take yet, written on `drain`.
    private unsent: Buffer | null = null;

    constructor(
        private nextId: () => number,
        private lostMessage: string
    ) {
        this.closed = new Promise(resolve => (this.resolveClosed = resolve));
    }

    get open() {
        return !this.isClosed;
    }

    request(method: string, params: unknown[]): Promise<unknown> {
        if (this.isClosed) return Promise.reject(this.lostMessage);
        const id = this.nextId();
        return new Promise((resolve, reject) => {
            this.inFlight.set(id, { resolve, reject });
            this.write(Buffer.from(`${JSON.stringify({ method, params, id })}\n`));
        });
    }

    onData(chunk: Uint8Array) {
        this.parser.push(this.decoder.decode(chunk, { stream: true }));
    }

    flush() {
        if (!this.unsent) return;
        const pending = this.unsent;
        this.unsent = null;
        this.write(pending);
    }

    /** Fails what's in flight; idempotent, whichever socket event gets here first. */
    onClose() {
        if (this.isClosed) return;
        this.isClosed = true;
        for (const { reject } of this.inFlight.values()) reject(this.lostMessage);
        this.inFlight.clear();
        this.resolveClosed();
    }

    close() {
        this.socket?.end();
        this.onClose();
    }

    private write(bytes: Buffer) {
        if (!this.socket || this.isClosed) return;
        if (this.unsent) {
            this.unsent = Buffer.concat([this.unsent, bytes]);
            return;
        }
        const written = Math.max(this.socket.write(bytes), 0);
        if (written < bytes.length) this.unsent = bytes.subarray(written);
    }

    private onResponse(res: JsonRpcResponse) {
        if (typeof res.id !== 'number') return;
        const pending = this.inFlight.get(res.id);
        if (!pending) return;
        this.inFlight.delete(res.id);
        if (res.error) {
            pending.reject(res.error);
        } else {
            pending.resolve(res.result);
        }
    }
}

export interface RemoteRpcOptions {
    /** First reconnect delay, doubled after each failed attempt. */
    initialBackoffMs?: number;
    /** Ceiling for the reconnect delay. */
    maxBackoffMs?: number;
    /** How long a TCP/TLS connect may take before the attempt counts as failed. */
    connectTimeoutMs?: number;
    log?: (message: string) => void;
    logError?: (message: string) => void;
}

type Phase = 'connecting' | 'authenticating' | 'ready' | 'backoff';

/**
 * The bridge's link to a hosted crs: `call` has the same contract as the
 * stdio RpcBridge's, resolving with the Go reply and rejecting with the Go
 * error (or a string saying why the call never reached the server).
 */
export class RemoteRpcClient {
    private phase: Phase = 'connecting';
    private stopped = false;
    private conn: Connection | null = null;
    // Calls waiting for the connect flow, sent in order once it succeeds:
    // those refused as not authenticated first, then those made meanwhile.
    private retries: Call[] = [];
    private queue: Call[] = [];
    private nextId = 1;
    private backoffMs: number;
    private retryAt = 0;
    private lastError = '';
    // A token the server issued while CRS_REMOTE_TOKEN named another one.
    private issuedToken = '';
    // Who the last connect flow signed in as.
    private session: RemoteSession | null = null;
    private cancelSleep: (() => void) | null = null;
    private readonly opts: Required<RemoteRpcOptions>;

    constructor(
        private settings: RemoteSettings,
        options: RemoteRpcOptions = {}
    ) {
        this.opts = {
            initialBackoffMs: 500,
            maxBackoffMs: 30_000,
            connectTimeoutMs: 10_000,
            log: message => console.log(`[Remote] ${message}`),
            logError: message => console.error(`[Remote] ${message}`),
            ...options,
        };
        this.backoffMs = this.opts.initialBackoffMs;
        void this.run();
    }

    private get where() {
        return `crs at ${this.settings.address}`;
    }

    call(method: string, params: unknown[]): Promise<unknown> {
        if (this.stopped) {
            return Promise.reject(`The connection to ${this.where} has been shut down`);
        }
        // The last attempt failed: say why now rather than leave the call
        // hanging until the next one.
        if (this.phase === 'backoff') {
            const seconds = Math.max(1, Math.ceil((this.retryAt - Date.now()) / 1000));
            return Promise.reject(`${this.lastError} (retrying in ${seconds}s)`);
        }
        return new Promise((resolve, reject) => {
            const call: Call = { method, params, resolve, reject, retried: false };
            if (this.phase === 'ready' && this.conn) {
                this.send(this.conn, call);
            } else {
                this.queue.push(call);
            }
        });
    }

    /**
     * The session for the web UI: who the connect flow signed in as, with the
     * GitHub link read fresh (GetMyWorkflows), since a link made or removed —
     * from this UI or another client — changes it after the flow ran. When
     * that read fails, the last known state comes back with the reason.
     */
    async describeSession(): Promise<BridgeSessionInfo> {
        const base = { remote: true, address: this.settings.address };
        try {
            const reply = (await this.call('RPCHandler.GetMyWorkflows', [
                {},
            ])) as GitHubLinkFlags | null;
            if (this.session) {
                this.session.github_linked = !!reply?.github_linked;
                this.session.github_link_available = !!reply?.github_link_available;
            }
        } catch (e) {
            return { ...base, ...this.session, error: errorText(e) };
        }
        return { ...base, ...this.session };
    }

    /** Closes the connection for good, failing anything still waiting. */
    close() {
        this.stopped = true;
        this.cancelSleep?.();
        this.conn?.close();
        this.rejectQueue(`The connection to ${this.where} has been shut down`);
    }

    private async run() {
        let delay = 0;
        while (!this.stopped) {
            if (delay > 0) await this.sleep(delay);
            if (this.stopped) return;
            this.phase = 'connecting';
            let conn: Connection | null = null;
            try {
                conn = await this.open();
                if (this.stopped) {
                    conn.close();
                    return;
                }
                this.conn = conn;
                this.phase = 'authenticating';
                await this.authenticate(conn);
            } catch (e) {
                conn?.close();
                this.conn = null;
                if (this.stopped) return;
                const message = errorText(e);
                delay = this.backoffMs;
                this.backoffMs = Math.min(this.backoffMs * 2, this.opts.maxBackoffMs);
                this.lastError = message;
                this.retryAt = Date.now() + delay;
                this.phase = 'backoff';
                this.opts.logError(`${message} (retrying in ${Math.ceil(delay / 1000)}s)`);
                this.rejectQueue(message);
                continue;
            }

            this.phase = 'ready';
            this.backoffMs = this.opts.initialBackoffMs;
            this.flushQueue(conn);
            await conn.closed;
            if (this.stopped) return;
            // Calls made from here on wait for the reconnect.
            this.conn = null;
            this.phase = 'connecting';
            this.opts.logError(`Lost the connection to ${this.where}; reconnecting`);
            delay = this.opts.initialBackoffMs;
        }
    }

    private async open(): Promise<Connection> {
        const conn = new Connection(
            () => this.nextId++,
            `The connection to ${this.where} was lost before it answered`
        );
        const connecting = Bun.connect<Connection>({
            hostname: this.settings.hostname,
            port: this.settings.port,
            tls: this.settings.tls,
            data: conn,
            socket: {
                data: (socket, chunk) => socket.data.onData(chunk),
                drain: socket => socket.data.flush(),
                // A FIN from the server: close our side too, which ends in `close`.
                end: socket => {
                    socket.end();
                },
                close: socket => socket.data.onClose(),
                error: (socket, err) => {
                    this.opts.logError(`Connection to ${this.where} failed: ${err.message}`);
                    socket.data.onClose();
                },
            },
        });
        let timer: ReturnType<typeof setTimeout> | undefined;
        const timeout = new Promise<never>((_, reject) => {
            timer = setTimeout(
                () => reject(new Error(`timed out after ${this.opts.connectTimeoutMs / 1000}s`)),
                this.opts.connectTimeoutMs
            );
        });
        try {
            conn.socket = await Promise.race([connecting, timeout]);
        } catch (e) {
            // A connect that lands after the timeout is closed straight away.
            connecting.then(
                socket => socket.end(),
                () => {}
            );
            throw new Error(`Can't reach ${this.where}: ${errorText(e)}`);
        } finally {
            clearTimeout(timer);
        }
        // A dead peer behind a NAT or proxy otherwise looks connected forever.
        conn.socket.setKeepAlive(true, 60_000);
        return conn;
    }

    /** The connect flow (see the top of the file). Throws a message saying what to fix. */
    private async authenticate(conn: Connection) {
        const { token, source } = this.currentToken();
        if (token) {
            const reply = (await this.handshake(conn, 'RPCHandler.Authenticate', {
                Token: token,
            })) as AuthenticateReply;
            if (reply?.okay) {
                this.session = sessionFrom(reply);
                conn.authEpoch++;
                return;
            }
            if (reply?.known) {
                throw new Error(
                    `${this.where} refused the token from ${source}: ${reply.message || 'no reason given'}`
                );
            }
            this.opts.log(
                `${this.where} doesn't know the token from ${source} (never registered there, or removed after a stretch of inactivity); registering again`
            );
        }
        await this.register(conn, token, source);
        conn.authEpoch++;
    }

    private async register(conn: Connection, token: string, source: string) {
        const { settings } = this;
        if (!settings.registrationKey) {
            throw new Error(
                token
                    ? `${this.where} doesn't know the token from ${source}, and CRS_REGISTRATION_KEY is not set to register again with it. Set CRS_REGISTRATION_KEY to the server's registration key.`
                    : `Not registered with ${this.where}: set CRS_REGISTRATION_KEY to the server's registration key to register, or put an existing token in ${settings.tokenFile} (or CRS_REMOTE_TOKEN).`
            );
        }
        let user: ReturnType<typeof readUserConfig>;
        try {
            user = readUserConfig(settings.configPath);
        } catch (e) {
            throw new Error(`Can't register with ${this.where}: ${errorText(e)}`);
        }
        const login = settings.login || user.githubUsername;
        if (!login) {
            throw new Error(
                `Can't register with ${this.where}: no GitHub login. Set CRS_LOGIN, or GithubUsername in ${settings.configPath}.`
            );
        }

        const reply = (await this.handshake(conn, 'RPCHandler.RegisterUser', {
            RegistrationKey: settings.registrationKey,
            Login: login,
            Token: token,
            ...user.payload,
        })) as RegisterUserReply;
        if (!reply?.okay || !reply.token) {
            const problems = (reply?.errors ?? []).map(describeProblem).filter(Boolean);
            let message = `${this.where} refused to register ${login}: ${reply?.message || 'no reason given'}`;
            if (problems.length > 0) message += ` (${problems.join('; ')})`;
            if (/already registered/i.test(reply?.message ?? '')) {
                message += `. The server holds a different token for ${login}: copy it from the machine that registered into ${settings.tokenFile} (or CRS_REMOTE_TOKEN), or ask the server's administrator to delete the user.`;
            }
            throw new Error(message);
        }

        try {
            writeTokenFile(settings.tokenFile, reply.token);
        } catch (e) {
            this.opts.logError(
                `Could not save the token to ${settings.tokenFile}: ${errorText(e)}; it lasts until the bridge exits`
            );
        }
        if (settings.envToken && reply.token !== settings.envToken) {
            this.issuedToken = reply.token;
            this.opts.logError(
                `${this.where} issued a token other than CRS_REMOTE_TOKEN; it was saved to ${settings.tokenFile}, but CRS_REMOTE_TOKEN wins on the next start — update or unset it`
            );
        }
        this.session = sessionFrom({ ...reply, login: reply.login || login });
        const who = `${reply.login || login}${reply.is_admin ? ' (administrator)' : ''}`;
        this.opts.log(
            `${reply.created ? 'Registered' : 'Re-registered'} with ${this.where} as ${who}; token saved to ${settings.tokenFile}`
        );
    }

    private currentToken(): { token: string; source: string } {
        if (this.issuedToken) return { token: this.issuedToken, source: this.settings.tokenFile };
        if (this.settings.envToken) {
            return { token: this.settings.envToken, source: 'CRS_REMOTE_TOKEN' };
        }
        return {
            token: readTokenFile(this.settings.tokenFile),
            source: this.settings.tokenFile,
        };
    }

    /** One connect-flow request; its errors say which step failed. */
    private async handshake(conn: Connection, method: string, args: Record<string, unknown>) {
        try {
            return await conn.request(method, [args]);
        } catch (e) {
            throw new Error(`${this.where} answered ${method} with an error: ${errorText(e)}`);
        }
    }

    private send(conn: Connection, call: Call) {
        const epoch = conn.authEpoch;
        conn.request(call.method, call.params).then(call.resolve, err =>
            this.onCallError(conn, call, epoch, err)
        );
    }

    /**
     * A call the server refused as not authenticated gets one more go after
     * the connect flow runs again on its connection. Every call refused while
     * that runs waits for the same run, and one sent before the latest run
     * finished is simply sent again.
     */
    private onCallError(conn: Connection, call: Call, epoch: number, err: unknown) {
        if (call.retried || !isNotAuthenticated(err) || !conn.open || conn !== this.conn) {
            call.reject(err);
            return;
        }
        call.retried = true;
        if (this.phase === 'authenticating') {
            this.retries.push(call);
        } else if (conn.authEpoch !== epoch) {
            this.send(conn, call);
        } else {
            void this.reauthenticate(conn, call);
        }
    }

    private async reauthenticate(conn: Connection, call: Call) {
        this.phase = 'authenticating';
        this.retries.push(call);
        this.opts.log(`${this.where} says this connection is not authenticated; connecting again`);
        try {
            await this.authenticate(conn);
        } catch (e) {
            // A dropped connection is the reconnect loop's to handle, and the
            // waiting calls go out on the next one.
            if (!conn.open || this.stopped) return;
            const message = errorText(e);
            this.opts.logError(message);
            this.rejectQueue(message);
            // The loop reconnects, re-running the flow with backoff.
            conn.close();
            return;
        }
        if (conn !== this.conn || !conn.open || this.stopped) return;
        this.phase = 'ready';
        this.flushQueue(conn);
    }

    private takeQueue() {
        const queued = [...this.retries, ...this.queue];
        this.retries = [];
        this.queue = [];
        return queued;
    }

    private flushQueue(conn: Connection) {
        for (const call of this.takeQueue()) this.send(conn, call);
    }

    private rejectQueue(reason: string) {
        for (const call of this.takeQueue()) call.reject(reason);
    }

    private sleep(ms: number) {
        return new Promise<void>(resolve => {
            const timer = setTimeout(resolve, ms);
            this.cancelSleep = () => {
                clearTimeout(timer);
                resolve();
            };
        });
    }
}

/**
 * One entry of a RegisterUser reply's `errors` — a config ValidationError,
 * `{workflow, field, message}` with workflow -1 for a root-level setting —
 * worded the way the server's own Error() words it.
 */
export function describeProblem(problem: unknown): string {
    if (typeof problem === 'string') return problem;
    if (typeof problem === 'object' && problem !== null) {
        const { workflow, field, message } = problem as Record<string, unknown>;
        if (typeof message === 'string') {
            if (typeof workflow === 'number' && workflow >= 0) {
                return `Workflows[${workflow}].${field}: ${message}`;
            }
            return typeof field === 'string' && field ? `${field}: ${message}` : message;
        }
    }
    return JSON.stringify(problem);
}
