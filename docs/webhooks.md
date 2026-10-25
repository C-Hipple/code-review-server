# GitHub Webhooks

By default the server polls: every cycle (`SleepDuration` minutes) it lists the PRs in your repositories and then asks GitHub, PR by PR, for their reviews, CI and the rest — even when nothing changed. With webhooks configured, GitHub tells the server when something changes instead, and a cycle shrinks to:

- **Listing the PRs**, as before — that is how new PRs are found and gone ones dropped.
- **Fetching in full only what is new**: a PR the server hasn't seen, or one whose head moved (a push changes the diff, which no webhook carries).

Everything else — reviews, comments, review threads, CI, review requests, titles, labels, drafts, merges — is written into the server's caches from the webhook deliveries themselves, without asking GitHub again. A PR whose caches are current is read from them by the cycle, the filters, and the review view alike.

Without a `[Webhooks]` table nothing changes: every PR is polled exactly as before.

## Setting It Up

### 1. Make the server reachable from GitHub

GitHub has to be able to reach the port the server listens on. The server usually runs on your own machine, so put it behind a tunnel — [smee.io](https://smee.io) (`smee --url https://smee.io/<channel> --target http://127.0.0.1:8790/github/webhook`), `cloudflared`, `ngrok` — or run it on a host GitHub can reach. Binding to `127.0.0.1` keeps the port off your network when a tunnel forwards to it.

### 2. Configure `[Webhooks]`

```toml
[Webhooks]
ListenAddress = "127.0.0.1:8790"
Repos = ["my-org/*", "me/side-project"]
# Path = "/github/webhook"
# FullRefreshInterval = 360
```

and give the server the webhook's secret:

```sh
export CRS_WEBHOOK_SECRET="the secret you give GitHub below"
```

| Key | Meaning |
| --- | --- |
| `ListenAddress` | Where to listen, as `host:port`. Required — without it nothing listens. |
| `Secret` | The webhook's secret. Leave it out of the file and export `CRS_WEBHOOK_SECRET` instead (the file's value wins when both are set). Required: unsigned deliveries are never accepted, so without a secret nothing listens. |
| `Path` | The URL path GitHub posts to. Default `/github/webhook`. |
| `Repos` | The repositories whose webhooks deliver here: `owner/repo`, or `owner/*` for an organization webhook. Only PRs in these are left to webhooks; any other PR is still polled. Default: every repository the config already names — the root `Repos` and each workflow's own. The search-driven workflows (`WaitingOnMeWorkflow`, `MyReviewRequestsWorkflow`) name none, so a config made only of them needs `Repos` set. |
| `FullRefreshInterval` | Minutes between cycles that refresh every PR as if webhooks were off, to catch up on any delivery that never arrived. Default 360 (six hours). Set it to `SleepDuration` or less and every cycle refreshes everything, with webhooks only filling in between — see [Rolling It Out](#rolling-it-out). |

The table is re-read every cycle, so adding, changing or removing it takes effect on the next sync, without a restart.

### 3. Create the webhook on GitHub

In the repository's (or organization's) **Settings → Webhooks → Add webhook**:

- **Payload URL**: the public URL that reaches `ListenAddress`, ending in `Path` — e.g. `https://smee.io/<channel>` or `https://crs.example.com/github/webhook`.
- **Content type**: `application/json`.
- **Secret**: the same value as `CRS_WEBHOOK_SECRET`.
- **Which events**: *Let me select individual events*, then tick:
    - Pull requests
    - Pull request reviews
    - Pull request review comments
    - Pull request review threads
    - Issue comments
    - Check runs
    - Statuses
    - Pushes

GitHub sends a ping when the webhook is created; the server logs `GitHub webhook ping received`.

### Rolling It Out

Webhooks can start as a supplement to polling rather than a replacement for it. With `FullRefreshInterval` no longer than `SleepDuration`, every cycle refreshes every PR exactly as without webhooks, and deliveries keep the caches and sections current between cycles:

```toml
SleepDuration = 10

[Webhooks]
ListenAddress = "127.0.0.1:8790"
Repos = ["my-org/*"]
FullRefreshInterval = 10   # every cycle refreshes everything
```

A review, comment, CI result or review request then shows up within seconds rather than at the next cycle, and anything a delivery gets wrong or misses — one lost while the laptop slept, two arriving out of order — is put right by that next cycle, no more than `SleepDuration` later. It saves no API calls: every row in `APICallStats` reads `mode = full_refresh`, and `webhook_api_calls` is what the deliveries added on top (a push's full fetch, which the next cycle then finds already done, and a new review thread's lookup).

Once the deliveries have proved reliable — the log's `Webhook delivery updated a PR` and `Webhook ... section` lines agree with what you see on GitHub — raise `FullRefreshInterval` (to an hour, then the default six) to start saving calls, and compare the modes in `APICallStats` (see [Measuring What It Saves](#measuring-what-it-saves)).

## What Each Delivery Updates

| Event | Updates | Calls GitHub? |
| --- | --- | --- |
| Pull requests | Title, body, state, draft, labels, assignees and review requests: the PR's metadata, its requested reviewers, its required-team chips, mergeability when GitHub sends it, and its rows on the review list (status, title, tags). | Only when the head moved (`synchronize`, or a reopened PR pushed to while closed) or the base branch changed: then the PR is fetched in full, like a newly added one, and plugins and automatic AI features run for the new head. |
| Pull request reviews | The PR's reviews, so approvals, change requests and dismissals; who approved in its metadata; its requested reviewers and required-team chips. | No. |
| Pull request review comments | The PR's comments, and its review threads when the comment replies in or is deleted from one. | Only for a comment starting a new thread: the thread's ID, which the *Resolve* button needs, comes from GraphQL alone. |
| Pull request review threads | Whether each thread is resolved, and by whom. | No. |
| Issue comments | The PR's conversation comments (comments on issues are ignored). | No. |
| Check runs, Statuses | The CI of the commit they ran on, and the CI line in the PR's metadata when that commit is the PR's head. A re-run replaces the attempt before it. | No. |
| Pushes | Nothing directly: a push to a PR's base branch marks the PR's mergeability for re-checking on the next cycle. | No — the next cycle asks GitHub about those PRs only. |

Deliveries are applied to every PR the server holds: one a section lists, or one a cycle has cached because a workflow's repositories list it — the filters decide which PRs join a section from those caches, so they are kept current too. A delivery about any other PR is ignored; the cycle that first lists it fetches it in full.

A delivery never *starts* one of a PR's lists (its reviews, comments, review threads or a commit's CI): a list holding only the one review a delivery mentions would look complete. A PR with nothing cached is left to the next cycle.

## Sections Follow Deliveries

A delivery can change which sections a PR belongs in, not only what its rows say: submitting a review clears your review request, which takes the PR out of *Review Requested*; an author's reply to your thread puts it into *Waiting on me*; converting a PR to a draft takes it out of a `FilterNotDraft` section. Rather than wait for the next cycle's listing, after each delivery the server checks the PR against every workflow again, from the caches the delivery just updated:

- **Added** to a section whose workflow it now matches,
- **rewritten** — details included — in one it still matches,
- **dropped** from one it no longer matches; a row no workflow claims any more is removed.

What "matches" means is what the workflow's own run would decide: for a repository-list workflow, the PR's repository and state and its filters; for *Review Requested*, a review request to you or one of your teams, then its filters; for *Waiting on me*, its filters. A project-list workflow's PRs are whatever its Jira epic links, which a PR can't tell, so there a delivery can only drop a PR its filters now reject.

This happens only while webhooks are trusted for the PR, and only when its caches hold its current head, so it never asks GitHub for anything. A delivery that lands while a cycle is refreshing everything is checked as soon as that cycle finishes. A delivery with no PR in it (a conversation comment, a check run, a commit status) is judged against the PR as the last listing or delivery showed it. If a cycle is writing sections when a delivery lands, the check waits for it to finish, so a cycle working from an older listing can't undo it. A burst of deliveries about one PR — a push sets off a check run delivery per check — is checked once.

## When the Server Still Polls

Every PR is still polled, as without webhooks:

- **Until the listener is up.** Only the process running the background sync listens (with several clients open, that's the first server started). `--oneoff` never listens. Nothing listens without a secret, and if the port can't be bound the server logs why and tries again next cycle.
- **For the first cycle after the listener comes up.** Anything that changed while nothing was listening was never delivered, so that cycle catches up on it.
- **Every `FullRefreshInterval`**, in case a delivery never arrived — a tunnel down for a minute, GitHub giving up on a slow delivery.
- **For PRs outside `Repos`.**

A cycle that refreshes everything logs `Refreshing every PR ...`; one that leaves PRs to webhooks logs `Webhook cycle: ...`, and `Pre-fetched auxiliary data` reports how many PRs it read from the caches (`from_webhook_caches`). Each cache write a delivery makes is recorded in the workflow action log under the workflow name `webhook`.

## What Webhooks Don't Cover

- **Reactions.** GitHub has no webhook for them, so who reacted to what is refreshed when the PR is next fetched in full: after a push, at the periodic full refresh, or when you sync the PR from a client.
- **Mergeability after the base branch moves**, unless *Pushes* is ticked: without it, a PR's conflict flag is re-checked only when its own head moves or at the full refresh.
- **The details text of a review-list row** (the org-mode body the Emacs client shows) for a PR whose sections can't be re-checked from a delivery — a project-list workflow's, or one while webhooks aren't trusted — is rebuilt on the next cycle; a delivery still updates the row's status, title and tags straight away.
- **CI lines on a review-list row** come from the commit's check runs and statuses rather than the Actions runs on the PR's branch, so `FilterCIPassing` / `FilterCIFailing` judge a webhook-covered PR by those too.

## Measuring What It Saves

Each cycle writes one row to the `APICallStats` table in `~/.crs/codereviewserver.db`, also served by the `GetRateLimitHistory` RPC. Besides what the cycle itself fetched, by type (`pr_list`, `reviews`, `check_runs`, ... and their sum, `total`), a row records:

| Column | Meaning |
| --- | --- |
| `mode` | `polling` (webhooks off, or nothing listening), `full_refresh` (webhooks on, but this cycle fetched every PR: the first after the listener came up, or the periodic one) or `webhook`. Empty on rows from before it was recorded. |
| `requests` | REST requests the server sent GitHub since the previous row, counted on the wire: the cycle's own — including the ones `total` has no type for, such as the waiting-on-me filters' lookups, mergeability and searches — plus everything webhook deliveries and the review view asked for in between. |
| `not_modified` | How many of those GitHub answered `304 Not Modified`, which it doesn't charge for. `requests - not_modified` is what was charged. |
| `search_requests`, `graphql_requests` | The same, for the search and GraphQL budgets. |
| `webhook_deliveries`, `webhook_api_calls` | Deliveries applied since the previous row, and the API calls applying them took. |
| `rate_limit_remaining`, `graphql_remaining` | Each budget left after the cycle, from GitHub's `/rate_limit`. |

So comparing the two modes is a query, with each row's `requests` covering the time since the row before:

```sql
SELECT mode, COUNT(*) AS cycles, SUM(requests) AS requests,
       SUM(requests - not_modified) AS charged, SUM(graphql_requests) AS graphql
FROM APICallStats
WHERE requests >= 0
GROUP BY mode;
```

Two things to keep in mind: the budget is shared with anything else using the same token, which `rate_limit_remaining` sees but `requests` doesn't; and the review view's cache misses count in whichever mode they happen in.

The log carries the same, per cycle: `GitHub API calls this cycle mode=...`, `GitHub rate limit post-cycle mode=... requests_since_last_cycle=...`, and, with webhooks, `Webhook activity since the last cycle` (deliveries applied, ignored and rejected, refetches, API calls, section rows added and dropped). Each delivery that changes something logs `Webhook delivery updated a PR`, `Fetched a PR in full after a webhook delivery`, or `Webhook put a PR into a section` / `took a PR out of a section`, and every request to GitHub is logged as `GitHub API Call`. Set `CRS_LOG_FILE` and `CRS_LOG_FORMAT=json` to have it in a file one JSON object per line (see [Command-Line Interface](cli.md#logging)).

## On a Hosted Server

On a [hosted server](hosted_server.md#github-webhooks) the server's `[Webhooks]` table serves everyone. The PR caches are shared, so deliveries keep every user's review view current, and a webhook cycle reads the PRs it covers from the caches for every dashboard's workflows alike. [Sections follow deliveries](#sections-follow-deliveries) on the server's own dashboard — its config file's workflows; a user's dashboard follows the cycles, its rows' status, title and tags included. `Repos` defaults to the repositories the server's config names, so PRs a user's workflows list elsewhere are polled; an organization webhook (`owner/*`) covers them.

## Security

A delivery is acted on only if its `X-Hub-Signature-256` (or the older `X-Hub-Signature`) matches the secret; anything else is answered `401` and logged. Deliveries are capped at GitHub's own limit of 25 MB, acknowledged straight away, and applied one at a time in the order they arrived; if a backlog builds up, the server answers `503` so GitHub records the delivery as failed and it can be redelivered from the webhook's *Recent Deliveries* page.
