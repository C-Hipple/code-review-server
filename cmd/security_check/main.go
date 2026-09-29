package main

import (
	"crs/cmd/internal/pluginkit"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func buildPrompt(diff string, metadata pluginkit.PRMetadata) string {
	return fmt.Sprintf(`Analyze the following PR diff for potential security issues, specifically focusing on endpoints.


If the code is not relevant to the tasks below, simply respond with "no security changes found."  Do not try to infer risks beyond the scope of the current changes.

Tasks:
1. Identify any new or modified API endpoints.
2. Check if these endpoints expose sensitive or critical information.
3. Verify if they are protected by security decorators (e.g., @authenticated, auth middleware, etc.) or other mitigation strategies.
4. Flags any endpoints that appear to be unprotected or insufficiently protected.
5. Identify any hardcoded secrets or credentials if present.

Format:
- List each potential issue briefly.
- Suggest a mitigation for each issue.
- If no issues are found, simply say "Security check passed: No unprotected sensitive endpoints identified."

Be terse and professional. No fluff.

%sDiff:
%s
`, metadata.Context(), diff)
}

func main() {
	diff := flag.String("diff", "", "PR diff content")
	owner := flag.String("owner", "", "PR owner")
	repo := flag.String("repo", "", "PR repo")
	number := flag.Int("number", 0, "PR number")
	commentsJSON := flag.String("comments", "", "PR comments JSON")
	headersJSON := flag.String("headers", "", "PR metadata JSON")
	// The analysis doesn't vary by call type, but the flag still has to be
	// accepted since the server always passes it.
	pluginkit.RegisterCallTypeFlag()

	flag.Parse()

	// Suppress unused warnings
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

	result, err := model.Generate(buildPrompt(*diff, metadata), nil)
	if err != nil {
		fmt.Printf("Error calling %s: %v\n", model, err)
		os.Exit(1)
	}

	fmt.Println(result)
}
