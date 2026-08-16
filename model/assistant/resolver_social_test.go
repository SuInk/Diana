// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestResolverAttachDownloadedVideoReturnsMedia(t *testing.T) {
	plugin := NewResolverPlugin(nil)
	plugin.mediaDownloader = func(context.Context, string) string {
		return "/tmp/diana-test-video.mp4"
	}
	result := plugin.attachDownloadedVideo(
		context.Background(),
		PluginRequest{},
		"https://youtu.be/example",
		"YouTube",
		resolverSocialResult{Handled: true, Context: "video"},
	)
	if len(result.VideoURLs) != 1 || result.VideoURLs[0] != "/tmp/diana-test-video.mp4" {
		t.Fatalf("VideoURLs = %#v", result.VideoURLs)
	}
}

func TestResolverSocialForwardMessagesBuildsMergedVideoCard(t *testing.T) {
	nodes := resolverSocialForwardMessages(resolverSocialResult{
		Handled:   true,
		Context:   "[Bilibili] 示例视频",
		ImageURLs: []string{"https://example.com/cover.jpg"},
		VideoURLs: []string{"/tmp/diana-test-video.mp4"},
	})
	if len(nodes) != 2 {
		t.Fatalf("nodes = %#v", nodes)
	}
	if nodes[0].Text != "[Bilibili] 示例视频" || !nodes[0].ImagesFirst || len(nodes[0].ImageURLs) != 1 {
		t.Fatalf("metadata node = %#v", nodes[0])
	}
	if len(nodes[1].VideoURLs) != 1 || nodes[1].VideoURLs[0] != "/tmp/diana-test-video.mp4" {
		t.Fatalf("video node = %#v", nodes[1])
	}
}

func TestResolverSocialForwardMessagesBuildsGalleryAndTextResults(t *testing.T) {
	gallery := resolverSocialForwardMessages(resolverSocialResult{
		Handled:   true,
		Context:   "[小红书] 示例图集",
		ImageURLs: []string{"https://example.com/1.jpg", "https://example.com/2.jpg"},
	})
	if len(gallery) != 3 || gallery[0].Text == "" || len(gallery[1].ImageURLs) != 1 || len(gallery[2].ImageURLs) != 1 {
		t.Fatalf("gallery nodes = %#v", gallery)
	}
	textOnly := resolverSocialForwardMessages(resolverSocialResult{Handled: true, Context: "媒体下载失败"})
	if len(textOnly) != 1 || textOnly[0].Text != "媒体下载失败" {
		t.Fatalf("text nodes = %#v", textOnly)
	}
}

func TestResolverPluginCurrentMediaPathAlwaysBuildsMergedForward(t *testing.T) {
	t.Setenv("DIANA_DOUYIN_CK", "")
	t.Setenv("DOUYIN_CK", "")
	plugin := NewResolverPlugin(nil)
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text: "https://www.douyin.com/video/1234567890",
		Settings: SettingValues{
			resolverSettingDownloadMedia: true,
		},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled || !resp.Forward || len(resp.ForwardMessages) != 1 {
		t.Fatalf("response = %#v", resp)
	}
	if !strings.Contains(resp.ForwardMessages[0].Text, "抖音 Cookie") {
		t.Fatalf("forward message = %#v", resp.ForwardMessages[0])
	}
}

func TestResolverDefaultVideoLimitsAreFifteenMinutesAndTwoHundredMB(t *testing.T) {
	t.Setenv("DIANA_RESOLVER_VIDEO_MAX_MB", "")
	t.Setenv("DIANA_RESOLVER_VIDEO_MAX_DURATION", "")
	if got := resolverVideoMaxMB(context.Background()); got != 200 {
		t.Fatalf("max MB = %d", got)
	}
	if got := resolverVideoMaxDuration(context.Background()); got != 15*60 {
		t.Fatalf("max duration = %d", got)
	}
}

func TestResolverMediaLimitsOverrideDefaults(t *testing.T) {
	ctx := withResolverMediaLimits(context.Background(), 42, 90, 720)
	if got := resolverVideoMaxMB(ctx); got != 42 {
		t.Fatalf("max MB = %d", got)
	}
	if got := resolverVideoMaxDuration(ctx); got != 90 {
		t.Fatalf("max duration = %d", got)
	}
	if got := resolverVideoMaxBytes(ctx); got != 42*1024*1024 {
		t.Fatalf("max bytes = %d", got)
	}
}

func TestXiaohongshuMediaImagesAreDeduplicated(t *testing.T) {
	note := map[string]any{
		"imageList": []any{
			map[string]any{"urlDefault": "https://example.com/1.jpg"},
			map[string]any{"urlDefault": "https://example.com/1.jpg"},
			map[string]any{"urlPre": "https://example.com/2.jpg"},
		},
	}
	got := xiaohongshuMediaImageURLs(note)
	if len(got) != 2 {
		t.Fatalf("images = %#v", got)
	}
}

func TestXiaohongshuSocialContextPreservesFullDescription(t *testing.T) {
	description := strings.Repeat("完整正文", 100)
	contextText := xiaohongshuSocialContext(map[string]any{
		"title": "测试标题",
		"desc":  description,
		"user":  map[string]any{"nickname": "测试作者"},
	})
	if !strings.Contains(contextText, description) {
		t.Fatalf("full description was truncated: %q", contextText)
	}
	if strings.HasSuffix(contextText, "...") {
		t.Fatalf("context has a truncation marker: %q", contextText)
	}
}

type fixedMediaSharer struct {
	path string
	ttl  time.Duration
}

func (s *fixedMediaSharer) Share(path string, ttl time.Duration) (string, bool) {
	s.path = path
	s.ttl = ttl
	return "http://diana:18080/media/resolver/token", true
}

func TestRuntimeSendsPluginMediaThroughSharer(t *testing.T) {
	withFastSendTiming(t)
	channel := &scriptedChannel{}
	sharer := &fixedMediaSharer{}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetLocalMediaSharer(sharer)

	err := runtime.sendPluginResponse(context.Background(), MessageEvent{
		Kind:   EventKindPrivate,
		UserID: "10001",
	}, PluginResponse{
		Reply:     "解析完成",
		ImageURLs: []string{"https://example.com/cover.jpg"},
		VideoURLs: []string{"/tmp/diana-test-video.mp4"},
	})
	if err != nil {
		t.Fatalf("sendPluginResponse() error = %v", err)
	}
	if sharer.path != "/tmp/diana-test-video.mp4" || sharer.ttl != resolverLocalMediaTTL {
		t.Fatalf("share = path %q ttl %s", sharer.path, sharer.ttl)
	}
	if len(channel.sent) != 1 {
		t.Fatalf("sent = %#v", channel.sent)
	}
	msg := channel.sent[0]
	if len(msg.ImageURLs) != 1 || len(msg.VideoURLs) != 1 || msg.VideoURLs[0] != "http://diana:18080/media/resolver/token" {
		t.Fatalf("message = %#v", msg)
	}
}
