package main

import (
	"crs/cmd/internal/pluginkit"
	"flag"
	"fmt"
)

func main() {
	owner := flag.String("owner", "", "PR owner")
	repo := flag.String("repo", "", "PR repo")
	number := flag.Int("number", 0, "PR number")
	diff := flag.String("diff", "", "PR diff content")
	comments := flag.String("comments", "", "PR comments JSON")
	headers := flag.String("headers", "", "PR metadata JSON")
	branch := flag.String("branch", "", "PR head branch name")
	callType := pluginkit.RegisterCallTypeFlag()

	flag.Parse()

	if *owner != "" {
		fmt.Printf("Owner: %s\n", *owner)
	}
	if *repo != "" {
		fmt.Printf("Repo: %s\n", *repo)
	}
	if *number != 0 {
		fmt.Printf("Number: %d\n", *number)
	}
	// The server always passes --call-type, so a plugin can tell an automatic
	// run apart from one a reviewer asked for.
	fmt.Printf("Call Type: %s\n", callType())
	if *branch != "" {
		fmt.Printf("Branch: %s\n", *branch)
	}
	if *diff != "" {
		fmt.Printf("Diff Content Length: %d\n", len(*diff))
	}
	if *comments != "" {
		fmt.Printf("Comments Content Length: %d\n", len(*comments))
	}
	if *headers != "" {
		fmt.Printf("Headers Content Length: %d\n", len(*headers))
	}
}
