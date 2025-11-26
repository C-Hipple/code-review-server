package utils

import (
	"fmt"
	"strings"
	"testing"
)

func Test_ReplaceLines(t *testing.T) {
	existing_lines := []string{"* TODO Code Review",
		"** TODO PR #1",
		"the_url",
		"author",
		"misc_info",
		"\n"}
	new_lines := []string{"** TODO PR #1", "updated_url"}
	updated := replaceLines(existing_lines, new_lines, 1, len(new_lines))
	target := []string{"* TODO Code Review",
		"** TODO PR #1",
		"updated_url",
		"author",
		"misc_info",
		"\n"}
	for _, ele := range Zip(updated, target) {
		fmt.Println(ele.First, ele.Second)
		if ele.First != ele.Second {
			fmt.Printf("len target: %d, len actual: %d", len(target), len(updated))
			t.Fatalf("Updated lines do not match.  Target: \n%v Actual \n%v", target, updated)
		}
	}
}

func Test_ReplaceLinesInMiddle(t *testing.T) {
	existing_lines := []string{"* TODO Code Review",
		"** TODO PR #1",
		"the_url",
		"author",
		"misc_info",
		"\n"}
	new_lines := []string{"updated_url", "updated_author"}
	updated := replaceLines(existing_lines, new_lines, 2, len(new_lines))

	target := []string{"* TODO Code Review",
		"** TODO PR #1",
		"updated_url",
		"updated_author",
		"misc_info",
		"\n"}
	for _, ele := range Zip(updated, target) {
		fmt.Println(ele.First, ele.Second)
		if ele.First != ele.Second {
			fmt.Printf("len target: %d, len actual: %d", len(target), len(updated))
			t.Fatalf("Updated lines do not match.  Target: \n%v Actual \n%v", target, updated)
		}

	}
}

func Test_ReplaceLinesInOldLinesLonger(t *testing.T) {
	// We're replacing the url, author, author2, author3 lines with just updated_url, updated_author
	existing_lines := []string{"* TODO Code Review",
		"** TODO PR #1",
		"the_url",
		"author",
		"author2",
		"author3",
		"misc_info",
		"END"}

	new_lines := []string{"updated_url", "updated_author"}
	updated := replaceLines(existing_lines, new_lines, 2, 4)

	target := []string{"* TODO Code Review",
		"** TODO PR #1",
		"updated_url",
		"updated_author",
		"misc_info",
		"END"}

	for _, ele := range Zip(updated, target) {
		fmt.Println(ele.First, ele.Second)
		if ele.First != ele.Second {
			fmt.Printf("len target: %d, len actual: %d", len(target), len(updated))
			t.Fatalf("Updated lines do not match.  Target: \n%v Actual \n%v", target, updated)
		}

	}
}

func Test_InsertLines(t *testing.T) {
	existing_lines := strings.Split(`* TODO Code Review
** TODO PR #1 :draft:
the_url
author`, "\n")
	new_lines := []string{"sub-header", "add. sub-header"}
	updated := insertLines(existing_lines, new_lines, 2)
	target := `* TODO Code Review
** TODO PR #1 :draft:
sub-header
add. sub-header
the_url
author
` // newline at the end
	if updated != target {
		t.Fatalf("Updated lines do not match.  Target: %v \nActual %v", target, updated)
	}
}

func Test_DeleteLines(t *testing.T) {
	existing_lines := []string{
		"* TODO PR #1 <title> :draft:",
		"sub-header",
		"add. sub-header",
		"url",
		"the_author",
		"*** Body",
		"",
		"In this PR We do things",
	}

	removed := removeLines(existing_lines, 0, 1)
	if removed[0] != "sub-header" {
		t.Fatal("Failed to remove the first line")
	}
	if len(removed) != len(existing_lines)-1 {

		t.Fatal("Removed too many lines")

	}

	removed = removeLines(existing_lines, 0, 0)
	if removed[0] != existing_lines[0] {
		t.Fatal("Remove 0 lines failed")
	}

	if len(removed) != len(existing_lines) {
		t.Fatal("Removed lines when shouldn't ahve")
	}

	removed = removeLines(existing_lines, 1, 2)
	target := []string{
		"* TODO PR #1 <title> :draft:",
		"url",
		"the_author",
		"*** Body",
		"",
		"In this PR We do things",
	}

	for _, ele := range Zip(removed, target) {
		if ele.First != ele.Second {
			t.Fatalf("Updated lines do not match.  Target: \n%v Actual \n%v", target, removed)
		}
	}
}

func Test_InsertLinesAtEnd(t *testing.T) {
	existing_lines := []string{
		"* TODO PR #1 <title> :draft:",
		"*** Body",
		"END",
	}
	raw_output := insertLines(existing_lines, []string{"added line"}, -1)
	output := strings.Split(raw_output, "\n")
	last_true_element := output[len(output)-2] // We end the file with a newline so -1 is empty.
	if last_true_element != "added line" {
		t.Fatalf("Adding Line at the end failed.  Expected to end with added line: %s", last_true_element)
	}
	empty_last_element := output[len(output)-1] // We end the file with a newline so -1 is empty.
	if empty_last_element != "" {
		t.Fatalf("Adding Line at the end failed.  Expected to end with END: %s", empty_last_element)
	}

	previous_last_element := output[len(output)-3] // We end the file with a newline so -1 is empty.
	if previous_last_element != "END" {
		t.Fatalf("Adding Line at the end failed.  Expected END before the newly added line")
	}
}
