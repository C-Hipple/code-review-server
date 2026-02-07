# code-review-server

code-review-server is a service which runs highly configurable workflows to load code reviews which you are interested into easily managed customizable interfaces.

## Documentation Sections

- [Clients](clients.md): Information on using the bundled Web and Emacs clients.
- [Configuration](configuration.md): Detailed guide on `codereviewserver.toml` configuration, including workflows and general settings.
- [Filters](filters.md): Learn how to filter PRs in your workflows with powerful query options.
- [Plugins](plugins.md): Extend the server's functionality with custom plugins or use the included AI-powered ones.
- [JSON-RPC Protocol](protocol.md): The full specification of the JSON-RPC API used by clients.
- [Building Clients](building_clients.md): Guide for developers wishing to create new clients for Code Review Server.

## Quickstart

1.  **Clone the repository**

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

    **Emacs Client (Brief):**
    Evaluate `client.el` and run `(crs-start-server)`.
