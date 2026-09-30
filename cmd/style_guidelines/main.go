package main

import (
	"context"
	"crs/cmd/internal/pluginkit"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

// style_guidelines emits the plugin response contract described in
// docs/plugins.md: a markdown body holding the compliance report, plus an
// annotation on each line of the diff that breaks one of the user's rules.
// The review is two agentic phases (see review.go), each a tool loop over the
// style guide, which may be one Markdown file or a directory of them.

const (
	// runTimeout keeps the whole run inside the server's five-minute plugin
	// timeout, with room to write the report.
	runTimeout = 4*time.Minute + 30*time.Second
	// findTimeout bounds the first phase, so the second one has time left.
	findTimeout = 2*time.Minute + 30*time.Second
)

func main() {
	diff := flag.String("diff", "", "PR diff content")
	owner := flag.String("owner", "", "PR owner")
	repo := flag.String("repo", "", "PR repo")
	number := flag.Int("number", 0, "PR number")
	commentsJSON := flag.String("comments", "", "PR comments JSON")
	headersJSON := flag.String("headers", "", "PR metadata JSON")
	guideDir := flag.String("style-guide-dir", "", "directory of Markdown style guide files (default $"+guideDirEnv+
		", then ~/.config/style_guidelines/, then the single file ~/.config/style_guidelines.md)")
	// The report doesn't vary by call type, but the flag still has to be
	// accepted since the server always passes it.
	pluginkit.RegisterCallTypeFlag()

	flag.Parse()

	_ = owner
	_ = repo
	_ = number
	_ = commentsJSON

	var metadata pluginkit.PRMetadata
	if *headersJSON != "" {
		if err := json.Unmarshal([]byte(*headersJSON), &metadata); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse headers: %v\n", err)
		}
	}

	model, err := pluginkit.ModelFromEnv()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	if *diff == "" {
		fmt.Println("Error: No diff provided")
		os.Exit(1)
	}

	guide, err := loadStyleGuide(*guideDir)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	result, err := review(ctx, model, newReviewInput(*diff, metadata, guide))
	if err != nil {
		fmt.Printf("Error calling %s: %v\n", model, err)
		os.Exit(1)
	}

	if err := pluginkit.EncodeResponse(os.Stdout, result.response(model.String(), guide)); err != nil {
		fmt.Printf("Error encoding plugin response: %v\n", err)
		os.Exit(1)
	}
}

// review runs both phases. The first one failing fails the run; the second
// one failing leaves the candidates unconfirmed, which the report says.
func review(ctx context.Context, model *pluginkit.Model, in reviewInput) (reviewResult, error) {
	var result reviewResult

	findCtx, cancel := context.WithTimeout(ctx, findTimeout)
	found, stats, err := findViolations(findCtx, model, in)
	cancel()
	result.find = stats
	if err != nil {
		return result, fmt.Errorf("reviewing the diff: %w", err)
	}
	result.candidates, result.notes = found.Findings, found.Notes
	if len(found.Findings) == 0 {
		return result, nil
	}

	verdicts, stats, err := validateFindings(ctx, model, in, found.Findings)
	result.validate = stats
	if err != nil {
		result.validationErr = err
		fmt.Fprintf(os.Stderr, "Warning: validating the findings: %v\n", err)
		return result, nil
	}
	result.verdicts, result.assessment = verdicts.Verdicts, verdicts.Assessment
	return result, nil
}
