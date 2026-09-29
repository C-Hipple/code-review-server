package ai

import (
	"context"
	"crs/config"
	"crs/llm"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// DefaultTimeout bounds one feature run, model calls included, unless the
// feature asks for another (TimeoutProvider). It matches the plugin timeout.
const DefaultTimeout = 5 * time.Minute

// MaxAutomaticRuns caps how many automatic runs execute at once, across every
// feature. Automatic runs are fan-out work — a workflow cycle can update many
// PRs at once — and each may cost a model call, so they queue; a run a client
// asked for never waits behind them.
const MaxAutomaticRuns = 2

// Stages the runner attributes failures to, beside llm's call stages.
const (
	// StageInput means the PR's inputs could not be assembled.
	StageInput = "input"
	// StageFeature means the feature itself failed outside any model call.
	StageFeature = "feature"
)

// Runner runs features for PRs: it decides whether a stored result already
// answers, keeps one run per PR and feature in flight, stores what a run
// produced and writes its call log entry.
type Runner struct {
	registry *Registry
	// NewProvider builds the provider a run's config chose. Tests replace it.
	NewProvider func(choice config.AIProviderChoice) (Provider, error)

	slots    chan struct{}
	inflight sync.Map // runKey -> struct{}
}

func NewRunner(registry *Registry) *Runner {
	return &Runner{
		registry:    registry,
		NewProvider: NewProvider,
		slots:       make(chan struct{}, MaxAutomaticRuns),
	}
}

// Registry is the set of features the runner runs.
func (r *Runner) Registry() *Registry { return r.registry }

// DefaultRunner runs the built-in features. The server's RPCs and post-update
// hooks share it, so they share its in-flight tracking and automatic-run cap.
var DefaultRunner = NewRunner(DefaultRegistry)

// Job asks for one feature to run for one PR.
type Job struct {
	Owner   string
	Repo    string
	Number  int
	Feature string
	Trigger Trigger
	// SHA and Digest are the PR's current inputs as the DB caches have them,
	// read up front so deciding whether to run needs no GitHub round trip.
	// Digest is the discussion's; the runner swaps in CodeOnlyDigest for a
	// code-only feature.
	// The stored result is keyed by the Request that Build returns, which may
	// have refreshed a cache on the way.
	SHA    string
	Digest string
	// Build assembles the full request. It runs on the job's goroutine, once the
	// run has a slot, and may reach GitHub on a cache miss.
	Build func(ctx context.Context) (Request, error)
}

// Outcome says what the runner did with a job.
type Outcome string

const (
	// OutcomeStarted means a run was started (or, for RunSync, completed).
	OutcomeStarted Outcome = "started"
	// OutcomeUpToDate means the stored result already covers the job's inputs.
	OutcomeUpToDate Outcome = "up-to-date"
	// OutcomeAlreadyRunning means a run for the same PR and feature is in
	// flight; its result lands where this one's would have.
	OutcomeAlreadyRunning Outcome = "already-running"
	// OutcomeDisabled means config does not enable the feature — or, for an
	// automatic job, does not enable it to run automatically.
	OutcomeDisabled Outcome = "disabled"
	// OutcomeUnknown means no feature is registered under the job's ID.
	OutcomeUnknown Outcome = "unknown-feature"
)

// claim is a job the runner accepted, with the config it runs under.
type claim struct {
	job      Job
	feature  Feature
	mode     string
	provider config.AIProviderChoice
}

// Dispatch starts job on its own goroutine and returns at once, unless it
// needn't run at all (see Outcome).
func (r *Runner) Dispatch(job Job) Outcome {
	c, outcome := r.claim(job)
	if outcome != OutcomeStarted {
		return outcome
	}
	go r.execute(c)
	return OutcomeStarted
}

// RunSync is Dispatch without the goroutine: when it starts a run, it returns
// once the result is stored.
func (r *Runner) RunSync(job Job) Outcome {
	c, outcome := r.claim(job)
	if outcome != OutcomeStarted {
		return outcome
	}
	r.execute(c)
	return OutcomeStarted
}

// Running reports whether a run of feature is in flight for the PR — queued
// for an automatic slot included.
func (r *Runner) Running(owner, repo string, number int, feature string) bool {
	_, ok := r.inflight.Load(runKey(owner, repo, number, feature))
	return ok
}

func runKey(owner, repo string, number int, feature string) string {
	return fmt.Sprintf("%s/%s#%d:%s", owner, repo, number, feature)
}

func (r *Runner) claim(job Job) (claim, Outcome) {
	f, ok := r.registry.Get(job.Feature)
	if !ok {
		return claim{}, OutcomeUnknown
	}
	cfg := config.C()
	entry, _ := cfg.AIFeatureSettings(job.Feature)
	if !entry.Enabled || (job.Trigger == TriggerAutomatic && !entry.Automatic) {
		return claim{}, OutcomeDisabled
	}
	job.Digest = KeyDigest(f, job.Digest)
	if job.Trigger != TriggerRerun && storedCovers(cfg, job) {
		return claim{}, OutcomeUpToDate
	}
	if _, running := r.inflight.LoadOrStore(runKey(job.Owner, job.Repo, job.Number, job.Feature), struct{}{}); running {
		return claim{}, OutcomeAlreadyRunning
	}
	return claim{job: job, feature: f, mode: resolveMode(f, entry), provider: cfg.AIProviderFor(entry)}, OutcomeStarted
}

// storedCovers reports whether the stored result was computed from exactly
// the job's inputs — the same head SHA and the same discussion digest.
func storedCovers(cfg config.Config, job Job) bool {
	if cfg.DB == nil || job.SHA == "" {
		return false
	}
	stored, ok, err := cfg.DB.GetAIResult(job.Owner, job.Repo, job.Number, job.Feature)
	if err != nil || !ok {
		return false
	}
	if stored.SHA != job.SHA || stored.InputHash != job.Digest {
		return false
	}
	// A failed run is retried when someone asks for the feature. The automatic
	// path leaves it until the inputs change, so an outage isn't retried on
	// every page load.
	return stored.Status != StatusError || job.Trigger == TriggerAutomatic
}

func (r *Runner) execute(c claim) {
	job := c.job
	key := runKey(job.Owner, job.Repo, job.Number, job.Feature)
	defer r.inflight.Delete(key)
	if job.Trigger == TriggerAutomatic {
		r.slots <- struct{}{}
		defer func() { <-r.slots }()
	}

	timeout := DefaultTimeout
	if tp, ok := c.feature.(TimeoutProvider); ok && tp.Timeout() > 0 {
		timeout = tp.Timeout()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	model := &lazyProvider{build: func() (Provider, error) { return r.NewProvider(c.provider) }}
	sha, digest := job.SHA, job.Digest

	var result Result
	req, err := buildRequest(ctx, job)
	if err != nil {
		err = &llm.CallError{Stage: StageInput, Err: fmt.Errorf("assembling the PR's inputs: %w", err)}
	} else {
		req.Digest = KeyDigest(c.feature, req.Digest)
		sha, digest = req.HeadSHA, req.Digest
		req.Trigger = job.Trigger
		req.Mode = c.mode
		req.Model = model
		if c.mode == config.AIModeAgent {
			req.Agent = NewProviderAgent(model)
		}
		result, err = runFeature(ctx, c.feature, req)
	}
	duration := time.Since(start)

	runLog := result.Log
	status := result.Status
	if status == "" {
		status = StatusSuccess
	}
	if err != nil {
		status = StatusError
		result = Result{Body: Body{
			BodyType:    BodyMarkdown,
			BodyContent: fmt.Sprintf("**%s failed.**\n\n%v", c.feature.Name(), err),
		}}
	}
	store(job, status, result, sha, digest)
	// The result is readable from here on; don't keep reporting it as pending
	// while the log entry is written.
	r.inflight.Delete(key)

	entry := llm.CallReport{
		Title:           "AI Feature Run",
		Time:            start,
		Repo:            job.Repo,
		PRNumber:        job.Number,
		SHA:             sha,
		Trigger:         string(job.Trigger),
		Purpose:         fmt.Sprintf("ai:%s (%s)", c.feature.ID(), c.mode),
		Provider:        model.Name(),
		Model:           model.Model(),
		Input:           runLog.Input,
		Duration:        duration,
		ResponseBytes:   model.responseBytes,
		Warnings:        runLog.Warnings,
		Parsed:          runLog.Parsed,
		ResponseSnippet: runLog.ResponseSnippet,
	}
	if model.built && model.buildErr != nil {
		entry.Provider, entry.Model = c.provider.Provider, "unavailable: "+model.buildErr.Error()
	}
	if model.calls == 0 {
		entry.Call = fmt.Sprintf("no model call, took %s", duration.Round(time.Millisecond))
	} else {
		entry.Call = fmt.Sprintf("%d model call(s), took %s, %d-byte response(s)",
			model.calls, duration.Round(time.Millisecond), model.responseBytes)
	}
	switch {
	case err != nil:
		entry.Stage, entry.Err = StageFeature, err
		var callErr *llm.CallError
		if errors.As(err, &callErr) {
			entry.Stage = callErr.Stage
		}
	case status == StatusInsufficientInput:
		entry.Status = "INSUFFICIENT-INPUT"
	}
	llm.AppendCallReport(entry)

	slog.Info("AI feature run finished", "feature", c.feature.ID(), "repo", job.Repo, "pr", job.Number,
		"trigger", job.Trigger, "status", status, "model_calls", model.calls, "duration_ms", duration.Milliseconds())
}

// buildRequest runs the job's Build, turning a panic into an error: it runs on
// a background goroutine, where an unrecovered panic would end the server.
func buildRequest(ctx context.Context, job Job) (req Request, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panicked: %v", p)
		}
	}()
	return job.Build(ctx)
}

// runFeature runs f, turning a panic into an error so one misbehaving feature
// can't take the server down with it.
func runFeature(ctx context.Context, f Feature, req Request) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%s panicked: %v", f.ID(), p)
		}
	}()
	return f.Run(ctx, req)
}

// store writes a run's result, keyed by the inputs it was computed from.
func store(job Job, status string, result Result, sha, digest string) {
	db := config.C().DB
	if db == nil {
		return
	}
	encoded, err := result.Encode()
	if err != nil {
		slog.Error("Failed to encode AI feature result", "feature", job.Feature, "error", err)
		status = StatusError
		encoded, _ = Result{Body: Body{BodyType: BodyMarkdown,
			BodyContent: fmt.Sprintf("The result could not be encoded: %v", err)}}.Encode()
	}
	if err := db.UpsertAIResult(job.Owner, job.Repo, job.Number, job.Feature, encoded, status, sha, digest); err != nil {
		slog.Error("Failed to store AI feature result", "feature", job.Feature, "repo", job.Repo, "pr", job.Number, "error", err)
	}
}
