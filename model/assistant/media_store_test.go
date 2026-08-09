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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// pngBytes 造一张真实 PNG，DataURL 会按内容嗅探类型。
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	buf := &bytes.Buffer{}
	if err := png.Encode(buf, img); err != nil {
		t.Fatalf("生成 PNG 失败：%v", err)
	}
	return buf.Bytes()
}

func mediaStore(t *testing.T) *MediaStore {
	t.Helper()
	return NewMediaStore(t.TempDir())
}

func TestMediaStorePersistsAndReuses(t *testing.T) {
	body := pngBytes(t)
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	store := mediaStore(t)
	path, err := store.Fetch(context.Background(), server.URL+"/a.png")
	if err != nil {
		t.Fatalf("下载失败：%v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件未落盘：%v", err)
	}

	// 同一地址再取一次不该再发请求——历史上下文每轮都会重放同一批图片。
	again, err := store.Fetch(context.Background(), server.URL+"/a.png")
	if err != nil {
		t.Fatalf("二次取用失败：%v", err)
	}
	if again != path {
		t.Fatalf("同一地址应命中同一文件，%q vs %q", again, path)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("应只下载一次，实际 %d 次", got)
	}
}

func TestMediaStoreDataURLIsBase64Image(t *testing.T) {
	body := pngBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	store := mediaStore(t)
	path, err := store.Fetch(context.Background(), server.URL+"/x")
	if err != nil {
		t.Fatalf("下载失败：%v", err)
	}
	dataURL, err := store.DataURL(path)
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("应是 PNG 的 base64 data URL，实际前缀 %.40s", dataURL)
	}
	// data URL 的负载必须是原始图片字节；llm 侧按同样的格式解码。
	payload := strings.TrimPrefix(dataURL, "data:image/png;base64,")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("base64 负载无法解码：%v", err)
	}
	if !bytes.Equal(decoded, body) {
		t.Fatal("解码结果与原图不一致")
	}
}

// 非图片内容不该被当成图片交给模型。
func TestMediaStoreRejectsNonImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer server.Close()

	store := mediaStore(t)
	path, err := store.Fetch(context.Background(), server.URL+"/page")
	if err != nil {
		t.Fatalf("下载失败：%v", err)
	}
	if _, err := store.DataURL(path); err == nil {
		t.Fatal("非图片应报错")
	}
}

func TestMediaStoreEnforcesSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
	}))
	defer server.Close()

	store := mediaStore(t)
	store.SetLimits(1024, 0)
	if _, err := store.Fetch(context.Background(), server.URL+"/big"); err == nil {
		t.Fatal("超过单文件上限应报错")
	}
	// 超限的下载不该留下半截文件被后续当作缓存命中。
	entries, _ := os.ReadDir(store.Dir())
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".partial-") {
			t.Fatalf("失败的下载不该留下文件：%s", entry.Name())
		}
	}
}

func TestMediaStoreSurfacesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	store := mediaStore(t)
	_, err := store.Fetch(context.Background(), server.URL+"/denied")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("应透出 HTTP 状态码，实际：%v", err)
	}
}

func TestMediaStoreRejectsNonHTTPScheme(t *testing.T) {
	store := mediaStore(t)
	if _, err := store.Fetch(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("非 http(s) 地址应被拒绝")
	}
}

// 同一地址并发只应下载一次。
func TestMediaStoreDedupesConcurrentFetches(t *testing.T) {
	body := pngBytes(t)
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	store := mediaStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Fetch(context.Background(), server.URL+"/same"); err != nil {
				t.Errorf("并发取用失败：%v", err)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("并发下应只下载一次，实际 %d 次", got)
	}
}

// 持久化不等于无限增长：超过总量上限要淘汰最旧的。
func TestMediaStoreEvictsOverCap(t *testing.T) {
	body := bytes.Repeat([]byte("y"), 1000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	store := mediaStore(t)
	store.SetLimits(0, 2500) // 只放得下 2 个

	for _, name := range []string{"/1", "/2", "/3"} {
		if _, err := store.Fetch(context.Background(), server.URL+name); err != nil {
			t.Fatalf("下载 %s 失败：%v", name, err)
		}
		// 让 mtime 拉开，淘汰顺序才确定。
		time.Sleep(10 * time.Millisecond)
	}

	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		t.Fatalf("读取目录失败：%v", err)
	}
	var total int64
	kept := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".partial-") {
			continue
		}
		info, _ := entry.Info()
		total += info.Size()
		kept++
	}
	if total > 2500 {
		t.Fatalf("总量应被压到上限以内，实际 %d 字节", total)
	}
	if kept == 0 {
		t.Fatal("不该把文件全删光")
	}
	// 最后一个下载的必须还在。
	if _, err := os.Stat(store.pathFor(server.URL + "/3")); err != nil {
		t.Fatal("最近使用的文件不该被淘汰")
	}
}

func TestMediaStoreDefaultDir(t *testing.T) {
	if got := NewMediaStore("  ").Dir(); got != defaultMediaCacheDir {
		t.Fatalf("空目录应回落默认值，实际 %q", got)
	}
	custom := filepath.Join(t.TempDir(), "media")
	if got := NewMediaStore(custom).Dir(); got != custom {
		t.Fatalf("自定义目录未生效：%q", got)
	}
}

// 端到端确认：图片进 LLM 请求时已经是本地文件的 base64，
// 而不是聊天平台那个短时效地址。
func TestLLMMessageUsesPersistedImage(t *testing.T) {
	body := pngBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	store := mediaStore(t)
	rt := NewRuntime(BotConfig{BotQQ: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	rt.SetMediaStore(store)

	remote := server.URL + "/photo.png"
	event := MessageEvent{
		Kind:     EventKindPrivate,
		UserID:   "7",
		Segments: []MessageSegment{{Type: "image", Data: map[string]string{"url": remote}}},
	}
	resolve := func(url string) string { return rt.resolveImageForLLM(context.Background(), url) }
	msg := llmMessageFromEvent(event, "这是什么", "看看图", resolve)

	var imageParts int
	for _, part := range msg.Parts {
		if part.Type != "image_url" {
			continue
		}
		imageParts++
		if part.ImageURL == remote {
			t.Fatal("图片仍是原始地址，没有走本地持久化")
		}
		if !strings.HasPrefix(part.ImageURL, "data:image/") {
			t.Fatalf("应提交 base64 data URL，实际 %.40s", part.ImageURL)
		}
	}
	if imageParts != 1 {
		t.Fatalf("期望 1 个图片部件，实际 %d", imageParts)
	}
}

// 下载失败时回落原地址：识图退化好过整条消息丢掉图片。
func TestLLMMessageFallsBackWhenFetchFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	rt := NewRuntime(BotConfig{BotQQ: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	rt.SetMediaStore(mediaStore(t))

	remote := server.URL + "/gone.png"
	got := rt.resolveImageForLLM(context.Background(), remote)
	if got != remote {
		t.Fatalf("失败时应回落原地址，实际 %q", got)
	}
}

// 没配 MediaStore 时保持原行为，不影响既有部署。
func TestLLMMessageKeepsURLWithoutStore(t *testing.T) {
	rt := NewRuntime(BotConfig{BotQQ: "42"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	remote := "https://example.com/a.png"
	if got := rt.resolveImageForLLM(context.Background(), remote); got != remote {
		t.Fatalf("未配置存储时应原样返回，实际 %q", got)
	}
}

// 追问场景必须保住：先发图、再问「这是什么」，历史里那张图要能进请求。
// 同时单次请求的图片总数要有上限，否则 20 条上下文全是 base64 会撑爆请求。
func TestImagePartsCappedKeepingRecent(t *testing.T) {
	img := func(url string) llm.ContentPart {
		return llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: url}
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "旧图", Parts: []llm.ContentPart{img("old-1"), img("old-2")}},
		{Role: llm.RoleUser, Content: "近图", Parts: []llm.ContentPart{img("recent-1")}},
		{Role: llm.RoleUser, Content: "这是什么", Parts: []llm.ContentPart{
			{Type: llm.ContentPartText, Text: "这是什么"}, img("newest"),
		}},
	}
	capped := capImageParts(messages, 2)

	var urls []string
	for _, msg := range capped {
		for _, part := range msg.Parts {
			if part.Type == llm.ContentPartImageURL {
				urls = append(urls, part.ImageURL)
			}
		}
	}
	if len(urls) != 2 {
		t.Fatalf("应只保留 2 张图，实际 %v", urls)
	}
	// 保留的必须是最近的两张——用户问的就是刚发的那张。
	if urls[0] != "recent-1" || urls[1] != "newest" {
		t.Fatalf("应保留最近两张，实际 %v", urls)
	}
	// 图片被丢光的那条消息要退回纯文本，不留只有文本部件的空壳。
	if len(capped[1].Parts) != 0 {
		t.Fatalf("旧图消息应退回纯文本，实际 %#v", capped[1].Parts)
	}
	if capped[1].Content != "旧图" {
		t.Fatalf("退回纯文本时不该丢掉正文，实际 %q", capped[1].Content)
	}
	// 最后一条里的文本部件必须保留。
	if len(capped[3].Parts) != 2 {
		t.Fatalf("当前消息应保留文本+图片两个部件，实际 %#v", capped[3].Parts)
	}
}

// 未超过上限时原样返回。
func TestImagePartsUnderCapUnchanged(t *testing.T) {
	messages := []llm.Message{{
		Role:  llm.RoleUser,
		Parts: []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: "a"}},
	}}
	if got := capImageParts(messages, 4); len(got[0].Parts) != 1 {
		t.Fatalf("未超限不该改动，实际 %#v", got[0].Parts)
	}
}
