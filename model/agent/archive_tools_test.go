// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveTestEntry struct {
	name, body string
	mode       os.FileMode
	kind       byte
}

func writeArchiveFixture(t *testing.T, root, format string, entries []archiveTestEntry) string {
	t.Helper()
	var buf bytes.Buffer
	name := "input." + format
	if format == "zip" {
		zw := zip.NewWriter(&buf)
		for _, entry := range entries {
			h := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
			mode := entry.mode
			if mode == 0 {
				mode = 0o644
			}
			h.SetMode(mode)
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		var tw *tar.Writer
		var gz *gzip.Writer
		if format == "tar.gz" {
			gz = gzip.NewWriter(&buf)
			tw = tar.NewWriter(gz)
		} else {
			tw = tar.NewWriter(&buf)
		}
		for _, entry := range entries {
			kind := entry.kind
			if kind == 0 {
				kind = tar.TypeReg
			}
			h := &tar.Header{Name: entry.name, Mode: 0o644, Typeflag: kind, Size: int64(len(entry.body))}
			if kind != tar.TypeReg {
				h.Size = 0
			}
			if kind == tar.TypeSymlink || kind == tar.TypeLink {
				h.Linkname = "outside"
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Size > 0 {
				if _, err := tw.Write([]byte(entry.body)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if gz != nil {
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, name), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestExtractArchiveFormats(t *testing.T) {
	for _, format := range []string{"zip", "tar", "tar.gz"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			archive := writeArchiveFixture(t, root, format, []archiveTestEntry{{name: "nested/a.txt", body: "hello"}, {name: "empty"}})
			tool := &ExtractArchiveTool{root: root}
			result, err := tool.Run(context.Background(), map[string]any{"archive_path": archive, "destination": "new/deep/out"})
			if err != nil {
				t.Fatal(err)
			}
			var summary map[string]any
			if err := json.Unmarshal([]byte(result), &summary); err != nil {
				t.Fatal(err)
			}
			if summary["files"] != float64(2) || summary["bytes"] != float64(5) {
				t.Fatalf("result %s", result)
			}
			body, err := os.ReadFile(filepath.Join(root, "new/deep/out/nested/a.txt"))
			if err != nil || string(body) != "hello" {
				t.Fatalf("body %q, err %v", body, err)
			}
			assertNoArchiveStage(t, root)
		})
	}
}

func assertNoArchiveStage(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".extract-archive-") {
			t.Fatalf("stage leaked %s", entry.Name())
		}
	}
}

func TestExtractArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, format := range []string{"zip", "tar", "tar.gz"} {
		for _, name := range []string{"../escape", "a/../../escape", "/absolute", "C:/windows", "C:relative", "\\\\server\\share", "a\\b", "a/../b", "."} {
			t.Run(format+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				archive := writeArchiveFixture(t, root, format, []archiveTestEntry{{name: "safe", body: "ok"}, {name: name, body: "bad"}})
				_, err := (&ExtractArchiveTool{root: root}).Run(context.Background(), map[string]any{"archive_path": archive, "destination": "parents/out"})
				if err == nil {
					t.Fatal("accepted unsafe path")
				}
				if _, err := os.Stat(filepath.Join(root, "parents")); !os.IsNotExist(err) {
					t.Fatalf("failure created destination: %v", err)
				}
				assertNoArchiveStage(t, root)
			})
		}
	}
}

func TestExtractArchiveRejectsLinksAndSpecialEntries(t *testing.T) {
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo, tar.TypeChar, tar.TypeBlock} {
		t.Run(string(kind), func(t *testing.T) {
			root := t.TempDir()
			archive := writeArchiveFixture(t, root, "tar", []archiveTestEntry{{name: "entry", kind: kind}})
			_, err := (&ExtractArchiveTool{root: root}).Run(context.Background(), map[string]any{"archive_path": archive, "destination": "out"})
			if err == nil {
				t.Fatal("accepted special entry")
			}
			assertNoArchiveStage(t, root)
		})
	}
	for _, mode := range []os.FileMode{os.ModeSymlink | 0o777, os.ModeNamedPipe | 0o644, os.ModeDevice | 0o644} {
		root := t.TempDir()
		archive := writeArchiveFixture(t, root, "zip", []archiveTestEntry{{name: "entry", body: "target", mode: mode}})
		if _, err := (&ExtractArchiveTool{root: root}).Run(context.Background(), map[string]any{"archive_path": archive, "destination": "out"}); err == nil {
			t.Fatal("accepted ZIP special entry")
		}
	}
}

func TestExtractArchiveLimitsAndRollback(t *testing.T) {
	for _, format := range []string{"zip", "tar", "tar.gz"} {
		for _, limit := range []string{"bytes", "files", "duplicate", "conflict"} {
			t.Run(format+"/"+limit, func(t *testing.T) {
				root := t.TempDir()
				entries := []archiveTestEntry{{name: "first", body: "1234"}, {name: "second", body: "5678"}}
				tool := &ExtractArchiveTool{root: root}
				switch limit {
				case "bytes":
					tool.maxBytes = 7
				case "files":
					tool.maxFiles = 1
				case "duplicate":
					entries[1].name = "first"
				case "conflict":
					entries[1].name = "first/child"
				}
				archive := writeArchiveFixture(t, root, format, entries)
				if err := os.Mkdir(filepath.Join(root, "out"), 0o755); err != nil {
					t.Fatal(err)
				}
				if _, err := tool.Run(context.Background(), map[string]any{"archive_path": archive, "destination": "out"}); err == nil {
					t.Fatal("accepted invalid archive")
				}
				actual, err := os.ReadDir(filepath.Join(root, "out"))
				if err != nil || len(actual) != 0 {
					t.Fatalf("destination modified: %v %v", actual, err)
				}
				assertNoArchiveStage(t, root)
			})
		}
	}
}

func TestExtractArchiveProtectedAndDestination(t *testing.T) {
	for _, which := range []string{"source", "destination", "entry", "occupied", "file", "symlink", "outside", "cancelled"} {
		t.Run(which, func(t *testing.T) {
			root := t.TempDir()
			archive := writeArchiveFixture(t, root, "zip", []archiveTestEntry{{name: "secret", body: "payload"}})
			tool := &ExtractArchiveTool{root: root, protected: protectedFiles{files: map[string]bool{}}}
			ctx := context.Background()
			switch which {
			case "source":
				tool.protected.files[filepath.Join(root, archive)] = true
			case "destination":
				tool.protected.files[filepath.Join(root, "out")] = true
			case "entry":
				tool.protected.files[filepath.Join(root, "out/secret")] = true
			case "occupied":
				if err := os.Mkdir(filepath.Join(root, "out"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "out/existing"), []byte("original"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(filepath.Join(root, "out"), []byte("original"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("real", filepath.Join(root, "out")); err != nil {
					t.Fatal(err)
				}
			case "outside":
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "out")); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := tool.Run(ctx, map[string]any{"archive_path": archive, "destination": "out"}); err == nil {
				t.Fatal("accepted forbidden extraction")
			}
			assertNoArchiveStage(t, root)
			if which == "occupied" {
				body, err := os.ReadFile(filepath.Join(root, "out/existing"))
				if err != nil || string(body) != "original" {
					t.Fatal("overwrote destination")
				}
			}
		})
	}
}

func TestExtractArchiveProtectedAncestor(t *testing.T) {
	root := t.TempDir()
	archive := writeArchiveFixture(t, root, "zip", []archiveTestEntry{{name: "secret/child", body: "payload"}})
	tool := &ExtractArchiveTool{root: root, protected: protectedFiles{files: map[string]bool{filepath.Join(root, "out/secret"): true}}}
	if _, err := tool.Run(context.Background(), map[string]any{"archive_path": archive, "destination": "out"}); err == nil {
		t.Fatal("created a directory at a protected file path")
	}
	if _, err := os.Stat(filepath.Join(root, "out")); !os.IsNotExist(err) {
		t.Fatal("destination created on failure")
	}
}

func TestExtractArchiveExactLimits(t *testing.T) {
	root := t.TempDir()
	archive := writeArchiveFixture(t, root, "zip", []archiveTestEntry{{name: "file", body: "1234"}})
	tool := &ExtractArchiveTool{root: root, maxBytes: 4, maxFiles: 1}
	if _, err := tool.Run(context.Background(), map[string]any{"archive_path": archive, "destination": "out"}); err != nil {
		t.Fatal(err)
	}
}

func TestExtractArchiveCorruptGzip(t *testing.T) {
	root := t.TempDir()
	archive := writeArchiveFixture(t, root, "tar.gz", []archiveTestEntry{{name: "a", body: "abc"}})
	filename := filepath.Join(root, archive)
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-8] ^= 0xff
	if err := os.WriteFile(filename, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ExtractArchiveTool{root: root}).Run(context.Background(), map[string]any{"archive_path": archive, "destination": "out"}); err == nil {
		t.Fatal("accepted invalid gzip checksum")
	}
	assertNoArchiveStage(t, root)
}

func TestExtractArchiveRejectsKeep(t *testing.T) {
	root := t.TempDir()
	archive := writeArchiveFixture(t, root, "zip", []archiveTestEntry{{name: "a.txt", body: "x"}})
	tool := &ExtractArchiveTool{root: root, protected: protectedFiles{files: map[string]bool{}}}
	for _, dest := range []string{"keep", "keep/bot/x"} {
		if _, err := tool.Run(context.Background(), map[string]any{"archive_path": archive, "destination": dest}); err == nil {
			t.Fatalf("extracted into %s", dest)
		}
	}
}
