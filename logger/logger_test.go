package logger

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDefaultsToTextAtInfo(t *testing.T) {
	var stderr bytes.Buffer
	log := NewFromEnv(env(nil), &stderr)
	log.Debug("hidden")
	log.Info("Webhook cycle", "mode", "webhook")

	out := stderr.String()
	if strings.Contains(out, "hidden") {
		t.Error("debug line logged at the default level")
	}
	if !strings.Contains(out, " - INFO - Webhook cycle mode=webhook") {
		t.Errorf("unexpected text line: %q", out)
	}
}

func TestJSONFormatAndLevel(t *testing.T) {
	var stderr bytes.Buffer
	log := NewFromEnv(env(map[string]string{LogFormatEnv: "JSON", LogLevelEnv: "debug"}), &stderr)
	log.Debug("GitHub API Call", "url", "https://api.github.com/repos/acme/widgets/pulls")

	var line map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON object: %q: %v", stderr.String(), err)
	}
	if line["msg"] != "GitHub API Call" || line["level"] != "DEBUG" || line["url"] == nil {
		t.Errorf("line = %v", line)
	}
}

func TestLogFileGetsEveryLineToo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "crs.log")
	var stderr bytes.Buffer
	log := NewFromEnv(env(map[string]string{LogFileEnv: path}), &stderr)
	log.Info("first")
	log.Info("second")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log file: %v", err)
	}
	if got := strings.Count(string(data), "\n"); got != 2 || !strings.Contains(string(data), "second") {
		t.Errorf("log file = %q", data)
	}
	if stderr.String() != string(data) {
		t.Error("stderr and the file got different lines")
	}
}

func TestOversizedLogFileIsMovedAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crs.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxLogFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	log := NewFromEnv(env(map[string]string{LogFileEnv: path}), &bytes.Buffer{})
	log.Info("fresh")

	if info, err := os.Stat(path + ".1"); err != nil || info.Size() <= maxLogFileBytes {
		t.Fatalf("old log not moved aside: %v", err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "fresh") || len(data) > 1024 {
		t.Errorf("new log = %d bytes", len(data))
	}
}

func TestUnopenableLogFileFallsBackToStderr(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, nil, 0o644)
	var stderr bytes.Buffer
	log := NewFromEnv(env(map[string]string{LogFileEnv: filepath.Join(blocker, "crs.log")}), &stderr)
	log.Info("still here")
	if !strings.Contains(stderr.String(), "Could not open the log file") || !strings.Contains(stderr.String(), "still here") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
