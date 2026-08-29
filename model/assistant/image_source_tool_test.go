// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 一张 1x1 的 PNG，够走完「取图 → 解码 → 上传」这条路。
const imageSourceTestDataURL = "data:image/png;base64," +
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func newImageSourceTestTool(t *testing.T, plugin *ImageSourcePlugin, settings map[string]any, event MessageEvent, store MessageHistoryStore) *dianaImageSourceTool {
	t.Helper()
	manager := NewPluginManager(NewImageSourcePlugin(nil))
	if settings != nil {
		if _, err := manager.UpdateSettings(imageSourcePluginID, settings); err != nil {
			t.Fatal(err)
		}
	}
	_, values, enabled := manager.PluginWithSettings(imageSourcePluginID, nil)
	if !enabled {
		t.Fatal("插件应当默认启用")
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, manager, nil, nil, nil, nil)
	if store != nil {
		runtime.SetMessageHistoryStore(store)
	}
	return newDianaImageSourceTool(runtime, event, plugin, values)
}

func imageSourceTestEvent() MessageEvent {
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "g", UserID: "10001", MessageID: "msg-1", ToMe: true,
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "这图哪来的"}},
			{Type: "image", Data: map[string]string{"url": imageSourceTestDataURL}},
		},
	}
}

func decodeImageSourceResult(t *testing.T, payload string) dianaImageSourceResult {
	t.Helper()
	var result dianaImageSourceResult
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatalf("结果不是 JSON：%v（%s）", err, payload)
	}
	return result
}

// 当前消息里的那张图就是要查的图，不用模型再去翻历史。
func TestImageSourceToolSearchesCurrentMessageImage(t *testing.T) {
	var uploaded atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		uploaded.Add(1)
		_, _ = w.Write([]byte(`{"error":"","result":[{"anilist":{"id":21,"title":{"native":"ワンピース"}},"episode":1071,"from":83.5,"similarity":0.964}]}`))
	}))
	defer server.Close()

	plugin := NewImageSourcePlugin(server.Client())
	plugin.traceMoeEndpoint = server.URL
	tool := newImageSourceTestTool(t, plugin, map[string]any{
		imageSourceSettingSauceNAOEnabled: false,
		imageSourceSettingTraceMoeEnabled: true,
	}, imageSourceTestEvent(), nil)

	payload, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result := decodeImageSourceResult(t, payload)
	if !result.OK || len(result.Matches) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Matches[0].Title != "ワンピース" {
		t.Fatalf("match = %#v", result.Matches[0])
	}
	if uploaded.Load() != 1 {
		t.Fatalf("上传次数 = %d", uploaded.Load())
	}
}

// 同一张图问第二遍不该再上传一次：结果落库，按内容哈希复用。
func TestImageSourceToolReusesStoredResult(t *testing.T) {
	var uploaded atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		uploaded.Add(1)
		_, _ = w.Write([]byte(`{"error":"","result":[{"anilist":{"id":21,"title":{"native":"ワンピース"}},"episode":1,"from":10,"similarity":0.97}]}`))
	}))
	defer server.Close()

	plugin := NewImageSourcePlugin(server.Client())
	plugin.traceMoeEndpoint = server.URL
	store := newFakeImageRecognitionStore()
	settings := map[string]any{
		imageSourceSettingSauceNAOEnabled: false,
		imageSourceSettingTraceMoeEnabled: true,
	}
	tool := newImageSourceTestTool(t, plugin, settings, imageSourceTestEvent(), store)

	if _, err := tool.Run(context.Background(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	payload, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result := decodeImageSourceResult(t, payload)
	if !result.Cached || len(result.Matches) != 1 {
		t.Fatalf("第二次没有命中缓存：%#v", result)
	}
	if uploaded.Load() != 1 {
		t.Fatalf("上传次数 = %d，缓存没生效", uploaded.Load())
	}
}

// 一条线路都没配好时，工具要说清原因，而不是含糊地「没查到」。
func TestImageSourceToolReportsMissingProvider(t *testing.T) {
	tool := newImageSourceTestTool(t, NewImageSourcePlugin(nil), map[string]any{
		imageSourceSettingSauceNAOEnabled: true,
		imageSourceSettingTraceMoeEnabled: false,
	}, imageSourceTestEvent(), nil)

	payload, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result := decodeImageSourceResult(t, payload)
	if result.OK || !strings.Contains(result.Message, "API Key") {
		t.Fatalf("result = %#v", result)
	}
}

// 私聊关掉之后不能再往外传图。
func TestImageSourceToolRespectsPrivateSwitch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("私聊关闭时不该发起反查")
	}))
	defer server.Close()

	plugin := NewImageSourcePlugin(server.Client())
	plugin.traceMoeEndpoint = server.URL
	event := imageSourceTestEvent()
	event.Kind = EventKindPrivate
	event.GroupID = ""
	tool := newImageSourceTestTool(t, plugin, map[string]any{
		imageSourceSettingTraceMoeEnabled: true,
		imageSourceSettingPrivateEnabled:  false,
	}, event, nil)

	payload, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if result := decodeImageSourceResult(t, payload); result.OK {
		t.Fatalf("私聊关闭时仍然反查了：%#v", result)
	}
}

// 消息里没有图时给一条能照着做的提示，而不是一句「失败」。
func TestImageSourceToolWithoutImage(t *testing.T) {
	event := imageSourceTestEvent()
	event.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "这图哪来的"}}}
	tool := newImageSourceTestTool(t, NewImageSourcePlugin(nil), map[string]any{
		imageSourceSettingTraceMoeEnabled: true,
	}, event, nil)

	payload, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result := decodeImageSourceResult(t, payload)
	if result.OK || !strings.Contains(result.Message, dianaChatHistoryToolName) {
		t.Fatalf("result = %#v", result)
	}
}

func TestImageSourceTestDataURLDecodes(t *testing.T) {
	// 这条断言看着多余，但测试里那串 base64 一旦被编辑器折行，
	// 上面所有用例都会以「图片读取失败」的形式一起挂掉，很难查。
	_, encoded, _ := strings.Cut(imageSourceTestDataURL, ",")
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		t.Fatalf("测试图片解码失败：%v", err)
	}
}

// 工具挂没挂上是这类功能最容易断的一环：插件在、设置在、界面在，
// 而模型的工具表里没有它。这里从 replyTo 走一遍，看提示词里有没有它。
func TestImageSourceToolRegisteredOnlyWhenProviderConfigured(t *testing.T) {
	toolPromptFor := func(t *testing.T, settings map[string]any) string {
		t.Helper()
		provider := &agentSequenceLLMProvider{responses: []string{
			`{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":false}`,
			`{"action":"final","content":"好"}`,
		}}
		plugins := NewDefaultPluginManager()
		if settings != nil {
			if _, err := plugins.UpdateSettings(imageSourcePluginID, settings); err != nil {
				t.Fatal(err)
			}
		}
		runtime := NewRuntime(BotConfig{OwnerID: "owner", AgentEnabled: true}, nilChannel{}, plugins, nil, nil, nil, func() (LLMProvider, error) {
			return provider, nil
		})
		if _, err := runtime.replyTo(context.Background(), MessageEvent{
			Kind: EventKindPrivate, UserID: "owner", MessageID: "message-1",
		}, "这图哪来的"); err != nil {
			t.Fatal(err)
		}
		if len(provider.requests) == 0 {
			t.Fatal("provider was not called")
		}
		return provider.requests[len(provider.requests)-1].Messages[0].Content
	}

	configured := toolPromptFor(t, map[string]any{
		imageSourceSettingSauceNAOEnabled: false,
		imageSourceSettingTraceMoeEnabled: true,
	})
	if !strings.Contains(configured, dianaImageSourceToolName) {
		t.Fatalf("配好线路之后模型仍然看不到溯源工具：%s", configured)
	}

	// 一条线路都没配好时不该挂：模型看得到就会去调，然后只能回一句「查不了」。
	unconfigured := toolPromptFor(t, map[string]any{
		imageSourceSettingSauceNAOEnabled: true,
		imageSourceSettingTraceMoeEnabled: false,
	})
	if strings.Contains(unconfigured, dianaImageSourceToolName) {
		t.Fatalf("没有可用线路时不该挂溯源工具：%s", unconfigured)
	}
}

// 图片是要出网的，超限的就别发：SauceNAO 那边也只会回一个 413，白跑一趟还
// 消耗当天的免费额度。断言的是「一次都没上传」，不是「报了个错」。
func TestImageSourceRefusesOversizedUpload(t *testing.T) {
	var uploaded atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		uploaded.Add(1)
		_, _ = w.Write([]byte(`{"error":"","result":[]}`))
	}))
	defer server.Close()

	plugin := NewImageSourcePlugin(server.Client())
	plugin.traceMoeEndpoint = server.URL
	// 1x1 的 PNG 只有几十字节，把上限压到 1 MB 还是拦不住它，所以这里换一张
	// 一定超限的图：上限设成最小的 1 MB，图片给到 2 MB。
	big := base64.StdEncoding.EncodeToString(make([]byte, 2*1024*1024))
	event := imageSourceTestEvent()
	event.Segments[1].Data["url"] = "data:image/png;base64," + big
	tool := newImageSourceTestTool(t, plugin, map[string]any{
		imageSourceSettingSauceNAOEnabled: false,
		imageSourceSettingTraceMoeEnabled: true,
		imageSourceSettingMaxUploadMB:     1,
	}, event, nil)

	payload, err := tool.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result := decodeImageSourceResult(t, payload)
	if result.OK {
		t.Fatalf("超限的图不该被当成查成功了：%#v", result)
	}
	if !strings.Contains(result.Message, "上传上限") {
		t.Fatalf("没有说明是体积超限：%q", result.Message)
	}
	if uploaded.Load() != 0 {
		t.Fatalf("超限的图仍然上传了 %d 次", uploaded.Load())
	}
}

// 接口地址可以改（自建网关、反代），但改错了不能把图片用明文发出去：
// 非 HTTPS 一律退回官方地址，本机调试除外。
func TestImageSourceEndpointRefusesPlaintext(t *testing.T) {
	const official = "https://api.trace.moe/search"
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"留空走官方", "", official},
		{"HTTPS 自建网关", "https://gateway.example/search", "https://gateway.example/search"},
		{"本机调试放行", "http://127.0.0.1:8080/search", "http://127.0.0.1:8080/search"},
		{"明文公网退回官方", "http://gateway.example/search", official},
		{"地址里带账号密码退回官方", "https://user:pass@gateway.example/search", official},
		{"根本不是地址退回官方", "gateway.example", official},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := imageSourceEndpoint(tc.raw, official); got != tc.want {
				t.Fatalf("imageSourceEndpoint(%q) = %q，期望 %q", tc.raw, got, tc.want)
			}
		})
	}
}

// 换了接口地址等于换了一家服务，上一次的结果不能直接顶上。
func TestImageSourceCacheKeyFollowsEndpoint(t *testing.T) {
	base := imageSourceConfig{TraceMoeEnabled: true, MinSimilarity: 60, MaxResults: 3}
	moved := base
	moved.TraceMoeURL = "https://gateway.example/search"
	if imageSourceCacheKey("digest", base) == imageSourceCacheKey("digest", moved) {
		t.Fatal("换了接口地址之后缓存键没变，旧结果会被直接顶上")
	}
}

// 机器人得知道自己有这个能力，否则被问「你能查图片出处吗」只会否认。
func TestImageSourceIsInCapabilityKnowledge(t *testing.T) {
	for _, doc := range coreCapabilityDocuments {
		if doc.ID == "core:image-source" {
			if !strings.Contains(doc.Content, dianaImageSourceToolName) {
				t.Fatalf("能力条目没写工具名：%q", doc.Content)
			}
			return
		}
	}
	t.Fatal("能力清单里没有图片溯源，机器人不知道自己能干这件事")
}
