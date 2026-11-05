export type LineType = 'code' | 'addition' | 'deletion' | 'hunk' | 'file-header' | 'skip';

export interface ParsedLine {
    text: string;
    file: string | null;
    pos: number | null;
    clickable: boolean;
    lineType: LineType;
    fileStatus?: 'modified' | 'new' | 'deleted' | 'renamed';
    origName?: string;
    originalLineIndex: number;
    // Source-side line numbers (old = base, new = head). null when N/A.
    oldLineNo?: number | null;
    newLineNo?: number | null;
    // For hunk headers: parsed function/context that appears after "@@".
    hunkContext?: string;
    // For hunk headers: this hunk picks up on the line right after the one
    // above it ends (see coalesceTouchingHunks), so the diff view draws the
    // two as one hunk, under the first one's header, whose `text` then spans
    // them both. The header still holds its comment position: GitHub counts
    // it, and the comments below it are anchored by that count.
    coalesced?: boolean;
}

// A hunk header's two ranges, `@@ -oldStart,oldLines +newStart,newLines @@`,
// and the text git puts after it (the enclosing function, say), without its
// leading space. A range that leaves out its length is one line long.
export interface HunkRange {
    oldStart: number;
    oldLines: number;
    newStart: number;
    newLines: number;
    context: string;
}

export const parseHunkHeader = (line: string): HunkRange | null => {
    const m = line.replace(/\r$/, '').match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$/);
    if (!m) return null;
    return {
        oldStart: parseInt(m[1], 10),
        oldLines: m[2] !== undefined ? parseInt(m[2], 10) : 1,
        newStart: parseInt(m[3], 10),
        newLines: m[4] !== undefined ? parseInt(m[4], 10) : 1,
        context: m[5].replace(/^\s+/, ''),
    };
};

// A header for `hunk`, written the way the server writes them.
const formatHunkHeader = (hunk: HunkRange): string =>
    `@@ -${hunk.oldStart},${hunk.oldLines} +${hunk.newStart},${hunk.newLines} @@` +
    (hunk.context ? ` ${hunk.context}` : '');

// Whether `below` starts on the line right after `above` ends, on both sides:
// no line lies between them, so together they read as one hunk.
const hunksTouch = (above: HunkRange, below: HunkRange): boolean =>
    above.oldStart + above.oldLines === below.oldStart &&
    above.newStart + above.newLines === below.newStart;

/**
 * Join each run of touching hunks into one, in place. Git never writes two
 * hunks with nothing between them, but expanding one hunk's context up to its
 * neighbour leaves them so (see planHunkExpansion). Every header after the
 * first in a run is marked `coalesced`, and the first one's text is rewritten
 * to span the run; positions are left alone.
 */
const coalesceTouchingHunks = (rows: ParsedLine[]) => {
    let run: { head: ParsedLine; range: HunkRange } | null = null;
    for (const row of rows) {
        if (row.lineType !== 'hunk') {
            if (row.lineType === 'file-header') run = null;
            continue;
        }
        const hunk = parseHunkHeader(row.text);
        if (hunk && run && hunksTouch(run.range, hunk)) {
            row.coalesced = true;
            run.range = {
                ...run.range,
                oldLines: hunk.oldStart + hunk.oldLines - run.range.oldStart,
                newLines: hunk.newStart + hunk.newLines - run.range.newStart,
            };
            run.head.text = formatHunkHeader(run.range);
        } else {
            run = hunk ? { head: row, range: hunk } : null;
        }
    }
};

/**
 * Parse a unified diff into renderable lines, tracking each line's GitHub
 * comment "position".
 *
 * `expandedLineIndices` holds indices (into `diff.split('\n')`) of context lines
 * that were fetched on demand via hunk expansion. They are displayed but are NOT
 * part of the PR's canonical diff, so they must not consume a comment position —
 * otherwise every line below an expansion shifts and comments created while a
 * hunk is expanded are stored at a position that no longer exists once the
 * canonical diff is restored.
 *
 * Expansion can also close the gap between two hunks; those are drawn as one
 * (see ParsedLine.coalesced), though the second header still counts.
 */
export const parseDiff = (
    diff: string,
    expandedLineIndices?: ReadonlySet<number>
): ParsedLine[] => {
    const parsedLines: ParsedLine[] = [];

    // Add Diff lines and track positions for comments
    if (diff) {
        const lines = diff.split('\n');
        let currentFile: string | null = null;
        let currentPos = 0;
        let foundFirstHunkInFile = false;
        let pendingFileStatus: 'modified' | 'new' | 'deleted' | 'renamed' = 'modified';

        // New state tracking for empty new files
        let fallbackFilename: string | null = null;
        let fallbackOrigName: string | null = null;
        let fallbackFileIndex: number | null = null;
        let hasEmittedHeader = false;

        // Per-hunk line-number cursors (old = base side, new = head side).
        let oldLineCursor = 0;
        let newLineCursor = 0;
        let currentHunkContext = '';

        lines.forEach((rawLine, index) => {
            const line = rawLine.replace(/\r$/, '');
            let clickable = false;
            let pos: number | null = null;
            let file: string | null = null;
            // Default to 'skip' — only lines that match a known diff
            // pattern (file header, hunk header, +/- /context) are
            // emitted. Without this, the trailing empty element from
            // split('\n'), "\ No newline at end of file" markers, and
            // any unrecognized line would silently become phantom
            // 'code' rows with empty line-number gutters.
            let lineType: LineType = 'skip';
            let lineOldNo: number | null = null;
            let lineNewNo: number | null = null;

            // Check for diff --git header to determine file status
            if (line.startsWith('diff --git')) {
                // Check if previous file needs a header
                if (fallbackFilename && !hasEmittedHeader) {
                    parsedLines.push({
                        text: fallbackFilename,
                        file: fallbackFilename,
                        pos: 0,
                        clickable: true,
                        lineType: 'file-header',
                        fileStatus: pendingFileStatus,
                        origName: fallbackOrigName || undefined,
                        originalLineIndex: fallbackFileIndex !== null ? fallbackFileIndex : index,
                    });
                }

                // Reset for new file
                hasEmittedHeader = false;
                pendingFileStatus = 'modified';
                fallbackOrigName = null;
                lineType = 'skip';

                // Parse filename from diff --git a/path b/path
                // Try exact match first (handles spaces)
                const sameNameMatch = line.match(/^diff --git a\/(.+) b\/\1$/);
                if (sameNameMatch) {
                    fallbackFilename = sameNameMatch[1];
                } else {
                    // Handle quoted filenames: diff --git "a/foo" "b/foo"
                    const quotedMatch = line.match(/^diff --git "a\/(.+)" "b\/(.+)"$/);
                    if (quotedMatch && quotedMatch[1] === quotedMatch[2]) {
                        fallbackFilename = quotedMatch[1];
                    } else {
                        // Fallback: try to grab the b/ part
                        const parts = line.split(' b/');
                        if (parts.length >= 2) {
                            fallbackFilename = parts.slice(1).join(' b/');
                            // Cleanup potential trailing quote from split if it was quoted "b/..."
                            if (fallbackFilename.endsWith('"') && line.includes('" b/')) {
                                fallbackFilename = fallbackFilename.slice(0, -1);
                            }
                        } else {
                            fallbackFilename = null;
                        }
                    }
                }
                fallbackFileIndex = index;
            } else if (line.startsWith('new file mode')) {
                pendingFileStatus = 'new';
                lineType = 'skip';
            } else if (line.startsWith('deleted file mode')) {
                pendingFileStatus = 'deleted';
                lineType = 'skip';
            } else if (line.startsWith('rename from ')) {
                pendingFileStatus = 'renamed';
                fallbackOrigName = line.slice('rename from '.length).trim();
                lineType = 'skip';
            } else if (line.startsWith('rename to ')) {
                pendingFileStatus = 'renamed';
                fallbackFilename = line.slice('rename to '.length).trim();
                lineType = 'skip';
            } else if (line.startsWith('similarity index')) {
                pendingFileStatus = 'renamed';
                lineType = 'skip';
            } else if (line.startsWith('index ') || line.startsWith('---')) {
                // Detect --- /dev/null: indicates a new file
                if (line === '--- /dev/null') {
                    pendingFileStatus = 'new';
                }
                lineType = 'skip';
            } else {
                // Match +++ b/filename as the file header
                const fileMatch = line.match(/^\+\+\+\s+b\/(.+)$/) || line.match(/^\+\+\+\s+(.+)$/);

                if (fileMatch) {
                    const matchedFile = (fileMatch[1] || fileMatch[2]).trim();

                    if (matchedFile === '/dev/null') {
                        // +++ /dev/null means this is a deleted file.
                        // Use fallbackFilename (from diff --git) as the displayed name.
                        if (fallbackFilename) {
                            currentFile = fallbackFilename;
                            currentPos = 0;
                            foundFirstHunkInFile = false;
                            pos = 0;
                            file = currentFile;
                            clickable = true;
                            lineType = 'file-header';
                            hasEmittedHeader = true;
                            parsedLines.push({
                                text: currentFile,
                                file,
                                pos,
                                clickable,
                                lineType,
                                fileStatus: 'deleted',
                                originalLineIndex: index,
                            });
                            pendingFileStatus = 'modified';
                        }
                        return;
                    }

                    currentFile = matchedFile;
                    currentPos = 0;
                    foundFirstHunkInFile = false;

                    // Allow comments on the file header itself (general file comments)
                    pos = 0;
                    file = currentFile;
                    clickable = true;
                    lineType = 'file-header';

                    hasEmittedHeader = true;

                    parsedLines.push({
                        text: currentFile,
                        file,
                        pos,
                        clickable,
                        lineType,
                        fileStatus: pendingFileStatus,
                        origName: fallbackOrigName || undefined,
                        originalLineIndex: index,
                    });
                    pendingFileStatus = 'modified'; // Reset for next file
                    fallbackOrigName = null;
                    return; // continue equivalent in forEach
                } else if (currentFile) {
                    const isHunkHeader = line.startsWith('@@');
                    const isAddition = line.startsWith('+') && !line.startsWith('+++');
                    const isDeletion = line.startsWith('-') && !line.startsWith('---');
                    const isContextLine = line.length > 0 && line[0] === ' ';

                    if (isHunkHeader) {
                        // For the first hunk in a file, the backend resets diffPosCount to 0.
                        // Subsequent hunks consume a position but aren't valid comment targets.
                        if (!foundFirstHunkInFile) {
                            foundFirstHunkInFile = true;
                            // Don't increment for first hunk - mimics backend reset to 0
                        } else {
                            // Subsequent hunks consume a position in the diff
                            currentPos++;
                        }
                        pos = currentPos;
                        file = currentFile;
                        // Hunk headers are not valid GitHub comment positions
                        clickable = false;
                        lineType = 'hunk';

                        // Seed per-side line cursors from the hunk header so we can
                        // render real line numbers alongside the diff.
                        const hunk = parseHunkHeader(line);
                        if (hunk) {
                            oldLineCursor = hunk.oldStart;
                            newLineCursor = hunk.newStart;
                            currentHunkContext = hunk.context;
                        }
                    } else if (isAddition || isDeletion || isContextLine) {
                        file = currentFile;

                        if (expandedLineIndices?.has(index)) {
                            // On-demand expanded context: render it and keep
                            // the gutter line numbers advancing, but do NOT
                            // consume a comment position or allow commenting,
                            // since it has no position in the canonical diff.
                            clickable = false;
                            lineType = 'code';
                            lineOldNo = oldLineCursor++;
                            lineNewNo = newLineCursor++;
                        } else {
                            currentPos++;
                            pos = currentPos;
                            clickable = true;
                            lineType = isAddition ? 'addition' : isDeletion ? 'deletion' : 'code';

                            if (isAddition) {
                                lineNewNo = newLineCursor++;
                            } else if (isDeletion) {
                                lineOldNo = oldLineCursor++;
                            } else {
                                lineOldNo = oldLineCursor++;
                                lineNewNo = newLineCursor++;
                            }
                        }
                    }
                }
            }

            if (lineType !== 'skip') {
                parsedLines.push({
                    text: line,
                    file,
                    pos,
                    clickable,
                    lineType,
                    originalLineIndex: index,
                    oldLineNo: lineOldNo,
                    newLineNo: lineNewNo,
                    hunkContext: lineType === 'hunk' ? currentHunkContext : undefined,
                });
            }
        });

        // Check if last file needs header
        if (fallbackFilename && !hasEmittedHeader) {
            parsedLines.push({
                text: fallbackFilename,
                file: fallbackFilename,
                pos: 0,
                clickable: true,
                lineType: 'file-header',
                fileStatus: pendingFileStatus,
                origName: fallbackOrigName || undefined,
                originalLineIndex: fallbackFileIndex !== null ? fallbackFileIndex : lines.length,
            });
        }
    }

    coalesceTouchingHunks(parsedLines);
    return parsedLines;
};

export interface HunkExpansion {
    // Index into the diff's lines of the hunk header to grow, and its ranges.
    headerIndex: number;
    hunk: HunkRange;
    // How many lines of context to ask for.
    count: number;
    // Where in the diff's lines the fetched lines go.
    insertAt: number;
}

/**
 * Plan growing the hunk headed at `lines[hunkLineIndex]` by up to `count`
 * lines of context `direction`-wards.
 *
 * That is a header the diff view draws: the first of a run of touching hunks,
 * which it shows as one (see coalesceTouchingHunks). Growing the run downward
 * grows its last hunk, upward its first.
 *
 * The context stops where the next hunk that way begins. The server reads it
 * from the file and knows nothing of other hunks, so asking for more would
 * show that hunk's lines a second time; asking for exactly the lines between
 * leaves the two touching, to be drawn as one. null when there is nothing to
 * fetch: no hunk header there, or no line between it and its neighbour.
 */
export const planHunkExpansion = (
    lines: string[],
    hunkLineIndex: number,
    direction: 'before' | 'after',
    count: number
): HunkExpansion | null => {
    const step = direction === 'after' ? 1 : -1;
    // The closest line to `from` that way heading a hunk or a file, or -1 /
    // lines.length past the diff's edge.
    const nextHeader = (from: number): number => {
        let i = from + step;
        while (
            i >= 0 &&
            i < lines.length &&
            !lines[i].startsWith('@@ ') &&
            !lines[i].startsWith('diff --git ')
        ) {
            i += step;
        }
        return i;
    };

    let headerIndex = hunkLineIndex;
    let hunk = parseHunkHeader(lines[headerIndex] ?? '');
    if (!hunk) return null;
    let room = Infinity;
    for (;;) {
        const next = nextHeader(headerIndex);
        const neighbour = lines[next]?.startsWith('@@ ') ? parseHunkHeader(lines[next]) : null;
        if (!neighbour) break;
        const [above, below] = direction === 'after' ? [hunk, neighbour] : [neighbour, hunk];
        if (!hunksTouch(above, below)) {
            room = below.newStart - (above.newStart + above.newLines);
            break;
        }
        headerIndex = next;
        hunk = neighbour;
    }
    if (room <= 0) return null;

    return {
        headerIndex,
        hunk,
        count: Math.min(count, room),
        // Right under the header going up; after the hunk's last line going down.
        insertAt: direction === 'before' ? headerIndex + 1 : nextHeader(headerIndex),
    };
};

export const isTestFile = (filename: string): boolean =>
    /\.(test|spec)\.[jt]sx?$/.test(filename) ||
    /_test\.[jt]sx?$/.test(filename) ||
    /_test\.go$/.test(filename) ||
    /\/__tests__\//.test(filename);

/**
 * Reorder parsed diff lines so files that look like tests render after
 * non-test files, preserving relative order within each group.
 */
export const sortParsedLinesTestsLast = (lines: ParsedLine[]): ParsedLine[] => {
    const groups: ParsedLine[][] = [];
    let currentGroup: ParsedLine[] = [];
    for (const line of lines) {
        if (line.lineType === 'file-header') {
            if (currentGroup.length > 0) groups.push(currentGroup);
            currentGroup = [line];
        } else {
            currentGroup.push(line);
        }
    }
    if (currentGroup.length > 0) groups.push(currentGroup);
    groups.sort((a, b) => {
        const aIsTest = a[0].file ? isTestFile(a[0].file) : false;
        const bIsTest = b[0].file ? isTestFile(b[0].file) : false;
        if (aIsTest === bIsTest) return 0;
        return aIsTest ? 1 : -1;
    });
    return groups.flat();
};
