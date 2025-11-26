package org

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type OrgTODO interface {
	ItemTitle(indent_level int, release_check_command string) string
	Summary() string
	Details() []string
	GetStatus() string
	CheckDone() bool
	ID() string
	StartLine() int
	LinesCount() int
	Repo() string
	Identifier() string
}

func CleanHeader(line string) string {
	line = strings.ReplaceAll(line, "*", "")
	line = strings.ReplaceAll(line, "TODO", "")
	line = strings.ReplaceAll(line, "DONE", "")
	line = strings.TrimSpace(line)
	if strings.Contains(line, "[") {
		line = strings.Split(line, "[")[0]
	}
	return line
}

func CheckForHeader(section_name string, line string, stars string) bool {
	// Need a better way of handling this,
	prefix := strings.HasPrefix(line, stars+" TODO ") || strings.HasPrefix(line, stars+" DONE ")
	return prefix && strings.Contains(line, section_name) && !strings.Contains(line, "CI Status")
}

func GetOrgFile(filename string, orgFileDir string) *os.File {
	orgFilePath := orgFileDir
	if strings.HasPrefix(orgFilePath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			slog.Error("Error getting home directory", "error", err)
			os.Exit(1)
		}
		orgFilePath = filepath.Join(home, orgFilePath[2:])
	}

	orgFilePath = filepath.Join(orgFilePath, filename)

	file, err := os.OpenFile(orgFilePath, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		slog.Error("Error opening or creating org file", "file", orgFilePath, "error", err)
		os.Exit(1)
	}
	return file
}

func CheckTODOInSection(todo OrgTODO, section Section) (int, OrgTODO) {
	// returns the line number if it's found, otherwise returns -1
	serializer := BaseOrgSerializer{}
	at_line := section.StartLine + 1 // account for the section title
	for _, line_item := range section.Items {
		if strings.Contains(line_item.Summary(), todo.Summary()) {
			return at_line, line_item
		}
		if line_item.Summary() == todo.Summary() {
			return at_line, line_item
		}
		at_line += len(serializer.Deserialize(line_item, section.IndentLevel))
	}
	return -1, nil
}
