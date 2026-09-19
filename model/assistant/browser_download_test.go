// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// chromeForTestingTestServer 起一个假 Chrome for Testing 服务：versions JSON 指向
// 自己，/chrome.zip 返回调用方给的 zip 内容。
func chromeForTestingTestServer(t *testing.T, zipBody []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	platform := chromeForTestingPlatform()
	mux.HandleFunc("/versions.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"channels":{"Stable":{"version":"153.0.0.0","downloads":{"chrome":[{"platform":%q,"url":%q}]}}}}`,
			platform, server.URL+"/chrome.zip")
	})
	mux.HandleFunc("/chrome.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipBody)
	})
	return server
}

func fakeChromeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	writer := zip.NewWriter(buf)
	for name, body := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDownloadChromeForTestingExtractsExecutable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIANA_BROWSER_DIR", dir)

	binaryBody := "#!/bin/sh\necho 'Google Chrome 153.0.0.0'\n"
	zipBody := fakeChromeZip(t, map[string]string{
		"chrome-" + chromeForTestingPlatform() + "/chrome":  binaryBody,
		"chrome-" + chromeForTestingPlatform() + "/LICENSE": "fake license\n",
	})
	server := chromeForTestingTestServer(t, zipBody)
	defer server.Close()

	oldEndpoint := chromeForTestingVersionsEndpoint
	chromeForTestingVersionsEndpoint = server.URL + "/versions.json"
	defer func() { chromeForTestingVersionsEndpoint = oldEndpoint }()

	if chromeForTestingPlatform() == "" {
		// 非 Linux 没有官方 CfT 构建，必须引导走系统包管理器。
		_, err := downloadChromeForTesting(context.Background())
		if err == nil || !strings.Contains(err.Error(), "只提供 Linux") {
			t.Fatalf("非 Linux 平台应拒绝下载并说明原因，实际：%v", err)
		}
		return
	}

	path, err := downloadChromeForTesting(context.Background())
	if err != nil {
		t.Fatalf("下载解压失败：%v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("解压产物缺失：%v", err)
	}
	if string(body) != binaryBody {
		t.Fatalf("解压产物内容不对：%q", body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("可执行文件没有执行权限：%v", info.Mode())
	}
	// zip 是下载到目标目录的临时文件，结束后必须清掉。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("下载目录里应只剩解压产物，实际：%v", names)
	}
}

func TestExtractChromeZipRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	zipBody := fakeChromeZip(t, map[string]string{"../evil": "pwned"})
	zipPath := filepath.Join(dir, "slip.zip")
	if err := os.WriteFile(zipPath, zipBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := extractChromeZip(zipPath, dir); err == nil || !strings.Contains(err.Error(), "不安全路径") {
		t.Fatalf("zip slip 应被拒绝，实际：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "evil")); err == nil {
		t.Fatal("zip slip 文件被写出了解压目录")
	}
}

func TestChromeForTestingPlatformOnlyLinux(t *testing.T) {
	platform := chromeForTestingPlatform()
	switch {
	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		if platform != "linux64" {
			t.Fatalf("amd64 平台名应为 linux64，实际 %q", platform)
		}
	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		if platform != "linux-arm64" {
			t.Fatalf("arm64 平台名应为 linux-arm64，实际 %q", platform)
		}
	default:
		if platform != "" {
			t.Fatalf("非 Linux 平台应返回空串，实际 %q", platform)
		}
	}
}
