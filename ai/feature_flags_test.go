package ai

import (
	"context"
	"crs/config"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A PR that adds a new checkout behind the new_checkout waffle flag, widens a
// model field for everyone, and brings a test and docs along.
const (
	viewsDiff = `diff --git a/app/views.py b/app/views.py
index 1111111..2222222 100644
--- a/app/views.py
+++ b/app/views.py
@@ -10,2 +10,4 @@ def checkout(request):
     cart = get_cart(request)
+    if flag_is_active(request, "new_checkout"):
+        return new_checkout(request, cart)
     return old_checkout(request, cart)
@@ -40,1 +42,4 @@ def old_checkout(request, cart):
     return render(request, "checkout.html")
+
+def new_checkout(request, cart):
+    return render(request, "new_checkout.html", {"cart": cart})
`
	modelsDiff = `diff --git a/app/models.py b/app/models.py
index 3333333..4444444 100644
--- a/app/models.py
+++ b/app/models.py
@@ -5,2 +5,2 @@ class Order(models.Model):
     total = models.DecimalField()
-    currency = models.CharField(max_length=3)
+    currency = models.CharField(max_length=8)
`
	testDiff = `diff --git a/app/tests/test_views.py b/app/tests/test_views.py
new file mode 100644
index 0000000..5555555
--- /dev/null
+++ b/app/tests/test_views.py
@@ -0,0 +1,3 @@
+@override_flag("new_checkout", active=True)
+def test_new_checkout(client):
+    assert client.get("/checkout").status_code == 200
`
	readmeDiff = `diff --git a/README.md b/README.md
index 6666666..7777777 100644
--- a/README.md
+++ b/README.md
@@ -1,1 +1,2 @@
 # Shop
+The new checkout is behind the new_checkout flag.
`
	lockDiff = `diff --git a/poetry.lock b/poetry.lock
index 8888888..9999999 100644
--- a/poetry.lock
+++ b/poetry.lock
@@ -1,2 +1,2 @@
 [[package]]
-name = "django-waffle" version = "4.0"
+name = "django-waffle" version = "4.1"
`
)

func flagsRequest(diff string, model Provider) Request {
	return Request{
		Owner:        "acme",
		Repo:         "shop",
		Number:       7,
		HeadSHA:      "sha-1",
		Digest:       CodeOnlyDigest,
		Diff:         diff,
		MetadataJSON: `{"title": "New checkout", "author": "alice"}`,
		Mode:         config.AIModeOneShot,
		Model:        model,
	}
}

func runFlags(t *testing.T, req Request) (Result, FlagsReport) {
	t.Helper()
	res, err := FeatureFlags{}.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	report, ok := res.Report.(FlagsReport)
	if !ok {
		t.Fatalf("report is %T", res.Report)
	}
	return res, report
}

func flagsAnswer(entries ...string) string {
	return `{"changes": [` + strings.Join(entries, ",") + `]}`
}

func change(id, status, flag, rationale string) string {
	return fmt.Sprintf(`{"id": %q, "status": %q, "flag": %q, "rationale": %q}`, id, status, flag, rationale)
}

func changeByID(t *testing.T, r FlagsReport, id string) FlagChange {
	t.Helper()
	for _, c := range r.Changes {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no change %s in %+v", id, r.Changes)
	return FlagChange{}
}

func TestFlagsAllGatedWhenEveryFlagIsInTheCode(t *testing.T) {
	model := &scriptedProvider{t: t, answers: []string{flagsAnswer(
		change("1", "gated", "new_checkout", "The new branch only runs when new_checkout is active."),
		change("Change 2", "gated", `"new_checkout"`, "new_checkout() is only called from the gated branch."),
	)}}
	res, r := runFlags(t, flagsRequest(viewsDiff+testDiff+readmeDiff, model))

	if r.Verdict != VerdictAllGated || res.Status != StatusSuccess {
		t.Fatalf("verdict %q status %q: %s", r.Verdict, res.Status, r.Summary)
	}
	if r.Counts != (FlagCounts{Total: 4, Gated: 2, NoEffect: 2, ByModel: 2}) {
		t.Errorf("counts = %+v", r.Counts)
	}
	if len(r.Flags) != 1 || r.Flags[0] != (FlagUse{Name: "new_checkout", Changes: 2}) {
		t.Errorf("flags = %+v", r.Flags)
	}
	if c := changeByID(t, r, "1"); c.Path != "app/views.py" || c.Line != 11 || c.EndLine != 12 || c.Context != "def checkout(request):" ||
		c.Added != 2 || c.Source != SourceModel || c.Flag != "new_checkout" {
		t.Errorf("change 1 = %+v", c)
	}
	if c := changeByID(t, r, "3"); c.Category != CategoryTest || c.Status != ChangeNoEffect || c.Source != SourceRule || !c.NewFile {
		t.Errorf("the test file = %+v", c)
	}
	if c := changeByID(t, r, "4"); c.Category != CategoryDocs || c.Status != ChangeNoEffect {
		t.Errorf("the README = %+v", c)
	}
	if !strings.Contains(r.Summary, "`new_checkout`") {
		t.Errorf("summary should name the flag: %s", r.Summary)
	}
	if outstanding, _ := res.Outstanding.([]FlagChange); len(outstanding) != 0 || len(res.Annotations) != 0 {
		t.Errorf("nothing needs attention: %+v %+v", res.Outstanding, res.Annotations)
	}

	prompt := model.prompts[0]
	for _, want := range []string{"### app/views.py", "[Change 1] head lines 11-12", "[Change 2] head lines 43-45",
		`"New checkout" by alice`, "## Test changes", `@override_flag("new_checkout"`, "- README.md: documentation"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "[Change 3]") || strings.Contains(prompt, "[Change 4]") {
		t.Error("files the path rules settled are not for the model to judge")
	}
}

func TestFlagsUngatedChangeNeedsAttention(t *testing.T) {
	model := &scriptedProvider{t: t, answers: []string{flagsAnswer(
		change("1", "gated", "new_checkout", "Behind the flag."),
		change("2", "gated", "new_checkout", "Only called from the gated branch."),
		change("3", "ungated", "", "Widens the currency column for every order."),
	)}}
	res, r := runFlags(t, flagsRequest(viewsDiff+modelsDiff, model))

	if r.Verdict != VerdictUngated || r.Summary != "1 of 3 change(s) run without a flag; 2 gated." {
		t.Errorf("verdict %q: %s", r.Verdict, r.Summary)
	}
	outstanding, _ := res.Outstanding.([]FlagChange)
	if len(outstanding) != 1 || outstanding[0].Path != "app/models.py" || outstanding[0].Line != 6 {
		t.Errorf("outstanding = %+v", outstanding)
	}
	if len(res.Annotations) != 1 || res.Annotations[0] != (Annotation{Filename: "app/models.py", Line: 6, Severity: "warning",
		Content: "[ungated] Widens the currency column for every order."}) {
		t.Errorf("annotations = %+v", res.Annotations)
	}
	body := res.Body.BodyContent
	for _, want := range []string{"### Runs without a flag (1)", "**app/models.py:6** +1 −1 in `class Order(models.Model):` _(model)_",
		"### Behind a flag (2)", "— behind `new_checkout`", "- `new_checkout` gates 2 change(s)"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(res.Log.Parsed, "verdict ungated: 2 gated, 1 ungated") {
		t.Errorf("log = %q", res.Log.Parsed)
	}
}

func TestFlagsGatedVerdictMustNameAFlagFromTheCode(t *testing.T) {
	model := &scriptedProvider{t: t, answers: []string{flagsAnswer(
		change("1", "gated", "", "It looks gated."),
		change("2", "gated", "beta_checkout", "Probably behind the beta flag."),
		change("3", "gated", "NEW-CHECKOUT", "Spelled differently, but the same flag."),
	)}}
	_, r := runFlags(t, flagsRequest(viewsDiff+modelsDiff, model))

	if c := changeByID(t, r, "1"); c.Status != ChangeUnclear || c.Flag != "" || !strings.Contains(c.Rationale, "named no flag") {
		t.Errorf("a gated verdict with no flag = %+v", c)
	}
	if c := changeByID(t, r, "2"); c.Status != ChangeUnclear || c.Flag != "" || !strings.Contains(c.Rationale, `"beta_checkout", which appears nowhere`) {
		t.Errorf("a flag the diff never mentions = %+v", c)
	}
	if c := changeByID(t, r, "3"); c.Status != ChangeGated || c.Flag != "NEW-CHECKOUT" {
		t.Errorf("case and separators don't matter when matching the flag: %+v", c)
	}
	if r.Verdict != VerdictUnclear {
		t.Errorf("verdict = %q", r.Verdict)
	}
}

func TestFlagSeen(t *testing.T) {
	code := normalizeFlagText(`if flag_is_active(request, "new_checkout"): pass  # FEATURE_X`)
	for flag, want := range map[string]bool{"new_checkout": true, "NewCheckout": true, "feature-x": true,
		"old_checkout": false, "x": false, "ne": false, "": false} {
		if got := flagSeen(flag, code); got != want {
			t.Errorf("flagSeen(%q) = %v, want %v", flag, got, want)
		}
	}
}

// A hunk deep inside a function only a flag check calls: the diff shows no
// flag at all.
const nestedDiff = `diff --git a/app/pricing.py b/app/pricing.py
index 1111111..2222222 100644
--- a/app/pricing.py
+++ b/app/pricing.py
@@ -30,2 +30,3 @@ def discounted_total(cart):
     total = cart.total()
+    total = apply_discounts(total)
     return total
`

func TestFlagsAgentCanFindTheFlagWithItsTools(t *testing.T) {
	gated := flagsAnswer(change("1", "gated", "discounts_v2", "discounted_total() is only called under the flag."))

	// One-shot: the flag appears nowhere the model looked.
	_, r := runFlags(t, flagsRequest(nestedDiff, &scriptedProvider{t: t, answers: []string{gated}}))
	if c := changeByID(t, r, "1"); c.Status != ChangeUnclear {
		t.Errorf("without seeing the flag the verdict can't stand: %+v", c)
	}

	// Agent: it searches for the callers and finds the flag check.
	model := &scriptedProvider{t: t, answers: []string{
		`TOOL_CALL {"name": "search_code", "arguments": {"query": "discounted_total("}}`,
		gated,
	}}
	req := flagsRequest(nestedDiff, model)
	req.Mode = config.AIModeAgent
	req.Agent = NewProviderAgent(model)
	req.ReadFile = func(context.Context, string) (string, error) { return "", errors.New("unused") }
	var queries []string
	req.SearchCode = func(_ context.Context, query string) (string, error) {
		queries = append(queries, query)
		return `app/views.py:12:    if switch_is_active("discounts_v2"): total = discounted_total(cart)` + "\n", nil
	}
	_, r = runFlags(t, req)
	if c := changeByID(t, r, "1"); c.Status != ChangeGated || c.Flag != "discounts_v2" {
		t.Errorf("a flag the agent's search turned up counts as seen: %+v", c)
	}
	if len(queries) != 1 || queries[0] != "discounted_total(" {
		t.Errorf("searches = %q", queries)
	}
	if r.Model.Turns != 2 || r.Model.ToolCalls != 1 {
		t.Errorf("model use = %+v", r.Model)
	}
	for _, tool := range []string{"- read_file:", "- search_code:"} {
		if !strings.Contains(model.prompts[0], tool) {
			t.Errorf("agent prompt should offer %s", tool)
		}
	}
}

func TestFlagsCutPromptTrustsOnlyUngatedVerdicts(t *testing.T) {
	var big strings.Builder
	big.WriteString("diff --git a/app/generated.py b/app/generated.py\nnew file mode 100644\n--- /dev/null\n+++ b/app/generated.py\n@@ -0,0 +1,4000 @@\n")
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&big, "+VALUE_%04d = %q\n", i, strings.Repeat("x", 30))
	}
	model := &scriptedProvider{t: t, answers: []string{flagsAnswer(
		change("1", "gated", "new_checkout", "Behind the flag."),
		change("2", "no-effect", "", "Nothing calls it."),
		change("3", "ungated", "", "Widens the column."),
	)}}
	res, r := runFlags(t, flagsRequest(viewsDiff+modelsDiff+big.String(), model))

	if !r.Truncated || !res.Truncated {
		t.Error("a change cut from the prompt makes the report truncated")
	}
	for _, id := range []string{"1", "2"} {
		if c := changeByID(t, r, id); c.Status != ChangeUnclear || !strings.Contains(c.Rationale, "not every change fit") {
			t.Errorf("change %s must not stand on a partial view: %+v", id, c)
		}
	}
	if c := changeByID(t, r, "3"); c.Status != ChangeUngated {
		t.Errorf("an ungated verdict stands: %+v", c)
	}
	if c := changeByID(t, r, "4"); c.Status != ChangeUnclear || c.Source != SourceRule || !strings.Contains(c.Rationale, "didn't fit its prompt") {
		t.Errorf("the change that didn't fit = %+v", c)
	}
	if strings.Contains(model.prompts[0], "[Change 4]") {
		t.Error("the change that didn't fit must not be in the prompt")
	}
	if r.Model.Asked != 3 {
		t.Errorf("asked = %d", r.Model.Asked)
	}
}

func TestFlagsTooManyChangesAreCapped(t *testing.T) {
	var diff strings.Builder
	diff.WriteString("diff --git a/app/settings.py b/app/settings.py\n--- a/app/settings.py\n+++ b/app/settings.py\n")
	for i := 0; i <= maxFlagModelChanges; i++ {
		fmt.Fprintf(&diff, "@@ -%d,1 +%d,1 @@\n-A_%d = 1\n+A_%d = 2\n", i*10+1, i*10+1, i, i)
	}
	model := &scriptedProvider{t: t, answers: []string{flagsAnswer()}}
	res, r := runFlags(t, flagsRequest(diff.String(), model))

	if r.Model.Asked != maxFlagModelChanges || !r.Truncated {
		t.Errorf("asked %d, truncated %v", r.Model.Asked, r.Truncated)
	}
	last := changeByID(t, r, fmt.Sprint(maxFlagModelChanges+1))
	if last.Status != ChangeUnclear || !strings.Contains(last.Rationale, "more than 100 changes") {
		t.Errorf("the change over the cap = %+v", last)
	}
	if !strings.Contains(changeByID(t, r, "1").Rationale, "The model gave no verdict for it.") {
		t.Error("changes the model skipped say so")
	}
	if len(res.Log.Warnings) == 0 || !strings.Contains(res.Log.Warnings[0], "no verdict for 100 of 100") {
		t.Errorf("warnings = %q", res.Log.Warnings)
	}
}

func TestFlagsPathRulesSettleWithoutTheModel(t *testing.T) {
	res, r := runFlags(t, flagsRequest(testDiff+readmeDiff, noModel{t}))
	if r.Verdict != VerdictNoRuntimeChanges || res.Status != StatusSuccess || r.Model.Consulted {
		t.Errorf("tests and docs only: verdict %q status %q model %+v", r.Verdict, res.Status, r.Model)
	}

	res, r = runFlags(t, flagsRequest(testDiff+lockDiff, noModel{t}))
	if r.Verdict != VerdictUngated || r.Counts.Ungated != 1 {
		t.Errorf("a lockfile runs for everyone: verdict %q counts %+v", r.Verdict, r.Counts)
	}
	if c := changeByID(t, r, "2"); c.Category != CategoryLockfile || c.Source != SourceRule || c.Line != 0 {
		t.Errorf("the lockfile = %+v", c)
	}
	if len(res.Annotations) != 0 {
		t.Errorf("a whole-file change anchors to no line: %+v", res.Annotations)
	}
	if !strings.Contains(res.Body.BodyContent, "**poetry.lock** — dependency lockfile _(path rule)_") {
		t.Errorf("body:\n%s", res.Body.BodyContent)
	}
}

func TestFlagsNoDiffIsInsufficientInput(t *testing.T) {
	res, r := runFlags(t, flagsRequest("", noModel{t}))
	if res.Status != StatusInsufficientInput || r.Verdict != VerdictInsufficientInput || len(r.Missing) != 1 {
		t.Errorf("status %q verdict %q missing %q", res.Status, r.Verdict, r.Missing)
	}
	if !strings.Contains(r.Summary, "no diff is available") {
		t.Errorf("summary = %q", r.Summary)
	}
}

func TestFlagsModelFailureLeavesChangesUnclear(t *testing.T) {
	res, r := runFlags(t, flagsRequest(viewsDiff+testDiff, &scriptedProvider{t: t, err: errors.New("quota exceeded")}))
	if r.Verdict != VerdictUnclear || r.Counts.Unclear != 2 || r.Counts.NoEffect != 1 || r.Model.Consulted {
		t.Errorf("verdict %q counts %+v model %+v", r.Verdict, r.Counts, r.Model)
	}
	if !strings.Contains(r.Model.Note, "could not be consulted, so 2 change(s) stay unclear: quota exceeded") {
		t.Errorf("note = %q", r.Model.Note)
	}
	if outstanding, _ := res.Outstanding.([]FlagChange); len(outstanding) != 2 {
		t.Errorf("both unclear changes need attention: %+v", outstanding)
	}
	if len(res.Annotations) != 2 || res.Annotations[0].Severity != "info" {
		t.Errorf("annotations = %+v", res.Annotations)
	}

	res, r = runFlags(t, flagsRequest(viewsDiff, &scriptedProvider{t: t, answers: []string{"They all look gated to me."}}))
	if r.Verdict != VerdictUnclear || !r.Model.Consulted || !strings.Contains(r.Model.Note, "could not be read") || res.Log.ResponseSnippet == "" {
		t.Errorf("an unreadable answer: verdict %q model %+v", r.Verdict, r.Model)
	}
}

func TestFlagsIgnoresVerdictsItDidNotAskFor(t *testing.T) {
	model := &scriptedProvider{t: t, answers: []string{flagsAnswer(
		change("2", "gated", "new_checkout", "Test file."),
		change("9", "gated", "new_checkout", "No such change."),
	)}}
	res, r := runFlags(t, flagsRequest(modelsDiff+testDiff, model))
	if c := changeByID(t, r, "2"); c.Source != SourceRule || c.Status != ChangeNoEffect {
		t.Errorf("a rule-settled change keeps its status: %+v", c)
	}
	if !strings.Contains(strings.Join(res.Log.Warnings, "; "), "answered for 2 change(s) it wasn't asked about") {
		t.Errorf("warnings = %q", res.Log.Warnings)
	}
}

func TestFlagPathRules(t *testing.T) {
	cases := map[string]string{
		"app/views.py":                   "",
		"app/tests/test_views.py":        CategoryTest,
		"test_utils.py":                  CategoryTest,
		"conftest.py":                    CategoryTest,
		"server/renderer_test.go":        CategoryTest,
		"src/components/Button.test.tsx": CategoryTest,
		"src/api.spec.ts":                CategoryTest,
		"spec/models/order_spec.rb":      CategoryTest,
		"src/test/java/ShopTest.java":    CategoryTest,
		"src/main/java/ABTest.java":      "",
		"pkg/testdata/golden.json":       CategoryTest,
		"bun_client/e2e/login.e2e.ts":    CategoryTest,
		"README.md":                      CategoryDocs,
		"docs/flags.md":                  CategoryDocs,
		"app/CHANGELOG.md":               CategoryDocs,
		"app/prompts/checkout.md":        "",
		"notes.txt":                      "",
		"package-lock.json":              CategoryLockfile,
		"web/yarn.lock":                  CategoryLockfile,
		"go.sum":                         CategoryLockfile,
		"go.mod":                         "",
		"requirements.txt":               "",
	}
	for p, want := range cases {
		if got := pathCategory(p); got != want {
			t.Errorf("pathCategory(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestParseChangedFiles(t *testing.T) {
	diff := viewsDiff + `diff --git a/app/legacy.py b/app/legacy.py
deleted file mode 100644
index 1234567..0000000
--- a/app/legacy.py
+++ /dev/null
@@ -1,2 +0,0 @@
-def legacy():
-    pass
diff --git a/app/config.py b/app/config.py
index 1234567..7654321 100644
--- a/app/config.py
+++ b/app/config.py
@@ -1,3 +1,2 @@
 A = 1
 B = 2
-C = 3
diff --git a/static/logo.png b/static/logo.png
index 1234567..7654321 100644
Binary files a/static/logo.png and b/static/logo.png differ
`
	files := parseChangedFiles(diff)
	if len(files) != 4 {
		t.Fatalf("files = %+v", files)
	}
	views := files[0]
	if views.path != "app/views.py" || len(views.hunks) != 2 || views.newFile || views.deleted {
		t.Fatalf("views = %+v", views)
	}
	if h := views.hunks[1]; h.line != 43 || h.endLine != 45 || h.added != 3 || h.removed != 0 ||
		h.context != "def old_checkout(request, cart):" || !strings.HasPrefix(h.text, "@@ -40,1 +42,4 @@") {
		t.Errorf("second hunk = %+v", h)
	}
	if legacy := files[1]; !legacy.deleted || legacy.hunks[0].line != 0 || legacy.hunks[0].removed != 2 {
		t.Errorf("a deleted file has no head lines: %+v", legacy)
	}
	if cfg := files[2]; cfg.hunks[0].line != 2 || cfg.hunks[0].endLine != 2 {
		t.Errorf("a removal at the end of a file anchors to its last line: %+v", cfg.hunks[0])
	}
	if logo := files[3]; logo.path != "static/logo.png" || len(logo.hunks) != 0 {
		t.Errorf("a binary file has no hunks: %+v", logo)
	}

	// The server's rendering of the same hunk: a space after each marker, and
	// a blank line before each hunk.
	rendered := "diff --git a/app/views.py b/app/views.py\nindex 1111111..2222222 100644\n--- a/app/views.py\n+++ b/app/views.py\n" +
		"\n@@ -10,2 +10,4 @@ def checkout(request):\n      cart = get_cart(request)\n+     if flag_is_active(request, \"new_checkout\"):\n" +
		"+         return new_checkout(request, cart)\n      return old_checkout(request, cart)\n" +
		"\n@@ -40,1 +42,4 @@ def old_checkout(request, cart):\n      return render(request, \"checkout.html\")\n+ \n+ def new_checkout(request, cart):\n+     return x\n"
	got := parseChangedFiles(rendered)
	if len(got) != 1 || len(got[0].hunks) != 2 {
		t.Fatalf("rendered = %+v", got)
	}
	for i, want := range [][2]int{{11, 12}, {43, 45}} {
		if h := got[0].hunks[i]; h.line != want[0] || h.endLine != want[1] {
			t.Errorf("rendered hunk %d spans %d-%d, want %d-%d", i, h.line, h.endLine, want[0], want[1])
		}
	}
}

func TestParseFlagVerdicts(t *testing.T) {
	answer := "Here you go:\n```json\n" + `{"changes": [
		{"id": 1, "status": "Gated", "flag": "` + "`new_checkout`" + `", "rationale": "Behind it."},
		{"id": "Change 2", "status": "maybe", "rationale": "Hard to say."},
		{"id": "#3", "status": "no-effect"},
		{"status": "ungated"}
	]}` + "\n```"
	got, err := parseFlagVerdicts(answer)
	if err != nil {
		t.Fatalf("parseFlagVerdicts: %v", err)
	}
	want := []flagVerdict{
		{ID: "1", Status: ChangeGated, Flag: "new_checkout", Rationale: "Behind it."},
		{ID: "2", Status: ChangeUnclear, Rationale: "Hard to say."},
		{ID: "3", Status: ChangeNoEffect},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if _, err := parseFlagVerdicts("They all look gated to me."); err == nil {
		t.Error("an answer without JSON is unreadable")
	}
	if got, err := parseFlagVerdicts(`[{"id": "4", "status": "ungated"}]`); err != nil || len(got) != 1 {
		t.Errorf("a bare array: %+v, %v", got, err)
	}
}

func TestFeatureFlagsIsCodeOnly(t *testing.T) {
	if got := KeyDigest(FeatureFlags{}, "digest-1"); got != CodeOnlyDigest {
		t.Errorf("KeyDigest(feature-flags) = %q", got)
	}
	if got := KeyDigest(CommentsAddressed{}, "digest-1"); got != "digest-1" {
		t.Errorf("comments-addressed reads the discussion: KeyDigest = %q", got)
	}
}

func TestFlagsReportJSONShape(t *testing.T) {
	model := &scriptedProvider{t: t, answers: []string{flagsAnswer(change("1", "ungated", "", "Runs for all."))}}
	res, _ := runFlags(t, flagsRequest(modelsDiff, model))
	encoded, err := res.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var doc struct {
		Report struct {
			Verdict string            `json:"verdict"`
			Counts  map[string]int    `json:"counts"`
			Flags   []json.RawMessage `json:"flags"`
			Changes []map[string]any  `json:"changes"`
			Missing []string          `json:"missing"`
		} `json:"report"`
		Outstanding []map[string]any `json:"outstanding"`
	}
	if err := json.Unmarshal([]byte(encoded), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Report.Verdict != "ungated" || doc.Report.Flags == nil || doc.Report.Missing == nil {
		t.Errorf("report = %+v", doc.Report)
	}
	for _, key := range []string{"total", "gated", "ungated", "no_effect", "unclear", "by_model"} {
		if _, ok := doc.Report.Counts[key]; !ok {
			t.Errorf("counts missing %q", key)
		}
	}
	c := doc.Report.Changes[0]
	for key, want := range map[string]any{"id": "1", "path": "app/models.py", "line": float64(6), "status": "ungated",
		"source": "model", "added": float64(1), "removed": float64(1), "context": "class Order(models.Model):"} {
		if c[key] != want {
			t.Errorf("change[%q] = %v, want %v", key, c[key], want)
		}
	}
	if len(doc.Outstanding) != 1 || doc.Outstanding[0]["id"] != "1" {
		t.Errorf("outstanding = %+v", doc.Outstanding)
	}
}
