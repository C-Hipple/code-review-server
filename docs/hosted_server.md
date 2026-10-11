# Hosted Server

One `crs` process can keep review dashboards for a whole team. Each person
runs a client — the web UI's bridge or Emacs — that connects to the shared
server instead of starting a server of its own. The server polls GitHub once
for everybody, with one token, and keeps each person's dashboard, drafts and
feedback apart.

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

Both come from the environment, never the config file, and the server never
logs them.

| Variable | What it does |
|----------|--------------|
| `CRS_REGISTRATION_KEY` | The key a client presents to register a user. Unset, registration is disabled: users already registered can still connect, nobody new can. The server warns at startup |
| `CRS_ADMIN_TOKEN` | Authenticates a connection as the server's own identity: its dashboard is the config file's workflows, and it is an administrator. A user who registers with it as `AdminToken` becomes an administrator too. Unset, only users registered as administrators are |

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
| `AllowServerIdentityActions` | `false` | Lets remote users submit reviews (see [Writing to GitHub](#writing-to-github)) |

Any authenticated call counts as being seen (written at most once a minute per
connection). A deleted user's client gets `not authenticated:` on its next call
and registers again with the token it holds, which the server accepts for a
login it no longer knows — so retention costs an idle user their drafts and a
cycle's wait for their dashboard, not their token.

Administrators — the `CRS_ADMIN_TOKEN` connection, and users registered with
it as `AdminToken` — can read and edit the server's config (`GetConfig`,
`UpdateConfig`), rerun plugins (`RerunPlugins`), bypass the cache
(`SkipCache`), and list and delete users (`ListUsers`, `DeleteUser`). Nobody
else can; the full method policy is in the [protocol](protocol.md#method-policy).

## Writing to GitHub

Everything the server reads from GitHub goes through `CRS_GITHUB_TOKEN`, for
every user. A write is different: GitHub records a review, a reply or a merge
as the token's user, whoever pressed the button. So from a remote connection:

- With `AllowServerIdentityActions = false` (the default), `SubmitReview` and
  `MergePR` are refused with `refused: this server posts to GitHub as its own
  account, ...`. Pending comments are left where they were.
- With `AllowServerIdentityActions = true`, users submit reviews under the
  server's account — right for an organization whose token belongs to a bot
  account, where everyone knows a review from it came through the server.
  Merging under it also takes an administrator.

The local stdio connection is the token owner's own, and is never refused.

### Linking your GitHub account

<!-- GitHub account linking (OAuth device flow) is documented here once it lands. -->

Not available yet: until it is, a remote user can only write to GitHub under
the server's own account, as above.

## What is shared and what is per user

| Per user | Shared by everyone |
|----------|--------------------|
| Workflows, and the dashboard they fill (sections and PRs) | The GitHub token, and its rate-limit budget |
| `SectionPriority`, `SectionSorting` and fallback `Repos` | The PR caches: metadata, diffs, comments, reviews, CI |
| Pending comments (drafts) and review feedback | Plugin results and AI feature results, which are per PR |
| Being an administrator | Downloaded images, repository clones and worktrees |
| | The server's config: plugins, AI features, `SleepDuration`, `JiraDomain` |

Everything the server computes "for me" is computed for the token's account,
not the viewer: a team chip's `mine` and `personal`, a reaction's
`viewer_reacted`. A user's workflows are the exception: their filters and
searches compare against the user's own login.

## Limitations

These are known, and accepted for a server on an internal network:

- **Logins are claimed, not proven.** Anyone with the registration key can
  register a login nobody holds yet. The key keeps strangers out; an
  administrator can `DeleteUser` a squatter.
- **Users can point workflows at any repository the server's token can
  read**, and see those PRs on their dashboard.
- **No request size limit** on the TCP connection: a client can send a request
  as large as it likes. Keep the port off the open internet.
- **Drafts live only on the server** and are deleted with their user by
  retention.
- **Local paths are the server's.** A PR's `repo_path` and `worktree_path`,
  and an image's `path`, are paths on the server's machine, so a remote
  client's language-server and file features do not work against them.
- **"Mine" is the token's account** (see above), so team chips highlight the
  server's login rather than the viewer's.
