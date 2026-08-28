// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const imageSourceTestGIF = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"

const imageSourceTestPixel = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func imageSourceTestEvent() MessageEvent {
	return MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "20001",
		UserID:    "10001",
		MessageID: "message-1",
		Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"url": imageSourceTestPixel}}},
	}
}

func imageSourceTestTool(t *testing.T, handler http.HandlerFunc, extra SettingValues) (*dianaImageSourceTool, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	plugin := NewImageSourcePlugin(server.Client())
	settings := SettingValues{
		imageSourceSettingSauceNAOURL:    server.URL + "/saucenao",
		imageSourceSettingTraceMoeURL:    server.URL + "/tracemoe",
		imageSourceSettingSauceNAOAPIKey: "test-key",
		imageSourceSettingMinSimilarity:  60,
	}
	for key, value := range extra {
		settings[key] = value
	}
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewDefaultPluginManager(), nil, nil, nil, nil)
	return newDianaImageSourceTool(runtime, imageSourceTestEvent(), plugin, settings), server
}

func runImageSourceTool(t *testing.T, tool *dianaImageSourceTool, input map[string]any) imageSourceResult {
	t.Helper()
	raw, err := tool.Run(context.Background(), input)
	if err != nil {
		t.Fatalf("Run() 出错：%v", err)
	}
	var result imageSourceResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("结果不是 JSON：%v / %s", err, raw)
	}
	return result
}

// 两个来源分工不同，不是互为备份：SauceNAO 认插画，trace.moe 认番剧截图并给到集数
// 和时间点。两边的结果要一起交给模型，并且换算成同一套刻度后按相似度排序。
func TestImageSourceToolMergesBothProviders(t *testing.T) {
	tool, _ := imageSourceTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/saucenao"):
			if r.URL.Query().Get("api_key") != "test-key" {
				t.Errorf("没有带上 API Key：%s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"header":{"status":0},"results":[
				{"header":{"similarity":"93.71","index_name":"Pixiv"},
				 "data":{"title":"纳西妲","member_name":"某位画师","ext_urls":["https://www.pixiv.net/artworks/1"]}}]}`)
		case strings.HasSuffix(r.URL.Path, "/tracemoe"):
			_, _ = io.WriteString(w, `{"result":[
				{"similarity":0.71,"episode":3,"from":83.4,
				 "anilist":{"id":21,"title":{"native":"原神","romaji":"Genshin"}}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}, nil)

	result := runImageSourceTool(t, tool, nil)
	if !result.OK || len(result.Matches) != 2 {
		t.Fatalf("两个来源的结果都应该在：%#v", result)
	}
	// trace.moe 的 0~1 要换算成百分制，否则 0.71 会被相似度下限当场丢掉。
	if result.Matches[0].Provider != "saucenao" || result.Matches[1].Provider != "tracemoe" {
		t.Fatalf("没有按相似度排序：%#v", result.Matches)
	}
	if result.Matches[1].Similarity < 70 || result.Matches[1].Similarity > 72 {
		t.Fatalf("trace.moe 的相似度没有换算成百分制：%v", result.Matches[1].Similarity)
	}
	if result.Matches[1].Episode != "3" || result.Matches[1].Timestamp != "01:23" {
		t.Fatalf("集数或时间点不对：%#v", result.Matches[1])
	}
	if len(result.Matches[0].URLs) != 1 || !strings.Contains(result.Matches[0].URLs[0], "pixiv") {
		t.Fatalf("没有带出原链：%#v", result.Matches[0])
	}
}

// 低分结果几乎全是噪声，而模型看到一条「来源」就容易当真。丢掉之后还要说清是丢了，
// 不能让它读成「什么都没查到」之外的意思。
func TestImageSourceToolDropsLowSimilarityNoise(t *testing.T) {
	tool, _ := imageSourceTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/saucenao") {
			_, _ = io.WriteString(w, `{"header":{"status":0},"results":[{"header":{"similarity":"12.3","index_name":"Pixiv"},"data":{"title":"完全不相干"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"result":[]}`)
	}, nil)

	result := runImageSourceTool(t, tool, nil)
	if len(result.Matches) != 0 {
		t.Fatalf("低分结果不该交给模型：%#v", result.Matches)
	}
	if result.FilteredOut != 1 || !strings.Contains(result.Message, "不要把低分结果说成来源") {
		t.Fatalf("没有说清是被过滤掉了：%#v", result)
	}
}

// 一个来源挂掉不影响另一个：番剧截图和插画本来就只有一边认得。
func TestImageSourceToolKeepsGoingWhenOneProviderFails(t *testing.T) {
	tool, _ := imageSourceTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/saucenao") {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"result":[{"similarity":0.98,"episode":1,"from":10,"anilist":{"id":7,"title":{"native":"孤独摇滚"}}}]}`)
	}, nil)

	result := runImageSourceTool(t, tool, nil)
	if len(result.Matches) != 1 || result.Matches[0].Title != "孤独摇滚" {
		t.Fatalf("另一个来源的结果应该照常返回：%#v", result)
	}
	if len(result.ProviderErrors) != 1 || !strings.Contains(result.ProviderErrors[0], "限流") {
		t.Fatalf("失败的那个来源要报出原因：%#v", result.ProviderErrors)
	}
}

// 全挂时不能只说「没找到」——没配 API Key 和真的搜不到是两件事，用户要能分清。
func TestImageSourceToolReportsWhyEveryProviderFailed(t *testing.T) {
	tool, _ := imageSourceTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}, SettingValues{imageSourceSettingSauceNAOAPIKey: ""})

	result := runImageSourceTool(t, tool, nil)
	if result.OK || result.FailureCode != "provider_failed" {
		t.Fatalf("全挂时不该报成功：%#v", result)
	}
	if !strings.Contains(result.Message, "API Key") {
		t.Fatalf("没有说清是缺配置：%s", result.Message)
	}
}

// 没有图片时直接说清楚，别去问第三方。
func TestImageSourceToolWithoutAnyImage(t *testing.T) {
	tool, _ := imageSourceTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("没有图片时不该发起请求")
	}, nil)
	tool.event.Segments = nil

	result := runImageSourceTool(t, tool, nil)
	if result.FailureCode != "no_image" {
		t.Fatalf("结果 = %#v", result)
	}
}

// 接口地址只放 HTTPS，本机 HTTP 留给调试——和联网搜索同一条规矩。
func TestImageSourceEndpointRejectsPlainHTTP(t *testing.T) {
	if _, err := imageSourceEndpoint("http://example.invalid/search"); err == nil {
		t.Fatal("公网 HTTP 地址必须被拒绝")
	}
	if _, err := imageSourceEndpoint("https://saucenao.com/search.php"); err != nil {
		t.Fatalf("HTTPS 地址被拒了：%v", err)
	}
	if _, err := imageSourceEndpoint("http://127.0.0.1:8080/search"); err != nil {
		t.Fatalf("本机调试地址被拒了：%v", err)
	}
	if _, err := imageSourceEndpoint("https://user:pass@saucenao.com/search.php"); err == nil {
		t.Fatal("带凭据的地址必须被拒绝")
	}
}

// 连发多张图时要能指定查第几张，越界要说清这里只有几张，而不是默默查了第一张。
func TestImageSourceToolSelectsImageByIndex(t *testing.T) {
	var searched int
	tool, _ := imageSourceTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		searched++
		_, _ = io.WriteString(w, `{"header":{"status":0},"results":[]}`)
	}, SettingValues{imageSourceSettingTraceMoeEnabled: false})
	tool.event.Segments = []MessageSegment{
		{Type: "image", Data: map[string]string{"url": imageSourceTestPixel}},
		{Type: "image", Data: map[string]string{"url": imageSourceTestGIF}},
	}

	// JSON 数字会解成 float64，取序号时不能想当然按 int 断言。
	result := runImageSourceTool(t, tool, map[string]any{"image_index": float64(2)})
	if result.ImageIndex != 2 || result.ImageCount != 2 {
		t.Fatalf("没有按序号选图：%#v", result)
	}
	if searched != 1 {
		t.Fatalf("请求次数 = %d", searched)
	}

	over := runImageSourceTool(t, tool, map[string]any{"image_index": float64(5)})
	if over.FailureCode != "invalid_input" || !strings.Contains(over.Message, "2 张") {
		t.Fatalf("越界时要说清这里有几张：%#v", over)
	}
}
