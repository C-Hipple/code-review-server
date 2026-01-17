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
2.  **Environment**: Ensure `GTDBOT_GITHUB_TOKEN` is set in the environment if required.
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
| Field     | Type   | Description                                      |
|-----------|--------|--------------------------------------------------|
| `Content` | string | Org-mode formatted string of all review sections |

---

### `RPCHandler.GetPR`

Fetches a pull request from GitHub and returns it as rendered content (including diff, comments, conversations).

**Arguments** (`GetPRstructArgs`):
| Field    | Type   | Required | Description                          |
|----------|--------|----------|--------------------------------------|
| `Owner`  | string | Yes      | Repository owner (e.g., `"octocat"`) |
| `Repo`   | string | Yes      | Repository name (e.g., `"hello"`)    |
| `Number` | int    | Yes      | Pull request number                  |

**Reply** (`GetPRReply`):
| Field      | Type         | Description                                     |
|------------|--------------|-------------------------------------------------|
| `Okay`     | bool         | `true` if the request succeeded                 |
| `Content`  | string       | Formatted PR response (diff, comments, metadata)|
| `metadata` | PRMetadata   | Structured PR metadata                          |
| `diff`     | string       | Raw diff content                                |
| `comments` | []CommentJSON| List of structured PR comments                  |
| `reviews`  | []ReviewJSON | List of submitted reviews                       |

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

---

### `RPCHandler.SyncPR`

Forces a fresh fetch of the pull request from GitHub, bypassing any cache.

**Arguments** (`SyncPRArgs`):
| Field    | Type   | Required | Description                          |
|----------|--------|----------|--------------------------------------|
| `Owner`  | string | Yes      | Repository owner                     |
| `Repo`   | string | Yes      | Repository name                      |
| `Number` | int    | Yes      | Pull request number                  |

**Reply** (`SyncPRReply`):
| Field      | Type         | Description                                     |
|------------|--------------|-------------------------------------------------|
| `Okay`     | bool         | `true` if the request succeeded                 |
| `Content`  | string       | Formatted PR response (freshly fetched)         |
| `metadata` | PRMetadata   | Structured PR metadata                          |
| `diff`     | string       | Raw diff content                                |
| `comments` | []CommentJSON| List of structured PR comments                  |
| `reviews`  | []ReviewJSON | List of submitted reviews                       |

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

**Reply** (`AddCommentReply`):
| Field      | Type         | Description                                     |
|------------|--------------|-------------------------------------------------|
| `ID`       | int64        | Local ID of the newly created comment           |
| `Content`  | string       | Formatted updated PR content                    |
| `metadata` | PRMetadata   | Structured PR metadata                          |
| `diff`     | string       | Raw diff content                                |
| `comments` | []CommentJSON| List of structured PR comments                  |
| `reviews`  | []ReviewJSON | List of submitted reviews                       |

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

**Reply** (`EditCommentReply`):
| Field      | Type         | Description                                     |
|------------|--------------|-------------------------------------------------|
| `Okay`     | bool         | `true` if the edit succeeded                    |
| `Content`  | string       | Formatted updated PR content                    |
| `metadata` | PRMetadata   | Structured PR metadata                          |
| `diff`     | string       | Raw diff content                                |
| `comments` | []CommentJSON| List of structured PR comments                  |
| `reviews`  | []ReviewJSON | List of submitted reviews                       |

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

**Reply** (`DeleteCommentReply`):
| Field      | Type         | Description                                     |
|------------|--------------|-------------------------------------------------|
| `Okay`     | bool         | `true` if deletion succeeded                    |
| `Content`  | string       | Formatted updated PR content                    |
| `metadata` | PRMetadata   | Structured PR metadata                          |
| `diff`     | string       | Raw diff content                                |
| `comments` | []CommentJSON| List of structured PR comments                  |
| `reviews`  | []ReviewJSON | List of submitted reviews                       |

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

**Reply** (`SetFeedbackReply`):
| Field      | Type         | Description                                     |
|------------|--------------|-------------------------------------------------|
| `ID`       | int64        | ID of the feedback entry                        |
| `Content`  | string       | Formatted updated PR content                    |
| `metadata` | PRMetadata   | Structured PR metadata                          |
| `diff`     | string       | Raw diff content                                |
| `comments` | []CommentJSON| List of structured PR comments                  |
| `reviews`  | []ReviewJSON | List of submitted reviews                       |

---

### `RPCHandler.RemovePRComments`

Removes all local (pending) comments for a specific pull request.

**Arguments** (`RemovePRCommentsArgs`):
| Field    | Type   | Required | Description                          |
|----------|--------|----------|--------------------------------------|
| `Owner`  | string | Yes      | Repository owner                     |
| `Repo`   | string | Yes      | Repository name                      |
| `Number` | int    | Yes      | Pull request number                  |

**Reply** (`RemovePRCommentsReply`):
| Field      | Type         | Description                                     |
|------------|--------------|-------------------------------------------------|
| `Okay`     | bool         | `true` if removal succeeded                     |
| `Content`  | string       | Formatted updated PR content                    |
| `metadata` | PRMetadata   | Structured PR metadata                          |
| `diff`     | string       | Raw diff content                                |
| `comments` | []CommentJSON| List of structured PR comments                  |
| `reviews`  | []ReviewJSON | List of submitted reviews                       |

---

### `RPCHandler.SubmitReview`

Submits a review to GitHub. This will:
1. Fetch all local pending comments for the PR
2. Submit reply comments individually to maintain threading
3. Submit top-level comments as part of a GitHub review
4. Delete all local comments after successful submission

**Arguments** (`SubmitReviewArgs`):
| Field    | Type   | Required | Description                                              |
|----------|--------|----------|----------------------------------------------------------|
| `Owner`  | string | Yes      | Repository owner                                         |
| `Repo`   | string | Yes      | Repository name                                          |
| `Number` | int    | Yes      | Pull request number                                      |
| `Event`  | string | Yes      | Review event type: `APPROVE`, `REQUEST_CHANGES`, or `COMMENT` |
| `Body`   | string | No       | Top-level review body (optional)                         |

**Reply** (`SubmitReviewReply`):
| Field      | Type         | Description                                     |
|------------|--------------|-------------------------------------------------|
| `Okay`     | bool         | `true` if submission succeeded                  |
| `Content`  | string       | Formatted updated PR content                    |
| `metadata` | PRMetadata   | Structured PR metadata                          |
| `diff`     | string       | Raw diff content                                |
| `comments` | []CommentJSON| List of structured PR comments                  |
| `reviews`  | []ReviewJSON | List of submitted reviews                       |

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
| `output` | map[string]PluginResult     | Map of plugin names to their respective results/status |

#### `PluginResult` Object
| Field    | Type   | Description                                                          |
|----------|--------|----------------------------------------------------------------------|
| `result` | string | The captured output (stdout/stderr) of the plugin                    |
| `status` | string | Execution status: `pending`, `success`, or `error`                   |

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
      "description": "PR description..."
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
        "outdated": false
      }
    ],
    "reviews": [
      {
        "id": 98765,
        "user": "coder1",
        "body": "Looks good!",
        "state": "APPROVED",
        "submitted_at": "2023-01-01T12:05:00Z",
        "html_url": "https://github.com/..."
      }
    ]
  },
  "error": null,
  "id": 1
}
```
