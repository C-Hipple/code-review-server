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
}

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
                        const m = line.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$/);
                        if (m) {
                            oldLineCursor = parseInt(m[1], 10);
                            newLineCursor = parseInt(m[3], 10);
                            currentHunkContext = (m[5] || '').replace(/^\s+/, '');
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

    return parsedLines;
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
