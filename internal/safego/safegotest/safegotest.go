// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// Package safegotest checks that a package's goroutine panic guard really
// recovers. The AST test in safego only proves a deferred recover* call
// exists; it cannot tell whether recover() runs in the right frame.
package safegotest

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// AssertRecovers panics inside a goroutine guarded exactly like production
// code (`defer recoverPanic(component)`) and requires the goroutine to finish
// normally and the panic to be logged under wantComponent. A guard that does
// not recover crashes the test binary, which is the loudest possible failure.
func AssertRecovers(t *testing.T, recoverPanic func(component string), component, wantComponent string) {
	t.Helper()

	logs := captureLogs(t)
	const marker = "safegotest probe panic"
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer recoverPanic(component)
		panic(marker)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("guarded goroutine did not finish after panicking")
	}

	output := logs.String()
	for _, want := range []string{"goroutine panic recovered", "component=" + wantComponent, marker} {
		if !strings.Contains(output, want) {
			t.Fatalf("panic log missing %q:\n%s", want, output)
		}
	}
}

// captureLogs routes slog (and the stdlib log bridge) into a buffer for the
// rest of the test and restores both loggers afterwards.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	previous := slog.Default()
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	buffer := &lockedBuffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(writer)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	})
	return buffer
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
