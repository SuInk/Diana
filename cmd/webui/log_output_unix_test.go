// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package main

import "testing"

func TestParseJournalStream(t *testing.T) {
	if device, inode, ok := parseJournalStream("8:12345"); !ok || device != 8 || inode != 12345 {
		t.Fatalf("parsed %d:%d ok=%t", device, inode, ok)
	}
	for _, value := range []string{"", "8", "8:", ":1", "a:b"} {
		if _, _, ok := parseJournalStream(value); ok {
			t.Fatalf("%q parsed as a journal stream", value)
		}
	}
}

// 从 systemd 会话里继承了 JOURNAL_STREAM、标准输出却已经重定向走了，不能当成 journal。
func TestStdoutIsJournalRequiresMatchingStream(t *testing.T) {
	t.Setenv("JOURNAL_STREAM", "1:1")
	if stdoutIsJournal() {
		t.Fatal("stdout matched an unrelated journal stream")
	}
}
