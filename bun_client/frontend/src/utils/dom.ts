import React from 'react';

/**
 * Compute the text column at a click position using caretRangeFromPoint.
 * Traverses text nodes within the container to calculate the offset.
 */
export const getClickColumn = (e: React.MouseEvent, container: HTMLElement): number => {
    let range: Range | null = null;

    // Standard
    if (document.caretRangeFromPoint) {
        range = document.caretRangeFromPoint(e.clientX, e.clientY);
    } else if ((document as any).caretPositionFromPoint) {
        // Firefox
        const pos = (document as any).caretPositionFromPoint(e.clientX, e.clientY);
        if (pos) {
            range = document.createRange();
            range.setStart(pos.offsetNode, pos.offset);
            range.collapse(true);
        }
    }

    if (!range) return 0;

    // Traverse text nodes to calculate offset relative to container
    const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
    let offset = 0;
    while (walker.nextNode()) {
        const node = walker.currentNode;
        if (node === range.startContainer) {
            return offset + range.startOffset;
        }
        offset += node.textContent?.length || 0;
    }
    return 0;
};
