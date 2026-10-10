package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// envFileVar names an env file to read in place of the default one.
const envFileVar = "CRS_NATIVE_HOST_ENV"

// envVar is one KEY=VALUE assignment from the env file.
type envVar struct {
	Key, Value string
}

// envFilePath is where the host reads extra environment from: Chrome starts
// native hosts with the desktop session's environment, which has none of
// what a login shell exports (PATH additions, CRS_GITHUB_TOKEN, API keys).
func envFilePath(getenv func(string) string, home string) string {
	if p := getenv(envFileVar); p != "" {
		return p
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".crs", "native_host.env")
}

// mergeEnvFile reads the env file at path into the process environment and
// returns the keys it set. A variable in the file replaces the inherited
// one: the file is the user's explicit configuration for the host, while
// the inherited environment is whatever the browser happened to start with.
// A missing file is not an error unless the path was named explicitly.
func mergeEnvFile(path, home string, explicit bool) (keys, warnings []string, err error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("env file: %w", err)
	}
	defer f.Close()

	vars, warnings, err := parseEnvFile(f, home)
	if err != nil {
		return nil, warnings, fmt.Errorf("env file %s: %w", path, err)
	}
	for _, v := range vars {
		if err := os.Setenv(v.Key, v.Value); err != nil {
			warnings = append(warnings, fmt.Sprintf("setting %s: %v", v.Key, err))
			continue
		}
		keys = append(keys, v.Key)
	}
	return keys, warnings, nil
}

// parseEnvFile reads KEY=VALUE lines. Blank lines and lines starting with #
// are skipped, a leading "export " is allowed so the file can be sourced by a
// shell too, and a value wrapped in matching single or double quotes is
// unwrapped (literally: there are no escapes or variable references). A value
// starting with ~/ has it replaced by the home directory. A # later in a line
// is part of the value, since tokens may contain one.
//
// Lines that can't be parsed are skipped with a warning naming the line
// number; warnings never include a value, which may be a secret.
func parseEnvFile(r io.Reader, home string) ([]envVar, []string, error) {
	var (
		vars     []envVar
		warnings []string
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := cutExport(line); ok {
			line = rest
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			warnings = append(warnings, fmt.Sprintf("line %d: no '=' in assignment", lineNo))
			continue
		}
		key = strings.TrimSpace(key)
		if !validEnvKey(key) {
			warnings = append(warnings, fmt.Sprintf("line %d: invalid variable name", lineNo))
			continue
		}
		vars = append(vars, envVar{Key: key, Value: expandHome(unquote(strings.TrimSpace(value)), home)})
	}
	return vars, warnings, sc.Err()
}

func cutExport(line string) (string, bool) {
	for _, prefix := range []string{"export ", "export\t"} {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return line, false
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func unquote(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
}

func expandHome(value, home string) string {
	if home == "" {
		return value
	}
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") {
		// Keep the rest literal: a PATH value goes on past the first entry.
		return strings.TrimSuffix(home, "/") + value[1:]
	}
	return value
}
