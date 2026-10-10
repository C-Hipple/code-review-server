# Building Clients

Code Review Server is designed to be client-agnostic. It communicates via standard input/output (stdio) using JSON-RPC 1.0.

If you want to build a new client (e.g. for VS Code, Vim, or a TUI), you should refer to the [Protocol Documentation](protocol.md).

The server binary `codereviewserver` should be spawned as a child process by your client. Your client send requests to the server's `stdin` and reads responses from `stdout`.

If you are building a **web** client, [Web Client Features](web_client_features.md) inventories everything the shipped `bun_client` does — the HTTP/WebSocket bridge that fronts the stdio server, the full feature list, which protocol methods it uses (and which it doesn't), and the gotchas worth knowing before you reimplement diff parsing or comment positions.

A **browser extension** can't spawn a process, so it has to go through a [native messaging](https://developer.chrome.com/docs/extensions/develop/concepts/native-messaging) host, which the browser launches and which spawns the server. The [Chrome extension](clients.md#chrome-extension)'s host, `chrome_extension/crs_native_host`, is a standalone Go program (standard library only) that any Chromium-based extension can reuse: register it with `install.sh --extension-id <your ID>` (the manifest it writes allows that one ID), open one port with `chrome.runtime.connectNative('com.c_hipple.crs')`, and that port is one server, stopped when the port closes. The contract on the port:

- **Requests** are the server's JSON-RPC 1.0 requests as JSON objects, `params` still a one-element array. The host forwards each to the server's stdin as one compacted line and skips anything that isn't an object.
- **Responses** come back as they are when they fit: Chrome takes at most 1 MB per message from a host, so a response over 900,000 bytes (a `GetPR` with a big diff) arrives instead as consecutive `{"crs_chunk": {"stream", "seq", "total", "data"}}` messages. Concatenate `data` over `seq` 0 to `total - 1` and parse the result as the response. One writer sends everything, so a stream's chunks are never interleaved with anything else.
- **Host events** arrive as `{"crs_host": {"event": ...}}`: `ready` (with `server_path`, `log_path` and `version`) once it has started the server, and `error` (the server can't be found or started) or `server_exited` (with its last lines of output), after which the host exits and the port closes.

Chrome starts hosts without the user's shell environment, so the host merges `~/.crs/native_host.env` into its own before starting the server; `install.sh --capture-env` writes it. The extension's [README](https://github.com/C-Hipple/code-review-server/blob/main/chrome_extension/README.md) covers the host in full: where it looks for the server, its log, and the error kinds the extension maps its failures to.

## Offering Configuration Editing

A client can let users manage the server's config without touching TOML by hand, via `RPCHandler.GetConfig` and `RPCHandler.UpdateConfig`.

Build the pickers from the reply rather than hard-coding lists: `GetConfig` returns `workflow_types`, `filters` and `ai_features` describing exactly what the running server supports, including which filters need an argument, which fields each workflow type uses, and which modes each AI feature runs in. A client written against those registries keeps working when the server gains a new workflow type, filter or AI feature. An AI feature's `legacy_key` marks one the config still switches on with a legacy root-level key; show it as on, since it is.

`UpdateConfig` reports a configuration it won't accept as `okay: false` with an `errors` list rather than as an RPC error. Each entry names the workflow index and field it belongs to — or, for a plugin or AI setting, a field like `Plugins[1].Model` — so surface them next to the offending input instead of as one opaque failure. See [Protocol](protocol.md#rpchandlergetconfig) for the full shapes.
