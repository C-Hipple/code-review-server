import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import Markdown from 'react-markdown';
import type { PluginBody } from '../plugin_utils';

interface PluginBodyViewProps {
    body: PluginBody;
    /** Rendered when the body has no content. */
    emptyLabel?: string;
}

// Theme variables the sandboxed HTML frame inherits from the app, so an HTML
// body doesn't render as a white box on a dark theme.
const FRAME_VARS = [
    '--bg-primary',
    '--bg-tertiary',
    '--text-primary',
    '--text-secondary',
    '--accent',
    '--border',
    '--font-mono',
];

const MIN_FRAME_HEIGHT = 32;
const MAX_FRAME_HEIGHT = 600;

function frameCssVars(): string {
    const styles = getComputedStyle(document.documentElement);
    return FRAME_VARS.map(name => `${name}:${styles.getPropertyValue(name).trim()};`).join('');
}

function buildFrameDocument(html: string): string {
    return `<!doctype html><html><head><meta charset="utf-8"><style>
:root{${frameCssVars()}}
html,body{margin:0;padding:0;background:transparent;color:var(--text-primary);
  font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;font-size:14px;line-height:1.6;
  overflow-wrap:break-word;}
h1,h2,h3,h4{margin:1em 0 .5em;color:var(--text-primary);}
h1{font-size:1.4em;}h2{font-size:1.2em;}h3{font-size:1.1em;}
p{margin:.5em 0;}
ul,ol{margin:.5em 0;padding-left:1.5em;}
li{margin:.25em 0;}
a{color:var(--accent);}
code{font-family:var(--font-mono);background:var(--bg-tertiary);padding:.15em .4em;border-radius:4px;font-size:.9em;}
pre{font-family:var(--font-mono);background:var(--bg-tertiary);padding:12px;border-radius:6px;overflow-x:auto;margin:.5em 0;}
pre code{background:none;padding:0;}
blockquote{border-left:3px solid var(--accent);margin:.5em 0;padding-left:1em;color:var(--text-secondary);}
hr{border:none;border-top:1px solid var(--border);margin:1em 0;}
table{border-collapse:collapse;width:100%;margin:.5em 0;}
th,td{border:1px solid var(--border);padding:8px;text-align:left;}
th{background:var(--bg-tertiary);}
img{max-width:100%;}
</style></head><body>${html}</body></html>`;
}

/**
 * Renders a plugin's HTML body inside a sandboxed frame.
 *
 * Plugin bodies are frequently LLM-generated text derived from a PR's diff and
 * description, so they are not trusted markup. The frame withholds
 * `allow-scripts`, which blocks script execution and inline event handlers; it
 * keeps `allow-same-origin` only so the parent can measure the content to size
 * the frame (harmless with scripting off, since nothing inside can run).
 */
function HtmlFrame({ html }: { html: string }) {
    const frameRef = useRef<HTMLIFrameElement>(null);
    const [height, setHeight] = useState(MIN_FRAME_HEIGHT);
    // Re-read the theme's colors whenever the app's theme changes.
    const [theme, setTheme] = useState(() => document.documentElement.dataset.theme ?? '');

    const srcDoc = useMemo(() => {
        // `theme` is not read directly: it re-derives the document so the frame
        // picks up the new theme's variables.
        void theme;
        return buildFrameDocument(html);
    }, [html, theme]);

    useEffect(() => {
        const observer = new MutationObserver(() =>
            setTheme(document.documentElement.dataset.theme ?? '')
        );
        observer.observe(document.documentElement, {
            attributes: true,
            attributeFilter: ['data-theme'],
        });
        return () => observer.disconnect();
    }, []);

    const measure = useCallback(() => {
        const frame = frameRef.current;
        const doc = frame?.contentDocument;
        if (!frame || !doc?.documentElement) return;
        // A document can't report a height below the frame it lives in, so
        // collapse the frame before reading: measuring in place would ratchet,
        // leaving blank space when a re-run produces shorter output.
        frame.style.height = '0px';
        const measured = doc.documentElement.scrollHeight;
        const next = Math.min(Math.max(measured, MIN_FRAME_HEIGHT), MAX_FRAME_HEIGHT);
        frame.style.height = `${next}px`;
        setHeight(next);
    }, []);

    // The frame's content can't resize itself (no scripting), but it does reflow
    // when the panel around it changes width.
    useEffect(() => {
        const frame = frameRef.current;
        if (!frame || typeof ResizeObserver === 'undefined') return;
        const observer = new ResizeObserver(() => measure());
        observer.observe(frame);
        return () => observer.disconnect();
    }, [measure]);

    return (
        <iframe
            ref={frameRef}
            title="Plugin HTML output"
            sandbox="allow-same-origin"
            srcDoc={srcDoc}
            onLoad={measure}
            style={{
                display: 'block',
                width: '100%',
                height: `${height}px`,
                border: 'none',
                background: 'transparent',
                colorScheme: 'normal',
            }}
        />
    );
}

/**
 * Renders the body of a plugin response according to its declared body_type.
 * Markdown is rendered inline (react-markdown ignores embedded raw HTML); an
 * `html` body goes through the sandboxed frame above.
 */
export default function PluginBodyView({ body, emptyLabel = 'No output' }: PluginBodyViewProps) {
    if (!body.body_content) {
        return (
            <span
                style={{
                    color: 'var(--text-secondary)',
                    fontStyle: 'italic',
                    fontSize: '14px',
                }}
            >
                {emptyLabel}
            </span>
        );
    }

    if (body.body_type === 'html') {
        return <HtmlFrame html={body.body_content} />;
    }

    return <Markdown>{body.body_content}</Markdown>;
}
