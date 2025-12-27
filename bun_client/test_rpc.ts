import { spawn } from "bun";

import { resolve } from "path";
const SERVER_PATH = resolve(process.cwd(), "../codereviewserver");

const proc = spawn([SERVER_PATH, "--server"], {
    cwd: "..",
    stdin: "pipe",
    stdout: "pipe",
    stderr: "inherit",
});

const reader = proc.stdout.getReader();
const decoder = new TextDecoder();

// Send GetAllReviews
const req = JSON.stringify({
    method: "RPCHandler.GetAllReviews",
    params: [{}],
    id: 1
});
proc.stdin.write(req);

// Read loop
async function read() {
    let buffer = "";
    while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value);
        console.log("RECEIVED:", buffer);
        if (buffer.includes("}")) { // Expecting one JSON
            const obj = JSON.parse(buffer);
            console.log("Parsed:", obj);
            process.exit(0);
        }
    }
}

read();
