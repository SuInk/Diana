// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestLLMReadyImageURLsLoadsConcurrentlyAndPreservesOrder(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	imageBodies := map[string][]byte{
		"/first.png":  {0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x01},
		"/second.png": {0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x02},
		"/third.png":  {0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x03},
	}
	started := make(chan string, len(imageBodies))
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		started <- req.URL.Path
		<-release
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageBodies[req.URL.Path])
	}))
	defer server.Close()

	paths := []string{"/first.png", "/second.png", "/third.png"}
	result := make(chan []string, 1)
	go func() {
		urls := make([]string, 0, len(paths))
		for _, path := range paths {
			urls = append(urls, server.URL+path)
		}
		result <- llmReadyImageURLs(context.Background(), urls)
	}()

	for range paths {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			releaseAll()
			t.Fatal("image loads did not overlap")
		}
	}
	releaseAll()

	var got []string
	select {
	case got = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("image loads did not finish")
	}
	want := make([]string, 0, len(paths))
	for _, path := range paths {
		want = append(want, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(imageBodies[path]))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ready images = %#v, want %#v", got, want)
	}
}

func TestLoadLLMImageURLsPreservesDuplicateMessages(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	body := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x01}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	got, complete := loadLLMImageURLs(context.Background(), []string{server.URL + "/first.png", server.URL + "/same.png"})
	if !complete || len(got) != 2 || got[0] != got[1] {
		t.Fatalf("ready images = %#v complete = %v", got, complete)
	}
	if deduped := llmReadyImageURLs(context.Background(), []string{server.URL + "/first.png", server.URL + "/same.png"}); len(deduped) != 1 {
		t.Fatalf("ordinary ready images = %#v, want one deduplicated image", deduped)
	}
}

func TestLoadLLMImageURLsRejectsInvalidSource(t *testing.T) {
	got, complete := loadLLMImageURLs(context.Background(), []string{"not-an-image-source"})
	if complete || len(got) != 0 {
		t.Fatalf("ready images = %#v complete = %v", got, complete)
	}
}

func TestLLMMessageDetailedRejectsPartialImageBatch(t *testing.T) {
	t.Setenv("DIANA_ALLOW_PRIVATE_HTTP_FETCHES", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "expired", http.StatusBadRequest)
	}))
	defer server.Close()
	message, complete := llmMessageFromEventWithImagesForContextDetailed(context.Background(), MessageEvent{
		Kind: EventKindPrivate,
		Segments: []MessageSegment{
			{Type: "image", Data: map[string]string{"url": "data:image/png;base64,YQ=="}},
			{Type: "image", Data: map[string]string{"url": server.URL + "/expired.jpg"}},
		},
	}, "逐张读取", nil)
	if complete {
		t.Fatal("partial image batch was reported complete")
	}
	imageCount := 0
	for _, part := range message.Parts {
		if part.Type == llm.ContentPartImageURL {
			imageCount++
		}
	}
	if imageCount != 1 {
		t.Fatalf("loaded image count = %d, want diagnostic partial result", imageCount)
	}
}
