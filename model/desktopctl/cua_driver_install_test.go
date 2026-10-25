// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func cuaTestArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
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
	return buf.Bytes()
}

func sha(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func testCUAInstaller(t *testing.T, archive []byte, binary string, mirror func(context.Context, string) string) (*CUADriverInstaller, *atomic.Int32, *httptest.Server) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	if mirror == nil {
		mirror = func(context.Context, string) string { return server.URL + "/asset.tar.gz" }
	}
	i := newCUADriverInstaller(t.TempDir(), "darwin", server.Client(), mirror)
	i.artifact = &cuaDriverArtifact{asset: "asset.tar.gz", archiveSHA256: sha(archive), binarySHA256: sha([]byte(binary))}
	return i, &hits, server
}

func waitInstall(t *testing.T, i *CUADriverInstaller) error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if running, err := i.Status(); !running {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("install did not finish")
	return nil
}

// 只解出 cua-driver 本体，压缩包和本体都要过 SHA-256；装好后可执行。
func TestCUADriverInstallerExtractsVerifiedBinary(t *testing.T) {
	binary := "#!/bin/sh\necho driver\n"
	archive := cuaTestArchive(t, map[string]string{"pkg/cua-driver": binary, "pkg/libcua_driver_sdk.dylib": "sdk"})
	i, _, _ := testCUAInstaller(t, archive, binary, nil)
	if i.Installed() != "" {
		t.Fatal("nothing should be installed yet")
	}
	done := make(chan struct{})
	i.Start(context.Background(), func() { close(done) })
	if err := waitInstall(t, i); err != nil {
		t.Fatal(err)
	}
	<-done
	path := i.Installed()
	if path == "" || !strings.HasSuffix(path, filepath.Join(CUADriverVersion, "cua-driver")) {
		t.Fatalf("installed = %q", path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("binary not executable: %v %v", info, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("only the driver should be extracted: %v", entries)
	}
	// 本体被替换后不再被当成已安装，也就不会被运行。
	if err := os.WriteFile(path, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if i.Installed() != "" {
		t.Fatal("a tampered binary must not be reported as installed")
	}
}

// 镜像或中间人给的包对不上锁定的哈希就丢弃，五分钟内不重复下载。
func TestCUADriverInstallerRejectsTamperedArchiveAndBacksOff(t *testing.T) {
	binary := "driver"
	good := cuaTestArchive(t, map[string]string{"cua-driver": binary})
	i, hits, _ := testCUAInstaller(t, cuaTestArchive(t, map[string]string{"cua-driver": "evil"}), binary, nil)
	i.artifact.archiveSHA256 = sha(good)
	i.Start(context.Background(), nil)
	err := waitInstall(t, i)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered archive accepted: %v", err)
	}
	if i.Installed() != "" {
		t.Fatal("nothing should be installed")
	}
	i.Start(context.Background(), nil)
	if running, _ := i.Status(); running || hits.Load() != 1 {
		t.Fatalf("retried too soon: running=%v hits=%d", running, hits.Load())
	}
}

// 压缩包哈希对得上、但里面的本体对不上（发布资产被替换）同样拒绝。
func TestCUADriverInstallerRejectsUnexpectedBinary(t *testing.T) {
	archive := cuaTestArchive(t, map[string]string{"cua-driver": "unexpected"})
	i, _, _ := testCUAInstaller(t, archive, "expected", nil)
	i.Start(context.Background(), nil)
	if err := waitInstall(t, i); err == nil || !strings.Contains(err.Error(), "可执行文件校验失败") {
		t.Fatalf("unexpected binary accepted: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(i.binaryPath())); len(entries) != 0 {
		t.Fatalf("rejected binary left on disk: %v", entries)
	}
}

func TestCUADriverInstallerUnsupportedPlatform(t *testing.T) {
	if newCUADriverInstaller(t.TempDir(), "plan9", nil, nil) != nil {
		t.Fatal("unverified platforms should have no installer")
	}
	var missing *CUADriverInstaller
	if missing.Installed() != "" {
		t.Fatal("nil installer reports nothing installed")
	}
	missing.Start(context.Background(), nil)
}
