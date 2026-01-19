package main

import (
	"crs/config"
	"crs/git_tools"
	"fmt"
	"os"
)

func main() {
	if err := config.Initialize(); err != nil {
		fmt.Printf("Failed to initialize configuration: %v\n", err)
		os.Exit(1)
	}
	defer config.C.DB.Close()

	owner := "C-Hipple"
	repo := "diff-lsp"
	number := 15

	client := git_tools.GetGithubClient()
	diff := git_tools.GetPRDiff(client, owner, repo, number)

	filename := "pr_15_diff.diff"
	if err := os.WriteFile(filename, []byte(diff), 0644); err != nil {
		fmt.Printf("Error writing file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully wrote raw GitHub diff to %s\n", filename)
}
