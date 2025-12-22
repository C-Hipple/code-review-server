# Code Review Server Protocol

This document describes the JSON-RPC API exposed by the code review server. The server communicates over **stdio** using the JSON-RPC 1.0 protocol, making it suitable for integration with editors like Emacs.

## Transport

- **Protocol**: JSON-RPC 1.0
- **Transport**: Standard input/output (stdin/stdout)
- **Encoding**: JSON

All methods are exposed under the `RPCHandler` namespace (e.g., `RPCHandler.GetPR`).

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
| Field     | Type   | Description                                     |
|-----------|--------|-------------------------------------------------|
| `Okay`    | bool   | `true` if the request succeeded                 |
| `Content` | string | Full PR response (diff, comments, metadata)     |

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
| Field     | Type   | Description                                     |
|-----------|--------|-------------------------------------------------|
| `Okay`    | bool   | `true` if the request succeeded                 |
| `Content` | string | Full PR response (freshly fetched)              |

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
| Field     | Type   | Description                                     |
|-----------|--------|-------------------------------------------------|
| `ID`      | int64  | Local ID of the newly created comment           |
| `Content` | string | Updated PR content with the new comment         |

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
| Field     | Type   | Description                                     |
|-----------|--------|-------------------------------------------------|
| `Okay`    | bool   | `true` if the edit succeeded                    |
| `Content` | string | Updated PR content                              |

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
| Field     | Type   | Description                                     |
|-----------|--------|-------------------------------------------------|
| `Okay`    | bool   | `true` if deletion succeeded                    |
| `Content` | string | Updated PR content                              |

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
| Field     | Type   | Description                                     |
|-----------|--------|-------------------------------------------------|
| `ID`      | int64  | ID of the feedback entry                        |
| `Content` | string | Updated PR content                              |

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
| Field     | Type   | Description                                     |
|-----------|--------|-------------------------------------------------|
| `Okay`    | bool   | `true` if removal succeeded                     |
| `Content` | string | Updated PR content                              |

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
| Field     | Type   | Description                                     |
|-----------|--------|-------------------------------------------------|
| `Okay`    | bool   | `true` if submission succeeded                  |
| `Content` | string | Updated PR content (reflecting GitHub state)    |

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
    "Okay": true,
    "Content": "... rendered PR content ..."
  },
  "error": null,
  "id": 1
}
```
