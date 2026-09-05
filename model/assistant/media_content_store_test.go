// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func countMediaObjects(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(filepath.Join(root, "objects"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".partial-") {
				t.Errorf("partial media file left behind: %s", path)
			}
			count++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return count
}

func TestMediaContentDeduplicatesURLsAndGeneratedImages(t *testing.T) {
	body := pngBytes(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	store := mediaStore(t)
	first, err := store.Fetch(context.Background(), server.URL+"/a?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Fetch(context.Background(), server.URL+"/different")
	if err != nil || first != second {
		t.Fatalf("URL aliases: %q %q %v", first, second, err)
	}
	generated, err := store.StoreImage(body, "image/jpeg")
	if err != nil || generated != first || countMediaObjects(t, store.Dir()) != 1 {
		t.Fatalf("generated alias: %q %v", generated, err)
	}
	if hits.Load() != 2 {
		t.Fatalf("unknown URLs need one initial fetch each, got %d", hits.Load())
	}
	reopened := NewMediaStore(store.Dir())
	path, err := reopened.Fetch(context.Background(), server.URL+"/a?token=secret")
	if err != nil || path != first || hits.Load() != 2 {
		t.Fatalf("persistent index: path=%q requests=%d err=%v", path, hits.Load(), err)
	}
	index, err := os.ReadFile(mediaSourceIndexPath(store.Dir(), "image:"+server.URL+"/a?token=secret"))
	if err != nil || bytes.Contains(index, []byte("secret")) || bytes.Contains(index, []byte(server.URL)) {
		t.Fatalf("source index leaked URL credentials: %v", err)
	}
	for _, corrupt := range []bool{false, true} {
		if corrupt {
			err = os.WriteFile(first, bytes.Repeat([]byte("x"), len(body)), 0o600)
		} else {
			err = os.Remove(first)
		}
		if err != nil {
			t.Fatal(err)
		}
		path, err := reopened.Fetch(context.Background(), server.URL+"/a?token=secret")
		if err != nil || path != first {
			t.Fatalf("cache repair: path=%q err=%v", path, err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("invalid repaired content: %v", err)
		}
	}
	if hits.Load() != 4 {
		t.Fatalf("missing/corrupt files must be downloaded again: %d", hits.Load())
	}
}

func TestMediaContentConcurrentWritesShareOneFile(t *testing.T) {
	root := t.TempDir()
	const workers = 20
	paths := make(chan string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path, err := persistMediaContent(root, []byte("identical file"), "", fmt.Sprintf("name-%d.ext%d", i, i))
			if err != nil {
				t.Error(err)
			}
			paths <- path
		}(i)
	}
	wg.Wait()
	close(paths)
	first := ""
	for path := range paths {
		if first == "" {
			first = path
		}
		if path != first {
			t.Errorf("same bytes have different paths: %q %q", path, first)
		}
	}
	if countMediaObjects(t, root) != 1 {
		t.Fatal("duplicate physical content files")
	}
	other, err := persistMediaContent(root, []byte("different file"), "", "same-name.pdf")
	if err != nil || other == first || countMediaObjects(t, root) != 2 {
		t.Fatalf("different bytes were merged: %q %v", other, err)
	}
}

func TestMediaContentWaitingFetchCanCancel(t *testing.T) {
	root := t.TempDir()
	started, finish := make(chan struct{}), make(chan struct{})
	var hits atomic.Int32
	loader := func(ctx context.Context) ([]byte, string, string, error) {
		hits.Add(1)
		close(started)
		<-finish
		return []byte("file"), "", "file.txt", nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := fetchMediaContent(context.Background(), root, "source", 1024, loader)
		done <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := fetchMediaContent(ctx, root, "source", 1024, loader)
	close(finish)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiting caller ignored cancellation: %v", err)
	}
	if err := <-done; err != nil || hits.Load() != 1 {
		t.Fatalf("leader: requests=%d err=%v", hits.Load(), err)
	}
}

func TestHistoryRemoteImageDownloadIsSharedAcrossMessages(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", root)
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	body := pngBytes(t)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	var wg sync.WaitGroup
	paths := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event := cacheMessageEventImages(context.Background(), MessageEvent{
				Platform: PlatformOneBotV11, MessageID: fmt.Sprint(i), GroupID: fmt.Sprint(i),
				Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": server.URL + "/same"}}},
			})
			paths <- event.Segments[0].Data["cached_file"]
		}(i)
	}
	wg.Wait()
	close(paths)
	var first string
	for path := range paths {
		if first == "" {
			first = path
		}
		if path == "" || path != first {
			t.Errorf("history did not share content: %q %q", first, path)
		}
	}
	if hits.Load() != 1 || countMediaObjects(t, root) != 1 {
		t.Fatalf("requests=%d, objects=%d", hits.Load(), countMediaObjects(t, root))
	}
}

func TestMediaContentFailedDownloadCanRetry(t *testing.T) {
	root := t.TempDir()
	var hits int
	loader := func(context.Context) ([]byte, string, string, error) {
		hits++
		if hits == 1 {
			return nil, "", "", fmt.Errorf("temporary network failure")
		}
		return []byte("valid"), "", "file.txt", nil
	}
	if _, err := fetchMediaContent(context.Background(), root, "source", 1024, loader); err == nil {
		t.Fatal("failed download was accepted")
	}
	if _, err := fetchMediaContent(context.Background(), root, "source", 1024, loader); err != nil || hits != 2 {
		t.Fatalf("retry failed: %d %v", hits, err)
	}
}

func TestMediaContentRepairsOriginalPathWhenNameChanges(t *testing.T) {
	root := t.TempDir()
	body := []byte("payload")
	first, err := persistMediaContent(root, body, "", "original.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := persistMediaContent(root, body, "", "renamed.txt")
	if err != nil || path != first || countMediaObjects(t, root) != 1 {
		t.Fatalf("repair changed content identity: %q %q %v", first, path, err)
	}
}

func TestMediaSourceIndexIsBoundedAndDoesNotDeleteObjects(t *testing.T) {
	root := t.TempDir()
	path, err := persistMediaContent(root, []byte("keep"), "", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4097; i++ {
		if err := writeAtomicMediaFile(mediaSourceIndexPath(root, fmt.Sprint(i)), []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	trimMediaSourceIndex(root)
	entries, err := os.ReadDir(filepath.Join(root, ".sources"))
	if err != nil || len(entries) != 4096 {
		t.Fatalf("source index count=%d err=%v", len(entries), err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatalf("source index eviction deleted content: %v", err)
	}
}

func TestMediaSourceRejectsInvalidReferenceAndReducedLimit(t *testing.T) {
	root := t.TempDir()
	path, err := fetchMediaContent(context.Background(), root, "key", 1024, func(context.Context) ([]byte, string, string, error) {
		return []byte("payload"), "", "file.txt", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cachedMediaSource(root, "key", 3) != "" {
		t.Fatal("cached file bypassed reduced size limit")
	}
	for _, relative := range []string{"../outside", filepath.ToSlash(path), "objects/aa/../media.txt"} {
		record, err := json.Marshal(mediaSourceRecord{File: relative, Size: 7})
		if err != nil {
			t.Fatal(err)
		}
		if err := writeAtomicMediaFile(mediaSourceIndexPath(root, "invalid"), record); err != nil {
			t.Fatal(err)
		}
		if cachedMediaSource(root, "invalid", 1024) != "" {
			t.Fatalf("invalid reference accepted: %q", relative)
		}
	}
}
