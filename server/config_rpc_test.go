package server

import (
	"crs/ai"
	"crs/config"
	"crs/workflows"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const testConfigTOML = `
Repos = ["owner/repo"]
SleepDuration = 5

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "My Open PRs"
Filters = ["FilterNotDraft"]
SectionTitle = "My PRs"
`

// useTempConfig points the config package at a throwaway config file and
// returns its path.
func useTempConfig(t *testing.T, contents string) string {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	oldUserHomeDir := config.UserHomeDir
	t.Cleanup(func() { config.UserHomeDir = oldUserHomeDir })
	config.UserHomeDir = func() (string, error) { return tempDir, nil }

	path := filepath.Join(tempDir, "codereviewserver.toml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	// Load it as the running config, the way a client's GetConfig call would,
	// so tests don't inherit whatever a previous test left in the global.
	if err := config.Reload(); err != nil {
		t.Fatalf("failed to load test config: %v", err)
	}
	return path
}

func TestGetConfigReturnsConfigAndRegistries(t *testing.T) {
	path := useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	reply := &GetConfigReply{}
	if err := h.GetConfig(&GetConfigArgs{}, reply); err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}

	if !reply.Okay {
		t.Errorf("expected okay, got message %q", reply.Message)
	}
	if reply.Path != path {
		t.Errorf("expected config path %q, got %q", path, reply.Path)
	}
	if len(reply.Config.Workflows) != 1 || reply.Config.Workflows[0].Name != "My Open PRs" {
		t.Errorf("unexpected workflows in reply: %+v", reply.Config.Workflows)
	}
	if reply.Config.SleepDuration != 5 {
		t.Errorf("expected SleepDuration in minutes, got %d", reply.Config.SleepDuration)
	}
	if len(reply.WorkflowTypes) == 0 || len(reply.Filters) == 0 {
		t.Error("expected the workflow type and filter registries to be included for client pickers")
	}
}

func TestUpdateConfigSavesValidWorkflows(t *testing.T) {
	path := useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	newWorkflows := []config.RawWorkflow{
		{
			WorkflowType: "SyncReviewRequestsWorkflow",
			Name:         "  My Open PRs  ", // whitespace should be trimmed away
			SectionTitle: "My PRs",
			Filters:      []string{"FilterNotDraft"},
		},
		{
			WorkflowType: "SyncReviewRequestsWorkflow",
			Name:         "Team Reviews",
			SectionTitle: "Needs Review",
			Filters:      []string{"FilterByLabel:bug"},
			Teams:        []string{"my-team"},
		},
	}
	reply := &UpdateConfigReply{}
	if err := h.UpdateConfig(&UpdateConfigArgs{Workflows: &newWorkflows}, reply); err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}

	if !reply.Okay {
		t.Fatalf("expected the update to be accepted, got %v", reply.Errors)
	}
	if len(reply.Config.Workflows) != 2 {
		t.Fatalf("expected 2 workflows in the reply, got %+v", reply.Config.Workflows)
	}
	if reply.Config.Workflows[0].Name != "My Open PRs" {
		t.Errorf("expected the workflow name to be trimmed, got %q", reply.Config.Workflows[0].Name)
	}
	if config.C().RawWorkflows[1].Name != "Team Reviews" {
		t.Errorf("expected the running config to be reloaded, got %+v", config.C().RawWorkflows)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written config: %v", err)
	}
	if !strings.Contains(string(written), "Team Reviews") {
		t.Errorf("expected the new workflow on disk, got:\n%s", written)
	}
	if !strings.Contains(string(written), `Repos = ['owner/repo']`) {
		t.Errorf("expected untouched settings to survive, got:\n%s", written)
	}
}

// A project spanning several repos is configured as one workflow with a Repos
// list, so the whole list has to survive the client round-trip: through
// UpdateConfig, onto disk, and back out of the reloaded config.
func TestUpdateConfigSavesMultiRepoProjectList(t *testing.T) {
	path := useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	newWorkflows := []config.RawWorkflow{{
		WorkflowType: "ProjectListWorkflow",
		Name:         "Project - Multi",
		SectionTitle: "Multi Repo Project",
		JiraEpic:     "BOARD-123",
		Repos:        []string{" C-Hipple/code-review-server ", "", "C-Hipple/diff-lsp"},
	}}
	reply := &UpdateConfigReply{}
	if err := h.UpdateConfig(&UpdateConfigArgs{Workflows: &newWorkflows}, reply); err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}

	if !reply.Okay {
		t.Fatalf("expected a multi-repo ProjectListWorkflow to be accepted, got %v", reply.Errors)
	}
	wantRepos := []string{"C-Hipple/code-review-server", "C-Hipple/diff-lsp"}
	if got := reply.Config.Workflows[0].Repos; !reflect.DeepEqual(got, wantRepos) {
		t.Errorf("expected blank entries dropped and the rest trimmed, got %v", got)
	}
	if got := config.C().RawWorkflows[0].Repos; !reflect.DeepEqual(got, wantRepos) {
		t.Errorf("expected both repos in the reloaded config, got %v", got)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written config: %v", err)
	}
	for _, repo := range wantRepos {
		if !strings.Contains(string(written), repo) {
			t.Errorf("expected %q on disk, got:\n%s", repo, written)
		}
	}

	// And the saved config must still build into a workflow carrying every repo.
	built := workflows.MatchWorkflows(config.C().RawWorkflows, &[]string{}, "https://example.atlassian.net")
	if len(built) != 1 {
		t.Fatalf("expected the saved workflow to build, got %d", len(built))
	}
	wf, ok := built[0].(workflows.ProjectListWorkflow)
	if !ok {
		t.Fatalf("expected a ProjectListWorkflow, got %T", built[0])
	}
	if !reflect.DeepEqual(wf.Repos, wantRepos) {
		t.Errorf("built workflow Repos = %v, want %v", wf.Repos, wantRepos)
	}
}

// Clients build their workflow editor from the registry this handler serves, so
// ProjectListWorkflow has to advertise Repos for the field to be offered at all.
func TestGetConfigAdvertisesReposForProjectList(t *testing.T) {
	useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	reply := &GetConfigReply{}
	if err := h.GetConfig(&GetConfigArgs{}, reply); err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}

	for _, wt := range reply.WorkflowTypes {
		if wt.Name != "ProjectListWorkflow" {
			continue
		}
		if !slices.Contains(wt.OptionalFields, "Repos") {
			t.Errorf("expected ProjectListWorkflow to offer Repos, got %v", wt.OptionalFields)
		}
		return
	}
	t.Error("ProjectListWorkflow missing from the workflow type registry")
}

func TestUpdateConfigRejectsInvalidWorkflows(t *testing.T) {
	path := useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	bad := []config.RawWorkflow{{
		WorkflowType: "NotAWorkflow",
		Name:         "Broken",
		SectionTitle: "",
		Filters:      []string{"FilterByAuthor"},
	}}
	reply := &UpdateConfigReply{}
	if err := h.UpdateConfig(&UpdateConfigArgs{Workflows: &bad}, reply); err != nil {
		t.Fatalf("a rejected update should not be an RPC error, got %v", err)
	}

	if reply.Okay {
		t.Fatal("expected the update to be rejected")
	}
	fields := map[string]bool{}
	for _, e := range reply.Errors {
		fields[e.Field] = true
		if e.Workflow != 0 {
			t.Errorf("expected problems to point at workflow 0, got %d", e.Workflow)
		}
	}
	for _, want := range []string{"SectionTitle", "WorkflowType", "Filters"} {
		if !fields[want] {
			t.Errorf("expected a %s problem, got %v", want, reply.Errors)
		}
	}
	if reply.Message == "" {
		t.Error("expected a summary message explaining the rejection")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if string(after) != testConfigTOML {
		t.Errorf("a rejected update must leave the file alone, got:\n%s", after)
	}
	// The reply should still describe the configuration that is actually live.
	if len(reply.Config.Workflows) != 1 || reply.Config.Workflows[0].Name != "My Open PRs" {
		t.Errorf("expected the unchanged config in the reply, got %+v", reply.Config.Workflows)
	}
}

func TestGetConfigReportsAnUnparsableFile(t *testing.T) {
	path := useTempConfig(t, testConfigTOML)
	if err := os.WriteFile(path, []byte("this is not = = toml"), 0o644); err != nil {
		t.Fatalf("failed to corrupt the test config: %v", err)
	}

	h := &RPCHandler{}
	reply := &GetConfigReply{}
	if err := h.GetConfig(&GetConfigArgs{}, reply); err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}

	if reply.Okay {
		t.Error("expected okay=false when the file on disk no longer parses")
	}
	if reply.Message == "" {
		t.Error("expected a message explaining why the file couldn't be read")
	}
	// The running configuration is still meaningful and should come back.
	if len(reply.Config.Workflows) != 1 {
		t.Errorf("expected the in-memory config in the reply, got %+v", reply.Config.Workflows)
	}
}

func TestUpdateConfigWithNoFieldsIsANoOp(t *testing.T) {
	path := useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	reply := &UpdateConfigReply{}
	if err := h.UpdateConfig(&UpdateConfigArgs{}, reply); err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	if !reply.Okay {
		t.Errorf("an empty update should succeed, got %v", reply.Errors)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if string(after) != testConfigTOML {
		t.Errorf("an empty update must not rewrite the file, got:\n%s", after)
	}
}

func TestUpdateConfigUpdatesGlobalSettings(t *testing.T) {
	useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	sleep := 15
	notify := true
	username := "octocat"
	repos := []string{" owner/repo ", "", "other/repo"}
	reply := &UpdateConfigReply{}
	args := &UpdateConfigArgs{
		SleepDuration:        &sleep,
		DesktopNotifications: &notify,
		GithubUsername:       &username,
		Repos:                &repos,
	}
	if err := h.UpdateConfig(args, reply); err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	if !reply.Okay {
		t.Fatalf("expected the update to be accepted, got %v", reply.Errors)
	}
	if reply.Config.SleepDuration != 15 || !reply.Config.DesktopNotifications || reply.Config.GithubUsername != "octocat" {
		t.Errorf("global settings not applied: %+v", reply.Config)
	}
	if len(reply.Config.Repos) != 2 || reply.Config.Repos[0] != "owner/repo" {
		t.Errorf("expected blank repo entries dropped and the rest trimmed, got %v", reply.Config.Repos)
	}
	// Workflows weren't part of the update, so they must survive untouched.
	if len(reply.Config.Workflows) != 1 {
		t.Errorf("expected the existing workflow to survive, got %+v", reply.Config.Workflows)
	}
}

func TestGetConfigReturnsPluginsAndAI(t *testing.T) {
	// Root-level keys go first: after testConfigTOML they would land in its
	// [[Workflows]] table.
	useTempConfig(t, "ExperimentalLLMReviewEase = true\n"+testConfigTOML+`
[[Plugins]]
Name = "Summarize"
Command = "summarize_diff"
Provider = "openrouter"
Model = "anthropic/claude-sonnet-4.5"

[AI]
DefaultCommand = "claude -p"

[[AIFeatures]]
ID = "comments-addressed"
Enabled = true
Mode = "agent"
`)

	h := &RPCHandler{}
	reply := &GetConfigReply{}
	if err := h.GetConfig(&GetConfigArgs{}, reply); err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}

	if len(reply.Config.Plugins) != 1 || reply.Config.Plugins[0].Model != "anthropic/claude-sonnet-4.5" {
		t.Errorf("unexpected plugins in reply: %+v", reply.Config.Plugins)
	}
	if reply.Config.AI.DefaultCommand != "claude -p" {
		t.Errorf("expected [AI] in reply, got %+v", reply.Config.AI)
	}
	// Only the file's own entries: the review-ease flag is reported through
	// the registry instead.
	if len(reply.Config.AIFeatures) != 1 || reply.Config.AIFeatures[0].Mode != "agent" {
		t.Errorf("unexpected AI features in reply: %+v", reply.Config.AIFeatures)
	}

	var reviewEase *ai.TypeInfo
	for i := range reply.AIFeatureTypes {
		if reply.AIFeatureTypes[i].ID == ai.ReviewEaseID {
			reviewEase = &reply.AIFeatureTypes[i]
		}
	}
	if len(reply.AIFeatureTypes) != len(aiRunner.Registry().Features()) || reviewEase == nil {
		t.Fatalf("expected the AI feature registry for client pickers, got %+v", reply.AIFeatureTypes)
	}
	if reviewEase.LegacyKey != "ExperimentalLLMReviewEase" || reviewEase.Legacy == nil || !reviewEase.Legacy.Enabled {
		t.Errorf("expected review-ease to name the flag that switches it on, got %+v", reviewEase)
	}
}

func TestGetConfigSendsEmptyListsNotNull(t *testing.T) {
	useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	reply := &GetConfigReply{}
	if err := h.GetConfig(&GetConfigArgs{}, reply); err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}
	if reply.Config.Plugins == nil || reply.Config.AIFeatures == nil {
		t.Errorf("expected [] rather than null for absent lists, got %+v / %+v", reply.Config.Plugins, reply.Config.AIFeatures)
	}
}

func TestUpdateConfigSavesPluginsAndAI(t *testing.T) {
	path := useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	plugins := []config.Plugin{{
		Name:        " Style Guidelines ",
		Command:     "style_guidelines ",
		IncludeDiff: true,
		Provider:    "openrouter",
		Model:       " google/gemini-2.5-flash",
	}}
	settings := config.AISettings{DefaultProvider: "command", DefaultCommand: " claude -p "}
	features := []config.AIFeature{
		{ID: " feature-flags", Enabled: true, Automatic: true, Mode: "agent"},
		{ID: "change-diagram", Enabled: true, Provider: "openrouter", Model: "anthropic/claude-sonnet-4.5"},
	}
	reply := &UpdateConfigReply{}
	args := &UpdateConfigArgs{Plugins: &plugins, AI: &settings, AIFeatures: &features}
	if err := h.UpdateConfig(args, reply); err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	if !reply.Okay {
		t.Fatalf("expected the update to be accepted, got %v", reply.Errors)
	}

	wantPlugin := config.Plugin{Name: "Style Guidelines", Command: "style_guidelines", IncludeDiff: true, Provider: "openrouter", Model: "google/gemini-2.5-flash"}
	if len(reply.Config.Plugins) != 1 || reply.Config.Plugins[0] != wantPlugin {
		t.Errorf("expected the plugin trimmed and saved, got %+v", reply.Config.Plugins)
	}
	if reply.Config.AI.DefaultCommand != "claude -p" {
		t.Errorf("expected [AI] trimmed and saved, got %+v", reply.Config.AI)
	}
	if len(reply.Config.AIFeatures) != 2 || reply.Config.AIFeatures[0].ID != "feature-flags" {
		t.Errorf("expected the AI features saved in order, got %+v", reply.Config.AIFeatures)
	}
	// The running config is reloaded, so the features apply to the next run.
	if got := config.C().AutomaticAIFeatures(); len(got) != 1 || got[0] != "feature-flags" {
		t.Errorf("expected feature-flags to run automatically now, got %v", got)
	}
	if len(reply.Config.Workflows) != 1 {
		t.Errorf("workflows weren't part of the update and must survive, got %+v", reply.Config.Workflows)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written config: %v", err)
	}
	for _, want := range []string{"[[Plugins]]", "[AI]", "[[AIFeatures]]", "style_guidelines"} {
		if !strings.Contains(string(written), want) {
			t.Errorf("expected %s on disk, got:\n%s", want, written)
		}
	}
}

func TestUpdateConfigRejectsInvalidPluginsAndAI(t *testing.T) {
	path := useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	plugins := []config.Plugin{{Name: "Summarize", Command: "summarize_diff", Provider: "openrouter"}}
	features := []config.AIFeature{
		{ID: "not-a-feature", Enabled: true, Provider: "gemini"},
		{ID: "review-ease", Mode: "agent"},
	}
	reply := &UpdateConfigReply{}
	if err := h.UpdateConfig(&UpdateConfigArgs{Plugins: &plugins, AIFeatures: &features}, reply); err != nil {
		t.Fatalf("a rejected update should not be an RPC error, got %v", err)
	}
	if reply.Okay {
		t.Fatal("expected the update to be rejected")
	}
	fields := map[string]bool{}
	for _, e := range reply.Errors {
		fields[e.Field] = true
		if e.Workflow != -1 {
			t.Errorf("plugin and AI problems are root-level, got workflow %d", e.Workflow)
		}
	}
	for _, want := range []string{"Plugins[0].Model", "AIFeatures[0].ID", "AIFeatures[1].Mode"} {
		if !fields[want] {
			t.Errorf("expected a %s problem, got %v", want, reply.Errors)
		}
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if string(after) != testConfigTOML {
		t.Errorf("a rejected update must leave the file alone, got:\n%s", after)
	}
}

func TestUpdateConfigReportsDuplicatePluginNames(t *testing.T) {
	useTempConfig(t, testConfigTOML)

	h := &RPCHandler{}
	// Names differing only by whitespace are the same name once trimmed.
	plugins := []config.Plugin{{Name: "Summarize", Command: "a"}, {Name: "Summarize ", Command: "b"}}
	reply := &UpdateConfigReply{}
	if err := h.UpdateConfig(&UpdateConfigArgs{Plugins: &plugins}, reply); err != nil {
		t.Fatalf("a duplicate plugin name should be a validation problem, got RPC error %v", err)
	}
	if reply.Okay || len(reply.Errors) != 1 || reply.Errors[0].Field != "Plugins[1].Name" {
		t.Errorf("expected the second Summarize reported, got %+v", reply.Errors)
	}
}
