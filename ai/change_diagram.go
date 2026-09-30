package ai

import (
	"context"
	"crs/llm"
	"fmt"
	"slices"
	"strings"
)

// ChangeDiagramID is the ID of the change-diagram feature.
const ChangeDiagramID = "change-diagram"

// maxMermaidBytes is the most diagram source a result may carry: Mermaid's
// own default limit (maxTextSize), beyond which it refuses to render.
const maxMermaidBytes = 50000

// ChangeDiagram draws what a PR changes as a Mermaid flowchart: the
// components it adds, changes or removes, and how they connect.
//
// The result's report carries the diagram's raw Mermaid source, which clients
// render themselves — the web client in a modal, Emacs in a mermaid-mode
// buffer. Its body wraps the same source in a ```mermaid fence, so a client
// that only renders markdown bodies still shows it.
//
// The model's answer is checked to be a diagram — its first statement must
// declare one of the types that can describe code — but whether it parses is
// decided where it is rendered. On the way, the server keeps the diagram alone
// (a fence or chatter around it is dropped), removes click, link and callback
// statements and %%{init}%% directives, which have no place in text a PR's
// author can steer, and, for a flowchart, defines the added / changed /
// removed classes the prompt asks the model to use, so every diagram is
// colored alike.
//
// The diagram depends on the code alone, so it is keyed by the head SHA only.
type ChangeDiagram struct{}

func (ChangeDiagram) ID() string   { return ChangeDiagramID }
func (ChangeDiagram) Name() string { return "Change diagram" }

func (ChangeDiagram) Description() string {
	return "Draws a Mermaid diagram of what the PR changes: the components it adds, changes or removes, " +
		"and how they connect."
}

// CodeOnly keys the feature's results by the head SHA alone.
func (ChangeDiagram) CodeOnly() bool { return true }

// ChangeDiagramReport is the typed report change-diagram stores as the
// result's "report".
type ChangeDiagramReport struct {
	// Mermaid is the diagram's raw Mermaid source.
	Mermaid string `json:"mermaid"`
	// DiagramType is the keyword the diagram declares itself with, e.g.
	// "flowchart".
	DiagramType string `json:"diagram_type"`
}

func (ChangeDiagram) Run(ctx context.Context, req Request) (Result, error) {
	in, err := readDiffInput(req.Diff)
	if err != nil {
		return Result{}, err
	}
	runLog := RunLog{Input: in.describe()}
	title, _ := prTitleAuthor(req)

	text, err := req.Model.Generate(ctx, buildChangeDiagramPrompt(title, in))
	if err != nil {
		return Result{Log: runLog}, err
	}
	source, kind := extractMermaid(text)
	if source == "" {
		runLog.Parsed = "no Mermaid diagram"
		runLog.ResponseSnippet = llm.Snippet(text)
		return Result{Log: runLog}, &llm.CallError{Stage: llm.StageParse, Err: fmt.Errorf("response contained no Mermaid diagram")}
	}
	if isFlowchart(kind) {
		source = defineChangeClasses(source)
	}
	if len(source) > maxMermaidBytes {
		runLog.Parsed = fmt.Sprintf("a %d-byte %s", len(source), kind)
		runLog.ResponseSnippet = llm.Snippet(text)
		return Result{Log: runLog}, &llm.CallError{Stage: llm.StageParse,
			Err: fmt.Errorf("the diagram is %d bytes, more than Mermaid renders (%d)", len(source), maxMermaidBytes)}
	}
	runLog.Parsed = fmt.Sprintf("a %d-line %s", strings.Count(source, "\n")+1, kind)
	return Result{
		Body:      Body{BodyType: BodyMarkdown, BodyContent: "```mermaid\n" + source + "\n```\n"},
		Report:    ChangeDiagramReport{Mermaid: source, DiagramType: kind},
		Truncated: in.truncated,
		Log:       runLog,
	}, nil
}

// buildChangeDiagramPrompt asks the model for a flowchart of the diff.
func buildChangeDiagramPrompt(title string, in diffInput) string {
	var b strings.Builder
	b.WriteString(`You are drawing a diagram of a pull request for a code reviewer, so they can see at a glance what the PR changes and how the pieces fit together before they read the diff.

Draw a Mermaid flowchart of the changes:
- Nodes are what the PR touches: packages, modules or files, and the functions, types, endpoints, jobs, tables or UI components in them that it adds, changes or removes. Group the nodes of one file or package in a subgraph when that makes the diagram clearer.
- Edges show how they relate: calls, data flow, reads and writes, events, rendering. Label an edge when the relation isn't obvious.
- Mark what the PR does to a node by appending :::added, :::changed or :::removed to its definition. Unchanged code may appear, unmarked, where it is needed to show how the change fits in.
- Keep it readable: about 30 nodes at most. Leave out tests, documentation and lockfiles unless they are what the PR is about.

The diagram must parse:
- Start with "flowchart TD", or "flowchart LR" if it reads better.
- Give every node a short alphanumeric ID and put its label in double quotes, e.g. api["server/api.go: HandleLogin"]:::changed. Never put a double quote inside a label.
- Do not define the added, changed and removed classes; they are defined for you. Do not use click, link or callback statements, %%{init}%% directives, or HTML in labels.
`)
	if title != "" {
		b.WriteString(fmt.Sprintf("\nPull request title: %s\n", title))
	}
	b.WriteString(fmt.Sprintf(`
Files in this diff:
%s
Full diff:
%s
`, in.fileList, in.text))
	b.WriteString(`
Respond with ONLY the Mermaid source, starting with the flowchart line. No code fences, no commentary.`)
	return b.String()
}

// flowDirections are the directions a flowchart's header may name.
var flowDirections = []string{"TB", "TD", "BT", "RL", "LR"}

// diagramTypes are the diagram declarations a change diagram may start with,
// besides flowchart and graph: the types that can describe code. A chart type
// such as pie or gantt can't, and its keyword could as easily open a line of
// prose.
var diagramTypes = []string{
	"sequenceDiagram", "classDiagram", "classDiagram-v2", "stateDiagram", "stateDiagram-v2",
	"erDiagram", "C4Context", "C4Container", "C4Component", "C4Dynamic", "C4Deployment",
	"block-beta", "architecture-beta", "mindmap",
}

// diagramHeader reports whether line declares a diagram, and of which type.
// A flowchart's header may name a direction; any other type's is the keyword
// alone.
func diagramHeader(line string) (string, bool) {
	fields := strings.Fields(strings.TrimSuffix(strings.TrimSpace(line), ";"))
	if len(fields) == 0 {
		return "", false
	}
	switch kind := fields[0]; {
	case isFlowchart(kind):
		if len(fields) == 1 || (len(fields) == 2 && slices.Contains(flowDirections, fields[1])) {
			return kind, true
		}
	case slices.Contains(diagramTypes, kind):
		if len(fields) == 1 {
			return kind, true
		}
	}
	return "", false
}

func isFlowchart(kind string) bool { return kind == "flowchart" || kind == "graph" }

// fence is one fenced code block of a model's answer.
type fence struct {
	lang string
	body string
}

// fencedBlocks returns the fenced code blocks in text, in order. A fence left
// open runs to the end of the text, since an answer cut short still holds the
// diagram's start.
func fencedBlocks(text string) []fence {
	var (
		blocks []fence
		open   string // the marker of the fence being read; "" outside one
		cur    fence
		lines  []string
	)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if open == "" {
			for _, marker := range []string{"```", "~~~"} {
				if strings.HasPrefix(trimmed, marker) {
					open = marker
					cur = fence{}
					if info := strings.Fields(strings.TrimLeft(trimmed, marker[:1])); len(info) > 0 {
						cur.lang = strings.ToLower(info[0])
					}
					lines = nil
					break
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, open) && strings.Trim(trimmed, open[:1]) == "" {
			cur.body = strings.Join(lines, "\n")
			blocks = append(blocks, cur)
			open = ""
			continue
		}
		lines = append(lines, line)
	}
	if open != "" {
		cur.body = strings.Join(lines, "\n")
		blocks = append(blocks, cur)
	}
	return blocks
}

// extractMermaid finds the diagram in a model's answer: a mermaid fence first,
// then any other fence holding one, then the answer itself. It returns the
// diagram's source, cleaned (see diagramFrom), and its type; "" when the
// answer holds no diagram.
func extractMermaid(text string) (string, string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var candidates []string
	blocks := fencedBlocks(text)
	for _, b := range blocks {
		if b.lang == "mermaid" {
			candidates = append(candidates, b.body)
		}
	}
	for _, b := range blocks {
		if b.lang != "mermaid" {
			candidates = append(candidates, b.body)
		}
	}
	candidates = append(candidates, text)
	for _, c := range candidates {
		if source, kind := diagramFrom(c); source != "" {
			return source, kind
		}
	}
	return "", ""
}

// diagramFrom returns the diagram in block, from its header line on, and its
// type. Whatever precedes the header goes: chatter, comments, front matter.
// So do the statements a diagram built from a PR has no business carrying: a
// click, link or callback, which makes a node a link or runs a function when
// clicked, and a %%{init}%% directive, which reconfigures the renderer.
func diagramFrom(block string) (string, string) {
	lines := strings.Split(block, "\n")
	start, kind := -1, ""
	for i, line := range lines {
		if k, ok := diagramHeader(line); ok {
			start, kind = i, k
			break
		}
	}
	if start < 0 {
		return "", ""
	}
	kept := []string{strings.TrimSpace(lines[start])}
	for _, line := range lines[start+1:] {
		if !interactiveStatement(line, kind) {
			kept = append(kept, strings.TrimRight(line, " \t"))
		}
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n"), kind
}

// interactiveStatement reports whether line is a directive, or a statement
// that makes the diagram do something when clicked: click in any diagram, and
// link or callback in a class diagram (elsewhere, either may be a node's ID).
func interactiveStatement(line, kind string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "%%{") {
		return true
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 2 {
		return false
	}
	switch fields[0] {
	case "click":
		return true
	case "link", "callback":
		return strings.HasPrefix(kind, "classDiagram")
	}
	return false
}

type changeClass struct{ name, style string }

// changeClasses are the flowchart classes that mark what the PR does to a
// node, with how each is drawn: light fills with dark text, which read on
// light and dark themes alike.
var changeClasses = []changeClass{
	{"added", "fill:#dcfce7,stroke:#16a34a,color:#14532d"},
	{"changed", "fill:#fef3c7,stroke:#d97706,color:#78350f"},
	{"removed", "fill:#fee2e2,stroke:#dc2626,color:#7f1d1d,stroke-dasharray: 5 5"},
}

// defineChangeClasses defines the added, changed and removed classes at the
// end of a flowchart, replacing any definition the model gave only those
// classes, so every diagram colors them alike.
func defineChangeClasses(source string) string {
	var kept []string
	for _, line := range strings.Split(source, "\n") {
		if !definesOnlyChangeClasses(line) {
			kept = append(kept, line)
		}
	}
	for _, c := range changeClasses {
		kept = append(kept, fmt.Sprintf("    classDef %s %s", c.name, c.style))
	}
	return strings.Join(kept, "\n")
}

// definesOnlyChangeClasses reports whether line is a classDef naming only
// classes among changeClasses.
func definesOnlyChangeClasses(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "classDef" {
		return false
	}
	for _, name := range strings.Split(fields[1], ",") {
		if !slices.ContainsFunc(changeClasses, func(c changeClass) bool { return c.name == name }) {
			return false
		}
	}
	return true
}
