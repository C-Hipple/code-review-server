package git_tools

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/google/go-github/v74/github"
)

const (
	// authenticatedLoginTTL caches the token→login lookup. The answer only
	// changes when the token does, so this is really just a guard against
	// asking again on every config reload.
	authenticatedLoginTTL = 1 * time.Hour

	// authenticatedLoginErrTTL caches a failed lookup briefly, so a bad or
	// rate-limited token doesn't produce a request per config reload while
	// still recovering within a cycle or two.
	authenticatedLoginErrTTL = 2 * time.Minute

	myTeamsTTL = 30 * time.Minute
)

// HasToken reports whether a GitHub token is configured. GetGithubClient exits
// the process without one, so anything that runs before the server has proven
// it can talk to GitHub — config loading, in particular — checks this first.
func HasToken() bool {
	return os.Getenv("CRS_GITHUB_TOKEN") != ""
}

// GetAuthenticatedLogin returns the GitHub login that CRS_GITHUB_TOKEN belongs
// to, so a user doesn't have to tell the config who they are. Returns "" when
// there is no token or the lookup fails; callers treat that as "identity
// unknown" rather than an error, because every one of them has a configured
// GithubUsername to fall back on.
func GetAuthenticatedLogin() string {
	if !HasToken() {
		slog.Warn("No CRS_GITHUB_TOKEN set; cannot look up which GitHub user this server is running as")
		return ""
	}

	const cacheKey = "authenticated_login"
	if val, found := GlobalCache.Get(cacheKey); found {
		return val.(string)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// An empty user asks for the user the token authenticates as.
	user, _, err := GetGithubClient().Users.Get(ctx, "")
	if err != nil {
		slog.Error("Failed to look up the authenticated GitHub user; identity filters need GithubUsername in the config", "error", err)
		GlobalCache.Set(cacheKey, "", authenticatedLoginErrTTL)
		return ""
	}

	login := user.GetLogin()
	if login == "" {
		slog.Error("GitHub returned no login for the authenticated user")
		GlobalCache.Set(cacheKey, "", authenticatedLoginErrTTL)
		return ""
	}
	slog.Info("Resolved GitHub identity from the API token", "login", login)
	GlobalCache.Set(cacheKey, login, authenticatedLoginTTL)
	return login
}

// GetMyTeamRefs returns every team the authenticated user belongs to as
// "org/team-slug" — the form GitHub's team-review-requested: search qualifier
// takes. GetMyTeams returns bare slugs instead, which is what the Teams config
// field matches against.
func GetMyTeamRefs() []string {
	if !HasToken() {
		return nil
	}

	const cacheKey = "my_team_refs"
	if val, found := GlobalCache.Get(cacheKey); found {
		return val.([]string)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	teams, err := ListAllPages("user/teams", func(opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
		return GetGithubClient().Teams.ListUserTeams(ctx, opts)
	})
	if err != nil {
		slog.Error("Failed to list the teams you belong to; team review requests will be missed", "error", err)
		return nil
	}

	refs := []string{}
	for _, team := range teams {
		if team == nil || team.GetSlug() == "" || team.GetOrganization() == nil {
			continue
		}
		org := team.GetOrganization().GetLogin()
		if org == "" {
			continue
		}
		refs = append(refs, org+"/"+team.GetSlug())
	}
	GlobalCache.Set(cacheKey, refs, myTeamsTTL)
	return refs
}
