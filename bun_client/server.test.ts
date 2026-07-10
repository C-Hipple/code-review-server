import { describe, expect, test } from 'bun:test';
import { JsonRpcLineParser } from './rpc_framing';

function collect(): { messages: unknown[]; parser: JsonRpcLineParser } {
    const messages: unknown[] = [];
    const parser = new JsonRpcLineParser(msg => messages.push(msg));
    return { messages, parser };
}

describe('JsonRpcLineParser', () => {
    test('parses a single newline-terminated response', () => {
        const { messages, parser } = collect();
        parser.push('{"id":1,"result":{"okay":true},"error":null}\n');
        expect(messages).toEqual([{ id: 1, result: { okay: true }, error: null }]);
    });

    test('parses multiple responses in one chunk', () => {
        const { messages, parser } = collect();
        parser.push('{"id":1,"result":1}\n{"id":2,"result":2}\n');
        expect(messages).toEqual([
            { id: 1, result: 1 },
            { id: 2, result: 2 },
        ]);
    });

    test('reassembles a response split across chunks', () => {
        const { messages, parser } = collect();
        parser.push('{"id":1,"result":{"con');
        expect(messages).toEqual([]);
        parser.push('tent":"partial"}}');
        expect(messages).toEqual([]);
        parser.push('\n');
        expect(messages).toEqual([{ id: 1, result: { content: 'partial' } }]);
    });

    test('handles escaped quotes, braces, and newlines inside strings', () => {
        const { messages, parser } = collect();
        const diff = 'diff --git a/x b/x\\n+ say \\"hi\\" { not a frame boundary }';
        parser.push(`{"id":7,"result":{"diff":"${diff}"}}\n`);
        expect(messages).toHaveLength(1);
        const msg = messages[0] as { result: { diff: string } };
        expect(msg.result.diff).toContain('say "hi" { not a frame boundary }');
        expect(msg.result.diff).toContain('\n+');
    });

    test('skips blank lines', () => {
        const { messages, parser } = collect();
        parser.push('\n\n{"id":1,"result":null}\n\n');
        expect(messages).toEqual([{ id: 1, result: null }]);
    });

    test('recovers after an unparseable line', () => {
        const { messages, parser } = collect();
        parser.push('not json at all\n{"id":2,"result":"ok"}\n');
        expect(messages).toEqual([{ id: 2, result: 'ok' }]);
    });
});
