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
