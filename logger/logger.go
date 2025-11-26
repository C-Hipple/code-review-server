package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
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

func New() *slog.Logger {
	handler := NewCustomHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	return slog.New(handler)
}
