# Building Clients

Code Review Server is designed to be client-agnostic. It communicates via standard input/output (stdio) using JSON-RPC 1.0.

If you want to build a new client (e.g. for VS Code, Vim, or a TUI), you should refer to the [Protocol Documentation](protocol.md).

The server binary `codereviewserver` should be spawned as a child process by your client. Your client send requests to the server's `stdin` and reads responses from `stdout`.
