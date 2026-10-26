# GitHub Webhooks

By default the server polls: every cycle (`SleepDuration` minutes) it lists the PRs in your repositories and then asks GitHub, PR by PR, for their reviews, CI and the rest — even when nothing changed. With webhooks configured, GitHub tells the server when something changes instead, and a cycle shrinks to:

- **Listing the PRs**, as before — that is how new PRs are found and gone ones dropped.
- **Fetching in full only what is new**: a PR the server hasn't seen, or one whose head moved (a push changes the diff, which no webhook carries).

Everything else — reviews, comments, review threads, CI, review requests, titles, labels, drafts, merges — is written into the server's caches from the webhook deliveries themselves, without asking GitHub again. A PR whose caches are current is read from them by the cycle, the filters, and the review view alike.

Deliveries reach the server one of two ways: GitHub posts each one to a port the server listens on, or — when your organization already relays its GitHub webhooks into a Google Cloud Pub/Sub topic — the server pulls them from a subscription to that topic (see [Receiving Deliveries from Pub/Sub](#receiving-deliveries-from-pubsub)). Everything they do once they arrive is the same.

Without a `[Webhooks]` table nothing changes: every PR is polled exactly as before.

## Setting It Up

These steps have GitHub deliver straight to the server. If your organization publishes its webhooks to Pub/Sub, follow [Receiving Deliveries from Pub/Sub](#receiving-deliveries-from-pubsub) instead.

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

## Receiving Deliveries from Pub/Sub

Some organizations receive GitHub's webhooks with a service of their own, which checks each delivery's signature and publishes it to a [Google Cloud Pub/Sub](https://cloud.google.com/pubsub/docs/overview) topic that any team can subscribe to. If yours does, the server can pull deliveries from a subscription to that topic rather than listening for them: no port open to GitHub, no tunnel, no webhook secret, and nothing to set up on GitHub. Everything else on this page — what each delivery updates, sections and clients following deliveries, the periodic full refresh — works the same.

### What the Messages Must Look Like

Each message is one delivery:

- **Its data** is the delivery's JSON body, exactly as GitHub sent it.
- **An attribute names the event**, what GitHub sends as the `X-GitHub-Event` header (`pull_request`, `check_run`, ...). The server reads `event_type` unless `EventTypeAttribute` names another.
- **An attribute holds the delivery's ID**, what GitHub sends as `X-GitHub-Delivery`. The server reads `delivery_id` unless `DeliveryIDAttribute` names another. It is optional: it names the delivery in the log, and lets the server drop one GitHub delivery published twice (a redelivery, a publisher's retry). Without it the message's ID stands in, which still drops a message Pub/Sub hands over twice.

The topic needs to carry the eight events [listed above](#3-create-the-webhook-on-github); an organization-wide relay usually carries every event, and the server acknowledges and drops the ones it doesn't use.

### 1. Create a Subscription

Create one subscription for each server. Pub/Sub splits a subscription's messages between everyone pulling from it, so two servers — yours and a teammate's — sharing one would each see only some of the deliveries.

```sh
gcloud pubsub subscriptions create crs-github-events-$USER \
  --project=my-project \
  --topic=projects/my-org-ci/topics/github-webhooks \
  --ack-deadline=60 \
  --message-retention-duration=1d \
  --expiration-period=never \
  --message-filter='hasPrefix(attributes.event_type, "pull_request") OR attributes.event_type = "issue_comment" OR attributes.event_type = "check_run" OR attributes.event_type = "status" OR attributes.event_type = "push"'
```

- **`--topic`** is the full name of your organization's topic. The subscription can live in a project of your own; whoever owns the topic may have to let you attach subscriptions to it (`roles/pubsub.subscriber` on the topic).
- **`--expiration-period=never`** keeps the subscription around. By default Pub/Sub deletes one nothing has pulled from for 31 days — a laptop left closed over a long holiday.
- **`--message-retention-duration`** is how long messages wait while the server isn't pulling. The server drops anything published before it started pulling (see [How It Differs](#how-it-differs-from-receiving-deliveries-directly)), so there's no point keeping them long; a day is plenty.
- **`--message-filter`** is optional. It saves pulling deliveries the server would drop, but can only test the attributes your organization's publisher sets — the example assumes `event_type`; check what yours sets. A filter can't be changed once the subscription exists, and is at most 256 bytes. The example takes 200, so narrowing it to your organization's repositories as well — `(...) AND hasPrefix(attributes.repository, "my-org/")`, if the publisher sets `repository` to `owner/repo` — only fits for an organization name of up to 12 characters. If the topic carries several organizations' deliveries and most of them aren't yours, filter on `hasPrefix(attributes.repository, "my-org/")` alone instead; the server drops the events it doesn't use either way.

### 2. Give the Server Credentials

Let the account the server runs as pull from the subscription:

```sh
gcloud pubsub subscriptions add-iam-policy-binding crs-github-events-$USER \
  --project=my-project \
  --member="user:you@example.com" \
  --role=roles/pubsub.subscriber
```

(`serviceAccount:...` for a service account.) `roles/pubsub.subscriber` on the subscription is all the server needs to pull, acknowledge and hand messages back. With your own account it also has to be allowed to use the quota project below (`roles/serviceusage.serviceUsageConsumer`, which a project's owners and editors already have).

The server signs in with [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials):

- **Your own account**: run `gcloud auth application-default login`, then `gcloud auth application-default set-quota-project my-project`. User credentials need a project to charge the calls to; the server sends the file's quota project with each call (`GOOGLE_CLOUD_QUOTA_PROJECT` overrides it).
- **A service account key**: name the file in `CredentialsFile`, or export `GOOGLE_APPLICATION_CREDENTIALS`.
- **On Google Cloud** (Compute Engine, GKE, Cloud Run): the attached service account, with nothing to set.

A client that starts the server without your shell's environment — the Chrome extension, an Emacs started from the Dock — doesn't pass `GOOGLE_APPLICATION_CREDENTIALS` on. Use `CredentialsFile` there, or the file `gcloud auth application-default login` writes, which the server finds in your home directory.

### 3. Configure `[Webhooks.PubSub]`

```toml
[Webhooks]
Repos = ["my-org/*"]

[Webhooks.PubSub]
Subscription = "projects/my-project/subscriptions/crs-github-events-me"
# CredentialsFile = "~/.config/crs/pubsub-key.json"
# EventTypeAttribute = "event_type"
# DeliveryIDAttribute = "delivery_id"
```

| Key | Meaning |
| --- | --- |
| `Subscription` | The subscription to pull from, as `projects/<project>/subscriptions/<subscription>`. Required: setting it is what makes the server pull rather than listen. |
| `CredentialsFile` | A credentials JSON file to pull with: a service account key, or what `gcloud auth application-default login` writes. Default: Application Default Credentials, as above. An absolute path, or one starting with `~/`. It is read when the server starts pulling and again whenever Google refuses a pull or a token, so a new key written over the old file is picked up without a restart once the old one stops working. |
| `EventTypeAttribute` | The message attribute naming the GitHub event. Default `event_type`. |
| `DeliveryIDAttribute` | The message attribute holding the delivery's ID. Default `delivery_id`. |

`ListenAddress`, `Path` and `Secret` don't apply; leave them out — with `ListenAddress` set as well, nothing listens and the configuration is reported as invalid. `Repos` and `FullRefreshInterval` mean what they do for direct deliveries: `Repos` names the repositories whose deliveries reach the topic.

Like the rest of `[Webhooks]`, the table is re-read every cycle. Once a pull succeeds the server logs `Receiving GitHub webhook deliveries from Pub/Sub`.

### How It Differs from Receiving Deliveries Directly

- **The server counts as receiving once a pull succeeds.** A subscription that doesn't exist, credentials that may not pull from it, or credentials Google won't issue a token for — an expired `gcloud` login, a revoked or deleted service account key — leave every PR polled: the server logs `Can't pull GitHub webhook deliveries from Pub/Sub` with the reason and tries again, backing off to once a minute and reading the credentials again each time, so a fresh `gcloud auth application-default login` is picked up without a restart. (Impersonated and workload identity credentials report a refusal less plainly: it is retried like a network failure, with the credentials read again each time, and leaves every PR polled after two minutes.) When pulls keep failing for two minutes for any other reason — the network down, pulls going unanswered, which a pull waits up to two minutes to find out — every PR is polled until one succeeds again. An empty pull on a quiet subscription is a success.
- **Deliveries published before then are dropped.** The subscription keeps messages while nothing pulls, so a server that was stopped comes back to a backlog. Applying hours-old deliveries, in no particular order, over what the catch-up refresh fetches could only roll it back, so the server acknowledges and drops them, and — as when the HTTP listener comes up — the first cycle after it starts receiving refreshes every PR instead. Deliveries count from when the server started pulling, which the subscription kept everything published since, so the first cycle after a restart is that catch-up refresh even when it starts before the first pull comes back — a quiet subscription's pull can take up to a minute — and is logged as a polling one.
- **A short outage loses nothing.** While the network blips, deliveries wait in the subscription and are applied once pulls resume, where a direct delivery sent while the tunnel was down is lost. A longer one is treated like a restart — pulls failing for two minutes, as above, or four minutes passing between two successful pulls, which is what a laptop that slept looks like: what was published meanwhile is dropped, and the next cycle refreshes every PR.
- **A delivery can arrive twice**, since Pub/Sub delivers at least once. The server remembers the last few thousand delivery IDs and drops a repeat.
- **Order isn't guaranteed.** Each pull's messages are applied oldest first. A pull request, review, review comment or review thread delivery whose PR has a head other than the cached one has the PR fetched in full, as for a push — at the newest head the PR has been seen at (the PR with the latest `updated_at` from a listing or delivery), so a late delivery about an older head doesn't roll the cache back to it. One at the cached head writes the PR's title, state and reviewers from the newest seen at that head, not from its own, out-of-date copy. A late check run or status only updates the CI cached for its own commit. Anything a late delivery still gets wrong is put right by the next full refresh.
- **Deliveries over 10 MB never arrive**: that is Pub/Sub's limit on a message, where GitHub's on a delivery is 25 MB. The periodic full refresh catches up on what they said.
- **A message is acknowledged once it is queued to be applied**, as a direct delivery is answered `202`. So is one that can never be applied — no event attribute, a body that doesn't parse — so that it isn't delivered again and again.

### Trying It Locally

With the [Pub/Sub emulator](https://cloud.google.com/pubsub/docs/emulator) running (`gcloud beta emulators pubsub start`), export `PUBSUB_EMULATOR_HOST=localhost:8085` before starting the server: it then pulls from the emulator over plain HTTP, without credentials. Publish a test delivery to the emulator's topic with the attributes above and the server applies it like any other.

## What Each Delivery Updates

| Event | Updates | Calls GitHub? |
| --- | --- | --- |
| Pull requests | Title, body, state, draft, labels, assignees and review requests: the PR's metadata, its requested reviewers, its required-team chips, mergeability when GitHub sends it, and its rows on the review list (status, title, tags). | Only when the delivery's head isn't the cached one (`synchronize`, a reopened PR pushed to while closed, or a late delivery from before a push) or the base branch changed: then the PR is fetched in full, like a newly added one, and plugins and automatic AI features run for the new head. |
| Pull request reviews | The PR's reviews, so approvals, change requests and dismissals; who approved in its metadata; its requested reviewers and required-team chips. | Only when the delivery's PR head isn't the cached one (a push it arrived ahead of, or a late delivery from before one): then the PR is fetched in full. |
| Pull request review comments | The PR's comments, and its review threads when the comment replies in or is deleted from one. | For a comment starting a new thread: the thread's ID, which the *Resolve* button needs, comes from GraphQL alone. And, as for reviews, when the delivery's PR head isn't the cached one. |
| Pull request review threads | Whether each thread is resolved, and by whom. | Only when the delivery's PR head isn't the cached one, as for reviews. |
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

## Clients Follow Deliveries

The server also tells a subscribed client about every PR a delivery changed — a [`Server.PRUpdated`](protocol.md#serverprupdated) notification naming the PR, what was rewritten and, in a few words, what happened — and again when the check above rewrites the PR's section rows. The web client takes it from there: an open review fetches the PR again from the caches and takes in the change where it stands, keeping the reader's place, and a toast says what happened (*alice approved*, *Checks failed: build*); the review list refreshes the PR's row and highlights it for a moment. CI is announced only as it settles — passing, or failing where it wasn't — though the CI line updates on every delivery; your own changes refresh without a toast. See [Live updates from webhooks](web_client_features.md) (§3.18).

## When the Server Still Polls

Every PR is still polled, as without webhooks:

- **Until the listener is up** — or, with Pub/Sub, until a pull from the subscription succeeds, and again while it can't pull. Only the process running the background sync listens or pulls (with several clients open, that's the first server started). `--oneoff` never does. Nothing listens without a secret, and if the port can't be bound the server logs why and tries again next cycle.
- **For the first cycle after the listener comes up**, or after pulls from Pub/Sub start succeeding — at first, or again after an outage, a sleep or a refused pull. Anything that changed before then was never delivered — or, from Pub/Sub, is dropped — so that cycle catches up on it.
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
| `mode` | `polling` (webhooks off, or nothing receiving deliveries), `full_refresh` (webhooks on, but this cycle fetched every PR: the first after the listener came up or Pub/Sub pulls started succeeding (again), or the periodic one) or `webhook`. Empty on rows from before it was recorded. |
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

## Security

A delivery is acted on only if its `X-Hub-Signature-256` (or the older `X-Hub-Signature`) matches the secret; anything else is answered `401` and logged. Deliveries are capped at GitHub's own limit of 25 MB, acknowledged straight away, and applied one at a time in the order they arrived; if a backlog builds up, the server answers `503` so GitHub records the delivery as failed and it can be redelivered from the webhook's *Recent Deliveries* page.

Deliveries pulled from Pub/Sub carry no signature: the service that published them checked GitHub's, and the topic's IAM policy decides who else may publish. The server applies whatever the subscription hands it, so publishing to the topic should be limited to that service. Even so, a delivery only ever patches the caches of PRs the server already tracks, and anything it needs fetched — a new head — is fetched from GitHub with your own token. When deliveries come in faster than they are applied, the server stops pulling until its queue has room and the rest wait in the subscription, rather than being turned away as a direct delivery is when the queue is full.
