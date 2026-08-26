// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testArchiveURL  = "https://github.com/SuInk/Diana/releases/download/v1.0.0/diana-webui-linux-amd64.tar.gz"
	testChecksumURL = "https://github.com/SuInk/Diana/releases/download/v1.0.0/SHA256SUMS"
	testMirrorBase  = "https://ghfast.top"
)

// 校验清单是整个下载的信任锚，优先直连；包体大，镜像优先。两头都留了回落。
func TestDownloadSourceOrder(t *testing.T) {
	checksums := checksumSources(testChecksumURL, testMirrorBase)
	if len(checksums) != 2 || checksums[0] != testChecksumURL || checksums[1] != testMirrorBase+"/"+testChecksumURL {
		t.Fatalf("校验清单顺序 = %#v", checksums)
	}
	archive := archiveSources(testArchiveURL, testMirrorBase)
	if len(archive) != 2 || archive[0] != testMirrorBase+"/"+testArchiveURL || archive[1] != testArchiveURL {
		t.Fatalf("发布包顺序 = %#v", archive)
	}
	// 没有镜像可用时不该凭空多出一条重复的线路。
	if got := archiveSources(testArchiveURL, ""); len(got) != 1 || got[0] != testArchiveURL {
		t.Fatalf("无镜像时的顺序 = %#v", got)
	}
	// 非 GitHub 地址（比如自建的分发点）不套镜像。
	other := "https://example.com/diana.tar.gz"
	if got := archiveSources(other, testMirrorBase); len(got) != 1 || got[0] != other {
		t.Fatalf("非 GitHub 地址被改写了：%#v", got)
	}
}

// 第一条线路失败要能换下一条：中途留下的半截文件必须清掉，
// 否则下一次 O_EXCL 打开会直接报「文件已存在」，回落等于没有。
func TestDownloadReleaseFileFallsBackToNextSource(t *testing.T) {
	payload := []byte("diana-release-package")
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/broken":
			attempts++
			// 先写一段再断开：磁盘上会留下半截文件。
			_, _ = w.Write(payload[:4])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		case "/good":
			attempts++
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "package.tar.gz")
	digest, err := downloadReleaseFile(context.Background(), server.Client(),
		[]string{server.URL + "/broken", server.URL + "/good"}, target, 1<<20, nil)
	if err != nil {
		t.Fatalf("downloadReleaseFile() error = %v", err)
	}
	want := sha256.Sum256(payload)
	if !strings.EqualFold(digest, hex.EncodeToString(want[:])) {
		t.Fatalf("digest = %s", digest)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(payload) {
		t.Fatalf("落盘内容 = %q", content)
	}
	if attempts != 2 {
		t.Fatalf("尝试次数 = %d，应当是先坏后好两次", attempts)
	}
}

// 全都不通时要把最后一次的原因带出来，别吞成一句「更新失败」。
func TestDownloadReleaseFileReportsLastError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "package.tar.gz")
	_, err := downloadReleaseFile(context.Background(), server.Client(),
		[]string{server.URL + "/a", server.URL + "/b"}, target, 1<<20, nil)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v，应当带上 HTTP 状态", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("失败之后不该留下半截文件：%v", statErr)
	}
}

// 取消是用户的意思，不该被当成「这条线路不行」继续换下一条重试。
func TestDownloadReleaseFileStopsOnCancel(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	target := filepath.Join(t.TempDir(), "package.tar.gz")
	if _, err := downloadReleaseFile(ctx, server.Client(),
		[]string{server.URL + "/a", server.URL + "/b"}, target, 1<<20, nil); err == nil {
		t.Fatal("取消之后应当返回错误")
	}
	if attempts != 0 {
		t.Fatalf("取消之后仍然发起了 %d 次请求", attempts)
	}
}
