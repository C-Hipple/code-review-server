package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUserConfigJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, configFileName)

	if _, err := UserConfigJSON(); err == nil || !strings.Contains(err.Error(), "no config file at "+path) {
		t.Errorf("without a config file: err = %v, want one naming %s", err, path)
	}

	if err := os.WriteFile(path, []byte(`
GithubUsername = "octocat"
Repos = ["acme/widgets"]
SleepDuration = 5
DesktopNotifications = true

[SectionPriority]
"Needs Review" = 1

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "review"
SectionTitle = "Needs Review"
Filters = ["FilterNotDraft"]
DesktopNotifications = true

[[Workflows]]
WorkflowType = "ListMyPRsWorkflow"
Name = "mine"
SectionTitle = "Mine"
GithubUsername = "someone-else"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := UserConfigJSON()
	if err != nil {
		t.Fatalf("UserConfigJSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	if len(keys) != 4 || got["Workflows"] == nil || got["Repos"] == nil || got["SectionPriority"] == nil || got["SectionSorting"] == nil {
		t.Errorf("keys = %v, want exactly Workflows, Repos, SectionPriority and SectionSorting\n%s", keys, out)
	}
	if fmt.Sprint(got["SectionSorting"]) != "map[]" {
		t.Errorf("an absent SectionSorting = %v, want {}", got["SectionSorting"])
	}

	var uc UserConfig
	if err := json.Unmarshal(out, &uc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(uc.Repos, []string{"acme/widgets"}) || uc.SectionPriority["Needs Review"] != 1 {
		t.Errorf("settings = %+v", uc)
	}
	if len(uc.Workflows) != 2 {
		t.Fatalf("workflows = %+v", uc.Workflows)
	}
	review, mine := uc.Workflows[0], uc.Workflows[1]
	if review.Name != "review" || review.WorkflowType != "SyncReviewRequestsWorkflow" || !reflect.DeepEqual(review.Filters, []string{"FilterNotDraft"}) {
		t.Errorf("first workflow = %+v", review)
	}
	// As written: the root username is not copied in, the explicit one stays.
	if review.GithubUsername != "" || mine.GithubUsername != "someone-else" {
		t.Errorf("usernames = %q, %q, want \"\" and someone-else", review.GithubUsername, mine.GithubUsername)
	}
	if review.DesktopNotifications != nil {
		t.Error("DesktopNotifications was not dropped")
	}

	// Nothing at all still prints empty lists and maps, not nulls.
	if err := os.WriteFile(path, []byte("SleepDuration = 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = UserConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"Workflows":[],"Repos":[],"SectionPriority":{},"SectionSorting":{}}`; string(out) != want {
		t.Errorf("empty config printed %s, want %s", out, want)
	}

	if err := os.WriteFile(path, []byte("Repos = \"not a list\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := UserConfigJSON(); err == nil || !strings.Contains(err.Error(), "could not parse "+path) {
		t.Errorf("an invalid file: err = %v, want a parse error naming it", err)
	}
}

func TestValidateUserSettings(t *testing.T) {
	valid := RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: "review", SectionTitle: "Needs Review"}
	if problems := ValidateUserSettings([]RawWorkflow{valid}, []string{"acme/widgets"}, map[string]string{"Needs Review": "oldest_first"}); len(problems) != 0 {
		t.Errorf("valid settings: %v", problems)
	}

	fields := func(problems []ValidationError) []string {
		var out []string
		for _, p := range problems {
			out = append(out, fmt.Sprintf("%d.%s", p.Workflow, p.Field))
		}
		return out
	}

	controlled := valid
	controlled.Name = "re\x1fview"
	nameless := valid
	nameless.Name = ""
	problems := ValidateUserSettings([]RawWorkflow{valid, valid, controlled, nameless},
		[]string{"not-a-repo"}, map[string]string{"S": "sideways"})
	want := []string{"-1.Repos", "-1.SectionSorting", "1.Name", "3.Name", "2.Name"}
	if got := fields(problems); !reflect.DeepEqual(got, want) {
		t.Errorf("problems = %v, want %v", problems, want)
	}

	many := make([]RawWorkflow, MaxUserWorkflows+1)
	for i := range many {
		many[i] = valid
		many[i].Name = fmt.Sprintf("wf-%d", i)
	}
	problems = ValidateUserSettings(many, nil, nil)
	if got := fields(problems); !reflect.DeepEqual(got, []string{"-1.Workflows"}) {
		t.Errorf("%d workflows: %v", len(many), problems)
	}
	if problems := ValidateUserSettings(many[:MaxUserWorkflows], nil, nil); len(problems) != 0 {
		t.Errorf("exactly %d workflows: %v", MaxUserWorkflows, problems)
	}
}
