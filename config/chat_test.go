package config

import (
	"slices"
	"strings"
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

func TestParseConfigReadsChatAgents(t *testing.T) {
	cfg, err := parseConfig([]byte(`
[Chat]
Agent = "gemini"

[Chat.Agents.gemini]
Name = "Gemini CLI"
Command = "gemini --experimental-acp"
Env = { GEMINI_API_KEY = "k" }
Models = ["gemini-2.5-pro"]
Mode = "plan"
AuthMethod = "gemini-api-key"

[Chat.Agents.wrapped]
Protocol = "claude-code"
Command = "my-claude"
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ChatAgent() != "gemini" {
		t.Errorf("ChatAgent() = %q", cfg.ChatAgent())
	}
	if ids := cfg.ChatAgentIDs(); !slices.Equal(ids, []string{"claude", "gemini", "wrapped"}) {
		t.Errorf("ChatAgentIDs() = %v", ids)
	}
	g := cfg.ChatBackend("gemini")
	if g.Protocol != ChatProtocolACP || g.Name != "Gemini CLI" || g.Command != "gemini --experimental-acp" || g.Env["GEMINI_API_KEY"] != "k" ||
		g.Mode != "plan" || g.AuthMethod != "gemini-api-key" || !slices.Equal(g.Models, []string{"gemini-2.5-pro"}) {
		t.Errorf("gemini = %+v (an agent naming no protocol speaks ACP)", g)
	}
	if w := cfg.ChatBackend("wrapped"); w.Protocol != ChatProtocolClaudeCode {
		t.Errorf("wrapped = %+v", w)
	}
	if c := cfg.ChatBackend(ChatBackendClaude); c.Protocol != ChatProtocolClaudeCode {
		t.Errorf("[Chat.Claude] is always Claude Code: %+v", c)
	}
	if problems := Validate(withChat(cfg.Chat)); len(problems) != 0 {
		t.Errorf("problems = %v", problems)
	}
}

func TestChatAgentDefaultsToClaude(t *testing.T) {
	if got := (Config{}).ChatAgent(); got != ChatBackendClaude {
		t.Errorf("ChatAgent() = %q", got)
	}
}

func TestChatTable(t *testing.T) {
	for id, want := range map[string]string{
		ChatBackendClaude: "[Chat.Claude]",
		"gemini":          "[Chat.Agents.gemini]",
		"my-agent_2":      "[Chat.Agents.my-agent_2]",
		"my agent":        `[Chat.Agents."my agent"]`,
	} {
		if got := ChatTable(id); got != want {
			t.Errorf("ChatTable(%q) = %q, want %q", id, got, want)
		}
	}
}

func withChat(chat ChatSettings) *Config {
	cfg := validConfig()
	cfg.Chat = chat
	return cfg
}

func TestValidateChecksTheChatAgents(t *testing.T) {
	for _, tt := range []struct {
		name  string
		chat  ChatSettings
		field string
		want  string
	}{
		{"an unknown agent", ChatSettings{Agent: "codex"}, "Chat.Agent", `there is no agent "codex": define it in [Chat.Agents.codex]`},
		{"claude's table reused", ChatSettings{Agents: map[string]ChatBackendSettings{"claude": {Command: "claude-code-acp"}}},
			"Chat.Agents.claude", "built-in Claude Code agent"},
		{"an unknown protocol", ChatSettings{Agents: map[string]ChatBackendSettings{"x": {Command: "x", Protocol: "smoke"}}},
			"Chat.Agents.x.Protocol", `unknown protocol "smoke" (expected one of: acp, claude-code)`},
		{"an ACP agent without a command", ChatSettings{Agents: map[string]ChatBackendSettings{"x": {}}},
			"Chat.Agents.x.Command", "is required"},
		{"a command that won't split", ChatSettings{Agents: map[string]ChatBackendSettings{"x": {Command: `"open`}}},
			"Chat.Agents.x.Command", ""},
		{"a protocol for [Chat.Claude]", ChatSettings{Claude: ChatBackendSettings{Protocol: "acp"}},
			"Chat.Claude.Protocol", "always Claude Code"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems := Validate(withChat(tt.chat))
			if len(problems) != 1 || problems[0].Field != tt.field || !strings.Contains(problems[0].Message, tt.want) {
				t.Errorf("problems = %v, want one for %s mentioning %q", problems, tt.field, tt.want)
			}
		})
	}
	// A Claude Code agent of [Chat.Agents] may leave Command to the default.
	if problems := Validate(withChat(ChatSettings{Agent: "c2", Agents: map[string]ChatBackendSettings{"c2": {Protocol: ChatProtocolClaudeCode}}})); len(problems) != 0 {
		t.Errorf("problems = %v", problems)
	}
}

func TestUpdateRenderKeepsChatAgents(t *testing.T) {
	useTempConfig(t, sampleConfig+`
[Chat]
Agent = "gemini"

[Chat.Agents.gemini]
Command = "gemini --experimental-acp"
Env = { GEMINI_API_KEY = "k" }
`)
	sleep := 15
	_, cfg, err := Update{SleepDuration: &sleep}.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if g := cfg.ChatBackend("gemini"); cfg.ChatAgent() != "gemini" || g.Command != "gemini --experimental-acp" || g.Env["GEMINI_API_KEY"] != "k" {
		t.Errorf("[Chat] lost in the round trip: %+v", cfg.Chat)
	}
}
