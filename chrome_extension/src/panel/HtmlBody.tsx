import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { usePrefersDark } from './color_scheme';
import { buildFrameDocument, readFrameVars } from './html_frame';

const MIN_HEIGHT = 32;
const MAX_HEIGHT = 640;

/**
 * An HTML body in a sandboxed iframe. Bodies are often model output derived
 * from the PR, so they are not trusted markup: the sandbox withholds
 * `allow-scripts` (no script, no inline handlers, no forms). It keeps
 * `allow-same-origin` only so the panel can measure the content to size the
 * frame — harmless with nothing able to run inside — and `allow-popups` so
 * links (`<base target=_blank>`) open in a new tab.
 */
export function HtmlBody({ html, title = 'HTML output' }: { html: string; title?: string }) {
    const frame = useRef<HTMLIFrameElement>(null);
    const [height, setHeight] = useState(MIN_HEIGHT);
    // Re-derive the document when the system theme flips, so the copied
    // colors follow it.
    const dark = usePrefersDark();
    const srcDoc = useMemo(() => {
        void dark;
        return buildFrameDocument(html, readFrameVars());
    }, [html, dark]);

    const measure = useCallback(() => {
        const el = frame.current;
        const doc = el?.contentDocument;
        if (!el || !doc?.documentElement) return;
        // Collapse first: a document never reports a height below its frame's.
        el.style.height = '0px';
        const next = Math.min(Math.max(doc.documentElement.scrollHeight, MIN_HEIGHT), MAX_HEIGHT);
        el.style.height = `${next}px`;
        setHeight(next);
    }, []);

    // No script runs inside to resize it, but its content reflows with the width.
    useEffect(() => {
        const el = frame.current;
        if (!el || typeof ResizeObserver === 'undefined') return;
        let width = el.clientWidth;
        const observer = new ResizeObserver(() => {
            if (el.clientWidth !== width) {
                width = el.clientWidth;
                measure();
            }
        });
        observer.observe(el);
        return () => observer.disconnect();
    }, [measure]);

    return (
        <iframe
            ref={frame}
            className="html-body"
            title={title}
            sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
            srcDoc={srcDoc}
            onLoad={measure}
            style={{ height: `${height}px` }}
        />
    );
}
