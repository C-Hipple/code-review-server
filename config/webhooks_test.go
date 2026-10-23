package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseConfigReadsWebhooks(t *testing.T) {
	cfg, err := parseConfig([]byte(`
[Webhooks]
ListenAddress = "127.0.0.1:8790"
Path = "/hooks/github"
Secret = "s3cret"
Repos = ["acme/widgets", "acme-labs/*"]
FullRefreshInterval = 120
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !cfg.WebhooksConfigured() {
		t.Error("expected webhooks to be configured")
	}
	if got := cfg.WebhookPath(); got != "/hooks/github" {
		t.Errorf("WebhookPath() = %q", got)
	}
	if got := cfg.WebhookSecret(); got != "s3cret" {
		t.Errorf("WebhookSecret() = %q", got)
	}
	if got := cfg.WebhookFullRefreshInterval(); got != 2*time.Hour {
		t.Errorf("WebhookFullRefreshInterval() = %v", got)
	}
	if got := cfg.WebhookRepos(); !slices.Equal(got, []string{"acme/widgets", "acme-labs/*"}) {
		t.Errorf("WebhookRepos() = %v", got)
	}
}

func TestWebhooksOffWithoutTheTable(t *testing.T) {
	t.Setenv(WebhookSecretEnv, "from-env")
	cfg, err := parseConfig([]byte(`Repos = ["acme/widgets"]`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	// A secret on its own turns nothing on: there's nowhere to listen.
	if cfg.WebhooksConfigured() {
		t.Error("webhooks configured without a ListenAddress")
	}
	if got := cfg.WebhookPath(); got != DefaultWebhookPath {
		t.Errorf("default WebhookPath() = %q", got)
	}
	if got := cfg.WebhookFullRefreshInterval(); got != DefaultWebhookFullRefresh {
		t.Errorf("default WebhookFullRefreshInterval() = %v", got)
	}
}

func TestWebhookSecretFallsBackToTheEnvironment(t *testing.T) {
	cfg := Config{Webhooks: WebhookSettings{ListenAddress: ":8790"}}

	t.Setenv(WebhookSecretEnv, "")
	if cfg.WebhooksConfigured() {
		t.Error("webhooks configured with no secret anywhere")
	}

	t.Setenv(WebhookSecretEnv, "from-env")
	if got := cfg.WebhookSecret(); got != "from-env" {
		t.Errorf("WebhookSecret() = %q, want the environment's", got)
	}
	if !cfg.WebhooksConfigured() {
		t.Error("expected the environment's secret to be enough")
	}

	cfg.Webhooks.Secret = "from-file"
	if got := cfg.WebhookSecret(); got != "from-file" {
		t.Errorf("WebhookSecret() = %q, want the file's to win", got)
	}
}

func TestWebhookReposDefaultsToTheConfiguredRepositories(t *testing.T) {
	cfg := Config{
		Repos: []string{"acme/widgets", "acme/gadgets"},
		RawWorkflows: []RawWorkflow{
			{Name: "a", Repos: []string{"acme/widgets", "other/thing"}},
			{Name: "b", Repo: "acme/single"},                     // SingleRepoSyncReviewRequestsWorkflow
			{Name: "c", Owner: "jira-org", Repo: "project-repo"}, // ProjectListWorkflow's old form
			{Name: "d"}, // a search workflow names no repository
		},
	}
	want := []string{"acme/widgets", "acme/gadgets", "other/thing", "acme/single", "jira-org/project-repo"}
	if got := cfg.WebhookRepos(); !slices.Equal(got, want) {
		t.Errorf("WebhookRepos() = %v, want %v", got, want)
	}

	cfg.Webhooks.Repos = []string{"acme/*"}
	if got := cfg.WebhookRepos(); !slices.Equal(got, []string{"acme/*"}) {
		t.Errorf("explicit Repos ignored: %v", got)
	}
}

func TestValidateWebhooks(t *testing.T) {
	t.Setenv(WebhookSecretEnv, "")
	tests := []struct {
		name        string
		webhooks    WebhookSettings
		wantField   string
		wantMessage string // substring; empty means no problem expected
	}{
		{
			name:     "complete",
			webhooks: WebhookSettings{ListenAddress: ":8790", Secret: "s", Repos: []string{"acme/widgets", "acme/*"}},
		},
		{
			name:     "absent",
			webhooks: WebhookSettings{},
		},
		{
			name:        "no secret",
			webhooks:    WebhookSettings{ListenAddress: ":8790"},
			wantField:   "Webhooks.Secret",
			wantMessage: WebhookSecretEnv,
		},
		{
			name:        "address without a port",
			webhooks:    WebhookSettings{ListenAddress: "localhost", Secret: "s"},
			wantField:   "Webhooks.ListenAddress",
			wantMessage: "host:port",
		},
		{
			name:        "relative path",
			webhooks:    WebhookSettings{ListenAddress: ":8790", Secret: "s", Path: "github"},
			wantField:   "Webhooks.Path",
			wantMessage: "start with",
		},
		{
			name:        "malformed repo",
			webhooks:    WebhookSettings{ListenAddress: ":8790", Secret: "s", Repos: []string{"widgets"}},
			wantField:   "Webhooks.Repos",
			wantMessage: "owner/*",
		},
		{
			name:        "wildcard owner",
			webhooks:    WebhookSettings{ListenAddress: ":8790", Secret: "s", Repos: []string{"*/widgets"}},
			wantField:   "Webhooks.Repos",
			wantMessage: "owner/repo",
		},
		{
			name:        "negative refresh interval",
			webhooks:    WebhookSettings{ListenAddress: ":8790", Secret: "s", FullRefreshInterval: -5},
			wantField:   "Webhooks.FullRefreshInterval",
			wantMessage: "positive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Webhooks = tt.webhooks
			problems := Validate(cfg)
			if tt.wantField == "" {
				if len(problems) != 0 {
					t.Fatalf("expected no problems, got %v", problems)
				}
				return
			}
			for _, p := range problems {
				if p.Field == tt.wantField && strings.Contains(p.Message, tt.wantMessage) {
					return
				}
			}
			t.Fatalf("expected a %s problem mentioning %q, got %v", tt.wantField, tt.wantMessage, problems)
		})
	}
}
