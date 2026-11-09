// Links to a line of a PR's diff on GitHub, the ones its Files changed view
// copies when a line number is clicked:
//
//   https://github.com/OWNER/REPO/pull/N/files#diff-<sha256(path)>R12
//
// R is the head side (an added or context line, by its new line number), L
// the base side (a removed line, by its old one). The anchor names the file
// by the hex SHA-256 of its path in the head.

export type DiffSide = 'L' | 'R';

// SHA-256 round constants: the first 32 bits of the fractional parts of the
// cube roots of the first 64 primes.
const K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

/**
 * The hex SHA-256 of `text`'s UTF-8 bytes. Synchronous and in plain JS rather
 * than `crypto.subtle`, which a browser only offers in a secure context — and
 * the review opened from a phone over the LAN is plain http.
 */
export function sha256Hex(text: string): string {
    const bytes = new TextEncoder().encode(text);
    // Message, a 0x80 byte, zero padding, then the bit length in 8 bytes,
    // filling a whole number of 64-byte blocks.
    const blocks = Math.ceil((bytes.length + 9) / 64);
    const padded = new Uint8Array(blocks * 64);
    padded.set(bytes);
    padded[bytes.length] = 0x80;
    const view = new DataView(padded.buffer);
    const bitLength = bytes.length * 8;
    view.setUint32(padded.length - 8, Math.floor(bitLength / 0x100000000));
    view.setUint32(padded.length - 4, bitLength >>> 0);

    const h = new Uint32Array([
        0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab,
        0x5be0cd19,
    ]);
    const w = new Uint32Array(64);
    const rotr = (x: number, n: number) => (x >>> n) | (x << (32 - n));
    for (let block = 0; block < blocks; block++) {
        for (let i = 0; i < 16; i++) w[i] = view.getUint32(block * 64 + i * 4);
        for (let i = 16; i < 64; i++) {
            const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
            const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
            w[i] = w[i - 16] + s0 + w[i - 7] + s1;
        }
        let [a, b, c, d, e, f, g, hh] = h;
        for (let i = 0; i < 64; i++) {
            const s1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
            const ch = (e & f) ^ (~e & g);
            const t1 = (hh + s1 + ch + K[i] + w[i]) >>> 0;
            const s0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
            const maj = (a & b) ^ (a & c) ^ (b & c);
            const t2 = (s0 + maj) >>> 0;
            hh = g;
            g = f;
            f = e;
            e = (d + t1) >>> 0;
            d = c;
            c = b;
            b = a;
            a = (t1 + t2) >>> 0;
        }
        h[0] += a;
        h[1] += b;
        h[2] += c;
        h[3] += d;
        h[4] += e;
        h[5] += f;
        h[6] += g;
        h[7] += hh;
    }
    return Array.from(h, x => x.toString(16).padStart(8, '0')).join('');
}

/**
 * The GitHub link to line `line` on `side` of `file` in the diff of the PR at
 * `prUrl` (its html URL, `https://github.com/OWNER/REPO/pull/N`). Without a
 * line, the link goes to the file.
 */
export function githubDiffLink(
    prUrl: string,
    file: string,
    side?: DiffSide,
    line?: number | null
): string {
    const base = prUrl.replace(/\/+$/, '').replace(/\/(files|changes|commits)$/, '');
    const anchor = `diff-${sha256Hex(file)}`;
    return line != null && side
        ? `${base}/files#${anchor}${side}${line}`
        : `${base}/files#${anchor}`;
}

/**
 * Put `text` on the clipboard. `navigator.clipboard` only exists in a secure
 * context, so over plain http (a phone on the LAN) this falls back to copying
 * a hidden textarea's selection.
 */
export async function copyText(text: string): Promise<void> {
    if (navigator.clipboard?.writeText && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        return;
    }
    const area = document.createElement('textarea');
    area.value = text;
    area.setAttribute('readonly', '');
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.appendChild(area);
    try {
        area.select();
        if (!document.execCommand('copy')) throw new Error('copy was refused');
    } finally {
        area.remove();
    }
}

/** Where a copied line link points: a side and line, or (both null) the file. */
export interface LineLinkTarget {
    side: DiffSide | null;
    line: number | null;
}

/**
 * Where the link for the line number clicked on `side` of `row` points. The
 * head side's numbers are the head's on any diff, so R takes them as they are.
 * The base side's are only the PR base's on the full diff: on a filtered one
 * they count the baseline's lines, so the row is looked up in the full diff
 * (`fullRows`, through `toFullRow`) and its base line taken from there; a row
 * the full diff shows as an addition links by its head line instead, and one
 * it doesn't show at all, to the file.
 */
export function lineLinkTarget(
    row: {
        file: string | null;
        pos: number | null;
        oldLineNo?: number | null;
        newLineNo?: number | null;
    },
    side: DiffSide,
    filtered?: {
        fullRows: readonly {
            file: string | null;
            pos: number | null;
            oldLineNo?: number | null;
        }[];
        toFullRow: (file: string, pos: number) => number | null;
    }
): LineLinkTarget {
    if (side === 'R') {
        return row.newLineNo != null
            ? { side: 'R', line: row.newLineNo }
            : { side: null, line: null };
    }
    if (!filtered) {
        return row.oldLineNo != null
            ? { side: 'L', line: row.oldLineNo }
            : { side: null, line: null };
    }
    const fullPos = row.file && row.pos != null ? filtered.toFullRow(row.file, row.pos) : null;
    const full =
        fullPos != null
            ? filtered.fullRows.find(r => r.file === row.file && r.pos === fullPos)
            : undefined;
    if (full?.oldLineNo != null) return { side: 'L', line: full.oldLineNo };
    if (row.newLineNo != null) return { side: 'R', line: row.newLineNo };
    return { side: null, line: null };
}
