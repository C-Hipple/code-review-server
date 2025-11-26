// Tools for parsing an org file into a struct
package org

import (
	"errors"
	"fmt"
	"gtdbot/utils"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

func GetOrgStatuses() []string {
	return []string{"TODO", "DONE", "CANCELLED", "BLOCKED", "PROGRESS", "WAITING", "TENTATIVE", "DELEGATED"}
}

type OrgDocument struct {
	Filename   string
	Sections   []Section
	Serializer OrgSerializer
	OrgFileDir string
}

func GetOrgDocument(file_name string, serializer OrgSerializer, orgFileDir string) OrgDocument {
	file := GetOrgFile(file_name, orgFileDir)
	all_lines, _ := utils.LinesFromReader(file)
	file.Close()
	sections, err := ParseSectionsFromLines(all_lines, serializer)
	if err != nil {
		slog.Error("Error parsing sections from file", "error", err)
		os.Exit(1)
	}
	doc := OrgDocument{Filename: file_name, Sections: sections, Serializer: serializer, OrgFileDir: orgFileDir}
	return doc
}

func (o OrgDocument) Refresh() {
	serializer := BaseOrgSerializer{}

	file := GetOrgFile(o.Filename, o.OrgFileDir)
	all_lines, _ := utils.LinesFromReader(file)
	file.Close()

	sections, err := ParseSectionsFromLines(all_lines, serializer)
	if err != nil {
		slog.Error("Error parsing sections from file", "error", err)
		os.Exit(1)
	}
	o.Sections = sections
}

func (o OrgDocument) AddSection(section_name string) (Section, error) {
	// Adds a new section always at the end
	slog.Info("Adding section to file", "section_name", section_name)
	formatted := fmt.Sprintf("* TODO %s [0/0]", section_name)
	at_line, err := utils.InsertLinesInFile(o.GetFile(), []string{formatted}, -1)
	if err != nil {
		return Section{}, err
	}
	section := Section{
		Name:        section_name,
		StartLine:   at_line,
		IndentLevel: 2,
		Items:       []OrgTODO{},
	}
	o.Sections = append(o.Sections, section)
	return section, nil
}

func (o OrgDocument) GetSection(section_name string) (Section, error) {
	for _, section := range o.Sections {
		if section.Name == section_name {
			return section, nil
		}
	}
	section, err := o.AddSection(section_name)
	if err != nil {
		return Section{}, errors.New("Section not found")
	}
	return section, nil
}

func (o OrgDocument) AddItemInSection(section_name string, new_item *OrgTODO) error {
	section, err := o.GetSection(section_name)
	if err != nil {
		panic(err)
		// return err
	}
	section.Items = append(section.Items, *new_item)
	new_lines := o.Serializer.Deserialize(*new_item, section.IndentLevel)
	utils.InsertLinesInFile(o.GetFile(), new_lines, section.StartLine)
	return nil
}

func (o OrgDocument) AddDeserializedItemInSection(section_name string, new_lines []string) error {
	section, err := o.GetSection(section_name)
	if err != nil {
		panic(err)
	}
	utils.InsertLinesInFile(o.GetFile(), new_lines, section.StartLine)
	return nil
}

func (o OrgDocument) UpdateItemInSection(section_name string, new_item *OrgTODO, archive bool) error {
	section, err := o.GetSection(section_name)
	if err != nil {
		return err
	}
	start_line, existing_item := CheckTODOInSection(*new_item, section)
	if start_line == -1 {
		return errors.New("Item not in section; Cannot update!")
	}

	new_lines := o.Serializer.Deserialize(*new_item, section.IndentLevel)
	if archive && !strings.Contains(new_lines[0], "ARCHIVE") {
		if !strings.HasSuffix(new_lines[0], ":") {
			// org tags are of the format :tag1:tag2:, if this is going to be the first tag, we need the first :
			new_lines[0] = new_lines[0] + ":"
		}
		new_lines[0] = new_lines[0] + "ARCHIVE:"
	}

	utils.ReplaceLinesInFile(o.GetFile(), new_lines, start_line-1, existing_item.LinesCount()) // we do -1 since the util is 0 index
	return nil
}

func (o OrgDocument) UpdateDeserializedItemInSection(section_name string, new_item *OrgTODO, archive bool, new_lines []string) error {
	section, err := o.GetSection(section_name)
	if err != nil {
		return err
	}
	start_line, existing_item := CheckTODOInSection(*new_item, section)
	if start_line == -1 {
		return errors.New("Item not in section; Cannot update!")
	}

	if archive && !strings.Contains(new_lines[0], "ARCHIVE") {
		if !strings.HasSuffix(new_lines[0], ":") {
			// org tags are of the format :tag1:tag2:, if this is going to be the first tag, we need the first :
			new_lines[0] = new_lines[0] + ":"
		}
		new_lines[0] = new_lines[0] + "ARCHIVE:"
	}

	utils.ReplaceLinesInFile(o.GetFile(), new_lines, start_line-1, existing_item.LinesCount()) // we do -1 since the util is 0 index
	return nil
}

func (o OrgDocument) DeleteItemInSection(section_name string, item_to_delete *OrgTODO) error {
	section, err := o.GetSection(section_name)
	if err != nil {
		return err
	}
	start_line, existing_item := CheckTODOInSection(*item_to_delete, section)
	if start_line == -1 {
		return errors.New("Item not in section; Cannot Delete!")
	}
	// We subtract 1 since the utils methods are 0 index and the file methods are 1 index
	utils.DeleteLinesInFile(o.GetFile(), start_line-1, existing_item.LinesCount())
	return nil
}

func (o OrgDocument) PrintAll() {
	slog.Info("Printing all sections from Document", "filename", o.Filename)
	for _, section := range o.Sections {
		slog.Info("Section", "header", section.Header(), "start_line", section.StartLine)
		for _, item := range section.Items {
			slog.Info("Item", "title", item.ItemTitle(section.IndentLevel+1, ""), "start_line", item.StartLine(), "line_count", item.LinesCount(), "end_line", item.StartLine()+item.LinesCount())
		}
	}
}

type Section struct {
	Name        string
	StartLine   int
	IndentLevel int // How many ** are we rocking per item (each item, not the header!)
	Items       []OrgTODO
}

func (s Section) GetStatus() string {
	if s.CheckAllComplete() {
		return "DONE"
	}
	return "TODO"
}

func (s Section) CalculateDoneRatio() string {
	// This is the [0/0] at the end
	return "[" + strconv.Itoa(len(s.Items)) + "/" + strconv.Itoa(s.DoneCount()) + "]"
}

func (s Section) Header() string {
	header_items := []string{
		strings.Repeat("*", s.IndentLevel-1), // header is 1 less indent than items
		s.GetStatus(),
		s.Name,
		s.CalculateDoneRatio(),
	}

	return strings.Join(header_items, " ")
}

func (o OrgDocument) GetFile() *os.File {
	return GetOrgFile(o.Filename, o.OrgFileDir)
}

func (s Section) CheckAllComplete() bool {
	return s.DoneCount() == len(s.Items)
}

func (s Section) DoneCount() int {
	var done_count int
	for _, item := range s.Items {
		if item.CheckDone() {
			done_count = done_count + 1
		}
	}
	return done_count
}

func AddTODO(doc OrgDocument, section Section, new_item OrgTODO) {
	section.Items = append(section.Items, new_item)
	new_lines := doc.Serializer.Deserialize(new_item, section.IndentLevel)
	utils.InsertLinesInFile(doc.GetFile(), new_lines, section.StartLine+1)
}

func ParseSectionsFromLines(all_lines []string, serializer OrgSerializer) ([]Section, error) {
	var sections []Section
	var header string
	section_start_line := 0
	item_start_line := 0
	in_section := false

	var items []OrgTODO
	var item_lines []string
	building_item := false

	for i, line := range all_lines {
		i = i + 1 // remove 0 index of file
		slog.Debug("Parsing line", "line_number", i, "line", line)
		// TODO need a better way of handling this
		if !strings.HasPrefix(line, "*") || strings.Contains(line, "*** BODY") || strings.Contains(line, "*** C") || strings.Contains(line, "****") || strings.Contains(line, "CI Status") || strings.Contains(line, "*** Diff") {
			if building_item {
				item_lines = append(item_lines, line)
				slog.Debug("Building item", "line", line)
				continue
			}
			if i == len(all_lines)-1 && line == "" {
				// Allow empty line at the end of file?
				slog.Debug("Found empty last line of file.")
				continue
			}
			slog.Warn("Rogue line", "line", line, "building_item", building_item, "in_section", in_section, "item_start_line", item_start_line, "header", header)
			panic("Rogue line: " + line)
		}

		if in_section && strings.HasPrefix(line, "*") && !strings.HasPrefix(line, "**") {
			// Check if we're into the next section at the same indent level as the header
			if building_item {
				building_item = false
				item, serialize_err := serializer.Serialize(item_lines, item_start_line)
				if serialize_err != nil {
					panic("Error serializing item: " + serialize_err.Error())
				}
				items = append(items, item)
				slog.Debug("Adding item inside", "summary", item.Summary(), "details", item.Details(), "line_number", i, "start_line", item_start_line, "line_count", item.LinesCount())
			}
			sections = append(sections, Section{
				Name:      CleanHeader(header),
				StartLine: section_start_line,
				//IndentLevel: strings.Count(header, "*") + 1,
				IndentLevel: 2,
				Items:       items,
			})
			slog.Debug("Adding section", "section_name", sections[len(sections)-1].Name)

			// cleanup, get ready for next section
			items = []OrgTODO{}
			header = ""
			in_section = false
			building_item = false
			// item_start_line = i
		}

		if CheckForHeader("TODO", line, "*") || CheckForHeader("DONE", line, "*") {
			in_section = true
			section_start_line = i
			header = CleanHeader(line)
			slog.Debug("Found Section Header", "header", header)
			items = []OrgTODO{}
			building_item = false
			item_lines = []string{}
			continue
		}

		if in_section && strings.HasPrefix(line, "**") && !strings.Contains(line, "BODY") {
			if building_item {
				building_item = false
				// gross on the section_start_line + 1, this is for the first item being added
				// item, serialize_err := serializer.Serialize(item_lines, section_start_line+1)
				item, serialize_err := serializer.Serialize(item_lines, item_start_line)
				if serialize_err != nil {
					panic("Error serializing item: " + serialize_err.Error())
				}
				items = append(items, item)
				slog.Debug("Adding item", "summary", item.Summary(), "details", item.Details(), "line_number", i, "start_line", item_start_line, "line_count", len(item.Details()))
			}
			slog.Debug("Starting to build item", "line", line, "line_number", i)
			item_lines = []string{line}
			building_item = true
			item_start_line = int(i)
			continue
		}
	}

	if building_item {
		// At the end of the file, if we're still building something we need to get the last item and include it
		item, serialize_err := serializer.Serialize(item_lines, item_start_line)
		if serialize_err != nil {
			panic("Error serializing item: " + serialize_err.Error())
		}
		items = append(items, item)
	}
	// if we're at the end of the file, we need to add the last section
	if in_section {
		sections = append(sections, Section{
			Name:        CleanHeader(header),
			StartLine:   section_start_line,
			IndentLevel: 2,
			Items:       items,
		})
	}

	// if section_start_line == 0 {
	//	return sections, errors.New("Did not find parsed section.")
	// }
	return sections, nil
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
			return strings.Split(line, ": ")[1]
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
	return oi.Details()[0]
}

func (oi OrgItem) CheckDone() bool {
	return oi.GetStatus() == "DONE" || oi.GetStatus() == "CANCELLED"
}

func (oi OrgItem) Identifier() string {
	return oi.details[0] + oi.details[1]
}

func findOrgTags(line string) []string {
	splits := strings.Split(line, ":")
	if len(splits) < 2 {
		return []string{}
	} else {
		return splits[1 : len(splits)-1]
	}

}

func findOrgStatus(line string) string {
	for _, status := range GetOrgStatuses() {
		if strings.Contains(line, status) {
			return status
		}
	}
	return ""
}

func PrintOrgFile(file *os.File) {
	res, _ := io.ReadAll(file)
	slog.Info("Org file contents", "content", string(res))
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
