package main

import (
	"context"
	"crs/config"
	"crs/git_tools"
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/go-github/v48/github"
)

func main() {
	if err := config.Initialize(); err != nil {
		fmt.Printf("Failed to initialize configuration: %v\n", err)
		os.Exit(1)
	}
	defer config.C.DB.Close()

	owner := "C-Hipple"
	repo := "gtdbot"
	number := 11

	client := git_tools.GetGithubClient()
	ctx := context.Background()
	opts := &github.PullRequestListCommentsOptions{}

	comments, _, err := client.PullRequests.ListComments(ctx, owner, repo, number, opts)
	if err != nil {
		fmt.Printf("Error processing PR: %v\n", err)
		os.Exit(1)
	}

	bytes, err := json.MarshalIndent(comments, "", "  ")
	if err != nil {
		fmt.Printf("Error marshaling comments: %v\n", err)
		os.Exit(1)
	}

	filename := "github_api_comments_pr_11.json"
	if err := os.WriteFile(filename, bytes, 0644); err != nil {
		fmt.Printf("Error writing file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully wrote raw GitHub comments to %s\n", filename)
}
