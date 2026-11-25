package config

import (
	"crs/database"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"sync"
)

// This struct implements all possible values a workflow can define, then they're written as-needed.
//
// The toml tags only matter when the server writes the config back out (see
// Update.Render): omitempty keeps a workflow's unused fields out of the file
// so a round-trip through the config RPCs doesn't litter it with empty
// strings and false booleans.
type RawWorkflow struct {
	WorkflowType   string   `toml:"WorkflowType"`
	Name           string   `toml:"Name"`
	Owner          string   `toml:"Owner,omitempty"`
	Repo           string   `toml:"Repo,omitempty"`
	Repos          []string `toml:"Repos,omitempty"`
	JiraEpic       string   `toml:"JiraEpic,omitempty"`
	Filters        []string `toml:"Filters,omitempty"`
	SectionTitle   string   `toml:"SectionTitle"`
	PRState        string   `toml:"PRState,omitempty"`
	GithubUsername string   `toml:"GithubUsername,omitempty"`
	IncludeDiff    bool     `toml:"IncludeDiff,omitempty"`
	Teams          []string `toml:"Teams,omitempty"` // Teams to filter PRs by when using FilterTeamRequested
	// DesktopNotifications overrides the global DesktopNotifications setting
	// for this workflow only. If nil, the global setting is used.
	DesktopNotifications *bool `toml:"DesktopNotifications,omitempty"`
	// Forge is the ID of the forge this workflow's PRs come from (see
	// forges.go); empty means the config's DefaultForge.
	Forge string `toml:"Forge,omitempty"`
	// AlwaysRunPlugins names plugins that run on every PR in this workflow's
	// section once per head SHA, as though someone had asked for each by
	// name: with --call-type explicit. A deferred (OnlyOnDemand) one runs there
	// anyway; one that runs on its own gets that run in place of its automatic
	// one. See SectionPlugins.
	AlwaysRunPlugins []string `toml:"AlwaysRunPlugins,omitempty"`
}

// RepoConfig holds per-repository configuration settings.
type RepoConfig struct {
	ReleaseCheckCommand string
}

// Plugin defines the configuration for an installed plugin.
//
// As on RawWorkflow, the toml tags only matter when the server writes the
// config back out: omitempty keeps unset options out of the file.
type Plugin struct {
	Name            string `toml:"Name"`
	Command         string `toml:"Command"`
	IncludeDiff     bool   `toml:"IncludeDiff,omitempty"`
	IncludeHeaders  bool   `toml:"IncludeHeaders,omitempty"`
	IncludeComments bool   `toml:"IncludeComments,omitempty"`
	IncludeBranch   bool   `toml:"IncludeBranch,omitempty"`
	OnlyOnDemand    bool   `toml:"OnlyOnDemand,omitempty"`
	// Provider and Model say which LLM backend the plugin should call:
	// PluginProviderGemini or PluginProviderOpenRouter, and for OpenRouter the
	// model to ask for. The server hands them to the plugin in the
	// CRS_LLM_PROVIDER and CRS_LLM_MODEL environment variables; the bundled
	// plugins honor them, and a plugin that calls no model ignores them. Empty
	// leaves the variables to whatever the server's own environment says.
	Provider string `toml:"Provider,omitempty"`
	Model    string `toml:"Model,omitempty"`
}

// SectionPlugins maps each section title to the plugins that always run on
// every PR in it: the AlwaysRunPlugins of each workflow writing into it, in
// file order without repeats. Sections none of them name are left out, so an
// empty map means no section always runs a plugin.
func (c Config) SectionPlugins() map[string][]string {
	sections := map[string][]string{}
	for _, wf := range c.RawWorkflows {
		for _, name := range wf.AlwaysRunPlugins {
			if !slices.Contains(sections[wf.SectionTitle], name) {
				sections[wf.SectionTitle] = append(sections[wf.SectionTitle], name)
			}
		}
	}
	return sections
}

// LLM backends a [[Plugins]] entry can name. A plugin is itself a command, so
// the AI features' command provider has no counterpart here.
const (
	PluginProviderGemini     = AIProviderGemini
	PluginProviderOpenRouter = AIProviderOpenRouter
)

// Environment variables a plugin's Provider and Model reach it through.
const (
	PluginProviderEnv = "CRS_LLM_PROVIDER"
	PluginModelEnv    = "CRS_LLM_MODEL"
)

// Env is what the server adds to the plugin's environment: its Provider and
// Model, each only when set.
func (p Plugin) Env() []string {
	var env []string
	if p.Provider != "" {
		env = append(env, PluginProviderEnv+"="+p.Provider)
	}
	if p.Model != "" {
		env = append(env, PluginModelEnv+"="+p.Model)
	}
	return env
}

// AI providers and execution modes a [[AIFeatures]] entry can name. They live
// here, beside the validation that checks them, because config cannot import
// the ai package (ai reads config).
const (
	AIProviderGemini     = "gemini"
	AIProviderOpenRouter = "openrouter"
	AIProviderCommand    = "command"

	// AIModeOneShot answers from a single model call.
	AIModeOneShot = "oneshot"
	// AIModeAgent runs a multi-turn loop in which the model may call tools.
	AIModeAgent = "agent"
)

// AISettings is the [AI] table: defaults every [[AIFeatures]] entry inherits.
// It enables nothing on its own — each feature is off until its entry says
// Enabled = true.
type AISettings struct {
	// DefaultProvider is the provider a feature uses when its entry sets
	// neither Provider nor Command: "gemini" (the Gemini API, which needs
	// GEMINI_API_KEY), "openrouter" (any model OpenRouter serves, which needs
	// OPENROUTER_API_KEY and a model) or "command" (a program on this machine).
	// Empty picks "command" when DefaultCommand is set and "gemini" otherwise.
	// The whole order is on AIProviderFor.
	DefaultProvider string `toml:"DefaultProvider,omitempty"`
	// DefaultCommand is the command line the command provider runs, e.g.
	// "claude -p". It is split into words like a shell would (quotes work, pipes
	// and variables do not), receives the prompt on stdin, and must print its
	// answer on stdout.
	DefaultCommand string `toml:"DefaultCommand,omitempty"`
	// DefaultModel is the model the openrouter provider asks for, as OpenRouter
	// names it, e.g. "anthropic/claude-sonnet-4.5". Only that provider reads it:
	// Gemini stays on gemini-flash-latest, and a command picks its own model.
	DefaultModel string `toml:"DefaultModel,omitempty"`
}

// AIFeature is one [[AIFeatures]] entry: it switches a registered AI feature on
// and says how it runs.
type AIFeature struct {
	// ID names the feature, e.g. "comments-addressed".
	ID      string `toml:"ID"`
	Enabled bool   `toml:"Enabled"`
	// Mode is AIModeOneShot or AIModeAgent; empty uses the feature's default.
	Mode string `toml:"Mode,omitempty"`
	// Automatic also runs the feature after a PR is fetched or updated, the way
	// plugins run, instead of only when a client asks for it. It has no effect
	// unless Enabled is set too.
	Automatic bool `toml:"Automatic,omitempty"`
	// Provider, Command and Model override the [AI] defaults for this feature.
	// A Command on its own also picks the command provider, over any [AI]
	// DefaultProvider; a Model picks nothing. See AIProviderFor.
	Provider string `toml:"Provider,omitempty"`
	Command  string `toml:"Command,omitempty"`
	Model    string `toml:"Model,omitempty"`
}

// AIProviderChoice is how a feature reaches a model, as AIProviderFor resolves
// it from config.
type AIProviderChoice struct {
	// Provider is AIProviderGemini, AIProviderOpenRouter or AIProviderCommand.
	Provider string
	// Command is the command line the command provider runs; only it reads it.
	Command string
	// Model is the model the openrouter provider asks for; only it reads it.
	Model string
}

// AIProviderFor resolves the provider a feature runs with, and the command
// and model that provider reads. The first of these that is set picks the
// provider:
//
//  1. the feature's Provider
//  2. the feature's Command, which picks "command"
//  3. [AI] DefaultProvider
//  4. [AI] DefaultCommand, which picks "command"
//  5. otherwise "gemini"
//
// That is, the feature's own settings beat the [AI] defaults, and at each
// level a named provider beats the one a command implies. No API key plays a
// part in the choice: GEMINI_API_KEY and OPENROUTER_API_KEY are read only once
// their provider has been picked. The command is the feature's Command, else
// [AI] DefaultCommand, and the model the feature's Model, else [AI]
// DefaultModel.
func (c Config) AIProviderFor(f AIFeature) AIProviderChoice {
	choice := AIProviderChoice{Command: f.Command, Model: f.Model}
	if choice.Command == "" {
		choice.Command = c.AI.DefaultCommand
	}
	if choice.Model == "" {
		choice.Model = c.AI.DefaultModel
	}
	switch {
	case f.Provider != "":
		choice.Provider = f.Provider
	case f.Command != "":
		choice.Provider = AIProviderCommand
	case c.AI.DefaultProvider != "":
		choice.Provider = c.AI.DefaultProvider
	case c.AI.DefaultCommand != "":
		choice.Provider = AIProviderCommand
	default:
		choice.Provider = AIProviderGemini
	}
	return choice
}

// IDs of the AI features the legacy root-level keys configure. config cannot
// import the ai package for them (ai reads config), so they are spelled out
// here; ai's tests check them against its registry.
const (
	legacyFileOrderingID = "file-ordering"
	legacyReviewEaseID   = "review-ease"
)

// legacyLLMKeys are the root-level keys that configured the diff file ordering
// and the review-ease rating before those were AI features. A config that
// still sets them keeps working: for either feature without an [[AIFeatures]]
// entry of its own, a flag that is on stands for an entry that enables it
// automatically on Provider (Gemini unless it names another) with Model — how
// the flags always ran, apart from any [AI] defaults.
type legacyLLMKeys struct {
	FileOrdering bool   // ExperimentalLLMFileOrdering
	ReviewEase   bool   // ExperimentalLLMReviewEase
	Provider     string // ExperimentalLLMProvider: "gemini" or "openrouter"
	Model        string // ExperimentalLLMModel: the model the openrouter provider asks for
}

// entry is the [[AIFeatures]] entry for the feature id that a legacy flag
// stands for, and false when no flag that is on stands for one.
func (k legacyLLMKeys) entry(id string) (AIFeature, bool) {
	on := (id == legacyFileOrderingID && k.FileOrdering) || (id == legacyReviewEaseID && k.ReviewEase)
	if !on {
		return AIFeature{}, false
	}
	provider := k.Provider
	if provider == "" {
		provider = AIProviderGemini
	}
	return AIFeature{ID: id, Enabled: true, Automatic: true, Provider: provider, Model: k.Model}, true
}

// AIFeatureSettings returns the [[AIFeatures]] entry for id, and false when
// the config has none — which leaves the feature disabled. For file-ordering
// and review-ease, a legacy flag that is on stands in for a missing entry
// (legacyLLMKeys), so code asking whether a feature is enabled goes through
// here rather than reading AIFeatures.
func (c Config) AIFeatureSettings(id string) (AIFeature, bool) {
	if f, ok := c.fileEntry(id); ok {
		return f, true
	}
	return c.legacy.entry(id)
}

// LegacyAIFeature returns the root-level key that switches the feature id on
// in the config file, and the [[AIFeatures]] entry it stands for, whether or
// not the file also has an entry of its own (which would win). ok is false
// when no legacy flag that is on stands for id.
func (c Config) LegacyAIFeature(id string) (key string, entry AIFeature, ok bool) {
	entry, ok = c.legacy.entry(id)
	if !ok {
		return "", AIFeature{}, false
	}
	key = "ExperimentalLLMFileOrdering"
	if id == legacyReviewEaseID {
		key = "ExperimentalLLMReviewEase"
	}
	return key, entry, true
}

// AutomaticAIFeatures returns the IDs of the AI features config enables to run
// automatically: the [[AIFeatures]] entries that say so, in file order, then
// the features a legacy flag stands in for.
func (c Config) AutomaticAIFeatures() []string {
	var ids []string
	for _, f := range c.AIFeatures {
		if f.Enabled && f.Automatic {
			ids = append(ids, f.ID)
		}
	}
	for _, id := range []string{legacyFileOrderingID, legacyReviewEaseID} {
		if _, own := c.fileEntry(id); own {
			continue
		}
		if f, ok := c.legacy.entry(id); ok && f.Enabled && f.Automatic {
			ids = append(ids, id)
		}
	}
	return ids
}

// fileEntry is the [[AIFeatures]] entry the config file has for id.
func (c Config) fileEntry(id string) (AIFeature, bool) {
	for _, f := range c.AIFeatures {
		if f.ID == id {
			return f, true
		}
	}
	return AIFeature{}, false
}

// The APIs a [GitHubAPI] key can send a lookup to. They live here, beside the
// validation that checks them, because config cannot import git_tools
// (git_tools reads config).
const (
	GitHubAPIREST    = "rest"
	GitHubAPIGraphQL = "graphql"
)

// GitHubAPISettings is the [GitHubAPI] table: which of GitHub's two APIs
// answers each lookup that both of them can answer. A key left unset means
// REST. The server revalidates a REST reply rather than refetching it, so
// asking again about data that hasn't changed costs nothing against the rate
// limit; a GraphQL query is charged every time, but against a budget of its
// own, and some lookups answer more completely there — each key says how.
// git_tools.RouteFor is what reads it.
type GitHubAPISettings struct {
	// ReviewRequestHistory is every team and user ever asked to review a PR,
	// behind the required-team chips on the review list. REST reads it from
	// the PR's issue events, GraphQL from its timeline; both answer fully.
	ReviewRequestHistory string `toml:"ReviewRequestHistory,omitempty"`
	// Reactions is who reacted to each comment and review. REST spends a
	// request per comment somebody reacted to, and has no endpoint for
	// reactions on a review's own body; GraphQL answers both in one query.
	Reactions string `toml:"Reactions,omitempty"`
	// Mergeability is whether each PR on the review list merges cleanly. REST
	// asks about one PR per request, GraphQL about fifty.
	Mergeability string `toml:"Mergeability,omitempty"`
}

// Routes maps each [GitHubAPI] key to the API it names, "" for a key left
// unset. Validation and git_tools.RouteFor both read the keys from here, so a
// new lookup is added in one place.
func (s GitHubAPISettings) Routes() map[string]string {
	return map[string]string{
		"ReviewRequestHistory": s.ReviewRequestHistory,
		"Reactions":            s.Reactions,
		"Mergeability":         s.Mergeability,
	}
}

// ChatSettings is the [Chat] table: which agent the review chat talks to (see
// the chat package), and how each agent is run. The agent is the config's
// choice, not a client's: every new chat talks to Agent. Each agent has a
// table of its own — the built-in Claude Code one [Chat.Claude] and
// Antigravity one [Chat.Antigravity], any other [Chat.Agents.<id>] — so a new
// one adds a table rather than reshaping the old ones. Nothing needs setting:
// Claude Code is offered as is when `claude` is on PATH, Antigravity when
// `agy` is.
type ChatSettings struct {
	// Agent is the ID of the agent new chats talk to: ChatBackendClaude,
	// ChatBackendAntigravity or a key of Agents. Empty means
	// ChatBackendClaude. Chats started with another agent carry on with it
	// while it is still defined.
	Agent string `toml:"Agent,omitempty"`
	// Claude is [Chat.Claude]: the local Claude Code CLI, run headless.
	Claude ChatBackendSettings `toml:"Claude,omitempty"`
	// Antigravity is [Chat.Antigravity]: Google's Antigravity CLI, `agy acp`.
	Antigravity ChatBackendSettings `toml:"Antigravity,omitempty"`
	// Agents are further agents, [Chat.Agents.<id>], each run over the
	// protocol its Protocol names: any agent that speaks the Agent Client
	// Protocol (ACP) — Gemini CLI, claude-code-acp, opencode, goose, ... — by
	// default.
	Agents map[string]ChatBackendSettings `toml:"Agents,omitempty"`
}

// ChatBackendSettings configures one chat agent.
type ChatBackendSettings struct {
	// Disabled stops the server offering the agent at all.
	Disabled bool `toml:"Disabled,omitempty"`
	// Name is what clients call the agent; empty takes the protocol's name
	// ("Claude Code", "Antigravity"), or for a plain ACP agent its ID.
	Name string `toml:"Name,omitempty"`
	// Protocol is how the server talks to the agent: ChatProtocolACP (the
	// default for [Chat.Agents]), ChatProtocolAntigravity or
	// ChatProtocolClaudeCode. [Chat.Claude] is always ChatProtocolClaudeCode
	// and [Chat.Antigravity] always ChatProtocolAntigravity.
	Protocol string `toml:"Protocol,omitempty"`
	// Command is the program to run, a name on PATH or a path, with any
	// arguments (split like a shell would): for an ACP agent, the command line
	// that starts it speaking ACP on stdio ("gemini --experimental-acp").
	// Empty uses the protocol's own program: `claude` for Claude Code, `agy
	// acp` for Antigravity; a plain ACP agent has none.
	Command string `toml:"Command,omitempty"`
	// Env adds environment variables to the agent's, over the server's own.
	Env map[string]string `toml:"Env,omitempty"`
	// Models are the models offered to pick from, as the agent takes them
	// (Claude Code's --model, an ACP agent's model IDs); empty offers the
	// protocol's built-in list — Claude Code's aliases, nothing for ACP. The
	// agent's own default is always offered first.
	Models []string `toml:"Models,omitempty"`
	// DefaultModel is the model picked when a chat starts without naming one;
	// empty leaves it to the agent.
	DefaultModel string `toml:"DefaultModel,omitempty"`
	// Mode is an ACP agent's session mode to switch each conversation to
	// ("plan", say); empty leaves the agent's default, except for Antigravity,
	// whose conversations are switched to its "default" mode, the one that
	// asks before it acts.
	Mode string `toml:"Mode,omitempty"`
	// AuthMethod is the ACP authentication method (one the agent lists in its
	// authMethods) to sign in with when the agent says it needs signing in;
	// empty reports that instead.
	AuthMethod string `toml:"AuthMethod,omitempty"`
}

// The protocols a chat agent can be run over. They live here, beside the
// settings that name them, because config cannot import the chat package
// (chat reads config); the chat package registers a driver for each.
const (
	// ChatProtocolClaudeCode runs the Claude Code CLI headless, one `claude
	// -p` per turn.
	ChatProtocolClaudeCode = "claude-code"
	// ChatProtocolACP runs any agent that speaks the Agent Client Protocol
	// (agentclientprotocol.com) over stdio.
	ChatProtocolACP = "acp"
	// ChatProtocolAntigravity runs Google's Antigravity CLI, `agy acp`: ACP,
	// with what Antigravity does its own way taken care of.
	ChatProtocolAntigravity = "antigravity"
)

// ChatProtocols are the protocols a [Chat.Agents] table may name.
var ChatProtocols = []string{ChatProtocolACP, ChatProtocolAntigravity, ChatProtocolClaudeCode}

// chatBuiltins are the agents the config defines without a [Chat.Agents]
// table, in the order they are listed, each with the protocol it always
// speaks.
var chatBuiltins = []struct{ id, protocol, table string }{
	{ChatBackendClaude, ChatProtocolClaudeCode, "[Chat.Claude]"},
	{ChatBackendAntigravity, ChatProtocolAntigravity, "[Chat.Antigravity]"},
}

// IsBuiltinChatAgent reports whether id is one of the agents the config
// defines without a [Chat.Agents] table.
func IsBuiltinChatAgent(id string) bool {
	for _, b := range chatBuiltins {
		if b.id == id {
			return true
		}
	}
	return false
}

// ChatBackend returns the settings of the chat agent id: [Chat.Claude] or
// [Chat.Antigravity] for the built-in agents, else its [Chat.Agents] table,
// with Protocol filled in. An unknown id gets the zero settings.
func (c Config) ChatBackend(id string) ChatBackendSettings {
	switch id {
	case ChatBackendClaude:
		s := c.Chat.Claude
		s.Protocol = ChatProtocolClaudeCode
		return s
	case ChatBackendAntigravity:
		s := c.Chat.Antigravity
		s.Protocol = ChatProtocolAntigravity
		return s
	}
	s, ok := c.Chat.Agents[id]
	if !ok {
		return ChatBackendSettings{}
	}
	if strings.TrimSpace(s.Protocol) == "" {
		s.Protocol = ChatProtocolACP
	}
	s.Protocol = strings.TrimSpace(s.Protocol)
	return s
}

// ChatAgent is the ID of the agent new chats talk to: [Chat] Agent, else
// ChatBackendClaude.
func (c Config) ChatAgent() string {
	if id := strings.TrimSpace(c.Chat.Agent); id != "" {
		return id
	}
	return ChatBackendClaude
}

// ChatAgentIDs are the IDs of every chat agent the config defines: the
// built-in Claude Code and Antigravity ones, then the [Chat.Agents] tables by
// ID. A [Chat.Agents] table with a built-in agent's ID is passed over.
func (c Config) ChatAgentIDs() []string {
	var ids []string
	for _, b := range chatBuiltins {
		ids = append(ids, b.id)
	}
	for _, id := range slices.Sorted(maps.Keys(c.Chat.Agents)) {
		if !IsBuiltinChatAgent(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// ChatTable names the table that configures the chat agent id, for messages:
// "[Chat.Claude]", "[Chat.Antigravity]" or "[Chat.Agents.<id>]".
func ChatTable(id string) string {
	for _, b := range chatBuiltins {
		if b.id == id {
			return b.table
		}
	}
	return "[Chat.Agents." + tomlKey(id) + "]"
}

// tomlKey is key as a TOML table header spells it: bare when it can be,
// quoted otherwise.
func tomlKey(key string) string {
	bare := key != ""
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			bare = false
			break
		}
	}
	if bare {
		return key
	}
	return strconv.Quote(key)
}

// The IDs of the built-in chat agents. They live here, beside the settings
// that name them, because config cannot import the chat package (chat reads
// config).
const (
	// ChatBackendClaude is the built-in Claude Code agent, [Chat.Claude].
	ChatBackendClaude = "claude"
	// ChatBackendAntigravity is the built-in Antigravity agent,
	// [Chat.Antigravity].
	ChatBackendAntigravity = "antigravity"
)

// WebhookSettings is the [Webhooks] table: GitHub webhook deliveries the server
// accepts in place of re-fetching every PR it lists on every cycle. With it set
// up, a cycle lists PRs and fully fetches the ones it is adding (or whose head
// moved), while reviews, comments, CI and the rest arrive by webhook. The
// deliveries come either over HTTP, to ListenAddress, or — with PubSub set —
// from a Google Cloud Pub/Sub subscription some other service publishes them
// to. Without either — or without a secret to verify HTTP deliveries with —
// nothing receives them and every PR is polled, exactly as if the table
// weren't there.
type WebhookSettings struct {
	// ListenAddress is where the server listens for deliveries, as host:port —
	// ":8790" on every interface, "127.0.0.1:8790" behind a tunnel or proxy.
	ListenAddress string `toml:"ListenAddress,omitempty"`
	// Path is the URL path GitHub posts to; empty means DefaultWebhookPath.
	Path string `toml:"Path,omitempty"`
	// Secret is the secret the GitHub webhook was created with. Leave it out
	// to keep it out of the file and export WebhookSecretEnv instead.
	Secret string `toml:"Secret,omitempty"`
	// Repos are the repositories whose webhooks deliver here: "owner/repo", or
	// "owner/*" for an organization webhook. Only PRs in these are left to
	// webhooks; any other PR is still polled. Empty means the repositories the
	// config already lists (see WebhookRepos).
	Repos []string `toml:"Repos,omitempty"`
	// FullRefreshInterval is how many minutes may pass between cycles that
	// refresh every PR as if webhooks were off, to catch up on deliveries that
	// never arrived. Zero means DefaultWebhookFullRefresh.
	FullRefreshInterval int `toml:"FullRefreshInterval,omitempty"`
	// PubSub, the [Webhooks.PubSub] table, takes deliveries from a Pub/Sub
	// subscription in place of listening for them.
	PubSub PubSubSettings `toml:"PubSub,omitempty"`
}

// PubSubSettings is the [Webhooks.PubSub] table: for an organization that
// receives GitHub's webhooks with a service of its own and publishes each
// delivery to a Google Cloud Pub/Sub topic, the subscription the server pulls
// them from. Each message's data is a delivery's JSON body as GitHub sent it,
// and its attributes name the event and the delivery. The publisher has
// already verified GitHub's signature; who may publish is up to the topic's
// IAM policy, so the server takes what the subscription hands it.
type PubSubSettings struct {
	// Subscription is the subscription to pull from, as
	// "projects/<project>/subscriptions/<subscription>". Setting it is what
	// turns Pub/Sub on.
	Subscription string `toml:"Subscription,omitempty"`
	// CredentialsFile is a Google credentials JSON file — a service account
	// key, or what `gcloud auth application-default login` writes — to pull
	// with. Empty means Application Default Credentials.
	CredentialsFile string `toml:"CredentialsFile,omitempty"`
	// EventTypeAttribute is the message attribute holding the GitHub event
	// type, what GitHub sends as X-GitHub-Event; empty means
	// DefaultPubSubEventTypeAttribute.
	EventTypeAttribute string `toml:"EventTypeAttribute,omitempty"`
	// DeliveryIDAttribute is the message attribute holding the delivery's ID,
	// what GitHub sends as X-GitHub-Delivery; empty means
	// DefaultPubSubDeliveryIDAttribute.
	DeliveryIDAttribute string `toml:"DeliveryIDAttribute,omitempty"`
}

const (
	// DefaultPubSubEventTypeAttribute is the attribute naming a message's
	// GitHub event when [Webhooks.PubSub] doesn't set EventTypeAttribute.
	DefaultPubSubEventTypeAttribute = "event_type"
	// DefaultPubSubDeliveryIDAttribute is the attribute holding a message's
	// delivery ID when [Webhooks.PubSub] doesn't set DeliveryIDAttribute.
	DefaultPubSubDeliveryIDAttribute = "delivery_id"
)

// PubSubConfigured reports whether deliveries come from a Pub/Sub
// subscription rather than over HTTP.
func (c Config) PubSubConfigured() bool {
	return strings.TrimSpace(c.Webhooks.PubSub.Subscription) != ""
}

// PubSubSubscription is the subscription deliveries are pulled from, or ""
// when they aren't.
func (c Config) PubSubSubscription() string {
	return strings.TrimSpace(c.Webhooks.PubSub.Subscription)
}

// PubSubCredentialsFile is the credentials file to pull with, with a leading
// "~" expanded, or "" for Application Default Credentials.
func (c Config) PubSubCredentialsFile() string {
	path := strings.TrimSpace(c.Webhooks.PubSub.CredentialsFile)
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := UserHomeDir(); err == nil {
			path = home + path[1:]
		}
	}
	return path
}

// PubSubEventTypeAttribute is the attribute a message's GitHub event type is
// read from.
func (c Config) PubSubEventTypeAttribute() string {
	if a := strings.TrimSpace(c.Webhooks.PubSub.EventTypeAttribute); a != "" {
		return a
	}
	return DefaultPubSubEventTypeAttribute
}

// PubSubDeliveryIDAttribute is the attribute a message's delivery ID is read
// from.
func (c Config) PubSubDeliveryIDAttribute() string {
	if a := strings.TrimSpace(c.Webhooks.PubSub.DeliveryIDAttribute); a != "" {
		return a
	}
	return DefaultPubSubDeliveryIDAttribute
}

const (
	// DefaultWebhookPath is the URL path deliveries are posted to when
	// [Webhooks] doesn't set Path.
	DefaultWebhookPath = "/github/webhook"
	// WebhookSecretEnv supplies the webhook secret when [Webhooks] leaves
	// Secret out.
	WebhookSecretEnv = "CRS_WEBHOOK_SECRET"
	// DefaultWebhookFullRefresh is how often a webhook-driven server still
	// refreshes every PR when [Webhooks] doesn't set FullRefreshInterval.
	DefaultWebhookFullRefresh = 6 * time.Hour
)

// WebhookSecret is the secret deliveries are verified with: [Webhooks] Secret,
// or the WebhookSecretEnv environment variable when the file leaves it out.
func (c Config) WebhookSecret() string {
	if s := strings.TrimSpace(c.Webhooks.Secret); s != "" {
		return s
	}
	return strings.TrimSpace(os.Getenv(WebhookSecretEnv))
}

// WebhooksConfigured reports whether the config asks for webhooks and gives
// what they need to work: a Pub/Sub subscription to pull them from, or an
// address to listen on and a secret. Unsigned HTTP deliveries are never
// accepted, so without a secret a listening server polls.
func (c Config) WebhooksConfigured() bool {
	if c.PubSubConfigured() {
		return true
	}
	return strings.TrimSpace(c.Webhooks.ListenAddress) != "" && c.WebhookSecret() != ""
}

// WebhookPath is the URL path GitHub posts deliveries to.
func (c Config) WebhookPath() string {
	if p := strings.TrimSpace(c.Webhooks.Path); p != "" {
		return p
	}
	return DefaultWebhookPath
}

// WebhookFullRefreshInterval is how long a webhook-driven server goes between
// cycles that refresh every PR regardless.
func (c Config) WebhookFullRefreshInterval() time.Duration {
	if c.Webhooks.FullRefreshInterval > 0 {
		return time.Duration(c.Webhooks.FullRefreshInterval) * time.Minute
	}
	return DefaultWebhookFullRefresh
}

// WebhookRepos is the repositories whose PRs are left to webhooks, as
// "owner/repo" or "owner/*": [Webhooks] Repos when set, otherwise every
// repository the config names — the root Repos and each workflow's own. The
// search-driven workflows name none, so a config made only of them covers
// nothing until Repos is set.
func (c Config) WebhookRepos() []string {
	var entries []string
	if len(c.Webhooks.Repos) > 0 {
		entries = c.Webhooks.Repos
	} else {
		entries = append(entries, c.Repos...)
		for _, wf := range c.RawWorkflows {
			entries = append(entries, wf.Repos...)
			switch {
			case strings.Contains(wf.Repo, "/"):
				entries = append(entries, wf.Repo)
			case wf.Owner != "" && wf.Repo != "":
				entries = append(entries, wf.Owner+"/"+wf.Repo)
			}
		}
	}
	seen := map[string]bool{}
	var repos []string
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" || seen[strings.ToLower(entry)] {
			continue
		}
		seen[strings.ToLower(entry)] = true
		repos = append(repos, entry)
	}
	return repos
}

// Define your classes
type Config struct {
	Repos                []string // List of repositories in "owner/repo" format. Workflows can override this.
	RawWorkflows         []RawWorkflow
	SleepDuration        time.Duration
	JiraDomain           string
	GithubUsername       string
	RepoLocation         string
	AutoWorktree         bool
	DesktopNotifications bool              // Send desktop notifications when a PR is added to a section
	SectionPriority      map[string]int    // Map of section title to priority (lower is better)
	SectionSorting       map[string]string // Map of section title to sorting method (e.g. "newest_first", "oldest_first")
	Plugins              []Plugin
	RepoConfigs          map[string]RepoConfig // Keyed by "owner/repo"
	// AI holds the defaults for the AI features, and AIFeatures switches them
	// on one by one. Both are absent from the built-in defaults, so the AI layer
	// does nothing until a config file enables a feature. AIFeatures holds only
	// the entries the file has: ask AIFeatureSettings whether a feature is on.
	AI         AISettings
	AIFeatures []AIFeature
	// GitHubAPI picks REST or GraphQL for the lookups both can answer.
	GitHubAPI GitHubAPISettings
	// Webhooks lets GitHub push PR updates instead of every cycle polling for
	// them; empty, it leaves polling as it was.
	Webhooks WebhookSettings
	// Chat picks the agent the review chat talks to and configures how each
	// agent is run; empty, the chat talks to Claude Code when it is installed.
	Chat ChatSettings
	// DefaultForge is the ID of the forge workflows that name none talk to,
	// and Forges configures each forge by ID (see forges.go). Both empty, every
	// workflow talks to GitHub, as before forges could be named.
	DefaultForge string
	Forges       map[string]ForgeSettings
	// legacy holds the root-level keys file ordering and review ease were
	// configured by before they were AI features; see legacyLLMKeys.
	legacy legacyLLMKeys
	// UsingDefaults is true when no config file exists and the server is
	// running DefaultConfigTOML. Nothing behaves differently because of it; it
	// exists so clients can say so.
	UsingDefaults bool
	DB            *database.DB
}

// LoginResolver looks up the GitHub login the configured API token belongs to,
// so a config that never names a user still has an identity for the filters
// that compare PRs against "me". main wires this to
// git_tools.GetAuthenticatedLogin; it is a variable because config cannot
// import git_tools (git_tools reads config). A nil resolver, or one that
// returns "", simply leaves GithubUsername empty — the behavior before this
// existed.
var LoginResolver func() string

var (
	c  Config
	mu sync.RWMutex
)

// C returns a copy of the current configuration.
func C() Config {
	mu.RLock()
	defer mu.RUnlock()
	return c
}

// SetC updates the current configuration. Primarily for tests.
func SetC(newCfg Config) {
	mu.Lock()
	defer mu.Unlock()
	c = newCfg
}

// GetReleaseCheckCommand returns the release check command for a given repo (owner/repo format).
func (c Config) GetReleaseCheckCommand(ownerRepo string) string {
	if rc, ok := c.RepoConfigs[ownerRepo]; ok {
		return rc.ReleaseCheckCommand
	}
	return ""
}

var UserHomeDir = os.UserHomeDir

func getCRSHome() (string, error) {
	return GetCRSHome()
}

// GetCRSHome returns the CRS home directory (respects CRS_HOME env override).
func GetCRSHome() (string, error) {
	if crsHome := os.Getenv("CRS_HOME"); crsHome != "" {
		return crsHome, nil
	}
	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".crs"), nil
}

func getXDGConfigHome() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg, nil
	}
	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// ParseConfigForTest is an exported wrapper around parseConfig for use in tests.
func ParseConfigForTest(data []byte) (*Config, error) {
	return parseConfig(data)
}

// parseConfig parses the configuration from bytes and returns a Config struct.
// It does NOT initialize the database.
func parseConfig(data []byte) (*Config, error) {
	var intermediate_config struct {
		Repos                []string
		JiraDomain           string
		SleepDuration        int64
		Workflows            []RawWorkflow
		GithubUsername       string
		RepoLocation         string
		AutoWorktree         bool
		DesktopNotifications bool
		SectionPriority      map[string]int
		SectionSorting       map[string]string
		Plugins              []Plugin
		RepoConfigs          map[string]RepoConfig
		AI                   AISettings
		AIFeatures           []AIFeature
		GitHubAPI            GitHubAPISettings
		Webhooks             WebhookSettings
		Chat                 ChatSettings
		DefaultForge         string
		Forges               map[string]ForgeSettings
		// The legacy keys; see legacyLLMKeys.
		LegacyFileOrdering bool   `toml:"ExperimentalLLMFileOrdering"`
		LegacyReviewEase   bool   `toml:"ExperimentalLLMReviewEase"`
		LegacyProvider     string `toml:"ExperimentalLLMProvider"`
		LegacyModel        string `toml:"ExperimentalLLMModel"`
	}

	err := toml.Unmarshal(data, &intermediate_config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	pluginNames := make(map[string]bool)
	for _, p := range intermediate_config.Plugins {
		if pluginNames[p.Name] {
			return nil, fmt.Errorf("duplicate plugin name found: %s", p.Name)
		}
		pluginNames[p.Name] = true
	}

	// Same rule as plugins: two entries for one feature would leave it
	// ambiguous which settings apply.
	aiFeatureIDs := make(map[string]bool)
	for _, f := range intermediate_config.AIFeatures {
		if aiFeatureIDs[f.ID] {
			return nil, fmt.Errorf("duplicate AI feature ID found: %s", f.ID)
		}
		aiFeatureIDs[f.ID] = true
	}

	// Fill in who "me" is from the API token when the config doesn't say. This
	// runs before the per-workflow copy below, so a resolved login reaches the
	// identity filters the same way a configured one does. Nothing is written
	// back to disk: the file stays as the user wrote it.
	if intermediate_config.GithubUsername == "" && LoginResolver != nil {
		if login := strings.TrimSpace(LoginResolver()); login != "" {
			slog.Debug("Using the API token's user as GithubUsername", "login", login)
			intermediate_config.GithubUsername = login
		}
	}

	for i := range intermediate_config.Workflows {
		if intermediate_config.Workflows[i].GithubUsername == "" {
			intermediate_config.Workflows[i].GithubUsername = intermediate_config.GithubUsername
		}
	}

	repoLocation := intermediate_config.RepoLocation
	if repoLocation == "" {
		repoLocation = "~/"
	}

	parsed_sleep_duration := time.Duration(10) * time.Minute
	if intermediate_config.SleepDuration != 0 {
		parsed_sleep_duration = time.Duration(intermediate_config.SleepDuration) * time.Minute
	}

	repoConfigs := intermediate_config.RepoConfigs
	if repoConfigs == nil {
		repoConfigs = make(map[string]RepoConfig)
	}

	return &Config{
		Repos:                intermediate_config.Repos,
		RawWorkflows:         intermediate_config.Workflows,
		SleepDuration:        parsed_sleep_duration,
		JiraDomain:           intermediate_config.JiraDomain,
		GithubUsername:       intermediate_config.GithubUsername,
		RepoLocation:         repoLocation,
		AutoWorktree:         intermediate_config.AutoWorktree,
		DesktopNotifications: intermediate_config.DesktopNotifications,
		SectionPriority:      intermediate_config.SectionPriority,
		SectionSorting:       intermediate_config.SectionSorting,
		Plugins:              intermediate_config.Plugins,
		RepoConfigs:          repoConfigs,
		AI:                   intermediate_config.AI,
		AIFeatures:           intermediate_config.AIFeatures,
		GitHubAPI:            intermediate_config.GitHubAPI,
		Webhooks:             intermediate_config.Webhooks,
		Chat:                 intermediate_config.Chat,
		DefaultForge:         intermediate_config.DefaultForge,
		Forges:               intermediate_config.Forges,
		legacy: legacyLLMKeys{
			FileOrdering: intermediate_config.LegacyFileOrdering,
			ReviewEase:   intermediate_config.LegacyReviewEase,
			Provider:     intermediate_config.LegacyProvider,
			Model:        intermediate_config.LegacyModel,
		},
	}, nil
}

// Initialize loads the configuration from the config file and initializes the database.
// This should be called from main() to allow proper error handling.
func loadConfig() (*Config, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	the_bytes, err := os.ReadFile(configPath)
	if err != nil {
		// A missing config file is a first run, not a failure: the built-in
		// defaults need nothing but a token, so the server comes up and starts
		// filling a dashboard instead of exiting with a message about a file
		// the user has never heard of.
		if errors.Is(err, os.ErrNotExist) {
			slog.Warn("No configuration file found; running with the built-in defaults",
				"path", configPath,
				"sections", "Waiting On Me, Review Requested",
				"customize", "codereviewserver -print-default-config > "+configPath)
			return DefaultConfig()
		}
		return nil, fmt.Errorf("failed to read config file at %s: %w", configPath, err)
	}

	return parseConfig(the_bytes)
}

// Reload reloads the configuration from the config file.
// It updates the global c struct but maintains the existing DB connection.
func Reload() error {
	newCfg, err := loadConfig()
	if err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()
	// Persist the database connection
	newCfg.DB = c.DB
	c = *newCfg
	slog.Info("Configuration reloaded successfully")
	return nil
}

// Initialize loads the configuration from the config file and initializes the database.
// This should be called from main() to allow proper error handling.
func Initialize() error {
	config, err := loadConfig()
	if err != nil {
		return err
	}

	// Initialize database
	crsHome, err := getCRSHome()
	if err != nil {
		return fmt.Errorf("failed to get CRS home: %w", err)
	}
	dbPath := filepath.Join(crsHome, "codereviewserver.db")

	// Attempt to migrate legacy database if it exists
	homeDir, err := UserHomeDir()
	if err == nil {
		legacyPaths := []string{
			filepath.Join(homeDir, ".config/codereviewserver.db"),
			filepath.Join(homeDir, ".config/codereviewserver/codereviewserver.db"),
		}

		for _, legacyDBPath := range legacyPaths {
			if _, err := os.Stat(legacyDBPath); err == nil {
				// Legacy DB exists
				if _, err := os.Stat(dbPath); os.IsNotExist(err) {
					// New DB does not exist, migrate
					slog.Info("Migrating database to new location", "old", legacyDBPath, "new", dbPath)
					if err := os.MkdirAll(crsHome, 0755); err != nil {
						slog.Error("Failed to create new CRS directory", "error", err)
					} else {
						if err := os.Rename(legacyDBPath, dbPath); err != nil {
							slog.Warn("Failed to move legacy database, falling back to legacy path", "error", err)
							dbPath = legacyDBPath
						}
					}
					break // Only migrate the first found legacy DB
				}
			}
		}
	}

	if _, err := os.Stat(dbPath); err == nil {
		slog.Info("Found database file", "path", dbPath)
	} else {
		slog.Info("Setting up database file", "path", dbPath)
	}
	db, err := database.NewDB(dbPath)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	slog.Info("Database initialized successfully")

	config.DB = db
	mu.Lock()
	defer mu.Unlock()
	c = *config
	return nil
}
