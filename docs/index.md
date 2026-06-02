# code-review-server

![Bun Client](img/bun-client-list.png)

![Emacs Client](img/emacs-client.png)

code-review-server is a service which runs highly configurable workflows to load code reviews which you are interested into easily managed customizable interfaces.

## Documentation Sections

- [Clients](clients.md): Information on using the bundled Web, TUI, and Emacs clients.
- [Command-Line Interface](cli.md): Query the database directly without starting the RPC server using CLI flags.
- [Configuration](configuration.md): Detailed guide on `codereviewserver.toml` configuration, including workflows and general settings.
- [Filters](filters.md): Learn how to filter PRs in your workflows with powerful query options.
- [Plugins](plugins.md): Extend the server's functionality with custom plugins or use the included AI-powered ones.
- [Reviewing Code](reviewing.md): Learn about the fast, cached, and LSP-integrated review process.
- [JSON-RPC Protocol](protocol.md): The full specification of the JSON-RPC API used by clients.
- [Building Clients](building_clients.md): Guide for developers wishing to create new clients for Code Review Server.

## Quickstart

1.  **Clone the repository**

[Repo](https://www.github.com/C-Hipple/code-review-server)

2.  **Configure environment**

    Create your config at `~/.config/codereviewserver.toml` (see [Configuration](configuration.md)).

    ```bash
    export CRS_GITHUB_TOKEN="Github Token"  # Required.
    export GEMINI_API_KEY="Gemini Token"  # Only necessary for plugin use.
    ```

    Minimal `~/.config/codereviewserver.toml`:
    ```toml
    Repos = ["owner/repo"]
    GithubUsername = "your-username"
    AutoWorktree = true

    [[Workflows]]
    WorkflowType = "SyncReviewRequestsWorkflow"
    Name = "My PRs"
    Filters = ["FilterMyPRs"]
    SectionTitle = "PRs to Review"

    [[Workflows]]
    WorkflowType = "SyncReviewRequestsWorkflow"
    Name = "PRs to Review"
    Filters = ["FilterNotDraft", "FilterMyReviewRequested"]
    SectionTitle = "PRs to Review"

    [[Plugins]]
    Name = "Summarize Diff"
    Command = "summarize_diff"
    IncludeDiff = true
    IncludeHeaders = true
    IncludeComments = true
    ```

3.  **Install Server**

    >  Alternatively, use Docker Compose to run steps 3 and 4 containerized with `docker compose up`.

    ```bash
    go install ./...
    ```

    This installs the server binary and included plugins to your `$GOPATH/bin`.

4.  **Run a Client**

    See [Clients](clients.md) for detailed instructions on running the Web or Emacs clients.

    **Web Client (Brief):**
    ```bash
    cd bun_client
    bun install && bun run build
    bun start
    ```

    **TUI Client (Brief):**
    ```bash
    cargo install --path tui_client
    crs-tui
    ```

    **Emacs Client (Brief):**
    Evaluate `client.el/crs-client.el` and run `(crs-start-server)`.
