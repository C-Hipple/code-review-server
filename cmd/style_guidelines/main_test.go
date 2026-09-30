package main

import (
	"bytes"
	"context"
	"crs/cmd/internal/pluginkit"
	"crs/server"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sampleDiff = `diff --git a/api/views.py b/api/views.py
index 1111111..2222222 100644
--- a/api/views.py
+++ b/api/views.py
@@ -10,3 +10,5 @@ def index():
 	return render()
+def AdminPanel():
+	return secret(42)
`

const styleGuideText = "# Style Guidelines\n\n- Use snake_case for all function names.\n"

func singleFileGuide() *styleGuide {
	return &styleGuide{Source: "/home/me/.config/style_guidelines.md", Files: []guideFile{{Path: "style_guidelines.md", Content: styleGuideText}}}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadGuideDirReadsEveryMarkdownFile(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"README.md":             "# Guide\n",
		"backend/python.md":     "# Python\n- snake_case\n",
		"frontend/react.mdx":    "# React\n- PascalCase components\n",
		"frontend/logo.png":     "not markdown",
		".drafts/unfinished.md": "# hidden\n",
		"backend/.notes.md":     "# hidden too\n",
	})

	guide, err := loadGuideDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range guide.Files {
		paths = append(paths, f.Path)
	}
	if want := []string{"README.md", "backend/python.md", "frontend/react.mdx"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("files = %v, want %v", paths, want)
	}
	if guide.Source != root {
		t.Errorf("source = %q", guide.Source)
	}

	if _, err := loadGuideDir(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no Markdown files") {
		t.Errorf("an empty directory should fail: %v", err)
	}
}

func TestLocateGuidePrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(guideDirEnv, "")

	// Nothing configured: the single file, as before.
	if path, isDir, err := locateGuide(""); err != nil || isDir || path != filepath.Join(home, ".config", "style_guidelines.md") {
		t.Errorf("default = %q, %v, %v", path, isDir, err)
	}

	defaultDir := filepath.Join(home, ".config", "style_guidelines")
	writeFiles(t, defaultDir, map[string]string{"a.md": "# A\n"})
	if path, isDir, err := locateGuide(""); err != nil || !isDir || path != defaultDir {
		t.Errorf("with ~/.config/style_guidelines/ = %q, %v, %v", path, isDir, err)
	}

	envDir := filepath.Join(home, "env-guide")
	writeFiles(t, envDir, map[string]string{"a.md": "# A\n"})
	t.Setenv(guideDirEnv, "~/env-guide")
	if path, _, err := locateGuide(""); err != nil || path != envDir {
		t.Errorf("with %s = %q, %v", guideDirEnv, path, err)
	}

	flagDir := filepath.Join(home, "flag-guide")
	writeFiles(t, flagDir, map[string]string{"a.md": "# A\n"})
	if path, _, err := locateGuide(flagDir); err != nil || path != flagDir {
		t.Errorf("with the flag = %q, %v", path, err)
	}

	// A directory named explicitly has to exist.
	if _, _, err := locateGuide(filepath.Join(home, "missing")); err == nil {
		t.Error("a missing --style-guide-dir should fail")
	}
	if _, _, err := locateGuide(filepath.Join(envDir, "a.md")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("a file given as the directory should fail: %v", err)
	}
}

func TestPromptSectionInlinesASmallGuideAndIndexesALargeOne(t *testing.T) {
	small := &styleGuide{Files: []guideFile{{Path: "backend/python.md", Content: "# Python\n- snake_case\n"}}}
	if section := small.promptSection(); !strings.Contains(section, "### Guide file: backend/python.md") || !strings.Contains(section, "- snake_case") {
		t.Errorf("small guide not inlined:\n%s", section)
	}

	large := &styleGuide{Files: []guideFile{
		{Path: "backend/python.md", Content: "# Python\n\n## Naming\n- snake_case\n" + strings.Repeat("filler\n", inlineGuideBytes/7)},
		{Path: "frontend/react.md", Content: "# React\n#hashtag, not a heading\n"},
	}}
	section := large.promptSection()
	for _, want := range []string{"2 files, too large", "- backend/python.md (", "- Python", "  - Naming", "- frontend/react.md", "- React"} {
		if !strings.Contains(section, want) {
			t.Errorf("index missing %q:\n%s", want, section)
		}
	}
	if strings.Contains(section, "filler") || strings.Contains(section, "hashtag") {
		t.Errorf("index should carry headings only:\n%s", section)
	}
}

func TestGuideReadAndSearch(t *testing.T) {
	guide := &styleGuide{Files: []guideFile{
		{Path: "backend/python.md", Content: "# Python\n- Use snake_case.\n- Type hints required.\n"},
		{Path: "frontend/react.md", Content: "# React\n- Components are PascalCase.\n"},
	}}

	out, err := guide.read("./backend/python.md", 2, 3)
	if err != nil || !strings.Contains(out, "    2 | - Use snake_case.") || !strings.Contains(out, "    3 | - Type hints") || strings.Contains(out, "# Python") {
		t.Errorf("read = %q, %v", out, err)
	}
	if _, err := guide.read("nope.md", 0, 0); err == nil {
		t.Error("reading a missing file should fail")
	}

	out, err = guide.search("case", "")
	if err != nil || !strings.Contains(out, "backend/python.md:2: - Use snake_case.") || !strings.Contains(out, "frontend/react.md:2:") {
		t.Errorf("search = %q, %v", out, err)
	}
	out, _ = guide.search("case", "frontend/react.md")
	if strings.Contains(out, "python") {
		t.Errorf("a search limited to a file found other files: %q", out)
	}
	// A pattern that doesn't compile is searched for literally.
	if out, err := guide.search("snake_case.(", ""); err != nil || !strings.Contains(out, "no matches") {
		t.Errorf("literal search = %q, %v", out, err)
	}
}

// scripted is a conversation whose model side is a list of turns.
type scripted struct {
	turns    []pluginkit.ChatTurn
	next     int
	users    []string
	results  [][]pluginkit.ToolResult
	requires []string
}

func (s *scripted) AddUser(text string) { s.users = append(s.users, text) }
func (s *scripted) AddToolResults(results []pluginkit.ToolResult) {
	s.results = append(s.results, results)
}
func (s *scripted) Next(_ context.Context, require string) (pluginkit.ChatTurn, error) {
	s.requires = append(s.requires, require)
	if s.next == len(s.turns) {
		return pluginkit.ChatTurn{}, errors.New("script exhausted")
	}
	s.next++
	return s.turns[s.next-1], nil
}

func call(name, args string) pluginkit.ChatTurn {
	return pluginkit.ChatTurn{ToolCalls: []pluginkit.ToolCall{{ID: name, Name: name, Args: json.RawMessage(args)}}}
}

func TestRunAgentRunsToolsUntilTheAnswerIsAccepted(t *testing.T) {
	conv := &scripted{turns: []pluginkit.ChatTurn{
		call("echo", `{"s": "hi"}`),
		{Text: "I think I'm done."},
		call("nope", `{}`),
		call("submit", `{"ok": false}`),
		call("submit", `{"ok": true}`),
	}}
	var accepted bool
	task := agentTask{
		prompt: "the task",
		tools: []tool{{ChatTool: pluginkit.ChatTool{Name: "echo"}, run: func(args json.RawMessage) (string, error) {
			return "echo " + string(args), nil
		}}},
		submit: pluginkit.ChatTool{Name: "submit"},
		accept: func(args json.RawMessage, last bool) error {
			var a struct{ OK bool }
			json.Unmarshal(args, &a)
			if !a.OK {
				return errors.New("not ok")
			}
			accepted = true
			return nil
		},
		maxTurns: 6,
	}

	stats, err := runAgent(context.Background(), conv, task)
	if err != nil || !accepted {
		t.Fatalf("runAgent = %v, accepted %v", err, accepted)
	}
	if stats.Turns != 5 || stats.ToolCalls != 4 {
		t.Errorf("stats = %+v", stats)
	}
	if conv.users[0] != "the task" || len(conv.users) != 2 || !strings.Contains(conv.users[1], "Only a call to submit") {
		t.Errorf("user messages = %q", conv.users)
	}
	if got := conv.results[0][0].Content; got != `echo {"s": "hi"}` {
		t.Errorf("tool result = %q", got)
	}
	if got := conv.results[1][0].Content; !strings.Contains(got, `no tool named "nope"`) {
		t.Errorf("unknown tool result = %q", got)
	}
	if got := conv.results[2][0].Content; !strings.Contains(got, "not accepted: not ok") {
		t.Errorf("rejected answer result = %q", got)
	}
	for _, r := range conv.requires {
		if r != "" {
			t.Errorf("no turn before the last should force a tool: %q", conv.requires)
		}
	}
}

func TestRunAgentForcesTheAnswerOnTheLastTurn(t *testing.T) {
	conv := &scripted{turns: []pluginkit.ChatTurn{
		call("echo", `{}`),
		call("submit", `{}`),
	}}
	var sawLast bool
	task := agentTask{
		tools:  []tool{{ChatTool: pluginkit.ChatTool{Name: "echo"}, run: func(json.RawMessage) (string, error) { return "", nil }}},
		submit: pluginkit.ChatTool{Name: "submit"},
		accept: func(_ json.RawMessage, last bool) error {
			sawLast = last
			return nil
		},
		maxTurns: 2,
	}
	if _, err := runAgent(context.Background(), conv, task); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(conv.requires, []string{"", "submit"}) || !sawLast {
		t.Errorf("requires = %q, last = %v", conv.requires, sawLast)
	}

	// A model that still won't answer fails the run.
	conv = &scripted{turns: []pluginkit.ChatTurn{call("echo", `{}`), {Text: "no"}}}
	if _, err := runAgent(context.Background(), conv, task); err == nil || !strings.Contains(err.Error(), "did not submit") {
		t.Errorf("runAgent = %v", err)
	}
}

func TestAcceptFindingsChecksTheirLocation(t *testing.T) {
	in := newReviewInput(sampleDiff, pluginkit.PRMetadata{}, singleFileGuide())
	var out findingsAnswer
	accept := in.acceptFindings(&out)

	args := `{"notes": " fine ", "findings": [
		{"filename": "./api/views.py", "line": 11, "severity": "WARNING", "guide": "style_guidelines.md", "rule": "snake_case", "content": " rename it "},
		{"filename": "api/views.py", "line": 10, "severity": "info", "guide": "g", "rule": "r", "content": "untouched line"},
		{"filename": "api/other.py", "line": 11, "severity": "info", "guide": "g", "rule": "r", "content": "wrong file"},
		{"filename": "api/views.py", "line": 99, "severity": "info", "guide": "g", "rule": "r", "content": "no such line"}
	]}`
	err := accept(json.RawMessage(args), false)
	if err == nil {
		t.Fatal("findings that can't be anchored should go back to the model")
	}
	for _, want := range []string{"finding 2: api/views.py:10 is not a line this PR adds", `finding 3: "api/other.py" is not one of the files`, "finding 4: api/views.py has no line 99"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}

	// On the last turn they are dropped instead.
	if err := accept(json.RawMessage(args), true); err != nil {
		t.Fatal(err)
	}
	want := []finding{{Filename: "api/views.py", Line: 11, Severity: "warning", Guide: "style_guidelines.md", Rule: "snake_case", Content: "rename it"}}
	if !reflect.DeepEqual(out.Findings, want) || out.Notes != "fine" {
		t.Errorf("accepted = %+v", out)
	}

	if err := accept(json.RawMessage(`not json`), false); err == nil {
		t.Error("malformed arguments should be rejected")
	}
}

func TestAcceptVerdictsNeedsOneForEveryFinding(t *testing.T) {
	var out verdictsAnswer
	accept := acceptVerdicts(3, &out)
	args := json.RawMessage(`{"assessment": "ok", "verdicts": [
		{"id": 1, "verdict": "CONFIRMED", "reason": "r", "severity": "error", "content": "c"},
		{"id": 3, "verdict": "maybe", "reason": "r", "severity": "", "content": ""},
		{"id": 7, "verdict": "confirmed", "reason": "r", "severity": "info", "content": ""}
	]}`)
	if err := accept(args, false); err == nil || !strings.Contains(err.Error(), "finding(s) 2") {
		t.Fatalf("a missing verdict should go back to the model: %v", err)
	}
	if err := accept(args, true); err != nil {
		t.Fatal(err)
	}
	want := []verdict{
		{ID: 1, Verdict: verdictConfirmed, Reason: "r", Severity: "error", Content: "c"},
		{ID: 2, Verdict: verdictRejected, Reason: "not checked by the validator"},
		{ID: 3, Verdict: verdictRejected, Reason: "r", Severity: "info"},
	}
	if !reflect.DeepEqual(out.Verdicts, want) {
		t.Errorf("verdicts = %+v", out.Verdicts)
	}
}

func TestPromptsCarryTheGuideDiffAndContext(t *testing.T) {
	in := newReviewInput(sampleDiff, pluginkit.PRMetadata{Title: "Add a panel", Body: "Because."}, singleFileGuide())
	shared := []string{
		"Use snake_case for all function names.",
		"PR Title: Add a panel",
		"PR Description: Because.",
		"api/views.py",
		// The head-side line number is what an annotation anchors to.
		"+    11 | def AdminPanel():",
	}
	for _, want := range append(shared, "Respect each rule's scope") {
		if !strings.Contains(in.findPrompt(), want) {
			t.Errorf("find prompt missing %q", want)
		}
	}

	candidates := []finding{{Filename: "api/views.py", Line: 11, Severity: "warning", Guide: "style_guidelines.md", Rule: "snake_case", Content: "rename"}}
	validate := in.validatePrompt(candidates)
	for _, want := range append(shared, "### Finding 1", "- location: api/views.py:11", "- the line: `+ def AdminPanel():`", "- rule: snake_case", "a frontend rule on backend code", "every id from 1 to 1") {
		if !strings.Contains(validate, want) {
			t.Errorf("validate prompt missing %q", want)
		}
	}
}

func TestPromptWithoutHunks(t *testing.T) {
	prompt := newReviewInput("not a diff at all", pluginkit.PRMetadata{}, singleFileGuide()).findPrompt()
	if !strings.Contains(prompt, "not a diff at all") {
		t.Error("prompt dropped the diff it could not number")
	}
	if !strings.Contains(prompt, "(none parsed") {
		t.Error("prompt did not tell the model there are no annotatable files")
	}
}

// scriptPhases makes newChat hand out one scripted conversation per phase.
func scriptPhases(t *testing.T, phases ...*scripted) {
	t.Helper()
	saved := newChat
	t.Cleanup(func() { newChat = saved })
	var systems []string
	newChat = func(_ *pluginkit.Model, system string, _ []pluginkit.ChatTool) conversation {
		systems = append(systems, system)
		if len(systems) > len(phases) {
			t.Fatalf("unexpected phase %d", len(systems))
		}
		return phases[len(systems)-1]
	}
}

const twoFindings = `{"notes": "n", "findings": [
	{"filename": "api/views.py", "line": 11, "severity": "info", "guide": "style_guidelines.md", "rule": "Use snake_case for all function names.", "content": "AdminPanel should be admin_panel."},
	{"filename": "api/views.py", "line": 12, "severity": "warning", "guide": "frontend/react.md", "rule": "No magic numbers.", "content": "42 is a magic number."}
]}`

func TestReviewAnnotatesOnlyConfirmedFindings(t *testing.T) {
	find := &scripted{turns: []pluginkit.ChatTurn{
		call("search_style_guide", `{"pattern": "snake"}`),
		call(submitFindingsTool, twoFindings),
	}}
	validate := &scripted{turns: []pluginkit.ChatTurn{
		call("read_style_guide", `{"path": "style_guidelines.md"}`),
		call(submitVerdictsTool, `{"assessment": "Mostly clean.", "verdicts": [
			{"id": 1, "verdict": "confirmed", "reason": "the rule covers all functions", "severity": "warning", "content": "Rename AdminPanel to admin_panel."},
			{"id": 2, "verdict": "rejected", "reason": "a frontend rule, and this is backend code", "severity": "info", "content": ""}
		]}`),
	}}
	scriptPhases(t, find, validate)

	guide := singleFileGuide()
	result, err := review(context.Background(), nil, newReviewInput(sampleDiff, pluginkit.PRMetadata{}, guide))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(find.results[0][0].Content, "style_guidelines.md:3:") {
		t.Errorf("the guide tools should search the guide: %q", find.results[0][0].Content)
	}

	response := result.response("Gemini", guide)
	want := []pluginkit.Annotation{{Filename: "api/views.py", Line: 11, Severity: "warning", Content: "Rename AdminPanel to admin_panel. (style_guidelines.md)"}}
	if !reflect.DeepEqual(response.Annotations, want) {
		t.Errorf("annotations = %+v, want %+v", response.Annotations, want)
	}
	body := response.Body.BodyContent
	for _, want := range []string{
		"Mostly clean.",
		"### Violations (1)",
		"`api/views.py:11` **warning**: Rename AdminPanel to admin_panel.",
		"style_guidelines.md: Use snake_case",
		"### Dismissed on validation (1)",
		"dismissed: a frontend rule, and this is backend code",
		"1 guide file in `/home/me/.config/style_guidelines.md` by Gemini: review 2 turns / 2 tool calls, validation 2 turns / 2 tool calls",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report missing %q:\n%s", want, body)
		}
	}
}

func TestReviewSkipsValidationWithoutFindings(t *testing.T) {
	scriptPhases(t, &scripted{turns: []pluginkit.ChatTurn{call(submitFindingsTool, `{"notes": "Names follow the guide.", "findings": []}`)}})
	guide := singleFileGuide()
	result, err := review(context.Background(), nil, newReviewInput(sampleDiff, pluginkit.PRMetadata{}, guide))
	if err != nil {
		t.Fatal(err)
	}
	response := result.response("Gemini", guide)
	if len(response.Annotations) != 0 || !strings.Contains(response.Body.BodyContent, "No violations found.\n\nNames follow the guide.") ||
		strings.Contains(response.Body.BodyContent, "validation") {
		t.Errorf("response = %+v", response)
	}
}

func TestReviewWithFailedValidationAnnotatesNothing(t *testing.T) {
	scriptPhases(t,
		&scripted{turns: []pluginkit.ChatTurn{call(submitFindingsTool, twoFindings)}},
		&scripted{}, // the model errors on its first turn
	)
	guide := singleFileGuide()
	result, err := review(context.Background(), nil, newReviewInput(sampleDiff, pluginkit.PRMetadata{}, guide))
	if err != nil {
		t.Fatal(err)
	}
	response := result.response("Gemini", guide)
	if len(response.Annotations) != 0 {
		t.Errorf("unvalidated findings were annotated: %+v", response.Annotations)
	}
	for _, want := range []string{"could not be validated (script exhausted)", "### Unconfirmed findings (2)", "42 is a magic number."} {
		if !strings.Contains(response.Body.BodyContent, want) {
			t.Errorf("report missing %q:\n%s", want, response.Body.BodyContent)
		}
	}
}

func TestReviewFailsWhenTheFirstPhaseDoes(t *testing.T) {
	scriptPhases(t, &scripted{})
	if _, err := review(context.Background(), nil, newReviewInput(sampleDiff, pluginkit.PRMetadata{}, singleFileGuide())); err == nil {
		t.Error("review should fail when the first phase does")
	}
}

// TestEncodedOutputMatchesContract runs what the plugin writes to stdout
// through the server's parser, which is what decides whether a plugin speaks
// the contract or gets treated as legacy output.
func TestEncodedOutputMatchesContract(t *testing.T) {
	result := reviewResult{
		candidates: []finding{{Filename: "api/views.py", Line: 11, Severity: "info", Guide: "g.md", Rule: "snake_case", Content: "x"}},
		verdicts:   []verdict{{ID: 1, Verdict: verdictConfirmed, Severity: "warning", Content: "Function names must be snake_case."}},
	}
	var out bytes.Buffer
	if err := pluginkit.EncodeResponse(&out, result.response("Gemini", singleFileGuide())); err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}

	parsed := server.ParsePluginOutput(out.String())
	if parsed.Body.BodyType != server.BodyTypeMarkdown || !strings.Contains(parsed.Body.BodyContent, "`api/views.py:11` **warning**") {
		t.Errorf("parsed body = %+v", parsed.Body)
	}
	want := []server.PluginAnnotation{
		{Filename: "api/views.py", Line: 11, Severity: "warning", Content: "Function names must be snake_case. (g.md)"},
	}
	if !reflect.DeepEqual(parsed.Annotations, want) {
		t.Errorf("parsed annotations = %+v, want %+v", parsed.Annotations, want)
	}
}
