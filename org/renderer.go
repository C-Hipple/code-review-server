package org

import (
	"codereviewserver/database"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type OrgRenderer struct {
	db         *database.DB
	serializer OrgSerializer
}

func NewOrgRenderer(db *database.DB, serializer OrgSerializer) *OrgRenderer {
	return &OrgRenderer{
		db:         db,
		serializer: serializer,
	}
}

func (r *OrgRenderer) RenderAllSectionsToString() (string, error) {
	sections, err := r.db.GetAllSections()
	if err != nil {
		return "", err
	}

	// Sort sections by ID to maintain order
	sort.Slice(sections, func(i, j int) bool {
		return sections[i].ID < sections[j].ID
	})

	// Build the org file content
	var content strings.Builder

	for _, section := range sections {
		// Get items for this section
		items, err := r.db.GetItemsBySection(section.ID)
		if err != nil {
			return "", err
		}

		// Build section header
		sectionHeader := r.buildSectionHeader(section, items)
		content.WriteString(sectionHeader)
		content.WriteString("\n")

		// Build items
		for _, item := range items {
			itemLines := r.buildItemLines(item, section.IndentLevel)
			for _, line := range itemLines {
				content.WriteString(line)
				if !strings.HasSuffix(line, "\n") {
					content.WriteString("\n")
				}
			}
		}
		// Add blank line between sections
		content.WriteString("\n")
	}

	return content.String(), nil
}

func (r *OrgRenderer) RenderFile(filename, orgFileDir string) error {
	content, err := r.RenderAllSectionsToString()
	if err != nil {
		return err
	}

	// Write to file
	orgFilePath := orgFileDir
	if strings.HasPrefix(orgFilePath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		orgFilePath = filepath.Join(home, orgFilePath[2:])
	}
	orgFilePath = filepath.Join(orgFilePath, filename)

	return os.WriteFile(orgFilePath, []byte(content), 0644)
}

func (r *OrgRenderer) buildSectionHeader(section *database.Section, items []*database.Item) string {
	doneCount := 0
	for _, item := range items {
		if item.Status == "DONE" || item.Status == "CANCELLED" {
			doneCount++
		}
	}

	status := "TODO"
	if doneCount == len(items) && len(items) > 0 {
		status = "DONE"
	}

	indentStars := strings.Repeat("*", section.IndentLevel-1)
	ratio := fmt.Sprintf("[%d/%d]", len(items), doneCount)

	return fmt.Sprintf("%s %s %s %s", indentStars, status, section.SectionName, ratio)
}

func (r *OrgRenderer) buildItemLines(item *database.Item, indentLevel int) []string {
	details, err := item.GetDetails()
	if err != nil {
		slog.Error("Error getting item details", "error", err, "item_id", item.ID)
		details = []string{}
	}

	tags, err := item.GetTags()
	if err != nil {
		slog.Error("Error getting item tags", "error", err, "item_id", item.ID)
		tags = []string{}
	}

	// Build the title line
	indentStars := strings.Repeat("*", indentLevel)
	titleLine := fmt.Sprintf("%s %s %s", indentStars, item.Status, item.Title)

	// Add tags
	if len(tags) > 0 {
		tagStr := ":" + strings.Join(tags, ":") + ":"
		titleLine += "\t\t" + tagStr
	}

	// Add archived tag if needed
	if item.Archived {
		if !strings.Contains(titleLine, ":") {
			titleLine += "\t\t:ARCHIVE:"
		} else {
			titleLine += ":ARCHIVE:"
		}
	}

	lines := []string{titleLine + "\n"}
	lines = append(lines, details...)

	return lines
}
