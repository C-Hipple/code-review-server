# Hosted Server

One `crs` process can keep review dashboards for a whole team. Each person
runs a client — the web UI's bridge or Emacs — that connects to the shared
server instead of starting a server of its own. The server polls GitHub once
for everybody, with one token, and keeps each person's dashboard, drafts and
feedback apart. Reviews and merges go out as each person's own GitHub
account, once they have [linked it](#linking-your-github-account).

```
 web UI / Emacs ──┐
 web UI / Emacs ──┼── TLS ──> proxy ──TCP──> crs --listen ──> GitHub (CRS_GITHUB_TOKEN)
 web UI / Emacs ──┘
```

A hosted server is meant for a team's internal network. Anyone who can reach
it and holds the registration key can register under any GitHub login (see
[Limitations](#limitations)).

## Running it

```bash
export CRS_GITHUB_TOKEN=...        # reads and polling, for every user
export CRS_REGISTRATION_KEY=...    # handed to the team; without it nobody can register
export CRS_ADMIN_TOKEN=...         # optional: the server administrator's own token
export CRS_SECRET_KEY=...          # for GitHub account linking: encrypts the users' tokens
export CRS_GITHUB_OAUTH_CLIENT_SECRET=...  # optional, for linking: refresh and revoke tokens
crs --listen 127.0.0.1:7700
```

`--listen host:port` runs the workflow cycle and its hooks (plugins, automatic
AI features) exactly as `-server` does, and serves the [RPC protocol](protocol.md)
over TCP to any number of clients instead of to one over stdio. It does not
read stdin, and cannot be combined with `-server` or `-oneoff`.

The server's own [config file](configuration.md) still applies: `SleepDuration`,
`Plugins`, `AIFeatures`, `JiraDomain`, `RepoLocation` and the rest are the
server's, and its `[[Workflows]]` fill the dashboard of the server's own
identity — the one the administrator token sees. It may have no workflows at
all.

### Secrets

They come from the environment, never the config file, and the server never
logs them.

| Variable | What it does |
|----------|--------------|
| `CRS_REGISTRATION_KEY` | The key a client presents to register a user. Unset, registration is disabled: users already registered can still connect, nobody new can. The server warns at startup |
| `CRS_ADMIN_TOKEN` | Authenticates a connection as the server's own identity: its dashboard is the config file's workflows, and it is an administrator. A user who registers with it as `AdminToken` becomes an administrator too. Unset, only users registered as administrators are |
| `CRS_SECRET_KEY` | Encrypts the GitHub tokens of users who [link their accounts](#linking-your-github-account). Unset or invalid, linking is unavailable |
| `CRS_GITHUB_OAUTH_CLIENT_SECRET` | Optional, for linking: lets the server refresh expiring tokens and revoke tokens on GitHub |

Use long random values, e.g. `openssl rand -hex 32`.

### Behind a TLS proxy

`crs --listen` speaks plain TCP. Bind it to localhost (or a private interface)
and put a proxy in front that terminates TLS. The protocol is newline-delimited
JSON-RPC, not HTTP, so the proxy has to pass a TCP stream through — nginx's
`stream` module, HAProxy in `mode tcp`, stunnel and the like:

```nginx
stream {
    server {
        listen 7443 ssl;
        ssl_certificate     /etc/ssl/crs.pem;
        ssl_certificate_key /etc/ssl/crs.key;
        proxy_pass 127.0.0.1:7700;
        proxy_timeout 1h;   # connections stay open for as long as a client runs
    }
}
```

Connections are long-lived: a client keeps one open while it runs and
reconnects when it drops. A connection that does not authenticate within 30
seconds is closed.

### GitHub webhooks

A `[Webhooks]` table in the server's config works as it does for one user
(see [GitHub Webhooks](webhooks.md)). Deliveries arrive over HTTP at its
`ListenAddress`, an address of its own beside `--listen`'s, which GitHub has to
be able to reach. The PR caches are shared, so a delivery keeps every user's
review view current, and a webhook cycle reads the PRs it covers from the
caches whichever dashboards list them. Sections follow deliveries on the
server's own dashboard; a user's dashboard — which sections a PR is in, and
its rows' status, title and tags — catches up on the next cycle. `Repos`
defaults to the repositories the server's config names, so PRs that users'
workflows list elsewhere go on being polled: an organization webhook
(`owner/*`) covers them.

## Clients

Pointing the web UI's bridge or Emacs at a hosted server is described in
[Clients](clients.md). Both follow the same flow ([protocol](protocol.md#connect-flow)):

1. Connect, and authenticate with the token stored from last time.
2. Without a token, or when the server no longer knows it, register: send the
   registration key, the GitHub login, and the `[[Workflows]]`, `Repos`,
   `SectionPriority` and `SectionSorting` from the client's own config file.
   `crs -print-user-config` prints exactly that as JSON, for clients without a
   TOML parser. The server returns a token, which the client stores.

Registering again with the same token replaces the user's workflows and
settings, so a client can resend its config file at any time; a user can also
change them over the connection with `UpdateMyWorkflows`. Either way the
dashboard follows on the next workflow cycle.

A user's workflows are validated as the server's own config is. On top of
that a user may keep at most 50, `DesktopNotifications` is dropped (the popup
would appear on the server's machine), and a workflow without its own
`GithubUsername` is about the registered login. One that names no `Repos`
falls back to the user's `Repos`, and to the server's when the user has none.

## Users

A user is a GitHub login, registered by claim: the server takes the login a
client sends. The token it hands back is the user's from then on — only its
SHA-256 is stored — and registering that login again needs it.

| Config key | Default | What it does |
|------------|---------|--------------|
| `UserActiveHours` | `24` | A user not seen for this long has their workflows skipped by the cycle. Their dashboard stays as it was and refreshes on the first cycle after they come back. `0` never skips anyone |
| `UserRetentionDays` | `7` | A user not seen for this long is deleted, with their workflows, drafts and feedback. `0` never deletes anyone |
| `AllowServerIdentityActions` | `false` | Lets remote users who have not linked a GitHub account submit reviews under the server's account, and administrators merge under it (see [Writing to GitHub](#writing-to-github)) |

Any authenticated call counts as being seen (written at most once a minute per
connection). A deleted user's client gets `not authenticated:` on its next call
and registers again with the token it holds, which the server accepts for a
login it no longer knows — so retention costs an idle user their drafts and a
cycle's wait for their dashboard, not their token. (An administrator's
`DeleteUser` does revoke the token.) Retention runs in the serving process
only: a `crs -oneoff` or workflow-only run against the same database deletes
nobody, since it could not revoke the GitHub tokens of the users it deleted.

Administrators — the `CRS_ADMIN_TOKEN` connection, and users registered with
it as `AdminToken` — can read and edit the server's config (`GetConfig`,
`UpdateConfig`), rerun plugins (`RerunPlugins`), bypass the cache
(`SkipCache`), and list and delete users (`ListUsers`, `DeleteUser`). Nobody
else can; the full method policy is in the [protocol](protocol.md#method-policy).

## Writing to GitHub

Everything the server reads from GitHub goes through `CRS_GITHUB_TOKEN`, for
every user. A write is different: GitHub records a review, a reply or a merge
as the token's user, whoever pressed the button. So from a remote connection,
`SubmitReview` (with the replies posted alongside it) and `MergePR`:

1. go out as the user's own GitHub account when they have
   [linked it](#linking-your-github-account) — GitHub then applies that
   account's permissions, branch protections and merge rights;
2. otherwise, with `AllowServerIdentityActions = true`, go out under the
   server's account — right for an organization whose token belongs to a bot
   account, where everyone knows a review from it came through the server.
   Merging under it also takes an administrator;
3. otherwise are refused: with `refused: link-github: link your GitHub account
   to submit reviews as yourself` where the server can link accounts, so the
   client offers to, or with `refused: this server posts to GitHub as its own
   account, ...` where it cannot.

A refused write leaves the pending comments where they were. The
administrator token's connection has no account to link, so only the second
and third rules apply to it. The local stdio connection is the token owner's
own, and is never refused.

### Linking your GitHub account

A user links their GitHub account once, from their client
([web or Emacs](clients.md#linking-your-github-account)), through GitHub's
[device flow](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow):
the client shows a code such as `WDJB-MJHT`, the user enters it at
<https://github.com/login/device> while signed in to GitHub, and authorizes
the app. Nothing has to reach the server from GitHub, so it works on an
internal network. Linking is optional: a user registers and browses without
it.

GitHub has to say the authorization belongs to the user's registered login
(compared case-insensitively) — someone registered as `alice` cannot link
`bob`'s account — or nothing is stored. Registration is by claim, but every
write that goes out as a user is one GitHub vouched for.

#### Setting it up

1. Create an app on GitHub, under the organization's (or your own) **Settings
   → Developer settings**, either:
    - an **OAuth App**, with **Enable Device Flow** ticked. The homepage and
      callback URLs are not used by the device flow; any URL will do. It asks
      users for the scopes in `GitHubOAuthScopes`, `repo` by default, which
      reviewing, replying and merging in private repositories need
      (`public_repo` covers public ones only). Its tokens do not expire. An
      organization that restricts OAuth App access has to approve the app.
    - a **GitHub App**, with **Enable Device Flow** ticked and no webhook. Give
      it the repository permissions **Pull requests: Read and write** (reviews
      and replies) and **Contents: Read and write** (merging), and install it
      on the organization or the repositories your team reviews: it acts for a
      user only where it is installed and the user has access. It ignores
      scopes. Its user tokens expire after eight hours by default, with a
      refresh token, so set the client secret below or users link again
      every eight hours.
2. Set the app's client ID in the server's config file, and the scopes if
   `repo` is not right for an OAuth App:

    ```toml
    GitHubOAuthClientID = "Iv1.0123456789abcdef"
    # GitHubOAuthScopes = "repo"
    ```

3. Set the secrets in the server's environment:

    | Variable | |
    |----------|--|
    | `CRS_SECRET_KEY` | Required. 32 random bytes, as 64 hex digits or base64: `openssl rand -hex 32`. Every stored token is encrypted with it. Without a valid one linking is unavailable — the server says so at startup — and no token is ever stored unencrypted. Back it up with the server's other secrets: if it changes, the stored links can no longer be read and users have to link again |
    | `CRS_GITHUB_OAUTH_CLIENT_SECRET` | Optional, recommended. The app's client secret. With it the server refreshes a GitHub App's expiring tokens before they lapse, and revokes a token on GitHub when its user unlinks it or is deleted. Without it linking still works, but an expired GitHub App token ends the link, and a removed token is only forgotten, not revoked |

`crs --listen` logs at startup whether linking is available, and what is
missing if it is not. Clients see it as `github_link_available`.

#### What is stored, and when it goes away

- **The token** — with a GitHub App's refresh token and their expiry times,
  the GitHub login, and when it was linked — is stored on the user's row,
  encrypted with AES-256-GCM under `CRS_SECRET_KEY` and bound to that row's
  login, so it opens for nobody else's. It is never sent to a client, never
  logged, and `ListUsers` shows only whether a user is linked and to which
  login. A copy of the database alone writes to GitHub as nobody.
- **Only for whoever started it**: a link is stored under the bearer token of
  the connection that started it, so one that completes after its user was
  deleted — or deleted and registered again by someone else — is discarded
  and revoked.
- **Unlinking** (`UnlinkGitHub`) deletes it, and revokes the token on GitHub
  given the client secret.
- **Revoked on github.com**, or a GitHub App token **expired** beyond
  refreshing: the next write gets a 401 from GitHub. The server deletes the
  link and refuses the write with `refused: link-github: your GitHub
  authorization has expired or was revoked; link your account again`. The
  pending comments stay; link again and submit again.
- **Deleting a user**, by `DeleteUser` or by retention, deletes their link
  with them and revokes the token given the client secret, and cancels a link
  they have in progress.
- **A link in progress** lives in the server's memory only, so a restart drops
  it and the user starts again. A user has at most one in progress, and the
  server at most 64.
- **Changing `CRS_SECRET_KEY`** makes every stored link unreadable: writes are
  refused with `refused: link-github:` until each user links again, which
  replaces the old link. **Losing it** — unset or invalid after a restart —
  makes linking unavailable: a linked user's writes are refused (never sent
  as the server's account instead), and work again once the key is back.
- **Removing `GitHubOAuthClientID`** stops new links. Existing OAuth App links
  keep working as long as `CRS_SECRET_KEY` is unchanged, but nothing can
  refresh a GitHub App's expiring token any more, so each of those lapses
  when it expires. A write whose link stopped working while the server cannot
  link accounts is refused with a plain `refused:`, since a client offering
  to link again would find linking unavailable.

The RPCs (`StartGitHubLink`, `GetGitHubLink`, `UnlinkGitHub`) and the
statuses a client sees are in the [protocol](protocol.md#linking-a-github-account).

## What is shared and what is per user

| Per user | Shared by everyone |
|----------|--------------------|
| Workflows, and the dashboard they fill (sections and PRs) | The GitHub token, and its rate-limit budget |
| `SectionPriority`, `SectionSorting` and fallback `Repos` | The PR caches: metadata, diffs, comments, reviews, CI |
| Pending comments (drafts) and review feedback | Plugin results and AI feature results, which are per PR |
| Being an administrator | Downloaded images, repository clones and worktrees |
| A linked GitHub account, which their reviews and merges go out as | The server's config: plugins, AI features, `SleepDuration`, `JiraDomain` |

Everything the server computes "for me" is computed for the token's account,
not the viewer: a team chip's `mine` and `personal`, a reaction's
`viewer_reacted`. A user's workflows are the exception: their filters and
searches compare against the user's own login. The token can list only its
own account's teams, so a user's review-request searches cover the requests
made of them by name, not those made of their teams.

## Limitations

These are known, and accepted for a server on an internal network:

- **Logins are claimed, not proven.** Anyone with the registration key can
  register a login nobody holds yet, and see that login's dashboard. The key
  keeps strangers out; an administrator can `DeleteUser` a squatter, which
  revokes the squatter's token so their client cannot register the login
  again on its own (registering afresh by hand still takes the key). Writes
  are another matter: a squatter cannot link the real account, since GitHub
  has to vouch for it.
- **Users can point workflows at any repository the server's token can
  read**, and see those PRs on their dashboard.
- **No request size limit** on the TCP connection: a client can send a request
  as large as it likes. Keep the port off the open internet.
- **Drafts live only on the server** and are deleted with their user by
  retention.
- **Local paths are the server's.** A PR's `repo_path` and `worktree_path`
  are paths on the server's machine, so a remote client's language-server and
  file features do not work against them. Images are the exception for the
  web client, which asks `GetImage` for an image's bytes (`IncludeData`)
  rather than reading its `path`; the Emacs client still reads the `path`, so
  on another machine it shows only the images `shr` can fetch itself — a
  public repository's, not a private one's.
- **"Mine" is the token's account** (see above), so team chips highlight the
  server's login rather than the viewer's.
