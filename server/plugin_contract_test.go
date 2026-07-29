package server

import (
	"crs/config"
	"testing"
)

func TestParsePluginOutput_ConformingMarkdownResponse(t *testing.T) {
	raw := `{
		"body": {"body_type": "markdown", "body_content": "This is the response."},
		"annotations": [
			{"filename": "test.py", "line": 75, "severity": "warning", "content": "this line looks wrong"},
			{"filename": "main.go", "line": 3, "severity": "error", "content": "nil dereference"}
		]
	}`
	resp := ParsePluginOutput(raw)

	if resp.Body.BodyType != BodyTypeMarkdown {
		t.Errorf("expected body_type %q, got %q", BodyTypeMarkdown, resp.Body.BodyType)
	}
	if resp.Body.BodyContent != "This is the response." {
		t.Errorf("unexpected body_content: %q", resp.Body.BodyContent)
	}
	if len(resp.Annotations) != 2 {
		t.Fatalf("expected 2 annotations, got %d", len(resp.Annotations))
	}
	first := resp.Annotations[0]
	if first.Filename != "test.py" || first.Line != 75 || first.Severity != "warning" || first.Content != "this line looks wrong" {
		t.Errorf("unexpected first annotation: %+v", first)
	}
}

func TestParsePluginOutput_ConformingHTMLResponse(t *testing.T) {
	raw := `{"body": {"body_type": "HTML", "body_content": "<p>hi</p>"}}`
	resp := ParsePluginOutput(raw)

	if resp.Body.BodyType != BodyTypeHTML {
		t.Errorf("expected body_type %q (normalized), got %q", BodyTypeHTML, resp.Body.BodyType)
	}
	if resp.Body.BodyContent != "<p>hi</p>" {
		t.Errorf("unexpected body_content: %q", resp.Body.BodyContent)
	}
	if len(resp.Annotations) != 0 {
		t.Errorf("expected no annotations, got %d", len(resp.Annotations))
	}
}

func TestParsePluginOutput_LegacyPlainTextFallsBackToMarkdown(t *testing.T) {
	raw := "Just some plain text summary.\nWith multiple lines."
	resp := ParsePluginOutput(raw)

	if resp.Body.BodyType != BodyTypeMarkdown {
		t.Errorf("expected fallback body_type markdown, got %q", resp.Body.BodyType)
	}
	if resp.Body.BodyContent != raw {
		t.Errorf("expected raw output preserved verbatim, got %q", resp.Body.BodyContent)
	}
	if len(resp.Annotations) != 0 {
		t.Errorf("expected no annotations on fallback, got %d", len(resp.Annotations))
	}
}

func TestParsePluginOutput_JSONWithoutBodyFallsBack(t *testing.T) {
	raw := `{"summary": "valid JSON but not the contract"}`
	resp := ParsePluginOutput(raw)

	if resp.Body.BodyType != BodyTypeMarkdown || resp.Body.BodyContent != raw {
		t.Errorf("expected whole output wrapped as markdown, got %+v", resp.Body)
	}
}

func TestParsePluginOutput_UnknownBodyTypeFallsBack(t *testing.T) {
	raw := `{"body": {"body_type": "plaintext", "body_content": "hello"}}`
	resp := ParsePluginOutput(raw)

	if resp.Body.BodyType != BodyTypeMarkdown || resp.Body.BodyContent != raw {
		t.Errorf("expected whole output wrapped as markdown, got %+v", resp.Body)
	}
}

func TestParsePluginOutput_DropsUnanchoredAnnotations(t *testing.T) {
	raw := `{
		"body": {"body_type": "markdown", "body_content": "ok"},
		"annotations": [
			{"filename": "", "line": 10, "content": "no filename"},
			{"filename": "a.go", "line": 0, "content": "no line"},
			{"filename": "a.go", "line": -3, "content": "negative line"},
			{"filename": "keep.go", "line": 1, "content": "kept"}
		]
	}`
	resp := ParsePluginOutput(raw)

	if len(resp.Annotations) != 1 {
		t.Fatalf("expected 1 annotation to survive, got %d: %+v", len(resp.Annotations), resp.Annotations)
	}
	if resp.Annotations[0].Filename != "keep.go" {
		t.Errorf("unexpected surviving annotation: %+v", resp.Annotations[0])
	}
}

func TestCollectPluginAnnotations_OnlySuccessfulPluginsTagged(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})

	conforming := `{
		"body": {"body_type": "markdown", "body_content": "found issues"},
		"annotations": [
			{"filename": "test.py", "line": 75, "severity": "warning", "content": "this line looks wrong"}
		]
	}`
	if err := db.UpsertPluginResult("owner", "repo", 1, "b-linter", conforming, "success", "sha1"); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	// Legacy plain-text plugin: contributes no annotations.
	if err := db.UpsertPluginResult("owner", "repo", 1, "a-summarizer", "plain summary", "success", "sha1"); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	// Failed plugin with annotation-shaped output must be excluded.
	if err := db.UpsertPluginResult("owner", "repo", 1, "c-broken", conforming, "error", "sha1"); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	annotations, err := collectPluginAnnotations("owner", "repo", 1)
	if err != nil {
		t.Fatalf("collectPluginAnnotations failed: %v", err)
	}
	if len(annotations) != 1 {
		t.Fatalf("expected 1 annotation, got %d: %+v", len(annotations), annotations)
	}
	a := annotations[0]
	if a.Plugin != "b-linter" {
		t.Errorf("expected annotation tagged with plugin name, got %q", a.Plugin)
	}
	if a.Filename != "test.py" || a.Line != 75 || a.Severity != "warning" {
		t.Errorf("unexpected annotation: %+v", a)
	}
}

func TestCollectPluginAnnotations_NoResults(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})

	annotations, err := collectPluginAnnotations("owner", "repo", 99)
	if err != nil {
		t.Fatalf("collectPluginAnnotations failed: %v", err)
	}
	if len(annotations) != 0 {
		t.Errorf("expected no annotations, got %+v", annotations)
	}
}

func TestParsePluginOutputs_KeepsRawResultAlongsideParsed(t *testing.T) {
	db := setupTestDB(t)
	raw := `{"body": {"body_type": "html", "body_content": "<b>done</b>"}}`
	if err := db.UpsertPluginResult("owner", "repo", 2, "renderer", raw, "success", "sha2"); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	results, err := db.GetPluginResults("owner", "repo", 2)
	if err != nil {
		t.Fatalf("get results failed: %v", err)
	}

	outputs := parsePluginOutputs(results)
	out, ok := outputs["renderer"]
	if !ok {
		t.Fatal("expected output for plugin 'renderer'")
	}
	if out.Result != raw {
		t.Errorf("expected raw result preserved for legacy clients, got %q", out.Result)
	}
	if out.Status != "success" {
		t.Errorf("expected status success, got %q", out.Status)
	}
	if out.Body.BodyType != BodyTypeHTML || out.Body.BodyContent != "<b>done</b>" {
		t.Errorf("unexpected parsed body: %+v", out.Body)
	}
}
