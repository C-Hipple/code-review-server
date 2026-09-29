package ai

import (
	"context"
	"crs/config"
	"crs/llm"
	"strings"
	"testing"
)

// agentFeature supports both modes, agent first.
type agentFeature struct{ fakeFeature }

func (*agentFeature) Modes() []string { return []string{config.AIModeAgent, config.AIModeOneShot} }

func TestRegistryDispatchesByID(t *testing.T) {
	reg := NewRegistry()
	a := &fakeFeature{id: "a"}
	b := &fakeFeature{id: "b"}
	reg.MustRegister(a)
	reg.MustRegister(b)

	if err := reg.Register(&fakeFeature{id: "a"}); err == nil {
		t.Error("a duplicate ID should be rejected")
	}
	if err := reg.Register(&fakeFeature{id: " "}); err == nil {
		t.Error("an empty ID should be rejected")
	}
	if got, ok := reg.Get("b"); !ok || got != b {
		t.Errorf("Get(b) = %v, %v", got, ok)
	}
	if _, ok := reg.Get("c"); ok {
		t.Error("Get(c) found an unregistered feature")
	}
	var ids []string
	for _, f := range reg.Features() {
		ids = append(ids, f.ID())
	}
	if strings.Join(ids, ",") != "a,b" {
		t.Errorf("Features() = %v, want registration order", ids)
	}

	f, _ := reg.Get("a")
	if _, err := f.Run(context.Background(), Request{}); err != nil || a.calls.Load() != 1 {
		t.Errorf("dispatch through the registry didn't reach the feature: %v", err)
	}
}

func TestRegistryValidate(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&fakeFeature{id: "oneshot-only"})
	reg.MustRegister(&agentFeature{fakeFeature{id: "agentic"}})

	problems := reg.Validate([]config.AIFeature{
		{ID: "oneshot-only", Mode: config.AIModeOneShot},
		{ID: "agentic", Mode: config.AIModeOneShot},
		{ID: "oneshot-only", Mode: config.AIModeAgent},
		{ID: "mermaid"},
		{ID: ""}, // config.Validate's to report
	})
	if len(problems) != 2 {
		t.Fatalf("expected 2 problems, got %v", problems)
	}
	if problems[0].Field != "AIFeatures[2].Mode" || !strings.Contains(problems[0].Message, `does not run in mode "agent"`) {
		t.Errorf("unexpected mode problem: %+v", problems[0])
	}
	if problems[1].Field != "AIFeatures[3].ID" || !strings.Contains(problems[1].Message, `unknown AI feature "mermaid"`) ||
		!strings.Contains(problems[1].Message, "oneshot-only, agentic") {
		t.Errorf("unexpected ID problem: %+v", problems[1])
	}
	for _, p := range problems {
		if p.Workflow != -1 {
			t.Errorf("AI problems are root-level: %+v", p)
		}
	}
}

func TestRegistryDescribeMergesConfig(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&fakeFeature{id: "plain"})
	reg.MustRegister(&agentFeature{fakeFeature{id: "agentic"}})

	cfg := config.Config{
		AI: config.AISettings{DefaultCommand: "claude -p"},
		AIFeatures: []config.AIFeature{
			{ID: "agentic", Enabled: true, Automatic: true, Provider: "gemini"},
		},
	}
	infos := reg.Describe(cfg)
	if len(infos) != 2 {
		t.Fatalf("Describe should list every registered feature, got %+v", infos)
	}
	plain, agentic := infos[0], infos[1]
	if plain.Enabled || plain.Automatic || plain.Mode != "oneshot" || plain.Provider != "command" {
		t.Errorf("unconfigured feature: %+v", plain)
	}
	if !agentic.Enabled || !agentic.Automatic || agentic.Mode != "agent" || agentic.Provider != "gemini" ||
		strings.Join(agentic.Modes, ",") != "agent,oneshot" {
		t.Errorf("configured feature: %+v", agentic)
	}

	// Automatic without Enabled is not automatic.
	cfg.AIFeatures[0].Enabled = false
	if info := reg.Describe(cfg)[1]; info.Automatic {
		t.Errorf("a disabled feature can't be automatic: %+v", info)
	}
}

func TestDefaultRegistryHasTheBuiltIns(t *testing.T) {
	for _, id := range []string{CommentsAddressedID, FeatureFlagsID} {
		f, ok := DefaultRegistry.Get(id)
		if !ok {
			t.Fatalf("%s is not registered", id)
		}
		if f.Name() == "" || strings.Join(modesOf(f), ",") != "oneshot,agent" {
			t.Errorf("unexpected feature: %q modes %v", f.Name(), modesOf(f))
		}
		if problems := ValidateFeatures([]config.AIFeature{{ID: id, Enabled: true, Mode: "agent"}}); len(problems) != 0 {
			t.Errorf("a valid entry was rejected: %v", problems)
		}
	}
	for _, id := range []string{FileOrderingID, ReviewEaseID} {
		f, ok := DefaultRegistry.Get(id)
		if !ok {
			t.Fatalf("%s is not registered", id)
		}
		if f.Name() == "" || !IsApplied(f) {
			t.Errorf("unexpected feature: %q applied %v", f.Name(), IsApplied(f))
		}
		if problems := ValidateFeatures([]config.AIFeature{{ID: id, Enabled: true, Automatic: true, Provider: "gemini"}}); len(problems) != 0 {
			t.Errorf("a valid entry was rejected: %v", problems)
		}
		if problems := ValidateFeatures([]config.AIFeature{{ID: id, Enabled: true, Mode: "agent"}}); len(problems) != 1 {
			t.Errorf("%s runs one-shot only, got %v", id, problems)
		}
	}
}

func TestDescribeMarksAppliedFeatures(t *testing.T) {
	infos := DefaultRegistry.Describe(config.Config{})
	applied := map[string]bool{}
	for _, info := range infos {
		applied[info.ID] = info.Applied
	}
	want := map[string]bool{CommentsAddressedID: false, FeatureFlagsID: false, FileOrderingID: true, ReviewEaseID: true}
	for id, w := range want {
		if got, ok := applied[id]; !ok || got != w {
			t.Errorf("%s: applied = %v (listed %v), want %v", id, got, ok, w)
		}
	}
}

func TestLegacyFlagsNameTheAppliedFeatures(t *testing.T) {
	// config spells out the IDs its legacy flags stand for, since it can't
	// import this package; they must name the applied features registered here.
	cfg, err := config.ParseConfigForTest([]byte("ExperimentalLLMFileOrdering = true\nExperimentalLLMReviewEase = true\n"))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	var enabled []string
	for _, f := range DefaultRegistry.Features() {
		entry, ok := cfg.AIFeatureSettings(f.ID())
		if !ok {
			continue
		}
		if !IsApplied(f) || !entry.Enabled || !entry.Automatic {
			t.Errorf("legacy flags enabled %s as %+v", f.ID(), entry)
		}
		if problems := ValidateFeatures([]config.AIFeature{entry}); len(problems) != 0 {
			t.Errorf("%s: the entry a flag stands for is rejected: %v", f.ID(), problems)
		}
		enabled = append(enabled, f.ID())
	}
	if strings.Join(enabled, ",") != FileOrderingID+","+ReviewEaseID {
		t.Errorf("legacy flags enabled %v", enabled)
	}
}

func TestLegacyBackendKeysBuildTheirProvider(t *testing.T) {
	// What llm.DefaultClient did for the experimental analysis: Gemini unless
	// ExperimentalLLMProvider says openrouter, which asks for
	// ExperimentalLLMModel; a model-less or unknown backend fails at
	// client-init.
	t.Setenv("GEMINI_API_KEY", "gemini-key")
	t.Setenv("OPENROUTER_API_KEY", "openrouter-key")
	build := func(toml string) (Provider, error) {
		t.Helper()
		cfg, err := config.ParseConfigForTest([]byte("ExperimentalLLMReviewEase = true\n" + toml))
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}
		entry, ok := cfg.AIFeatureSettings(ReviewEaseID)
		if !ok {
			t.Fatal("review-ease isn't enabled")
		}
		return NewProvider(cfg.AIProviderFor(entry))
	}

	if p, err := build(""); err != nil || p.Name() != "gemini" {
		t.Errorf("no backend set: %v, %v; want gemini", p, err)
	}
	p, err := build("ExperimentalLLMProvider = \"openrouter\"\nExperimentalLLMModel = \"google/gemini-2.5-flash\"\n")
	if err != nil || p.Name() != "openrouter" || p.Model() != "google/gemini-2.5-flash" {
		t.Errorf("openrouter: %v, %v", p, err)
	}
	_, err = build("ExperimentalLLMProvider = \"openrouter\"\n")
	wantStage(t, err, llm.StageClientInit)
	_, err = build("ExperimentalLLMProvider = \"openai\"\n")
	wantStage(t, err, llm.StageClientInit)
}
