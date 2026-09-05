// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
	"github.com/gin-gonic/gin"
)

func isolateMediaCachePolicy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", dir)
	previous := assistant.CurrentMediaDownloadCachePolicy()
	t.Cleanup(func() { _ = assistant.ConfigureMediaDownloadCache(previous.RetentionDays, previous.MaxMB<<20) })
	return dir
}

func requestMediaCache(router http.Handler, method, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/api/system/media-cache", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func TestMediaCachePolicyHotApplyAndPersistence(t *testing.T) {
	dir := isolateMediaCachePolicy(t)
	dbPath := filepath.Join(t.TempDir(), "settings.db")
	store, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initial := assistant.MediaDownloadCachePolicy{RetentionDays: 30}
	h, err := NewMediaCacheHandler(context.Background(), store, initial)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	h.Register(router)
	w := requestMediaCache(router, http.MethodGet, "")
	var got assistant.MediaDownloadCachePolicy
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got != initial {
		t.Fatalf("initial policy: %s, %v", w.Body.String(), err)
	}
	objectDir := filepath.Join(dir, "download-cache", "objects", "aa", "object")
	if err := os.MkdirAll(objectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	object := filepath.Join(objectDir, "media.txt")
	writeOldObject := func() {
		t.Helper()
		if err := os.MkdirAll(objectDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(object, []byte("cached"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(object, old, old); err != nil {
			t.Fatal(err)
		}
	}
	writeOldObject()
	w = requestMediaCache(router, http.MethodPost, `{"retention_days":1,"max_mb":0}`)
	if w.Code != 200 || assistant.CurrentMediaDownloadCachePolicy().RetentionDays != 1 {
		t.Fatalf("policy not immediately applied: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(object); !os.IsNotExist(err) {
		t.Fatal("new retention not applied to existing cache")
	}
	writeOldObject()
	w = requestMediaCache(router, http.MethodPost, `{"retention_days":-1,"max_mb":0}`)
	if w.Code != 200 {
		t.Fatalf("save never-clean: %s", w.Body.String())
	}
	if _, err := os.Stat(object); err != nil {
		t.Fatal("never-clean deleted cache")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := NewMediaCacheHandler(context.Background(), reopened, assistant.MediaDownloadCachePolicy{RetentionDays: 3, MaxMB: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if restored.policy.RetentionDays != -1 || restored.policy.MaxMB != 0 || assistant.CurrentMediaDownloadCachePolicy() != restored.policy {
		t.Fatalf("restart/YAML overwrote saved policy: %+v", restored.policy)
	}
}

type failingMediaCacheStore struct{ MediaCachePolicyStore }

func (s failingMediaCacheStore) SaveMediaDownloadCachePolicy(context.Context, assistant.MediaDownloadCachePolicy) error {
	return errors.New("disk unavailable")
}

func TestMediaCachePolicyRejectsInvalidAndFailedSaves(t *testing.T) {
	isolateMediaCachePolicy(t)
	store, err := storage.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h, err := NewMediaCacheHandler(context.Background(), store, assistant.MediaDownloadCachePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	h.Register(router)
	initial := assistant.CurrentMediaDownloadCachePolicy()
	for _, body := range []string{`{}`, `{"retention_days":null,"max_mb":0}`, `{"retention_days":7}`, `{"retention_days":-2,"max_mb":0}`, `{"retention_days":36501,"max_mb":0}`, `{"retention_days":7,"max_mb":-1}`, `{"retention_days":7,"max_mb":1048577}`, `{"retention_days":1.5,"max_mb":0}`} {
		if w := requestMediaCache(router, http.MethodPost, body); w.Code != 400 {
			t.Errorf("accepted %s: %d", body, w.Code)
		}
	}
	h.store = failingMediaCacheStore{store}
	w := requestMediaCache(router, http.MethodPost, `{"retention_days":-1,"max_mb":0}`)
	if w.Code != 500 || h.policy != initial || assistant.CurrentMediaDownloadCachePolicy() != initial {
		t.Fatalf("failed save changed active policy: %d %s", w.Code, w.Body.String())
	}
	if _, found, err := store.LoadMediaDownloadCachePolicy(context.Background()); err != nil || found {
		t.Fatalf("failed save changed persistent policy: found=%v err=%v", found, err)
	}
}
