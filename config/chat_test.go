package config

import (
	"slices"
	"testing"
)

func TestParseConfigReadsChatSettings(t *testing.T) {
	cfg, err := parseConfig([]byte(`
[Chat.Claude]
Command = "/opt/bin/claude"
Models = ["opus", "sonnet"]
DefaultModel = "sonnet"
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	got := cfg.ChatBackend(ChatBackendClaude)
	if got.Command != "/opt/bin/claude" || got.DefaultModel != "sonnet" || got.Disabled ||
		!slices.Equal(got.Models, []string{"opus", "sonnet"}) {
		t.Errorf("unexpected [Chat.Claude]: %+v", got)
	}
	if other := cfg.ChatBackend("codex"); other.Command != "" || other.Disabled {
		t.Errorf("an unknown backend should get the zero settings, got %+v", other)
	}
}

func TestChatSettingsDefaultToNothing(t *testing.T) {
	cfg, err := parseConfig([]byte(`Repos = ["acme/widgets"]`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if got := cfg.ChatBackend(ChatBackendClaude); got.Disabled || got.Command != "" || len(got.Models) != 0 {
		t.Errorf("a config without [Chat] should leave Claude at its defaults, got %+v", got)
	}
}

func TestUpdateRenderKeepsChatSettings(t *testing.T) {
	// The config RPCs don't model [Chat]; saving anything else carries it over.
	useTempConfig(t, sampleConfig+`
[Chat.Claude]
Disabled = true
Models = ["haiku"]
`)
	sleep := 15
	_, cfg, err := Update{SleepDuration: &sleep}.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := cfg.ChatBackend(ChatBackendClaude); !got.Disabled || !slices.Equal(got.Models, []string{"haiku"}) {
		t.Errorf("[Chat.Claude] lost in the round trip: %+v", got)
	}
}
