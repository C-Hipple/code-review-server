// Framing for the Go backend's JSON-RPC responses on stdout.
//
// Go's net/rpc/jsonrpc codec writes exactly one JSON object per response,
// each terminated by a newline (encoding/json's Encoder.Encode appends one),
// and raw newlines never occur inside an encoded JSON value — they are always
// escaped as \n within strings. Splitting the stream on newlines therefore
// frames it exactly, with no need to track brace depth or string-escape state.

export class JsonRpcLineParser {
    private buffer = '';

    constructor(private onMessage: (msg: unknown) => void) {}

    push(chunk: string) {
        this.buffer += chunk;

        let newlineIdx = this.buffer.indexOf('\n');
        while (newlineIdx !== -1) {
            const line = this.buffer.substring(0, newlineIdx).trim();
            this.buffer = this.buffer.substring(newlineIdx + 1);
            if (line !== '') {
                try {
                    this.onMessage(JSON.parse(line));
                } catch (e) {
                    console.error(
                        'Failed to parse JSON-RPC response line:',
                        line.length > 200 ? `${line.slice(0, 200)}…` : line,
                        e
                    );
                }
            }
            newlineIdx = this.buffer.indexOf('\n');
        }
    }
}
