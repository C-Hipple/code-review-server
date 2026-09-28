package ai

import (
	"context"
	"crs/config"
	"crs/database"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeFeature is a Feature whose Run is supplied by the test.
type fakeFeature struct {
	id    string
	run   func(ctx context.Context, req Request) (Result, error)
	calls atomic.Int32
}

func (f *fakeFeature) ID() string   { return f.id }
func (f *fakeFeature) Name() string { return "Fake " + f.id }
func (f *fakeFeature) Run(ctx context.Context, req Request) (Result, error) {
	f.calls.Add(1)
	if f.run == nil {
		return Result{Body: Body{BodyType: BodyMarkdown, BodyContent: "done"}}, nil
	}
	return f.run(ctx, req)
}

// testRunner builds a runner over the given features, with a fresh DB and
// CRS home, and every feature enabled (automatically, too) unless entries
// says otherwise.
func testRunner(t *testing.T, entries []config.AIFeature, features ...Feature) (*Runner, *database.DB, string) {
	t.Helper()
	crsHome := t.TempDir()
	t.Setenv("CRS_HOME", crsHome)
	db, err := database.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	if entries == nil {
		for _, f := range features {
			entries = append(entries, config.AIFeature{ID: f.ID(), Enabled: true, Automatic: true})
		}
	}
	config.SetC(config.Config{DB: db, AIFeatures: entries})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	reg := NewRegistry()
	for _, f := range features {
		reg.MustRegister(f)
	}
	r := NewRunner(reg)
	r.NewProvider = func(provider, command string) (Provider, error) {
		return nil, errors.New("no provider in this test")
	}
	return r, db, crsHome
}

// job is a job for acme/widgets#42 whose request carries sha and digest.
func job(feature string, trigger Trigger, sha, digest string) Job {
	return Job{
		Owner: "acme", Repo: "widgets", Number: 42,
		Feature: feature, Trigger: trigger,
		SHA: sha, Digest: digest,
		Build: func(context.Context) (Request, error) {
			return Request{Owner: "acme", Repo: "widgets", Number: 42, HeadSHA: sha, Digest: digest}, nil
		},
	}
}

func callLog(t *testing.T, crsHome string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(crsHome, "llm_calls.log"))
	if err != nil {
		t.Fatalf("reading the call log: %v", err)
	}
	return string(data)
}

func TestRunnerStoresTheResultKeyedByItsInputs(t *testing.T) {
	f := &fakeFeature{id: "fake"}
	r, db, crsHome := testRunner(t, nil, f)

	if got := r.RunSync(job("fake", TriggerExplicit, "sha-1", "digest-1")); got != OutcomeStarted {
		t.Fatalf("RunSync = %q", got)
	}
	stored, ok, err := db.GetAIResult("acme", "widgets", 42, "fake")
	if err != nil || !ok {
		t.Fatalf("nothing stored (err %v)", err)
	}
	if stored.Status != StatusSuccess || stored.SHA != "sha-1" || stored.InputHash != "digest-1" {
		t.Errorf("unexpected row: %+v", stored)
	}
	if doc := DecodeDocument(stored.Result); doc.Body.BodyContent != "done" {
		t.Errorf("stored body = %+v", doc.Body)
	}
	log := callLog(t, crsHome)
	for _, want := range []string{"=== AI Feature Run ===", "Purpose:  ai:fake (oneshot)", "Trigger:  explicit", "Call:     no model call", "Outcome:  SUCCESS"} {
		if !strings.Contains(log, want) {
			t.Errorf("call log missing %q:\n%s", want, log)
		}
	}
}

func TestRunnerCacheHitNeedsBothKeyParts(t *testing.T) {
	f := &fakeFeature{id: "fake"}
	r, _, _ := testRunner(t, nil, f)

	// Review threads as the PRReviewThreads cache holds them, before and after
	// the reviewer resolves the only thread. Nothing else changes.
	const comments = `[{"id": 5001, "body": "Rename this."}]`
	const reviews = `[{"id": 700, "state": "COMMENTED"}]`
	open := InputsDigest(comments, reviews, `[{"id": "PRRT_1", "is_resolved": false, "is_outdated": false}]`)
	resolved := InputsDigest(comments, reviews, `[{"id": "PRRT_1", "is_resolved": true, "is_outdated": false}]`)

	r.RunSync(job("fake", TriggerExplicit, "sha-1", open))
	if got := r.RunSync(job("fake", TriggerExplicit, "sha-1", open)); got != OutcomeUpToDate {
		t.Errorf("same SHA and digest: got %q, want up-to-date", got)
	}
	if got := r.RunSync(job("fake", TriggerExplicit, "sha-1", resolved)); got != OutcomeStarted {
		t.Errorf("a resolved thread must invalidate the result: got %q", got)
	}
	if got := r.RunSync(job("fake", TriggerExplicit, "sha-2", resolved)); got != OutcomeStarted {
		t.Errorf("a new head SHA must invalidate the result: got %q", got)
	}
	if got := r.RunSync(job("fake", TriggerRerun, "sha-2", resolved)); got != OutcomeStarted {
		t.Errorf("a rerun ignores the cache: got %q", got)
	}
	if n := f.calls.Load(); n != 4 {
		t.Errorf("feature ran %d times, want 4", n)
	}
}

// codeOnlyFeature is a fakeFeature that reads only the code.
type codeOnlyFeature struct{ fakeFeature }

func (*codeOnlyFeature) CodeOnly() bool { return true }

func TestRunnerKeysACodeOnlyFeatureByTheSHAAlone(t *testing.T) {
	var seen string
	f := &codeOnlyFeature{fakeFeature{id: "code", run: func(_ context.Context, req Request) (Result, error) {
		seen = req.Digest
		return Result{}, nil
	}}}
	r, db, _ := testRunner(t, nil, f)

	r.RunSync(job("code", TriggerExplicit, "sha-1", "digest-1"))
	if seen != CodeOnlyDigest {
		t.Errorf("the feature saw digest %q, want %q", seen, CodeOnlyDigest)
	}
	stored, _, _ := db.GetAIResult("acme", "widgets", 42, "code")
	if stored.SHA != "sha-1" || stored.InputHash != CodeOnlyDigest {
		t.Errorf("stored key = %q/%q, want sha-1/%s", stored.SHA, stored.InputHash, CodeOnlyDigest)
	}
	if got := r.RunSync(job("code", TriggerExplicit, "sha-1", "digest-2")); got != OutcomeUpToDate {
		t.Errorf("a new comment must not invalidate a code-only result: got %q", got)
	}
	if got := r.RunSync(job("code", TriggerExplicit, "sha-2", "digest-2")); got != OutcomeStarted {
		t.Errorf("a new head SHA must invalidate a code-only result: got %q", got)
	}
	if n := f.calls.Load(); n != 2 {
		t.Errorf("feature ran %d times, want 2", n)
	}
}

func TestRunnerRetriesAFailedRunOnlyWhenAsked(t *testing.T) {
	fail := true
	f := &fakeFeature{id: "fake", run: func(context.Context, Request) (Result, error) {
		if fail {
			return Result{}, errors.New("model down")
		}
		return Result{}, nil
	}}
	r, db, _ := testRunner(t, nil, f)

	r.RunSync(job("fake", TriggerAutomatic, "sha-1", "d"))
	if stored, _, _ := db.GetAIResult("acme", "widgets", 42, "fake"); stored.Status != StatusError ||
		!strings.Contains(DecodeDocument(stored.Result).Body.BodyContent, "model down") {
		t.Fatalf("expected the failure stored with its reason, got %+v", stored)
	}
	if got := r.RunSync(job("fake", TriggerAutomatic, "sha-1", "d")); got != OutcomeUpToDate {
		t.Errorf("the automatic path must not retry the same inputs: got %q", got)
	}
	fail = false
	if got := r.RunSync(job("fake", TriggerExplicit, "sha-1", "d")); got != OutcomeStarted {
		t.Errorf("someone asking retries a failure: got %q", got)
	}
	if stored, _, _ := db.GetAIResult("acme", "widgets", 42, "fake"); stored.Status != StatusSuccess {
		t.Errorf("status after the retry = %q", stored.Status)
	}
}

func TestRunnerHonorsConfig(t *testing.T) {
	onDemand := &fakeFeature{id: "on-demand"}
	disabled := &fakeFeature{id: "disabled"}
	r, _, _ := testRunner(t, []config.AIFeature{
		{ID: "on-demand", Enabled: true},
		{ID: "disabled", Automatic: true}, // Automatic alone enables nothing
	}, onDemand, disabled)

	if got := r.RunSync(job("disabled", TriggerExplicit, "s", "d")); got != OutcomeDisabled {
		t.Errorf("disabled feature: got %q", got)
	}
	if got := r.RunSync(job("on-demand", TriggerAutomatic, "s", "d")); got != OutcomeDisabled {
		t.Errorf("an on-demand feature must not run automatically: got %q", got)
	}
	if got := r.RunSync(job("on-demand", TriggerExplicit, "s", "d")); got != OutcomeStarted {
		t.Errorf("an on-demand feature runs when asked: got %q", got)
	}
	if got := r.RunSync(job("nope", TriggerExplicit, "s", "d")); got != OutcomeUnknown {
		t.Errorf("unregistered feature: got %q", got)
	}
	if disabled.calls.Load() != 0 {
		t.Error("the disabled feature ran")
	}
}

func TestRunnerKeepsOneRunInFlightPerPRAndFeature(t *testing.T) {
	release := make(chan struct{})
	f := &fakeFeature{id: "fake", run: func(context.Context, Request) (Result, error) {
		<-release
		return Result{}, nil
	}}
	r, _, _ := testRunner(t, nil, f)

	if got := r.Dispatch(job("fake", TriggerExplicit, "s", "d")); got != OutcomeStarted {
		t.Fatalf("Dispatch = %q", got)
	}
	if !r.Running("acme", "widgets", 42, "fake") {
		t.Error("Running should report the dispatched run")
	}
	if got := r.Dispatch(job("fake", TriggerRerun, "s", "d")); got != OutcomeAlreadyRunning {
		t.Errorf("second dispatch = %q, want already-running", got)
	}
	close(release)
	waitFor(t, func() bool { return !r.Running("acme", "widgets", 42, "fake") })
	if n := f.calls.Load(); n != 1 {
		t.Errorf("feature ran %d times, want 1", n)
	}
}

func TestRunnerQueuesAutomaticRunsButNotRequestedOnes(t *testing.T) {
	f := &fakeFeature{id: "fake"}
	g := &fakeFeature{id: "other"}
	r, _, _ := testRunner(t, nil, f, g)

	// A burst of background runs already holds every automatic slot.
	for i := 0; i < MaxAutomaticRuns; i++ {
		r.slots <- struct{}{}
	}
	r.Dispatch(job("fake", TriggerAutomatic, "s", "d"))
	time.Sleep(100 * time.Millisecond)
	if f.calls.Load() != 0 {
		t.Fatal("an automatic run started past the cap")
	}
	if !r.Running("acme", "widgets", 42, "fake") {
		t.Error("a queued run should read as running, so clients show it pending")
	}

	// Someone asking for a feature doesn't wait behind the background.
	if got := r.RunSync(job("other", TriggerExplicit, "s", "d")); got != OutcomeStarted || g.calls.Load() != 1 {
		t.Errorf("explicit run blocked behind the cap: %q, %d calls", got, g.calls.Load())
	}

	for i := 0; i < MaxAutomaticRuns; i++ {
		<-r.slots
	}
	waitFor(t, func() bool { return f.calls.Load() == 1 })
}

func TestRunnerRecordsInputFailures(t *testing.T) {
	f := &fakeFeature{id: "fake"}
	r, db, crsHome := testRunner(t, nil, f)

	j := job("fake", TriggerExplicit, "sha-1", "d")
	j.Build = func(context.Context) (Request, error) { return Request{}, errors.New("GitHub is down") }
	r.RunSync(j)

	if f.calls.Load() != 0 {
		t.Error("the feature ran without its inputs")
	}
	stored, _, _ := db.GetAIResult("acme", "widgets", 42, "fake")
	if stored.Status != StatusError || stored.SHA != "sha-1" {
		t.Errorf("unexpected row: %+v", stored)
	}
	if log := callLog(t, crsHome); !strings.Contains(log, `FAILURE at stage "input"`) || !strings.Contains(log, "GitHub is down") {
		t.Errorf("call log should attribute the failure:\n%s", log)
	}
}

func TestRunnerSurvivesAPanickingFeature(t *testing.T) {
	f := &fakeFeature{id: "fake", run: func(context.Context, Request) (Result, error) { panic("boom") }}
	r, db, _ := testRunner(t, nil, f)

	r.RunSync(job("fake", TriggerExplicit, "s", "d"))
	if stored, _, _ := db.GetAIResult("acme", "widgets", 42, "fake"); stored.Status != StatusError ||
		!strings.Contains(stored.Result, "panicked") {
		t.Errorf("unexpected row: %+v", stored)
	}
}

func TestRunnerSurvivesAPanickingBuild(t *testing.T) {
	f := &fakeFeature{id: "fake"}
	r, db, _ := testRunner(t, nil, f)

	j := job("fake", TriggerExplicit, "s", "d")
	j.Build = func(context.Context) (Request, error) { panic("nil map") }
	if got := r.Dispatch(j); got != OutcomeStarted {
		t.Fatalf("Dispatch = %q", got)
	}
	waitFor(t, func() bool { return !r.Running("acme", "widgets", 42, "fake") })
	if stored, _, _ := db.GetAIResult("acme", "widgets", 42, "fake"); stored.Status != StatusError ||
		!strings.Contains(stored.Result, "panicked") {
		t.Errorf("unexpected row: %+v", stored)
	}
}

func TestRunnerLogsInsufficientInput(t *testing.T) {
	f := &fakeFeature{id: "fake", run: func(context.Context, Request) (Result, error) {
		return Result{Status: StatusInsufficientInput, Log: RunLog{
			Warnings: []string{"no comments are cached"},
			Parsed:   "verdict insufficient-input",
		}}, nil
	}}
	r, db, crsHome := testRunner(t, nil, f)

	r.RunSync(job("fake", TriggerAutomatic, "s", "d"))
	if stored, _, _ := db.GetAIResult("acme", "widgets", 42, "fake"); stored.Status != StatusInsufficientInput {
		t.Errorf("status = %q", stored.Status)
	}
	log := callLog(t, crsHome)
	for _, want := range []string{"Outcome:  INSUFFICIENT-INPUT", "Problem:  no comments are cached", "Parsed:   verdict insufficient-input"} {
		if !strings.Contains(log, want) {
			t.Errorf("call log missing %q:\n%s", want, log)
		}
	}
}

func TestRunnerBuildsTheConfiguredProviderLazily(t *testing.T) {
	quiet := &fakeFeature{id: "quiet"}
	chatty := &fakeFeature{id: "chatty", run: func(ctx context.Context, req Request) (Result, error) {
		if req.Agent == nil || req.Mode != config.AIModeAgent {
			t.Errorf("agent mode should hand the feature an agent: mode %q agent %v", req.Mode, req.Agent)
		}
		text, err := req.Model.Generate(ctx, "prompt")
		return Result{Body: Body{BodyContent: text}}, err
	}}
	r, db, crsHome := testRunner(t, []config.AIFeature{
		{ID: "quiet", Enabled: true},
		{ID: "chatty", Enabled: true, Mode: config.AIModeAgent, Command: "my-agent --fast"},
	}, quiet, chatty)

	var built []string
	r.NewProvider = func(provider, command string) (Provider, error) {
		built = append(built, provider+"|"+command)
		return &scriptedProvider{t: t, answers: []string{"hello"}}, nil
	}

	r.RunSync(job("quiet", TriggerExplicit, "s", "d"))
	if len(built) != 0 {
		t.Errorf("a run that never calls the model built a provider: %v", built)
	}

	r.RunSync(job("chatty", TriggerExplicit, "s", "d"))
	if len(built) != 1 || built[0] != "command|my-agent --fast" {
		t.Errorf("providers built = %v", built)
	}
	if stored, _, _ := db.GetAIResult("acme", "widgets", 42, "chatty"); DecodeDocument(stored.Result).Body.BodyContent != "hello" {
		t.Errorf("unexpected stored result: %+v", stored)
	}
	log := callLog(t, crsHome)
	if !strings.Contains(log, "LLM:      fake (fake-model)") || !strings.Contains(log, "1 model call(s)") {
		t.Errorf("call log should describe the model call:\n%s", log)
	}
}

func TestRunnerLogsAProviderThatCouldNotBeBuilt(t *testing.T) {
	f := &fakeFeature{id: "fake", run: func(ctx context.Context, req Request) (Result, error) {
		_, err := req.Model.Generate(ctx, "prompt")
		return Result{Log: RunLog{Warnings: []string{"model unavailable: " + err.Error()}}}, nil
	}}
	r, _, crsHome := testRunner(t, nil, f)
	r.NewProvider = func(string, string) (Provider, error) { return nil, errors.New("GEMINI_API_KEY not set") }

	r.RunSync(job("fake", TriggerExplicit, "s", "d"))
	log := callLog(t, crsHome)
	if !strings.Contains(log, "LLM:      gemini (unavailable: GEMINI_API_KEY not set)") || !strings.Contains(log, "Outcome:  PARTIAL") {
		t.Errorf("call log should say the provider was unavailable:\n%s", log)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
