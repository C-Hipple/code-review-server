package main

import (
	"crs/cmd/internal/pluginkit"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// guideDirEnv names a directory of Markdown files that together make up the
// style guide. The server passes its own environment to a plugin, so
// exporting it to the server is how a [[Plugins]] entry reaches it.
const guideDirEnv = "CRS_STYLE_GUIDE_DIR"

const (
	// maxGuideFiles and maxGuideFileBytes bound what a directory can load, so
	// a guide pointed at the wrong place fails rather than reading a repo.
	maxGuideFiles     = 500
	maxGuideFileBytes = 1 << 20

	// inlineGuideBytes is the largest guide whose full text goes into the
	// prompts. A larger one is given as an index the model reads from with
	// the guide tools.
	inlineGuideBytes = 40000

	// maxToolResultBytes caps what one tool call adds to the conversation,
	// which is resent on every turn.
	maxToolResultBytes = 20000

	maxSearchMatches = 60
	// maxIndexHeadings bounds the headings the index lists for one file.
	maxIndexHeadings = 15
)

// guideExtensions are the files a guide directory is read for.
var guideExtensions = map[string]bool{".md": true, ".markdown": true, ".mdx": true}

type guideFile struct {
	// Path is relative to the guide's root, with forward slashes: what the
	// model names a file by.
	Path    string
	Content string
}

// styleGuide is the style guide the diff is checked against: a single file,
// or every Markdown file under a directory.
type styleGuide struct {
	// Source is where it was read from, for the report.
	Source string
	Files  []guideFile
}

// locateGuide says where the style guide lives, in order of precedence: the
// --style-guide-dir flag, CRS_STYLE_GUIDE_DIR, a ~/.config/style_guidelines/
// directory, and the single ~/.config/style_guidelines.md file.
func locateGuide(flagDir string) (path string, isDir bool, err error) {
	for _, explicit := range []struct{ value, from string }{
		{flagDir, "--style-guide-dir"},
		{os.Getenv(guideDirEnv), guideDirEnv},
	} {
		if strings.TrimSpace(explicit.value) == "" {
			continue
		}
		dir := expandHome(strings.TrimSpace(explicit.value))
		info, err := os.Stat(dir)
		if err != nil {
			return "", false, fmt.Errorf("could not read the style guide directory %s (from %s): %w", dir, explicit.from, err)
		}
		if !info.IsDir() {
			return "", false, fmt.Errorf("%s (from %s) is not a directory", dir, explicit.from)
		}
		return dir, true, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("could not determine home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "style_guidelines")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir, true, nil
	}
	return filepath.Join(home, ".config", "style_guidelines.md"), false, nil
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

func loadStyleGuide(flagDir string) (*styleGuide, error) {
	path, isDir, err := locateGuide(flagDir)
	if err != nil {
		return nil, err
	}
	if isDir {
		return loadGuideDir(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read style guidelines from %s: %w", path, err)
	}
	return &styleGuide{Source: path, Files: []guideFile{{Path: filepath.Base(path), Content: string(data)}}}, nil
}

// loadGuideDir reads every Markdown file under root, skipping hidden files
// and directories.
func loadGuideDir(root string) (*styleGuide, error) {
	guide := &styleGuide{Source: root}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !guideExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if len(guide.Files) == maxGuideFiles {
			return fmt.Errorf("more than %d Markdown files under %s; point the style guide at a smaller directory", maxGuideFiles, root)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxGuideFileBytes {
			fmt.Fprintf(os.Stderr, "Warning: skipping %s: larger than %d bytes\n", path, maxGuideFileBytes)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		guide.Files = append(guide.Files, guideFile{Path: filepath.ToSlash(rel), Content: string(data)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("could not read the style guide directory %s: %w", root, err)
	}
	if len(guide.Files) == 0 {
		return nil, fmt.Errorf("no Markdown files (%s) found under %s", strings.Join(sortedKeys(guideExtensions), ", "), root)
	}
	sort.Slice(guide.Files, func(i, j int) bool { return guide.Files[i].Path < guide.Files[j].Path })
	return guide, nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (g *styleGuide) size() int {
	total := 0
	for _, f := range g.Files {
		total += len(f.Content)
	}
	return total
}

func (g *styleGuide) file(path string) (guideFile, bool) {
	path = strings.TrimPrefix(strings.TrimSpace(filepath.ToSlash(path)), "./")
	for _, f := range g.Files {
		if f.Path == path {
			return f, true
		}
	}
	return guideFile{}, false
}

// promptSection is the guide as the prompts carry it: its full text when it
// is small enough, otherwise an index of its files and their headings.
func (g *styleGuide) promptSection() string {
	var b strings.Builder
	if g.size() <= inlineGuideBytes {
		b.WriteString("The style guide is given in full below, one section per file. The guide tools can still search it.\n")
		for _, f := range g.Files {
			fmt.Fprintf(&b, "\n### Guide file: %s\n\n%s\n", f.Path, strings.TrimSpace(f.Content))
		}
		return b.String()
	}
	fmt.Fprintf(&b, "The style guide is %d files, too large to include. Below is an index of each file and its headings; "+
		"use search_style_guide and read_style_guide to read the rules that bear on the diff.\n\n", len(g.Files))
	for _, f := range g.Files {
		lines := strings.Count(f.Content, "\n") + 1
		fmt.Fprintf(&b, "- %s (%d lines)\n", f.Path, lines)
		headings := 0
		for _, line := range strings.Split(f.Content, "\n") {
			level := len(line) - len(strings.TrimLeft(line, "#"))
			if level == 0 || level > 3 || !strings.HasPrefix(line[level:], " ") {
				continue
			}
			if headings == maxIndexHeadings {
				b.WriteString("    - ...\n")
				break
			}
			fmt.Fprintf(&b, "    %s- %s\n", strings.Repeat("  ", level-1), strings.TrimSpace(line[level:]))
			headings++
		}
	}
	return b.String()
}

// read returns lines start..end of a guide file (1-based and inclusive; zero
// means the file's first or last line), numbered.
func (g *styleGuide) read(path string, start, end int) (string, error) {
	f, ok := g.file(path)
	if !ok {
		return "", fmt.Errorf("there is no guide file %q; the files are the ones the guide lists", path)
	}
	lines := strings.Split(f.Content, "\n")
	if start < 1 {
		start = 1
	}
	if end < 1 || end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return "", fmt.Errorf("%s has %d lines; start_line %d is past its end", f.Path, len(lines), start)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s, lines %d-%d of %d:\n", f.Path, start, end, len(lines))
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%5d | %s\n", i, lines[i-1])
	}
	return truncate(b.String(), fmt.Sprintf("read a later range with start_line to see past line %d", end)), nil
}

// search finds the lines of the guide matching pattern, a case-insensitive
// regular expression, or a literal string when it doesn't compile.
func (g *styleGuide) search(pattern string, path string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", errors.New("pattern is empty")
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(pattern))
	}
	files := g.Files
	if strings.TrimSpace(path) != "" {
		f, ok := g.file(path)
		if !ok {
			return "", fmt.Errorf("there is no guide file %q", path)
		}
		files = []guideFile{f}
	}

	var b strings.Builder
	matches := 0
	for _, f := range files {
		for i, line := range strings.Split(f.Content, "\n") {
			if !re.MatchString(line) {
				continue
			}
			matches++
			if matches <= maxSearchMatches {
				fmt.Fprintf(&b, "%s:%d: %s\n", f.Path, i+1, strings.TrimSpace(line))
			}
		}
	}
	if matches == 0 {
		return "no matches for " + pattern, nil
	}
	if matches > maxSearchMatches {
		fmt.Fprintf(&b, "... %d more matches; narrow the pattern or pass a path\n", matches-maxSearchMatches)
	}
	return truncate(b.String(), "narrow the pattern or pass a path"), nil
}

func truncate(s, hint string) string {
	if len(s) <= maxToolResultBytes {
		return s
	}
	cut := strings.LastIndex(s[:maxToolResultBytes], "\n")
	if cut < 0 {
		cut = maxToolResultBytes
	}
	return s[:cut] + "\n... truncated; " + hint + "\n"
}

// guideTools are the tools both phases get for reading the guide.
func guideTools(g *styleGuide) []tool {
	return []tool{
		{
			ChatTool: pluginkit.ChatTool{
				Name:        "search_style_guide",
				Description: "Search the style guide for lines matching a case-insensitive regular expression. Returns file:line: text for each match.",
				Parameters: &pluginkit.Schema{
					Type: "OBJECT",
					Properties: map[string]*pluginkit.Schema{
						"pattern": {Type: "STRING", Description: "Regular expression, e.g. \"error handling|errors\"."},
						"path":    {Type: "STRING", Description: "Optional guide file to limit the search to."},
					},
					PropertyOrdering: []string{"pattern", "path"},
					Required:         []string{"pattern"},
				},
			},
			run: func(args json.RawMessage) (string, error) {
				var a struct {
					Pattern string `json:"pattern"`
					Path    string `json:"path"`
				}
				if err := json.Unmarshal(args, &a); err != nil {
					return "", fmt.Errorf("arguments are not the object expected: %v", err)
				}
				return g.search(a.Pattern, a.Path)
			},
		},
		{
			ChatTool: pluginkit.ChatTool{
				Name:        "read_style_guide",
				Description: "Read a style guide file, or a range of its lines, with line numbers.",
				Parameters: &pluginkit.Schema{
					Type: "OBJECT",
					Properties: map[string]*pluginkit.Schema{
						"path":       {Type: "STRING", Description: "Guide file path, as the guide lists it."},
						"start_line": {Type: "INTEGER", Description: "First line to read (1-based); omit for the start."},
						"end_line":   {Type: "INTEGER", Description: "Last line to read; omit for the end."},
					},
					PropertyOrdering: []string{"path", "start_line", "end_line"},
					Required:         []string{"path"},
				},
			},
			run: func(args json.RawMessage) (string, error) {
				var a struct {
					Path  string `json:"path"`
					Start int    `json:"start_line"`
					End   int    `json:"end_line"`
				}
				if err := json.Unmarshal(args, &a); err != nil {
					return "", fmt.Errorf("arguments are not the object expected: %v", err)
				}
				return g.read(a.Path, a.Start, a.End)
			},
		},
	}
}
