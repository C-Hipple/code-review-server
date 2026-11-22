package chat

import (
	"crs/config"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestEveryConfigProtocolHasADriver(t *testing.T) {
	for _, name := range config.ChatProtocols {
		if _, ok := LookupProtocol(name); !ok {
			t.Errorf("config names the protocol %q, which nothing drives", name)
		}
	}
	if got := Protocols(); !slices.Equal(got, slices.Sorted(slices.Values(config.ChatProtocols))) {
		t.Errorf("Protocols() = %v, config.ChatProtocols = %v", got, config.ChatProtocols)
	}
}

func TestConfigSourceListsTheConfiguredAgents(t *testing.T) {
	cfg := config.Config{Chat: config.ChatSettings{
		Agent: "gemini",
		Agents: map[string]config.ChatBackendSettings{
			"zed":    {Command: "claude-code-acp", Name: "Claude (ACP)"},
			"gemini": {Command: "gemini --experimental-acp"},
			"odd":    {Command: "x", Protocol: "smoke-signals"},
		},
	}}
	src := &ConfigSource{Config: func() config.Config { return cfg }}
	backends := src.Backends()
	var ids []string
	for _, b := range backends {
		ids = append(ids, b.ID())
	}
	if !slices.Equal(ids, []string{"claude", "gemini", "odd", "zed"}) {
		t.Fatalf("ids = %v", ids)
	}
	if src.Default() != "gemini" {
		t.Errorf("Default() = %q", src.Default())
	}
	if _, ok := backends[0].(*ClaudeBackend); !ok {
		t.Errorf("claude should be Claude Code: %T", backends[0])
	}
	if _, ok := backends[1].(*ACPBackend); !ok {
		t.Errorf("an agent naming no protocol should speak ACP: %T", backends[1])
	}
	if info := backends[3].Info(); info.Name != "Claude (ACP)" {
		t.Errorf("zed's name = %q", info.Name)
	}
	if info := backends[2].Info(); info.Available || !strings.Contains(info.Reason, `its protocol "smoke-signals" isn't one the server speaks`) {
		t.Errorf("odd = %+v", info)
	}

	// Backends are kept, so an agent running between turns is too.
	if again := src.Backends(); again[1] != backends[1] {
		t.Error("a backend should be kept across calls")
	}

	// A protocol change makes a new backend; an agent taken out is dropped.
	cfg.Chat.Agents = map[string]config.ChatBackendSettings{"gemini": {Command: "gemini", Protocol: config.ChatProtocolClaudeCode}}
	cfg.Chat.Agent = ""
	backends = src.Backends()
	if len(backends) != 2 || backends[1].ID() != "gemini" {
		t.Fatalf("backends = %v", backends)
	}
	if _, ok := backends[1].(*ClaudeBackend); !ok {
		t.Errorf("gemini should now be Claude Code: %T", backends[1])
	}
	if src.Default() != config.ChatBackendClaude {
		t.Errorf("with no Agent, new chats talk to Claude Code: %q", src.Default())
	}
}

func TestManagerAgentIsTheSourcesDefault(t *testing.T) {
	a := &ClaudeBackend{AgentID: "a", LookPath: func(string) (string, error) { return "", errNotFound }}
	b := &ClaudeBackend{AgentID: "b", LookPath: func(string) (string, error) { return "/bin/claude", nil }}
	if got := NewManager(Fixed{a, b}).Agent(); got != "b" {
		t.Errorf("with no default, the first available agent: %q", got)
	}
	if got := NewManager(defaultSource{Fixed: Fixed{a, b}, id: "a"}).Agent(); got != "a" {
		t.Errorf("the source's default, available or not: %q", got)
	}
	if got := NewManager(Fixed{}).Agent(); got != "" {
		t.Errorf("no agents: %q", got)
	}
}

var errNotFound = errors.New("not found")
