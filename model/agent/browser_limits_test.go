// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
	"github.com/gorilla/websocket"
)

// fakeCDP 是一个只会标签页增删和几条 Page/Runtime 命令的假浏览器。地址里带 "hang"
// 的页面，Page.navigate 永远不回，模拟服务器一直不响应；地址里带 "busy" 的页面跳过去
// 之后 Runtime.evaluate 永远不回，模拟页面主线程被脚本卡死。
type fakeCDP struct {
	server *httptest.Server

	mu      sync.Mutex
	next    int
	order   []string
	targets map[string]string // id → url
	stopped map[string]int
	// cookies 是 Network.getCookies 交回的 Cookie 值；evalValue 非空时，普通脚本
	// 的 Runtime.evaluate 交回它而不是默认的页面摘要。
	cookies   []string
	evalValue any
	// busy 是主线程卡死的标签页，crashed 是渲染进程崩了的标签页：两种都不回
	// Runtime.evaluate，导航回 about:blank 后恢复。terminable 为真时
	// Runtime.terminateExecution 能打断卡死（真 Chrome 里只有卡住之前就挂上的会话才行）。
	busy       map[string]bool
	crashed    map[string]bool
	terminable bool
	terminated int
}

func newFakeCDP(t *testing.T, urls ...string) *fakeCDP {
	t.Helper()
	f := &fakeCDP{targets: map[string]string{}, stopped: map[string]int{}, busy: map[string]bool{}, crashed: map[string]bool{}}
	for _, u := range urls {
		f.add(u)
	}
	upgrader := websocket.Upgrader{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/json/list":
			f.writeList(w)
		case r.URL.Path == "/json/new":
			pageURL, _ := url.QueryUnescape(r.URL.RawQuery)
			id := f.add(pageURL)
			_ = json.NewEncoder(w).Encode(f.target(id))
		case strings.HasPrefix(r.URL.Path, "/json/close/"):
			f.mu.Lock()
			id := strings.TrimPrefix(r.URL.Path, "/json/close/")
			delete(f.targets, id)
			f.order = slices.DeleteFunc(f.order, func(v string) bool { return v == id })
			f.mu.Unlock()
		case strings.HasPrefix(r.URL.Path, "/json/activate/"):
		case strings.HasPrefix(r.URL.Path, "/devtools/page/"):
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			f.serve(conn, strings.TrimPrefix(r.URL.Path, "/devtools/page/"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeCDP) add(pageURL string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("T%d", f.next)
	f.targets[id] = pageURL
	f.order = append(f.order, id)
	return id
}

func (f *fakeCDP) target(id string) browserTarget {
	f.mu.Lock()
	defer f.mu.Unlock()
	return browserTarget{
		ID: id, Type: "page", URL: f.targets[id],
		WebSocketDebuggerURL: "ws" + strings.TrimPrefix(f.server.URL, "http") + "/devtools/page/" + id,
	}
}

func (f *fakeCDP) writeList(w http.ResponseWriter) {
	f.mu.Lock()
	ids := append([]string(nil), f.order...)
	f.mu.Unlock()
	list := make([]browserTarget, 0, len(ids))
	for _, id := range ids {
		list = append(list, f.target(id))
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (f *fakeCDP) pages() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.targets)
}

func (f *fakeCDP) stops(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped[id]
}

func (f *fakeCDP) serve(conn *websocket.Conn, id string) {
	defer conn.Close()
	for {
		var msg struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		result := map[string]any{}
		switch msg.Method {
		case "Page.navigate":
			pageURL, _ := msg.Params["url"].(string)
			if strings.Contains(pageURL, "hang") {
				continue
			}
			f.mu.Lock()
			f.targets[id] = pageURL
			f.busy[id] = strings.Contains(pageURL, "busy")
			f.crashed[id] = false
			f.mu.Unlock()
		case "Inspector.enable":
			f.mu.Lock()
			crashed := f.crashed[id]
			f.mu.Unlock()
			if crashed {
				if err := conn.WriteJSON(map[string]any{"method": "Inspector.targetCrashed", "params": map[string]any{}}); err != nil {
					return
				}
			}
		case "Runtime.terminateExecution":
			f.mu.Lock()
			terminable := f.terminable
			if terminable {
				f.busy[id] = false
				f.terminated++
			}
			f.mu.Unlock()
			if !terminable {
				continue
			}
		case "Page.stopLoading":
			f.mu.Lock()
			f.stopped[id]++
			f.mu.Unlock()
		case "Runtime.evaluate":
			expr, _ := msg.Params["expression"].(string)
			f.mu.Lock()
			stuck := f.busy[id] || f.crashed[id]
			f.mu.Unlock()
			if stuck {
				continue
			}
			if expr == "1" {
				result["result"] = map[string]any{"type": "number", "value": 1}
			} else if strings.Contains(expr, "readyState") {
				result["result"] = map[string]any{"type": "boolean", "value": true}
			} else {
				f.mu.Lock()
				current := f.targets[id]
				value := f.evalValue
				f.mu.Unlock()
				if value == nil {
					value = map[string]any{"url": current, "title": id, "text": "page " + id}
				}
				result["result"] = map[string]any{"type": "object", "value": value}
			}
		case "Network.getCookies":
			f.mu.Lock()
			cookies := make([]map[string]any, 0, len(f.cookies))
			for index, value := range f.cookies {
				cookies = append(cookies, map[string]any{"name": fmt.Sprintf("c%d", index), "value": value})
			}
			f.mu.Unlock()
			result["cookies"] = cookies
		}
		if err := conn.WriteJSON(map[string]any{"id": msg.ID, "result": result}); err != nil {
			return
		}
	}
}

// browserToolsFor 登记一套连到假浏览器的工具，标签页记录用测试自己的一份。
func browserToolsFor(t *testing.T, f *fakeCDP, tabs *browserTabRegistry, key string, timeout time.Duration) *ToolRegistry {
	t.Helper()
	registry := NewToolRegistry()
	registry.RegisterBrowserTools(t.TempDir(), Config{BrowserCDPURL: f.server.URL, BrowserTimeoutMS: int(timeout / time.Millisecond)}.WithDefaults())
	for _, name := range InteractiveBrowserToolNames {
		tool, _ := registry.Get(name)
		base := browserToolBaseOf(tool)
		base.tabs = tabs
		base.session = tabs.session(key)
	}
	return registry
}

func browserToolBaseOf(tool Tool) *browserToolBase {
	switch v := tool.(type) {
	case *BrowserOpenTool:
		return &v.base
	case *BrowserTextTool:
		return &v.base
	case *BrowserClickTool:
		return &v.base
	case *BrowserTypeTool:
		return &v.base
	case *BrowserScreenshotTool:
		return &v.base
	case *BrowserTabsTool:
		return &v.base
	case *BrowserScrollTool:
		return &v.base
	case *BrowserPressKeyTool:
		return &v.base
	case *BrowserNavigateTool:
		return &v.base
	case *BrowserSelectTool:
		return &v.base
	case *BrowserWaitTool:
		return &v.base
	case *BrowserEvalTool:
		return &v.base
	}
	panic(fmt.Sprintf("unknown browser tool %T", tool))
}

func runBrowserTool(ctx context.Context, registry *ToolRegistry, name string, input map[string]any) (string, error) {
	tool, _ := registry.Get(name)
	return tool.Run(ctx, input)
}

func activeTab(registry *ToolRegistry) string {
	tool, _ := registry.Get("browser_open")
	return browserToolBaseOf(tool).session.active()
}

// 页面一直不响应时，browser_open 按浏览器超时收手，补发 Page.stopLoading，报清楚是超时。
func TestBrowserOpenTimesOutAndStopsLoading(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", 300*time.Millisecond)
	started := time.Now()
	_, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://hang.example/"})
	if err == nil || !strings.Contains(err.Error(), "超时") || !strings.Contains(err.Error(), "已停止加载") {
		t.Fatalf("应当报超时并说明已停止加载：%v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("超时没有按浏览器超时收手，用了 %s", elapsed)
	}
	if f.stops("T1") != 1 {
		t.Fatalf("超时后应当对那一页发 Page.stopLoading，实际 %d 次", f.stops("T1"))
	}
	// 连接挂掉的那一页之后照样能用。
	if out, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://example.com/next"}); err != nil || !strings.Contains(out, "example.com/next") {
		t.Fatalf("超时之后同一页应当还能继续用：%s %v", out, err)
	}
}

// 走一遍 Runner：模型拿到的是浏览器自己的超时原因，不是「工具执行超时（上限 60000ms）」。
func TestBrowserTimeoutReachesModelThroughRunner(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", 300*time.Millisecond)
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"browser_open","input":{"url":"https://hang.example/"}}`,
		`{"action":"final","content":"打不开"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 1, ToolTimeoutMS: 60_000}, registry)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "打开"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Steps) != 1 || !strings.Contains(resp.Steps[0].Error, "打开 https://hang.example/ 超时") || strings.Contains(resp.Steps[0].Error, "工具执行超时") {
		t.Fatalf("模型应当拿到浏览器自己的超时原因：%#v", resp.Steps)
	}
}

// 工具调用被取消时同步中止加载，不等满浏览器超时。
func TestBrowserOpenCancelStopsLoading(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	started := time.Now()
	_, err := runBrowserTool(ctx, registry, "browser_open", map[string]any{"url": "https://hang.example/"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应当原样交回 context.Canceled：%v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("取消之后还在等，用了 %s", elapsed)
	}
	if f.stops("T1") != 1 {
		t.Fatalf("取消后应当发 Page.stopLoading，实际 %d 次", f.stops("T1"))
	}
}

// 新开的标签页加载超时就关掉，不在后台留一个卡住的页。
func TestBrowserNewTabTimeoutClosesTheTab(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	tabs := newBrowserTabRegistry()
	registry := browserToolsFor(t, f, tabs, "bot\x00group:1", 300*time.Millisecond)
	for _, call := range []map[string]any{
		{"tool": "browser_open", "url": "https://hang.example/", "new_tab": true},
		{"tool": "browser_tabs", "action": "new", "url": "https://hang.example/"},
	} {
		name := call["tool"].(string)
		if _, err := runBrowserTool(context.Background(), registry, name, call); err == nil || !strings.Contains(err.Error(), "超时") {
			t.Fatalf("%s 应当报超时：%v", name, err)
		}
		if f.pages() != 1 {
			t.Fatalf("%s 超时的新标签页应当被关掉，还剩 %d 页", name, f.pages())
		}
		if activeTab(registry) != "" {
			t.Fatalf("%s 关掉的页不该还记成当前页", name)
		}
	}
	if len(tabs.opened) != 0 {
		t.Fatalf("关掉的页不该再占名额：%v", tabs.opened)
	}
}

// 两个对话同时用同一个浏览器时各用各的标签页；同一个对话的下一轮接着用上一轮的页。
func TestBrowserSessionsDoNotShareTabs(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	tabs := newBrowserTabRegistry()
	groupA := browserToolsFor(t, f, tabs, "bot\x00group:A", time.Second)
	groupB := browserToolsFor(t, f, tabs, "bot\x00group:B", time.Second)
	ctx := context.Background()

	if _, err := runBrowserTool(ctx, groupA, "browser_open", map[string]any{"url": "https://a.example/"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runBrowserTool(ctx, groupB, "browser_open", map[string]any{"url": "https://b.example/"}); err != nil {
		t.Fatal(err)
	}
	a, b := activeTab(groupA), activeTab(groupB)
	if a == "" || b == "" || a == b {
		t.Fatalf("两个群应当各用各的标签页：A=%q B=%q", a, b)
	}
	if out, _ := runBrowserTool(ctx, groupA, "browser_text", nil); !strings.Contains(out, "a.example") {
		t.Fatalf("A 群读到的应当是自己的页：%s", out)
	}

	// 同一个群的下一轮建的是新工具表，但接着用上一轮那一页。
	nextTurn := browserToolsFor(t, f, tabs, "bot\x00group:A", time.Second)
	if out, _ := runBrowserTool(ctx, nextTurn, "browser_text", nil); !strings.Contains(out, "a.example") {
		t.Fatalf("同一个对话的下一轮应当接着用原来那一页：%s", out)
	}

	// 占着的页闲置超过租期之后，别的对话才能接手。
	tabs.now = func() time.Time { return time.Now().Add(browserTabLeaseIdle + time.Minute) }
	if tabs.heldByOther(a, tabs.session("bot\x00group:C")) {
		t.Fatal("闲置超过租期的页不该再算被占着")
	}
}

// 机器人开的标签页有上限：满了先回收没人在用的闲置页，都有人在用就明确拒绝。
func TestBrowserTabLimitReclaimsIdleThenRefuses(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	tabs := newBrowserTabRegistry()
	clock := time.Now()
	tabs.now = func() time.Time { return clock }
	ctx := context.Background()
	for i := 0; i < maxAgentBrowserTabs; i++ {
		registry := browserToolsFor(t, f, tabs, fmt.Sprintf("bot\x00group:%d", i), time.Second)
		if _, err := runBrowserTool(ctx, registry, "browser_tabs", map[string]any{"action": "new"}); err != nil {
			t.Fatalf("第 %d 个标签页：%v", i+1, err)
		}
		clock = clock.Add(time.Second)
	}
	pages := f.pages()

	late := browserToolsFor(t, f, tabs, "bot\x00group:late", time.Second)
	_, err := runBrowserTool(ctx, late, "browser_open", map[string]any{"url": "https://late.example/", "new_tab": true})
	if err == nil || !strings.Contains(err.Error(), "上限") || !strings.Contains(err.Error(), "browser_tabs") {
		t.Fatalf("都有对话在用时应当明确拒绝并指路：%v", err)
	}
	if f.pages() != pages {
		t.Fatalf("拒绝时不该多开或误关标签页：%d → %d", pages, f.pages())
	}

	// 过了租期，最早那一页没人占着了，新开时先把它回收掉。
	clock = clock.Add(browserTabLeaseIdle)
	if _, err := runBrowserTool(ctx, late, "browser_open", map[string]any{"url": "https://late.example/", "new_tab": true}); err != nil {
		t.Fatalf("有闲置页可回收时应当能新开：%v", err)
	}
	if f.pages() != pages {
		t.Fatalf("回收一页、新开一页，总数应当不变：%d → %d", pages, f.pages())
	}
	if _, ok := f.targets["T2"]; ok {
		t.Fatal("最久没用的那一页应当先被回收")
	}
	if _, ok := f.targets["T1"]; !ok {
		t.Fatal("不是机器人开的页不能被回收")
	}
}

// 一次性浏览器同时在跑的数量有上限：满了排队，排不上报忙，取消立刻返回。
func TestDisposableBrowserSlots(t *testing.T) {
	slots := newBrowserSlots(1, 100*time.Millisecond)
	release, err := slots.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slots.acquire(context.Background()); !errors.Is(err, ErrBrowserBusy) || !strings.Contains(err.Error(), "上限（1 个）") {
		t.Fatalf("满了应当报忙：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := slots.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应当立刻返回：%v", err)
	}
	// 排队中有人释放就轮到。
	time.AfterFunc(20*time.Millisecond, release)
	next, err := slots.acquire(context.Background())
	if err != nil {
		t.Fatalf("释放之后应当轮到：%v", err)
	}
	next()
	next() // 重复释放不能多还一个名额。
	if len(slots.slots) != 0 {
		t.Fatalf("名额没还干净：%d", len(slots.slots))
	}
}

// 网页渲染和截图都占同一份名额，满了不起进程、直接报忙。
func TestDisposableBrowserLimitCoversRenderAndScreenshot(t *testing.T) {
	previous := disposableBrowsers
	disposableBrowsers = newBrowserSlots(1, 50*time.Millisecond)
	t.Cleanup(func() { disposableBrowsers = previous })
	release, err := disposableBrowsers.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// 可执行文件指向一个肯定存在、但不是浏览器的文件：真起进程的话就不是「忙」这个错。
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	renderer := NewSandboxedHeadlessBrowser(SandboxedBrowserConfig{Executable: executable, Timeout: 5 * time.Second})
	if _, err := renderer.Render(context.Background(), "http://1.1.1.1/"); !errors.Is(err, ErrBrowserBusy) {
		t.Fatalf("渲染应当排队后报忙：%v", err)
	}
	if _, err := CaptureHTMLScreenshot(context.Background(), ScreenshotRequest{HTML: "<p>x</p>", Executable: executable}); !errors.Is(err, ErrBrowserBusy) {
		t.Fatalf("截图应当排队后报忙：%v", err)
	}
}

// browser_render 群成员也能触发，每次都是一份全新的临时 profile：和别的调用、和主人的
// 常驻浏览器都不共用登录态。这里钉住「每次一份、用完就删」。
func TestDisposableRenderProfilesAreNeverShared(t *testing.T) {
	first, err := newBrowserSandboxDirs("diana-headless-browser-")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newBrowserSandboxDirs("diana-headless-browser-")
	if err != nil {
		t.Fatal(err)
	}
	defer first.remove()
	defer second.remove()
	if first.profile == second.profile {
		t.Fatal("两次渲染不能共用 profile")
	}
	args := sandboxedChromeArgs(first.profile, first.cache, first.crash, SandboxedBrowserConfig{})
	var dataDirs []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "--user-data-dir=") {
			dataDirs = append(dataDirs, strings.TrimPrefix(arg, "--user-data-dir="))
		}
	}
	if len(dataDirs) != 1 || dataDirs[0] != first.profile {
		t.Fatalf("一次性渲染只能用自己的临时 profile：%v", dataDirs)
	}
	if !strings.HasPrefix(first.profile, os.TempDir()) || strings.Contains(first.profile, "browser-box") {
		t.Fatalf("临时 profile 不该落在内置浏览器的数据目录：%s", first.profile)
	}
}
