package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestResolveServerOrder(t *testing.T) {
	root := t.TempDir()
	name := serverBinaryName()
	home := filepath.Join(root, "home")
	hostExe := filepath.Join(root, "hostdir", "crs_native_host")
	env := map[string]string{
		"PATH":   strings.Join([]string{"relative/bin", filepath.Join(root, "empty"), filepath.Join(root, "path")}, string(os.PathListSeparator)),
		"GOBIN":  filepath.Join(root, "gobin"),
		"GOPATH": strings.Join([]string{filepath.Join(root, "gopath1"), filepath.Join(root, "gopath2")}, string(os.PathListSeparator)),
	}
	getenv := func(k string) string { return env[k] }

	explicit := filepath.Join(root, "explicit", "my-server")
	order := []string{
		explicit,
		filepath.Join(root, "path", name),
		filepath.Join(root, "hostdir", name),
		filepath.Join(root, "gobin", name),
		filepath.Join(root, "gopath1", "bin", name),
		filepath.Join(root, "gopath2", "bin", name),
		filepath.Join(home, "go", "bin", name),
	}
	for _, p := range order {
		writeExecutable(t, p, 0o755)
	}
	env[serverPathVar] = explicit

	// Each candidate wins while it exists; removing it hands over to the next.
	for _, want := range order {
		got, err := resolveServer(getenv, hostExe, home)
		if err != nil {
			t.Fatalf("want %s, got error %v", want, err)
		}
		if got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
		if err := os.Remove(want); err != nil {
			t.Fatal(err)
		}
	}

	_, err := resolveServer(getenv, hostExe, home)
	if err == nil {
		t.Fatal("nothing left to find, but no error")
	}
	msg := err.Error()
	for _, mention := range append([]string{"$" + serverPathVar + "=" + explicit, name + " on PATH (" + env["PATH"] + ")"}, order[2:]...) {
		if !strings.Contains(msg, mention) {
			t.Errorf("error doesn't mention %q:\n%s", mention, msg)
		}
	}
}

func TestResolveServerSkipsWhatCannotRun(t *testing.T) {
	root := t.TempDir()
	name := serverBinaryName()
	// A file that isn't executable, a directory with the server's name, and
	// an executable in a relative PATH entry are all passed over.
	writeExecutable(t, filepath.Join(root, "noexec", name), 0o644)
	if err := os.MkdirAll(filepath.Join(root, "isdir", name), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	writeExecutable(t, filepath.Join(root, "rel", name), 0o755)
	want := filepath.Join(root, "good", name)
	writeExecutable(t, want, 0o755)

	path := strings.Join([]string{
		filepath.Join(root, "noexec"),
		filepath.Join(root, "isdir"),
		"rel",
		filepath.Join(root, "good"),
	}, string(os.PathListSeparator))
	got, err := resolveServer(func(k string) string {
		if k == "PATH" {
			return path
		}
		return ""
	}, "", "")
	if err != nil || got != want {
		t.Errorf("got %q, %v; want %s", got, err, want)
	}
}

func TestResolveServerMakesExplicitPathAbsolute(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeExecutable(t, filepath.Join(root, "bin", "srv"), 0o755)
	got, err := resolveServer(func(k string) string {
		if k == serverPathVar {
			return filepath.Join("bin", "srv")
		}
		return ""
	}, "", "")
	if err != nil || got != filepath.Join(root, "bin", "srv") {
		t.Errorf("got %q, %v", got, err)
	}
}
