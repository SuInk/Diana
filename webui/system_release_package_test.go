// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/SuInk/diana/model/updater"
)

func TestReleasePackagePrefersManifestRuleOverNamingConvention(t *testing.T) {
	h := NewSystemUpdateHandler(nil)
	h.SetReleasePackageUpdater(&recordingReleasePackageUpdater{expected: "diana-old-name.tar.gz"})
	entry := ReleaseEntry{
		Tag: "v1.2.3",
		Assets: []ReleaseAsset{
			{Name: "diana-old-name.tar.gz", URL: "https://example.test/old"},
			{Name: "diana-new-name.bin", URL: "https://example.test/new", Size: 42},
			{Name: "SHA256SUMS", URL: "https://example.test/sums"},
			{Name: "CHECKSUMS.txt", URL: "https://example.test/checksums"},
		},
		Packages: []ReleasePackageRule{{
			OS: runtime.GOOS, Arch: runtime.GOARCH, Asset: "diana-new-name.bin", Format: "exe",
			InstallAs: "diana-webui", Checksums: "CHECKSUMS.txt",
		}},
	}
	release, err := h.releasePackage(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if release.Archive.URL != "https://example.test/new" || release.Archive.Size != 42 ||
		release.Checksums.Name != "CHECKSUMS.txt" || release.Format != "exe" || release.InstallAs != "diana-webui" {
		t.Fatalf("releasePackage() = %#v", release)
	}

	// 清单点名的文件不在资产列表里就报缺包，不回落到约定名，也不自己拼地址。
	entry.Packages[0].Asset = "diana-missing.bin"
	if _, err := h.releasePackage(context.Background(), entry); !errors.Is(err, updater.ErrReleaseAssetMissing) {
		t.Fatalf("releasePackage() with missing rule asset error = %v", err)
	}
}

func TestReleasePackageReadsPublishedManifestForAPIReleases(t *testing.T) {
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"schema": 1, "releases": []map[string]any{{
			"tag": "v1.2.3",
			"packages": []map[string]string{{
				"os": runtime.GOOS, "arch": runtime.GOARCH, "asset": "diana-new-name.bin", "format": "exe",
			}},
		}}})
	}))
	defer server.Close()
	h := NewSystemUpdateHandler(nil)
	h.httpClient = server.Client()
	h.SetReleasePackageUpdater(&recordingReleasePackageUpdater{expected: "diana-old-name.tar.gz"})
	entry := ReleaseEntry{Tag: "v1.2.3", Assets: []ReleaseAsset{
		{Name: "diana-new-name.bin", URL: "https://example.test/new"},
		{Name: "SHA256SUMS.txt", URL: "https://example.test/sums"},
		{Name: "latest.json", URL: server.URL + "/latest.json"},
	}}
	for range 2 {
		release, err := h.releasePackage(context.Background(), entry)
		if err != nil {
			t.Fatal(err)
		}
		if release.Archive.Name != "diana-new-name.bin" || release.Format != "exe" || release.Checksums.Name != "SHA256SUMS.txt" {
			t.Fatalf("releasePackage() = %#v", release)
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("published manifest fetched %d times, want 1", fetches.Load())
	}

	// 旧版本没带清单，按命名约定找包，校验文件还是不带后缀的旧名。
	entry = ReleaseEntry{Tag: "v1.2.2", Assets: []ReleaseAsset{
		{Name: "diana-old-name.tar.gz", URL: "https://example.test/old"},
		{Name: "SHA256SUMS", URL: "https://example.test/old-sums"},
	}}
	release, err := h.releasePackage(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if release.Archive.Name != "diana-old-name.tar.gz" || release.Format != "" || release.Checksums.Name != "SHA256SUMS" {
		t.Fatalf("releasePackage() by convention = %#v", release)
	}
}
