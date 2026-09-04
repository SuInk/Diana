// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/applog"
	"github.com/SuInk/diana/model/llm"
)

func TestResolverPlatformHostMatchingRejectsLookalikes(t *testing.T) {
	for _, host := range []string{
		"bilibili.com.attacker.example",
		"evil-youtube.com",
		"twitter.com.example",
		"xiaohongshu.com.attacker.example",
		"notdouyin.com",
	} {
		if isKnownResolverPlatformHost(host) {
			t.Fatalf("lookalike host %q was trusted", host)
		}
	}
	for _, host := range []string{"bilibili.com", "www.bilibili.com", "m.youtube.com", "x.com", "www.xiaohongshu.com", "v.douyin.com"} {
		if !isKnownResolverPlatformHost(host) {
			t.Fatalf("real platform host %q was rejected", host)
		}
	}
}

func TestExtractURLsNormalizesCopiedXiaohongshuText(t *testing.T) {
	text := "打开小红书查看【测试笔记】 xhslink.com/o/20YWuppICeI 复制本条信息"
	urls := knownResolverPlatformURLs(text)
	want := "https://xhslink.com/o/20YWuppICeI"
	if len(urls) != 1 || urls[0] != want {
		t.Fatalf("urls = %#v, want %#v", urls, []string{want})
	}
}

func TestExtractURLsNormalizesStandaloneBilibiliVideoID(t *testing.T) {
	text := "帮我解析 BV1Gc7K6UEgz 的内容"
	urls := knownResolverPlatformURLs(text)
	want := "https://www.bilibili.com/video/BV1Gc7K6UEgz"
	if len(urls) != 1 || urls[0] != want {
		t.Fatalf("urls = %#v, want %#v", urls, []string{want})
	}
	if urls := knownResolverPlatformURLs("普通单词 abbreviation 不应触发"); len(urls) != 0 {
		t.Fatalf("ordinary text produced urls: %#v", urls)
	}
}

// 下载上限必须始终跟随配置的最终文件上限：下载过程中也要拦，否则恶意服务端能
// 一直吐流把磁盘写满。
func TestResolverVideoDownloadLimitFollowsConfiguredLimit(t *testing.T) {
	t.Setenv("DIANA_RESOLVER_VIDEO_MAX_MB", "64")
	if got := resolverVideoDownloadMaxMB(context.Background()); got != 64 {
		t.Fatalf("download limit = %d, want 64", got)
	}
}

// TestPluginManagerInstallEnableRun 验证对应功能场景。
func TestPluginManagerInstallEnableRun(t *testing.T) {
	manager := NewPluginManager(testPlugin{})
	state, ok := manager.Get("test")
	if !ok {
		t.Fatal("plugin missing")
	}
	if state.Installed {
		t.Fatalf("Installed = true, want false for non built-in plugin")
	}

	if _, err := manager.Install("test"); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	responses := manager.Run(context.Background(), PluginRequest{Text: "hello"})
	if len(responses) != 1 || responses[0].Context != "ctx: hello" {
		t.Fatalf("responses = %#v", responses)
	}

	if _, err := manager.SetEnabled("test", false); err != nil {
		t.Fatalf("SetEnabled() error = %v", err)
	}
	if responses := manager.Run(context.Background(), PluginRequest{Text: "hello"}); len(responses) != 0 {
		t.Fatalf("disabled responses = %#v", responses)
	}
}

func TestPluginManagerRunRecoversPluginPanic(t *testing.T) {
	manager := NewPluginManager(panicPlugin{}, testPlugin{})
	if _, err := manager.Install("panic"); err != nil {
		t.Fatalf("Install(panic) error = %v", err)
	}
	if _, err := manager.Install("test"); err != nil {
		t.Fatalf("Install(test) error = %v", err)
	}
	responses := manager.Run(context.Background(), PluginRequest{Text: "hello"})
	if len(responses) != 1 || responses[0].Context != "ctx: hello" {
		t.Fatalf("responses = %#v", responses)
	}
}

func TestPluginManagerObserveRecoversPluginPanic(t *testing.T) {
	manager := NewPluginManager(panicObserverPlugin{})
	event := manager.ObserveEvent(context.Background(), MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "123",
		MessageID:  "m1",
		RawMessage: "hello",
	})
	if event.RawMessage != "hello" {
		t.Fatalf("event = %#v", event)
	}
}

// TestResolverPluginExtractsKnownPlatformContext 验证对应功能场景。
func TestResolverPluginExtractsKnownPlatformContext(t *testing.T) {
	t.Setenv("DIANA_XHS_CK", "")
	t.Setenv("XHS_CK", "")
	t.Setenv("xhs_ck", "")
	plugin := NewResolverPlugin(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})})
	plugin.videoDownloader = func(context.Context, string) string {
		return ""
	}
	resp, err := plugin.Handle(context.Background(), PluginRequest{Text: "看这个 https://www.xiaohongshu.com/discovery/item/abc?xsec_source=pc_share&amp;xsec_token=tok"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	if strings.Contains(resp.Context, "链接解析结果") || !strings.Contains(resp.Context, "识别内容来自：【小红书】") {
		t.Fatalf("Context = %q", resp.Context)
	}
	if !resp.Forward || len(resp.ForwardMessages) != 1 {
		t.Fatalf("forward resp = %#v", resp)
	}
}

func TestFetchFinalURLDetailsReportsExpiredShortLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer server.Close()

	finalURL, statusCode, err := fetchFinalURLDetails(context.Background(), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if finalURL != server.URL || statusCode != http.StatusNotFound {
		t.Fatalf("finalURL=%q statusCode=%d", finalURL, statusCode)
	}
}

// App 新版分享用的是 xhslink.cn，旧域名 xhslink.com 仍在流通。
// 漏掉 .cn 会让整条链接识别不出平台，被链接解析静默丢弃。
func TestXiaohongshuShortLinkDomains(t *testing.T) {
	cases := map[string]string{
		"点击查看超自然融图 https://xhslink.cn/o/6kxZZXGfDWA 留住这段口令，去【小红书】瞅瞅笔记~": "https://xhslink.cn/o/6kxZZXGfDWA",
		"xhslink.cn/o/6kxZZXGfDWA": "https://xhslink.cn/o/6kxZZXGfDWA",
		"避雷立充 http://xhslink.com/o/20YWuppICeI 留住这段口令": "http://xhslink.com/o/20YWuppICeI",
	}
	for text, want := range cases {
		urls := extractURLs(text)
		if len(urls) != 1 || urls[0] != want {
			t.Fatalf("extractURLs(%q) = %v, want [%q]", text, urls, want)
		}
		if !isKnownResolverPlatformURL(want) {
			t.Fatalf("isKnownResolverPlatformURL(%q) = false", want)
		}
		host := strings.TrimPrefix(strings.SplitN(strings.SplitN(want, "://", 2)[1], "/", 2)[0], "www.")
		if key, _ := platformKeyAndLabel(host); key != "xiaohongshu" {
			t.Fatalf("platformKeyAndLabel(%q) = %q, want xiaohongshu", host, key)
		}
	}
}

func TestIsXiaohongshuLiveURL(t *testing.T) {
	for _, rawURL := range []string{
		"http://xhslink.com/m/9ry2BJL0V4D",
		"https://xhslink.cn/m/9ry2BJL0V4D",
		"https://www.xiaohongshu.com/live/123",
		"https://www.xiaohongshu.com/livestream/123",
	} {
		if !isXiaohongshuLiveURL(rawURL) {
			t.Fatalf("isXiaohongshuLiveURL(%q) = false", rawURL)
		}
	}
	if isXiaohongshuLiveURL("https://www.xiaohongshu.com/explore/note-id") {
		t.Fatal("ordinary note classified as live")
	}
}

func TestFetchXiaohongshuNoteIntegration(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("DIANA_XHS_TEST_URL"))
	if rawURL == "" {
		t.Skip("set DIANA_XHS_TEST_URL and XHS_CK to test the live parser")
	}
	note, status := fetchXiaohongshuNote(context.Background(), rawURL)
	if status != "" || len(note) == 0 {
		t.Fatalf("status=%q note=%#v", status, note)
	}
	t.Logf("parsed type=%q title=%q", anyString(note["type"]), anyString(note["title"]))
}

func TestResolverPluginExtractsURLFromRawMessage(t *testing.T) {
	plugin := NewResolverPlugin(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("<title>Raw 标题</title>")),
		}, nil
	})})
	plugin.videoDownloader = func(context.Context, string) string {
		return ""
	}
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Event: MessageEvent{RawMessage: "分享 https://github.com/SuInk/Diana"},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !strings.Contains(resp.Context, "Raw 标题") {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestResolverPluginDownloadsPlatformVideo(t *testing.T) {
	plugin := NewResolverPlugin(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader("<title>X 视频</title>")),
		}, nil
	})})
	plugin.videoDownloader = func(_ context.Context, raw string) string {
		if !strings.Contains(raw, "x.com") {
			t.Fatalf("downloader raw=%q", raw)
		}
		return "/tmp/diana-test-video.mp4"
	}
	logs := &captureAppLogs{}

	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text:    "看这个 https://x.com/example/status/1",
		Event:   MessageEvent{Kind: EventKindPrivate, UserID: "10001"},
		AppLogs: logs,
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled || len(resp.VideoURLs) != 1 {
		t.Fatalf("resp = %#v", resp)
	}
	if resp.VideoURLs[0] != "/tmp/diana-test-video.mp4" {
		t.Fatalf("VideoURLs = %#v", resp.VideoURLs)
	}
	if len(resp.ImageURLs) != 0 {
		t.Fatalf("ImageURLs = %#v", resp.ImageURLs)
	}
	if resp.Context != "识别：小蓝鸟学习版" {
		t.Fatalf("Context = %q", resp.Context)
	}
	if !resp.Forward || len(resp.ForwardMessages) != 2 {
		t.Fatalf("forward resp = %#v", resp)
	}
	if resp.ForwardMessages[0].Text != "识别：小蓝鸟学习版" {
		t.Fatalf("meta node = %#v", resp.ForwardMessages[0])
	}
	if len(resp.ForwardMessages[1].VideoURLs) != 1 || resp.ForwardMessages[1].VideoURLs[0] != "/tmp/diana-test-video.mp4" {
		t.Fatalf("video node = %#v", resp.ForwardMessages[1])
	}
	if len(logs.entries) != 1 || logs.entries[0].Action != "diana.resolver.video_download" || logs.entries[0].Kind != applog.KindOperation {
		t.Fatalf("logs = %#v", logs.entries)
	}
}

func TestResolverPluginDedupesHTMLEscapedURLs(t *testing.T) {
	raw := "https://www.xiaohongshu.com/discovery/item/abc?xsec_source=pc_share&xsec_token=tok"
	escaped := "https://www.xiaohongshu.com/discovery/item/abc?xsec_source=pc_share&amp;xsec_token=tok"
	doubleEscaped := "https://www.xiaohongshu.com/discovery/item/abc?xsec_source=pc_share&amp;amp;xsec_token=tok"
	urls := extractResolverRequestURLs(PluginRequest{
		Text:  "看这个 " + escaped + " " + doubleEscaped,
		Event: MessageEvent{RawMessage: "分享 " + raw},
	})
	if len(urls) != 1 || urls[0] != raw {
		t.Fatalf("urls = %#v", urls)
	}
}

func TestResolverPluginExtractsEscapedMiniAppURL(t *testing.T) {
	raw := `[CQ:json,data={"meta":{"detail_1":{"title":"哔哩哔哩","qqdocurl":"https:\/\/b23.tv\/tOnAfAQ?share_medium=android&amp;share_source=qq"}}}]`
	urls := extractResolverRequestURLs(PluginRequest{
		Event: MessageEvent{RawMessage: raw},
	})
	want := "https://b23.tv/tOnAfAQ?share_medium=android&share_source=qq"
	if len(urls) != 1 || urls[0] != want {
		t.Fatalf("urls = %#v, want %#v", urls, want)
	}
	platformURLs := knownResolverPlatformURLs(raw)
	if len(platformURLs) != 1 || platformURLs[0] != want {
		t.Fatalf("platformURLs = %#v, want %#v", platformURLs, want)
	}
}

// TestDefaultPluginManagerIncludesFileParser 验证对应功能场景。
func TestDefaultPluginManagerIncludesFileParser(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get("official.file-parser-go")
	if !ok {
		t.Fatal("file parser plugin missing")
	}
	if !state.Installed || !state.Enabled {
		t.Fatalf("file parser state = %#v", state)
	}
}

func TestDefaultPluginManagerIncludesNoOpLLMConfigPlugin(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, ok := manager.Get(llmConfigPluginID)
	if !ok || !state.Installed || !state.Enabled || !state.Manifest.BuiltIn {
		t.Fatalf("llm config plugin state = %#v, found = %v", state, ok)
	}

	tests := []PluginRequest{
		{Event: MessageEvent{UserID: "20002"}, OwnerID: "10001", Text: "我做了一个 OCR 和模型选择页面"},
		{Event: MessageEvent{UserID: "10001"}, OwnerID: "10001", Text: "比较 Gemini 和 Claude 的长文能力"},
		{Event: MessageEvent{UserID: "10001"}, OwnerID: "10001", Text: "这个 OpenAI API 中转项目怎么样？"},
		{Event: MessageEvent{UserID: "20002"}, OwnerID: "10001", Text: "把 Diana 的模型切到 claude-sonnet-4-5"},
	}
	for _, req := range tests {
		resp, err := manager.RunOneWithOverrides(context.Background(), llmConfigPluginID, req, nil)
		if err != nil || resp != nil {
			t.Fatalf("text=%q resp=%#v err=%v", req.Text, resp, err)
		}
	}
}

// TestPluginManagerRestoreKeepsBuiltInDisabledChoice 验证对应功能场景。
func TestPluginManagerRestoreKeepsBuiltInDisabledChoice(t *testing.T) {
	manager := NewDefaultPluginManager()
	manager.Restore(map[string]PluginState{
		"official.file-parser-go": {
			Enabled: false,
		},
	})
	state, ok := manager.Get("official.file-parser-go")
	if !ok {
		t.Fatal("file parser plugin missing")
	}
	if !state.Installed {
		t.Fatalf("Installed = false, want true")
	}
	if state.Enabled {
		t.Fatalf("Enabled = true, want false")
	}
}

// TestFileParserPluginParsesTextFileURL 验证对应功能场景。
func TestFileParserPluginParsesTextFileURL(t *testing.T) {
	plugin := NewFileParserPlugin(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader("hello\nworld")),
		}, nil
	})})

	resp, err := plugin.Handle(context.Background(), PluginRequest{Text: "看文件 https://example.com/report.txt"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	if !strings.Contains(resp.Context, "report.txt") || !strings.Contains(resp.Context, "hello") {
		t.Fatalf("Context = %q", resp.Context)
	}
}

// 「刚才那个文件」这类指代由前置指代解析（LLM）处理：它把选中的历史消息填进
// event.Quoted，文件从那里带进来。运行时不再用词表去正文里猜用户是不是在指之前
// 发过的文件。
func TestFileParserPluginCollectsSemanticallyResolvedFile(t *testing.T) {
	file := MessageSegment{
		Type: "file",
		Data: map[string]string{
			"name":    "项目说明文档.pdf",
			"file_id": "file-1",
			"busid":   "101",
		},
	}
	refs := collectFileRefs(PluginRequest{
		Text: "这两个文件有什么区别",
		Event: MessageEvent{
			Kind:    EventKindGroup,
			GroupID: "123456",
			Quoted: &QuotedMessage{
				MessageID: "earlier-file",
				GroupID:   "123456",
				Semantic:  true,
				Segments:  []MessageSegment{file},
			},
		},
	})
	if len(refs) != 1 {
		t.Fatalf("refs = %#v", refs)
	}
	if refs[0].Name != "项目说明文档.pdf" || refs[0].FileID != "file-1" || refs[0].GroupID != "123456" {
		t.Fatalf("ref = %#v", refs[0])
	}
}

func TestFileParserPluginDoesNotCollectUnreferencedRecentFile(t *testing.T) {
	// 正文写什么都一样：没有被指代解析选中，就不会被收集。
	for _, text := range []string{"宝", "什么意思", "重试下", "这两个文件有什么区别", "看看那个 pdf"} {
		t.Run(text, func(t *testing.T) {
			refs := collectFileRefs(PluginRequest{
				Text: text,
				RecentEvents: []MessageEvent{{
					Kind:    EventKindGroup,
					GroupID: "123456",
					Segments: []MessageSegment{{
						Type: "file",
						Data: map[string]string{
							"name":    "讲义.pdf",
							"file_id": "file-1",
						},
					}},
				}},
			})
			if len(refs) != 0 {
				t.Fatalf("refs = %#v, want none", refs)
			}
		})
	}
}

func TestFileParserPluginCollectsCurrentAndQuotedFiles(t *testing.T) {
	refs := collectFileRefs(PluginRequest{
		Text: "看看",
		Event: MessageEvent{
			Kind:    EventKindGroup,
			GroupID: "123456",
			Segments: []MessageSegment{{
				Type: "file",
				Data: map[string]string{"name": "当前.txt", "file_id": "current-file"},
			}},
			Quoted: &QuotedMessage{
				GroupID: "654321",
				Segments: []MessageSegment{{
					Type: "file",
					Data: map[string]string{"name": "引用.pdf", "file_id": "quoted-file"},
				}},
			},
		},
	})
	if len(refs) != 2 {
		t.Fatalf("refs = %#v", refs)
	}
	if refs[0].FileID != "current-file" || refs[0].GroupID != "123456" {
		t.Fatalf("current ref = %#v", refs[0])
	}
	if refs[1].FileID != "quoted-file" || refs[1].GroupID != "654321" {
		t.Fatalf("quoted ref = %#v", refs[1])
	}
}

func TestExtractPDFTextUsesSandboxedRendererText(t *testing.T) {
	renderer := &fakePDFTextRenderer{texts: []string{
		"Hello PDF text layer",
		strings.Repeat("中文", 20),
	}}
	extracted := extractPDFText(context.Background(), renderer, []byte("pdf"), 1000)
	if extracted.NeedsOCR() {
		t.Fatalf("text extraction was classified as scanned: %#v", extracted)
	}
	for _, want := range []string{"第 1 页：", "Hello PDF text layer", "第 2 页：", "中文中文"} {
		if !strings.Contains(extracted.Text, want) {
			t.Fatalf("extracted text missing %q: %q", want, extracted.Text)
		}
	}
}

func TestExtractPDFTextKeepsOCRFallbackForSparseDocuments(t *testing.T) {
	renderer := &fakePDFTextRenderer{texts: []string{"tiny", ""}}
	extracted := extractPDFText(context.Background(), renderer, []byte("pdf"), 1000)
	if !extracted.NeedsOCR() {
		t.Fatalf("sparse extraction should require OCR: %#v", extracted)
	}
}

type fakePDFTextRenderer struct {
	texts []string
}

func (r *fakePDFTextRenderer) Open(context.Context, []byte) (pdfRenderSession, error) {
	return &fakePDFTextSession{texts: append([]string(nil), r.texts...)}, nil
}

type fakePDFTextSession struct {
	texts []string
}

func (s *fakePDFTextSession) PageCount() int { return len(s.texts) }
func (s *fakePDFTextSession) Close() error   { return nil }
func (s *fakePDFTextSession) RenderJPEG(context.Context, int) ([]byte, error) {
	return []byte{0xff, 0xd8, 0xff, 0xd9}, nil
}
func (s *fakePDFTextSession) PageText(_ context.Context, page int) (string, error) {
	if page < 0 || page >= len(s.texts) {
		return "", errors.New("page out of range")
	}
	return s.texts[page], nil
}

// TestFileParserPluginResolvesOneBotFileID 验证 OneBot 文件段只有 file_id 时会调用 OneBot 获取文件。
func TestFileParserPluginResolvesOneBotFileID(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "report.txt")
	if err := os.WriteFile(filePath, []byte("hello from onebot file"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	channel := &fileResolveChannel{filePath: filePath}
	plugin := NewFileParserPlugin(nil)
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Channel: channel,
		Event: MessageEvent{
			Kind:    EventKindGroup,
			GroupID: "123456",
			Segments: []MessageSegment{{
				Type: "file",
				Data: map[string]string{
					"name":    "report.txt",
					"file_id": "file-1",
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !strings.Contains(resp.Context, "hello from onebot file") {
		t.Fatalf("resp = %#v", resp)
	}
	if len(channel.calls) == 0 || channel.calls[0] != "get_group_file_url" {
		t.Fatalf("calls = %#v", channel.calls)
	}
}

func TestOneBotFileResolveRequestsFallsBackToFilename(t *testing.T) {
	requests := oneBotFileResolveRequests(fileRef{
		Name:    "report.pdf",
		FileID:  "stale-file-id",
		GroupID: "123456",
	})
	if len(requests) != 4 {
		t.Fatalf("requests = %#v", requests)
	}
	filenameFallback := requests[1]
	if filenameFallback.action != "get_file" || filenameFallback.params["file"] != "report.pdf" {
		t.Fatalf("filename fallback = %#v", filenameFallback)
	}
}

func TestFileParserPluginRefreshesStaleFileFromGroupHistory(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "report.txt")
	if err := os.WriteFile(filePath, []byte("history refreshed file"), 0o600); err != nil {
		t.Fatal(err)
	}
	channel := &historyFileResolveChannel{filePath: filePath}
	plugin := NewFileParserPlugin(nil)
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Channel: channel,
		Event: MessageEvent{
			Kind:    EventKindGroup,
			GroupID: "123456",
			Segments: []MessageSegment{{Type: "file", Data: map[string]string{
				"name":    "report.txt",
				"file_id": "stale-file-id",
			}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || !strings.Contains(resp.Context, "history refreshed file") {
		t.Fatalf("resp = %#v", resp)
	}
	if !containsPluginTestString(channel.calls, "get_group_msg_history") {
		t.Fatalf("calls = %#v", channel.calls)
	}
}

func TestRefreshOneBotFilePaginatesOlderGroupHistory(t *testing.T) {
	channel := &pagedHistoryFileResolveChannel{}
	ref, ok := refreshOneBotFileFromGroupHistory(context.Background(), channel, fileRef{
		Name:    "older.pdf",
		FileID:  "stale-file-id",
		GroupID: "123456",
	})
	if !ok || ref.FileID != "fresh-file-id" {
		t.Fatalf("ref = %#v ok = %v", ref, ok)
	}
	if len(channel.params) != 2 || channel.params[1]["message_seq"] != "oldest-page-1" || channel.params[1]["reverse_order"] != true {
		t.Fatalf("params = %#v", channel.params)
	}
}

// TestFileParserPluginIgnoresUnsupportedURL 验证对应功能场景。
func TestFileParserPluginIgnoresUnsupportedURL(t *testing.T) {
	plugin := NewFileParserPlugin(nil)
	resp, err := plugin.Handle(context.Background(), PluginRequest{Text: "图片 https://example.com/a.png"})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp != nil {
		t.Fatalf("resp = %#v, want nil", resp)
	}
}

type fileResolveChannel struct {
	filePath string
	calls    []string
}

type historyFileResolveChannel struct {
	filePath string
	calls    []string
}

type pagedHistoryFileResolveChannel struct {
	params []map[string]any
}

func (c *pagedHistoryFileResolveChannel) Connect(context.Context, EventHandler) error { return nil }
func (c *pagedHistoryFileResolveChannel) Send(context.Context, OutgoingMessage) error { return nil }
func (c *pagedHistoryFileResolveChannel) Status() ChannelStatus                       { return ChannelStatus{} }
func (c *pagedHistoryFileResolveChannel) Close() error                                { return nil }
func (c *pagedHistoryFileResolveChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	if action != "get_group_msg_history" {
		return nil, errors.New("unexpected action")
	}
	c.params = append(c.params, params)
	if len(c.params) == 1 {
		return map[string]any{"messages": []any{map[string]any{
			"message_id": "oldest-page-1",
			"message":    []any{map[string]any{"type": "text", "data": map[string]any{"text": "newer"}}},
		}}}, nil
	}
	return map[string]any{"messages": []any{map[string]any{
		"message_id": "oldest-page-2",
		"message": []any{map[string]any{
			"type": "file",
			"data": map[string]any{"file": "older.pdf", "file_id": "fresh-file-id"},
		}},
	}}}, nil
}

func (c *historyFileResolveChannel) Connect(context.Context, EventHandler) error { return nil }
func (c *historyFileResolveChannel) Send(context.Context, OutgoingMessage) error { return nil }
func (c *historyFileResolveChannel) Status() ChannelStatus                       { return ChannelStatus{} }
func (c *historyFileResolveChannel) Close() error                                { return nil }
func (c *historyFileResolveChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	c.calls = append(c.calls, action)
	switch action {
	case "get_group_msg_history":
		return map[string]any{"messages": []any{map[string]any{
			"message": []any{map[string]any{
				"type": "file",
				"data": map[string]any{"file": "report.txt", "file_id": "fresh-file-id"},
			}},
		}}}, nil
	case "get_file":
		if params["file_id"] == "fresh-file-id" || params["file"] == "fresh-file-id" {
			return map[string]any{"file": c.filePath}, nil
		}
	}
	return nil, errors.New("not found")
}

func containsPluginTestString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func (c *fileResolveChannel) Connect(context.Context, EventHandler) error { return nil }
func (c *fileResolveChannel) Send(context.Context, OutgoingMessage) error { return nil }
func (c *fileResolveChannel) Status() ChannelStatus                       { return ChannelStatus{} }
func (c *fileResolveChannel) Close() error                                { return nil }
func (c *fileResolveChannel) CallAPI(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	c.calls = append(c.calls, action)
	return map[string]any{"file": c.filePath}, nil
}

// 聊天里换模型改的是机器人的对话模型分配，不是 LLM provider 配置。
func TestDianaLLMConfigToolRebindsChatModelRole(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "main", Name: "主配置", Group: "default", Config: llm.ProviderConfig{
				Provider: llm.ProviderOpenAICompatible, APIKey: "valid-key", Model: "example-chat-model",
				Models: []llm.ModelInfo{{ID: "example-chat-model"}, {ID: "example-pro-model"}},
			}},
		},
	}}
	logs := &captureAppLogs{}
	saver := &restoredConfigSaver{}
	runtime := NewRuntime(BotConfig{
		OwnerID:    "10001",
		ModelRoles: map[string]ModelRole{"chat": {ProfileID: "main", Model: "example-chat-model"}},
	}, nilChannel{}, NewPluginManager(), store, nil, saver, nil)
	runtime.SetAppLogWriter(logs)
	runtime.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "example-chat-model"}, {ID: "example-pro-model", MaxOutputTokens: 8192}}, nil
	})

	output, err := newDianaLLMConfigTool(runtime, MessageEvent{Kind: EventKindGroup, UserID: "10001", GroupID: "20002"}).Run(
		context.Background(), map[string]any{"operation": "update", "model": "example-pro-model"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(output, "已把对话模型换成 example-pro-model") {
		t.Fatalf("output = %q", output)
	}
	// 这才是真正决定机器人用什么模型的地方。
	roles := normalizeModelRoles(runtime.Config().ModelRoles)
	if roles["chat"].Model != "example-pro-model" || roles["chat"].ProfileID != "main" {
		t.Fatalf("chat role = %#v", roles["chat"])
	}
	if saver.calls != 1 {
		t.Fatalf("机器人配置没有落盘，重启就丢了: calls=%d", saver.calls)
	}
	// LLM provider 配置归 WebUI，聊天里不动它。
	if got := store.Current(); got.Model != "example-chat-model" {
		t.Fatalf("provider config was rewritten: %#v", got)
	}
	// 换完之后实际生效的就是新模型——旧实现在这里仍然是旧模型。
	profiles, err := runtime.roleBoundProfiles("", store.Profiles().WithDefaults(), llm.GroupChat)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Config.Model != "example-pro-model" {
		t.Fatalf("对话实际使用的模型没有跟着换: %#v", profiles)
	}
	if len(logs.entries) != 1 {
		t.Fatalf("logs = %#v", logs.entries)
	}
	if logs.entries[0].Kind != applog.KindOperation || logs.entries[0].Actor != "qq:10001" {
		t.Fatalf("log entry = %#v", logs.entries[0])
	}
	if logs.entries[0].Metadata["group_id"] != "20002" || logs.entries[0].Metadata["new_model"] != "example-pro-model" {
		t.Fatalf("log metadata = %#v", logs.entries[0].Metadata)
	}
}

// 切到一个 WebUI 里根本没配过的 provider 时要明确拒绝：provider 不是一个枚举值，
// 换家就得换地址和密钥，把当前配置的 provider 字段改掉只会得到一套连不上的配置。
func TestDianaLLMConfigToolRefusesUnconfiguredProvider(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{{ID: "main", Name: "主配置", Config: llm.ProviderConfig{
			Provider: llm.ProviderOpenAICompatible, APIKey: "valid-key", Model: "example-chat-model",
		}}},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), store, nil, &restoredConfigSaver{}, nil)
	runtime.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "gemini-2.5-pro"}}, nil
	})
	_, err := newDianaLLMConfigTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"}).Run(
		context.Background(), map[string]any{"operation": "update", "provider": "gemini", "model": "gemini-2.5-pro"})
	if err == nil || !strings.Contains(err.Error(), "没有配置 gemini 的 provider") {
		t.Fatalf("err = %v", err)
	}
	if got := store.Current(); got.Provider != llm.ProviderOpenAICompatible || got.Model != "example-chat-model" {
		t.Fatalf("被拒绝的切换不该改动任何配置: %#v", got)
	}
}

// 只报模型名时，如果它挂在另一套配置下，绑定要跟着换过去。
func TestDianaLLMConfigToolFollowsModelToItsProfile(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{
			{ID: "main", Name: "主配置", Config: llm.ProviderConfig{
				Provider: llm.ProviderOpenAICompatible, APIKey: "k1", Model: "chat-model",
				Models: []llm.ModelInfo{{ID: "chat-model"}},
			}},
			{ID: "claude", Name: "Claude 配置", Config: llm.ProviderConfig{
				Provider: llm.ProviderAnthropic, APIKey: "k2", Model: "claude-sonnet",
				Models: []llm.ModelInfo{{ID: "claude-sonnet"}},
			}},
		},
	}}
	runtime := NewRuntime(BotConfig{
		OwnerID:    "10001",
		ModelRoles: map[string]ModelRole{"chat": {ProfileID: "main", Model: "chat-model"}},
	}, nilChannel{}, NewPluginManager(), store, nil, &restoredConfigSaver{}, nil)
	runtime.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "claude-sonnet"}}, nil
	})
	if _, err := newDianaLLMConfigTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"}).Run(
		context.Background(), map[string]any{"operation": "update", "model": "claude-sonnet"}); err != nil {
		t.Fatal(err)
	}
	roles := normalizeModelRoles(runtime.Config().ModelRoles)
	if roles["chat"].ProfileID != "claude" || roles["chat"].Model != "claude-sonnet" {
		t.Fatalf("chat role = %#v", roles["chat"])
	}
}

// TestDianaLLMConfigToolUpdatesModelOnly verifies a model-only structured update.
func TestDianaLLMConfigToolUpdatesModelOnly(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			Profiles: []llm.Profile{
				{
					ID:   "main",
					Name: "主配置",
					Config: llm.ProviderConfig{
						Provider: llm.ProviderOpenAICompatible,
						APIKey:   "valid-key",
						Model:    "example-chat-model",
					},
				},
			},
		},
	}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), store, nil, &restoredConfigSaver{}, nil)
	runtime.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "example-chat-model"}, {ID: "gpt-4.1-mini"}}, nil
	})
	output, err := newDianaLLMConfigTool(runtime, MessageEvent{UserID: "10001"}).Run(context.Background(), map[string]any{"model": "gpt-4.1-mini"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(output, "gpt-4.1-mini") {
		t.Fatalf("output = %q", output)
	}
	// 换模型写的是机器人的模型分配，provider 配置一个字不动。
	if roles := normalizeModelRoles(runtime.Config().ModelRoles); roles["chat"].Model != "gpt-4.1-mini" {
		t.Fatalf("chat role = %#v", roles["chat"])
	}
	if got := store.Current(); got.Model != "example-chat-model" {
		t.Fatalf("provider config was rewritten: %#v", got)
	}
	// 第一次给对话定绑定会连带影响未分配的用途，回执要说出来。
	if !strings.Contains(output, "会跟随对话模型") {
		t.Fatalf("output 缺少连带影响说明: %q", output)
	}
}

// TestDianaLLMConfigToolRejectsModelOutsideList verifies backend model validation.
func TestDianaLLMConfigToolRejectsModelOutsideList(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			Profiles: []llm.Profile{
				{
					ID:   "main",
					Name: "主配置",
					Config: llm.ProviderConfig{
						Provider: llm.ProviderOpenAICompatible,
						APIKey:   "valid-key",
						Model:    "example-chat-model",
					},
				},
			},
		},
	}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "example-chat-model"}}, nil
	})
	_, err := newDianaLLMConfigTool(runtime, MessageEvent{UserID: "10001"}).Run(context.Background(), map[string]any{"model": "gemini-9-ultra"})
	if err == nil || !strings.Contains(err.Error(), "不在") {
		t.Fatalf("error = %v", err)
	}
	if got := store.Current(); got.Provider != llm.ProviderOpenAICompatible || got.Model != "example-chat-model" {
		t.Fatalf("current = %#v", got)
	}
}

// TestDianaLLMConfigToolRejectsNonOwner verifies the owner boundary.
func TestDianaLLMConfigToolRejectsNonOwner(t *testing.T) {
	store := &stubLLMProfileStore{
		set: llm.ProfileSet{
			Profiles: []llm.Profile{
				{ID: "main", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "valid-key", Model: "example-chat-model"}},
			},
		},
	}
	logs := &captureAppLogs{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), store, nil, nil, nil)
	runtime.SetAppLogWriter(logs)
	_, err := newDianaLLMConfigTool(runtime, MessageEvent{UserID: "20002"}).Run(context.Background(), map[string]any{"model": "gpt-4.1-mini"})
	if err == nil || !strings.Contains(err.Error(), "只有主人") {
		t.Fatalf("error = %v", err)
	}
	if got := store.Current(); got.Model != "example-chat-model" {
		t.Fatalf("current = %#v", got)
	}
	if len(logs.entries) != 0 {
		t.Fatalf("logs = %#v", logs.entries)
	}
}

func TestPluginManagerUpdateSettingsValidatesAndClamps(t *testing.T) {
	manager := NewDefaultPluginManager()
	state, err := manager.UpdateSettings(resolverPluginID, map[string]any{
		"fetch_title": false,
		"max_links":   float64(50),
	})
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if got := state.Settings["fetch_title"]; got != false {
		t.Fatalf("fetch_title = %#v, want false", got)
	}
	if got := state.Settings["max_links"]; got != float64(20) {
		t.Fatalf("max_links = %#v, want 20", got)
	}

	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{"no_such_key": true}); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{"fetch_title": "yes"}); err == nil {
		t.Fatal("wrong type accepted")
	}
	if state, err := manager.UpdateSettings(resolverPluginID, map[string]any{"enabled_platforms": []any{"weibo", "douyin", "weibo"}}); err != nil {
		t.Fatalf("multi_select UpdateSettings() error = %v", err)
	} else if got, ok := state.Settings["enabled_platforms"].([]string); !ok || len(got) != 2 {
		t.Fatalf("enabled_platforms = %#v", state.Settings["enabled_platforms"])
	}
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{"enabled_platforms": []any{"douyin", "netflix"}}); err == nil {
		t.Fatal("unknown platform option accepted")
	}
	rules := []any{map[string]any{
		"platform": "youtube", "minimum_level": float64(25), "unknown_policy": LevelUnknownAllow,
		"owner_bypass": true, "mention_bypass": true, "enabled": true,
	}}
	if state, err := manager.UpdateSettings(resolverPluginID, map[string]any{resolverSettingPlatformLevelRules: rules}); err != nil {
		t.Fatalf("platform rules UpdateSettings() error = %v", err)
	} else if got, ok := state.Settings[resolverSettingPlatformLevelRules].([]map[string]any); !ok || len(got) != 1 {
		t.Fatalf("platform rules = %#v", state.Settings[resolverSettingPlatformLevelRules])
	}
	invalidRules := []any{map[string]any{"platform": "netflix", "minimum_level": 1, "unknown_policy": LevelUnknownDeny}}
	if _, err := manager.UpdateSettings(resolverPluginID, map[string]any{resolverSettingPlatformLevelRules: invalidRules}); err == nil {
		t.Fatal("invalid platform rule accepted")
	}
	if _, err := manager.UpdateSettings("missing", map[string]any{"a": 1}); err == nil {
		t.Fatal("missing plugin accepted")
	}
	state, err = manager.UpdateSettings(resolverPluginID, map[string]any{})
	if err != nil {
		t.Fatalf("UpdateSettings(reset) error = %v", err)
	}
	if len(state.Settings) != 0 {
		t.Fatalf("Settings after reset = %#v, want empty", state.Settings)
	}
}

func TestPluginManagerRestoreSanitizesSettings(t *testing.T) {
	manager := NewDefaultPluginManager()
	manager.Restore(map[string]PluginState{
		resolverPluginID: {
			Installed: true,
			Enabled:   true,
			Settings: map[string]any{
				"fetch_title":     false,
				"max_links":       float64(99),
				"removed_key":     "stale",
				"timeout_seconds": "slow",
			},
		},
	})
	state, ok := manager.Get(resolverPluginID)
	if !ok {
		t.Fatal("resolver plugin missing")
	}
	if got := state.Settings["fetch_title"]; got != false {
		t.Fatalf("fetch_title = %#v, want false", got)
	}
	if got := state.Settings["max_links"]; got != float64(20) {
		t.Fatalf("max_links = %#v, want 20", got)
	}
	if _, ok := state.Settings["removed_key"]; ok {
		t.Fatalf("removed_key survived: %#v", state.Settings)
	}
	if _, ok := state.Settings["timeout_seconds"]; ok {
		t.Fatalf("invalid timeout survived: %#v", state.Settings)
	}
}

func TestPluginManagerRunInjectsEffectiveSettings(t *testing.T) {
	probe := &settingsProbePlugin{}
	manager := NewPluginManager(probe)
	if _, err := manager.Install("probe"); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := manager.UpdateSettings("probe", map[string]any{"limit": float64(7)}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	manager.Run(context.Background(), PluginRequest{Text: "hi"})
	if got := probe.seen.Int("limit", -1); got != 7 {
		t.Fatalf("limit = %d, want 7", got)
	}
	if got := probe.seen.Bool("verbose", false); !got {
		t.Fatal("verbose default was not injected")
	}
}

func TestPluginManagerGroupSettingsOverrideGlobalAndInheritSecrets(t *testing.T) {
	probe := &settingsProbePlugin{}
	manager := NewPluginManager(probe)
	if _, err := manager.Install("probe"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateSettings("probe", map[string]any{"limit": float64(7), "token": "global-secret"}); err != nil {
		t.Fatal(err)
	}

	overrides, err := manager.ValidateGroupSettingOverrides(PluginSettingOverrides{
		"probe": {"limit": float64(9), "verbose": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.RunWithGroupOverrides(context.Background(), PluginRequest{Text: "hi"}, nil, overrides)
	if got := probe.seen.Int("limit", -1); got != 9 {
		t.Fatalf("group limit = %d, want 9", got)
	}
	if probe.seen.Bool("verbose", true) {
		t.Fatal("group boolean override was not injected")
	}
	if got := probe.seen.String("token", ""); got != "global-secret" {
		t.Fatalf("global secret = %q", got)
	}

	if _, err := manager.ValidateGroupSettingOverrides(PluginSettingOverrides{"probe": {"token": "group-secret"}}); err == nil {
		t.Fatal("group secret override was accepted")
	}
	if _, err := manager.ValidateGroupSettingOverrides(PluginSettingOverrides{"probe": {"unknown": true}}); err == nil {
		t.Fatal("unknown group setting was accepted")
	}
	sanitized := manager.SanitizeGroupSettingOverrides(PluginSettingOverrides{
		"probe": {"limit": float64(8), "token": "leak", "unknown": true},
	})
	if sanitized["probe"]["limit"] != float64(8) || sanitized["probe"]["token"] != nil || sanitized["probe"]["unknown"] != nil {
		t.Fatalf("sanitized overrides = %#v", sanitized)
	}
}

func TestResolverPluginRespectsSettings(t *testing.T) {
	requests := 0
	plugin := NewResolverPlugin(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("<title>页面</title>")),
		}, nil
	})})
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text: "两个链接 https://www.bilibili.com/video/BV1 和 https://github.com/foo/bar",
		Settings: SettingValues{
			"fetch_title":    false,
			"download_media": false,
			"max_links":      float64(1),
		},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled {
		t.Fatalf("resp = %#v", resp)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
	if !strings.Contains(resp.Context, "Bilibili") || strings.Contains(resp.Context, "GitHub") {
		t.Fatalf("Context = %q", resp.Context)
	}
}

func TestFileParserPluginRespectsMaxFileSize(t *testing.T) {
	large := strings.Repeat("a", 65*1024)
	plugin := NewFileParserPlugin(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Type", "text/plain")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(large)),
		}, nil
	})})
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text:     "看下 https://example.com/big.txt",
		Settings: SettingValues{"max_file_bytes": float64(64 * 1024)},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if resp == nil || !resp.Handled || !strings.Contains(resp.Context, "exceeds") {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestFileParserPluginPublishesVideoFormatLimit(t *testing.T) {
	plugin := NewFileParserPlugin(nil)
	manifest := plugin.Manifest()
	if manifest.Version != "0.3.2" {
		t.Fatalf("file parser version = %q", manifest.Version)
	}
	manager := NewPluginManager(plugin)
	_, settings, enabled := manager.PluginWithSettings(fileParserPluginID, nil)
	if !enabled {
		t.Fatal("file parser is not enabled")
	}
	if got := fileParserVideoMaxBytes(settings); got != 200*1024*1024 {
		t.Fatalf("default video file limit = %d", got)
	}
}

type testPlugin struct{}

type settingsProbePlugin struct {
	seen SettingValues
}

func (*settingsProbePlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:   "probe",
		Name: "Probe",
		Settings: []PluginSettingSpec{
			{Key: "limit", Label: "Limit", Type: PluginSettingTypeNumber, Default: 3, Min: settingRange(1), Max: settingRange(10)},
			{Key: "verbose", Label: "Verbose", Type: PluginSettingTypeBool, Default: true},
			{Key: "token", Label: "Token", Type: PluginSettingTypeString, Default: "", Secret: true},
		},
	}
}

func (p *settingsProbePlugin) Handle(_ context.Context, req PluginRequest) (*PluginResponse, error) {
	p.seen = req.Settings
	return &PluginResponse{Handled: true}, nil
}

type panicPlugin struct{}

type panicObserverPlugin struct{}

type captureAppLogs struct {
	mu      sync.Mutex
	entries []applog.Entry
}

// AppendLog 封装当前模块的 AppendLog 逻辑。
func (c *captureAppLogs) AppendLog(_ context.Context, entry applog.Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, entry)
	return nil
}

func (c *captureAppLogs) entriesSnapshot() []applog.Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]applog.Entry(nil), c.entries...)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip 封装当前模块的 RoundTrip 逻辑。
func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

// Manifest 返回插件清单信息。
func (testPlugin) Manifest() PluginManifest {
	return PluginManifest{ID: "test", Name: "Test"}
}

// Handle 处理当前插件请求。
func (testPlugin) Handle(_ context.Context, req PluginRequest) (*PluginResponse, error) {
	return &PluginResponse{Handled: true, Context: "ctx: " + req.Text}, nil
}

func (panicPlugin) Manifest() PluginManifest {
	return PluginManifest{ID: "panic", Name: "Panic"}
}

func (panicPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	panic("boom")
}

func (panicObserverPlugin) Manifest() PluginManifest {
	return PluginManifest{ID: "panic-observer", Name: "Panic Observer", BuiltIn: true}
}

func (panicObserverPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

func (panicObserverPlugin) Observe(context.Context, MessageEvent) MessageEvent {
	panic("observe boom")
}

// 内化的能力：插件页看不到，也没有任何路径能把它们关掉。
func TestInternalPluginIsAlwaysOnAndHidden(t *testing.T) {
	manager := NewDefaultPluginManager()
	internal := []string{messageHistoryPluginID, llmConfigPluginID, capabilityKnowledgePluginID}

	visible := map[string]bool{}
	for _, state := range manager.ListVisible() {
		visible[state.Manifest.ID] = true
	}
	listed := map[string]bool{}
	for _, state := range manager.List() {
		listed[state.Manifest.ID] = true
	}

	for _, id := range internal {
		if visible[id] {
			t.Errorf("%s 不该出现在面向用户的插件列表里", id)
		}
		// 内部消费方（能力索引、Agent 能力清单）仍然要看得到。
		if !listed[id] {
			t.Errorf("List 仍应包含 %s，否则能力索引会漏掉它", id)
		}
		if _, err := manager.SetEnabled(id, false); !errors.Is(err, ErrInternalPluginDisable) {
			t.Errorf("停用 %s 应被拒绝，实际错误：%v", id, err)
		}
		if !manager.Enabled(id) {
			t.Errorf("%s 停用失败后应保持启用", id)
		}
		// 群级覆盖也不能关掉它。
		if !manager.EnabledWithOverrides(id, map[string]bool{id: false}) {
			t.Errorf("群级覆盖不该关掉 %s", id)
		}
	}
}

// 未勾选的平台会整条跳过，这条路径以前完全静默：用户只看到「链接没反应」，
// 日志和调试追踪里什么都查不到。跳过必须写一条失败日志。
func TestResolverLogsDisabledPlatformSkip(t *testing.T) {
	logs := &captureAppLogs{}
	plugin := &ResolverPlugin{}
	text := "复制打开小红书 http://xhslink.com/a/8bV2Sd1cLkVfb"
	resp, err := plugin.Handle(context.Background(), PluginRequest{
		Text:    text,
		Event:   MessageEvent{Kind: EventKindGroup, RawMessage: text},
		AppLogs: logs,
		Settings: SettingValues{
			resolverSettingEnabledPlatforms: []any{"bilibili"},
			resolverSettingDownloadMedia:    true,
		},
	})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("expected the disabled platform to produce no response, got %+v", resp)
	}
	entries := logs.entriesSnapshot()
	found := false
	for _, entry := range entries {
		if strings.Contains(entry.Detail, "platform_disabled") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a platform_disabled log entry, got %+v", entries)
	}
}

// /m/ 既是直播短链也是普通笔记分享短链。跳转成功后必须以最终地址为准，
// 否则任何跳转到非笔记页的 /m/ 短链都会被误报成「暂不支持直播链接」。
func TestXiaohongshuUnresolvedStatus(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		pageURL string
		want    string
	}{
		{"live final url", "http://xhslink.com/m/abc", "https://www.xiaohongshu.com/livestream/dynpath/123", "live_link"},
		{"note share short link that landed elsewhere", "http://xhslink.com/m/abc", "https://www.xiaohongshu.com/user/profile/123", "unsupported_link: https://www.xiaohongshu.com/user/profile/123"},
		{"redirect never left the short domain", "http://xhslink.com/m/abc", "http://xhslink.com/m/abc", "live_link"},
		{"a-style short link", "http://xhslink.com/a/abc", "https://www.xiaohongshu.com", "unsupported_link: https://www.xiaohongshu.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := xiaohongshuUnresolvedStatus(tc.raw, tc.pageURL); got != tc.want {
				t.Fatalf("xiaohongshuUnresolvedStatus(%q, %q) = %q, want %q", tc.raw, tc.pageURL, got, tc.want)
			}
		})
	}
}

// 四个用途都能在聊天里改，而且只动自己那一档。
func TestDianaLLMConfigToolRebindsEveryModelRole(t *testing.T) {
	newRuntime := func() (*Runtime, *stubLLMProfileStore) {
		store := &stubLLMProfileStore{set: llm.ProfileSet{
			Profiles: []llm.Profile{
				{ID: "main", Name: "主配置", Group: "default", Config: llm.ProviderConfig{
					Provider: llm.ProviderOpenAICompatible, APIKey: "k", Model: "chat-model", ImageModel: "draw-model",
					Models: []llm.ModelInfo{{ID: "chat-model"}, {ID: "see-model"}, {ID: "route-model"}, {ID: "draw-model"}, {ID: "draw-model-2"}},
				}},
			},
		}}
		runtime := NewRuntime(BotConfig{
			OwnerID: "10001",
			ModelRoles: map[string]ModelRole{
				"chat":   {ProfileID: "main", Model: "chat-model"},
				"vision": {ProfileID: "main", Model: "see-model"},
			},
		}, nilChannel{}, NewPluginManager(), store, nil, &restoredConfigSaver{}, nil)
		runtime.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
			return []llm.ModelInfo{{ID: "chat-model"}, {ID: "see-model"}, {ID: "see-model-pro"}, {ID: "route-model"}, {ID: "draw-model-2"}}, nil
		})
		return runtime, store
	}

	cases := []struct {
		role      string
		model     string
		wantLabel string
	}{
		{role: "vision", model: "see-model-pro", wantLabel: "视觉理解"},
		{role: "intent", model: "route-model", wantLabel: "意图识别"},
		{role: "image", model: "draw-model-2", wantLabel: "图片生成"},
		{role: "chat", model: "route-model", wantLabel: "对话"},
	}
	for _, item := range cases {
		runtime, _ := newRuntime()
		output, err := newDianaLLMConfigTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"}).Run(
			context.Background(), map[string]any{"operation": "update", "role": item.role, "model": item.model})
		if err != nil {
			t.Fatalf("role %s: %v", item.role, err)
		}
		if !strings.Contains(output, "已把"+item.wantLabel+"模型换成 "+item.model) {
			t.Fatalf("role %s output = %q", item.role, output)
		}
		roles := normalizeModelRoles(runtime.Config().ModelRoles)
		if roles[item.role].Model != item.model {
			t.Fatalf("role %s = %#v", item.role, roles[item.role])
		}
		// 只动自己那一档。
		for _, other := range []struct{ key, model string }{{"chat", "chat-model"}, {"vision", "see-model"}} {
			if other.key == item.role {
				continue
			}
			if roles[other.key].Model != other.model {
				t.Fatalf("改 %s 时动了 %s：%#v", item.role, other.key, roles[other.key])
			}
		}
	}
}

// 改「视觉理解」只写自己那一档，不会顺手给对话也钉一个绑定。
func TestDianaLLMConfigToolBindsNonChatRoleWithoutTouchingChat(t *testing.T) {
	store := &stubLLMProfileStore{set: llm.ProfileSet{
		Profiles: []llm.Profile{{ID: "main", Name: "主配置", Config: llm.ProviderConfig{
			Provider: llm.ProviderOpenAICompatible, APIKey: "k", Model: "chat-model",
			Models: []llm.ModelInfo{{ID: "chat-model"}, {ID: "see-model"}},
		}}},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), store, nil, &restoredConfigSaver{}, nil)
	runtime.SetLLMModelLister(func(context.Context, llm.ProviderConfig) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{ID: "chat-model"}, {ID: "see-model"}}, nil
	})
	if _, err := newDianaLLMConfigTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10001"}).Run(
		context.Background(), map[string]any{"operation": "update", "role": "vision", "model": "see-model"}); err != nil {
		t.Fatal(err)
	}
	roles := normalizeModelRoles(runtime.Config().ModelRoles)
	if roles["vision"].Model != "see-model" {
		t.Fatalf("vision role = %#v", roles["vision"])
	}
	if _, pinned := roles["chat"]; pinned {
		t.Fatalf("不该顺手给对话钉一个绑定: %#v", roles)
	}
	// 对话仍然走激活配置的默认模型。
	if got := store.Current(); got.Model != "chat-model" {
		t.Fatalf("provider config changed: %#v", got)
	}
}

func TestBuiltinPluginsDeclarePlatformSupport(t *testing.T) {
	manager := NewDefaultPluginManager()
	states := manager.List()
	for _, state := range states {
		if state.Manifest.BuiltIn && len(state.Manifest.Platforms) == 0 {
			t.Fatalf("built-in plugin %s has no platform metadata", state.Manifest.ID)
		}
	}
	for _, id := range []string{oneBotV11PluginID, musicPluginID, voiceTTSPluginID} {
		state, ok := manager.Get(id)
		if !ok || len(state.Manifest.Platforms) != 1 || state.Manifest.Platforms[0] != PlatformOneBotV11 {
			t.Fatalf("%s platforms = %#v", id, state.Manifest.Platforms)
		}
	}
	resolver, ok := manager.Get(resolverPluginID)
	if !ok || !pluginSupportsPlatform(resolver.Manifest, PlatformTelegram) || !pluginSupportsPlatform(resolver.Manifest, PlatformOneBotV11) {
		t.Fatalf("resolver platforms = %#v", resolver.Manifest.Platforms)
	}
}

func TestAgentToolsAreFilteredByCurrentPlatform(t *testing.T) {
	manager := NewDefaultPluginManager()
	tools, err := manager.AgentToolsForPlatformWithGroupOverrides(PlatformTelegram, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	if names["diana.music"] || names["diana.tts"] || names[dianaOneBotV11ToolName] {
		t.Fatalf("OneBot-only tools exposed on Telegram: %#v", names)
	}
	if !names["diana.capabilities"] || !names["web_search.search"] {
		t.Fatalf("cross-platform tools missing on Telegram: %#v", names)
	}
}
