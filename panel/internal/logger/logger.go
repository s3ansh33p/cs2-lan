package logger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// panelHandler is a custom slog.Handler that produces human-readable log lines:
//
//	2026-04-12 14:03:22 [INFO] message key=value key="quoted value"
type panelHandler struct {
	w     io.Writer
	mu    *sync.Mutex
	level slog.Level
	group string
	attrs []slog.Attr
}

func (h *panelHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *panelHandler) Handle(_ context.Context, r slog.Record) error {
	var buf bytes.Buffer

	buf.WriteString(r.Time.Format("2006-01-02 15:04:05"))
	buf.WriteString(" [")
	buf.WriteString(r.Level.String())
	buf.WriteString("] ")

	if h.group != "" {
		buf.WriteString(h.group)
		buf.WriteString(": ")
	}

	buf.WriteString(r.Message)

	writeAttrs := func(a slog.Attr) bool {
		if a.Key == "" {
			return true
		}
		buf.WriteByte(' ')
		buf.WriteString(a.Key)
		buf.WriteByte('=')
		val := a.Value.Resolve().String()
		if strings.ContainsAny(val, " \t\"\n") {
			fmt.Fprintf(&buf, "%q", val)
		} else {
			buf.WriteString(val)
		}
		return true
	}

	for _, a := range h.attrs {
		writeAttrs(a)
	}
	r.Attrs(writeAttrs)

	buf.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := h.w.Write(buf.Bytes())
	return err
}

func (h *panelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &panelHandler{
		w:     h.w,
		mu:    h.mu,
		level: h.level,
		group: h.group,
		attrs: append(append([]slog.Attr{}, h.attrs...), attrs...),
	}
}

func (h *panelHandler) WithGroup(name string) slog.Handler {
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &panelHandler{
		w:     h.w,
		mu:    h.mu,
		level: h.level,
		group: g,
		attrs: h.attrs,
	}
}

// Setup opens the log file, configures a human-readable slog handler as the
// default logger, and redirects Go's stdlib log package to the same file.
// Returns the open file so the caller can defer Close().
func Setup(filePath string) (*os.File, error) {
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	mu := &sync.Mutex{}
	handler := &panelHandler{
		w:     f,
		mu:    mu,
		level: slog.LevelInfo,
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)

	// Redirect stdlib log to the same file so third-party libraries
	// (gorcon, Docker SDK, etc.) also write to the log file.
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime)

	slog.Info("logging started", "file", filePath)

	return f, nil
}
