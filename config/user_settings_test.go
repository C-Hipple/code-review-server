package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestParseConfigDefaultsTheUserKeys(t *testing.T) {
	cfg, err := parseConfig([]byte(`SleepDuration = 5`))
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if cfg.UserActiveHours != DefaultUserActiveHours {
		t.Errorf("UserActiveHours = %d, want the default %d", cfg.UserActiveHours, DefaultUserActiveHours)
	}
	if cfg.UserRetentionDays != DefaultUserRetentionDays {
		t.Errorf("UserRetentionDays = %d, want the default %d", cfg.UserRetentionDays, DefaultUserRetentionDays)
	}
	if cfg.AllowServerIdentityActions {
		t.Error("AllowServerIdentityActions should default to false")
	}
}

// 0 means "never" for both counts, so a file that says 0 must not be read as
// one that says nothing and given the default.
func TestParseConfigKeepsAnExplicitZero(t *testing.T) {
	cfg, err := parseConfig([]byte("UserActiveHours = 0\nUserRetentionDays = 0\n"))
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if cfg.UserActiveHours != 0 {
		t.Errorf("UserActiveHours = %d, want 0", cfg.UserActiveHours)
	}
	if cfg.UserRetentionDays != 0 {
		t.Errorf("UserRetentionDays = %d, want 0", cfg.UserRetentionDays)
	}
}

func TestParseConfigReadsTheUserKeys(t *testing.T) {
	cfg, err := parseConfig([]byte("UserActiveHours = 6\nUserRetentionDays = 30\nAllowServerIdentityActions = true\n"))
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if cfg.UserActiveHours != 6 || cfg.UserRetentionDays != 30 || !cfg.AllowServerIdentityActions {
		t.Errorf("got UserActiveHours=%d UserRetentionDays=%d AllowServerIdentityActions=%v, want 6, 30, true",
			cfg.UserActiveHours, cfg.UserRetentionDays, cfg.AllowServerIdentityActions)
	}
}

func TestValidateRejectsNegativeUserKeys(t *testing.T) {
	cfg := validConfig()
	cfg.UserActiveHours = -1
	cfg.UserRetentionDays = -1
	fields := map[string]bool{}
	for _, p := range Validate(cfg) {
		fields[p.Field] = true
	}
	for _, want := range []string{"UserActiveHours", "UserRetentionDays"} {
		if !fields[want] {
			t.Errorf("expected a %s problem, got %v", want, Validate(cfg))
		}
	}

	// 0 is "never", a setting rather than a mistake.
	cfg = validConfig()
	if problems := Validate(cfg); len(problems) != 0 {
		t.Errorf("0 for both keys should validate, got %v", problems)
	}
}

func TestUpdateRenderWritesTheUserKeys(t *testing.T) {
	useTempConfig(t, sampleConfig)

	zero, days, allow := 0, 14, true
	data, cfg, err := Update{
		UserActiveHours:            &zero,
		UserRetentionDays:          &days,
		AllowServerIdentityActions: &allow,
	}.Render()
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if cfg.UserActiveHours != 0 || cfg.UserRetentionDays != 14 || !cfg.AllowServerIdentityActions {
		t.Errorf("rendered config has UserActiveHours=%d UserRetentionDays=%d AllowServerIdentityActions=%v, want 0, 14, true",
			cfg.UserActiveHours, cfg.UserRetentionDays, cfg.AllowServerIdentityActions)
	}
	for _, line := range []string{"UserActiveHours = 0", "UserRetentionDays = 14", "AllowServerIdentityActions = true"} {
		if !strings.Contains(string(data), line) {
			t.Errorf("expected %q in the rendered file, got:\n%s", line, data)
		}
	}
	if (Update{UserActiveHours: &zero}).IsEmpty() {
		t.Error("setting UserActiveHours is a change")
	}
}

// User says whose dashboard a built workflow fills. It is not a setting, so it
// must never reach the config file or a client — and a client must not be
// able to set it.
func TestRawWorkflowUserIsNeverSerialized(t *testing.T) {
	wf := RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: "wf", SectionTitle: "S", User: "alice"}

	encoded, err := toml.Marshal(wf)
	if err != nil {
		t.Fatalf("toml.Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "alice") {
		t.Errorf("User leaked into TOML:\n%s", encoded)
	}

	asJSON, err := json.Marshal(wf)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(asJSON), "alice") {
		t.Errorf("User leaked into JSON: %s", asJSON)
	}

	var decoded RawWorkflow
	if err := json.Unmarshal([]byte(`{"Name":"wf","User":"mallory"}`), &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded.User != "" {
		t.Errorf("User was read from JSON: %q", decoded.User)
	}
}
