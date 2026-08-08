package assistant

import (
	"context"
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
