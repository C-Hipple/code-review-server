package config

// DefaultConfigTOML is the configuration the server runs when no config file
// exists. It is a real TOML document, parsed through the same path as a file on
// disk, so what runs by default is exactly what a user gets by writing this out
// and editing it — `codereviewserver -print-default-config` does just that.
//
// Neither default workflow names a repository or a user: both find their PRs
// through GitHub search, and the login they search for is taken from the API
// token when GithubUsername is unset. That makes a valid CRS_GITHUB_TOKEN the
// only setup step.
const DefaultConfigTOML = `# code-review-server configuration.
#
# This is the built-in default, used when this file does not exist. Both
# workflows find their pull requests through GitHub search instead of a
# repository list, and identify you from CRS_GITHUB_TOKEN, so there is nothing
# here you must fill in. See https://github.com/C-Hipple/code-review-server
# (docs/configuration.md) for everything you can add: Repos, more workflows,
# filters, plugins, and section ordering.

# Minutes between background syncs.
SleepDuration = 10

# Lower numbers sort first in the client.
[SectionPriority]
"Waiting On Me" = 1
"Review Requested" = 2

# PRs that need something from you right now.
[[Workflows]]
WorkflowType = "WaitingOnMeWorkflow"
Name = "waiting-on-me"
SectionTitle = "Waiting On Me"

# Everything you or your teams have been asked to review, whether or not it is
# your turn yet — the same list as github.com's "Review requests".
[[Workflows]]
WorkflowType = "MyReviewRequestsWorkflow"
Name = "review-requested"
SectionTitle = "Review Requested"
`

// DefaultConfig parses DefaultConfigTOML. The returned Config is flagged with
// UsingDefaults so clients can tell that nothing was configured.
func DefaultConfig() (*Config, error) {
	cfg, err := parseConfig([]byte(DefaultConfigTOML))
	if err != nil {
		return nil, err
	}
	cfg.UsingDefaults = true
	return cfg, nil
}
