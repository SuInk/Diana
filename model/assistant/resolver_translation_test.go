package assistant

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResolverTranslationFallbackAndMedia(t *testing.T) {
	for _, mode := range []string{"disabled", "missing", "failed", "empty", "oversize", "cancelled", "success"} {
		t.Run(mode, func(t *testing.T) {
			original := PluginResponse{Reply: "Original https://example.com", Context: "Original evidence", ImageURLs: []string{"https://example.com/a.jpg"}, VideoURLs: []string{"https://example.com/a.mp4"}, Forward: true, ForwardMessages: []OutgoingMessage{{Text: "Original", ImageURLs: []string{"https://example.com/a.jpg"}}}}
			response := original
			called := false
			req := PluginRequest{Settings: SettingValues{resolverSettingTranslate: mode != "disabled"}}
			req.TranslateResolver = func(ctx context.Context, event MessageEvent, text string) (string, error) {
				called = true
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 15*time.Second {
					t.Fatal("translation has no bounded deadline")
				}
				if text != original.Reply {
					t.Fatalf("unexpected source %q", text)
				}
				switch mode {
				case "failed":
					return "", errors.New("offline")
				case "empty":
					return " ", nil
				case "oversize":
					return strings.Repeat("字", 6001), nil
				}
				return "原文译文 [CQ:at,qq=all]", nil
			}
			if mode == "missing" {
				req.TranslateResolver = nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			NewResolverPlugin(nil).translateResponse(ctx, req, &response)
			if mode == "success" {
				if !strings.Contains(response.Reply, "原文译文") || !strings.HasPrefix(response.Reply, original.Reply) || len(response.ForwardMessages) != 2 || !response.ForwardMessages[1].LiteralText {
					t.Fatalf("missing translation: %#v", response)
				}
				if strings.Contains(response.Reply, "[CQ:") {
					t.Fatal("translation introduced CQ controls")
				}
				if !reflect.DeepEqual(response.ImageURLs, original.ImageURLs) || !reflect.DeepEqual(response.VideoURLs, original.VideoURLs) || !reflect.DeepEqual(response.ForwardMessages[0], original.ForwardMessages[0]) {
					t.Fatal("media changed")
				}
			} else if !reflect.DeepEqual(response, original) {
				t.Fatalf("fallback changed response: %#v", response)
			}
			if mode == "disabled" && called {
				t.Fatal("disabled translation called model")
			}
		})
	}
}

func TestResolverTranslationBoundsSource(t *testing.T) {
	response := &PluginResponse{Reply: strings.Repeat("a", 15000)}
	called := false
	req := PluginRequest{Settings: SettingValues{resolverSettingTranslate: true}, TranslateResolver: func(ctx context.Context, event MessageEvent, text string) (string, error) {
		called = true
		if len([]rune(text)) != 12000 {
			t.Fatal("source is not bounded")
		}
		return "摘要", nil
	}}
	NewResolverPlugin(nil).translateResponse(context.Background(), req, response)
	if !called {
		t.Fatal("translator was not called")
	}
	if !strings.HasPrefix(response.Reply, strings.Repeat("a", 15000)) {
		t.Fatal("original was truncated")
	}
}

// 本来就是中文的解析结果不再翻一遍，免得原文后面附一份几乎一样的「译文」。
func TestResolverTranslationSkipsChineseSource(t *testing.T) {
	original := "【B站】这期视频讲了缓存为什么会丢图，作者 UP 主顺带测了 JSON round trip。https://www.bilibili.com/video/BV1xx411c7mD"
	response := &PluginResponse{Reply: original}
	req := PluginRequest{Settings: SettingValues{resolverSettingTranslate: true}, TranslateResolver: func(ctx context.Context, event MessageEvent, text string) (string, error) {
		t.Fatal("chinese source should not be translated")
		return "", nil
	}}
	NewResolverPlugin(nil).translateResponse(context.Background(), req, response)
	if response.Reply != original {
		t.Fatalf("reply changed: %s", response.Reply)
	}
	if mostlyChineseText("Release notes: photos now survive a JSON round trip（图片缓存修复）") {
		t.Fatal("english-dominant text with a short chinese aside should still be translated")
	}
}

// Opt-in replay against the configured current model; never sends a chat message.
func TestLiveResolverTranslation(t *testing.T) {
	client := liveLLMClient(t)
	runtime := rssMediaRuntime(t, NewRSSWatchPlugin(nil), client)
	response := &PluginResponse{Reply: "The release fixes an image cache bug. Photos now survive a JSON round trip; notification filtering remains unchanged. https://example.com/release", ImageURLs: []string{"https://example.com/photo.jpg"}}
	req := PluginRequest{Settings: SettingValues{resolverSettingTranslate: true}, TranslateResolver: runtime.translateResolverText}
	NewResolverPlugin(nil).translateResponse(context.Background(), req, response)
	if !strings.Contains(response.Reply, "中文译文") {
		t.Fatal("current-model replay fell back instead of producing a translation")
	}
	if !strings.Contains(response.Reply, "缓存") || !strings.Contains(response.Reply, "JSON") {
		t.Fatalf("translation lost key facts: %s", response.Reply)
	}
	t.Log(response.Reply)
}

func TestResolverHandleTranslatesForwardAndDirect(t *testing.T) {
	for _, forward := range []bool{false, true} {
		plugin := NewResolverPlugin(nil)
		plugin.twitterPostFetcher = func(context.Context, string) (twitterPost, bool) {
			return twitterPost{Text: "Hello world", AuthorName: "Example", AuthorHandle: "example", Media: []twitterMedia{{Type: "photo", URL: "https://pbs.twimg.com/media/example.jpg"}}}, true
		}
		plugin.twitterMediaDownloader = func(context.Context, twitterMedia) string { return "/tmp/example.jpg" }
		calls := 0
		req := PluginRequest{Text: "https://x.com/example/status/123456", Settings: SettingValues{resolverSettingTranslate: true, resolverSettingDownloadMedia: true, resolverSettingMergedForward: forward}, TranslateResolver: func(ctx context.Context, event MessageEvent, text string) (string, error) {
			calls++
			if !strings.Contains(text, "Hello world") {
				t.Fatalf("missing extracted text %q", text)
			}
			return "你好世界", nil
		}}
		response, err := plugin.Handle(context.Background(), req)
		if err != nil || response == nil {
			t.Fatalf("response=%#v error=%v", response, err)
		}
		if calls != 1 || response.Forward != forward || !strings.Contains(response.Reply, "你好世界") || !strings.Contains(response.Context, "Hello world") {
			t.Fatalf("response=%#v calls=%d", response, calls)
		}
		if !strings.Contains(response.ForwardMessages[len(response.ForwardMessages)-1].Text, "你好世界") || len(response.ImageURLs) != 1 {
			t.Fatalf("missing translation or photo: %#v", response)
		}
	}
}
