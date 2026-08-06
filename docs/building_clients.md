# Building Clients

Code Review Server is designed to be client-agnostic. It communicates via standard input/output (stdio) using JSON-RPC 1.0.

If you want to build a new client (e.g. for VS Code, Vim, or a TUI), you should refer to the [Protocol Documentation](protocol.md).

The server binary `codereviewserver` should be spawned as a child process by your client. Your client send requests to the server's `stdin` and reads responses from `stdout`.

If you are building a **web** client, [Web Client Features](web_client_features.md) inventories everything the shipped `bun_client` does — the HTTP/WebSocket bridge that fronts the stdio server, the full feature list, which protocol methods it uses (and which it doesn't), and the gotchas worth knowing before you reimplement diff parsing or comment positions.

## Offering Configuration Editing

A client can let users manage the server's config without touching TOML by hand, via `RPCHandler.GetConfig` and `RPCHandler.UpdateConfig`.

Build the pickers from the reply rather than hard-coding lists: `GetConfig` returns `workflow_types` and `filters` describing exactly what the running server supports, including which filters need an argument and which fields each workflow type uses. A client written against those registries keeps working when the server gains a new workflow type or filter.

`UpdateConfig` reports a configuration it won't accept as `okay: false` with an `errors` list rather than as an RPC error. Each entry names the workflow index and field it belongs to, so surface them next to the offending input instead of as one opaque failure. See [Protocol](protocol.md#rpchandlergetconfig) for the full shapes.
