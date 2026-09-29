package ai

import (
	"context"
	"crs/config"
	"crs/llm"
	"errors"
	"strings"
	"testing"
)

const diagramAnswer = `flowchart TD
    main["main.go: main"]:::changed --> greet["helper.go: greet"]:::added`

// changeClassDefs is what the server appends to every flowchart.
const changeClassDefs = `
    classDef added fill:#dcfce7,stroke:#16a34a,color:#14532d
    classDef changed fill:#fef3c7,stroke:#d97706,color:#78350f
    classDef removed fill:#fee2e2,stroke:#dc2626,color:#7f1d1d,stroke-dasharray: 5 5`

func TestChangeDiagramStoresTheRawMermaid(t *testing.T) {
	model := &scriptedProvider{t: t, answers: []string{diagramAnswer + "\n"}}
	req := appliedRequest(orderingDiff, model)
	req.MetadataJSON = `{"title": "Greet people by name"}`
	res, err := ChangeDiagram{}.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := diagramAnswer + changeClassDefs
	report, ok := res.Report.(ChangeDiagramReport)
	if !ok || report.Mermaid != want || report.DiagramType != "flowchart" {
		t.Fatalf("report = %#v", res.Report)
	}
	if res.Body.BodyType != BodyMarkdown || res.Body.BodyContent != "```mermaid\n"+want+"\n```\n" {
		t.Errorf("body = %+v", res.Body)
	}
	if res.Status != "" || res.Truncated || len(res.Annotations) != 0 {
		t.Errorf("result = %+v", res)
	}
	if res.Log.Parsed != "a 5-line flowchart" || !strings.HasPrefix(res.Log.Input, "3 file(s), ") {
		t.Errorf("log = %+v", res.Log)
	}

	prompt := model.prompts[0]
	for _, want := range []string{
		"Draw a Mermaid flowchart of the changes",
		"appending :::added, :::changed or :::removed",
		"Pull request title: Greet people by name\n",
		"Files in this diff:\n- main_test.go\n- main.go\n- helper.go\n",
		"=== FILE: helper.go ===",
		"Respond with ONLY the Mermaid source",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestChangeDiagramStoresTheResult(t *testing.T) {
	// Encoded the way the runner stores it, the report carries the source
	// verbatim for clients to render.
	model := &scriptedProvider{t: t, answers: []string{diagramAnswer}}
	res, err := ChangeDiagram{}.Run(context.Background(), appliedRequest(orderingDiff, model))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	encoded, err := res.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	doc := DecodeDocument(encoded)
	if !strings.Contains(string(doc.Report), `"mermaid":"flowchart TD\n    main[\"main.go: main\"]:::changed`) ||
		!strings.Contains(string(doc.Report), `"diagram_type":"flowchart"`) {
		t.Errorf("stored report = %s", doc.Report)
	}
}

func TestChangeDiagramFailures(t *testing.T) {
	t.Run("no diff", func(t *testing.T) {
		_, err := ChangeDiagram{}.Run(context.Background(), appliedRequest("", noModel{t}))
		wantStage(t, err, StageInput)
	})
	t.Run("model unavailable", func(t *testing.T) {
		model := &scriptedProvider{t: t, err: &llm.CallError{Stage: llm.StageHTTPStatus, Err: errors.New("quota exceeded")}}
		_, err := ChangeDiagram{}.Run(context.Background(), appliedRequest(orderingDiff, model))
		wantStage(t, err, llm.StageHTTPStatus)
	})
	t.Run("no diagram", func(t *testing.T) {
		model := &scriptedProvider{t: t, answers: []string{"The PR adds a greet helper and calls it from main."}}
		res, err := ChangeDiagram{}.Run(context.Background(), appliedRequest(orderingDiff, model))
		wantStage(t, err, llm.StageParse)
		if !strings.Contains(res.Log.ResponseSnippet, "adds a greet helper") {
			t.Errorf("an unreadable answer is logged for debugging: %+v", res.Log)
		}
	})
	t.Run("too large to render", func(t *testing.T) {
		huge := "flowchart TD\n" + strings.Repeat("    a --> b\n", maxMermaidBytes/10)
		model := &scriptedProvider{t: t, answers: []string{huge}}
		_, err := ChangeDiagram{}.Run(context.Background(), appliedRequest(orderingDiff, model))
		wantStage(t, err, llm.StageParse)
	})
}

func TestExtractMermaid(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		want     string
		wantKind string
	}{
		{"bare", "flowchart LR\n  a --> b\n", "flowchart LR\n  a --> b", "flowchart"},
		{"mermaid fence among chatter",
			"Here is the diagram:\n\n```mermaid\ngraph TD;\n  a --> b\n```\n\nIt shows a calling b.",
			"graph TD;\n  a --> b", "graph"},
		{"a mermaid fence wins over an earlier one",
			"```text\nflowchart TD\n  x --> y\n```\n```mermaid\nflowchart TD\n  a --> b\n```",
			"flowchart TD\n  a --> b", "flowchart"},
		{"an unlabelled fence", "```\nsequenceDiagram\n  A->>B: hi\n```", "sequenceDiagram\n  A->>B: hi", "sequenceDiagram"},
		{"an unclosed fence", "```mermaid\nflowchart TD\n  a --> b", "flowchart TD\n  a --> b", "flowchart"},
		{"CRLF line endings", "flowchart TD\r\n  a --> b\r\n", "flowchart TD\n  a --> b", "flowchart"},
		{"front matter and comments before the header go",
			"---\ntitle: PR\n---\n%% the change\nflowchart TD\n  a --> b", "flowchart TD\n  a --> b", "flowchart"},
		{"interactions and directives go",
			"flowchart TD\n  %%{init: {\"theme\": \"forest\"}}%%\n  a --> b\n  click a \"https://evil.example\" \"tip\"\n  click b call alert()\n",
			"flowchart TD\n  a --> b", "flowchart"},
		{"class diagram links go",
			"classDiagram\n  class Link\n  link Link \"https://evil.example\"\n  callback Link \"run\"\n",
			"classDiagram\n  class Link", "classDiagram"},
		{"a flowchart node may be called link", "flowchart TD\n  link --> b", "flowchart TD\n  link --> b", "flowchart"},
		{"prose that opens with a keyword is not a header", "graph of the changes:\nnone", "", ""},
		{"a chart is not a change diagram", "pie title Files\n  \"go\" : 3", "", ""},
		{"nothing", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, kind := extractMermaid(tt.text)
			if got != tt.want || kind != tt.wantKind {
				t.Errorf("extractMermaid(%q) = %q, %q; want %q, %q", tt.text, got, kind, tt.want, tt.wantKind)
			}
		})
	}
}

func TestDefineChangeClasses(t *testing.T) {
	// The model's own definitions of the change classes are replaced; a
	// definition that also names another class is kept.
	got := defineChangeClasses("flowchart TD\n  a:::added --> b:::keep\n  classDef added fill:#f00\n  classDef changed,removed fill:#0f0\n  classDef keep,added fill:#00f")
	want := "flowchart TD\n  a:::added --> b:::keep\n  classDef keep,added fill:#00f" + changeClassDefs
	if got != want {
		t.Errorf("defineChangeClasses:\n%s\nwant:\n%s", got, want)
	}

	// Only a flowchart gets them: classDef means nothing to a sequence diagram.
	model := &scriptedProvider{t: t, answers: []string{"sequenceDiagram\n  A->>B: hi"}}
	res, err := ChangeDiagram{}.Run(context.Background(), appliedRequest(orderingDiff, model))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report := res.Report.(ChangeDiagramReport); report.Mermaid != "sequenceDiagram\n  A->>B: hi" {
		t.Errorf("report = %#v", report)
	}
}

func TestChangeDiagramIsACodeOnlyReport(t *testing.T) {
	f, ok := DefaultRegistry.Get(ChangeDiagramID)
	if !ok {
		t.Fatal("change-diagram is not registered")
	}
	if IsApplied(f) {
		t.Error("change-diagram is a report a client opens, not an applied feature")
	}
	if KeyDigest(f, "discussion") != CodeOnlyDigest {
		t.Error("change-diagram reads only the code, so comments must not make it stale")
	}
	if strings.Join(modesOf(f), ",") != "oneshot" {
		t.Errorf("modes = %v", modesOf(f))
	}
	if problems := ValidateFeatures([]config.AIFeature{{ID: ChangeDiagramID, Enabled: true, Automatic: true, Provider: "gemini"}}); len(problems) != 0 {
		t.Errorf("a valid entry was rejected: %v", problems)
	}
	if problems := ValidateFeatures([]config.AIFeature{{ID: ChangeDiagramID, Enabled: true, Mode: "agent"}}); len(problems) != 1 {
		t.Errorf("change-diagram runs one-shot only, got %v", problems)
	}
}
