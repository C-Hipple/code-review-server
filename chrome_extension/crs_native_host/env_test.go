package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	input := strings.Join([]string{
		"# Written by install.sh --capture-env",
		"",
		"PATH=~/go/bin:/usr/local/bin:/usr/bin",
		`export CRS_GITHUB_TOKEN="ghp_abc#123"`,
		"export\tTABBED=1",
		"SINGLE='a \"quoted\" value'",
		"  SPACED  =  padded value  ",
		"EMPTY=",
		"TILDE=~",
		"NOT_LEADING=a/~/b",
		`QUOTED_TILDE="~/x"`,
		`UNMATCHED="abc`,
		"ghp_this_line_has_no_equals",
		"1BAD=x",
		"=novalue",
		"   # indented comment",
		"PATH=/override/bin",
	}, "\n")
	vars, warnings, err := parseEnvFile(strings.NewReader(input), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	want := []envVar{
		{"PATH", "/home/u/go/bin:/usr/local/bin:/usr/bin"},
		{"CRS_GITHUB_TOKEN", "ghp_abc#123"},
		{"TABBED", "1"},
		{"SINGLE", `a "quoted" value`},
		{"SPACED", "padded value"},
		{"EMPTY", ""},
		{"TILDE", "/home/u"},
		{"NOT_LEADING", "a/~/b"},
		{"QUOTED_TILDE", "/home/u/x"},
		{"UNMATCHED", `"abc`},
		{"PATH", "/override/bin"},
	}
	if !reflect.DeepEqual(vars, want) {
		t.Errorf("vars:\n got %q\nwant %q", vars, want)
	}
	wantWarnings := []string{
		"line 13: no '=' in assignment",
		"line 14: invalid variable name",
		"line 15: invalid variable name",
	}
	if !reflect.DeepEqual(warnings, wantWarnings) {
		t.Errorf("warnings:\n got %q\nwant %q", warnings, wantWarnings)
	}
	for _, w := range warnings {
		if strings.Contains(w, "ghp_") {
			t.Errorf("warning %q repeats the line's content", w)
		}
	}
}

func TestMergeEnvFileOverridesInheritedEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native_host.env")
	content := "CRS_TEST_OVERRIDDEN=from-file\nCRS_TEST_NEW=~/new\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRS_TEST_OVERRIDDEN", "inherited")
	t.Setenv("CRS_TEST_UNTOUCHED", "inherited")
	t.Setenv("CRS_TEST_NEW", "") // registers the restore
	os.Unsetenv("CRS_TEST_NEW")

	keys, warnings, err := mergeEnvFile(path, "/home/u", true)
	if err != nil || len(warnings) > 0 {
		t.Fatalf("mergeEnvFile: %v %q", err, warnings)
	}
	if want := []string{"CRS_TEST_OVERRIDDEN", "CRS_TEST_NEW"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %q, want %q", keys, want)
	}
	for key, want := range map[string]string{
		"CRS_TEST_OVERRIDDEN": "from-file",
		"CRS_TEST_UNTOUCHED":  "inherited",
		"CRS_TEST_NEW":        "/home/u/new",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestMergeEnvFileMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.env")
	if keys, _, err := mergeEnvFile(missing, "/home/u", false); err != nil || keys != nil {
		t.Errorf("missing default file: keys %q, err %v; want nothing", keys, err)
	}
	if _, _, err := mergeEnvFile(missing, "/home/u", true); err == nil {
		t.Error("missing file named by CRS_NATIVE_HOST_ENV: want an error")
	}
}

func TestEnvFilePath(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if got := envFilePath(getenv, "/home/u"); got != "/home/u/.crs/native_host.env" {
		t.Errorf("default = %q", got)
	}
	env[envFileVar] = "/etc/crs.env"
	if got := envFilePath(getenv, "/home/u"); got != "/etc/crs.env" {
		t.Errorf("with %s = %q", envFileVar, got)
	}
}

func TestLogFilePath(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if got := logFilePath(getenv, "/home/u"); got != "/home/u/.crs/native_host.log" {
		t.Errorf("default = %q", got)
	}
	env["CRS_HOME"] = "/data/crs"
	if got := logFilePath(getenv, "/home/u"); got != "/data/crs/native_host.log" {
		t.Errorf("with CRS_HOME = %q", got)
	}
}

func TestOpenLogTruncatesOnlyLargeLogs(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "nested", "small.log")
	if err := os.MkdirAll(filepath.Dir(small), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(small, []byte("earlier run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(dir, "large.log")
	if err := os.WriteFile(large, make([]byte, maxLogBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, wantSize := range map[string]int64{small: 12, large: 0} {
		f, err := openLog(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := f.Stat()
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != wantSize {
			t.Errorf("%s: size %d, want %d", filepath.Base(path), info.Size(), wantSize)
		}
	}

	created := filepath.Join(dir, "new", "dir", "native_host.log")
	f, err := openLog(created)
	if err != nil {
		t.Fatalf("openLog should create missing directories: %v", err)
	}
	f.Close()
}
