package ai

import (
	"context"
	"crs/config"
	"crs/llm"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CommentsAddressedID is the ID of the comments-addressed feature.
const CommentsAddressedID = "comments-addressed"

// CommentsAddressed answers "are all the review comments addressed, and what
// is still outstanding?" for a PR.
//
// It is deterministic first. GitHub's own thread state decides whatever it
// can: a resolved thread is addressed, and an unresolved one that somebody
// replied to after the latest commit is outstanding — the discussion is about
// the code as it is now. The model is consulted only for what those rules
// leave genuinely unclear: an unresolved thread nobody has replied to since
// commits landed (did they fix it?), and top-level conversation comments,
// which GitHub tracks no resolution for at all.
//
// The design goal is never to report a confidently wrong "all addressed":
//
//   - A resolved thread always stays addressed. The model is never asked about
//     one, and if it volunteers a verdict anyway it is kept as a note only.
//   - A thread GitHub reported no state for stays unclear; the model is not
//     asked to guess at state GitHub didn't give.
//   - A model verdict is discarded (back to unclear) for any item whose
//     evidence was cut to fit the prompt.
//   - Missing inputs make the whole run insufficient-input rather than empty.
//   - A reviewer whose latest review still requests changes keeps the verdict
//     at outstanding even when every thread is resolved.
//
// Every item records its source — "github" or "model" — so a reader can see
// which statuses are GitHub's and which are the model's judgment.
type CommentsAddressed struct{}

func (CommentsAddressed) ID() string   { return CommentsAddressedID }
func (CommentsAddressed) Name() string { return "Comments addressed?" }

func (CommentsAddressed) Description() string {
	return "Reports which review comments are still outstanding. GitHub's thread resolution decides " +
		"first; the model only judges unresolved threads nobody replied to since the latest commit, " +
		"and conversation comments."
}

func (CommentsAddressed) Modes() []string {
	return []string{config.AIModeOneShot, config.AIModeAgent}
}

// Item statuses, sources and kinds in a comments-addressed report.
const (
	ItemAddressed   = "addressed"
	ItemOutstanding = "outstanding"
	ItemUnclear     = "unclear"

	SourceGitHub = "github"
	SourceModel  = "model"

	KindThread       = "thread"
	KindConversation = "conversation"
)

// Verdicts: the report's one-word answer.
const (
	VerdictAllAddressed      = "all-addressed"
	VerdictOutstanding       = "outstanding"
	VerdictUnclear           = "unclear"
	VerdictNoComments        = "no-comments"
	VerdictInsufficientInput = "insufficient-input"
)

// Prompt budgets. Anything cut to fit them marks the report truncated, and a
// model verdict that depended on the cut part is not trusted.
const (
	maxModelItems      = 40
	maxCommentChars    = 2000
	maxConversationCtx = 40000
	maxPromptDiffBytes = 150000
	maxRationaleChars  = 400
	excerptChars       = 200
	agentMaxTurns      = 6
)

// CommentsReport is the typed report comments-addressed stores as the
// result's "report".
type CommentsReport struct {
	Verdict string `json:"verdict"`
	// Summary is the verdict in one sentence.
	Summary string       `json:"summary"`
	Counts  ReportCounts `json:"counts"`
	// Items is every thread and conversation comment the report judged.
	Items []ReportItem `json:"items"`
	// ChangeRequests lists reviewers whose latest review still requests
	// changes. They are GitHub's state, never the model's.
	ChangeRequests []ChangeRequest `json:"change_requests"`
	Model          ModelUse        `json:"model"`
	// Truncated is set when something was cut to fit the model's prompt.
	Truncated bool `json:"truncated"`
	// Missing names the inputs that were unavailable, when the verdict is
	// insufficient-input or a thread's state is unknown.
	Missing []string `json:"missing"`
}

// ReportCounts tallies the items by status.
type ReportCounts struct {
	Total       int `json:"total"`
	Addressed   int `json:"addressed"`
	Outstanding int `json:"outstanding"`
	Unclear     int `json:"unclear"`
	// ByModel counts the items whose status the model decided.
	ByModel int `json:"by_model"`
}

// ReportItem is the verdict on one review thread or conversation comment.
type ReportItem struct {
	// RootCommentID is the comment that opened the thread, or the conversation
	// comment itself. It matches CommentJSON.ID in the GetPR payload.
	RootCommentID string `json:"root_comment_id"`
	// ThreadID is GitHub's node ID for the thread; null for conversation
	// comments and for threads GitHub reported no state for.
	ThreadID  *string `json:"thread_id"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status"`
	Source    string  `json:"source"`
	Rationale string  `json:"rationale"`
	// ModelNote is what the model said about an item it was not allowed to
	// decide. The status stands regardless.
	ModelNote    string    `json:"model_note,omitempty"`
	Author       string    `json:"author"`
	Excerpt      string    `json:"excerpt"`
	Path         string    `json:"path,omitempty"`
	Line         int       `json:"line,omitempty"`
	Outdated     bool      `json:"outdated"`
	Resolved     bool      `json:"resolved"`
	ResolvedBy   string    `json:"resolved_by,omitempty"`
	Replies      int       `json:"replies"`
	LastAuthor   string    `json:"last_author"`
	LastActivity time.Time `json:"last_activity"`
	HTMLURL      string    `json:"html_url,omitempty"`
	// CodeContext is the code a thread was left on: the last lines of GitHub's
	// diff hunk, ending at the commented line. Set only on threads that are not
	// addressed, since those are the ones a reader still has to look at.
	CodeContext string `json:"code_context,omitempty"`
}

// ChangeRequest is a reviewer whose latest decisive review requests changes.
type ChangeRequest struct {
	Reviewer    string    `json:"reviewer"`
	ReviewID    int64     `json:"review_id"`
	SubmittedAt time.Time `json:"submitted_at"`
	Excerpt     string    `json:"excerpt"`
	HTMLURL     string    `json:"html_url,omitempty"`
}

// ModelUse says whether and how the model was consulted.
type ModelUse struct {
	Consulted bool   `json:"consulted"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Mode      string `json:"mode"`
	// Asked counts the items sent to the model for a verdict.
	Asked int `json:"asked"`
	// Turns and ToolCalls describe an agent-mode run.
	Turns     int `json:"turns,omitempty"`
	ToolCalls int `json:"tool_calls,omitempty"`
	// Note explains a model that was needed but not (usefully) consulted.
	Note string `json:"note,omitempty"`
}

// workItem is a report item plus what the model step needs to know about it.
type workItem struct {
	item    ReportItem
	thread  *Thread
	comment *Comment
	// askModel marks the items the deterministic rules left for the model.
	askModel bool
	// asked is set once the item actually went into the prompt.
	asked bool
	// complete is false when part of the item's evidence was cut from the
	// prompt, so a model verdict on it can't be trusted.
	complete bool
	created  time.Time
}

func (c CommentsAddressed) Run(ctx context.Context, req Request) (Result, error) {
	d := req.Discussion
	report := CommentsReport{
		Items:          []ReportItem{},
		ChangeRequests: activeChangeRequests(d.Reviews, d.PRAuthor),
		Model:          ModelUse{Mode: req.Mode},
		Missing:        []string{},
	}
	var runLog RunLog

	if !d.CommentsKnown {
		report.Missing = append(report.Missing,
			"no comments are cached for this PR, so it can't be told whether any are waiting on the author")
	}
	items := classify(d)
	if len(d.Threads) > 0 && !d.ThreadsKnown {
		report.Missing = append(report.Missing,
			"GitHub's review-thread resolution state is unavailable, so no thread can be called resolved")
	}
	insufficient := len(report.Missing) > 0

	var toAsk []*workItem
	for _, w := range items {
		if !w.askModel {
			continue
		}
		if len(toAsk) == maxModelItems {
			report.Truncated = true
			w.item.Rationale += fmt.Sprintf(" It was not sent to the model: more than %d items needed a judgment.", maxModelItems)
			continue
		}
		toAsk = append(toAsk, w)
	}
	switch {
	case len(toAsk) == 0:
	case insufficient:
		// No verdict can stand on missing input, so a model call would buy
		// nothing but its cost. A rerun once the input arrives will ask.
		report.Model.Note = "The model was not consulted: input the report depends on is missing."
	default:
		c.consultModel(ctx, req, items, toAsk, &report, &runLog)
	}

	return finishReport(req, items, report, insufficient, runLog), nil
}

// classify applies the deterministic rules to every thread and conversation
// comment.
func classify(d Discussion) []*workItem {
	var items []*workItem
	for i := range d.Threads {
		t := &d.Threads[i]
		if len(t.Comments) == 0 {
			continue
		}
		items = append(items, classifyThread(t, d))
	}
	for i := range d.Conversation {
		cm := &d.Conversation[i]
		// The PR author's own comments are replies and updates, not requests to
		// the author; bots post status, not review feedback. Both still reach the
		// model as context for the comments that are judged.
		if (d.PRAuthor != "" && cm.Author == d.PRAuthor) || isBot(cm.Author) {
			continue
		}
		items = append(items, &workItem{
			item: ReportItem{
				RootCommentID: cm.ID,
				Kind:          KindConversation,
				Status:        ItemUnclear,
				Source:        SourceGitHub,
				Rationale:     "A conversation comment: GitHub tracks no resolution for these.",
				Author:        cm.Author,
				Excerpt:       excerpt(cm.Body),
				LastAuthor:    cm.Author,
				LastActivity:  cm.CreatedAt,
				HTMLURL:       cm.HTMLURL,
			},
			comment:  cm,
			askModel: true,
			complete: true,
			created:  cm.CreatedAt,
		})
	}
	return items
}

// classifyThread decides a review thread from GitHub's state where it can.
func classifyThread(t *Thread, d Discussion) *workItem {
	root := t.Comments[0]
	last := t.Comments[len(t.Comments)-1]
	w := &workItem{
		item: ReportItem{
			RootCommentID: t.RootID,
			Kind:          KindThread,
			Source:        SourceGitHub,
			Author:        root.Author,
			Excerpt:       excerpt(root.Body),
			Path:          t.Path,
			Line:          t.Line,
			Outdated:      t.Outdated,
			Resolved:      t.Resolved,
			ResolvedBy:    t.ResolvedBy,
			Replies:       len(t.Comments) - 1,
			LastAuthor:    last.Author,
			LastActivity:  last.CreatedAt,
			HTMLURL:       root.HTMLURL,
		},
		thread:   t,
		complete: true,
		created:  root.CreatedAt,
	}
	if t.ThreadID != "" {
		id := t.ThreadID
		w.item.ThreadID = &id
	}

	switch {
	case t.ThreadID == "":
		// No state from GitHub: not resolved as far as we know, but not known
		// to be unresolved either. Only GitHub can answer that.
		w.item.Status = ItemUnclear
		if d.ThreadsKnown {
			w.item.Rationale = "GitHub reported no resolution state for this thread; it may be newer than the cached thread state."
		} else {
			w.item.Rationale = "GitHub's review-thread state is unavailable, so whether this thread was resolved is unknown."
		}
	case t.Resolved:
		w.item.Status = ItemAddressed
		if t.ResolvedBy != "" {
			w.item.Rationale = fmt.Sprintf("Resolved on GitHub by %s.", t.ResolvedBy)
		} else {
			w.item.Rationale = "Resolved on GitHub."
		}
	case !d.LatestCommitAt.IsZero() && last.CreatedAt.After(d.LatestCommitAt):
		w.item.Status = ItemOutstanding
		who := last.Author
		if who == d.PRAuthor && who != "" {
			who += " (the author)"
		}
		w.item.Rationale = fmt.Sprintf("Unresolved on GitHub, and %s replied after the latest commit.", who)
	default:
		w.item.Status = ItemUnclear
		w.askModel = true
		if d.LatestCommitAt.IsZero() {
			w.item.Rationale = "Unresolved on GitHub; the PR's commits aren't known, so whether code changed since the last reply can't be told."
		} else {
			w.item.Rationale = "Unresolved on GitHub, with no reply since the latest commit: those commits may have addressed it."
		}
	}
	return w
}

// activeChangeRequests lists the reviewers whose latest decisive review —
// approve, request changes or dismissal; plain comments don't change a
// reviewer's standing on GitHub — still requests changes.
func activeChangeRequests(reviews []Review, prAuthor string) []ChangeRequest {
	sorted := slices.Clone(reviews)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SubmittedAt.Before(sorted[j].SubmittedAt) })
	latest := map[string]Review{}
	var order []string
	for _, r := range sorted {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		default:
			continue
		}
		if r.User == "" || r.User == prAuthor {
			continue
		}
		if _, seen := latest[r.User]; !seen {
			order = append(order, r.User)
		}
		latest[r.User] = r
	}
	out := []ChangeRequest{}
	for _, user := range order {
		r := latest[user]
		if r.State != "CHANGES_REQUESTED" {
			continue
		}
		out = append(out, ChangeRequest{
			Reviewer:    r.User,
			ReviewID:    r.ID,
			SubmittedAt: r.SubmittedAt,
			Excerpt:     excerpt(r.Body),
			HTMLURL:     r.HTMLURL,
		})
	}
	return out
}

// consultModel asks the model about toAsk and merges its verdicts into items.
// A model that can't be reached, or answers in a shape that can't be read,
// leaves the items unclear and says why; it never fails the run.
func (c CommentsAddressed) consultModel(ctx context.Context, req Request, items, toAsk []*workItem,
	report *CommentsReport, runLog *RunLog) {
	prompt, promptTruncated, readable := buildCommentsPrompt(req, toAsk)
	if promptTruncated {
		report.Truncated = true
	}
	for _, w := range toAsk {
		w.asked = true
	}
	report.Model.Asked = len(toAsk)

	var answer string
	var err error
	if req.Mode == config.AIModeAgent && req.Agent != nil {
		var tools []Tool
		if req.ReadFile != nil {
			tools = append(tools, readFileTool(req.ReadFile))
		}
		var res AgentResult
		res, err = req.Agent.Run(ctx, AgentTask{Prompt: prompt, Tools: tools, MaxTurns: agentMaxTurns})
		answer = res.Answer
		// A file the agent read in full counts as seen, even if the diff for it
		// was cut from the prompt.
		for _, call := range res.ToolCalls {
			var args struct {
				Path string `json:"path"`
			}
			if call.Name == "read_file" && call.Err == "" && json.Unmarshal(call.Args, &args) == nil &&
				!strings.HasSuffix(call.Result, "... (truncated)") {
				readable[args.Path] = true
			}
		}
		report.Model.Turns = res.Turns
		report.Model.ToolCalls = len(res.ToolCalls)
	} else {
		answer, err = req.Model.Generate(ctx, prompt)
	}
	if req.Model != nil {
		report.Model.Provider = req.Model.Name()
		report.Model.Model = req.Model.Model()
	}
	if err != nil {
		// The note is shown to the reader, so it gets the gist; the call log
		// keeps the whole error (an HTTP failure carries the response body).
		report.Model.Note = fmt.Sprintf("The model could not be consulted, so %d item(s) stay unclear: %s", len(toAsk), briefError(err))
		runLog.Warnings = append(runLog.Warnings, fmt.Sprintf("model unavailable: %v", err))
		return
	}
	report.Model.Consulted = true

	verdicts, perr := parseVerdicts(answer)
	if perr != nil {
		report.Model.Note = "The model's answer could not be read, so the items it was asked about stay unclear."
		runLog.Warnings = append(runLog.Warnings, fmt.Sprintf("model answer unreadable: %v", perr))
		runLog.ResponseSnippet = llm.Snippet(answer)
		return
	}

	byID := make(map[string]*workItem, len(items))
	for _, w := range items {
		byID[w.item.RootCommentID] = w
	}
	unknown := 0
	answered := map[string]bool{}
	for _, v := range verdicts {
		w, ok := byID[v.ID]
		if !ok {
			unknown++
			continue
		}
		if !w.asked {
			// Resolved on GitHub, or otherwise settled without the model: its
			// opinion is kept as a note and changes nothing.
			if v.Rationale != "" {
				w.item.ModelNote = fmt.Sprintf("The model called this %s: %s", v.Status, v.Rationale)
			}
			continue
		}
		answered[v.ID] = true
		status := v.Status
		rationale := v.Rationale
		if rationale == "" {
			rationale = "The model gave no reason."
		}
		if status != ItemUnclear && !evidenceComplete(w, readable) {
			rationale = fmt.Sprintf("The model called this %s, but it could not see all of the relevant input (cut to fit its prompt, or unavailable), so the verdict isn't trusted. Its reason: %s", status, rationale)
			status = ItemUnclear
		}
		w.item.Status = status
		w.item.Source = SourceModel
		w.item.Rationale = rationale
	}

	missed := 0
	for _, w := range toAsk {
		if !answered[w.item.RootCommentID] {
			missed++
			w.item.Rationale += " The model gave no verdict for it."
		}
	}
	if missed > 0 {
		runLog.Warnings = append(runLog.Warnings, fmt.Sprintf("model gave no verdict for %d of %d item(s)", missed, len(toAsk)))
		runLog.ResponseSnippet = llm.Snippet(answer)
	}
	if unknown > 0 {
		runLog.Warnings = append(runLog.Warnings, fmt.Sprintf("model answered for %d item(s) it wasn't asked about", unknown))
	}
}

// evidenceComplete reports whether everything the model needed to judge w
// made it into the prompt: the item's own text, plus the diff of its file for
// a thread, or the whole diff for a conversation comment (which can be
// addressed anywhere).
func evidenceComplete(w *workItem, readable map[string]bool) bool {
	if !w.complete {
		return false
	}
	if w.item.Kind == KindThread && w.item.Path != "" {
		return readable[w.item.Path]
	}
	return readable[wholeDiffKey]
}

// wholeDiffKey marks, in the readable set, that every file's diff made it in.
const wholeDiffKey = "\x00whole-diff"

// buildCommentsPrompt renders the model prompt for the items in toAsk. It
// reports whether anything was cut to fit, and returns the set of files whose
// diff went in whole (plus wholeDiffKey when all of them did).
func buildCommentsPrompt(req Request, toAsk []*workItem) (string, bool, map[string]bool) {
	var b strings.Builder
	truncated := false

	b.WriteString(`You are helping a code reviewer check whether the review comments on a pull request have been addressed.

For each item below, decide from the diff and the discussion whether what it asks for has been done in the code as it now stands:
- "addressed": the diff shows the requested change was made, or the item asks for nothing (praise, an FYI, a question that has been answered).
- "outstanding": the requested change is not in the diff, or the discussion shows it is still pending.
- "unclear": you cannot tell from what you were given.
Judge only the items listed. Keep each rationale to one sentence that names the evidence.
`)
	title, author := prTitleAuthor(req)
	if title != "" {
		b.WriteString(fmt.Sprintf("\nPull request: %q by %s\n", title, author))
	}

	b.WriteString("\n## Items\n")
	hasConversation := false
	for _, w := range toAsk {
		switch w.item.Kind {
		case KindThread:
			where := w.item.Path
			if w.item.Line > 0 {
				where = fmt.Sprintf("%s:%d", where, w.item.Line)
			}
			b.WriteString(fmt.Sprintf("\n### Item %s: review thread on %s", w.item.RootCommentID, where))
			if w.item.Outdated {
				b.WriteString(" (GitHub marks it outdated: the lines it was left on have changed)")
			}
			b.WriteString("\n")
			for _, cm := range w.thread.Comments {
				body, cut := clip(plainText(cm.Body), maxCommentChars)
				if cut {
					w.complete = false
					truncated = true
				}
				b.WriteString(fmt.Sprintf("%s (%s): %s\n", cm.Author, formatTime(cm.CreatedAt), body))
			}
		case KindConversation:
			hasConversation = true
			body, cut := clip(plainText(w.comment.Body), maxCommentChars)
			if cut {
				w.complete = false
				truncated = true
			}
			b.WriteString(fmt.Sprintf("\n### Item %s: conversation comment\n%s (%s): %s\n",
				w.item.RootCommentID, w.comment.Author, formatTime(w.comment.CreatedAt), body))
		}
	}

	if hasConversation {
		b.WriteString("\n## Whole PR conversation, oldest first (context for the conversation items)\n")
		used := 0
		contextCut := false
		for _, cm := range req.Discussion.Conversation {
			if isBot(cm.Author) {
				continue
			}
			body, cut := clip(plainText(cm.Body), maxCommentChars)
			line := fmt.Sprintf("- %s (%s): %s\n", cm.Author, formatTime(cm.CreatedAt), body)
			if used+len(line) > maxConversationCtx {
				b.WriteString("- ... (the rest of the conversation was omitted to fit)\n")
				contextCut = true
				break
			}
			contextCut = contextCut || cut
			used += len(line)
			b.WriteString(line)
		}
		// The part that was cut may hold the reply that settles an item, so no
		// verdict on a conversation item can be trusted.
		if contextCut {
			truncated = true
			for _, w := range toAsk {
				if w.item.Kind == KindConversation {
					w.complete = false
				}
			}
		}
	}

	diffText, readable, diffCut := budgetDiff(req.Diff, toAsk, maxPromptDiffBytes)
	if diffCut {
		truncated = true
	}
	b.WriteString("\n## Diff\n")
	b.WriteString(diffText)

	b.WriteString(`
Respond with only a JSON object — no prose, no code fence — with one entry per item above:
{"items": [{"id": "<item id>", "status": "addressed" | "outstanding" | "unclear", "rationale": "<one sentence>"}]}
`)
	return b.String(), truncated, readable
}

// prTitleAuthor reads the PR's title and author from the metadata JSON.
func prTitleAuthor(req Request) (string, string) {
	var meta struct {
		Title  string `json:"title"`
		Author string `json:"author"`
	}
	_ = json.Unmarshal([]byte(req.MetadataJSON), &meta)
	if meta.Author == "" {
		meta.Author = req.Discussion.PRAuthor
	}
	return meta.Title, meta.Author
}

// diffFile is one file's section of a unified diff.
type diffFile struct {
	path string
	text string
}

// splitDiffFiles cuts a unified diff into per-file sections at each
// "diff --git" line. A file's path comes from its "+++ b/" header line, or
// from the "diff --git" line when there is none (a deleted file).
func splitDiffFiles(diff string) []diffFile {
	var files []diffFile
	var cur *diffFile
	var b strings.Builder
	inHeader := false
	flush := func() {
		if cur != nil {
			cur.text = b.String()
			files = append(files, *cur)
		}
		b.Reset()
	}
	for _, line := range strings.SplitAfter(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &diffFile{path: gitDiffNewPath(line)}
			inHeader = true
		case cur == nil:
			// Text before the first file header: keep it, under no path.
			if strings.TrimSpace(line) == "" {
				continue
			}
			cur = &diffFile{}
		case inHeader && strings.HasPrefix(line, "@@"):
			inHeader = false
		case inHeader:
			if p, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "+++ b/"); ok {
				cur.path = p
			}
		}
		b.WriteString(line)
	}
	flush()
	return files
}

// gitDiffNewPath reads the new-side path from a "diff --git a/X b/Y" line.
func gitDiffNewPath(line string) string {
	rest := strings.TrimRight(strings.TrimPrefix(line, "diff --git "), "\n")
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+len(" b/"):]
	}
	return ""
}

// budgetDiff fits the diff into max bytes, whole files at a time: first the
// files the asked threads are on, then the rest in diff order. A file that
// doesn't fit in the space left goes in cut off, if at least some of it
// does, and doesn't count as readable. It returns the text, the files that
// went in whole (plus wholeDiffKey when all of them did) and whether anything
// was cut.
func budgetDiff(diff string, toAsk []*workItem, max int) (string, map[string]bool, bool) {
	files := splitDiffFiles(diff)
	wanted := map[string]bool{}
	for _, w := range toAsk {
		if w.item.Kind == KindThread && w.item.Path != "" {
			wanted[w.item.Path] = true
		}
	}
	order := make([]int, 0, len(files))
	for i, f := range files {
		if wanted[f.path] {
			order = append(order, i)
		}
	}
	for i, f := range files {
		if !wanted[f.path] {
			order = append(order, i)
		}
	}

	readable := map[string]bool{}
	included := make([]string, len(files))
	var omitted []string
	used := 0
	for _, i := range order {
		f := files[i]
		switch {
		case used+len(f.text) <= max:
			included[i] = f.text
			used += len(f.text)
			readable[f.path] = true
		case max-used > 2000:
			cut := truncateText(f.text, max-used)
			included[i] = cut
			used += len(cut)
		default:
			omitted = append(omitted, f.path)
		}
	}

	var b strings.Builder
	cut := false
	for i, f := range files {
		if included[i] == "" {
			continue
		}
		b.WriteString(included[i])
		if !strings.HasSuffix(included[i], "\n") {
			b.WriteString("\n")
		}
		if !readable[f.path] {
			cut = true
		}
	}
	if len(omitted) > 0 {
		cut = true
		b.WriteString(fmt.Sprintf("\n(Diff omitted to fit the prompt for: %s)\n", strings.Join(omitted, ", ")))
	}
	if len(files) == 0 {
		// Nothing to show is not something cut; the model's verdicts still
		// can't lean on a diff, so none of them count as fully informed.
		return "(no diff available)\n", readable, false
	}
	if !cut {
		readable[wholeDiffKey] = true
	}
	return b.String(), readable, cut
}

// modelVerdict is one entry of the model's answer.
type modelVerdict struct {
	ID        string
	Status    string
	Rationale string
}

var codeFence = regexp.MustCompile("(?s)^\\s*```[a-zA-Z]*\\s*\n?(.*?)\\s*```\\s*$")

// parseVerdicts reads the model's JSON answer. It tolerates a code fence, prose
// around the object, a bare array, and numeric IDs; an entry with an unknown
// status counts as unclear.
func parseVerdicts(text string) ([]modelVerdict, error) {
	type rawEntry struct {
		ID        json.RawMessage `json:"id"`
		Status    string          `json:"status"`
		Rationale string          `json:"rationale"`
	}
	entries, err := decodeAnswerList[rawEntry](text, "items")
	if err != nil {
		return nil, err
	}

	out := make([]modelVerdict, 0, len(entries))
	for _, e := range entries {
		id := normalizeItemID(e.ID)
		if id == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(e.Status))
		switch status {
		case ItemAddressed, ItemOutstanding, ItemUnclear:
		default:
			status = ItemUnclear
		}
		rationale, _ := clip(strings.TrimSpace(e.Rationale), maxRationaleChars)
		out = append(out, modelVerdict{ID: id, Status: status, Rationale: rationale})
	}
	return out, nil
}

// decodeAnswerList reads the list under key from a model's JSON answer, e.g.
// {"items": [...]}. It tolerates a code fence, prose around the object, and a
// bare array of entries.
func decodeAnswerList[T any](text, key string) ([]T, error) {
	s := strings.TrimSpace(text)
	if m := codeFence.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	// Decode from the first opening bracket and stop at the end of that value,
	// so prose after it — braces included — doesn't matter.
	var wrapped map[string]json.RawMessage
	if start := strings.Index(s, "{"); start >= 0 &&
		json.NewDecoder(strings.NewReader(s[start:])).Decode(&wrapped) == nil {
		var entries []T
		if raw, ok := wrapped[key]; ok && json.Unmarshal(raw, &entries) == nil && entries != nil {
			return entries, nil
		}
	}
	var entries []T
	if start := strings.Index(s, "["); start >= 0 &&
		json.NewDecoder(strings.NewReader(s[start:])).Decode(&entries) == nil {
		return entries, nil
	}
	return nil, fmt.Errorf("no JSON object with an %q list", key)
}

// normalizeItemID accepts an item ID as a JSON string or number, with a
// stray "#", "Item " or "Change " prefix.
func normalizeItemID(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			return ""
		}
		if i, err := n.Int64(); err == nil {
			return strconv.FormatInt(i, 10)
		}
		s = n.String()
	}
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"Item ", "item ", "Change ", "change "} {
		s = strings.TrimPrefix(s, prefix)
	}
	return strings.TrimSpace(strings.TrimPrefix(s, "#"))
}

// finishReport counts, orders and renders the report once every item has its
// final status.
func finishReport(req Request, items []*workItem, report CommentsReport, insufficient bool, runLog RunLog) Result {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.item.Kind != b.item.Kind {
			return a.item.Kind == KindThread
		}
		if a.item.Path != b.item.Path {
			return a.item.Path < b.item.Path
		}
		if a.item.Line != b.item.Line {
			return a.item.Line < b.item.Line
		}
		return a.created.Before(b.created)
	})
	for _, w := range items {
		if w.thread != nil && w.item.Status != ItemAddressed {
			w.item.CodeContext = codeContext(w.thread.DiffHunk)
		}
	}

	outstanding := []ReportItem{}
	for _, w := range items {
		report.Items = append(report.Items, w.item)
		report.Counts.Total++
		switch w.item.Status {
		case ItemAddressed:
			report.Counts.Addressed++
		case ItemOutstanding:
			report.Counts.Outstanding++
			outstanding = append(outstanding, w.item)
		default:
			report.Counts.Unclear++
		}
		if w.item.Source == SourceModel {
			report.Counts.ByModel++
		}
	}
	// Outstanding first, then unclear, each in report order.
	for _, w := range items {
		if w.item.Status == ItemUnclear {
			outstanding = append(outstanding, w.item)
		}
	}

	switch {
	case insufficient:
		report.Verdict = VerdictInsufficientInput
	case report.Counts.Outstanding > 0 || len(report.ChangeRequests) > 0:
		report.Verdict = VerdictOutstanding
	case report.Counts.Unclear > 0:
		report.Verdict = VerdictUnclear
	case report.Counts.Total == 0:
		report.Verdict = VerdictNoComments
	default:
		report.Verdict = VerdictAllAddressed
	}
	report.Summary = summarize(report)

	var annotations []Annotation
	for _, w := range items {
		it := w.item
		if it.Kind != KindThread || it.Status == ItemAddressed || it.Outdated || it.Line <= 0 || it.Path == "" {
			continue
		}
		severity := "warning"
		if it.Status == ItemUnclear {
			severity = "info"
		}
		annotations = append(annotations, Annotation{
			Filename: it.Path,
			Line:     it.Line,
			Severity: severity,
			Content:  fmt.Sprintf("[%s] %s: %s\n%s", it.Status, it.Author, it.Excerpt, it.Rationale),
		})
	}

	status := StatusSuccess
	if insufficient {
		status = StatusInsufficientInput
		runLog.Warnings = append(runLog.Warnings, report.Missing...)
	}
	threads, conversation := 0, 0
	for _, w := range items {
		if w.item.Kind == KindThread {
			threads++
		} else {
			conversation++
		}
	}
	runLog.Input = fmt.Sprintf("%d thread(s) and %d conversation comment(s) judged, %d sent to the model; %d-byte diff",
		threads, conversation, report.Model.Asked, len(req.Diff))
	if report.Truncated {
		runLog.Input += " (prompt truncated)"
	}
	runLog.Parsed = fmt.Sprintf("verdict %s: %d addressed, %d outstanding, %d unclear (%d by model); %d change request(s)",
		report.Verdict, report.Counts.Addressed, report.Counts.Outstanding, report.Counts.Unclear,
		report.Counts.ByModel, len(report.ChangeRequests))
	if report.Model.ToolCalls > 0 {
		runLog.Parsed += fmt.Sprintf("; agent made %d tool call(s) over %d turn(s)", report.Model.ToolCalls, report.Model.Turns)
	}

	return Result{
		Status:      status,
		Body:        Body{BodyType: BodyMarkdown, BodyContent: renderCommentsReport(report, outstanding)},
		Annotations: annotations,
		Report:      report,
		Outstanding: outstanding,
		Truncated:   report.Truncated,
		Log:         runLog,
	}
}

// summarize is the verdict in one sentence.
func summarize(r CommentsReport) string {
	c := r.Counts
	switch r.Verdict {
	case VerdictInsufficientInput:
		return "Not enough input to say whether the comments are addressed: " + strings.Join(r.Missing, "; ") + "."
	case VerdictNoComments:
		return "There are no review threads or conversation comments to address."
	case VerdictAllAddressed:
		s := fmt.Sprintf("All %d item(s) are addressed", c.Total)
		if c.ByModel > 0 {
			s += fmt.Sprintf(" (%d by the model's judgment)", c.ByModel)
		}
		return s + "."
	case VerdictUnclear:
		return fmt.Sprintf("Nothing is known to be outstanding, but %d of %d item(s) are unclear; %d addressed.",
			c.Unclear, c.Total, c.Addressed)
	}

	// Outstanding: open items, an active change request, or both.
	var s string
	switch {
	case c.Outstanding > 0 && c.Unclear > 0:
		s = fmt.Sprintf("%d outstanding and %d unclear of %d item(s); %d addressed", c.Outstanding, c.Unclear, c.Total, c.Addressed)
	case c.Outstanding > 0:
		s = fmt.Sprintf("%d outstanding of %d item(s); %d addressed", c.Outstanding, c.Total, c.Addressed)
	case c.Total == 0:
		s = "There are no comment threads to address"
	case c.Unclear > 0:
		s = fmt.Sprintf("Nothing is known to be outstanding in the comments, but %d of %d item(s) are unclear", c.Unclear, c.Total)
	default:
		s = fmt.Sprintf("All %d item(s) are addressed", c.Total)
	}
	if len(r.ChangeRequests) > 0 {
		names := make([]string, 0, len(r.ChangeRequests))
		for _, cr := range r.ChangeRequests {
			names = append(names, cr.Reviewer)
		}
		verb := "requests"
		if len(names) > 1 {
			verb = "request"
		}
		joiner := "; "
		if c.Outstanding == 0 {
			joiner = ", but "
		}
		s += fmt.Sprintf("%s%s still %s changes", joiner, strings.Join(names, ", "), verb)
	}
	return s + "."
}

// renderCommentsReport is the report as markdown: the body every client can
// render, so it stands on its own.
func renderCommentsReport(r CommentsReport, outstanding []ReportItem) string {
	var b strings.Builder
	b.WriteString("**" + r.Summary + "**\n")

	var unclear []ReportItem
	var open []ReportItem
	for _, it := range outstanding {
		if it.Status == ItemOutstanding {
			open = append(open, it)
		} else {
			unclear = append(unclear, it)
		}
	}
	writeItems := func(title string, items []ReportItem) {
		if len(items) == 0 {
			return
		}
		b.WriteString(fmt.Sprintf("\n### %s (%d)\n", title, len(items)))
		for _, it := range items {
			b.WriteString("- " + describeItem(it) + "\n")
		}
	}
	writeItems("Outstanding", open)
	writeItems("Unclear", unclear)

	if len(r.ChangeRequests) > 0 {
		b.WriteString(fmt.Sprintf("\n### Changes requested (%d)\n", len(r.ChangeRequests)))
		for _, cr := range r.ChangeRequests {
			line := fmt.Sprintf("- **%s** requested changes", cr.Reviewer)
			if !cr.SubmittedAt.IsZero() {
				line += " on " + cr.SubmittedAt.UTC().Format("2006-01-02")
			}
			if cr.Excerpt != "" {
				line += fmt.Sprintf(": “%s”", cr.Excerpt)
			}
			if cr.HTMLURL != "" {
				line += fmt.Sprintf(" ([review](%s))", cr.HTMLURL)
			}
			b.WriteString(line + " _(GitHub)_\n")
		}
	}

	var addressed []ReportItem
	for _, it := range r.Items {
		if it.Status == ItemAddressed {
			addressed = append(addressed, it)
		}
	}
	writeItems("Addressed", addressed)

	var notes []string
	if r.Truncated {
		notes = append(notes, "Some input was cut to fit the model's prompt; verdicts that depended on it were not trusted.")
	}
	if r.Model.Note != "" {
		notes = append(notes, r.Model.Note)
	} else if r.Model.Consulted {
		notes = append(notes, fmt.Sprintf("The model (%s) judged %d item(s); everything else is GitHub's state.",
			strings.TrimSpace(r.Model.Provider+" "+r.Model.Model), r.Model.Asked))
	}
	if len(notes) > 0 {
		b.WriteString("\n---\n")
		for _, n := range notes {
			b.WriteString("_" + n + "_\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// describeItem is one item as a markdown list entry.
func describeItem(it ReportItem) string {
	where := "conversation"
	if it.Kind == KindThread {
		where = it.Path
		if it.Line > 0 {
			where = fmt.Sprintf("%s:%d", it.Path, it.Line)
		}
		if it.Outdated {
			where += " (outdated)"
		}
	}
	s := fmt.Sprintf("**%s** — %s: “%s”", where, it.Author, it.Excerpt)
	if it.HTMLURL != "" {
		s += fmt.Sprintf(" ([view](%s))", it.HTMLURL)
	}
	s += fmt.Sprintf(" _(%s)_  \n  %s", sourceLabel(it.Source), it.Rationale)
	if it.ModelNote != "" {
		s += "  \n  _" + it.ModelNote + "_"
	}
	if it.CodeContext != "" {
		// Indented to sit inside the list item.
		marker := codeFenceFor(it.CodeContext)
		s += "\n\n  " + marker + "diff\n  " + strings.ReplaceAll(it.CodeContext, "\n", "\n  ") + "\n  " + marker
	}
	return s
}

// codeContextLines is how many lines of a thread's diff hunk the report keeps.
const codeContextLines = 8

// maxCodeLineRunes caps each kept line, so a minified line can't bloat the
// report.
const maxCodeLineRunes = 240

// codeContext is the tail of a review comment's diff hunk. GitHub's hunk runs
// from its "@@" header down to the commented line, so its last lines are the
// code the comment is about. The header goes when lines are cut: its line
// numbers would no longer match the first line shown.
func codeContext(hunk string) string {
	hunk = strings.TrimRight(strings.ReplaceAll(hunk, "\r\n", "\n"), "\n")
	if strings.TrimSpace(hunk) == "" {
		return ""
	}
	lines := strings.Split(hunk, "\n")
	if len(lines) > codeContextLines {
		lines = lines[len(lines)-codeContextLines:]
	}
	for i, line := range lines {
		if utf8.RuneCountInString(line) > maxCodeLineRunes {
			lines[i] = string([]rune(line)[:maxCodeLineRunes]) + "…"
		}
	}
	return strings.Join(lines, "\n")
}

// codeFenceFor is a markdown code fence longer than any run of backticks in
// code, so the code can't close it early.
func codeFenceFor(code string) string {
	longest, run := 0, 0
	for _, r := range code {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

func sourceLabel(source string) string {
	if source == SourceModel {
		return "model"
	}
	return "GitHub"
}

// readFileTool lets an agent read a file at the PR head.
func readFileTool(read func(ctx context.Context, path string) (string, error)) Tool {
	return Tool{
		Name:        "read_file",
		Description: "Read a file as it is at the pull request's head revision.",
		Arguments:   `{"path": "file path relative to the repository root"}`,
		Call: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.Path) == "" {
				return "", fmt.Errorf(`expected {"path": "<file path>"}`)
			}
			content, err := read(ctx, strings.TrimSpace(a.Path))
			if err != nil {
				return "", err
			}
			return truncateText(content, maxToolResultBytes), nil
		},
	}
}

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	// svgElement is an inline SVG, drawing and all: review bots put badges and
	// icons in front of their comments, and none of it is text.
	svgElement = regexp.MustCompile(`(?s)<svg\b.*?</svg\s*>`)
	imgTag     = regexp.MustCompile(`<img\b[^<>]*>`)
	imgAlt     = regexp.MustCompile(`\balt\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	// htmlTag is an opening or closing tag of an element GitHub renders in a
	// comment. Only known, lowercase names with attribute-shaped contents
	// match, so text like Vec<String>, Pair<A, B> or a<b and c>d survives.
	htmlTag    = regexp.MustCompile(`</?(a|abbr|b|blockquote|br|code|dd|del|details|div|dl|dt|em|h[1-6]|hr|i|ins|kbd|li|ol|p|picture|pre|q|s|samp|source|span|strong|sub|summary|sup|svg|table|tbody|td|tfoot|th|thead|tr|tt|u|ul|var)(?:\s+(?:open|[^<>=]*=[^<>]*))?\s*/?>`)
	blankLines = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)+`)
)

// htmlBlockTags are the tags that break a line when rendered; the rest are
// inline and vanish without a trace.
var htmlBlockTags = map[string]bool{
	"blockquote": true, "br": true, "dd": true, "details": true, "div": true, "dl": true, "dt": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "hr": true, "li": true,
	"ol": true, "p": true, "pre": true, "summary": true, "table": true, "tr": true, "ul": true,
}

// plainText is a comment body with its HTML reduced to the text a reader sees:
// comments and inline SVGs dropped, an image replaced by its alt text in
// brackets (a bot's "P1" badge reads "[P1]"), other tags removed and entities
// decoded. Markdown is left as it is, and so is anything inside backticks,
// where a tag is code rather than markup.
func plainText(body string) string {
	parts := strings.Split(htmlComment.ReplaceAllString(body, ""), "`")
	// Even parts are outside code spans and fences.
	for i := 0; i < len(parts); i += 2 {
		s := svgElement.ReplaceAllString(parts[i], "")
		s = imgTag.ReplaceAllStringFunc(s, func(tag string) string {
			m := imgAlt.FindStringSubmatch(tag)
			if m == nil {
				return ""
			}
			if alt := strings.TrimSpace(m[1] + m[2]); alt != "" {
				return "[" + alt + "]"
			}
			return ""
		})
		s = htmlTag.ReplaceAllStringFunc(s, func(tag string) string {
			name := htmlTag.FindStringSubmatch(tag)[1]
			if htmlBlockTags[name] {
				return "\n"
			}
			if name == "td" || name == "th" {
				return " "
			}
			return ""
		})
		parts[i] = html.UnescapeString(s)
	}
	s := strings.Join(parts, "`")
	return strings.TrimSpace(blankLines.ReplaceAllString(s, "\n\n"))
}

// excerpt is a comment body shortened to one line of plain text for lists.
func excerpt(body string) string {
	s := strings.Join(strings.Fields(plainText(body)), " ")
	if utf8.RuneCountInString(s) > excerptChars {
		runes := []rune(s)
		s = strings.TrimSpace(string(runes[:excerptChars])) + "…"
	}
	return s
}

// briefError is an error on one line, short enough to show a reader.
func briefError(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if utf8.RuneCountInString(s) > 240 {
		s = string([]rune(s)[:240]) + "…"
	}
	return s
}

// clip cuts s to max bytes on a rune boundary and reports whether it did.
func clip(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	return truncateText(s, max), true
}

func isBot(login string) bool {
	return strings.HasSuffix(strings.ToLower(login), "[bot]")
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "time unknown"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}
