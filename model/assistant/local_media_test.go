// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalMediaStoreServesSharedFile(t *testing.T) {
	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "video.mp4")
	if err := os.WriteFile(videoPath, []byte("fake video"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := NewLocalMediaStore("http://127.0.0.1:18080/api/qqbot/media")
	sharedURL, ok := store.Share(videoPath, time.Minute)
	if !ok {
		t.Fatal("Share() returned false")
	}
	parsed, err := url.Parse(sharedURL)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	token := path.Base(parsed.Path)
	req := httptest.NewRequest(http.MethodGet, "/api/qqbot/media/"+token, nil)
	rec := httptest.NewRecorder()
	store.ServeToken(rec, req, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rec.Code, rec.Body.String())
	}
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "fake video" {
		t.Fatalf("body = %q", body)
	}
}

func TestLocalMediaStoreResolvesSharedPath(t *testing.T) {
	tempDir := t.TempDir()
	imagePath := filepath.Join(tempDir, "generated.jpg")
	if err := os.WriteFile(imagePath, []byte("image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewLocalMediaStore("http://127.0.0.1:18080/api/qqbot/media")
	sharedURL, ok := store.Share(imagePath, time.Minute)
	if !ok {
		t.Fatal("Share() returned false")
	}
	resolved, ok := store.ResolveSharedPath(sharedURL)
	if !ok || resolved != imagePath {
		t.Fatalf("ResolveSharedPath() = %q, %v, want %q, true", resolved, ok, imagePath)
	}
	if resolved, ok := store.ResolveSharedPath(strings.Replace(sharedURL, "/media/", "/media-other/", 1)); ok || resolved != "" {
		t.Fatalf("ResolveSharedPath() accepted unrelated URL: %q, %v", resolved, ok)
	}
}

func TestLocalMediaStoreExpiresSharedFile(t *testing.T) {
	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "video.mp4")
	if err := os.WriteFile(videoPath, []byte("fake video"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	now := time.Now()
	store := NewLocalMediaStore("http://127.0.0.1:18080/api/qqbot/media")
	store.now = func() time.Time { return now }
	sharedURL, ok := store.Share(videoPath, time.Second)
	if !ok {
		t.Fatal("Share() returned false")
	}
	parsed, err := url.Parse(sharedURL)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	token := path.Base(parsed.Path)
	now = now.Add(2 * time.Second)

	req := httptest.NewRequest(http.MethodGet, "/api/qqbot/media/"+token, nil)
	rec := httptest.NewRecorder()
	store.ServeToken(rec, req, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestLocalMediaStoreShareUsesConnectionOrigin(t *testing.T) {
	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "video.mp4")
	if err := os.WriteFile(videoPath, []byte("fake video"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := NewLocalMediaStore("http://127.0.0.1:18080/media/resolver")
	origin := "http://host.docker.internal:18080"
	store.SetOriginProvider(func() string { return origin })

	sharedURL, ok := store.Share(videoPath, time.Minute)
	if !ok {
		t.Fatal("Share() returned false")
	}
	if !strings.HasPrefix(sharedURL, "http://host.docker.internal:18080/media/resolver/") {
		t.Fatalf("Share() = %q, want host.docker.internal origin", sharedURL)
	}
	// 桥掉线（provider 返回空）时退回静态基址。
	origin = ""
	fallbackURL, ok := store.Share(videoPath, time.Minute)
	if !ok {
		t.Fatal("Share() returned false with empty origin")
	}
	if !strings.HasPrefix(fallbackURL, "http://127.0.0.1:18080/media/resolver/") {
		t.Fatalf("Share() = %q, want static base fallback", fallbackURL)
	}
}

func TestLocalMediaStoreResolvesSharedPathAcrossHosts(t *testing.T) {
	tempDir := t.TempDir()
	imagePath := filepath.Join(tempDir, "generated.jpg")
	if err := os.WriteFile(imagePath, []byte("image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewLocalMediaStore("http://127.0.0.1:18080/media/resolver")
	store.SetOriginProvider(func() string { return "https://bridge.example:8443" })
	sharedURL, ok := store.Share(imagePath, time.Minute)
	if !ok {
		t.Fatal("Share() returned false")
	}
	// 分享时的主机名与静态基址不同，仍应按路径 + token 解析回本地文件。
	resolved, ok := store.ResolveSharedPath(sharedURL)
	if !ok || resolved != imagePath {
		t.Fatalf("ResolveSharedPath() = %q, %v, want %q, true", resolved, ok, imagePath)
	}
	if _, ok := store.ResolveSharedPath("ftp://bridge.example/media/resolver/token"); ok {
		t.Fatal("ResolveSharedPath() accepted non-http scheme")
	}
}
