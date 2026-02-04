export type LineType = 'code' | 'addition' | 'deletion' | 'hunk' | 'file-header' | 'skip';

export interface ParsedLine {
    text: string;
    file: string | null;
    pos: number | null;
    clickable: boolean;
    lineType: LineType;
    fileStatus?: 'modified' | 'new' | 'deleted' | 'renamed';
    originalLineIndex: number;
}

export const parseDiff = (diff: string): ParsedLine[] => {
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
        let fallbackFileIndex: number | null = null;
        let hasEmittedHeader = false;

        lines.forEach((rawLine, index) => {
            const line = rawLine.replace(/\r$/, '');
            let clickable = false;
            let pos: number | null = null;
            let file: string | null = null;
            let lineType: LineType = 'code';

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
                        originalLineIndex: fallbackFileIndex !== null ? fallbackFileIndex : index
                    });
                }

                // Reset for new file
                hasEmittedHeader = false;
                pendingFileStatus = 'modified';
                lineType = 'skip';

                // Parse filename from diff --git a/path b/path
                // Try exact match first (handles spaces)
                const sameNameMatch = line.match(/^diff --git a\/(.+) b\/\1$/);
                if (sameNameMatch) {
                    fallbackFilename = sameNameMatch[1];
                } else {
                    // Fallback: try to grab the b/ part
                        const parts = line.split(' b/');
                        if (parts.length >= 2) {
                            fallbackFilename = parts.slice(1).join(' b/');
                        } else {
                            fallbackFilename = null;
                        }
                }
                fallbackFileIndex = index;

            } else if (line.startsWith('new file mode')) {
                pendingFileStatus = 'new';
                lineType = 'skip';
            } else if (line.startsWith('deleted file mode')) {
                pendingFileStatus = 'deleted';
                lineType = 'skip';
            } else if (line.startsWith('rename from') || line.startsWith('rename to') || line.startsWith('similarity index')) {
                pendingFileStatus = 'renamed';
                lineType = 'skip';
            } else if (line.startsWith('index ') || line.startsWith('---')) {
                // Skip these git metadata lines
                lineType = 'skip';
            } else {
                // Match +++ b/filename as the file header
                const fileMatch = line.match(/^\+\+\+\s+b\/(.+)$/) ||
                    line.match(/^\+\+\+\s+(.+)$/) ||
                    line.match(/^\s*(modified|deleted|new file|renamed)\s+(.+)$/);

                if (fileMatch) {
                    currentFile = (fileMatch[1] || fileMatch[2]).trim();
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
                        originalLineIndex: index
                    });
                    pendingFileStatus = 'modified'; // Reset for next file
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
                    } else if (isAddition || isDeletion || isContextLine) {
                        currentPos++;
                        pos = currentPos;
                        file = currentFile;
                        clickable = true;
                        lineType = isAddition ? 'addition' : isDeletion ? 'deletion' : 'code';
                    }
                }
            }

            if (lineType !== 'skip') {
                parsedLines.push({ text: line, file, pos, clickable, lineType, originalLineIndex: index });
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
                originalLineIndex: fallbackFileIndex !== null ? fallbackFileIndex : lines.length
            });
        }
    }

    return parsedLines;
};
