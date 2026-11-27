// The language servers behind the code viewer: once a reference or definition
// takes a review from the diff into a real file, that file gets the language
// server an editor would start for it (gopls, rust-analyzer, pyright,
// typescript-language-server), rooted where that editor would root it, so
// hover, definitions and references keep working from file to file. The
// emacs client gets the same by opening the file in a buffer, where lsp-mode
// takes over.
//
// Adding a language is one entry in LANGUAGE_SERVERS.

import { stat } from 'node:fs/promises';
import { basename, dirname, extname, isAbsolute, join, relative, resolve } from 'node:path';

export interface LanguageServerCommand {
    cmd: string;
    args: string[];
    /**
     * Arguments for a quick run that has to succeed before the command
     * counts as installed: rustup puts a `rust-analyzer` proxy on PATH that
     * fails unless the component is installed.
     */
    check?: string[];
}

export interface LanguageServerSpec {
    /** LSP language identifier for each file extension (no dot) the server handles. */
    extensions: Record<string, string>;
    /** Files whose directory is a project root for this language. */
    rootMarkers: string[];
    /** Servers to try, in order; the first one installed is used. */
    commands: LanguageServerCommand[];
}

export const LANGUAGE_SERVERS: Record<string, LanguageServerSpec> = {
    go: {
        extensions: { go: 'go' },
        rootMarkers: ['go.work', 'go.mod'],
        commands: [{ cmd: 'gopls', args: ['serve'] }],
    },
    rust: {
        extensions: { rs: 'rust' },
        rootMarkers: ['Cargo.toml'],
        commands: [{ cmd: 'rust-analyzer', args: [], check: ['--version'] }],
    },
    python: {
        extensions: { py: 'python', pyi: 'python' },
        rootMarkers: [
            'pyproject.toml',
            'pyrightconfig.json',
            'setup.py',
            'setup.cfg',
            'requirements.txt',
        ],
        commands: [
            { cmd: 'pyright-langserver', args: ['--stdio'] },
            { cmd: 'basedpyright-langserver', args: ['--stdio'] },
            { cmd: 'pylsp', args: [] },
            { cmd: 'jedi-language-server', args: [] },
        ],
    },
    typescript: {
        extensions: {
            ts: 'typescript',
            mts: 'typescript',
            cts: 'typescript',
            tsx: 'typescriptreact',
            js: 'javascript',
            mjs: 'javascript',
            cjs: 'javascript',
            jsx: 'javascriptreact',
        },
        rootMarkers: ['tsconfig.json', 'jsconfig.json', 'package.json'],
        commands: [
            { cmd: 'typescript-language-server', args: ['--stdio'] },
            { cmd: 'vtsls', args: ['--stdio'] },
        ],
    },
};

// Older clients ask /api/check-lsp-file and /api/lsp-file for a Prism
// language rather than a server.
const LANGUAGE_ALIASES: Record<string, string> = {
    javascript: 'typescript',
    tsx: 'typescript',
    jsx: 'typescript',
};

/** The LANGUAGE_SERVERS key for a server name or an older client's language. */
export function serverLanguage(lang: string | null | undefined): string | null {
    if (!lang) return null;
    if (Object.hasOwn(LANGUAGE_SERVERS, lang)) return lang;
    return Object.hasOwn(LANGUAGE_ALIASES, lang) ? LANGUAGE_ALIASES[lang] : null;
}

/** Which server handles a file, and the LSP language id its didOpen names. */
export function languageForPath(path: string): { lang: string; languageId: string } | null {
    const ext = extname(path).slice(1).toLowerCase();
    if (!ext) return null;
    for (const [lang, spec] of Object.entries(LANGUAGE_SERVERS)) {
        if (Object.hasOwn(spec.extensions, ext)) return { lang, languageId: spec.extensions[ext] };
    }
    return null;
}

async function isFile(path: string): Promise<boolean> {
    try {
        return (await stat(path)).isFile();
    } catch {
        return false;
    }
}

/** Whether `path` is `dir` or inside it. */
export function isWithin(dir: string, path: string): boolean {
    const rel = relative(dir, path);
    return rel === '' || (!rel.startsWith('..') && !isAbsolute(rel));
}

/**
 * Where a file's language server is rooted. An editor opening the file would
 * pick the project it belongs to, and so does this:
 *
 *   - the checkout itself when it holds one of the language's markers (a
 *     go.mod, Cargo.toml, pyproject.toml or package.json at the top), so one
 *     server covers the whole repo;
 *   - otherwise the nearest directory above the file, inside the checkout,
 *     that holds one (a repo whose Go module or Python project lives in a
 *     subdirectory);
 *   - otherwise the checkout.
 *
 * A file outside the checkout (a dependency or the standard library, reached
 * through a definition) is rooted at the checkout; the client keeps the
 * session it navigated from for those (see keepsLspSession).
 */
export async function workspaceRoot(
    checkout: string,
    file: string,
    markers: string[]
): Promise<string> {
    const top = resolve(checkout);
    const path = resolve(top, file);
    if (!isWithin(top, path)) return top;

    const hasMarker = async (dir: string) => {
        for (const marker of markers) {
            if (await isFile(join(dir, marker))) return true;
        }
        return false;
    };
    if (await hasMarker(top)) return top;
    for (let dir = dirname(path); isWithin(top, dir) && dir !== top; dir = dirname(dir)) {
        if (await hasMarker(dir)) return dir;
    }
    return top;
}

export interface ResolvedLanguageServer {
    lang: string;
    argv: string[];
    /** The command's name, for showing which server is running. */
    name: string;
}

interface ResolverOptions {
    which(cmd: string): string | null;
    /** Runs a check command; resolves to whether it exited 0. */
    runs(argv: string[]): Promise<boolean>;
    now(): number;
    /** How long a language with nothing installed stays unavailable before asking again. */
    retryMissingMs: number;
}

/**
 * Finds the installed server for each language, the first of its commands
 * that is on PATH (and passes its check). A server found stays found; a
 * language with none is asked about again after a while, so installing gopls
 * doesn't take a bridge restart.
 */
export class LanguageServerResolver {
    private found = new Map<string, ResolvedLanguageServer>();
    private missing = new Map<string, number>();
    private inFlight = new Map<string, Promise<ResolvedLanguageServer | null>>();

    constructor(private opts: ResolverOptions) {}

    resolve(lang: string): Promise<ResolvedLanguageServer | null> {
        const known = this.found.get(lang);
        if (known) return Promise.resolve(known);
        const missingSince = this.missing.get(lang);
        if (missingSince !== undefined && this.opts.now() - missingSince < this.opts.retryMissingMs)
            return Promise.resolve(null);
        const running = this.inFlight.get(lang);
        if (running) return running;

        const lookup = this.lookUp(lang).finally(() => this.inFlight.delete(lang));
        this.inFlight.set(lang, lookup);
        return lookup;
    }

    private async lookUp(lang: string): Promise<ResolvedLanguageServer | null> {
        if (!Object.hasOwn(LANGUAGE_SERVERS, lang)) return null;
        const spec = LANGUAGE_SERVERS[lang];
        for (const command of spec.commands) {
            const path = this.opts.which(command.cmd);
            if (!path) continue;
            if (command.check && !(await this.opts.runs([path, ...command.check]))) {
                console.log(`[LSP] ${path} is on PATH but \`${command.check.join(' ')}\` fails`);
                continue;
            }
            const resolved = { lang, argv: [path, ...command.args], name: basename(command.cmd) };
            this.found.set(lang, resolved);
            this.missing.delete(lang);
            return resolved;
        }
        this.missing.set(lang, this.opts.now());
        return null;
    }
}

/** Runs a command briefly, for a LanguageServerCommand's check. */
export async function commandRuns(argv: string[], timeoutMs = 10_000): Promise<boolean> {
    try {
        const proc = Bun.spawn(argv, { stdin: 'ignore', stdout: 'ignore', stderr: 'ignore' });
        const timer = setTimeout(() => proc.kill(), timeoutMs);
        const code = await proc.exited;
        clearTimeout(timer);
        return code === 0;
    } catch {
        return false;
    }
}

/** What the code viewer needs to talk to a file's language server. */
export interface LspWorkspace {
    available: boolean;
    /** The LANGUAGE_SERVERS key; empty for a file no server handles. */
    lang: string;
    languageId: string;
    /** Where the server is rooted (see workspaceRoot). */
    root: string;
    /** Which server it is, e.g. "gopls". */
    server: string;
    /** Whether the file is outside the checkout. */
    outside: boolean;
}

export async function lspWorkspaceFor(
    resolver: LanguageServerResolver,
    checkout: string,
    file: string
): Promise<LspWorkspace> {
    const top = resolve(checkout);
    const path = resolve(top, file);
    const outside = !isWithin(top, path);
    const language = languageForPath(path);
    if (!language) {
        return { available: false, lang: '', languageId: '', root: top, server: '', outside };
    }
    const spec = LANGUAGE_SERVERS[language.lang];
    const [server, root] = await Promise.all([
        resolver.resolve(language.lang),
        workspaceRoot(top, path, spec.rootMarkers),
    ]);
    return {
        available: !!server,
        lang: language.lang,
        languageId: language.languageId,
        root,
        server: server?.name ?? spec.commands[0].cmd,
        outside,
    };
}
