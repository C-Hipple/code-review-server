package main

import (
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

	flag.Parse()

	if *owner != "" {
		fmt.Printf("Owner Length: %d\n", len(*owner))
	}
	if *repo != "" {
		fmt.Printf("Repo Length: %d\n", len(*repo))
	}
	if *number != 0 {
		fmt.Printf("Number: %d\n", *number)
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
