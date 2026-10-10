package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// serverPathVar names the server binary explicitly, ahead of any search.
const serverPathVar = "CRS_SERVER_PATH"

func serverBinaryName() string {
	if runtime.GOOS == "windows" {
		return "codereviewserver.exe"
	}
	return "codereviewserver"
}

// resolveServer finds the codereviewserver binary to spawn. The first hit
// wins, in this order: $CRS_SERVER_PATH; codereviewserver on PATH; next to
// the host's own executable; then where `go install` puts it — $GOBIN,
// each $GOPATH entry's bin, and ~/go/bin — since a browser's PATH rarely
// includes those. The error lists everything tried, so the extension can
// show the user what to fix.
//
// getenv is consulted rather than the process environment so tests can
// describe one; main passes os.Getenv after merging the env file.
func resolveServer(getenv func(string) string, hostExe, home string) (string, error) {
	var tried []string
	if p := getenv(serverPathVar); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if isExecutable(p) {
			return p, nil
		}
		tried = append(tried, fmt.Sprintf("$%s=%s (not an executable file)", serverPathVar, p))
	}

	name := serverBinaryName()
	path := getenv("PATH")
	for _, dir := range filepath.SplitList(path) {
		// A relative entry would resolve against whatever directory the
		// browser started us in; exec.LookPath refuses those too.
		if !filepath.IsAbs(dir) {
			continue
		}
		if p := filepath.Join(dir, name); isExecutable(p) {
			return p, nil
		}
	}
	tried = append(tried, fmt.Sprintf("%s on PATH (%s)", name, path))

	var dirs []string
	if hostExe != "" {
		dirs = append(dirs, filepath.Dir(hostExe))
	}
	if gobin := getenv("GOBIN"); gobin != "" {
		dirs = append(dirs, gobin)
	}
	for _, gopath := range filepath.SplitList(getenv("GOPATH")) {
		if gopath != "" {
			dirs = append(dirs, filepath.Join(gopath, "bin"))
		}
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "go", "bin"))
	}
	seen := make(map[string]bool)
	for _, dir := range dirs {
		p := filepath.Join(dir, name)
		if seen[p] {
			continue
		}
		seen[p] = true
		if isExecutable(p) {
			return p, nil
		}
		tried = append(tried, p)
	}
	return "", fmt.Errorf("%s not found; tried %s", name, strings.Join(tried, ", "))
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}
