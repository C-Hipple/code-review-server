package org

import (
	"database/sql"
	"fmt"
    "codereviewserver/database"
	"log/slog"
	"strings"
)

// DBOrgDocument adapts the database to work with the existing OrgDocument interface
type DBOrgDocument struct {
	Filename   string
	DB         *database.DB
	Serializer OrgSerializer
	OrgFileDir string
}

func NewDBOrgDocument(filename string, db *database.DB, serializer OrgSerializer, orgFileDir string) *DBOrgDocument {
	return &DBOrgDocument{
		Filename:   filename,
		DB:         db,
		Serializer: serializer,
		OrgFileDir: orgFileDir,
	}
}

func (d *DBOrgDocument) GetSection(sectionName string) (*DBSection, error) {
	section, err := d.DB.GetOrCreateSection(d.Filename, sectionName, 2)
	if err != nil {
		return nil, err
	}
	releaseCmd := ""
	if bos, ok := d.Serializer.(BaseOrgSerializer); ok {
		releaseCmd = bos.ReleaseCheckCommand
	}
	return &DBSection{
		Section:             section,
		DB:                  d.DB,
		Serializer:          d.Serializer,
		ReleaseCheckCommand: releaseCmd,
	}, nil
}

func (d *DBOrgDocument) AddItemInSection(sectionName string, newItem OrgTODO) error {
	section, err := d.GetSection(sectionName)
	if err != nil {
		return err
	}
	return section.AddItem(newItem)
}

func (d *DBOrgDocument) UpdateItemInSection(sectionName string, newItem OrgTODO, archive bool) error {
	section, err := d.GetSection(sectionName)
	if err != nil {
		return err
	}
	return section.UpdateItem(newItem, archive)
}

func (d *DBOrgDocument) DeleteItemInSection(sectionName string, itemToDelete OrgTODO) error {
	section, err := d.GetSection(sectionName)
	if err != nil {
		return err
	}
	return section.DeleteItem(itemToDelete)
}

func (d *DBOrgDocument) AddDeserializedItemInSection(sectionName string, newLines []string) error {
	section, err := d.GetSection(sectionName)
	if err != nil {
		return err
	}
	// Parse the lines back into an OrgTODO
	item, err := d.Serializer.Serialize(newLines, 0)
	if err != nil {
		return err
	}
	return section.AddItem(item)
}

func (d *DBOrgDocument) UpdateDeserializedItemInSection(sectionName string, newItem OrgTODO, archive bool, newLines []string) error {
	section, err := d.GetSection(sectionName)
	if err != nil {
		return err
	}
	// Parse the lines back into an OrgTODO
	item, err := d.Serializer.Serialize(newLines, 0)
	if err != nil {
		return err
	}
	return section.UpdateItem(item, archive)
}

type DBSection struct {
	*database.Section
	DB                  *database.DB
	Serializer          OrgSerializer
	ReleaseCheckCommand string
}

func (s *DBSection) Name() string {
	return s.Section.SectionName
}

func (s *DBSection) GetItems() ([]OrgTODO, error) {
	dbItems, err := s.DB.GetItemsBySection(s.ID)
	if err != nil {
		return nil, err
	}

	items := make([]OrgTODO, len(dbItems))
	for i, dbItem := range dbItems {
		items[i] = &DBOrgItem{
			Item:        dbItem,
			Serializer:  s.Serializer,
			IndentLevel: s.IndentLevel,
		}
	}
	return items, nil
}

func (s *DBSection) AddItem(item OrgTODO) error {
	identifier := item.Identifier()
	slog.Debug("Adding item with identifier: " + identifier)
	status := item.GetStatus()

	// Get the full formatted title line (includes tags)
	titleLine := item.ItemTitle(s.IndentLevel, s.ReleaseCheckCommand)

	// Extract tags and clean title
	tags := extractTagsFromTitle(titleLine)
	title := cleanTitle(titleLine)

	details := item.Details()

	_, err := s.DB.UpsertItem(s.ID, identifier, status, title, details, tags, false)
	if err != nil {
		slog.Error("Failed Upsert: ", err)
	}
	slog.Debug("completed adding item: " + identifier)
	return err
}

func (s *DBSection) UpdateItem(item OrgTODO, archive bool) error {
	identifier := item.Identifier()
	slog.Debug("updating item with identifier: " + identifier)
	status := item.GetStatus()

	// Get the full formatted title line (includes tags)
	titleLine := item.ItemTitle(s.IndentLevel, s.ReleaseCheckCommand)

	// Extract tags and clean title
	tags := extractTagsFromTitle(titleLine)
	title := cleanTitle(titleLine)

	details := item.Details()

	_, err := s.DB.UpsertItem(s.ID, identifier, status, title, details, tags, archive)
	return err
}

func (s *DBSection) DeleteItem(item OrgTODO) error {
	identifier := item.Identifier()
	return s.DB.DeleteItem(s.ID, identifier)
}

func (s *DBSection) FindItem(item OrgTODO) (OrgTODO, error) {
	identifier := item.Identifier()
	dbItem, err := s.DB.GetItem(s.ID, identifier)
	if err != nil {
		return nil, err
	}
	return &DBOrgItem{
		Item:        dbItem,
		Serializer:  s.Serializer,
		IndentLevel: s.IndentLevel,
	}, nil
}

// DBOrgItem adapts database.Item to OrgTODO interface
type DBOrgItem struct {
	*database.Item
	Serializer  OrgSerializer
	IndentLevel int
}

func (d *DBOrgItem) ID() string {
	details, err := d.GetDetails()
	if err != nil || len(details) == 0 {
		return ""
	}
	return strings.TrimSpace(details[0])
}

func (d *DBOrgItem) Repo() string {
	details, err := d.GetDetails()
	if err != nil {
		return ""
	}
	for _, line := range details {
		if strings.HasPrefix(line, "Repo:") {
			parts := strings.SplitN(line, ": ", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func (d *DBOrgItem) Identifier() string {
	return d.Item.Identifier
}

func (d *DBOrgItem) Summary() string {
	return d.Item.Title
}

func (d *DBOrgItem) GetStatus() string {
	return d.Item.Status
}

func (d *DBOrgItem) CheckDone() bool {
	return d.Item.Status == "DONE" || d.Item.Status == "CANCELLED"
}

func (d *DBOrgItem) Details() []string {
	details, err := d.GetDetails()
	if err != nil {
		slog.Error("Error getting details", "error", err)
		return []string{}
	}
	return details
}

func (d *DBOrgItem) ItemTitle(indentLevel int, releaseCheckCommand string) string {
	indentStars := strings.Repeat("*", indentLevel)
	tags, _ := d.GetTags()
	tagStr := ""
	if len(tags) > 0 {
		tagStr = "\t\t:" + strings.Join(tags, ":") + ":"
	}
	if d.Archived {
		if tagStr == "" {
			tagStr = "\t\t:ARCHIVE:"
		} else {
			tagStr += ":ARCHIVE:"
		}
	}
	return fmt.Sprintf("%s %s %s%s", indentStars, d.Item.Status, d.Item.Title, tagStr)
}

func (d *DBOrgItem) StartLine() int {
	// Not applicable for database items
	return 0
}

func (d *DBOrgItem) LinesCount() int {
	details, err := d.GetDetails()
	if err != nil {
		return 1
	}
	return 1 + len(details)
}

// Helper functions
func extractTagsFromTitle(titleLine string) []string {
	tags := []string{}
	// Look for tags in format :tag1:tag2: at the end
	// Format is: ** STATUS Title\t\t:tag1:tag2: or ** STATUS Title :tag1:tag2:

	// Try to find tags after \t\t: or just :
	tagStart := strings.Index(titleLine, "\t\t:")
	if tagStart == -1 {
		tagStart = strings.LastIndex(titleLine, " :")
		if tagStart != -1 {
			tagStart += 2 // Skip " :"
		}
	} else {
		tagStart += 3 // Skip "\t\t:"
	}

	if tagStart > 0 && tagStart < len(titleLine) {
		tagPart := titleLine[tagStart:]
		// Remove trailing newline if present
		tagPart = strings.TrimSuffix(tagPart, "\n")
		// Remove leading and trailing colons
		tagPart = strings.Trim(tagPart, ":")
		if tagPart != "" {
			tags = strings.Split(tagPart, ":")
		}
	}
	return tags
}

func cleanTitle(titleLine string) string {
	// Remove tags from title line
	// Format is: ** STATUS Title\t\t:tag1:tag2: or ** STATUS Title :tag1:tag2:

	// Find where tags start
	tagStart := strings.Index(titleLine, "\t\t:")
	if tagStart == -1 {
		tagStart = strings.LastIndex(titleLine, " :")
		if tagStart != -1 {
			tagStart += 1 // Keep the space before :
		}
	}

	if tagStart > 0 {
		titleLine = titleLine[:tagStart]
	}

	// Remove the ** STATUS prefix to get just the title
	parts := strings.Fields(titleLine)
	if len(parts) >= 3 {
		// Skip ** and STATUS, join the rest
		return strings.Join(parts[2:], " ")
	}
	return strings.TrimSpace(titleLine)
}

// CheckTODOInSectionDB checks if a TODO exists in a DBSection
func CheckTODOInSectionDB(todo OrgTODO, section *DBSection) (bool, OrgTODO) {
	identifier := todo.Identifier()
	dbItem, err := section.DB.GetItem(section.ID, identifier)
	if err != nil {
		if err != sql.ErrNoRows {
			slog.Error("Error checking TODO in DB", "identifier", identifier, "sectionID", section.ID, "error", err)
		} else {
			// slog.Debug("Item not found in DB", "identifier", identifier, "sectionID", section.ID)
		}
		return false, nil
	}
	return true, &DBOrgItem{
		Item:        dbItem,
		Serializer:  section.Serializer,
		IndentLevel: section.IndentLevel,
	}
}
