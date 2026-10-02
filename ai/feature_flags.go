package ai

import (
	"context"
	"crs/config"
	"crs/llm"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// FeatureFlagsID is the ID of the feature-flags feature.
const FeatureFlagsID = "feature-flags"

// FeatureFlags answers "is every change in this PR behind a feature flag?" —
// how safe the PR is to approve, if a flag keeps what it changes switched off
// until someone turns it on.
//
// The unit it judges is a change: one hunk of the diff, or a whole file that a
// path rule decides. Path rules come first. A test file or a documentation
// file has no runtime effect, and a dependency lockfile changes what every
// build installs, which no flag can gate. The model judges every other hunk:
// gated, ungated, a definition, no effect, or unclear.
//
// Only logic changes count. A hunk that just adds a function, class, type or
// constant is a definition: it runs where it's called, and that call site is
// the change that gets judged. In agent mode the model is told to look a
// changed function's callers up, since code only reached from behind a flag
// is gated even though the diff shows no flag around it.
//
// The design goal is never to report a confidently wrong "all gated":
//
//   - A gated verdict must name its flag, and the name must appear in the code
//     the model was shown — the diff in its prompt, or what its tools returned.
//     Otherwise the change goes back to unclear.
//   - When any change the model had to judge was cut from its prompt, only its
//     ungated verdicts stand: whether code is reachable only from behind a flag
//     can hinge on a part it didn't see.
//   - A change the model wasn't asked about, or gave no verdict for, stays
//     unclear; so does every change when the model can't be reached.
//   - No diff makes the whole run insufficient-input rather than empty.
//
// Every change records its source — "rule" or "model" — so a reader can see
// which statuses are the path rules' and which are the model's judgment. The
// result depends on the code alone, so it is keyed by the head SHA only: a new
// comment doesn't make it stale.
type FeatureFlags struct{}

func (FeatureFlags) ID() string   { return FeatureFlagsID }
func (FeatureFlags) Name() string { return "Behind a flag?" }

func (FeatureFlags) Description() string {
	return "Reports which logic changes would take effect with every feature flag off; new functions and " +
		"classes are judged where they're called. Path rules settle tests, docs and lockfiles; the model " +
		"judges the rest, and a \"behind a flag\" verdict must name a flag found in the code it was shown."
}

func (FeatureFlags) Modes() []string {
	return []string{config.AIModeOneShot, config.AIModeAgent}
}

// CodeOnly keys the feature's results by the head SHA alone.
func (FeatureFlags) CodeOnly() bool { return true }

// Change statuses in a feature-flags report.
const (
	// ChangeGated only takes effect while a flag is on.
	ChangeGated = "gated"
	// ChangeUngated takes effect whatever the flags say.
	ChangeUngated = "ungated"
	// ChangeDefinition only adds definitions — functions, classes, types,
	// constants — that run where they're called. It isn't a logic change: the
	// call sites are, and they're judged where the diff adds them.
	ChangeDefinition = "definition"
	// ChangeNoEffect can't change behaviour at all: tests, docs, comments.
	ChangeNoEffect = "no-effect"
	ChangeUnclear  = "unclear"

	// SourceRule marks a status a path rule decided.
	SourceRule = "rule"
)

// Path categories: what a path rule decided a whole file is.
const (
	CategoryTest     = "test"
	CategoryDocs     = "docs"
	CategoryLockfile = "lockfile"
)

// Verdicts of a feature-flags report, beside the shared VerdictUnclear and
// VerdictInsufficientInput.
const (
	VerdictAllGated         = "all-gated"
	VerdictUngated          = "ungated"
	VerdictNoRuntimeChanges = "no-runtime-changes"
)

// Prompt budgets. A change that doesn't fit isn't sent to the model, and then
// no gated or no-effect verdict is trusted.
const (
	maxFlagModelChanges = 100
	maxFlagNameChars    = 100
	// minFlagNameChars keeps a one- or two-letter "flag" from matching some
	// stray text of the diff.
	minFlagNameChars = 3
)

// FlagsReport is the typed report feature-flags stores as the result's
// "report".
type FlagsReport struct {
	Verdict string `json:"verdict"`
	// Summary is the verdict in one sentence.
	Summary string     `json:"summary"`
	Counts  FlagCounts `json:"counts"`
	// Flags lists the flags that gate changes, in the order they first appear.
	Flags []FlagUse `json:"flags"`
	// Changes is every change the report judged, in diff order.
	Changes []FlagChange `json:"changes"`
	Model   ModelUse     `json:"model"`
	// Truncated is set when a change was cut from the model's prompt.
	Truncated bool `json:"truncated"`
	// Missing names what was unavailable, when the verdict is
	// insufficient-input.
	Missing []string `json:"missing"`
}

// FlagCounts tallies the changes by status.
type FlagCounts struct {
	Total       int `json:"total"`
	Gated       int `json:"gated"`
	Ungated     int `json:"ungated"`
	Definitions int `json:"definitions"`
	NoEffect    int `json:"no_effect"`
	Unclear     int `json:"unclear"`
	// ByModel counts the changes whose status the model decided.
	ByModel int `json:"by_model"`
}

// FlagUse is a flag and how many changes it gates.
type FlagUse struct {
	Name    string `json:"name"`
	Changes int    `json:"changes"`
}

// FlagChange is the verdict on one hunk, or on a whole file a path rule
// decided.
type FlagChange struct {
	// ID is the change's position in the diff, from "1"; the model's prompt
	// numbers changes the same way.
	ID   string `json:"id"`
	Path string `json:"path"`
	// Line and EndLine span the change in the head version of Path; 0 for a
	// whole file, and for a deleted file, which has no head version.
	Line    int `json:"line,omitempty"`
	EndLine int `json:"end_line,omitempty"`
	// Context is what the hunk header names, usually the enclosing function.
	Context     string `json:"context,omitempty"`
	Added       int    `json:"added"`
	Removed     int    `json:"removed"`
	NewFile     bool   `json:"new_file,omitempty"`
	DeletedFile bool   `json:"deleted_file,omitempty"`
	Status      string `json:"status"`
	// Source is "model" when the model's verdict decided Status, and "rule"
	// otherwise: a path rule decided it, or nothing did and it stays unclear.
	Source string `json:"source"`
	// Category is the path rule that decided the whole file: test, docs or
	// lockfile. Empty for a change the model judged.
	Category string `json:"category,omitempty"`
	// Flag is the flag a gated change sits behind, as the code spells it.
	Flag      string `json:"flag,omitempty"`
	Rationale string `json:"rationale"`
}

// changedFile is one file of the diff, cut into hunks.
type changedFile struct {
	path     string
	text     string
	newFile  bool
	deleted  bool
	category string
	hunks    []diffHunk
}

// diffHunk is one hunk of a file's diff.
type diffHunk struct {
	// text is the hunk as the diff has it, its @@ line first.
	text    string
	context string
	// line and endLine span the changed lines in the head version.
	line, endLine  int
	added, removed int
}

// flagWork is a change plus what the model step needs to know about it.
type flagWork struct {
	change FlagChange
	file   *changedFile
	// hunk is nil for a file with no hunks (a binary file, a rename or a mode
	// change) and for a whole file a path rule decided.
	hunk *diffHunk
	// askModel marks the changes the path rules left for the model.
	askModel bool
	// asked is set once the change actually went into the prompt.
	asked bool
}

func (f FeatureFlags) Run(ctx context.Context, req Request) (Result, error) {
	report := FlagsReport{
		Flags:   []FlagUse{},
		Changes: []FlagChange{},
		Model:   ModelUse{Mode: req.Mode},
		Missing: []string{},
	}
	var runLog RunLog

	files := parseChangedFiles(req.Diff)
	switch {
	case strings.TrimSpace(req.Diff) == "":
		report.Missing = append(report.Missing, "no diff is available for this PR")
	case len(files) == 0:
		report.Missing = append(report.Missing, "the PR's diff could not be read")
	}
	insufficient := len(report.Missing) > 0

	items := classifyChanges(files)
	var toAsk []*flagWork
	for _, w := range items {
		if !w.askModel {
			continue
		}
		if len(toAsk) == maxFlagModelChanges {
			report.Truncated = true
			w.change.Rationale += fmt.Sprintf(" It was not sent to the model: more than %d changes needed a judgment.", maxFlagModelChanges)
			continue
		}
		toAsk = append(toAsk, w)
	}
	if len(toAsk) > 0 {
		f.consultModel(ctx, req, items, toAsk, &report, &runLog)
	}

	return finishFlagsReport(req, files, items, report, insufficient, runLog), nil
}

// hunkHeader reads the head-side start line and the trailing context of an
// "@@ -a,b +c,d @@ context" line.
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@ ?(.*)$`)

// parseChangedFiles cuts a unified diff into files and hunks. It reads both
// raw git diffs and the server's rendering of them, which puts a space after
// each line's +/- marker and a blank line before each hunk.
func parseChangedFiles(diff string) []changedFile {
	var files []changedFile
	for _, section := range splitDiffFiles(diff) {
		if section.path == "" {
			continue // text before the first file header
		}
		cf := changedFile{path: section.path, text: section.text}
		var cur *diffHunk
		var b strings.Builder
		newLine, lastNew := 0, 0
		first, last := 0, 0
		flush := func() {
			if cur == nil {
				return
			}
			cur.text = b.String()
			// A removal at the very end of a file anchors past its last line.
			if lastNew > 0 && first > lastNew {
				first = lastNew
			}
			if lastNew > 0 && last > lastNew {
				last = lastNew
			}
			cur.line, cur.endLine = first, last
			if cf.deleted {
				cur.line, cur.endLine = 0, 0
			}
			cf.hunks = append(cf.hunks, *cur)
			b.Reset()
			cur = nil
		}
		// mark records a change sitting at head line n.
		mark := func(n int) {
			if first == 0 {
				first = n
			}
			last = n
		}
		for _, raw := range strings.SplitAfter(section.text, "\n") {
			line := strings.TrimRight(raw, "\n")
			if m := hunkHeader.FindStringSubmatch(line); m != nil {
				flush()
				start, _ := strconv.Atoi(m[1])
				cur = &diffHunk{context: strings.TrimSpace(m[2])}
				newLine, lastNew, first, last = start, 0, 0, 0
				b.WriteString(raw)
				continue
			}
			if cur == nil {
				switch line {
				case "--- /dev/null":
					cf.newFile = true
				case "+++ /dev/null":
					cf.deleted = true
				}
				continue
			}
			if line == "" {
				continue // the rendered diff's gap before the next hunk
			}
			b.WriteString(raw)
			switch line[0] {
			case '+':
				cur.added++
				mark(newLine)
				lastNew = newLine
				newLine++
			case '-':
				cur.removed++
				// A removed line has no head line; it sits where the next one is.
				mark(newLine)
			case '\\':
				// "\ No newline at end of file"
			default:
				lastNew = newLine
				newLine++
			}
		}
		flush()
		files = append(files, cf)
	}
	return files
}

// classifyChanges applies the path rules to every file and turns what they
// leave into changes for the model, one per hunk.
func classifyChanges(files []changedFile) []*flagWork {
	var items []*flagWork
	next := func() string { return strconv.Itoa(len(items) + 1) }
	for i := range files {
		cf := &files[i]
		cf.category = pathCategory(cf.path)
		base := FlagChange{Path: cf.path, NewFile: cf.newFile, DeletedFile: cf.deleted}
		if cf.category != "" {
			c := base
			c.ID = next()
			c.Source = SourceRule
			c.Category = cf.category
			for _, h := range cf.hunks {
				c.Added += h.added
				c.Removed += h.removed
			}
			switch cf.category {
			case CategoryTest:
				c.Status = ChangeNoEffect
				c.Rationale = "A test file: it doesn't ship, so it can't change what anyone sees."
			case CategoryDocs:
				c.Status = ChangeNoEffect
				c.Rationale = "Documentation: it doesn't change how the code behaves."
			case CategoryLockfile:
				c.Status = ChangeUngated
				c.Rationale = "A dependency lockfile: it changes what every build installs, which no flag can gate."
			}
			items = append(items, &flagWork{change: c, file: cf})
			continue
		}
		if len(cf.hunks) == 0 {
			c := base
			c.ID = next()
			c.Status = ChangeUnclear
			c.Source = SourceRule
			c.Rationale = "Needs the model's judgment: the file changed with no text diff (a binary file, a rename or a mode change)."
			items = append(items, &flagWork{change: c, file: cf, askModel: true})
			continue
		}
		for j := range cf.hunks {
			h := &cf.hunks[j]
			c := base
			c.ID = next()
			c.Line, c.EndLine = h.line, h.endLine
			c.Context = h.context
			c.Added, c.Removed = h.added, h.removed
			c.Status = ChangeUnclear
			c.Source = SourceRule
			c.Rationale = "Needs the model's judgment."
			items = append(items, &flagWork{change: c, file: cf, hunk: h, askModel: true})
		}
	}
	return items
}

// Path rules. They are deliberately narrow: a file they miss goes to the
// model, while a runtime file they took for a test or a doc would be reported
// as having no effect.
var (
	// testFileName matches the test naming conventions of the common
	// languages; a name like ABTest.java is left alone, since it may well be
	// production code about experiments.
	testFileName = regexp.MustCompile(`^(?:test_.+\.py|conftest\.py|.+_test\.(?:go|py|rb|exs|dart|cc|cpp)|.+_spec\.rb|.+\.(?:test|spec)\.[cm]?[jt]sx?)$`)
	testDirs     = map[string]bool{"test": true, "tests": true, "__tests__": true, "testdata": true, "e2e": true}

	docExtensions = map[string]bool{".md": true, ".markdown": true, ".rst": true, ".adoc": true}
	docDirs       = map[string]bool{"docs": true, "doc": true}
	docNames      = map[string]bool{"README": true, "CHANGELOG": true, "CHANGES": true, "HISTORY": true,
		"CONTRIBUTING": true, "AUTHORS": true, "CODE_OF_CONDUCT": true, "SECURITY": true}

	lockfiles = map[string]bool{"package-lock.json": true, "npm-shrinkwrap.json": true, "yarn.lock": true,
		"pnpm-lock.yaml": true, "bun.lock": true, "bun.lockb": true, "go.sum": true, "Cargo.lock": true,
		"poetry.lock": true, "Pipfile.lock": true, "uv.lock": true, "pdm.lock": true, "Gemfile.lock": true,
		"composer.lock": true, "mix.lock": true, "pubspec.lock": true, "Podfile.lock": true,
		"packages.lock.json": true, "gradle.lockfile": true, "flake.lock": true}
)

// pathCategory is the path rule that decides a whole file, or "" when the
// model must judge it.
func pathCategory(p string) string {
	base := path.Base(p)
	dirs := strings.Split(path.Dir(p), "/")
	if testFileName.MatchString(base) || containsAny(dirs, testDirs) {
		return CategoryTest
	}
	if ext := strings.ToLower(path.Ext(base)); docExtensions[ext] {
		// Markdown elsewhere may be read at run time — a template, a prompt —
		// so only the usual places for documentation count.
		if path.Dir(p) == "." || containsAny(dirs, docDirs) || docNames[strings.ToUpper(strings.TrimSuffix(base, path.Ext(base)))] {
			return CategoryDocs
		}
	}
	if lockfiles[base] {
		return CategoryLockfile
	}
	return ""
}

func containsAny(dirs []string, set map[string]bool) bool {
	for _, d := range dirs {
		if set[d] {
			return true
		}
	}
	return false
}

// consultModel asks the model about toAsk and merges its verdicts into items.
// A model that can't be reached, or answers in a shape that can't be read,
// leaves the changes unclear and says why; it never fails the run.
func (FeatureFlags) consultModel(ctx context.Context, req Request, items, toAsk []*flagWork,
	report *FlagsReport, runLog *RunLog) {
	prompt, shown, complete := buildFlagsPrompt(req, items, toAsk)
	if !complete {
		report.Truncated = true
	}
	var sent []*flagWork
	for _, w := range toAsk {
		if w.asked {
			sent = append(sent, w)
		}
	}
	report.Model.Asked = len(sent)
	if len(sent) == 0 {
		report.Model.Note = "The model was not consulted: none of the changes it had to judge fit its prompt."
		return
	}

	// seen is everything the model was shown of the code, for checking that a
	// flag it names is really there.
	seen := []string{shown}
	var answer string
	var err error
	if req.Mode == config.AIModeAgent && req.Agent != nil {
		var tools []Tool
		if req.ReadFile != nil {
			tools = append(tools, readFileTool(req.ReadFile))
		}
		if req.SearchCode != nil {
			tools = append(tools, searchCodeTool(req.SearchCode))
		}
		var res AgentResult
		res, err = req.Agent.Run(ctx, AgentTask{Prompt: prompt, Tools: tools, MaxTurns: agentMaxTurns})
		answer = res.Answer
		for _, call := range res.ToolCalls {
			if call.Err == "" {
				seen = append(seen, call.Result)
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
		report.Model.Note = fmt.Sprintf("The model could not be consulted, so %d change(s) stay unclear: %s", len(sent), briefError(err))
		runLog.Warnings = append(runLog.Warnings, fmt.Sprintf("model unavailable: %v", err))
		return
	}
	report.Model.Consulted = true

	verdicts, perr := parseFlagVerdicts(answer)
	if perr != nil {
		report.Model.Note = "The model's answer could not be read, so the changes it was asked about stay unclear."
		runLog.Warnings = append(runLog.Warnings, fmt.Sprintf("model answer unreadable: %v", perr))
		runLog.ResponseSnippet = llm.Snippet(answer)
		return
	}

	seenCode := normalizeFlagText(strings.Join(seen, "\n"))
	byID := make(map[string]*flagWork, len(items))
	for _, w := range items {
		byID[w.change.ID] = w
	}
	unknown := 0
	answered := map[string]bool{}
	for _, v := range verdicts {
		w, ok := byID[v.ID]
		if !ok || !w.asked {
			unknown++
			continue
		}
		answered[v.ID] = true
		status, flag, rationale := v.Status, v.Flag, v.Rationale
		if rationale == "" {
			rationale = "The model gave no reason."
		}
		switch {
		case status == ChangeGated && flag == "":
			rationale = "The model called this gated but named no flag, so the verdict isn't trusted. Its reason: " + rationale
			status = ChangeUnclear
		case status == ChangeGated && !flagSeen(flag, seenCode):
			rationale = fmt.Sprintf("The model called this gated by %q, which appears nowhere in the code it was shown, so the verdict isn't trusted. Its reason: %s", flag, rationale)
			status, flag = ChangeUnclear, ""
		case status == ChangeDefinition && (w.hunk == nil || w.change.Added == 0):
			// Deleting a definition, or a file with no text diff, adds none.
			rationale = "The model called this a new definition, but it adds no code, so the verdict isn't trusted. Its reason: " + rationale
			status = ChangeUnclear
		case (status == ChangeGated || status == ChangeDefinition || status == ChangeNoEffect) && !complete:
			called := status
			if status == ChangeDefinition {
				called = "a new definition"
			}
			rationale = fmt.Sprintf("The model called this %s, but not every change fit its prompt, and what it didn't see could reach this code without a flag, so the verdict isn't trusted. Its reason: %s", called, rationale)
			status, flag = ChangeUnclear, ""
		}
		if status != ChangeGated {
			flag = ""
		}
		w.change.Status = status
		w.change.Source = SourceModel
		w.change.Flag = flag
		w.change.Rationale = rationale
	}

	missed := 0
	for _, w := range sent {
		if !answered[w.change.ID] {
			missed++
			w.change.Rationale += " The model gave no verdict for it."
		}
	}
	if missed > 0 {
		runLog.Warnings = append(runLog.Warnings, fmt.Sprintf("model gave no verdict for %d of %d change(s)", missed, len(sent)))
		runLog.ResponseSnippet = llm.Snippet(answer)
	}
	if unknown > 0 {
		runLog.Warnings = append(runLog.Warnings, fmt.Sprintf("model answered for %d change(s) it wasn't asked about", unknown))
	}
}

// normalizeFlagText lowercases s and keeps only letters and digits, so a flag
// named NEW_CHECKOUT is found where the code says "new-checkout" or
// NewCheckout.
func normalizeFlagText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// flagSeen reports whether flag appears in the normalized code the model saw.
func flagSeen(flag, seenCode string) bool {
	name := normalizeFlagText(flag)
	return len(name) >= minFlagNameChars && strings.Contains(seenCode, name)
}

// buildFlagsPrompt renders the model prompt for toAsk: the changes to judge,
// grouped by file in diff order, then the tests as context when they fit, then
// the files the path rules settled. It marks each change that went in as asked
// and returns the prompt, the diff text it shows, and whether every change in
// toAsk — and every change that needed a judgment — made it in.
func buildFlagsPrompt(req Request, items, toAsk []*flagWork) (string, string, bool) {
	var b strings.Builder
	b.WriteString(`You are helping a code reviewer judge how safe a pull request is to approve: is each logic change behind a feature flag — a feature flag, a waffle flag, switch or sample, a toggle, a kill switch, an experiment or a gate — so that it has no effect while the flag is off?

For each numbered change below, answer: "If this change were wrong, could it affect anyone while its flags are off, at their defaults?"
- "gated": no — the change only runs while a flag is on. Name the flag in "flag", exactly as the code spells it (the flag's name or the constant holding it) and nothing else. Code that can only be reached from behind a flag counts as gated: a function whose every caller is behind the flag, or a change that only calls code which checks the flag itself before doing anything.
- "definition": the change only adds new definitions — functions, methods, classes, types, constants, and the imports they need — which do nothing until something calls them. Adding one is not a logic change: the code that calls it is, and that is judged where the diff adds it. It is not a definition, and is judged like any other change, when the new code takes effect without new code calling it: it registers itself (a route, view, handler, signal receiver, task, command, plugin or admin entry, a registering decorator), runs when it is loaded (Go's init(), module- or class-level statements), overrides or implements something existing code already calls (an overridden method, a framework hook such as save() or __str__, Go's String() or MarshalJSON, a method that makes a type satisfy an interface), or changes an existing definition — its body, signature, fields or defaults. A change that adds definitions and also changes code that runs is judged by the code that runs.
- "ungated": yes — it runs whatever the flags say. That includes refactors, however behaviour-preserving they look; changes to what runs while the flag is off; removing a flag check, so the code it guarded always runs; new routes, handlers, scheduled jobs, signal receivers, migrations and schema changes; dependency, build and configuration changes; and a flag this diff turns on by default (a default of true, a migration creating it active, everyone=True), which protects nothing.
- "no-effect": the change cannot affect behaviour at all: comments, documentation, formatting, or changes to code nothing runs.
- "unclear": you cannot tell from what you were given — for example, whether a flag check encloses the change is outside the diff shown.
Prefer "unclear" to guessing "gated". Keep each rationale to one sentence that names the evidence.

`)
	b.WriteString(flagsReachability(req))
	b.WriteString(`

Flag checks look like: django-waffle's flag_is_active, switch_is_active and sample_is_active, @waffle_flag and @waffle_switch, {% flag %} and {% switch %}; LaunchDarkly's variation and boolVariation; Unleash's isEnabled; Flipper.enabled?; OpenFeature's getBooleanValue; GrowthBook's isOn; Statsig's checkGate; settings or environment toggles; and in-house helpers of the same shape.
`)
	if title, author := prTitleAuthor(req); title != "" {
		b.WriteString(fmt.Sprintf("\nPull request: %q by %s\n", title, author))
	}

	// Fit whole changes into the budget in diff order, skipping any that
	// don't fit so a later, smaller one still can.
	used := 0
	complete := len(toAsk) == countAskModel(items)
	for _, w := range toAsk {
		size := len(changeText(w)) + 200
		if used+size > maxPromptDiffBytes {
			complete = false
			w.change.Rationale += " It was not sent to the model: the diff didn't fit its prompt."
			continue
		}
		used += size
		w.asked = true
	}

	var shown strings.Builder
	b.WriteString("\n## Changes to judge\n")
	var lastFile *changedFile
	for _, w := range toAsk {
		if !w.asked {
			continue
		}
		if w.file != lastFile {
			lastFile = w.file
			heading := "\n### " + w.file.path
			switch {
			case w.file.deleted:
				heading += " (deleted)"
			case w.file.newFile:
				heading += " (new file)"
			}
			b.WriteString(heading + "\n")
		}
		label := fmt.Sprintf("[Change %s]", w.change.ID)
		switch {
		case w.hunk == nil:
			label += " no text diff: a binary file, a rename or a mode change"
		case w.change.Line > 0 && w.change.EndLine > w.change.Line:
			label += fmt.Sprintf(" head lines %d-%d", w.change.Line, w.change.EndLine)
		case w.change.Line > 0:
			label += fmt.Sprintf(" head line %d", w.change.Line)
		}
		text := changeText(w)
		b.WriteString(label + "\n" + text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
		shown.WriteString(text)
		shown.WriteString("\n")
	}

	var tests, settled []string
	for _, w := range items {
		switch {
		case w.change.Category == CategoryTest && used+len(w.file.text) <= maxPromptDiffBytes:
			used += len(w.file.text)
			tests = append(tests, w.file.text)
		case w.change.Category != "":
			settled = append(settled, fmt.Sprintf("- %s: %s", w.change.Path, categoryLabel(w.change.Category)))
		}
	}
	if len(tests) > 0 {
		b.WriteString("\n## Test changes (context only: tests don't ship, so don't judge them — but they can show which flag a change is behind)\n")
		for _, t := range tests {
			b.WriteString(t)
			shown.WriteString(t)
			if !strings.HasSuffix(t, "\n") {
				b.WriteString("\n")
			}
		}
	}
	if len(settled) > 0 {
		b.WriteString("\n## Other changed files (already decided; don't judge them)\n")
		b.WriteString(strings.Join(settled, "\n") + "\n")
	}

	b.WriteString(`
Respond with only a JSON object — no prose, no code fence — with one entry per change under "Changes to judge":
{"changes": [{"id": "<change number>", "status": "gated" | "ungated" | "definition" | "no-effect" | "unclear", "flag": "<the flag, for gated>", "rationale": "<one sentence>"}]}
`)
	return b.String(), shown.String(), complete
}

// flagsReachability is the prompt's guidance for a change whose status hinges
// on code outside the diff — its callers, or what it calls. An agent that can
// search the repository is told to look the callers up; without a search, a
// change that may only be reached from behind a flag is unclear rather than
// ungated, so code a flag already guards elsewhere isn't reported as running
// for everyone.
func flagsReachability(req Request) string {
	const question = "Whether a change to existing code runs without a flag can hinge on code the diff doesn't show: who calls it, or whether code it calls checks the flag itself. "
	if canLookUpCallers(req) {
		return question + fmt.Sprintf(`Look it up rather than guess. Before calling such a change "ungated" or "unclear", use search_code to find the callers of the function it changes (its name usually follows the hunk's @@), and read_file a caller whose flag check isn't on the line the search returns, or code the change calls that may check the flag. A change whose every caller is behind a flag is gated by that flag; when a caller is a helper, follow its callers in turn. Shared code with callers of its own — a model, a view, a utility used across the codebase — is ungated without a search, and a definition needs none. You can make at most %d tool calls, so spend them on the changes whose status hinges on them.`, agentMaxTurns-1)
	}
	return question + `Say "ungated" when the diff shows a way to reach the change without a flag, or when it is shared code with callers of its own — a model, a view, a utility used across the codebase. Say "unclear" when it looks like part of a flagged feature — named for it, in its module, or written for it — and its callers aren't shown: they may all be behind the flag.`
}

func countAskModel(items []*flagWork) int {
	n := 0
	for _, w := range items {
		if w.askModel {
			n++
		}
	}
	return n
}

// changeText is the diff text the model sees for a change.
func changeText(w *flagWork) string {
	if w.hunk != nil {
		return w.hunk.text
	}
	return w.file.text
}

func categoryLabel(category string) string {
	switch category {
	case CategoryTest:
		return "test file"
	case CategoryDocs:
		return "documentation"
	case CategoryLockfile:
		return "dependency lockfile"
	}
	return category
}

// flagVerdict is one entry of the model's answer.
type flagVerdict struct {
	ID        string
	Status    string
	Flag      string
	Rationale string
}

// parseFlagVerdicts reads the model's JSON answer, with the same tolerance as
// parseVerdicts; an entry with an unknown status counts as unclear.
func parseFlagVerdicts(text string) ([]flagVerdict, error) {
	type rawEntry struct {
		ID        json.RawMessage `json:"id"`
		Status    string          `json:"status"`
		Flag      string          `json:"flag"`
		Rationale string          `json:"rationale"`
	}
	entries, err := decodeAnswerList[rawEntry](text, "changes")
	if err != nil {
		return nil, err
	}
	out := make([]flagVerdict, 0, len(entries))
	for _, e := range entries {
		id := normalizeItemID(e.ID)
		if id == "" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(e.Status))
		switch status {
		case ChangeGated, ChangeUngated, ChangeDefinition, ChangeNoEffect, ChangeUnclear:
		default:
			status = ChangeUnclear
		}
		flag, _ := clip(strings.Trim(strings.TrimSpace(e.Flag), "`'\""), maxFlagNameChars)
		rationale, _ := clip(strings.TrimSpace(e.Rationale), maxRationaleChars)
		out = append(out, flagVerdict{ID: id, Status: status, Flag: flag, Rationale: rationale})
	}
	return out, nil
}

// finishFlagsReport counts and renders the report once every change has its
// final status.
func finishFlagsReport(req Request, files []changedFile, items []*flagWork, report FlagsReport,
	insufficient bool, runLog RunLog) Result {
	outstanding := []FlagChange{}
	flagIndex := map[string]int{}
	for _, w := range items {
		c := w.change
		report.Changes = append(report.Changes, c)
		report.Counts.Total++
		switch c.Status {
		case ChangeGated:
			report.Counts.Gated++
			if i, ok := flagIndex[c.Flag]; ok {
				report.Flags[i].Changes++
			} else {
				flagIndex[c.Flag] = len(report.Flags)
				report.Flags = append(report.Flags, FlagUse{Name: c.Flag, Changes: 1})
			}
		case ChangeUngated:
			report.Counts.Ungated++
			outstanding = append(outstanding, c)
		case ChangeDefinition:
			report.Counts.Definitions++
		case ChangeNoEffect:
			report.Counts.NoEffect++
		default:
			report.Counts.Unclear++
		}
		if c.Source == SourceModel {
			report.Counts.ByModel++
		}
	}
	// Ungated first, then unclear, each in diff order.
	for _, c := range report.Changes {
		if c.Status == ChangeUnclear {
			outstanding = append(outstanding, c)
		}
	}

	switch {
	case insufficient:
		report.Verdict = VerdictInsufficientInput
	case report.Counts.Ungated > 0:
		report.Verdict = VerdictUngated
	case report.Counts.Unclear > 0:
		report.Verdict = VerdictUnclear
	case report.Counts.Gated > 0:
		report.Verdict = VerdictAllGated
	default:
		report.Verdict = VerdictNoRuntimeChanges
	}
	report.Summary = summarizeFlags(report)

	var annotations []Annotation
	for _, c := range outstanding {
		if c.Line <= 0 {
			continue
		}
		severity := "warning"
		if c.Status == ChangeUnclear {
			severity = "info"
		}
		annotations = append(annotations, Annotation{
			Filename: c.Path,
			Line:     c.Line,
			Severity: severity,
			Content:  fmt.Sprintf("[%s] %s", c.Status, c.Rationale),
		})
	}

	status := StatusSuccess
	if insufficient {
		status = StatusInsufficientInput
		runLog.Warnings = append(runLog.Warnings, report.Missing...)
	}
	byRule := 0
	for _, w := range items {
		if w.change.Category != "" {
			byRule++
		}
	}
	runLog.Input = fmt.Sprintf("%d file(s), %d change(s): %d file(s) decided by path rules, %d change(s) sent to the model; %d-byte diff",
		len(files), len(items), byRule, report.Model.Asked, len(req.Diff))
	if report.Truncated {
		runLog.Input += " (prompt truncated)"
	}
	runLog.Parsed = fmt.Sprintf("verdict %s: %d gated, %d ungated, %d definition(s), %d no effect, %d unclear (%d by model); %d flag(s)",
		report.Verdict, report.Counts.Gated, report.Counts.Ungated, report.Counts.Definitions, report.Counts.NoEffect,
		report.Counts.Unclear, report.Counts.ByModel, len(report.Flags))
	if report.Model.ToolCalls > 0 {
		runLog.Parsed += fmt.Sprintf("; agent made %d tool call(s) over %d turn(s)", report.Model.ToolCalls, report.Model.Turns)
	}

	return Result{
		Status:      status,
		Body:        Body{BodyType: BodyMarkdown, BodyContent: renderFlagsReport(report, callerHint(req, report))},
		Annotations: annotations,
		Report:      report,
		Outstanding: outstanding,
		Truncated:   report.Truncated,
		Log:         runLog,
	}
}

// summarizeFlags is the verdict in one sentence.
func summarizeFlags(r FlagsReport) string {
	c := r.Counts
	switch r.Verdict {
	case VerdictInsufficientInput:
		return "Not enough input to say whether the changes are behind a flag: " + strings.Join(r.Missing, "; ") + "."
	case VerdictNoRuntimeChanges:
		if c.Definitions > 0 {
			return fmt.Sprintf("None of the %d change(s) changes logic that runs, so there is nothing to gate: %d only add definitions nothing calls yet.",
				c.Total, c.Definitions)
		}
		return fmt.Sprintf("None of the %d change(s) affects how the code behaves, so there is nothing to gate.", c.Total)
	case VerdictAllGated:
		s := fmt.Sprintf("Every logic change is behind a flag: %d gated by %s", c.Gated, flagList(r.Flags))
		if c.Definitions > 0 {
			s += fmt.Sprintf("; %d more only add definitions", c.Definitions)
		}
		if c.NoEffect > 0 {
			s += fmt.Sprintf("; %d more have no runtime effect", c.NoEffect)
		}
		return s + "."
	case VerdictUnclear:
		s := fmt.Sprintf("Nothing is known to run without a flag, but %d of %d change(s) are unclear; %d gated", c.Unclear, c.Total, c.Gated)
		if c.Definitions > 0 {
			s += fmt.Sprintf(", %d only adding definitions", c.Definitions)
		}
		return s + fmt.Sprintf(", %d with no runtime effect.", c.NoEffect)
	}
	s := fmt.Sprintf("%d of %d change(s) run without a flag", c.Ungated, c.Total)
	var rest []string
	if c.Unclear > 0 {
		rest = append(rest, fmt.Sprintf("%d unclear", c.Unclear))
	}
	if c.Gated > 0 {
		rest = append(rest, fmt.Sprintf("%d gated", c.Gated))
	}
	if c.Definitions > 0 {
		rest = append(rest, fmt.Sprintf("%d only adding definitions", c.Definitions))
	}
	if c.NoEffect > 0 {
		rest = append(rest, fmt.Sprintf("%d with no runtime effect", c.NoEffect))
	}
	if len(rest) > 0 {
		s += "; " + strings.Join(rest, ", ")
	}
	return s + "."
}

// flagList names the flags, e.g. "`new_checkout` and `beta_banner`".
func flagList(flags []FlagUse) string {
	names := make([]string, 0, len(flags))
	for _, f := range flags {
		names = append(names, codeSpan(f.Name))
	}
	switch len(names) {
	case 0:
		return "no named flag"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// renderFlagsReport is the report as markdown: the body every client can
// render, so it stands on its own. hint, when set, says how a run could have
// settled more (see callerHint).
func renderFlagsReport(r FlagsReport, hint string) string {
	var b strings.Builder
	b.WriteString("**" + r.Summary + "**\n")

	section := func(title, status string) {
		var changes []FlagChange
		for _, c := range r.Changes {
			if c.Status == status {
				changes = append(changes, c)
			}
		}
		if len(changes) == 0 {
			return
		}
		b.WriteString(fmt.Sprintf("\n### %s (%d)\n", title, len(changes)))
		for _, c := range changes {
			b.WriteString("- " + describeChange(c) + "\n")
		}
	}
	section("Runs without a flag", ChangeUngated)
	section("Unclear", ChangeUnclear)
	section("Behind a flag", ChangeGated)
	section("New definitions (judged where they're called)", ChangeDefinition)
	section("No runtime effect", ChangeNoEffect)

	if len(r.Flags) > 0 {
		b.WriteString("\n### Flags\n")
		for _, f := range r.Flags {
			b.WriteString(fmt.Sprintf("- %s gates %d change(s)\n", codeSpan(f.Name), f.Changes))
		}
	}

	var notes []string
	if r.Truncated {
		notes = append(notes, "Some changes didn't fit the model's prompt, so no change is called gated or free of runtime effect on the model's word.")
	}
	if r.Model.Note != "" {
		notes = append(notes, r.Model.Note)
	} else if r.Model.Consulted {
		notes = append(notes, fmt.Sprintf("The model (%s) judged %d change(s); every flag it named was checked against the code it was shown. Tests, docs and lockfiles are decided by path.",
			strings.TrimSpace(r.Model.Provider+" "+r.Model.Model), r.Model.Asked))
		if hint != "" {
			notes = append(notes, hint)
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n---\n")
		for _, n := range notes {
			b.WriteString("_" + n + "_\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// canLookUpCallers reports whether the model runs as an agent with the tools
// to find a changed function's callers and read around them.
func canLookUpCallers(req Request) bool {
	return req.Mode == config.AIModeAgent && req.Agent != nil && req.SearchCode != nil && req.ReadFile != nil
}

// callerHint is a note for a report whose model couldn't search for callers
// and called a change ungated or unclear — the verdicts a look at a changed
// function's callers could turn into gated. "" when there is nothing to say.
func callerHint(req Request, r FlagsReport) string {
	if !r.Model.Consulted || canLookUpCallers(req) {
		return ""
	}
	open := false
	for _, c := range r.Changes {
		if c.Source == SourceModel && (c.Status == ChangeUngated || c.Status == ChangeUnclear) {
			open = true
			break
		}
	}
	switch {
	case !open:
		return ""
	case req.Mode == config.AIModeAgent:
		return "The model couldn't search the repository for a changed function's callers, to see whether they are all behind a flag: that needs a local clone under RepoLocation."
	}
	return "The model saw only the diff, so it couldn't check whether a changed function's callers are all behind a flag. Mode = \"agent\" lets it look them up in a local clone under RepoLocation."
}

// describeChange is one change as a markdown list entry.
func describeChange(c FlagChange) string {
	where := c.Path
	if c.Line > 0 {
		where = fmt.Sprintf("%s:%d", c.Path, c.Line)
	}
	s := "**" + where + "**"
	switch {
	case c.DeletedFile:
		s += " (deleted file)"
	case c.NewFile:
		s += " (new file)"
	}
	if c.Category != "" {
		return s + fmt.Sprintf(" — %s _(path rule)_", categoryLabel(c.Category))
	}
	if c.Added > 0 || c.Removed > 0 {
		s += fmt.Sprintf(" +%d −%d", c.Added, c.Removed)
	}
	if c.Context != "" {
		s += " in " + codeSpan(c.Context)
	}
	if c.Flag != "" {
		s += " — behind " + codeSpan(c.Flag)
	}
	source := "model"
	if c.Source != SourceModel {
		source = "not judged"
	}
	return s + fmt.Sprintf(" _(%s)_  \n  %s", source, c.Rationale)
}

// codeSpan wraps s in backticks, dropping any it contains.
func codeSpan(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "'") + "`"
}

// searchCodeTool lets an agent search the repository at the PR head, e.g. for
// the callers of a function the diff changes.
func searchCodeTool(search func(ctx context.Context, query string) (string, error)) Tool {
	return Tool{
		Name:        "search_code",
		Description: "Find the lines of the repository, as it is at the pull request's head revision, that contain a fixed string (case-sensitive), e.g. a function's name to find its callers. Returns one path:line:text per match.",
		Arguments:   `{"query": "the exact text to find"}`,
		Call: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.Query) == "" {
				return "", fmt.Errorf(`expected {"query": "<text to find>"}`)
			}
			out, err := search(ctx, a.Query)
			if err != nil {
				return "", err
			}
			return truncateText(out, maxToolResultBytes), nil
		},
	}
}
