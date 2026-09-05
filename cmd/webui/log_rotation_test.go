// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFollowLogRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diana.log")
	w, err := newRotatingLogWriter(path, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	f, err := openFollowedLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.ReadAll(f); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	f, err = refreshFollowedLog(f, path)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := io.ReadAll(f); err != nil || string(data) != "new" {
		t.Fatalf("follow: %q %v", data, err)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	f, err = refreshFollowedLog(f, path)
	if err != nil {
		t.Fatal(err)
	}
	if offset, err := f.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
		t.Fatalf("truncated offset=%d err=%v", offset, err)
	}
}

func TestLogRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diana.log")
	w, err := newRotatingLogWriter(path, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("abcdefghijklmn")); n != 14 || err != nil {
		t.Fatalf("write=%d %v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, want := range map[string]string{"": "mn", ".1": "ijkl", ".2": "efgh"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil || string(data) != want {
			t.Fatalf("%s: %q, %v", suffix, data, err)
		}
	}
	if _, err := w.Write([]byte("closed")); err == nil {
		t.Fatal("write after close succeeded")
	}
	w, err = newRotatingLogWriter(path, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("opq")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path + ".1"); err != nil || string(data) != "mnop" {
		t.Fatalf("restart: %q, %v", data, err)
	}
}

func TestLogRotationConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diana.log")
	w, err := newRotatingLogWriter(path, 32, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := w.Write([]byte("line\n")); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 3 {
		t.Fatalf("files=%d err=%v", len(entries), err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.Size() > 32 {
			t.Fatalf("oversized log: %s %v", entry.Name(), err)
		}
	}
}
