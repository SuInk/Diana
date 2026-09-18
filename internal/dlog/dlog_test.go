// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package dlog

import (
	"bytes"
	"log"
	"regexp"
	"strings"
	"testing"
)

func TestInitCompatFormatAndBridge(t *testing.T) {
	var buf bytes.Buffer
	Init(&buf)
	defer Init(nil)

	// New-code leveled helper.
	Error("disk almost full", "path", "/data", "free_bytes", 1024)

	// Legacy stdlib call site routed through the same pipeline.
	log.Printf("logging to %s", "/tmp/diana.log")

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}

	// Line format: `2006/01/02 15:04:05 [LEVEL ]message key=value ...`
	lineRe := regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)
	for _, line := range lines {
		if !lineRe.MatchString(line) {
			t.Errorf("line missing stdlib-style timestamp prefix: %q", line)
		}
	}
	if !strings.Contains(lines[0], "ERROR disk almost full path=/data free_bytes=1024") {
		t.Errorf("leveled record mismatch: %q", lines[0])
	}
	// Bridged stdlib records keep the historical look: no level token.
	if !strings.Contains(lines[1], " logging to /tmp/diana.log") {
		t.Errorf("bridged stdlib record mismatch: %q", lines[1])
	}
}

func TestInitNilRestoresStdlib(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	Init(&buf)
	Init(nil)
	if log.Writer() != previous {
		t.Fatal("Init(nil) did not restore the stdlib log writer")
	}
	log.SetOutput(previous)
}

func TestErrorfReturnsError(t *testing.T) {
	var buf bytes.Buffer
	Init(&buf)
	defer Init(nil)

	err := Errorf("wrap %d", 42)
	if err == nil || err.Error() != "wrap 42" {
		t.Fatalf("Errorf error mismatch: %v", err)
	}
	if !strings.Contains(buf.String(), "ERROR wrap 42") {
		t.Errorf("Errorf did not log at error level: %q", buf.String())
	}
}
