// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package dlog centralizes Diana's process logging on slog while keeping
// byte-level output compatible with the historical stdlib log format
// (log.LstdFlags). Existing log.Printf call sites keep working unchanged:
// since Go 1.24 slog.SetDefault also routes the stdlib default logger into
// the slog handler, so every line lands in one destination with one format.
// New code should use the leveled helpers below to gain levels and
// structured attributes.
package dlog

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
)

var (
	mu       sync.Mutex
	savedLog struct {
		writer io.Writer
		flags  int
		prefix string
		ok     bool
	}
)

// Init installs w as the single log destination and routes both slog
// records and stdlib log output through one handler. The emitted line
// format matches the historical `log.SetOutput(w)` behavior, so log files,
// the `diana logs` CLI, and console output look exactly as before.
//
// It is safe to call Init multiple times (tests do); each call replaces the
// previous configuration. A nil writer detaches the pipeline and restores
// the stdlib logger to the state captured before the first Init.
func Init(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	if !savedLog.ok {
		savedLog.writer = log.Writer()
		savedLog.flags = log.Flags()
		savedLog.prefix = log.Prefix()
		savedLog.ok = true
	}
	if w == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
		log.SetOutput(savedLog.writer)
		log.SetFlags(savedLog.flags)
		log.SetPrefix(savedLog.prefix)
		return
	}
	// slog.SetDefault (Go 1.24+) also redirects stdlib log.Printf output
	// into this handler; strip stdlib's own timestamp so the compat
	// handler is the single place that renders the time prefix.
	log.SetFlags(0)
	log.SetPrefix("")
	handler := newCompatHandler(w)
	slog.SetDefault(slog.New(handler))
}

// Compat line format: `2006/01/02 15:04:05 [ERROR|WARN ]message key=value`.
// Bridged stdlib records arrive at Info level and therefore keep the
// historical look with no level token.
type compatHandler struct {
	w io.Writer
}

func newCompatHandler(w io.Writer) slog.Handler {
	return &compatHandler{w: w}
}

func (h *compatHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *compatHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("2006/01/02 15:04:05 "))
	if r.Level >= slog.LevelError {
		b.WriteString("ERROR ")
	} else if r.Level >= slog.LevelWarn {
		b.WriteString("WARN ")
	}
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString("=")
		b.WriteString(fmt.Sprintf("%v", a.Value.Any()))
		return true
	})
	b.WriteString("\n")
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *compatHandler) WithAttrs(_ []slog.Attr) slog.Handler {
	return h
}

func (h *compatHandler) WithGroup(_ string) slog.Handler {
	return h
}

// Leveled helpers for new code. They delegate to the slog default logger,
// so they respect Init's destination and accept attributes naturally.

func Debug(msg string, args ...any) { slog.Debug(msg, args...) }

func Info(msg string, args ...any) { slog.Info(msg, args...) }

func Warn(msg string, args ...any) { slog.Warn(msg, args...) }

func Error(msg string, args ...any) { slog.Error(msg, args...) }

// Errorf formats like fmt.Errorf, logs at Error level, and returns the
// error so call sites can keep one-liner `return dlog.Errorf(...)`.
func Errorf(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	slog.Error(err.Error())
	return err
}
