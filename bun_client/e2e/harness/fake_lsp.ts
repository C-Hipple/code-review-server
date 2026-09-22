// A small but honest language server for the e2e suite. It plays two roles,
// picked by the first argument its PATH shim passes:
//
//   diff  — stands in for `diff-lsp`. Like the real one, it reads its init
//           params from the tempfile the bridge hands it (the 5-line header
//           written by /api/prepare-diff-lsp, then the diff) and treats an
//           incoming position's `line` as a 1-indexed line of that tempfile
//           and `character` as a column of the raw diff line (+/- prefix
//           included). A client that gets the header offset or the prefix
//           column wrong resolves the wrong word, which the tests catch.
//   file  — stands in for a per-language server (typescript-language-server)
//           used by the code viewer: plain 0-indexed positions into the
//           document sent with textDocument/didOpen.
//
// Symbols come from scanning the project root's source files for
// declarations, so hover/definition/references/typeDefinition answers point
// at real files and lines. Every message received is appended to
// $CRS_E2E_STATE_DIR/lsp-<role>.jsonl so tests can assert on the positions the
// frontend actually sent.

import { appendFileSync, readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';

const role = process.argv[2] === 'diff' ? 'diff' : 'file';
const stateDir = process.env.CRS_E2E_STATE_DIR;
const logPath = stateDir ? join(stateDir, `lsp-${role}.jsonl`) : null;

function log(entry: Record<string, unknown>) {
    if (!logPath) return;
    appendFileSync(logPath, `${JSON.stringify({ pid: process.pid, ...entry })}\n`);
}

// ---------------------------------------------------------------------------
// Init params

let root = '';
let tempfileLines: string[] = [];
const tempfile = role === 'diff' ? process.argv[3] : undefined;

if (role === 'diff') {
    if (!tempfile) {
        // The real diff-lsp would fall back to the newest /tmp/diff_lsp_* file;
        // the bridge is supposed to always pin one, so treat this as fatal.
        log({ event: 'spawn', argv: process.argv.slice(2), error: 'no tempfile argument' });
        process.exit(2);
    }
    const text = readFileSync(tempfile, 'utf-8');
    tempfileLines = text.split('\n');
    const header: Record<string, string> = {};
    for (const line of tempfileLines.slice(0, 5)) {
        const m = line.match(/^(\w+):\s?(.*)$/);
        if (m) header[m[1]] = m[2];
    }
    root = header.Root ?? '';
    log({ event: 'spawn', argv: process.argv.slice(2), header, tempfile: text });
} else {
    log({ event: 'spawn', argv: process.argv.slice(2) });
}

// ---------------------------------------------------------------------------
// Symbol index over the project root

interface Loc {
    file: string; // absolute path
    line: number; // 0-indexed
    character: number;
}

interface Symbol {
    name: string;
    decl: Loc;
    declText: string;
}

function sourceFiles(dir: string): string[] {
    const out: string[] = [];
    let entries: import('node:fs').Dirent[];
    try {
        entries = readdirSync(dir, { withFileTypes: true });
    } catch {
        return out;
    }
    for (const e of entries) {
        if (e.name === 'node_modules' || e.name.startsWith('.')) continue;
        const p = join(dir, e.name);
        if (e.isDirectory()) out.push(...sourceFiles(p));
        else if (/\.(ts|tsx|js|jsx|go)$/.test(e.name)) out.push(p);
    }
    return out;
}

const DECL = /\b(?:function|interface|const|let|class|type|func)\s+([A-Za-z_]\w*)/g;

function buildIndex(dir: string) {
    const symbols = new Map<string, Symbol>();
    const files = new Map<string, string[]>();
    for (const file of sourceFiles(dir)) {
        const lines = readFileSync(file, 'utf-8').split('\n');
        files.set(file, lines);
        lines.forEach((text, line) => {
            for (const m of text.matchAll(DECL)) {
                if (symbols.has(m[1])) continue;
                symbols.set(m[1], {
                    name: m[1],
                    decl: { file, line, character: m.index! + m[0].length - m[1].length },
                    declText: text.trim().replace(/\s*[{=].*$/, ''),
                });
            }
        });
    }
    return { symbols, files };
}

let index = buildIndex(root);

function references(name: string): Loc[] {
    const out: Loc[] = [];
    const re = new RegExp(`\\b${name}\\b`, 'g');
    for (const [file, lines] of index.files) {
        lines.forEach((text, line) => {
            for (const m of text.matchAll(re)) out.push({ file, line, character: m.index! });
        });
    }
    return out;
}

function toLocation(loc: Loc, length: number) {
    return {
        uri: `file://${loc.file}`,
        range: {
            start: { line: loc.line, character: loc.character },
            end: { line: loc.line, character: loc.character + length },
        },
    };
}

function wordAt(text: string, character: number): string | null {
    if (character < 0 || character >= text.length || !/\w/.test(text[character])) return null;
    let start = character;
    let end = character;
    while (start > 0 && /\w/.test(text[start - 1])) start--;
    while (end < text.length && /\w/.test(text[end])) end++;
    return text.slice(start, end);
}

// ---------------------------------------------------------------------------
// Position resolution

const documents = new Map<string, string>();

interface Resolved {
    word: string | null;
    // Where the position lands in the project, e.g. "src/main.ts:3".
    where: string;
}

// Map a 1-indexed tempfile line back to "<file>:<new-side line>" the way
// diff-lsp's code-review parser does, by walking the diff's hunk headers.
function diffSourceLocation(tempLineIdx: number): string {
    let file = '';
    let newLine = 0;
    for (let i = 5; i <= tempLineIdx && i < tempfileLines.length; i++) {
        const l = tempfileLines[i];
        const plus = l.match(/^\+\+\+ b\/(.*)$/);
        const hunk = l.match(/^@@ -\d+(?:,\d+)? \+(\d+)/);
        if (plus) file = plus[1];
        else if (hunk) newLine = parseInt(hunk[1], 10) - 1;
        else if (l.startsWith('diff --git') || l.startsWith('---') || l.startsWith('index '))
            continue;
        else if (l.startsWith('-')) {
            if (i === tempLineIdx) return `${file}:deleted`;
        } else newLine++;
    }
    return `${file}:${newLine}`;
}

function resolve(params: {
    textDocument: { uri: string };
    position: { line: number; character: number };
}): Resolved {
    const { line, character } = params.position;
    if (role === 'diff') {
        // 1-indexed tempfile line, raw diff column.
        const idx = line - 1;
        const text = tempfileLines[idx] ?? '';
        return { word: wordAt(text, character), where: diffSourceLocation(idx) };
    }
    const doc = documents.get(params.textDocument.uri) ?? '';
    const text = doc.split('\n')[line] ?? '';
    const file = params.textDocument.uri.replace('file://', '');
    return { word: wordAt(text, character), where: `${relative(root, file)}:${line + 1}` };
}

// ---------------------------------------------------------------------------
// Request handling

type Params = Parameters<typeof resolve>[0];

const handlers: Record<string, (params: any) => unknown> = {
    initialize: (params: { rootUri?: string }) => {
        if (role === 'file' && params.rootUri) {
            root = params.rootUri.replace('file://', '');
            index = buildIndex(root);
        }
        return {
            capabilities: {
                hoverProvider: true,
                definitionProvider: true,
                referencesProvider: true,
                typeDefinitionProvider: true,
                textDocumentSync: 1,
            },
            serverInfo: { name: `crs-e2e-fake-${role}` },
        };
    },
    shutdown: () => null,
    'textDocument/hover': (params: Params) => {
        const { word, where } = resolve(params);
        const sym = word ? index.symbols.get(word) : undefined;
        if (!sym) return null;
        return {
            contents: {
                kind: 'markdown',
                value: `\`\`\`typescript\n${sym.declText}\n\`\`\`\n\nhovered \`${word}\` at ${where}`,
            },
        };
    },
    'textDocument/definition': (params: Params) => {
        const { word } = resolve(params);
        const sym = word ? index.symbols.get(word) : undefined;
        return sym ? [toLocation(sym.decl, sym.name.length)] : null;
    },
    'textDocument/references': (params: Params) => {
        const { word } = resolve(params);
        if (!word || !index.symbols.has(word)) return null;
        return references(word).map(l => toLocation(l, word.length));
    },
    // The type of a declaration is the first other declared symbol named
    // after a ':' in it, e.g. formatGreeting(greeting: Greeting) -> Greeting.
    'textDocument/typeDefinition': (params: Params) => {
        const { word } = resolve(params);
        const sym = word ? index.symbols.get(word) : undefined;
        if (!sym) return null;
        for (const m of sym.declText.matchAll(/:\s*([A-Za-z_]\w*)/g)) {
            const target = index.symbols.get(m[1]);
            if (target && target.name !== sym.name) {
                return [toLocation(target.decl, target.name.length)];
            }
        }
        return null;
    },
    // Echoes params back, written in two chunks split inside a multi-byte
    // character, to exercise the bridge's Content-Length reassembly.
    'e2e/echo': (params: unknown) => ({ echo: params, split: true }),
};

const notifications: Record<string, (params: any) => void> = {
    initialized: () => {
        send({
            jsonrpc: '2.0',
            method: 'window/logMessage',
            params: { type: 3, message: `crs-e2e fake ${role} server ready` },
        });
    },
    'textDocument/didOpen': (params: { textDocument: { uri: string; text: string } }) => {
        documents.set(params.textDocument.uri, params.textDocument.text);
    },
    exit: () => process.exit(0),
};

function frame(msg: unknown): Buffer {
    const body = Buffer.from(JSON.stringify(msg), 'utf-8');
    return Buffer.concat([Buffer.from(`Content-Length: ${body.length}\r\n\r\n`), body]);
}

// Writes are chained so a reply sent in two halves (sendSplit) can never have
// another message land in the middle of it.
let writes = Promise.resolve();

function send(msg: unknown) {
    const buf = frame(msg);
    writes = writes.then(() => {
        process.stdout.write(buf);
    });
}

function sendSplit(msg: unknown) {
    const buf = frame(msg);
    // Split in the middle of the first multi-byte UTF-8 sequence if there is
    // one, otherwise in the middle of the body.
    let cut = buf.findIndex(b => b >= 0xc0);
    cut = cut > 0 ? cut + 1 : Math.floor(buf.length / 2);
    writes = writes.then(async () => {
        process.stdout.write(buf.subarray(0, cut));
        await new Promise(r => setTimeout(r, 50));
        process.stdout.write(buf.subarray(cut));
    });
}

function dispatch(msg: { id?: number | string; method?: string; params?: unknown }) {
    log({ event: 'recv', msg });
    if (!msg.method) return;
    if (msg.id === undefined) {
        notifications[msg.method]?.(msg.params);
        return;
    }
    const handler = handlers[msg.method];
    if (!handler) {
        send({
            jsonrpc: '2.0',
            id: msg.id,
            error: { code: -32601, message: `unhandled ${msg.method}` },
        });
        return;
    }
    const reply = { jsonrpc: '2.0', id: msg.id, result: handler(msg.params) };
    if (msg.method === 'e2e/echo') sendSplit(reply);
    else send(reply);
}

let pending = Buffer.alloc(0);
process.stdin.on('data', (chunk: Buffer) => {
    pending = Buffer.concat([pending, chunk]);
    while (true) {
        const sep = pending.indexOf('\r\n\r\n');
        if (sep === -1) return;
        const m = pending
            .subarray(0, sep)
            .toString()
            .match(/Content-Length: (\d+)/i);
        if (!m) {
            pending = pending.subarray(sep + 4);
            continue;
        }
        const len = parseInt(m[1], 10);
        if (pending.length < sep + 4 + len) return;
        const body = pending.subarray(sep + 4, sep + 4 + len).toString('utf-8');
        pending = pending.subarray(sep + 4 + len);
        dispatch(JSON.parse(body));
    }
});
process.stdin.on('end', () => process.exit(0));
