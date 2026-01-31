package org

import (
	"errors"
	"fmt"
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

type OrgSerializer interface {
	Deserialize(item OrgTODO, indent_level int) []string
	Serialize(lines []string, start_line int) (OrgTODO, error)
}

type BaseOrgSerializer struct {
	ReleaseCheckCommand string
}

// Implement the OrgSerializer interface with our most generic structs / interfaces

func (bos BaseOrgSerializer) Deserialize(item OrgTODO, indent_level int) []string {
	var result []string
	result = append(result, item.ItemTitle(indent_level, bos.ReleaseCheckCommand))
	result = append(result, item.Details()...)
	return result
}

func (bos BaseOrgSerializer) Serialize(lines []string, start_line int) (OrgTODO, error) {
	// each one has the format ** TODO URL Title.  Check stars to allow for auxillary text between items
	if len(lines) == 0 {
		return OrgItem{}, errors.New("No Lines passed for serialization")
	}
	status := findOrgStatus(lines[0])
	tags := findOrgTags(lines[0])
	return OrgItem{header: lines[0], status: status, details: lines[1:], tags: tags, start_line: start_line, lines_count: len(lines)}, nil
}

func findOrgStatus(line string) string {
	for _, status := range GetOrgStatuses() {
		if strings.Contains(line, status) {
			return status
		}
	}
	return ""
}
func GetOrgStatuses() []string {
	return []string{"TODO", "DONE", "CANCELLED", "BLOCKED", "PROGRESS", "WAITING", "TENTATIVE", "DELEGATED"}
}

func findOrgTags(line string) []string {
	splits := strings.Split(line, ":")
	if len(splits) < 2 {
		return []string{}
	} else {
		return splits[1 : len(splits)-1]
	}

}

type OrgItem struct {
	header      string
	details     []string
	status      string
	tags        []string
	start_line  int
	lines_count int
}

func NewOrgItem(header string, details []string, status string, tags []string, start_line int, lines_count int) OrgItem {
	return OrgItem{
		header,
		details,
		status,
		tags,
		start_line,
		lines_count,
	}
}

// Implement the OrgTODO Interface for OrgItem
func (oi OrgItem) ItemTitle(indent_level int, release_command_check string) string {
	// This reads from the org file, so it'll still have the ** in it.
	stripped_header := oi.header
	if strings.HasPrefix(oi.header, "*") {
		stripped_header = strings.Join(strings.Split(oi.header, "* ")[1:], "")
	}
	return strings.Repeat("*", indent_level) + " " + stripped_header
}

func (oi OrgItem) Details() []string {
	return oi.details
}

// TODO: Implement? Better way?
func (oi OrgItem) Repo() string {
	for _, line := range oi.Details() {
		if strings.HasPrefix(line, "Repo:") {
			parts := strings.SplitN(line, ": ", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func (oi OrgItem) GetStatus() string {
	return oi.status
}

func (oi OrgItem) Summary() string {
	return oi.header
}

func (oi OrgItem) StartLine() int {
	return oi.start_line
}

func (oi OrgItem) LinesCount() int {
	return oi.lines_count
}

func (oi OrgItem) ID() string {
	if len(oi.Details()) == 0 {
		return ""
	}
	return strings.TrimSpace(oi.Details()[0])
}

func (oi OrgItem) CheckDone() bool {
	return oi.GetStatus() == "DONE" || oi.GetStatus() == "CANCELLED"
}

func (oi OrgItem) Identifier() string {
	return fmt.Sprintf("%s-%s", oi.Repo(), oi.ID())
}
