package config

import (
	"os"
	"path/filepath"
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

func TestParseConfigReadsPubSub(t *testing.T) {
	t.Setenv(WebhookSecretEnv, "")
	cfg, err := parseConfig([]byte(`
[Webhooks]
Repos = ["acme/*"]

[Webhooks.PubSub]
Subscription = "projects/acme-ci/subscriptions/crs-github-events"
CredentialsFile = "~/keys/crs.json"
EventTypeAttribute = "X-GitHub-Event"
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	// Pub/Sub needs no secret and no address: the publisher verified GitHub's
	// signature, and nothing listens.
	if !cfg.WebhooksConfigured() || !cfg.PubSubConfigured() {
		t.Error("expected Pub/Sub to configure webhooks on its own")
	}
	if got := cfg.PubSubSubscription(); got != "projects/acme-ci/subscriptions/crs-github-events" {
		t.Errorf("PubSubSubscription() = %q", got)
	}
	oldUserHomeDir := UserHomeDir
	t.Cleanup(func() { UserHomeDir = oldUserHomeDir })
	UserHomeDir = func() (string, error) { return "/home/me", nil }
	if got := cfg.PubSubCredentialsFile(); got != "/home/me/keys/crs.json" {
		t.Errorf("PubSubCredentialsFile() = %q, want ~ expanded", got)
	}
	if got := cfg.PubSubEventTypeAttribute(); got != "X-GitHub-Event" {
		t.Errorf("PubSubEventTypeAttribute() = %q", got)
	}
	if got := cfg.PubSubDeliveryIDAttribute(); got != DefaultPubSubDeliveryIDAttribute {
		t.Errorf("default PubSubDeliveryIDAttribute() = %q", got)
	}
}

func TestPubSubOffWithoutASubscription(t *testing.T) {
	cfg := Config{Webhooks: WebhookSettings{PubSub: PubSubSettings{CredentialsFile: "/tmp/key.json"}}}
	if cfg.PubSubConfigured() || cfg.WebhooksConfigured() {
		t.Error("Pub/Sub configured without a subscription")
	}
	if got := cfg.PubSubEventTypeAttribute(); got != DefaultPubSubEventTypeAttribute {
		t.Errorf("default PubSubEventTypeAttribute() = %q", got)
	}
}

func TestValidatePubSub(t *testing.T) {
	t.Setenv(WebhookSecretEnv, "")
	key := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(key, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const sub = "projects/acme-ci/subscriptions/crs-github-events"
	tests := []struct {
		name        string
		pubsub      PubSubSettings
		listen      string
		wantField   string
		wantMessage string // substring; empty means no problem expected
	}{
		{name: "subscription alone", pubsub: PubSubSettings{Subscription: sub}},
		{name: "with credentials and attributes", pubsub: PubSubSettings{Subscription: sub, CredentialsFile: key, EventTypeAttribute: "type", DeliveryIDAttribute: "id"}},
		{
			name:        "short subscription name",
			pubsub:      PubSubSettings{Subscription: "crs-github-events"},
			wantField:   "Webhooks.PubSub.Subscription",
			wantMessage: "projects/<project>/subscriptions/<subscription>",
		},
		{
			name:        "topic instead of subscription",
			pubsub:      PubSubSettings{Subscription: "projects/acme-ci/topics/github-events"},
			wantField:   "Webhooks.PubSub.Subscription",
			wantMessage: "projects/<project>/subscriptions/<subscription>",
		},
		{
			name:        "settings without a subscription",
			pubsub:      PubSubSettings{CredentialsFile: key},
			wantField:   "Webhooks.PubSub.Subscription",
			wantMessage: "is required",
		},
		// The subscriber reports a missing file when it reads it; a key
		// missing on one machine mustn't block saving the config there.
		{name: "credentials file not there", pubsub: PubSubSettings{Subscription: sub, CredentialsFile: filepath.Join(t.TempDir(), "absent.json")}},
		{name: "credentials under home", pubsub: PubSubSettings{Subscription: sub, CredentialsFile: "~/.config/crs/key.json"}},
		{
			name:        "relative credentials path",
			pubsub:      PubSubSettings{Subscription: sub, CredentialsFile: "keys/crs.json"},
			wantField:   "Webhooks.PubSub.CredentialsFile",
			wantMessage: "absolute path",
		},
		{
			name:        "listening as well",
			pubsub:      PubSubSettings{Subscription: sub},
			listen:      ":8790",
			wantField:   "Webhooks.ListenAddress",
			wantMessage: "one or the other",
		},
	}
	// Pulling needs no webhook secret, and an address beside a subscription
	// is reported once, as one to remove, not also as missing its secret.
	cfg := validConfig()
	cfg.Webhooks = WebhookSettings{ListenAddress: "localhost", PubSub: PubSubSettings{Subscription: sub}}
	if problems := Validate(cfg); len(problems) != 1 || problems[0].Field != "Webhooks.ListenAddress" || !strings.Contains(problems[0].Message, "one or the other") {
		t.Errorf("problems = %v, want only the one saying to remove ListenAddress", problems)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Webhooks = WebhookSettings{ListenAddress: tt.listen, PubSub: tt.pubsub}
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

// The config RPCs don't model [Webhooks]; saving through them keeps it,
// nested [Webhooks.PubSub] table and all.
func TestUpdateRenderKeepsWebhooksAndPubSub(t *testing.T) {
	useTempConfig(t, sampleConfig+`
[Webhooks]
Repos = ["acme/*"]
FullRefreshInterval = 60

[Webhooks.PubSub]
Subscription = "projects/acme-ci/subscriptions/crs-github-events"
EventTypeAttribute = "type"
`)
	sleep := 7
	_, cfg, err := Update{SleepDuration: &sleep}.Render()
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := WebhookSettings{
		Repos:               []string{"acme/*"},
		FullRefreshInterval: 60,
		PubSub:              PubSubSettings{Subscription: "projects/acme-ci/subscriptions/crs-github-events", EventTypeAttribute: "type"},
	}
	if !slices.Equal(cfg.Webhooks.Repos, want.Repos) || cfg.Webhooks.FullRefreshInterval != want.FullRefreshInterval || cfg.Webhooks.PubSub != want.PubSub {
		t.Errorf("Webhooks = %+v, want %+v", cfg.Webhooks, want)
	}
}
