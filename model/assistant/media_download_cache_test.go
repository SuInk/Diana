// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

func mediaTestMD5(body []byte) string {
	sum := md5.Sum(body)
	return hex.EncodeToString(sum[:])
}

func TestMediaDownloadConcurrentDedupAndRepair(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	body := []byte("shared document or video content")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	var wg sync.WaitGroup
	paths := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path, mime, release, err := acquireMediaDownload(context.Background(), server.Client(), fmt.Sprintf("%s/?token=%d", server.URL, i), "file.txt", mediaTestMD5(body), "adapter", 1024)
			defer release()
			if err != nil || mime != "application/octet-stream" {
				t.Errorf("download: mime=%q err=%v", mime, err)
				return
			}
			paths <- path
		}(i)
	}
	wg.Wait()
	close(paths)
	first := ""
	for path := range paths {
		if first != "" && first != path {
			t.Fatalf("different objects: %s / %s", first, path)
		}
		first = path
	}
	if first == "" || hits.Load() != 1 {
		t.Fatalf("downloads=%d path=%q", hits.Load(), first)
	}
	if err := os.WriteFile(first, bytes.Repeat([]byte("x"), len(body)), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _, release, err := acquireMediaDownload(context.Background(), server.Client(), server.URL+"/repair", "other.pdf", mediaTestMD5(body), "adapter", 1024)
	defer release()
	if err != nil || path != first || hits.Load() != 2 {
		t.Fatalf("repair path=%q err=%v hits=%d", path, err, hits.Load())
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("corrupt repair: %v", err)
	}
}

func TestMediaDownloadURLAndContentDedup(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "same content")
	}))
	defer server.Close()
	first := ""
	for _, suffix := range []string{"/a", "/a", "/b"} {
		path, _, release, err := acquireMediaDownload(context.Background(), server.Client(), server.URL+suffix, "file.txt", "", "adapter", 1024)
		if err != nil {
			release()
			t.Fatal(err)
		}
		if first != "" && path != first {
			t.Error("identical content stored twice")
		}
		first = path
		release()
	}
	dir, _ := mediaDownloadCacheDir()
	if hits.Load() != 2 || countMediaObjects(t, dir) != 1 {
		t.Fatalf("hits=%d", hits.Load())
	}
}

func TestMediaDownloadWrongMD5AndTrustIsolation(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, r.URL.Path)
	}))
	defer server.Close()
	for _, suffix := range []string{"/a", "/b"} {
		path, _, release, err := acquireMediaDownload(context.Background(), server.Client(), server.URL+suffix, "file.txt", strings.Repeat("0", 32), "adapter", 1024)
		if err != nil {
			release()
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		release()
		if string(got) != suffix {
			t.Fatalf("wrong identity reused: %q", got)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("MD5 fallback redownloaded: %d", hits.Load())
	}
	denied := errors.New("public access denied")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, denied })}
	_, _, release, err := acquireMediaDownload(context.Background(), client, server.URL+"/a", "file.txt", "", "public", 1024)
	release()
	if !errors.Is(err, denied) {
		t.Fatalf("trusted source bypassed public policy: %v", err)
	}
}

func TestMediaDownloadLimitsAndFailureCleanup(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/chunked" {
			w.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(w, strings.Repeat("x", 128))
	}))
	defer server.Close()
	for _, suffix := range []string{"/length", "/chunked"} {
		_, _, release, err := acquireMediaDownload(context.Background(), server.Client(), server.URL+suffix, "file.txt", "", "adapter", 64)
		release()
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("size limit not enforced: %v", err)
		}
	}
	path, _, release, err := acquireMediaDownload(context.Background(), server.Client(), server.URL+"/length", "file.txt", "", "adapter", 256)
	release()
	if err != nil || path == "" {
		t.Fatalf("failed fetch was cached: %v", err)
	}
	_, _, release, err = acquireMediaDownload(context.Background(), server.Client(), server.URL+"/length", "file.txt", "", "adapter", 64)
	release()
	if err == nil {
		t.Fatal("cache bypassed reduced size limit")
	}
	dir, _ := mediaDownloadCacheDir()
	partials, _ := filepath.Glob(filepath.Join(dir, ".partial-*"))
	if len(partials) != 0 {
		t.Fatalf("partial downloads leaked: %v", partials)
	}
}

func TestMediaDownloadLeaseAndEviction(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	dir, _ := mediaDownloadCacheDir()
	release := holdMediaDownloadCache(dir)
	path, err := persistMediaContent(dir, []byte("active"), "text/plain", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-mediaDownloadCacheMaxAge - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	otherRelease := holdMediaDownloadCache(dir)
	otherRelease()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("active reader evicted")
	}
	release()
	release() // Cleanup functions can be called more than once.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expired file retained after last reader")
	}
	path, err = persistMediaContent(dir, []byte("over budget"), "text/plain", "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	pruneMediaDownloadCache(dir, 1, mediaDownloadCacheMaxAge, time.Now())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cache capacity not enforced")
	}
}

func TestMediaDownloadConfigurableRetention(t *testing.T) {
	mediaDownloadUsers.Lock()
	previousAge, previousBytes := mediaDownloadUsers.maxAge, mediaDownloadUsers.maxBytes
	mediaDownloadUsers.Unlock()
	t.Cleanup(func() {
		mediaDownloadUsers.Lock()
		mediaDownloadUsers.maxAge, mediaDownloadUsers.maxBytes = previousAge, previousBytes
		mediaDownloadUsers.Unlock()
	})
	for _, tc := range []struct {
		name        string
		days        int
		maxBytes    int64
		wantRemoved bool
	}{
		{"default_seven_days_no_cap", 0, 0, false},
		{"expire_after_one_day", 1, 0, true},
		{"retain_thirty_days", 30, 0, false},
		{"disable_expiry", -1, 0, false},
		{"optional_capacity", -1, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
			if err := ConfigureMediaDownloadCache(tc.days, tc.maxBytes); err != nil {
				t.Fatal(err)
			}
			dir, _ := mediaDownloadCacheDir()
			path, err := persistMediaContent(dir, []byte("cached bytes"), "text/plain", "file.txt")
			if err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-2 * 24 * time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			release := holdMediaDownloadCache(dir)
			if err := CleanupMediaDownloadCache(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Error("scheduled cleanup removed an active file")
			}
			release()
			_, err = os.Stat(path)
			if os.IsNotExist(err) != tc.wantRemoved {
				t.Fatalf("removed=%v, want %v", os.IsNotExist(err), tc.wantRemoved)
			}
		})
	}
	for _, tc := range []struct {
		days     int
		maxBytes int64
	}{{-2, 0}, {36501, 0}, {7, -1}} {
		if err := ConfigureMediaDownloadCache(tc.days, tc.maxBytes); err == nil {
			t.Errorf("invalid policy accepted: %+v", tc)
		}
	}
}

func TestMediaDownloadTimeOnlyPolicyHasNoSizeCap(t *testing.T) {
	dir := t.TempDir()
	path, err := persistMediaContent(dir, []byte("file"), "text/plain", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	// Sparse file exercises the former 1 GiB threshold without allocating it.
	if err := os.Truncate(path, (1<<30)+1); err != nil {
		t.Fatal(err)
	}
	pruneMediaDownloadCache(dir, 0, mediaDownloadCacheMaxAge, time.Now())
	if _, err := os.Stat(path); err != nil {
		t.Fatal("time-only retention still imposed a capacity limit")
	}
}

func TestMediaDownloadNeverCleanPreservesOldFiles(t *testing.T) {
	dir := t.TempDir()
	path, err := persistMediaContent(dir, []byte("keep forever"), "text/plain", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(dir, ".partial-abandoned")
	if err := os.WriteFile(partial, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(-2, 0, 0)
	for _, file := range []string{path, partial} {
		if err := os.Chtimes(file, old, old); err != nil {
			t.Fatal(err)
		}
	}
	pruneMediaDownloadCache(dir, 0, 0, time.Now())
	for _, file := range []string{path, partial} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("never-clean policy deleted %s: %v", file, err)
		}
	}
}

func TestOneBotFileDownloadUsesExplicitMD5(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	body := []byte("document contents")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	for i := 0; i < 2; i++ {
		event := MessageEvent{Platform: PlatformOneBotV11, Segments: []MessageSegment{{Type: "file", Data: map[string]string{
			"name": "document.txt", "url": fmt.Sprintf("%s/?token=%d", server.URL, i), "file_md5": mediaTestMD5(body),
		}}}}
		plugin := NewFileParserPlugin(server.Client())
		response, err := plugin.Handle(context.Background(), PluginRequest{Event: event})
		if err != nil || response == nil || !strings.Contains(response.Context, string(body)) {
			t.Fatalf("parse: %v, response=%+v", err, response)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("file redownloaded across messages: %d", hits.Load())
	}
	ref := fileRefFromOneBotData(fileRef{}, map[string]any{"fileMd5": strings.ToUpper(mediaTestMD5(body)), "url": server.URL + "/document.txt"})
	if ref.MD5 != mediaTestMD5(body) {
		t.Fatal("adapter response lost explicit digest")
	}
}

func TestOneBotVideoDownloadUsesExplicitMD5(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	body := []byte("video content")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	for i, kind := range []string{"video", "file"} {
		source := fmt.Sprintf("%s/video.mp4?token=%d", server.URL, i)
		segments := []MessageSegment{{Type: kind, Data: map[string]string{"url": source, "name": "video.mp4", "md5": mediaTestMD5(body)}}}
		ctx := withVideoMediaIdentities(context.Background(), PlatformOneBotV11, segments)
		path, release, err := materializeVideoContextSource(ctx, source, 0)
		if err != nil {
			release()
			t.Fatal(err)
		}
		release()
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("video cleanup removed shared object: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("video/file-segment redownloaded: %d", hits.Load())
	}
}

func TestMediaDownloadCanceledWaiterDoesNotCancelReader(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	started := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	unblockServer := func() { once.Do(func() { close(unblock) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-unblock
		_, _ = io.WriteString(w, "body")
	}))
	defer server.Close()
	defer unblockServer()
	finished := make(chan error, 1)
	go func() {
		_, _, release, err := acquireMediaDownload(context.Background(), server.Client(), server.URL, "file.txt", "", "adapter", 1024)
		release()
		finished <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, release, err := acquireMediaDownload(ctx, server.Client(), server.URL, "file.txt", "", "adapter", 1024)
	release()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiting cancellation: %v", err)
	}
	unblockServer()
	if err := <-finished; err != nil {
		t.Fatalf("waiting cancellation affected original reader: %v", err)
	}
}

func TestOneBotVideoHistoryAndQuotedFramesShareDownload(t *testing.T) {
	t.Setenv("DIANA_HISTORY_MEDIA_DIR", t.TempDir())
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	body, err := os.ReadFile(createTimelineVideo(t))
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	segment := MessageSegment{Type: "file", Data: map[string]string{
		"name": "video.mp4", "url": server.URL + "/video.mp4?token=first", "md5": mediaTestMD5(body),
	}}
	segments := cacheVideoFrames(context.Background(), PlatformOneBotV11, 1, "group", "1", "2", "3", []MessageSegment{segment})
	if !hasCachedVideoFrames(segments) {
		t.Fatal("file segment produced no history frames")
	}
	segment.Data["url"] = server.URL + "/video.mp4?token=changed"
	event := MessageEvent{Platform: PlatformOneBotV11, Quoted: &QuotedMessage{Segments: []MessageSegment{segment}}}
	_, failures := llmMessageFromEventWithVideoFramesDiagnostics(context.Background(), event, "video", nil)
	if len(failures) != 0 || hits.Load() != 1 {
		t.Fatalf("quoted frame extraction did not reuse history download: failures=%v hits=%d", failures, hits.Load())
	}
}
