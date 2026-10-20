import type { PluginBody } from '../types';
import { HtmlBody } from './HtmlBody';
import { Markdown } from './Markdown';

/** A plugin or AI body by its declared type; markdown unless it says `html`. */
export function OutputBody({ body, empty = 'No output.' }: { body: PluginBody; empty?: string }) {
    if (!body.body_content.trim()) return <p className="muted empty-body">{empty}</p>;
    if (body.body_type === 'html') return <HtmlBody html={body.body_content} />;
    return <Markdown>{body.body_content}</Markdown>;
}
