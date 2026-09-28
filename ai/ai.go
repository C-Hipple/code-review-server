// Package ai is the server's AI feature layer. A feature is a registered unit
// of AI work on one PR — the comments-addressed report is the first — that a
// client runs on demand, or that runs automatically after a PR is fetched or
// updated. Results are stored per feature per PR in the AIResults table and
// keyed by the head SHA plus a digest of the PR's discussion (digest.go), so a
// result can always say whether it still describes the PR.
//
// The layer sits beside the two older AI paths rather than on top of them.
// Plugins (server/plugins.go) remain separate binaries with their own config,
// table and RPCs, and share only the subprocess helper with this package. The
// llm package's diff analysis keeps its own flags; this package reuses llm's
// Client for Gemini and its call log (~/.crs/llm_calls.log).
//
// Features reach a model through two seams: Provider for one-shot calls and
// Agent for multi-turn work with tool callbacks. Neither seam names a backend.
// Gemini and a command-backed provider — any CLI agent the user names — are
// peers, chosen per feature in config (config.AIProviderFor).
package ai

import (
	"context"
	"crs/git_tools"
	"encoding/json"
	"time"
)

// Feature is one unit of AI work on a PR.
type Feature interface {
	// ID is the stable identifier config and the RPCs use, e.g.
	// "comments-addressed".
	ID() string
	// Name is the label clients show.
	Name() string
	// Run does the work. An error means nothing usable was produced; a run that
	// completed without enough input to reach a verdict instead returns a Result
	// whose Status is StatusInsufficientInput.
	Run(ctx context.Context, req Request) (Result, error)
}

// Describer is implemented by features that describe themselves to clients.
type Describer interface {
	Description() string
}

// ModeSupporter is implemented by features that can run in more than the
// one-shot mode. The first mode listed is the feature's default.
type ModeSupporter interface {
	Modes() []string
}

// TimeoutProvider is implemented by features that need a deadline other than
// DefaultTimeout.
type TimeoutProvider interface {
	Timeout() time.Duration
}

// Statuses of a feature's result. Only the first three are ever stored; the
// others describe a PR's result in a reply.
const (
	StatusSuccess = "success"
	// StatusError means the run failed; the stored body says why.
	StatusError = "error"
	// StatusInsufficientInput is a completed run whose inputs were missing
	// something it needed to reach a verdict (see the result's body).
	StatusInsufficientInput = "insufficient-input"
	// StatusPending means a run is in flight for the PR right now.
	StatusPending = "pending"
	// StatusNotRun means the feature has never produced a result for the PR.
	StatusNotRun = "not-run"
)

// Trigger says why a feature is running. It is recorded in the call log, and
// decides whether a cached result may stand in for a run.
type Trigger string

const (
	// TriggerAutomatic is a run the server started on its own after a PR was
	// fetched or updated. It queues behind the automatic-run cap.
	TriggerAutomatic Trigger = "automatic"
	// TriggerExplicit is a client asking for the feature. A cached result for
	// the PR's current inputs answers it without a run.
	TriggerExplicit Trigger = "explicit"
	// TriggerRerun is a client forcing a fresh run whatever is cached.
	TriggerRerun Trigger = "rerun"
)

// Request is everything a feature may read about the PR it runs for.
type Request struct {
	Owner  string
	Repo   string // the short repo name, as the DB caches key it
	Number int

	// HeadSHA and Digest identify the inputs — the code and the discussion.
	// The stored result is keyed by both.
	HeadSHA string
	Digest  string

	// Diff, CommentsJSON and MetadataJSON are what plugins receive: the diff as
	// the server renders it, the raw PRComments cache entry (empty when nothing
	// is cached) and the PR metadata.
	Diff         string
	CommentsJSON string
	MetadataJSON string
	// ReviewThreads is GitHub's review-thread state from the PRReviewThreads
	// cache; nil when it is unknown.
	ReviewThreads []git_tools.ReviewThread
	// Discussion is the server's partition of the PR's comments into threads,
	// conversation and reviews (server/renderer.go).
	Discussion Discussion

	Trigger Trigger
	// Mode is the configured execution mode, config.AIModeOneShot or
	// config.AIModeAgent.
	Mode string

	// Model is the one-shot seam. It is never nil: when no provider can be
	// built, its calls fail at stage client-init, so a feature that can answer
	// without a model degrades instead of failing.
	Model Provider
	// Agent is the agentic seam, built on Model. Set only in agent mode.
	Agent Agent
	// ReadFile reads a file as of the PR head, for agent tools. Nil when the
	// server has no way to.
	ReadFile func(ctx context.Context, path string) (string, error)
}

// Discussion is a PR's conversation as features see it. The server builds it
// from the same caches GetPR renders, with GitHub's review-thread state
// already merged in; local (unsubmitted) comments are never part of it.
type Discussion struct {
	PRAuthor string
	// CommentsKnown is false when nothing is cached for the PR's comments, so an
	// empty Threads cannot be told apart from comments never fetched.
	CommentsKnown bool
	// ThreadsKnown is false when GitHub's review-thread state is unavailable,
	// in which case no thread's Resolved or Outdated flag means anything.
	ThreadsKnown bool
	Threads      []Thread
	// Conversation holds the top-level PR comments, oldest first.
	Conversation []Comment
	Reviews      []Review
	// LatestCommitAt is when the newest commit on the PR was authored; zero when
	// the commits are unknown.
	LatestCommitAt time.Time
}

// Comment is one PR comment.
type Comment struct {
	ID        string
	Author    string
	Body      string
	CreatedAt time.Time
	HTMLURL   string
}

// Thread is one review-comment thread.
type Thread struct {
	// RootID is the ID of the comment that opened the thread.
	RootID string
	// ThreadID is GitHub's node ID for the thread. Empty when GitHub reported no
	// state for it, in which case Resolved and ResolvedBy mean nothing.
	ThreadID   string
	Resolved   bool
	ResolvedBy string
	Outdated   bool
	Path       string
	// Line is the thread's line in the head version of Path; 0 when it has none
	// there (the thread is outdated, or sits on a removed line).
	Line int
	// Comments are the thread's comments in posting order, root first.
	Comments []Comment
}

// Review is one submitted review.
type Review struct {
	ID          int64
	User        string
	State       string // APPROVED, CHANGES_REQUESTED, COMMENTED or DISMISSED
	Body        string
	SubmittedAt time.Time
	HTMLURL     string
}

// Result is what a feature produced. Body and Annotations follow the plugin
// response contract (docs/plugins.md), so clients render them with code they
// already have; Report and Outstanding carry the feature's typed payload.
type Result struct {
	// Status is StatusSuccess or StatusInsufficientInput; empty means success.
	Status      string
	Body        Body
	Annotations []Annotation
	// Report is the feature's typed report, stored and served as JSON.
	Report any
	// Outstanding lists what still needs attention, one entry per item, so
	// clients can show every item — including ones no diff line can anchor.
	Outstanding any
	// Truncated is set when some input did not fit what the model was sent.
	Truncated bool
	// Log is what the call log should say about the run.
	Log RunLog
}

// Body is the renderable part of a result, as in the plugin contract.
type Body struct {
	BodyType    string `json:"body_type"`
	BodyContent string `json:"body_content"`
}

// Body types, as in the plugin contract.
const (
	BodyMarkdown = "markdown"
	BodyHTML     = "html"
)

// Annotation anchors a remark to a line of a file in the head revision, as in
// the plugin contract.
type Annotation struct {
	Filename string `json:"filename"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Content  string `json:"content"`
}

// RunLog is a feature's contribution to its call log entry.
type RunLog struct {
	// Input says what the feature worked from, in one line.
	Input string
	// Parsed summarizes the outcome, in one line.
	Parsed string
	// Warnings are non-fatal problems, e.g. a model answer that didn't parse.
	Warnings []string
	// ResponseSnippet is the head of a model response that had problems.
	ResponseSnippet string
}

// Document is a result as stored in AIResults and served to clients: the
// plugin response contract plus the AI-only fields, so the server can parse
// its body and annotations with the plugin contract parser.
type Document struct {
	Body        Body            `json:"body"`
	Annotations []Annotation    `json:"annotations"`
	Report      json.RawMessage `json:"report,omitempty"`
	Outstanding json.RawMessage `json:"outstanding,omitempty"`
	Truncated   bool            `json:"truncated"`
}

// Encode serializes a result into its stored form.
func (r Result) Encode() (string, error) {
	doc := Document{
		Body:        r.Body,
		Annotations: r.Annotations,
		Truncated:   r.Truncated,
	}
	if doc.Body.BodyType == "" {
		doc.Body.BodyType = BodyMarkdown
	}
	if doc.Annotations == nil {
		doc.Annotations = []Annotation{}
	}
	var err error
	if doc.Report, err = marshalOptional(r.Report); err != nil {
		return "", err
	}
	if doc.Outstanding, err = marshalOptional(r.Outstanding); err != nil {
		return "", err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DecodeDocument parses a stored result. A malformed one yields an empty
// document rather than an error, since a reply should still go out.
func DecodeDocument(stored string) Document {
	var doc Document
	if stored == "" || json.Unmarshal([]byte(stored), &doc) != nil {
		return Document{Body: Body{BodyType: BodyMarkdown}, Annotations: []Annotation{}}
	}
	if doc.Annotations == nil {
		doc.Annotations = []Annotation{}
	}
	return doc
}

func marshalOptional(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(data) == "null" {
		return nil, nil
	}
	return data, nil
}
