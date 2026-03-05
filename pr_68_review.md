## PR #68 Code Review: Add PR-level Review Feedback Persistence

### Summary
This PR wires up an existing `Feedback` DB table to a new UI panel in the React frontend. The infrastructure is well-thought-out, but there are a few design issues worth addressing.

---

### Issues

#### 1. `SetFeedback` triggers full PR re-fetch (performance concern)
**File:** `server/server.go` — `SetFeedback` handler

```go
func (h *RPCHandler) SetFeedback(args *SetFeedbackArgs, reply *SetFeedbackReply) error {
    err := config.C().DB.InsertFeedback(...)
    ...
    details, content, err := h.fetchPRAndRunPlugins(...)  // expensive!
    ...
}
```

`fetchPRAndRunPlugins` fetches the PR diff, comments, reviews, and triggers async plugin execution. For a simple "save draft text" operation this is overkill and wastes GitHub API rate limit. `SetFeedbackReply` should be a lightweight `{ okay bool }` response — the client already has the PR data and doesn't need it refreshed.

#### 2. Unused `ID` field in `SetFeedbackReply`
**File:** `server/server.go`

```go
type SetFeedbackReply struct {
    ID       int64         `json:"id"`   // always 0; never set
    Content  string        ...
    ...
}
```

This appears to be copy-paste from `AddCommentReply`. Since `InsertFeedback` does an upsert with no meaningful row ID to return, the `ID` field is always 0 and is misleading. Remove it.

#### 3. Error silently ignored for `GetFeedback`
**File:** `server/server.go` — `GetPR` and `SyncPR` handlers

```go
feedback, _ := config.C().DB.GetFeedback(args.Owner, args.Repo, args.Number)
```

The error is discarded with `_`. While the codebase does this in some other places, it would be better to at least log it:
```go
feedback, err := config.C().DB.GetFeedback(args.Owner, args.Repo, args.Number)
if err != nil {
    h.Log.Warn("Error fetching feedback", "error", err)
}
```

#### 4. `Feedback` not included in other reply types
**File:** `server/server.go`

`Feedback string` was added to `GetPRReply` and `SyncPRReply`, but not to `AddCommentReply`, `EditCommentReply`, `DeleteCommentReply`, or `SubmitReviewReply`. All these handlers call `fetchPRAndRunPlugins` and return a refreshed PR view to the client — but without `Feedback`, the client would lose its saved feedback state if it relied on any of those responses to update local state.

#### 5. Feedback not cleared after `SubmitReview`
**File:** `server/server.go` — `SubmitReview` handler

After a review is successfully submitted to GitHub, local comments are deleted:
```go
err = config.C().DB.DeleteLocalCommentsForPR(args.Owner, args.Repo, args.Number)
```
But the saved feedback draft is left in the DB. If the user opens the same PR again after submitting, the old feedback would pre-populate. Consider deleting the feedback after a successful submission, similar to how local comments are cleaned up.

#### 6. Frontend: pre-filling review body with feedback overwrites user input
**File:** `bun_client/frontend/src/components/Review.tsx`

```ts
setReviewBody(feedbackBody);
```

When the user clicks "Submit Review," this unconditionally overwrites any text they may have typed directly in the review body field. If the user has typed something in the review body and also has saved feedback, the saved feedback takes precedence. The intent seems to be "use feedback as default if body is empty" — consider `setReviewBody(prev => prev || feedbackBody)` instead.

---

### Minor Nits

- `isSavingFeedback` state variable tracks save in progress — good UX, but the button label should probably change to "Saving..." while in progress (if it doesn't already).
- `database.go`'s new `GetFeedback` method is clean and consistent with existing patterns. No issues.

---

### Verdict
The core idea is solid and the DB schema (`Feedback` table with `UNIQUE(owner, repo, number)` for upsert) is well-designed. The main concerns are:
1. The expensive `SetFeedback` response (fix this)
2. Missing feedback in other reply types (fix for consistency)
3. Stale feedback after review submission (fix for correctness)
