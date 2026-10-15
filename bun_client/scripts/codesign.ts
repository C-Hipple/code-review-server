import { join } from 'node:path';

// `bun build --compile` writes the bundle into the binary and re-hashes its pages, but leaves the
// hash of the last __LINKEDIT page stale. macOS 27's dyld reads that page at startup, the kernel
// finds the hash wrong and SIGKILLs the process ("Code Signature Invalid"). Ad-hoc re-signing
// rebuilds the whole signature.
if (process.platform === 'darwin') {
    const binary = join(import.meta.dir, '../crs-gui');
    const sign = Bun.spawnSync(['codesign', '--force', '--sign', '-', binary]);
    if (sign.exitCode !== 0) {
        console.error(`codesign failed:\n${sign.stderr.toString()}`);
        process.exit(1);
    }
    console.log('Signed:', binary);
}
