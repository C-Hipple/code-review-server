package main

import (
	"context"
	"crs/cmd/internal/pluginkit"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The review runs in two agentic phases. The first reads the guide and checks
// the diff against it, submitting candidate findings; the second takes each
// candidate back to the guide and the diff and confirms or rejects it — a
// rule that exists but governs other code (a frontend rule on a backend file,
// say) is the false positive it is there to catch. Only confirmed findings
// become annotations.

const (
	// maxCandidates bounds the first phase's findings, before validation
	// thins them out.
	maxCandidates = 25
	// maxAnnotations is how many confirmed findings are annotated in the diff.
	// A style guide can be broken on a lot of lines at once, and a diff buried
	// in flags is unreadable, so the worst offenders are worth more than an
	// exhaustive list. The report lists them all.
	maxAnnotations = 10

	findTurns     = 12
	validateTurns = 10

	submitFindingsTool = "submit_findings"
	submitVerdictsTool = "submit_verdicts"

	verdictConfirmed = "confirmed"
	verdictRejected  = "rejected"
)

// finding is a candidate violation from the first phase.
type finding struct {
	Filename string `json:"filename"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	// Guide and Rule say what the line breaks: the guide file, and the rule
	// as the guide states it.
	Guide   string `json:"guide"`
	Rule    string `json:"rule"`
	Content string `json:"content"`
}

type findingsAnswer struct {
	Findings []finding `json:"findings"`
	Notes    string    `json:"notes"`
}

type verdict struct {
	ID       int    `json:"id"`
	Verdict  string `json:"verdict"`
	Reason   string `json:"reason"`
	Severity string `json:"severity"`
	Content  string `json:"content"`
}

type verdictsAnswer struct {
	Verdicts   []verdict `json:"verdicts"`
	Assessment string    `json:"assessment"`
}

// reviewInput is what both phases look at.
type reviewInput struct {
	diff     string
	metadata pluginkit.PRMetadata
	guide    *styleGuide
	// headLines is the diff's annotatable lines by file, which a finding is
	// checked against before the model is believed about where it is.
	headLines map[string]map[int]pluginkit.HeadLine
}

func newReviewInput(diff string, metadata pluginkit.PRMetadata, guide *styleGuide) reviewInput {
	return reviewInput{diff: diff, metadata: metadata, guide: guide, headLines: pluginkit.DiffHeadLines(diff)}
}

func severitySchema() *pluginkit.Schema {
	return &pluginkit.Schema{Type: "STRING", Enum: []string{pluginkit.SeverityInfo, pluginkit.SeverityWarning, pluginkit.SeverityError}}
}

func findingsTool() pluginkit.ChatTool {
	return pluginkit.ChatTool{
		Name:        submitFindingsTool,
		Description: "Submit the style guide violations found in the diff. Call it once, when you are done; an empty findings list is a valid answer.",
		Parameters: &pluginkit.Schema{
			Type: "OBJECT",
			Properties: map[string]*pluginkit.Schema{
				"findings": {
					Type:        "ARRAY",
					Description: fmt.Sprintf("Violations, at most %d, most important first.", maxCandidates),
					Items: &pluginkit.Schema{
						Type: "OBJECT",
						Properties: map[string]*pluginkit.Schema{
							"filename": {Type: "STRING", Description: "Path of the file, copied exactly from the Files list."},
							"line":     {Type: "INTEGER", Description: "The number shown beside the line in the diff."},
							"severity": severitySchema(),
							"guide":    {Type: "STRING", Description: "The guide file the rule is in, as the guide names it."},
							"rule":     {Type: "STRING", Description: "The rule broken, quoted or closely paraphrased from the guide."},
							"content":  {Type: "STRING", Description: "What is wrong with the line and how to fix it, in one or two sentences."},
						},
						PropertyOrdering: []string{"filename", "line", "severity", "guide", "rule", "content"},
						Required:         []string{"filename", "line", "severity", "guide", "rule", "content"},
					},
				},
				"notes": {Type: "STRING", Description: "A sentence or two on how the diff otherwise follows the guide."},
			},
			PropertyOrdering: []string{"findings", "notes"},
			Required:         []string{"findings", "notes"},
		},
	}
}

func verdictsTool() pluginkit.ChatTool {
	return pluginkit.ChatTool{
		Name:        submitVerdictsTool,
		Description: "Submit a verdict on every candidate finding. Call it once, when you are done.",
		Parameters: &pluginkit.Schema{
			Type: "OBJECT",
			Properties: map[string]*pluginkit.Schema{
				"verdicts": {
					Type:        "ARRAY",
					Description: "One verdict per candidate finding.",
					Items: &pluginkit.Schema{
						Type: "OBJECT",
						Properties: map[string]*pluginkit.Schema{
							"id":       {Type: "INTEGER", Description: "The finding's id."},
							"verdict":  {Type: "STRING", Enum: []string{verdictConfirmed, verdictRejected}},
							"reason":   {Type: "STRING", Description: "Why, in one sentence: the rule's scope, what the line does."},
							"severity": severitySchema(),
							"content":  {Type: "STRING", Description: "For a confirmed finding, the remark to show on the line (the original, or a corrected one)."},
						},
						PropertyOrdering: []string{"id", "verdict", "reason", "severity", "content"},
						Required:         []string{"id", "verdict", "reason", "severity", "content"},
					},
				},
				"assessment": {Type: "STRING", Description: "A short markdown assessment of the diff's compliance with the guide, given the confirmed findings."},
			},
			PropertyOrdering: []string{"verdicts", "assessment"},
			Required:         []string{"verdicts", "assessment"},
		},
	}
}

// context is the part of the prompt both phases share: the PR, its diff and
// the guide.
func (in reviewInput) context() string {
	rendered, fileList := pluginkit.PromptDiff(in.diff)
	return fmt.Sprintf(`## PR Context

%s
## Files

%s

## Diff

%s

%s

## Style Guide

%s`, in.metadata.Context(), fileList, rendered, pluginkit.DiffLegend, in.guide.promptSection())
}

const findSystem = `You are a code reviewer checking a pull request against a team's style guide.
You have tools to search and read the guide. Work until you have checked every
changed file against the rules that apply to it, then submit your findings with
the submit_findings tool. Only that call is read as your answer.`

func (in reviewInput) findPrompt() string {
	return in.context() + fmt.Sprintf(`

## Instructions

Check the lines this PR adds against the style guide, and submit the lines that
break it.

- Find the rules that bear on each changed file. A large guide is split by
  language, framework, layer or directory; search it for the file's language
  and area, and read the sections that apply, before deciding a file is clean.
- Respect each rule's scope. A rule written for one language, layer (frontend,
  backend, tests, scripts) or directory does not apply to files outside it.
- Only flag what the guide actually says. Your own taste, or conventions the
  guide doesn't state, are not findings.
- Only flag lines marked "+": the PR is not to blame for code it didn't touch.
- filename is one of the paths under Files, copied exactly, and line is the
  number shown beside the line. Removed lines have no number and cannot be
  flagged.
- severity: %s for a nit, %s for a clear violation, %s for one that breaks a
  rule the guide states as a hard requirement.
- At most %d findings, most important first. An empty list is a valid answer,
  and a clean diff should get one.
`, pluginkit.SeverityInfo, pluginkit.SeverityWarning, pluginkit.SeverityError, maxCandidates)
}

const validateSystem = `You are verifying another reviewer's style guide findings on a pull request.
Be skeptical: a finding is only worth showing the author if it is correct and
relevant. You have tools to search and read the guide. Check each finding, then
submit a verdict on every one with the submit_verdicts tool. Only that call is
read as your answer.`

func (in reviewInput) validatePrompt(candidates []finding) string {
	var b strings.Builder
	for i, f := range candidates {
		fmt.Fprintf(&b, "\n### Finding %d\n- location: %s:%d\n", i+1, f.Filename, f.Line)
		if line, ok := in.headLines[f.Filename][f.Line]; ok {
			mark := " "
			if line.Added {
				mark = "+"
			}
			fmt.Fprintf(&b, "- the line: `%s %s`\n", mark, line.Content)
		}
		fmt.Fprintf(&b, "- severity: %s\n- guide file: %s\n- rule: %s\n- remark: %s\n", f.Severity, f.Guide, f.Rule, f.Content)
	}

	return in.context() + fmt.Sprintf(`

## Candidate Findings
%s
## Instructions

For each finding, read the rule where the guide states it and decide:

1. Does the rule exist? It must be in the guide file named (or elsewhere in the
   guide), saying what the finding says it does. Reject a rule the guide
   doesn't state, or states differently.
2. Does it apply to this file? Rules are scoped by language, framework, layer
   (frontend, backend, tests, scripts), directory or file type — read the
   headings and text around the rule for its scope. Reject a rule applied
   outside it: a frontend rule on backend code, a Python rule on Go, a test
   convention on production code.
3. Does the line break it? Look at the line and its context in the diff.
   Reject a finding the line doesn't actually break, or one about a line the
   PR didn't add.
4. Is it a duplicate? Keep one of several findings on the same line for the
   same rule, and reject the rest.

Confirm the finding only when all four hold. For a confirmed finding set
severity to %s for a nit, %s for a clear violation or %s for a hard
requirement, and content to the remark to show on the line — the original, or
a corrected one. Give a verdict on every id from 1 to %d.
`, b.String(), pluginkit.SeverityInfo, pluginkit.SeverityWarning, pluginkit.SeverityError, len(candidates))
}

// checkLocation says why a finding can't be anchored where it says, or
// returns nil. A diff with no parsed files can't anchor anything, and its
// findings go in the report alone.
func (in reviewInput) checkLocation(f finding) error {
	if len(in.headLines) == 0 {
		return nil
	}
	lines, ok := in.headLines[f.Filename]
	if !ok {
		return fmt.Errorf("%q is not one of the files under Files", f.Filename)
	}
	line, ok := lines[f.Line]
	if !ok {
		return fmt.Errorf("%s has no line %d in the diff", f.Filename, f.Line)
	}
	if !line.Added {
		return fmt.Errorf("%s:%d is not a line this PR adds", f.Filename, f.Line)
	}
	return nil
}

func normalizeFinding(f finding) finding {
	f.Filename = strings.TrimPrefix(strings.TrimSpace(f.Filename), "./")
	f.Severity = normalizeSeverity(f.Severity)
	f.Guide = strings.TrimSpace(f.Guide)
	f.Rule = strings.TrimSpace(f.Rule)
	f.Content = strings.TrimSpace(f.Content)
	return f
}

func normalizeSeverity(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case pluginkit.SeverityWarning, pluginkit.SeverityError:
		return s
	default:
		return pluginkit.SeverityInfo
	}
}

// acceptFindings parses the first phase's answer. Findings it can't anchor
// are sent back to be fixed, or on the last turn dropped.
func (in reviewInput) acceptFindings(out *findingsAnswer) func(json.RawMessage, bool) error {
	return func(args json.RawMessage, last bool) error {
		var answer findingsAnswer
		if err := json.Unmarshal(args, &answer); err != nil {
			return fmt.Errorf("the arguments are not the object submit_findings takes: %v", err)
		}
		var kept []finding
		var problems []string
		for i, f := range answer.Findings {
			f = normalizeFinding(f)
			if f.Content == "" {
				problems = append(problems, fmt.Sprintf("finding %d has no content", i+1))
				continue
			}
			if err := in.checkLocation(f); err != nil {
				problems = append(problems, fmt.Sprintf("finding %d: %v", i+1, err))
				continue
			}
			kept = append(kept, f)
		}
		if len(kept) > maxCandidates {
			problems = append(problems, fmt.Sprintf("%d findings is more than the %d allowed; keep the most important", len(kept), maxCandidates))
			kept = kept[:maxCandidates]
		}
		if len(problems) > 0 && !last {
			return errors.New(strings.Join(problems, "; ") + ". Correct or drop these findings")
		}
		*out = findingsAnswer{Findings: kept, Notes: strings.TrimSpace(answer.Notes)}
		return nil
	}
}

// acceptVerdicts parses the second phase's answer, which must cover every
// candidate. On the last turn a candidate without a verdict counts as
// rejected: an unchecked finding isn't shown as a confirmed one.
func acceptVerdicts(candidates int, out *verdictsAnswer) func(json.RawMessage, bool) error {
	return func(args json.RawMessage, last bool) error {
		var answer verdictsAnswer
		if err := json.Unmarshal(args, &answer); err != nil {
			return fmt.Errorf("the arguments are not the object submit_verdicts takes: %v", err)
		}
		byID := map[int]verdict{}
		for _, v := range answer.Verdicts {
			if v.ID < 1 || v.ID > candidates {
				continue
			}
			v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
			if v.Verdict != verdictConfirmed {
				v.Verdict = verdictRejected
			}
			v.Severity = normalizeSeverity(v.Severity)
			v.Reason = strings.TrimSpace(v.Reason)
			v.Content = strings.TrimSpace(v.Content)
			byID[v.ID] = v
		}
		var missing []string
		verdicts := make([]verdict, 0, candidates)
		for id := 1; id <= candidates; id++ {
			v, ok := byID[id]
			if !ok {
				missing = append(missing, fmt.Sprint(id))
				v = verdict{ID: id, Verdict: verdictRejected, Reason: "not checked by the validator"}
			}
			verdicts = append(verdicts, v)
		}
		if len(missing) > 0 && !last {
			return fmt.Errorf("no verdict for finding(s) %s; give one for every id from 1 to %d", strings.Join(missing, ", "), candidates)
		}
		*out = verdictsAnswer{Verdicts: verdicts, Assessment: strings.TrimSpace(answer.Assessment)}
		return nil
	}
}

// newChat starts a conversation for one phase; tests replace it.
var newChat = func(model *pluginkit.Model, system string, tools []pluginkit.ChatTool) conversation {
	return model.NewChat(system, tools)
}

// reviewResult is what the two phases found.
type reviewResult struct {
	candidates []finding
	notes      string
	// verdicts is nil when the validation phase didn't run or failed.
	verdicts      []verdict
	assessment    string
	validationErr error
	find          agentStats
	validate      agentStats
}

// findViolations runs the first phase.
func findViolations(ctx context.Context, model *pluginkit.Model, in reviewInput) (findingsAnswer, agentStats, error) {
	var answer findingsAnswer
	task := agentTask{
		prompt:   in.findPrompt(),
		tools:    guideTools(in.guide),
		submit:   findingsTool(),
		accept:   in.acceptFindings(&answer),
		maxTurns: findTurns,
	}
	stats, err := runAgent(ctx, newChat(model, findSystem, task.chatTools()), task)
	return answer, stats, err
}

// validateFindings runs the second phase.
func validateFindings(ctx context.Context, model *pluginkit.Model, in reviewInput, candidates []finding) (verdictsAnswer, agentStats, error) {
	var answer verdictsAnswer
	task := agentTask{
		prompt:   in.validatePrompt(candidates),
		tools:    guideTools(in.guide),
		submit:   verdictsTool(),
		accept:   acceptVerdicts(len(candidates), &answer),
		maxTurns: validateTurns,
	}
	stats, err := runAgent(ctx, newChat(model, validateSystem, task.chatTools()), task)
	return answer, stats, err
}

// confirmed pairs a candidate with the verdict that confirmed it.
type confirmed struct {
	finding
	reason string
}

var severityRank = map[string]int{pluginkit.SeverityError: 0, pluginkit.SeverityWarning: 1, pluginkit.SeverityInfo: 2}

// confirmedFindings are the candidates the validator confirmed, carrying its
// severity and remark, worst first.
func (r reviewResult) confirmedFindings() (kept []confirmed, rejected []confirmed) {
	for i, v := range r.verdicts {
		f := r.candidates[i]
		if v.Verdict != verdictConfirmed {
			rejected = append(rejected, confirmed{finding: f, reason: v.Reason})
			continue
		}
		f.Severity = v.Severity
		if v.Content != "" {
			f.Content = v.Content
		}
		kept = append(kept, confirmed{finding: f, reason: v.Reason})
	}
	sort.SliceStable(kept, func(i, j int) bool { return severityRank[kept[i].Severity] < severityRank[kept[j].Severity] })
	return kept, rejected
}

func (f finding) location() string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.Filename, f.Line)
	}
	return f.Filename
}

func (f finding) source() string {
	switch {
	case f.Guide != "" && f.Rule != "":
		return fmt.Sprintf("%s: %s", f.Guide, f.Rule)
	case f.Guide != "":
		return f.Guide
	default:
		return f.Rule
	}
}

// response builds the plugin response: a markdown report, and an annotation
// on each of the worst confirmed findings.
func (r reviewResult) response(model string, guide *styleGuide) pluginkit.Response {
	var b strings.Builder
	b.WriteString("## Style Guide Review\n\n")

	var annotations []pluginkit.Annotation
	switch {
	case len(r.candidates) == 0:
		b.WriteString("No violations found.\n")
		if r.notes != "" {
			b.WriteString("\n" + r.notes + "\n")
		}
	case r.verdicts == nil:
		// Without validation the candidates are only listed: an unchecked
		// finding in the diff is the noise the second phase exists to cut.
		fmt.Fprintf(&b, "The findings below could not be validated (%v), so none is annotated in the diff. Treat them as unconfirmed.\n\n", r.validationErr)
		fmt.Fprintf(&b, "### Unconfirmed findings (%d)\n\n", len(r.candidates))
		for _, f := range r.candidates {
			fmt.Fprintf(&b, "- `%s` **%s**: %s\n  - _%s_\n", f.location(), f.Severity, f.Content, f.source())
		}
	default:
		kept, rejected := r.confirmedFindings()
		if r.assessment != "" {
			b.WriteString(r.assessment + "\n\n")
		}
		if len(kept) == 0 {
			b.WriteString("### Violations\n\nNone confirmed.\n")
		} else {
			fmt.Fprintf(&b, "### Violations (%d)\n\n", len(kept))
			for _, f := range kept {
				fmt.Fprintf(&b, "- `%s` **%s**: %s\n  - _%s_\n", f.location(), f.Severity, f.Content, f.source())
			}
			if len(kept) > maxAnnotations {
				fmt.Fprintf(&b, "\nThe %d most severe are annotated in the diff.\n", maxAnnotations)
			}
		}
		if len(rejected) > 0 {
			fmt.Fprintf(&b, "\n### Dismissed on validation (%d)\n\n", len(rejected))
			for _, f := range rejected {
				fmt.Fprintf(&b, "- `%s`: %s\n  - dismissed: %s\n", f.location(), f.Content, f.reason)
			}
		}
		for _, f := range kept {
			if len(annotations) == maxAnnotations {
				break
			}
			content := f.Content
			if f.Guide != "" {
				content += " (" + f.Guide + ")"
			}
			annotations = append(annotations, pluginkit.Annotation{Filename: f.Filename, Line: f.Line, Severity: f.Severity, Content: content})
		}
	}

	files := "1 guide file"
	if len(guide.Files) != 1 {
		files = fmt.Sprintf("%d guide files", len(guide.Files))
	}
	fmt.Fprintf(&b, "\n---\n_Checked against %s in `%s` by %s: %s._\n", files, guide.Source, model, r.stats())
	return pluginkit.MarkdownResponse(strings.TrimSpace(b.String()), annotations)
}

func (r reviewResult) stats() string {
	s := fmt.Sprintf("review %d turns / %d tool calls", r.find.Turns, r.find.ToolCalls)
	if r.validate.Turns > 0 {
		s += fmt.Sprintf(", validation %d turns / %d tool calls", r.validate.Turns, r.validate.ToolCalls)
	}
	return s
}
