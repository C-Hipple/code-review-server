package org

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func rawLines() []string {
	raw := `* TODO Team Reviews [0/2]
** TODO dev: PR 1 :repo-name:
15479
https://github.com/org-name/repo-name/pull/15479
Title: dev: PR-1
Author: C-Hipple
*** BODY
abc

def

ge
** TODO feature: PR 2 :repo-name:
15480
https://github.com/org-name/repo-name/pull/15480
Title: feature: PR-2
Author: C-Hipple
*** BODY
abc2

def2

ge2
open line end

* TODO My Review Requests [0/2]
** TODO dev: PR 3 :repo-name:
15479
https://github.com/org-name/repo-name/pull/15479
Title: dev: PR-3
Author: C-Hipple
*** BODY
abc

def
** TODO feature: PR 4 :repo-name:
15480
https://github.com/org-name/repo-name/pull/15480
Title: feature: PR-4
Author: C-Hipple
*** BODY
short body
`
	return strings.Split(raw, "\n")
}

func RealRawLines() []string {
	// This is a set of lines pulled from an actual sync run
	raw := `* TODO Team Reviews [0/3]
** TODO dev: Handle             :lsp-example:
1
https://github.com/C-Hipple/lsp-example/pull/1
Title: dev: Handle
Author: C-Hipple
Branch: C-Hipple:dev/example-pr
Requested Reviewers:
Requested Teams:
*** BODY
 ### Section 1

This PR is for testing code-review PR body parsing.

### Section 2

Adds some silly changes in mlutple go files
** TODO tseting elgot vs lsp-mode		:diff-lsp.el:
1
https://github.com/C-Hipple/diff-lsp.el/pull/1
Title: tseting elgot vs lsp-mode
Author: C-Hipple
Branch: C-Hipple:testing-clients
Requested Reviewers:
Requested Teams:
*** BODY

** TODO This really bothered me		:diff-lsp:
1
https://github.com/C-Hipple/diff-lsp/pull/1
Title: This really bothered me
Author: test-user-afk
Branch: test-user-afk:patch-1
Requested Reviewers:
Requested Teams:
*** BODY
 - Please don't include this print statement
* TODO My Review Requests [0/0]
* TODO Other Repos [0/0]
* TODO My Pull Requests [0/2]
** TODO dev: Handle             :lsp-example:
1
https://github.com/C-Hipple/lsp-example/pull/1
Title: dev: Handle
Author: C-Hipple
Branch: C-Hipple:dev/example-pr
Requested Reviewers:
Requested Teams:
*** BODY
 ### Section 1

This PR is for testing code-review PR body parsing.

### Section 2

Adds some silly changes in mlutple go files
** TODO tseting elgot vs lsp-mode		:diff-lsp.el:
1
https://github.com/C-Hipple/diff-lsp.el/pull/1
Title: tseting elgot vs lsp-mode
Author: C-Hipple
Branch: C-Hipple:testing-clients
Requested Reviewers:
Requested Teams:
*** BODY
* TODO My Closed Pull Requests [0/0]
* TODO Other [0/0]`
	return strings.Split(raw, "\n")
}

func makeTestOrgDoc(all_lines []string) OrgDocument {
	// Helper which skips reading the file and let's inject the lines
	serializer := BaseOrgSerializer{}
	sections, err := ParseSectionsFromLines(all_lines, serializer)
	if err != nil {
		fmt.Println("Error parsing sections from file: ", err)
	}
	fmt.Println(sections)
	fmt.Println(len(sections))
	return OrgDocument{Filename: "test_file_name.org", Sections: sections, Serializer: serializer}

}

func Test_findOrgTags(t *testing.T) {
	line := "* TODO Example name  :tag1:tag2:"
	tags := findOrgTags(line)
	if !reflect.DeepEqual([]string{"tag1", "tag2"}, tags) {
		t.Fatalf(":tag1:tag2: parsed to %v", tags)
	}

	line2 := "* TODO Example name  :tag1:"
	tags2 := findOrgTags(line2)
	if !reflect.DeepEqual([]string{"tag1"}, tags2) {
		t.Fatalf(":tag1:tag2: parsed to %v", tags2)
	}
}

func Test_findOrgStatus(t *testing.T) {
	line_1 := "* TODO Example name  :tag1:tag2:"
	status_1 := findOrgStatus(line_1)
	if !(status_1 == "TODO") {
		t.Fatalf("Failed.  TODO Should've been found. Found status_1: %v", status_1)
	}

	line_2 := "* DONE Example name  :tag1:tag2:"
	status_2 := findOrgStatus(line_2)
	if !(status_2 == "DONE") {
		t.Fatalf("Failed.  DONE Should've been found. Found status_2: %v", status_2)
	}

}

func Test_Serialize(t *testing.T) {
	raw := `** TODO dev: Serialize PrivateShow Objects into Dataclass		:repo-name:
15479
https://github.com/org-name/repo-name/pull/15479
Title: dev: Serialize PrivateShow Objects into Dataclass
Author: C-Hipple
*** BODY
abc

def

ge
`
	parser := BaseOrgSerializer{}
	item, err := parser.Serialize(strings.Split(raw, "\n"), 0)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}
	expected := OrgItem{
		header: "** TODO dev: Serialize PrivateShow Objects into Dataclass		:repo-name:",
		status: "TODO",
		details: []string{
			"15479",
			"https://github.com/org-name/repo-name/pull/15479",
			"Title: dev: Serialize PrivateShow Objects into Dataclass",
			"Author: C-Hipple",
		},
		tags: []string{"repo-name"},
	}
	if item.GetStatus() != expected.GetStatus() {
		t.Fatalf("Mismatched status %v-%v", item.GetStatus(), expected.GetStatus())
	}

	if item.Summary() != expected.Summary() {
		t.Fatalf("Mismatched summary %v-%v", item.Summary(), expected.Summary())
	}

	if item.ID() != "15479" {
		t.Fatalf("Mismatched ID %v-%v", item.ID(), "15479")
	}

	if item.CheckDone() {
		t.Fatalf("This isn't done!")
	}
	if len(item.Details()) != 11 {
		t.Fatalf("Wrong length of details! %v", len(item.Details()))
	}

	if item.LinesCount() != 12 {
		t.Fatalf("Wrong size of LinesCount! %v", item.LinesCount())
	}
}

func Test_LineNumbers(t *testing.T) {
	raw := `** TODO dev: Serialize PrivateShow Objects into Dataclass		:repo-name:
15479
https://github.com/org-name/repo-name/pull/15479
Title: dev: Serialize PrivateShow Objects into Dataclass
Author: C-Hipple
*** BODY
abc

def

ge
`
	parser := BaseOrgSerializer{}
	item, err := parser.Serialize(strings.Split(raw, "\n"), 0)
	if err != nil {
		t.Fatalf("Error on Serialized: %s", err)
	}
	fmt.Println("ID: ", item.ID())
	fmt.Println("Details: ")
	fmt.Println(item.Details())
	fmt.Println("Done ")
}

func Test_ParseExampleSections(t *testing.T) {
	raw_lines := rawLines()
	sections, err := ParseSectionsFromLines(raw_lines, BaseOrgSerializer{})
	if err != nil {
		t.Fatalf("Error parsing sections %v", err)
	}

	if len(sections) != 2 {
		t.Fatalf("Wrong length of sections ! %v", len(sections))
	}

	section_team_review := sections[0]
	section_my_review := sections[1]

	if section_my_review.Name != "My Review Requests" {
		t.Fatalf("Wrong Parsed Name of my Review Section '%s'", section_my_review.Name)
	}

	if section_team_review.Name != "Team Reviews" {
		t.Fatalf("Wrong Parsed Name of team Review Section '%s'", section_team_review.Name)
	}

	if len(section_my_review.Items) != 2 {
		t.Fatalf("Wrong length of my review items! %v", len(section_my_review.Items))
	}

	if len(section_team_review.Items) != 2 {
		t.Fatalf("Wrong length of team review items! %v", len(section_team_review.Items))
	}

	if section_team_review.StartLine != 1 {
		t.Fatalf("Wrong start line of first section %v", section_team_review.StartLine)
	}

	if section_my_review.StartLine != 26 {
		t.Fatalf("Wrong start line of second section %v", section_my_review.StartLine)
	}

	// Test item start lines
	if section_team_review.Items[0].StartLine() != 2 {
		t.Fatalf("Wrong start line of first item in first section %v", section_team_review.Items[0].StartLine())
	}

	if section_team_review.Items[1].StartLine() != 13 {
		t.Fatalf("Wrong start line of second item in first section %v", section_team_review.Items[1].StartLine())
	}

	if section_my_review.Items[0].StartLine() != 27 {
		t.Fatalf("Wrong start line of first item in second section: %v, expected %v", section_my_review.Items[0].StartLine(), 27)
	}

	if section_my_review.Items[1].StartLine() != 36 {
		t.Fatalf("Wrong start line of second item in second section %v", section_my_review.Items[1].StartLine())
	}
}

func Test_UpdateItemInSection(t *testing.T) {
	serializer := BaseOrgSerializer{}
	new_item_lines := `** TODO feature: PR 2 :repo-name:
15480
https://github.com/org-name/repo-name/pull/15480
Title: feature: PR-2
Author: C-Hipple
*** BODY
abc2
`
	t.Skip("Skipping test since I haven't separated UpdateItemInSection from writing to file immediately")
	new_item, err := serializer.Serialize(strings.Split(new_item_lines, "\n"), 0)
	fmt.Println("ID", new_item.ID())
	if err != nil {
		t.Fatal("Failed to serialze new item in UpdateItemSection")
	}
	doc := makeTestOrgDoc(rawLines())
	err = doc.UpdateItemInSection("Team Reviews", &new_item, false)
	if err != nil {
		t.Fatalf("Error on updating item in section: %v", err)
	}

}

func Test_ParseSectionsRealText(t *testing.T) {
	raw_lines := RealRawLines()
	sections, err := ParseSectionsFromLines(raw_lines, BaseOrgSerializer{})
	if err != nil {
		t.Fatalf("Error parsing sections %v", err)
	}

	if len(sections) != 6 {
		t.Fatalf("Wrong length of sections ! %v", len(sections))
	}

	section_team_review := sections[0]
	section_my_prs := sections[3]

	if section_my_prs.Name != "My Pull Requests" {
		t.Fatalf("Wrong Parsed Name of my Review Section '%s'", section_my_prs.Name)
	}

	if section_team_review.Name != "Team Reviews" {
		t.Fatalf("Wrong Parsed Name of team Review Section '%s'", section_team_review.Name)
	}

	if len(section_my_prs.Items) != 2 {
		t.Fatalf("Wrong length of my prs items! %v", len(section_my_prs.Items))
	}

	if len(section_team_review.Items) != 3 {
		t.Fatalf("Wrong length of team review items! %v", len(section_team_review.Items))
	}

	if section_team_review.StartLine != 1 {
		t.Fatalf("Wrong start line of team reviews section %v", section_team_review.StartLine)
	}

	if section_my_prs.StartLine != 40 {
		t.Fatalf("Wrong start line of my prs section %v", section_my_prs.StartLine)
	}

	// Test item start lines
	if section_team_review.Items[0].StartLine() != 2 {
		t.Fatalf("Wrong start line of first item in first section %v", section_team_review.Items[0].StartLine())
	}

	if section_team_review.Items[1].StartLine() != 18 {
		t.Fatalf("Wrong start line of second item in first section %v", section_team_review.Items[1].StartLine())
	}

	if section_team_review.Items[2].StartLine() != 28 {
		t.Fatalf("Wrong start line of second item in first section %v", section_team_review.Items[1].StartLine())
	}

	if section_my_prs.Items[0].StartLine() != 41 {
		t.Fatalf("Wrong start line of first item in second section %v", section_my_prs.Items[0].StartLine())
	}

	if section_my_prs.Items[1].StartLine() != 57 {
		t.Fatalf("Wrong start line of second item in second section %v", section_my_prs.Items[1].StartLine())
	}
}
