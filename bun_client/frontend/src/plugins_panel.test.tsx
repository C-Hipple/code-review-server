import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import PluginsPanel from './components/review/PluginsPanel';
import type { PluginResult } from './plugin_utils';

function markdown(content: string): PluginResult {
    return {
        result: content,
        status: 'success',
        body: { body_type: 'markdown', body_content: content },
    };
}

/** A plugin whose output is `lines` lines, the first reading `first`. */
function longOutput(first: string, lines: number): PluginResult {
    return markdown(
        [first, ...Array.from({ length: lines - 1 }, (_, i) => `- finding ${i + 1}`)].join('\n')
    );
}

function renderPanel(pluginOutputs: Record<string, PluginResult>): string {
    return renderToStaticMarkup(
        <PluginsPanel
            pluginOutputs={pluginOutputs}
            executingPlugins={new Set()}
            onRefresh={() => {}}
            onExecutePlugin={() => {}}
            onClose={() => {}}
        />
    );
}

describe('PluginsPanel', () => {
    test('shows each plugin expanded while the output is short', () => {
        const html = renderPanel({
            summarize: markdown('This PR adds a **greeting helper**.'),
            security: markdown('No issues found.'),
        });

        expect(html.match(/aria-expanded="true"/g)).toHaveLength(2);
        expect(html).toContain('greeting helper');
        expect(html).toContain('No issues found.');
    });

    test('collapses every plugin when the output is over 300 lines in total', () => {
        const html = renderPanel({
            summarize: longOutput('Summary of the change', 200),
            security: longOutput('Security findings', 101),
        });

        expect(html).not.toContain('aria-expanded="true"');
        expect(html.match(/aria-expanded="false"/g)).toHaveLength(2);
        // The names stay, with how much each would show; the output doesn't.
        expect(html).toContain('summarize');
        expect(html).toContain('200 lines');
        expect(html).toContain('101 lines');
        expect(html).not.toContain('Summary of the change');
        expect(html).not.toContain('Security findings');
    });

    test('collapses a plugin with its annotations', () => {
        const html = renderPanel({
            lint: {
                ...longOutput('Lint report', 300),
                annotations: [
                    { filename: 'src/a.ts', line: 3, severity: 'error', content: 'unused var' },
                ],
            },
        });

        expect(html).toContain('301 lines');
        expect(html).not.toContain('unused var');
    });
});
