// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestObscuraReleaseMatrixCoversDianaPlatforms(t *testing.T) {
	for _, platform := range []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64"} {
		asset, ok := obscuraReleaseAssets[platform]
		if !ok || asset.name == "" || len(asset.sha256) != 64 {
			t.Fatalf("invalid Obscura asset for %s: %#v", platform, asset)
		}
	}
}

func TestExtractObscuraTarGZIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "obscura.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	entries := map[string]string{"README.md": "ignore", "release/obscura": "executable"}
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "installed-obscura")
	if err := extractObscuraTarGZ(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "executable" {
		t.Fatalf("extracted content = %q", content)
	}
}
