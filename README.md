# code-review-server

code-review-server is a service which runs highly configurable workflows to load code reviews which you are interested into easily managed customizable interfaces.

It is designed to be client-agnostic, communicating via JSON-RPC. It ships with a web client (bun/react) and an emacs client.

## Documentation

Full documentation is available in the [docs/](docs/) directory.

- [Configuration](docs/configuration.md)
- [Clients](docs/clients.md)
- [Filters](docs/filters.md)
- [Plugins](docs/plugins.md)
- [Protocol](docs/protocol.md)

## Quickstart

1.  **Clone the repository**

2.  **Configure environment**

    Create your config at `~/.config/codereviewserver.toml` (see [Configuration](docs/configuration.md)).

    ```bash
    export CRS_GITHUB_TOKEN="Github Token"  # Required.
    export GEMINI_API_KEY="Gemini Token"  # Only necessary for plugin use.
    ```

3.  **Install Server**

    ```bash
    go install ./...
    ```

    This installs the server binary and included plugins to your `$GOPATH/bin`.

4.  **Run a Client**

    See [Clients](docs/clients.md) for detailed instructions on running the Web or Emacs clients.

    **Web Client (Brief):**
    ```bash
    cd bun_client
    bun install && bun run build
    ./start-server
    ```

    **Emacs Client (Brief):**
    Evaluate `client.el` and run `(crs-start-server)`.
