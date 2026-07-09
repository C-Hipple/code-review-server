import type { ParsedLine } from '../../diff_utils';
import { getFileStatusInfo } from './types';

interface FileIndexProps {
    parsed: ParsedLine[];
    onSelectFile: (file: string) => void;
}

// File index — jump to any file in the diff, with per-file +/- counts.
export default function FileIndex({ parsed, onSelectFile }: FileIndexProps) {
    const fileHeaders = parsed.filter(p => p.lineType === 'file-header' && p.file);
    if (fileHeaders.length === 0) return null;

    // Tally additions/deletions per file from the parsed lines.
    const counts = new Map<string, { add: number; del: number }>();
    let curFile: string | null = null;
    for (const p of parsed) {
        if (p.lineType === 'file-header') {
            curFile = p.file;
            if (curFile && !counts.has(curFile)) {
                counts.set(curFile, { add: 0, del: 0 });
            }
        } else if (curFile && counts.has(curFile)) {
            const c = counts.get(curFile)!;
            if (p.lineType === 'addition') c.add++;
            else if (p.lineType === 'deletion') c.del++;
        }
    }

    const totalAdd = [...counts.values()].reduce((s, c) => s + c.add, 0);
    const totalDel = [...counts.values()].reduce((s, c) => s + c.del, 0);

    return (
        <div className="file-index" aria-label="Files changed">
            <span
                style={{
                    fontSize: '11px',
                    color: 'var(--text-tertiary)',
                    textTransform: 'uppercase',
                    letterSpacing: '0.5px',
                    marginRight: '4px',
                    alignSelf: 'center',
                }}
            >
                {fileHeaders.length} file{fileHeaders.length === 1 ? '' : 's'}
            </span>
            {totalAdd > 0 && (
                <span
                    style={{
                        color: 'var(--text-success)',
                        fontSize: '11px',
                        alignSelf: 'center',
                    }}
                >
                    +{totalAdd}
                </span>
            )}
            {totalDel > 0 && (
                <span
                    style={{
                        color: 'var(--text-danger)',
                        fontSize: '11px',
                        alignSelf: 'center',
                    }}
                >
                    −{totalDel}
                </span>
            )}
            {fileHeaders.map(p => {
                if (!p.file) return null;
                const info = getFileStatusInfo(p.fileStatus);
                const c = counts.get(p.file) || { add: 0, del: 0 };
                const shortName = p.file.length > 40 ? '…' + p.file.slice(-39) : p.file;
                return (
                    <button
                        key={p.file}
                        type="button"
                        onClick={() => onSelectFile(p.file!)}
                        className="file-index-item"
                        title={p.file}
                    >
                        <span style={{ color: info.color, fontWeight: 600 }}>{info.icon}</span>
                        <span>{shortName}</span>
                        {c.add > 0 && <span className="delta-add">+{c.add}</span>}
                        {c.del > 0 && <span className="delta-del">−{c.del}</span>}
                    </button>
                );
            })}
        </div>
    );
}
