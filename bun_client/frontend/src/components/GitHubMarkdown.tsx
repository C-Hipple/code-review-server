import { type ComponentProps, useState } from 'react';
import Markdown, { type Components, type ExtraProps } from 'react-markdown';
import rehypeRaw from 'rehype-raw';
import rehypeSanitize from 'rehype-sanitize';
import { proxyImageUrl, proxySrcSet } from '../github_images';

// Order matters: `rehype-raw` parses the raw HTML in the body, and
// `rehype-sanitize` then runs over the parsed result. Sanitizing has to happen
// on the tree, not on the markdown string.
const REHYPE_PLUGINS = [rehypeRaw, rehypeSanitize];

function MarkdownImage({ node: _node, src, srcSet, ...props }: ComponentProps<'img'> & ExtraProps) {
    // The original URL is what a new tab should open: a top-level navigation to
    // github.com carries the user's session, so even a private attachment
    // renders full size there.
    const original = typeof src === 'string' ? src : '';
    const proxied = original ? proxyImageUrl(original) : '';
    // Remembering the URL that failed rather than a bare flag means a body that
    // re-renders with a different image tries the proxy again.
    const [failedSrc, setFailedSrc] = useState('');
    // A bridge running without CRS_GITHUB_TOKEN, or not running at all, still
    // shouldn't lose a screenshot that GitHub serves publicly.
    const useOriginal = proxied !== original && failedSrc === proxied;

    return (
        <img
            {...props}
            alt={props.alt ?? ''}
            src={useOriginal ? original : proxied || src}
            srcSet={srcSet ? proxySrcSet(srcSet) : undefined}
            onError={() => {
                if (proxied && proxied !== original) setFailedSrc(proxied);
            }}
            loading="lazy"
            title={props.title ?? (original ? 'Click to open the full-size image' : undefined)}
            style={{
                // GitHub stamps the screenshot's natural size onto the tag,
                // routinely 1900px wide — several times the card it lands in.
                maxWidth: '100%',
                height: 'auto',
                borderRadius: '4px',
                verticalAlign: 'middle',
                cursor: original ? 'zoom-in' : 'default',
            }}
            onClick={e => {
                if (!original) return;
                // A bare click on a comment card means "reply to this thread";
                // opening the screenshot shouldn't also open the reply box.
                e.stopPropagation();
                window.open(original, '_blank', 'noopener,noreferrer');
            }}
        />
    );
}

// <picture> pairs, which GitHub emits for light/dark screenshots.
function MarkdownSource({
    node: _node,
    src,
    srcSet,
    ...props
}: ComponentProps<'source'> & ExtraProps) {
    return (
        <source
            {...props}
            src={src ? proxyImageUrl(src) : undefined}
            srcSet={srcSet ? proxySrcSet(srcSet) : undefined}
        />
    );
}

function MarkdownLink({ node: _node, ...props }: ComponentProps<'a'> & ExtraProps) {
    // Links in a comment point at GitHub, issues, or docs — none of which
    // should replace the review you are in the middle of.
    return <a {...props} target="_blank" rel="noopener noreferrer" onClick={stopPropagation} />;
}

function stopPropagation(e: { stopPropagation: () => void }) {
    e.stopPropagation();
}

const COMPONENTS: Components = {
    a: MarkdownLink,
    img: MarkdownImage,
    source: MarkdownSource,
};

/**
 * Markdown rendered the way GitHub renders it, for text written by people on
 * the PR: descriptions, review bodies and comments.
 *
 * Two things it does that plain react-markdown does not:
 *
 * 1. **Raw HTML is parsed.** GitHub's upload widget writes a pasted screenshot
 *    into the body as an `<img>` tag, and people hand-write `<details>` blocks.
 *    react-markdown drops raw HTML, so all of that used to disappear from a
 *    thread — most visibly, the screenshot someone attached to explain a bug.
 * 2. **Images load through the bridge.** See `github_images.ts`: an attachment
 *    on a private repository needs the server's token, which the browser has no
 *    way to supply.
 *
 * These bodies are written by whoever commented on the PR, so the raw HTML is
 * untrusted. `rehype-sanitize`'s default schema is GitHub's own: `img`,
 * `details`, `picture`, tables and their sizing attributes survive; `script`,
 * event handlers, inline styles and non-http(s) URLs do not.
 */
export default function GitHubMarkdown({ children }: { children: string }) {
    return (
        <Markdown rehypePlugins={REHYPE_PLUGINS} components={COMPONENTS}>
            {children}
        </Markdown>
    );
}
