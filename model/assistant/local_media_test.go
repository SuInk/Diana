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

func TestLocalMediaStorePersistentShareStillExpires(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(t.TempDir(), "media.mp4")
	if err := os.WriteFile(file, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store := NewLocalMediaStore("http://localhost/media/resolver")
	store.now = func() time.Time { return now }
	if err := store.SetIndexDir(dir); err != nil {
		t.Fatal(err)
	}
	shared, ok := store.Share(file, time.Minute)
	if !ok {
		t.Fatal("share failed")
	}
	token := path.Base(shared)
	restarted := NewLocalMediaStore("http://localhost/media/resolver")
	restarted.now = func() time.Time { return now }
	if err := restarted.SetIndexDir(dir); err != nil {
		t.Fatal(err)
	}
	if got, ok := restarted.ResolveSharedPath(shared); !ok || got != file {
		t.Fatalf("restored path = %q, %v", got, ok)
	}
	live := httptest.NewRecorder()
	restarted.ServeToken(live, httptest.NewRequest(http.MethodGet, shared, nil), token)
	if live.Code != http.StatusOK || live.Body.String() != "video" {
		t.Fatalf("restored HTTP response = %d %q", live.Code, live.Body.String())
	}
	now = now.Add(2 * time.Minute)
	if _, ok := restarted.ResolveSharedPath(shared); ok {
		t.Fatal("expired share was restored")
	}
	recorder := httptest.NewRecorder()
	restarted.ServeToken(recorder, httptest.NewRequest(http.MethodGet, shared, nil), token)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expired HTTP status = %d", recorder.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, token+".json")); !os.IsNotExist(err) {
		t.Fatalf("expired index retained: %v", err)
	}
	if _, ok := restarted.ResolveSharedPath("http://localhost/media/resolver/..%2Fsecret"); ok {
		t.Fatal("accepted traversal token")
	}
}

func TestLocalMediaStorePersistentMissingAndExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(t.TempDir(), "media.mp4")
	if err := os.WriteFile(file, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store := NewLocalMediaStore("http://localhost/media/resolver")
	store.now = func() time.Time { return now }
	if err := store.SetIndexDir(dir); err != nil {
		t.Fatal(err)
	}
	shared, ok := store.Share(file, time.Minute)
	if !ok {
		t.Fatal("share failed")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	restarted := NewLocalMediaStore("http://localhost/media/resolver")
	if err := restarted.SetIndexDir(dir); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	restarted.ServeToken(rec, httptest.NewRequest(http.MethodGet, shared, nil), path.Base(shared))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing source status = %d", rec.Code)
	}
	restarted.now = func() time.Time { return now.Add(2 * time.Minute) }
	if err := restarted.SetIndexDir(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("expired indexes not cleaned: %v %v", entries, err)
	}
}

func TestLocalMediaStoreServesSharedFile(t *testing.T) {
	tempDir := t.TempDir()
	videoPath := filepath.Join(tempDir, "video.mp4")
	if err := os.WriteFile(videoPath, []byte("fake video"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := NewLocalMediaStore("http://127.0.0.1:18080/api/assistant/media")
	sharedURL, ok := store.Share(videoPath, time.Minute)
	if !ok {
		t.Fatal("Share() returned false")
	}
	parsed, err := url.Parse(sharedURL)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	token := path.Base(parsed.Path)
	req := httptest.NewRequest(http.MethodGet, "/api/assistant/media/"+token, nil)
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
	store := NewLocalMediaStore("http://127.0.0.1:18080/api/assistant/media")
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
	store := NewLocalMediaStore("http://127.0.0.1:18080/api/assistant/media")
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

	req := httptest.NewRequest(http.MethodGet, "/api/assistant/media/"+token, nil)
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
