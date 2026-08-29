package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLogsOptions(t *testing.T) {
	options, err := parseLogsOptions([]string{"-f", "--lines", "42", "--config=/tmp/diana.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if !options.follow || options.lines != 42 || options.configPath != "/tmp/diana.yaml" {
		t.Fatalf("options = %#v", options)
	}
}

func TestReadLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diana.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, offset, err := readLastLines(file, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "three\nfour\n" || offset != int64(len("one\ntwo\nthree\nfour\n")) {
		t.Fatalf("readLastLines() = (%q, %d)", got, offset)
	}
}

func TestRunLogsCommandUsesConfiguredRelativePath(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte("storage:\n  log_path: logs/diana.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "logs", "diana.log"), []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := runLogsCommand([]string{"--config", configPath, "-n", "1"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "second\n" {
		t.Fatalf("output = %q", output.String())
	}
}
