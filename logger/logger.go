package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type CustomHandler struct {
	w    io.Writer
	mu   sync.Mutex
	opts slog.HandlerOptions
}

func NewCustomHandler(w io.Writer, opts *slog.HandlerOptions) *CustomHandler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	return &CustomHandler{
		w:    w,
		opts: *opts,
	}
}

func (h *CustomHandler) Enabled(_ context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return level >= minLevel
}

func (h *CustomHandler) Handle(_ context.Context, r slog.Record) error {
	buf := make([]byte, 0, 1024)

	// DATE::TIME - LEVEL - MESSAGE
	buf = r.Time.AppendFormat(buf, "2006-01-02::15:04:05")
	buf = append(buf, " - "...)
	buf = append(buf, r.Level.String()...)
	buf = append(buf, " - "...)
	buf = append(buf, r.Message...)

	r.Attrs(func(a slog.Attr) bool {
		buf = append(buf, ' ')
		buf = append(buf, a.Key...)
		buf = append(buf, '=')
		buf = append(buf, a.Value.String()...)
		return true
	})

	buf = append(buf, '\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf)
	return err
}

func (h *CustomHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// For simplicity, this handler does not support WithAttrs.
	// The attributes will be logged, but not pre-formatted.
	return h
}

func (h *CustomHandler) WithGroup(name string) slog.Handler {
	// For simplicity, this handler does not support WithGroup.
	return h
}

// The environment variables that shape the log. The server's log goes to
// stderr, which each client sends somewhere different (a terminal, an Emacs
// buffer, the Chrome native host's log file); these give it a form and a place
// of its own that a script or an agent can read.
const (
	// LogFormatEnv is "text" (the default) or "json", one object per line.
	LogFormatEnv = "CRS_LOG_FORMAT"
	// LogFileEnv names a file the log is appended to as well as stderr.
	LogFileEnv = "CRS_LOG_FILE"
	// LogLevelEnv is "debug", "info" (the default), "warn" or "error".
	LogLevelEnv = "CRS_LOG_LEVEL"
)

// maxLogFileBytes is how large CRS_LOG_FILE may grow before the next start
// moves it aside to <file>.1, replacing the one moved aside before.
const maxLogFileBytes = 64 << 20

func New() *slog.Logger {
	return NewFromEnv(os.Getenv, os.Stderr)
}

// NewFromEnv builds the logger the environment asks for, writing to stderr
// and, when LogFileEnv is set, to that file too. A file that can't be opened
// is reported on the logger rather than stopping the server.
func NewFromEnv(getenv func(string) string, stderr io.Writer) *slog.Logger {
	var w io.Writer = stderr
	path := strings.TrimSpace(getenv(LogFileEnv))
	var fileErr error
	if path != "" {
		if f, err := openLogFile(path); err != nil {
			fileErr = err
		} else {
			w = io.MultiWriter(stderr, f)
		}
	}

	opts := &slog.HandlerOptions{Level: parseLevel(getenv(LogLevelEnv))}
	var handler slog.Handler
	if strings.EqualFold(strings.TrimSpace(getenv(LogFormatEnv)), "json") {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = NewCustomHandler(w, opts)
	}
	log := slog.New(handler)
	if fileErr != nil {
		log.Warn("Could not open the log file; logging to stderr only", "path", path, "error", fileErr)
	}
	return log
}

// parseLevel reads a LogLevelEnv value, defaulting to info.
func parseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// openLogFile opens path for appending — a leading ~/ is the home directory —
// after moving it aside if it has outgrown maxLogFileBytes. Several servers
// (one per client) may share the file; each line is a single append.
func openLogFile(path string) (*os.File, error) {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogFileBytes {
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}
