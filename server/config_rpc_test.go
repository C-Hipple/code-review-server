package server

import (
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
