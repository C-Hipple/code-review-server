// Builds the unpacked extension into dist/:
//
//   manifest.json, icons/        copied
//   background.js                service worker (ESM; the manifest says type: module)
//   content.js                   content script (IIFE: content scripts are classic scripts)
//   panel.html                   copied from src/panel/
//   panel/main.js, chunk-*.js    the panel; splitting puts lazily imported mermaid in its own chunk
//   panel/panel.css              src/panel/panel.css with its @imports bundled
//
// then checks that every file the manifest and panel.html name exists.
//
//   bun scripts/build.ts          minified, no source maps
//   bun scripts/build.ts --dev    unminified, inline source maps

import { cp, readdir, readFile, rm, stat } from 'node:fs/promises';
import path from 'node:path';

const root = path.resolve(import.meta.dir, '..');
const dist = path.join(root, 'dist');
const dev = process.argv.includes('--dev');

type BuildConfig = Parameters<typeof Bun.build>[0];

async function build(config: BuildConfig): Promise<Bun.BuildOutput> {
    const result = await Bun.build({
        target: 'browser',
        minify: !dev,
        sourcemap: dev ? 'inline' : 'none',
        define: { 'process.env.NODE_ENV': JSON.stringify(dev ? 'development' : 'production') },
        ...config,
    });
    if (!result.success) {
        for (const log of result.logs) console.error(log);
        throw new Error(`build failed: ${config.entrypoints.join(', ')}`);
    }
    return result;
}

const src = (p: string) => path.join(root, 'src', p);

await rm(dist, { recursive: true, force: true });

await build({
    entrypoints: [src('background.ts')],
    outdir: dist,
    format: 'esm',
    naming: '[name].[ext]',
});

await build({
    entrypoints: [src('content.ts')],
    outdir: dist,
    format: 'iife',
    naming: '[name].[ext]',
});

const panel = await build({
    entrypoints: [src('panel/main.tsx')],
    outdir: path.join(dist, 'panel'),
    format: 'esm',
    splitting: true,
    naming: { entry: '[name].[ext]', chunk: 'chunk-[hash].[ext]', asset: '[name]-[hash].[ext]' },
});
// panel.html links panel.css alone, so CSS imported from TypeScript would be
// built but never loaded.
const strayCss = panel.outputs.filter(o => o.path.endsWith('.css'));
if (strayCss.length > 0) {
    throw new Error(
        `CSS imported from TypeScript (${strayCss.map(o => path.basename(o.path)).join(', ')}); @import it from src/panel/panel.css instead`
    );
}

await build({
    entrypoints: [src('panel/panel.css')],
    outdir: path.join(dist, 'panel'),
    naming: '[name].[ext]',
});

await cp(path.join(root, 'manifest.json'), path.join(dist, 'manifest.json'));
await cp(path.join(root, 'icons'), path.join(dist, 'icons'), { recursive: true });
await cp(src('panel/panel.html'), path.join(dist, 'panel.html'));

await verify();
await report();

// --- Checks ------------------------------------------------------------------

async function exists(rel: string): Promise<boolean> {
    try {
        return (await stat(path.join(dist, rel))).isFile();
    } catch {
        return false;
    }
}

/** Files under dist/, relative and with forward slashes. */
async function listDist(): Promise<string[]> {
    const entries = await readdir(dist, { recursive: true, withFileTypes: true });
    return entries
        .filter(e => e.isFile())
        .map(e => path.relative(dist, path.join(e.parentPath, e.name)).split(path.sep).join('/'))
        .sort();
}

function globToRegExp(glob: string): RegExp {
    const escaped = glob.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*');
    return new RegExp(`^${escaped}$`);
}

async function verify(): Promise<void> {
    const manifest = JSON.parse(await readFile(path.join(dist, 'manifest.json'), 'utf8'));
    const files = await listDist();
    const missing: string[] = [];
    const need = async (rel: string, from: string) => {
        if (!(await exists(rel))) missing.push(`${rel} (${from})`);
    };

    await need(manifest.background.service_worker, 'background');
    for (const script of manifest.content_scripts) {
        for (const js of script.js ?? []) await need(js, 'content_scripts');
    }
    for (const icon of Object.values<string>(manifest.icons)) await need(icon, 'icons');
    for (const icon of Object.values<string>(manifest.action.default_icon)) {
        await need(icon, 'action.default_icon');
    }
    for (const war of manifest.web_accessible_resources) {
        for (const resource of war.resources as string[]) {
            if (resource.includes('*')) {
                const re = globToRegExp(resource);
                if (!files.some(f => re.test(f))) {
                    missing.push(`${resource} (web_accessible_resources matches nothing)`);
                }
            } else {
                await need(resource, 'web_accessible_resources');
            }
        }
    }

    // Everything panel.html loads must exist, and be web accessible so the
    // iframe on github.com can load it.
    const html = await readFile(path.join(dist, 'panel.html'), 'utf8');
    const accessible = manifest.web_accessible_resources.flatMap((w: { resources: string[] }) =>
        w.resources.map(globToRegExp)
    );
    for (const [, ref] of html.matchAll(/\b(?:src|href)="([^"]+)"/g)) {
        await need(ref, 'panel.html');
        if (!accessible.some((re: RegExp) => re.test(ref))) {
            missing.push(`${ref} (panel.html; not in web_accessible_resources)`);
        }
    }

    const content = await readFile(path.join(dist, 'content.js'), 'utf8');
    if (/^\s*(?:import|export)\b/m.test(content)) {
        missing.push('content.js is an ES module; content scripts must be classic scripts');
    }

    if (missing.length > 0) {
        throw new Error(`dist/ is incomplete:\n  ${missing.join('\n  ')}`);
    }
}

async function report(): Promise<void> {
    for (const rel of await listDist()) {
        const { size } = await stat(path.join(dist, rel));
        console.log(`${(size / 1024).toFixed(1).padStart(9)} KiB  dist/${rel}`);
    }
}
