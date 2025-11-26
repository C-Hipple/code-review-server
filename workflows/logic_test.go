package workflows

import (
	"gtdbot/org"
	"strings"
	"testing"
)

func makeSection() org.Section {
	var items []org.OrgTODO
	for _, val := range []string{"1", "2", "3"} {
		items = append(items, makeItem(val))
	}

	section := org.Section{
		Name:        "Section Desc",
		StartLine:   10,
		IndentLevel: 2,
		Items:       items,
	}
	return section

}

func makeItem(i string) org.OrgTODO {
	return org.NewOrgItem("header "+i,
		[]string{"detail 1" + i, "detail 2" + i, "detail 3" + i},
		"TODO",
		[]string{"tag1", "tag2"},
		0,
		0,
	)
}

func Test_CheckTODOInSection(t *testing.T) {
	section := makeSection()

	at_line, _ := org.CheckTODOInSection(makeItem("1"), section)
	if at_line != 11 {
		t.Fatalf("Incorrect starting line found.  Expected %v, found %v", 11, at_line)
	}

	at_line, _ = org.CheckTODOInSection(makeItem("2"), section)
	if at_line != 15 {
		t.Fatalf("Incorrect starting line found.  Expected %v, found %v", 15, at_line)
	}

	at_line, _ = org.CheckTODOInSection(makeItem("4"), section)
	if at_line != -1 {
		t.Fatalf("Incorrect starting line found.  Expected %v, found %v", -1, at_line)
	}
}

func Test_CleanEmptyEndingLines(t *testing.T) {
	lines := []string{"a", "b", "c", "", "d", ""}

	clean_lines := cleanEmptyEndingLines(&lines)
	if len(clean_lines) != 5 {
		t.Fatalf("Improper Line Items Left, len was %v", len(clean_lines))
	}

	if clean_lines[0] != "a" {
		t.Fatalf("Incorrect first item, should've been a, got %s", clean_lines[0])
	}

	if clean_lines[len(clean_lines)-1] != "d" {
		t.Fatalf("Incorrect first item, should've been a, got %s", clean_lines[0])
	}
}

func Test_CleanLines(t *testing.T) {
	lines := []string{"*a", "b", "c", "", "d", ""}
	clean_lines := strings.Split(cleanLines(&lines), "\n")

	if len(clean_lines) != 5 {
		t.Fatalf("Improper Line Items Left, len was %v", len(clean_lines))
	}

	if clean_lines[0] != "-a" {
		t.Fatalf("Incorrect first item, should've been -a, got %s", clean_lines[0])
	}

	if clean_lines[4] != "d" {
		t.Fatalf("Incorrect first item, should've been a, got %s", clean_lines[0])
	}

	lines = []string{"*a", "b", "c", "", "d", "test\n\n"}
	clean_lines = strings.Split(cleanLines(&lines), "\n")

	if len(clean_lines) != 6 {
		t.Fatalf("Improper Line Items Left, len was %v", len(clean_lines))
	}

	if clean_lines[0] != "-a" {
		t.Fatalf("Incorrect first item, should've been -a, got %s", clean_lines[0])
	}

	if clean_lines[len(clean_lines)-1] != "test" {
		t.Fatalf("Incorrect first item, should've been a, got %s", clean_lines[0])
	}
}
