import ReactMarkdown, { type Components } from 'react-markdown';
import rehypeSanitize from 'rehype-sanitize';
import remarkGfm from 'remark-gfm';
import { usePanel } from './context';

const remarkPlugins = [remarkGfm];
const rehypePlugins = [rehypeSanitize];

/**
 * A markdown body (plugin or AI output). It is model or plugin output, so
 * raw HTML is dropped and what's left is sanitized; links open where the
 * panel's other GitHub links do.
 */
export function Markdown({ children }: { children: string }) {
    const { target } = usePanel();
    const components: Components = {
        a: ({ node: _node, ...props }) => <a {...props} target={target} rel="noreferrer" />,
        table: ({ node: _node, ...props }) => (
            <div className="table-scroll">
                <table {...props} />
            </div>
        ),
    };
    return (
        <div className="markdown">
            <ReactMarkdown
                remarkPlugins={remarkPlugins}
                rehypePlugins={rehypePlugins}
                components={components}
            >
                {children}
            </ReactMarkdown>
        </div>
    );
}
