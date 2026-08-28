// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestNormalizeOversizedLLMImageResizesInsteadOfRejecting(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	img.Set(0, 0, color.White)
	var source bytes.Buffer
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	// PNG decoders ignore trailing bytes. Padding gives the test a deterministic valid image
	// above the transport limit without spending time generating incompressible pixel noise.
	source.Write(make([]byte, maxLLMImageBase64Bytes-source.Len()+1))
	dataURL, err := normalizeLLMImageBytes(source.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") && !strings.HasPrefix(dataURL, "data:image/jpeg;base64,") {
		t.Fatalf("normalized image type = %q", dataURL[:min(len(dataURL), 64)])
	}
	decoded, err := decodeDataURLImage(dataURL)
	if err != nil {
		t.Fatal(err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != 2000 || bounds.Dy() != 1125 {
		t.Fatalf("normalized dimensions = %dx%d", bounds.Dx(), bounds.Dy())
	}
	_, encoded, _ := strings.Cut(dataURL, ",")
	if len(encoded) >= maxLLMImageBase64Bytes {
		t.Fatalf("normalized image still exceeds model limit")
	}
}

func TestNormalizeOversizedLLMImageIntegration(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("DIANA_LLM_IMAGE_NORMALIZE_INTEGRATION"))
	if path == "" {
		t.Skip("set DIANA_LLM_IMAGE_NORMALIZE_INTEGRATION to an image path")
	}
	dataURL, err := localImageAsDataURL(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeDataURLImage(dataURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("normalized %s to %dx%d, data URL bytes=%d", path, decoded.Bounds().Dx(), decoded.Bounds().Dy(), len(dataURL))
}

func TestLongImageExpandsToOverviewAndOrderedOverlappingTiles(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1200, 4500))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	parts, err := normalizeLLMImageParts(encoded.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 4 {
		t.Fatalf("parts=%d, want overview plus three tiles", len(parts))
	}
	for index, value := range parts[1:] {
		decoded, err := decodeDataURLImage(value)
		if err != nil {
			t.Fatalf("decode tile %d: %v", index, err)
		}
		if decoded.Bounds().Dx() != 1200 || decoded.Bounds().Dy() != 1800 {
			t.Fatalf("tile %d dimensions=%v", index, decoded.Bounds())
		}
	}

	message, complete := llmMessageFromEventWithImagesForContextDetailed(context.Background(), MessageEvent{
		Kind: EventKindPrivate,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"url": imageBytesAsDataURL(encoded.Bytes(), "image/png"),
		}}},
	}, "读取长图", nil)
	if !complete || len(message.Parts) != 5 {
		t.Fatalf("message complete=%v parts=%d", complete, len(message.Parts))
	}
	if !strings.Contains(message.Content, "完整总览") || !strings.Contains(message.Content, "相邻切片有重叠") {
		t.Fatalf("message is missing long-image guidance: %q", message.Content)
	}
	imageOnly, complete := llmMessageFromEventWithImagesForContextDetailed(context.Background(), MessageEvent{
		Kind: EventKindPrivate,
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{
			"url": imageBytesAsDataURL(encoded.Bytes(), "image/png"),
		}}},
	}, "", nil)
	if !complete || !strings.Contains(imageOnly.Content, "一张图片") || strings.Contains(imageOnly.Content, "4 张图片") || !strings.Contains(imageOnly.Content, "完整总览") {
		t.Fatalf("image-only long prompt = %q", imageOnly.Content)
	}
}

func TestExtremeLongImageTilesCoverCompleteLongEdge(t *testing.T) {
	tiles := longImageTileBounds(image.Rect(0, 0, 1000, 20000))
	if len(tiles) != maxLongImageTiles {
		t.Fatalf("tiles=%d, want capped %d", len(tiles), maxLongImageTiles)
	}
	if tiles[0].Min.Y != 0 || tiles[len(tiles)-1].Max.Y != 20000 {
		t.Fatalf("tiles do not cover both ends: first=%v last=%v", tiles[0], tiles[len(tiles)-1])
	}
	for index := 1; index < len(tiles); index++ {
		if tiles[index].Min.Y >= tiles[index-1].Max.Y {
			t.Fatalf("tiles %d and %d do not overlap: %v %v", index-1, index, tiles[index-1], tiles[index])
		}
	}
}

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
