package config

import (
	"strings"
	"testing"
)

func TestParseConfigReadsAISettings(t *testing.T) {
	cfg, err := parseConfig([]byte(`
[AI]
DefaultProvider = "command"
DefaultCommand = "claude -p"

[[AIFeatures]]
ID = "comments-addressed"
Enabled = true
Automatic = true
Mode = "agent"

[[AIFeatures]]
ID = "mermaid"
Provider = "gemini"
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.AI.DefaultProvider != "command" || cfg.AI.DefaultCommand != "claude -p" {
		t.Errorf("unexpected [AI] table: %+v", cfg.AI)
	}
	if len(cfg.AIFeatures) != 2 {
		t.Fatalf("expected 2 AI features, got %+v", cfg.AIFeatures)
	}
	first := cfg.AIFeatures[0]
	if first.ID != "comments-addressed" || !first.Enabled || !first.Automatic || first.Mode != "agent" {
		t.Errorf("unexpected first feature: %+v", first)
	}
	if second := cfg.AIFeatures[1]; second.Enabled || second.Automatic || second.Provider != "gemini" {
		t.Errorf("unexpected second feature: %+v", second)
	}
}

func TestParseConfigRejectsDuplicateAIFeatures(t *testing.T) {
	_, err := parseConfig([]byte(`
[[AIFeatures]]
ID = "comments-addressed"

[[AIFeatures]]
ID = "comments-addressed"
Enabled = true
`))
	if err == nil || err.Error() != "duplicate AI feature ID found: comments-addressed" {
		t.Errorf("expected a duplicate AI feature error, got %v", err)
	}
}

func TestDefaultConfigLeavesAIOff(t *testing.T) {
	// Rollout relies on this: existing users, and anyone on the built-in
	// defaults, get no AI behavior until they opt in.
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	if len(cfg.AIFeatures) != 0 || cfg.AI != (AISettings{}) {
		t.Errorf("expected no AI settings in the defaults, got %+v / %+v", cfg.AI, cfg.AIFeatures)
	}
	if strings.Contains(DefaultConfigTOML, "AIFeatures") {
		t.Error("DefaultConfigTOML must not enable any AI feature")
	}
}

func TestAIProviderFor(t *testing.T) {
	const sonnet = "anthropic/claude-sonnet-4.5"
	tests := []struct {
		name    string
		ai      AISettings
		feature AIFeature
		want    AIProviderChoice
	}{
		{"nothing configured falls back to gemini", AISettings{}, AIFeature{}, AIProviderChoice{Provider: "gemini"}},
		{"a default command implies the command provider", AISettings{DefaultCommand: "llm"}, AIFeature{}, AIProviderChoice{Provider: "command", Command: "llm"}},
		{"a feature command implies the command provider", AISettings{}, AIFeature{Command: "claude -p"}, AIProviderChoice{Provider: "command", Command: "claude -p"}},
		{"an explicit default provider wins over inference", AISettings{DefaultProvider: "gemini", DefaultCommand: "llm"}, AIFeature{}, AIProviderChoice{Provider: "gemini", Command: "llm"}},
		{"a default provider alone is used as given", AISettings{DefaultProvider: "command"}, AIFeature{}, AIProviderChoice{Provider: "command"}},
		{"a feature provider wins over the default", AISettings{DefaultProvider: "command", DefaultCommand: "llm"}, AIFeature{Provider: "gemini"}, AIProviderChoice{Provider: "gemini", Command: "llm"}},
		{"a feature provider wins over its own command", AISettings{}, AIFeature{Provider: "gemini", Command: "claude -p"}, AIProviderChoice{Provider: "gemini", Command: "claude -p"}},
		{"a feature command wins over the default", AISettings{DefaultCommand: "llm"}, AIFeature{Command: "claude -p"}, AIProviderChoice{Provider: "command", Command: "claude -p"}},
		{"a feature command wins over a default provider", AISettings{DefaultProvider: "gemini"}, AIFeature{Command: "claude -p"}, AIProviderChoice{Provider: "command", Command: "claude -p"}},
		{"a feature provider takes the default command", AISettings{DefaultProvider: "gemini", DefaultCommand: "llm"}, AIFeature{Provider: "command"}, AIProviderChoice{Provider: "command", Command: "llm"}},
		{"openrouter as the default takes the default model", AISettings{DefaultProvider: "openrouter", DefaultModel: sonnet}, AIFeature{}, AIProviderChoice{Provider: "openrouter", Model: sonnet}},
		{"a feature model wins over the default model", AISettings{DefaultProvider: "openrouter", DefaultModel: sonnet}, AIFeature{Model: "openai/gpt-5"}, AIProviderChoice{Provider: "openrouter", Model: "openai/gpt-5"}},
		{"a feature picks openrouter over a default command", AISettings{DefaultCommand: "llm"}, AIFeature{Provider: "openrouter", Model: sonnet}, AIProviderChoice{Provider: "openrouter", Command: "llm", Model: sonnet}},
		{"a model alone picks no provider", AISettings{}, AIFeature{Model: sonnet}, AIProviderChoice{Provider: "gemini", Model: sonnet}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{AI: tt.ai}
			if got := cfg.AIProviderFor(tt.feature); got != tt.want {
				t.Errorf("AIProviderFor = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAIFeatureSettings(t *testing.T) {
	cfg := Config{AIFeatures: []AIFeature{{ID: "comments-addressed", Enabled: true}}}
	if f, ok := cfg.AIFeatureSettings("comments-addressed"); !ok || !f.Enabled {
		t.Errorf("expected the configured entry, got %+v, %v", f, ok)
	}
	if _, ok := cfg.AIFeatureSettings("mermaid"); ok {
		t.Error("expected no entry for an unconfigured feature")
	}
}

func TestValidateAI(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*Config)
		wantField   string
		wantMessage string
	}{
		{
			name:        "unknown default provider",
			mutate:      func(c *Config) { c.AI.DefaultProvider = "openai" },
			wantField:   "AI.DefaultProvider",
			wantMessage: "unknown provider",
		},
		{
			name:        "unsplittable default command",
			mutate:      func(c *Config) { c.AI.DefaultCommand = `claude "-p` },
			wantField:   "AI.DefaultCommand",
			wantMessage: "unterminated",
		},
		{
			name:        "feature without an ID",
			mutate:      func(c *Config) { c.AIFeatures = []AIFeature{{Enabled: true}} },
			wantField:   "AIFeatures[0].ID",
			wantMessage: "required",
		},
		{
			name:        "unknown mode",
			mutate:      func(c *Config) { c.AIFeatures = []AIFeature{{ID: "x", Mode: "swarm"}} },
			wantField:   "AIFeatures[0].Mode",
			wantMessage: "unknown mode",
		},
		{
			name:        "unknown feature provider",
			mutate:      func(c *Config) { c.AIFeatures = []AIFeature{{ID: "x", Provider: "openai"}} },
			wantField:   "AIFeatures[0].Provider",
			wantMessage: "unknown provider",
		},
		{
			name: "enabled command provider without a command",
			mutate: func(c *Config) {
				c.AIFeatures = []AIFeature{{ID: "x", Enabled: true, Provider: "command"}}
			},
			wantField:   "AIFeatures[0].Command",
			wantMessage: "needs a command",
		},
		{
			name: "enabled openrouter provider without a model",
			mutate: func(c *Config) {
				c.AI.DefaultProvider = "openrouter"
				c.AIFeatures = []AIFeature{{ID: "x", Enabled: true}}
			},
			wantField:   "AIFeatures[0].Model",
			wantMessage: "needs a model",
		},
		{
			name:        "unknown plugin provider",
			mutate:      func(c *Config) { c.Plugins = []Plugin{{Name: "p", Command: "p", Provider: "command"}} },
			wantField:   "Plugins[0].Provider",
			wantMessage: "unknown provider",
		},
		{
			name:        "openrouter plugin without a model",
			mutate:      func(c *Config) { c.Plugins = []Plugin{{Name: "p", Command: "p", Provider: "openrouter"}} },
			wantField:   "Plugins[0].Model",
			wantMessage: "needs a model",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)
			found := false
			for _, p := range Validate(cfg) {
				if p.Workflow == -1 && p.Field == tt.wantField && strings.Contains(p.Message, tt.wantMessage) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected a %s problem mentioning %q, got %v", tt.wantField, tt.wantMessage, Validate(cfg))
			}
		})
	}
}

func TestValidateAIAcceptsWorkingSettings(t *testing.T) {
	cfg := validConfig()
	cfg.AI = AISettings{DefaultCommand: `claude -p --append-system-prompt "be brief"`, DefaultModel: "anthropic/claude-sonnet-4.5"}
	cfg.AIFeatures = []AIFeature{
		{ID: "comments-addressed", Enabled: true, Automatic: true, Mode: "oneshot"},
		{ID: "mermaid", Enabled: true, Provider: "gemini", Mode: "agent"},
		{ID: "feature-flags", Enabled: true, Provider: "openrouter"},
		// Disabled and half-configured: harmless, so not worth blocking a save.
		{ID: "later", Provider: "command"},
		{ID: "someday", Provider: "openrouter", Model: " "},
	}
	cfg.Plugins = []Plugin{
		{Name: "a", Command: "a"},
		{Name: "b", Command: "b", Provider: "gemini"},
		{Name: "c", Command: "c", Provider: "openrouter", Model: "google/gemini-2.5-flash"},
	}
	if problems := Validate(cfg); len(problems) != 0 {
		t.Errorf("expected no problems, got %v", problems)
	}
}

func TestPluginEnv(t *testing.T) {
	if env := (Plugin{Name: "p"}).Env(); len(env) != 0 {
		t.Errorf("a plugin with no backend set should add nothing to the environment, got %v", env)
	}
	env := Plugin{Provider: "openrouter", Model: "openai/gpt-5"}.Env()
	if len(env) != 2 || env[0] != "CRS_LLM_PROVIDER=openrouter" || env[1] != "CRS_LLM_MODEL=openai/gpt-5" {
		t.Errorf("Env = %v", env)
	}
}

func TestUpdateRenderKeepsAISettings(t *testing.T) {
	// The config RPCs don't model the AI tables; a save of anything else must
	// still carry them over untouched.
	useTempConfig(t, sampleConfig+`
[AI]
DefaultCommand = "claude -p"

[[AIFeatures]]
ID = "comments-addressed"
Enabled = true
Automatic = true
`)
	sleep := 15
	_, cfg, err := Update{SleepDuration: &sleep}.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if cfg.AI.DefaultCommand != "claude -p" {
		t.Errorf("[AI] lost in the round trip: %+v", cfg.AI)
	}
	if len(cfg.AIFeatures) != 1 || cfg.AIFeatures[0].ID != "comments-addressed" ||
		!cfg.AIFeatures[0].Enabled || !cfg.AIFeatures[0].Automatic {
		t.Errorf("[[AIFeatures]] lost in the round trip: %+v", cfg.AIFeatures)
	}
}
