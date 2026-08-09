# Code Review Server Protocol

This document describes the JSON-RPC API exposed by the code review server. The server communicates over **stdio** using the JSON-RPC 1.0 protocol, making it suitable for integration with editors like Emacs.

## Transport

- **Protocol**: JSON-RPC 1.0
- **Transport**: Standard input/output (stdin/stdout)
- **Encoding**: JSON

All methods are exposed under the `RPCHandler` namespace (e.g., `RPCHandler.GetPR`).

## Lifecycle and Process Management

Since the server communicates over **stdin/stdout**, the client is responsible for managing the server's lifecycle:

1.  **Spawning**: The client should start the `codereviewserver` binary as a child process.
2.  **Environment**: Ensure `CRS_GITHUB_TOKEN` is set in the environment if required.
3.  **Communication**: The client sends JSON-RPC requests to the server's `stdin` and reads responses from its `stdout`.
4.  **Logging**: The server may write logs or errors to `stderr`. It is recommended that clients monitor `stderr` for debugging and error handling.
5.  **Termination**: The server will terminate when its `stdin` is closed or when it receives an interrupt signal (SIGINT/SIGTERM).

---

## Methods

### `RPCHandler.Hello`

A simple health check / test method.

**Arguments** (`HelloArgs`):
```json
{}
```

**Reply** (`HelloReply`):
| Field     | Type   | Description                                      |
|-----------|--------|--------------------------------------------------|
| `Count`   | int    | Number of sections in the database               |
| `Content` | string | Greeting message with cumulative count           |

---

### `RPCHandler.GetAllReviews`

Fetches all review sections from the local database, rendered as org-mode formatted text.

**Arguments** (`GetReviewsArgs`):
```json
{}
```

**Reply** (`GetReviewsReply`):
| Field     | Type         | Description                                       |
|-----------|--------------|---------------------------------------------------|
| `content` | string       | Org-mode formatted string of all review sections  |
| `items`   | []ReviewItem | Structured metadata for each PR in the list       |

#### ReviewItem Object

| Field              | Type   | Description                                                       |
|--------------------|--------|-------------------------------------------------------------------|
| `section`          | string | Title of the section the PR belongs to                            |
| `section_priority` | int    | Priority of the section (lower sorts first)                       |
| `status`           | string | Item status (`TODO`, `WAITING`, `DONE`)                           |
| `tags`             | string | Item tags (e.g. `merged`)                                         |
| `title`            | string | PR title                                                          |
| `owner`            | string | Repository owner                                                  |
| `repo`             | string | Repository name                                                   |
| `number`           | int    | Pull request number                                               |
| `author`           | string | PR author login                                                   |
| `url`              | string | GitHub HTML URL                                                   |
| `release_status`   | string | Release status from the configured release check command, if any  |
| `review_ease`      | string | LLM rating of how easy the PR is to review: `easy`, `medium`, or `hard`. Empty unless `ExperimentalLLMReviewEase` is enabled in the config and a rating has been computed |
| `created_at`       | Time   | PR creation timestamp                                             |

---

### The PR Payload

Every method that returns a pull request's full state replies with the same shared
body, described here once:

`GetPR`, `GetAdjacentPR`, `SyncPR`, `AddComment`, `EditComment`, `DeleteComment`,
`SetFeedback`, `RemovePRComments`, and `SubmitReview`.

Some of those methods add fields on top of it; those extras are listed with each
method below. The payload is always complete — a mutation like `AddComment` returns
the PR's whole refreshed state, not just the part that changed, so clients can apply
the reply wholesale instead of patching local state.

| Field               | Type           | Description                                                                 |
|---------------------|----------------|-----------------------------------------------------------------------------|
| `okay`              | bool           | `true` if the request succeeded                                             |
| `content`           | string         | Formatted PR response (diff, comments, metadata)                            |
| `metadata`          | PRMetadata     | Structured PR metadata                                                      |
| `diff`              | string         | Raw diff content                                                            |
| `comments`          | []CommentJSON  | Structured PR active comments                                               |
| `outdated_comments` | []CommentJSON  | Structured PR outdated comments                                             |
| `reviews`           | []ReviewJSON   | Submitted reviews                                                           |
| `commits`           | []CommitJSON   | Commits on the PR                                                           |
| `feedback`          | string         | The locally saved review feedback draft for this PR (see `SetFeedback`); empty when none has been saved |
| `annotations`       | []PRAnnotation | Diff annotations aggregated from every plugin that has already executed successfully for this PR (see [Plugins](plugins.md#plugin-response-contract)) |

Slice fields are normalized before sending: a client always sees `[]`, never `null`.

#### PRAnnotation Object

| Field      | Type   | Description                                              |
|------------|--------|----------------------------------------------------------|
| `plugin`   | string | Name of the plugin that produced the annotation          |
| `filename` | string | Path of the file within the repo                         |
| `line`     | int    | 1-based line number the annotation applies to            |
| `severity` | string | Free-form severity, e.g. `info`, `warning`, `error`      |
| `content`  | string | The annotation text                                      |

#### PRMetadata Object

| Field                  | Type     | Description                                               |
|------------------------|----------|-----------------------------------------------------------|
| `number`               | int      | Pull request number                                       |
| `title`                | string   | PR title                                                  |
| `author`               | string   | PR author login                                           |
| `base_ref`             | string   | Base branch name                                          |
| `head_ref`             | string   | Head branch name                                          |
| `state`                | string   | PR state (open, closed, etc.)                             |
| `milestone`            | string   | Milestone title                                           |
| `labels`               | []string | List of label names                                       |
| `assignees`            | []string | List of assignee logins                                   |
| `reviewers`            | []string | List of requested individual reviewers                    |
| `requested_teams`      | []string | List of requested team reviewers                          |
| `approved_by`          | []string | Logins of users who approved                              |
| `changes_requested_by` | []string | Logins of users who requested changes                      |
| `commented_by`         | []string | Logins of users who commented                             |
| `draft`                | bool     | Whether the PR is a draft                                 |
| `ci_status`            | string   | Summary of CI status                                      |
| `ci_failures`          | []string | List of failed CI check names and messages                |
| `body`                 | string   | PR description body                                       |
| `url`                  | string   | GitHub HTML URL                                           |
| `repo_path`            | string   | Absolute path to the local clone of the repository, when one exists under the configured `RepoLocation`. Empty when the repo isn't checked out locally. Distinct from `worktree_path`: this is the repository itself, not the PR's worktree. Clients use it to gate features that need local source, such as language-server lookups or a file browser |
| `worktree_path`        | string   | Absolute path to the local git worktree (if managed by server) |
| `release_status`       | string   | Release status from the configured release check command, if any |
| `review_ease`          | string   | LLM rating of how easy the PR is to review: `easy`, `medium`, or `hard`. Empty unless `ExperimentalLLMReviewEase` is enabled in the config and a rating has been computed |
| `changed_files`        | int      | Number of files changed by the PR                         |
| `additions`            | int      | Lines added by the PR                                     |
| `deletions`            | int      | Lines removed by the PR                                   |

#### Using the Worktree

When `worktree_path` is provided, you can use it to quickly switch to the source code for that PR:

- **Shell**: `cd $(codereviewserver get-path --owner octocat --repo hello --number 42)` or simply `cd <worktree_path>`
- **Git Management**: The server manages these using `git worktree`. You can see all active worktrees with `git worktree list` inside the main repository.

#### Rendered Comment Format

Comments are rendered inline within the diff or at the file headers. They use a boxed format with special headers to indicate their type:

- **Regular Review Comment**: Indicates a comment on a specific line in the current version of the code.
  ```
  ┌─ REVIEW COMMENT ─────────────────
  ```
- **Outdated Review Comment**: Indicates a comment that was made on a previous version of the code that no longer matches the current head or position.
  ```
  ┌─ REVIEW COMMENT [OUTDATED] ──────
  ```
- **File Comment**: Indicates a comment made on the file as a whole, rather than a specific line.
  ```
  ┌─ FILE COMMENT ───────────────────
  ```

Each comment block includes the file path, timestamp, author(s), and comment ID, followed by the conversation thread.

#### Comment Object (`CommentJSON`)

| Field         | Type   | Description                                                                 |
|---------------|--------|-----------------------------------------------------------------------------|
| `id`          | string | Comment ID. For a local (pending) comment this is the local ID accepted by `EditComment` / `DeleteComment` |
| `author`      | string | GitHub login of the author, or `local` for a pending comment not yet submitted |
| `body`        | string | Comment body text                                                           |
| `path`        | string | File the comment is attached to                                             |
| `position`    | string | Position within the diff (not a file line number). `0` means the comment is on the file as a whole |
| `in_reply_to` | int64  | ID of the comment this one replies to; `0` for a thread root                 |
| `created_at`  | Time   | Creation timestamp                                                          |
| `outdated`    | bool   | Whether the comment refers to a version of the code the current diff no longer matches |
| `diff_hunk`   | string | The diff context the comment was made against — the only way to show an outdated comment in place |
| `review_id`   | int64  | The review this comment was submitted with; `0` for a standalone issue comment or a local (pending) comment |
| `html_url`    | string | Link to the comment on GitHub; empty for local comments |
| `thread_id`   | string | GraphQL node ID of the review thread this comment belongs to (see [Review thread resolution](#review-thread-resolution)) |
| `resolved`    | bool   | Whether the thread has been marked resolved on GitHub |
| `resolved_by` | string | GitHub login of whoever resolved the thread; empty when unresolved |

##### Review thread resolution

`thread_id`, `resolved`, and `resolved_by` come from GitHub's GraphQL
`reviewThreads` connection, which the server fetches on the client's behalf —
**clients never call GitHub directly**. The REST comment endpoints do not report
resolution at all, so this is the only source for it.

Two consequences for clients:

- **Resolution is a property of the thread, not the comment.** Every comment in
  a resolved thread carries `resolved: true`, including replies. Reading it off
  the thread root is enough.
- **`resolved: false` means "not resolved *or* not known".** If the GraphQL
  fetch is skipped or fails, the server logs it, leaves all three fields zeroed,
  and still returns the PR. Render the absence as "no information", not as an
  explicit "unresolved" — the web client does this by only ever showing a
  *Resolved* badge and never an *Unresolved* one.

`outdated` and `resolved` are **independent, and frequently both true**: a
conversation gets resolved, then later pushes rewrite the lines it pointed at.
A client that presents them as one status, or that sums the two counts, will
double-count those threads. When the thread data is available, `outdated`
reflects GitHub's own judgement for the whole thread rather than a single
comment's position mapping, and it also decides whether a comment lands in
`comments` or in `outdated_comments`.

#### Review Object

Represents a submitted review (e.g. APPROVED, CHANGES_REQUESTED).

| Field          | Type      | Description                                      |
|----------------|-----------|--------------------------------------------------|
| `id`           | int64     | Review ID                                        |
| `user`         | string    | GitHub login of the reviewer                     |
| `body`         | string    | Main body text of the review                     |
| `state`        | string    | Review state (APPROVED, CHANGES_REQUESTED, etc.) |
| `submitted_at` | Time      | Timestamp when the review was submitted          |
| `html_url`     | string    | Link to the review on GitHub                     |

#### Commit Object (`CommitJSON`)

| Field     | Type   | Description                        |
|-----------|--------|------------------------------------|
| `sha`     | string | Commit SHA                         |
| `message` | string | Commit message                     |
| `author`  | string | Commit author                      |
| `date`    | string | Commit date                        |
| `url`     | string | Link to the commit on GitHub       |

---

### `RPCHandler.GetPR`

Fetches a pull request from GitHub and returns it as rendered content (including diff, comments, conversations). Cached data is used where available; see `SyncPR` for a forced refresh.

This is a read-only query as far as the caller is concerned: it never blocks on plugin execution or LLM analysis. Those are dispatched in the background, and their results arrive through `GetPluginOutput` (or on a later `GetPR`'s `annotations`).

**Arguments** (`GetPRstructArgs`):
| Field       | Type   | Required | Description                          |
|-------------|--------|----------|--------------------------------------|
| `Owner`     | string | Yes      | Repository owner (e.g., `"octocat"`) |
| `Repo`      | string | Yes      | Repository name (e.g., `"hello"`)    |
| `Number`    | int    | Yes      | Pull request number                  |
| `SkipCache` | bool   | No       | If `true`, bypass the DB caches and fetch from GitHub |

**Reply** (`GetPRReply`): [the PR payload](#the-pr-payload), with no extra fields.

---

### `RPCHandler.GetAdjacentPR`

Fetches the next or previous pull request relative to the given PR in the sorted review list (same ordering as `GetAllReviews`: by status, then repo, then number). Navigation **wraps around** — calling with `Previous: false` on the last PR returns the first, and calling with `Previous: true` on the first PR returns the last.

**Arguments** (`GetAdjacentPRArgs`):
| Field      | Type   | Required | Description                                              |
|------------|--------|----------|----------------------------------------------------------|
| `Owner`    | string | Yes      | Repository owner of the **current** PR                   |
| `Repo`     | string | Yes      | Repository name of the **current** PR                    |
| `Number`   | int    | Yes      | Pull request number of the **current** PR                |
| `SkipCache`| bool   | No       | If `true`, bypass cached data for the adjacent PR        |
| `Previous` | bool   | No       | If `true`, return the previous PR; if `false` (default), return the next PR |

**Reply** (`GetAdjacentPRReply`): [the PR payload](#the-pr-payload), plus:

| Field              | Type   | Description                                           |
|--------------------|--------|-------------------------------------------------------|
| `adjacent_owner`   | string | Owner of the adjacent PR                              |
| `adjacent_repo`    | string | Repo name of the adjacent PR                          |
| `adjacent_number`  | int    | PR number of the adjacent PR                          |

> **Note**: The `adjacent_owner`, `adjacent_repo`, and `adjacent_number` fields identify the PR whose data is returned. Clients should use these to update their navigation state rather than parsing `metadata.url`.

**Example** — advance to the next PR from PR #42:
```json
{
  "method": "RPCHandler.GetAdjacentPR",
  "params": [{"Owner": "octocat", "Repo": "Hello-World", "Number": 42, "Previous": false}],
  "id": 2
}
```

**Response**:
```json
{
  "result": {
    "okay": true,
    "adjacent_owner": "octocat",
    "adjacent_repo": "Hello-World",
    "adjacent_number": 43,
    "content": "... formatted PR content ...",
    "metadata": { "number": 43, "title": "Next PR", ... },
    "diff": "...",
    "comments": [],
    "outdated_comments": [],
    "reviews": [],
    "commits": [],
    "feedback": "",
    "annotations": []
  },
  "error": null,
  "id": 2
}
```

---

### `RPCHandler.SyncPR`

Forces a fresh fetch of the pull request from GitHub, bypassing any cache.

**Arguments** (`SyncPRArgs`):
| Field    | Type   | Required | Description                          |
|----------|--------|----------|--------------------------------------|
| `Owner`  | string | Yes      | Repository owner                     |
| `Repo`   | string | Yes      | Repository name                      |
| `Number` | int    | Yes      | Pull request number                  |

**Reply** (`SyncPRReply`): [the PR payload](#the-pr-payload), freshly fetched, plus:

| Field     | Type | Description                                                                                         |
|-----------|------|-----------------------------------------------------------------------------------------------------|
| `updated` | bool | `true` if the sync pulled in a new head SHA or new comments compared to the previously cached state |

---

### `RPCHandler.GetHunkContext`

Returns extra context lines immediately before or after a hunk boundary, so a client can expand the visible context around a diff without fetching the whole file.

The lines come back as plain text, without the leading space that marks a context line in a unified diff — a client splicing them into a diff adds that itself. They are **not** part of the PR's canonical diff, so they must not consume a comment `position`: counting them would shift the position of every line below the expansion, and comments created while expanded would be stored against a position that no longer exists once the canonical diff is restored.

**Arguments** (`GetHunkContextArgs`):
| Field        | Type   | Required | Description                                                                 |
|--------------|--------|----------|-----------------------------------------------------------------------------|
| `Owner`      | string | Yes      | Repository owner                                                            |
| `Repo`       | string | Yes      | Repository name                                                             |
| `Number`     | int    | Yes      | Pull request number                                                         |
| `Filename`   | string | Yes      | Path of the file within the repo                                            |
| `Side`       | string | Yes      | Which version of the file to read from: `"old"` or `"new"`                   |
| `AnchorLine` | int    | Yes      | 1-based line number **in the file** (not a diff position) to expand from. With `Direction: "before"`, lines above it are returned; with `"after"`, lines below it |
| `Direction`  | string | Yes      | `"before"` or `"after"`                                                     |
| `Count`      | int    | No       | Number of extra lines to fetch. Defaults to 20, capped at 100                |
| `OrigStart`  | int    | Yes      | Current hunk's `@@ -OrigStart,OrigLength`                                    |
| `OrigLength` | int    | Yes      | Current hunk's old-side length                                              |
| `NewStart`   | int    | Yes      | Current hunk's `@@ +NewStart,NewLength`                                      |
| `NewLength`  | int    | Yes      | Current hunk's new-side length                                              |
| `HunkHeader` | string | No       | Text after the `@@` in the current hunk header (e.g. the enclosing function), preserved in the rewritten header |

The four range fields and `HunkHeader` describe the hunk as it currently stands; the server uses them to compute the header the hunk should carry once expanded.

**Reply** (`GetHunkContextReply`):
| Field          | Type     | Description                                                        |
|----------------|----------|--------------------------------------------------------------------|
| `lines`        | []string | The extra context lines                                            |
| `start_line`   | int      | 1-based line number of the first returned line                     |
| `end_line`     | int      | 1-based line number of the last returned line                      |
| `range_header` | string   | Updated `@@ -a,b +c,d @@` header for the expanded hunk; a client replaces the existing hunk header with this |

`Direction` and `Side` are validated, and `AnchorLine` must be at least 1 — anything else is returned as an RPC error.

**Example Request** (20 lines above a hunk starting at line 42 of the head file):
```json
{
  "method": "RPCHandler.GetHunkContext",
  "params": [{
    "Owner": "octocat", "Repo": "Hello-World", "Number": 42,
    "Filename": "server/server.go",
    "Side": "new", "AnchorLine": 42, "Direction": "before", "Count": 20,
    "OrigStart": 40, "OrigLength": 6, "NewStart": 42, "NewLength": 8,
    "HunkHeader": "func (h *RPCHandler) GetPR("
  }],
  "id": 9
}
```

---

### `RPCHandler.AddComment`

Adds a new local (pending) comment to a pull request. The comment is stored locally until the review is submitted.

**Arguments** (`AddCommentArgs`):
| Field       | Type    | Required | Description                                              |
|-------------|---------|----------|----------------------------------------------------------|
| `Owner`     | string  | Yes      | Repository owner                                         |
| `Repo`      | string  | Yes      | Repository name                                          |
| `Number`    | int     | Yes      | Pull request number                                      |
| `Filename`  | string  | Yes      | Path to the file being commented on                      |
| `Position`  | int64   | Yes      | Line position in the diff                                |
| `Body`      | string  | Yes      | Comment body text                                        |
| `ReplyToID` | *int64  | No       | If replying to an existing comment, the comment ID       |

**Reply** (`AddCommentReply`): [the PR payload](#the-pr-payload), plus:

| Field | Type  | Description                           |
|-------|-------|---------------------------------------|
| `id`  | int64 | Local ID of the newly created comment |

---

### `RPCHandler.EditComment`

Edits an existing local (pending) comment.

**Arguments** (`EditCommentArgs`):
| Field    | Type   | Required | Description                          |
|----------|--------|----------|--------------------------------------|
| `Owner`  | string | Yes      | Repository owner                     |
| `Repo`   | string | Yes      | Repository name                      |
| `Number` | int    | Yes      | Pull request number                  |
| `ID`     | int64  | Yes      | Local comment ID to edit             |
| `Body`   | string | Yes      | New body text for the comment        |

**Reply** (`EditCommentReply`): [the PR payload](#the-pr-payload), with no extra fields.

---

### `RPCHandler.DeleteComment`

Deletes a local (pending) comment.

**Arguments** (`DeleteCommentArgs`):
| Field    | Type   | Required | Description                          |
|----------|--------|----------|--------------------------------------|
| `Owner`  | string | Yes      | Repository owner                     |
| `Repo`   | string | Yes      | Repository name                      |
| `Number` | int    | Yes      | Pull request number                  |
| `ID`     | int64  | Yes      | Local comment ID to delete           |

**Reply** (`DeleteCommentReply`): [the PR payload](#the-pr-payload), with no extra fields.

---

### `RPCHandler.SetFeedback`

Sets the top-level feedback/review body for a pull request.

**Arguments** (`SetFeedbackArgs`):
| Field    | Type   | Required | Description                          |
|----------|--------|----------|--------------------------------------|
| `Owner`  | string | Yes      | Repository owner                     |
| `Repo`   | string | Yes      | Repository name                      |
| `Number` | int    | Yes      | Pull request number                  |
| `Body`   | string | Yes      | Feedback/review body text            |

**Reply** (`SetFeedbackReply`): [the PR payload](#the-pr-payload) — whose `feedback` field carries the body just saved — plus:

| Field | Type  | Description              |
|-------|-------|--------------------------|
| `id`  | int64 | ID of the feedback entry |

---

### `RPCHandler.RemovePRComments`

Removes all local (pending) comments for a specific pull request.

**Arguments** (`RemovePRCommentsArgs`):
| Field    | Type   | Required | Description                          |
|----------|--------|----------|--------------------------------------|
| `Owner`  | string | Yes      | Repository owner                     |
| `Repo`   | string | Yes      | Repository name                      |
| `Number` | int    | Yes      | Pull request number                  |

**Reply** (`RemovePRCommentsReply`): [the PR payload](#the-pr-payload), with no extra fields.

---

### `RPCHandler.SubmitReview`

Submits a review to GitHub. This will:
1. Fetch all local pending comments for the PR
2. Submit reply comments individually to maintain threading
3. Submit top-level comments as part of a GitHub review
4. Delete all local comments after successful submission
5. Re-evaluate the PR against every workflow that targets its repo, updating the
   PR's membership in those workflows' sections immediately rather than at the
   next workflow cycle. A PR that no longer matches a section's filters (e.g. a
   "needs review" section, now that the review request is gone) drops out of it;
   one that now matches a section it wasn't in is added. Reprocessing failures
   are logged, not returned — the review itself has already been submitted.

**Arguments** (`SubmitReviewArgs`):
| Field    | Type   | Required | Description                                              |
|----------|--------|----------|----------------------------------------------------------|
| `Owner`  | string | Yes      | Repository owner                                         |
| `Repo`   | string | Yes      | Repository name                                          |
| `Number` | int    | Yes      | Pull request number                                      |
| `Event`  | string | Yes      | Review event type: `APPROVE`, `REQUEST_CHANGES`, or `COMMENT` |
| `Body`   | string | No       | Top-level review body (optional)                         |

**Reply** (`SubmitReviewReply`): [the PR payload](#the-pr-payload), with no extra fields.

---

### `RPCHandler.MergePR`

Asks GitHub to merge the given pull request. The merge method defaults to **squash** when `MergeMethod` is omitted or empty; callers may override it with any value GitHub accepts (`"merge"`, `"squash"`, or `"rebase"`).

The server passes through GitHub's response verbatim — clients should rely on the `merged` and `message` fields to determine whether the merge succeeded rather than inferring success from the absence of an error. A non-error response with `merged: false` (e.g. due to a protected branch, failing checks, or a dirty mergeable state) is still a meaningful answer from GitHub.

**Arguments** (`MergePRArgs`):
| Field         | Type   | Required | Description                                                                 |
|---------------|--------|----------|-----------------------------------------------------------------------------|
| `Owner`       | string | Yes      | Repository owner                                                            |
| `Repo`        | string | Yes      | Repository name                                                             |
| `Number`      | int    | Yes      | Pull request number                                                         |
| `MergeMethod` | string | No       | One of `"merge"`, `"squash"`, or `"rebase"`. Defaults to `"squash"` if empty |

**Reply** (`MergePRReply`):
| Field     | Type   | Description                                                        |
|-----------|--------|--------------------------------------------------------------------|
| `merged`  | bool   | `true` if GitHub reports the PR was successfully merged            |
| `sha`     | string | SHA of the merge commit produced by GitHub (empty if not merged)   |
| `message` | string | Human-readable message returned by GitHub describing the outcome   |

**Example Request** (squash merge — default):
```json
{
  "method": "RPCHandler.MergePR",
  "params": [{"Owner": "octocat", "Repo": "Hello-World", "Number": 42}],
  "id": 7
}
```

**Example Request** (rebase merge):
```json
{
  "method": "RPCHandler.MergePR",
  "params": [{"Owner": "octocat", "Repo": "Hello-World", "Number": 42, "MergeMethod": "rebase"}],
  "id": 7
}
```

**Example Response**:
```json
{
  "result": {
    "merged": true,
    "sha": "6dcb09b5b57875f334f61aebed695e2e4193db5e",
    "message": "Pull Request successfully merged"
  },
  "error": null,
  "id": 7
}
```

---

### `RPCHandler.ListPlugins`

Lists all installed and configured plugins.

**Arguments** (`ListPluginsArgs`):
```json
{}
```

**Reply** (`ListPluginsReply`):
| Field     | Type       | Description                        |
|-----------|------------|------------------------------------|
| `plugins` | []Plugin   | List of configured plugin objects  |

#### `Plugin` Object
| Field             | Type   | Description                                           |
|-------------------|--------|-------------------------------------------------------|
| `Name`            | string | Human-readable name of the plugin                     |
| `Command`         | string | Command or path to the plugin binary                  |
| `IncludeDiff`     | bool   | Whether the plugin receives the PR diff               |
| `IncludeHeaders`  | bool   | Whether the plugin receives the PR metadata (headers) |
| `IncludeComments` | bool   | Whether the plugin receives the PR comments           |

---

### `RPCHandler.GetPluginOutput`

Retrieves the output and status of all plugins for a specific pull request.

**Arguments** (`GetPluginOutputArgs`):
| Field    | Type   | Required | Description                          |
|----------|--------|----------|--------------------------------------|
| `Owner`  | string | Yes      | Repository owner                     |
| `Repo`   | string | Yes      | Repository name                      |
| `Number` | int    | Yes      | Pull request number                  |

**Reply** (`GetPluginOutputReply`):
| Field    | Type                        | Description                                            |
|----------|-----------------------------|--------------------------------------------------------|
| `output` | map[string]PluginOutput     | Map of plugin names to their respective results/status |

#### `PluginOutput` Object
| Field         | Type         | Description                                                                       |
|---------------|--------------|-----------------------------------------------------------------------------------|
| `result`      | string       | The raw captured stdout of the plugin, unchanged (kept for older clients)          |
| `status`      | string       | Execution status: `pending`, `success`, `error`, or `deferred`                     |
| `body`        | PluginBody   | The plugin's output parsed against the [response contract](plugins.md#plugin-response-contract); non-conforming output is wrapped as a `markdown` body holding the raw output |
| `annotations` | []Annotation | Line-level diff annotations declared by the plugin (empty for legacy output)       |

#### `PluginBody` Object
| Field          | Type   | Description                            |
|----------------|--------|----------------------------------------|
| `body_type`    | string | Either `markdown` or `html`            |
| `body_content` | string | The renderable output of the plugin    |

#### `Annotation` Object
| Field      | Type   | Description                                          |
|------------|--------|------------------------------------------------------|
| `filename` | string | Path of the file within the repo                     |
| `line`     | int    | 1-based line number the annotation applies to        |
| `severity` | string | Free-form severity, e.g. `info`, `warning`, `error`  |
| `content`  | string | The annotation text                                  |

---

### `RPCHandler.RerunPlugins`

Clears cached plugin results for a pull request and runs the plugins again, bypassing the head-SHA cache check that normally makes a plugin skip a PR it has already seen.

Execution happens in the background: the reply means the rerun was **accepted**, not that it finished. Poll `GetPluginOutput` for results — a plugin that is still running reports `pending`.

**Arguments** (`RerunPluginsArgs`):
| Field     | Type     | Required | Description                                                           |
|-----------|----------|----------|------------------------------------------------------------------------|
| `Owner`   | string   | Yes      | Repository owner                                                      |
| `Repo`    | string   | Yes      | Repository name                                                       |
| `Number`  | int      | Yes      | Pull request number                                                   |
| `Plugins` | []string | No       | Names of specific plugins to rerun. Omitted or empty reruns all of them |

**Reply** (`RerunPluginsReply`):
| Field     | Type                    | Description                                              |
|-----------|-------------------------|-----------------------------------------------------------|
| `okay`    | bool                    | `true` if the rerun was accepted                          |
| `message` | string                  | Human-readable status                                     |
| `output`  | map[string]PluginOutput | Plugin results as they stood when the rerun was dispatched; see `GetPluginOutput` |

Because dispatch is asynchronous, `output` will generally still show the cleared or previous state. Treat `GetPluginOutput` as the source of truth once the rerun has been accepted.

**Example Request** (rerun a single plugin):
```json
{
  "method": "RPCHandler.RerunPlugins",
  "params": [{"Owner": "octocat", "Repo": "Hello-World", "Number": 42, "Plugins": ["security_check"]}],
  "id": 10
}
```

---

### `RPCHandler.GetConfig`

Returns the server's configuration file, re-read from disk so the client sees edits made outside the server. The reply also carries the workflow type and filter registries, so a client can build its pickers from what this server actually supports instead of hard-coding the lists.

**Arguments** (`GetConfigArgs`):
```json
{}
```

**Reply** (`GetConfigReply`):
| Field            | Type               | Description                                                        |
|------------------|--------------------|--------------------------------------------------------------------|
| `okay`           | bool               | `true` if the configuration was read successfully                  |
| `message`        | string             | Empty on success; explains the problem when the file can't be read |
| `path`           | string             | Absolute path of the TOML file being read and written              |
| `config`         | Config             | The current configuration (see below)                              |
| `using_defaults` | bool               | `true` when no file exists at `path` and the built-in defaults are running |
| `workflow_types` | []WorkflowTypeInfo | Workflow types this server can run                                 |
| `filters`        | []FilterInfo       | Filters a workflow may use                                         |

If the file on disk no longer parses, `okay` is `false` and `config` describes the configuration still running in memory.

When `using_defaults` is `true` there is no file yet; `config` describes the built-in defaults (see [Configuration](configuration.md#running-without-a-config-file)). Any `UpdateConfig` call writes them out along with the change, so a client can present them as an ordinary editable configuration.

#### `Config` Object

Field names match the TOML keys. See [Configuration](configuration.md) for what each one does.

| Field                         | Type              | Description                                                     |
|-------------------------------|-------------------|------------------------------------------------------------------|
| `Repos`                       | []string          | Repositories in `owner/repo` form                               |
| `SleepDuration`               | int               | Minutes between workflow syncs                                   |
| `JiraDomain`                  | string            | Jira domain used by `ProjectListWorkflow`                        |
| `GithubUsername`              | string            | Login used by the "me" filters                                   |
| `RepoLocation`                | string            | Directory holding local clones                                   |
| `AutoWorktree`                | bool              | Whether the server manages git worktrees                         |
| `DesktopNotifications`        | bool              | Global desktop notification setting                              |
| `SectionPriority`             | map[string]int    | Section title → priority (lower sorts first)                     |
| `SectionSorting`              | map[string]string | Section title → `newest_first` / `oldest_first`                  |
| `Workflows`                   | []Workflow        | Configured workflows                                             |
| `Plugins`                     | []Plugin          | Configured plugins (read-only; `UpdateConfig` does not set them) |
| `ExperimentalLLMFileOrdering` | bool              | LLM diff file ordering                                           |
| `ExperimentalLLMReviewEase`   | bool              | LLM review-ease rating                                           |

#### `Workflow` Object

| Field                  | Type      | Description                                                              |
|------------------------|-----------|---------------------------------------------------------------------------|
| `WorkflowType`         | string    | One of the names in `workflow_types`                                      |
| `Name`                 | string    | Unique workflow name; identifies which workflow owns an item              |
| `SectionTitle`         | string    | Section the workflow's PRs land in                                        |
| `Repos`                | []string  | Repositories for this workflow; empty inherits the root-level `Repos`      |
| `Repo`                 | string    | Single repository (`SingleRepoSyncReviewRequestsWorkflow`)                |
| `Owner`                | string    | Repository owner, for configs using the singular `Owner`/`Repo` form      |
| `Filters`              | []string  | Filter names, argument-taking ones written as `FilterByLabel:bug`         |
| `Teams`                | []string  | Team slugs whose review requests should match                             |
| `JiraEpic`             | string    | Epic key (`ProjectListWorkflow`)                                          |
| `PRState`              | string    | `open`, `closed`, or `all`; empty means `open`                            |
| `IncludeDiff`          | bool      | Include the full diff in the section body                                 |
| `GithubUsername`       | string    | Per-workflow username; inherits the root-level one when empty             |
| `DesktopNotifications` | bool/null | Per-workflow override; `null` inherits the global setting                 |

#### `WorkflowTypeInfo` Object

| Field             | Type     | Description                                                       |
|-------------------|----------|--------------------------------------------------------------------|
| `name`            | string   | Value to put in a workflow's `WorkflowType`                        |
| `description`     | string   | What the workflow type does                                        |
| `deprecated`      | bool     | Whether the type still works but shouldn't be used for new configs |
| `deprecated_by`   | string   | What to use instead (deprecated types only)                        |
| `required_fields` | []string | Type-specific fields the workflow must set                         |
| `optional_fields` | []string | Type-specific fields the workflow may set                          |

`Name`, `WorkflowType`, and `SectionTitle` are required by every type and are not repeated in `required_fields`. Clients can use these two lists to decide which fields to show for the selected type.

#### `FilterInfo` Object

| Field          | Type   | Description                                                   |
|----------------|--------|----------------------------------------------------------------|
| `name`         | string | Filter name as written in `Filters`                            |
| `description`  | string | What the filter matches                                        |
| `requires_arg` | bool   | Whether the filter must be written as `Name:argument`          |
| `arg_label`    | string | What the argument means (e.g. `label`, `username`)             |

---

### `RPCHandler.UpdateConfig`

Writes a **partial** change to the configuration file and reloads the running config. Every argument is optional: a field that is omitted (or `null`) keeps whatever is on disk, as do settings the server doesn't model. Sending `Workflows` replaces the entire list, which is how a client adds, removes, or reorders entries.

The new configuration is validated **before** anything is written. If it fails, the file is untouched and the reply comes back with `okay: false` and a populated `errors` list — a rejected configuration is not an RPC error, so clients can attach each message to the field that caused it.

On success the file is replaced atomically and the previous contents are kept alongside it as `<path>.bak`. Comments and the original key ordering in the file are **not** preserved.

The background workflow manager re-derives its workflows from the config at the start of every sync cycle, so a saved change takes effect on the next sync rather than immediately.

**Arguments** (`UpdateConfigArgs`): any subset of the `Config` fields listed under `GetConfig`, except `Plugins` (which the server never rewrites).

| Field                         | Type              | Required | Description                                      |
|-------------------------------|-------------------|----------|--------------------------------------------------|
| `Repos`                       | []string          | No       | Replaces the root-level repository list          |
| `SleepDuration`               | int               | No       | Minutes between syncs                            |
| `JiraDomain`                  | string            | No       | Jira domain                                      |
| `GithubUsername`              | string            | No       | GitHub login                                     |
| `RepoLocation`                | string            | No       | Local clone directory                            |
| `AutoWorktree`                | bool              | No       | Worktree management                              |
| `DesktopNotifications`        | bool              | No       | Global notification setting                      |
| `SectionPriority`             | map[string]int    | No       | Replaces the section priority map                |
| `SectionSorting`              | map[string]string | No       | Replaces the section sorting map                 |
| `Workflows`                   | []Workflow        | No       | Replaces the whole workflow list                 |
| `ExperimentalLLMFileOrdering` | bool              | No       | LLM diff file ordering                           |
| `ExperimentalLLMReviewEase`   | bool              | No       | LLM review-ease rating                           |

**Reply** (`UpdateConfigReply`):

All fields from `GetConfigReply`, plus:

| Field    | Type               | Description                                              |
|----------|--------------------|-----------------------------------------------------------|
| `errors` | []ValidationError  | Problems that caused the update to be rejected (empty on success) |

`config` always describes the configuration that is actually in effect: the newly saved one after a successful update, or the unchanged one after a rejection.

#### `ValidationError` Object

| Field      | Type   | Description                                                              |
|------------|--------|---------------------------------------------------------------------------|
| `workflow` | int    | Index into `Workflows` the problem belongs to, or `-1` for a root setting |
| `field`    | string | Field name the problem is about (e.g. `SectionTitle`, `Filters`)          |
| `message`  | string | Human-readable description of the problem                                 |

**Example Request** (replace the workflow list):
```json
{
  "method": "RPCHandler.UpdateConfig",
  "params": [{
    "Workflows": [
      {
        "WorkflowType": "SyncReviewRequestsWorkflow",
        "Name": "Team Reviews",
        "SectionTitle": "Needs My Team's Review",
        "Filters": ["FilterNotDraft", "FilterByLabel:bug"],
        "Teams": ["my-team"]
      }
    ]
  }],
  "id": 8
}
```

**Example Response** (rejected):
```json
{
  "result": {
    "okay": false,
    "message": "Configuration not saved: found 2 problems",
    "path": "/home/user/.config/codereviewserver.toml",
    "errors": [
      {"workflow": 0, "field": "SectionTitle", "message": "is required"},
      {"workflow": 0, "field": "Filters", "message": "FilterByLabel requires an argument (e.g. FilterByLabel:<label>)"}
    ],
    "config": { "...": "the unchanged configuration" },
    "workflow_types": [{ "...": "as in GetConfig" }],
    "filters": [{ "...": "as in GetConfig" }]
  },
  "error": null,
  "id": 8
}
```

---

### `RPCHandler.GetRateLimitStatus`

Returns the current GitHub API rate limit status, including remaining quota, reset time, and usage metrics.

**Arguments** (`GetRateLimitStatusArgs`):
```json
{}
```

**Reply** (`GetRateLimitStatusReply`):
| Field                | Type   | Description                                                   |
|----------------------|--------|---------------------------------------------------------------|
| `remaining`          | int    | Number of API requests remaining in the current window        |
| `limit`              | int    | Total API request limit (typically 5000 for authenticated)    |
| `reset_at`           | string | Timestamp when the rate limit resets (formatted string)       |
| `total_requests`     | int64  | Total number of API requests made since server start          |
| `throttled_count`    | int64  | Number of requests that were throttled due to low quota       |
| `rate_limited_count` | int64  | Number of times the server hit a 429/403 rate limit response |

**Example Response**:
```json
{
  "remaining": 4850,
  "limit": 5000,
  "reset_at": "2026-02-05 20:15:32 EST",
  "total_requests": 150,
  "throttled_count": 0,
  "rate_limited_count": 0
}
```

This endpoint is useful for monitoring GitHub API usage and determining if the server is approaching rate limits. The server automatically:
- Throttles requests when `remaining < 100`
- Blocks requests when `remaining <= 10` (emergency reserve)
- Retries on 429/403 errors with exponential backoff

---

### `RPCHandler.CheckRepoExists`

Checks if a repository is stored locally in the user's home directory (`~/RepoName`). This is useful for determining if features like LSP (which often require local source code) should be enabled.

**Arguments** (`CheckRepoExistsArgs`):
| Field  | Type   | Required | Description                       |
|--------|--------|----------|-----------------------------------|
| `Repo` | string | Yes      | Repository name (e.g., `"hello"`) |

**Reply** (`CheckRepoExistsReply`):
| Field    | Type   | Description                                      |
|----------|--------|--------------------------------------------------|
| `Exists` | bool   | `true` if the directory exists and is a directory|
| `Path`   | string | The full absolute path to the repository         |

---

## Workflow

A typical code review workflow using this API:

1. **Fetch PR**: Call `GetPR` to retrieve the pull request content
2. **Add Comments**: Use `AddComment` to add inline comments as you review
3. **Edit/Delete**: Use `EditComment` or `DeleteComment` to modify pending comments
4. **Set Feedback**: Optionally use `SetFeedback` to add a top-level review message
5. **Submit Review**: Call `SubmitReview` with the appropriate event type to publish the review to GitHub
6. **Sync**: Use `SyncPR` to fetch the latest state after submission
7. **Navigate**: Use `GetAdjacentPR` to move to the next or previous PR in the review queue without returning to the list; navigation wraps around at both ends

Alongside that loop:

- **Expand context**: `GetHunkContext` fetches lines around a hunk boundary when the diff's own context isn't enough to judge a change.
- **Plugin results**: `GetPluginOutput` polls for plugin results, which arrive asynchronously; `RerunPlugins` forces a re-execution.

Steps 2–5 each return [the PR payload](#the-pr-payload) in full, so a client can apply the reply directly rather than tracking which parts of its local state a mutation invalidated.

---

## Error Handling

Errors are returned in the standard JSON-RPC format. Common error scenarios:

- GitHub API errors (rate limiting, authentication, network issues)
- Database errors (local comment storage)
- Invalid PR references (non-existent owner/repo/number)

---

## Example Request/Response

**Request** (GetPR):
```json
{
  "method": "RPCHandler.GetPR",
  "params": [{"Owner": "octocat", "Repo": "Hello-World", "Number": 42}],
  "id": 1
}
```

**Response**:
```json
{
  "result": {
    "okay": true,
    "content": "... formatted PR content ...",
    "metadata": {
      "number": 42,
      "title": "Example PR",
      "author": "octocat",
      "state": "open",
      "body": "PR description...",
      "repo_path": "/home/user/code/Hello-World",
      "worktree_path": "/home/user/code/repo_worktrees/42_branch"
    },
    "diff": "--- a/file.txt\n+++ b/file.txt\n...",
    "comments": [
      {
        "id": "12345",
        "author": "octocat",
        "body": "Nice catch!",
        "path": "file.txt",
        "position": "5",
        "created_at": "2023-01-01T12:00:00Z",
        "outdated": false,
        "review_id": 98765,
        "html_url": "https://github.com/octocat/Hello-World/pull/42#discussion_r12345",
        "thread_id": "PRRT_kwDOA...",
        "resolved": true,
        "resolved_by": "coder1"
      }
    ],
    "outdated_comments": [],
    "reviews": [
      {
        "id": 98765,
        "user": "coder1",
        "body": "Looks good!",
        "state": "APPROVED",
        "submitted_at": "2023-01-01T12:05:00Z",
        "html_url": "https://github.com/..."
      }
    ],
    "commits": [
      {
        "sha": "6dcb09b5b57875f334f61aebed695e2e4193db5e",
        "message": "Fix the thing",
        "author": "octocat",
        "date": "2023-01-01T11:00:00Z",
        "url": "https://github.com/..."
      }
    ],
    "feedback": "Draft review body saved locally, not yet submitted",
    "annotations": [
      {
        "plugin": "security_check",
        "filename": "file.txt",
        "line": 12,
        "severity": "warning",
        "content": "Unvalidated input reaches the query"
      }
    ]
  },
  "error": null,
  "id": 1
}
```
