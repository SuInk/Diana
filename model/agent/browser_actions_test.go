// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
	"github.com/gorilla/websocket"
)

func TestParseBrowserKey(t *testing.T) {
	cases := []struct {
		spec      string
		key, code string
		keyCode   int
		modifiers int
	}{
		{"Enter", "Enter", "Enter", 13, 0},
		{"pagedown", "PageDown", "PageDown", 34, 0},
		{"a", "a", "KeyA", 65, 0},
		{"Control+A", "A", "KeyA", 65, 2},
		{"Shift+Tab", "Tab", "Tab", 9, 8},
		{"Meta+Shift+z", "z", "KeyZ", 90, 12},
		{"7", "7", "Digit7", 55, 0},
		{"Control++", "+", "", 0, 2},
	}
	for _, tc := range cases {
		key, modifiers, err := parseBrowserKey(tc.spec)
		if err != nil {
			t.Fatalf("%s: %v", tc.spec, err)
		}
		if key.key != tc.key || key.code != tc.code || key.keyCode != tc.keyCode || modifiers != tc.modifiers {
			t.Fatalf("%s: got key=%q code=%q keyCode=%d modifiers=%d", tc.spec, key.key, key.code, key.keyCode, modifiers)
		}
	}
	for _, bad := range []string{"", "Hyper+A", "NotAKey"} {
		if _, _, err := parseBrowserKey(bad); err == nil {
			t.Fatalf("%q 应当报错", bad)
		}
	}
}

// 常驻浏览器只有主人能驱动，不屏蔽本机和内网地址；一次性渲染群成员也能触发，照旧屏蔽。
func TestPersistentBrowserReachesLocalHostsButRenderDoesNot(t *testing.T) {
	persistent := PersistentBrowserArgs("/tmp/p", "/tmp/c", "/tmp/x", true, 0, 1280, 800)
	for _, arg := range persistent {
		if strings.HasPrefix(arg, "--host-resolver-rules=") {
			t.Fatalf("常驻浏览器不该屏蔽本机地址：%s", arg)
		}
	}
	// 其余加固参数一条不少。
	for _, want := range []string{"--disable-sync", "--no-pings", "--user-data-dir=/tmp/p", "--disable-blink-features=AutomationControlled"} {
		if !slices.Contains(persistent, want) {
			t.Fatalf("常驻浏览器丢了加固参数 %q", want)
		}
	}
	render := sandboxedChromeArgs("/tmp/p", "/tmp/c", "/tmp/x", SandboxedBrowserConfig{})
	if !slices.ContainsFunc(render, func(arg string) bool { return strings.HasPrefix(arg, "--host-resolver-rules=") }) {
		t.Fatal("一次性渲染必须继续屏蔽本机地址")
	}
}

func TestBrowserToolsAreRepeatable(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterBrowserTools(t.TempDir(), Config{}.WithDefaults())
	for _, name := range InteractiveBrowserToolNames {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("%s 没有登记", name)
		}
		if name == "browser_select" {
			continue
		}
		if repeatable, ok := tool.(RepeatableTool); !ok || !repeatable.RepeatableCalls() {
			t.Fatalf("%s 连续两次相同调用会被 Runner 当成重复跳过", name)
		}
	}
}

// TestBrowserToolsIntegration 用本机 Chrome 实跑一遍整组工具。设
// DIANA_BROWSER_TOOLS_INTEGRATION=1 才跑。
func TestBrowserToolsIntegration(t *testing.T) {
	if os.Getenv("DIANA_BROWSER_TOOLS_INTEGRATION") != "1" {
		t.Skip("set DIANA_BROWSER_TOOLS_INTEGRATION=1 to run Chrome integration")
	}
	executable, err := FindBrowserExecutable("")
	if err != nil {
		t.Skip(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/second" {
			fmt.Fprint(w, `<title>second</title><p>第二页</p>`)
			return
		}
		fmt.Fprint(w, `<title>first</title>
<input id="name">
<p id="mirror"></p>
<button id="btn">按钮</button>
<p id="clicks">0</p>
<select id="fruit"><option value="a">苹果</option><option value="b">香蕉</option></select>
<p id="keys"></p>
<a id="next" href="/second">下一页</a>
<div style="height:4000px"></div>
<p id="bottom">到底了</p>
<script>
let trusted = 0;
document.getElementById("btn").addEventListener("click", e => { if (e.isTrusted) trusted++; document.getElementById("clicks").textContent = trusted; });
document.getElementById("name").addEventListener("input", e => { document.getElementById("mirror").textContent = e.target.value; });
document.addEventListener("keydown", e => { if (e.isTrusted) document.getElementById("keys").textContent += e.key + ","; });
setTimeout(() => { const p = document.createElement("p"); p.id = "late"; p.textContent = "晚到的元素"; document.body.appendChild(p); }, 800);
</script>`)
	}))
	defer server.Close()
	// 用 localhost 而不是 127.0.0.1 访问，顺带验证常驻浏览器不再屏蔽本机域名。
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	pageURL := "http://localhost:" + port + "/"

	root := t.TempDir()
	cdpURL := startIntegrationChrome(t, executable, root)

	registry := NewToolRegistry()
	registry.RegisterBrowserTools(root, Config{BrowserCDPURL: cdpURL}.WithDefaults())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := func(name string, input map[string]any) string {
		t.Helper()
		tool, _ := registry.Get(name)
		out, err := tool.Run(ctx, input)
		if err != nil {
			t.Fatalf("%s(%v): %v", name, input, err)
		}
		return out
	}
	eval := func(script string) string {
		t.Helper()
		var value string
		raw := run("browser_eval", map[string]any{"script": script})
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatalf("eval %s: %s", script, raw)
		}
		return value
	}

	if out := run("browser_open", map[string]any{"url": pageURL}); !strings.Contains(out, "first") {
		t.Fatalf("open: %s", out)
	}
	run("browser_type", map[string]any{"selector": "#name", "text": "嘉然"})
	if got := eval(`document.getElementById("mirror").textContent`); got != "嘉然" {
		t.Fatalf("输入没有触发 input 事件：%s", got)
	}
	run("browser_click", map[string]any{"selector": "#btn"})
	run("browser_click", map[string]any{"selector": "#btn"})
	if got := eval(`document.getElementById("clicks").textContent`); got != "2" {
		t.Fatalf("点击不是真实事件或连点被跳过：%s", got)
	}
	run("browser_select", map[string]any{"selector": "#fruit", "label": "香蕉"})
	if got := eval(`document.getElementById("fruit").value`); got != "b" {
		t.Fatalf("select: %s", got)
	}
	run("browser_press_key", map[string]any{"key": "Escape", "repeat": 2})
	if got := eval(`document.getElementById("keys").textContent`); got != "Escape,Escape," {
		t.Fatalf("按键：%s", got)
	}
	var scrolled struct {
		AtBottom bool `json:"at_bottom"`
	}
	_ = json.Unmarshal([]byte(run("browser_scroll", map[string]any{"direction": "bottom"})), &scrolled)
	if !scrolled.AtBottom {
		t.Fatal("没有滚到底")
	}
	if out := run("browser_wait", map[string]any{"selector": "#late", "timeout_ms": 5000}); !strings.Contains(out, `"found":true`) {
		t.Fatalf("wait: %s", out)
	}
	if _, err := registry.tools["browser_eval"].Run(ctx, map[string]any{"script": `(() => { throw new Error("boom") })()`}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("脚本异常应当交回错误：%v", err)
	}

	shot := registry.tools["browser_screenshot"].(*BrowserScreenshotTool)
	out := run("browser_screenshot", nil)
	parts := shot.ToolResultParts(out)
	if len(parts) != 1 || parts[0].Type != llm.ContentPartImageURL || !strings.HasPrefix(parts[0].ImageURL, "data:image/png;base64,") {
		t.Fatalf("截图没有交给模型：%+v", parts)
	}

	run("browser_click", map[string]any{"selector": "#next"})
	if out := run("browser_text", nil); !strings.Contains(out, "第二页") {
		t.Fatalf("点链接没有跳过去：%s", out)
	}
	if out := run("browser_navigate", map[string]any{"action": "back"}); !strings.Contains(out, "first") {
		t.Fatalf("back: %s", out)
	}

	// 默认沿用当前标签页；new_tab 才多开一页，之后的工具作用在新页上。
	var listed struct {
		Tabs []struct {
			ID     string `json:"tab_id"`
			URL    string `json:"url"`
			Active bool   `json:"active"`
		} `json:"tabs"`
	}
	countPages := func() int {
		listed.Tabs = nil
		_ = json.Unmarshal([]byte(run("browser_tabs", map[string]any{"action": "list"})), &listed)
		return len(listed.Tabs)
	}
	before := countPages()
	run("browser_open", map[string]any{"url": pageURL + "second"})
	if after := countPages(); after != before {
		t.Fatalf("默认应当沿用当前标签页：%d → %d", before, after)
	}
	run("browser_open", map[string]any{"url": pageURL, "new_tab": true})
	if after := countPages(); after != before+1 {
		t.Fatalf("new_tab 应当多开一页：%d → %d", before, after)
	}
	if out := run("browser_text", nil); !strings.Contains(out, "first") {
		t.Fatalf("新开的页应当成为当前页：%s", out)
	}
	var second string
	for _, tab := range listed.Tabs {
		if strings.HasSuffix(tab.URL, "/second") {
			second = tab.ID
		}
	}
	run("browser_tabs", map[string]any{"action": "switch", "tab_id": second})
	if out := run("browser_text", nil); !strings.Contains(out, "第二页") {
		t.Fatalf("切换之后应当读切过去的页：%s", out)
	}
	run("browser_tabs", map[string]any{"action": "close"})
	if after := countPages(); after != before {
		t.Fatalf("close 之后应当少一页：%d", after)
	}
}

// startIntegrationChrome 按常驻浏览器的参数起一个 Chrome，返回调试地址。
func startIntegrationChrome(t *testing.T, executable, root string) string {
	t.Helper()
	args := PersistentBrowserArgs(root+"/profile", root+"/cache", root+"/crash", true, 0, 1280, 800)
	cmd := exec.Command(executable, append(args, "about:blank")...)
	cmd.Env = BrowserLaunchEnvironment(os.Environ(), root)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	devtools := make(chan string, 1)
	go func() {
		pattern := regexp.MustCompile(`DevTools listening on ws://([^/]+)/`)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			if match := pattern.FindStringSubmatch(scanner.Text()); match != nil {
				devtools <- "http://" + match[1]
			}
		}
	}()
	select {
	case cdpURL := <-devtools:
		return cdpURL
	case <-time.After(20 * time.Second):
		t.Fatal("Chrome 没有报出调试地址")
	}
	return ""
}

// TestBrowserToolsHangingPageIntegration 用本机 Chrome 验证卡死的页面：服务器永远不回、
// 或者回了响应头却一直传不完正文。设 DIANA_BROWSER_TOOLS_INTEGRATION=1 才跑。
func TestBrowserToolsHangingPageIntegration(t *testing.T) {
	if os.Getenv("DIANA_BROWSER_TOOLS_INTEGRATION") != "1" {
		t.Skip("set DIANA_BROWSER_TOOLS_INTEGRATION=1 to run Chrome integration")
	}
	executable, err := FindBrowserExecutable("")
	if err != nil {
		t.Skip(err)
	}
	// released 在浏览器掐掉这条请求（连接断开）时收到一个信号：证明加载真的停了，
	// 而不只是工具这一侧不等了。
	released := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hang":
			<-r.Context().Done()
			released <- r.URL.Path
		case "/stream":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<title>stream</title><p>部分内容</p>`)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			released <- r.URL.Path
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<title>ok</title><p>正常页面</p>`)
		}
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	base := "http://localhost:" + port

	root := t.TempDir()
	cdpURL := startIntegrationChrome(t, executable, root)
	registry := NewToolRegistry()
	registry.RegisterBrowserTools(root, Config{BrowserCDPURL: cdpURL, BrowserTimeoutMS: 1500}.WithDefaults())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := func(name string, input map[string]any) (string, error) {
		tool, _ := registry.Get(name)
		return tool.Run(ctx, input)
	}
	waitReleased := func(path string) {
		t.Helper()
		select {
		case got := <-released:
			if got != path {
				t.Fatalf("断开的是 %s，想要 %s", got, path)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s 的请求没有被浏览器掐掉，页面还在加载", path)
		}
	}
	countPages := func() int {
		out, err := run("browser_tabs", map[string]any{"action": "list"})
		if err != nil {
			t.Fatal(err)
		}
		var listed struct {
			Tabs []json.RawMessage `json:"tabs"`
		}
		_ = json.Unmarshal([]byte(out), &listed)
		return len(listed.Tabs)
	}

	started := time.Now()
	if _, err := run("browser_open", map[string]any{"url": base + "/hang"}); err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("服务器不回时应当报超时：%v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("超时没按浏览器超时收手：%s", elapsed)
	}
	waitReleased("/hang")
	if out, err := run("browser_open", map[string]any{"url": base + "/"}); err != nil || !strings.Contains(out, "正常页面") {
		t.Fatalf("超时之后同一个标签页应当还能用：%s %v", out, err)
	}

	before := countPages()
	if _, err := run("browser_open", map[string]any{"url": base + "/hang", "new_tab": true}); err == nil {
		t.Fatal("新标签页打开卡死的地址应当报错")
	}
	waitReleased("/hang")
	if after := countPages(); after != before {
		t.Fatalf("超时的新标签页应当被关掉：%d → %d", before, after)
	}

	out, err := run("browser_open", map[string]any{"url": base + "/stream"})
	if err != nil || !strings.Contains(out, "部分内容") {
		t.Fatalf("正文传不完的页面应当交回已到手的内容：%s %v", out, err)
	}
	waitReleased("/stream")
}

// TestBrowserToolsStuckPageIntegration 用本机 Chrome 验证页面主线程被脚本卡死、渲染进程
// 崩溃这两种情况：工具在浏览器超时量级内交回清楚的原因，标签页被关掉或救回来，同一个
// 浏览器之后照常能用。设 DIANA_BROWSER_TOOLS_INTEGRATION=1 才跑。
func TestBrowserToolsStuckPageIntegration(t *testing.T) {
	if os.Getenv("DIANA_BROWSER_TOOLS_INTEGRATION") != "1" {
		t.Skip("set DIANA_BROWSER_TOOLS_INTEGRATION=1 to run Chrome integration")
	}
	executable, err := FindBrowserExecutable("")
	if err != nil {
		t.Skip(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/busy" {
			fmt.Fprint(w, `<title>busy</title><p>卡住之前</p><script>while(true){}</script>`)
			return
		}
		fmt.Fprint(w, `<title>ok</title><p>正常页面</p>`)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	base := "http://localhost:" + port

	const timeout = 3 * time.Second
	root := t.TempDir()
	cdpURL := startIntegrationChrome(t, executable, root)
	registry := NewToolRegistry()
	registry.RegisterBrowserTools(root, Config{BrowserCDPURL: cdpURL, BrowserTimeoutMS: int(timeout / time.Millisecond)}.WithDefaults())
	// 和 Runner 给工具的上限一样：修之前卡死的页面会一直耗到这里。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run := func(name string, input map[string]any) (string, time.Duration, error) {
		tool, _ := registry.Get(name)
		started := time.Now()
		out, err := tool.Run(ctx, input)
		return out, time.Since(started), err
	}
	// 卡死要等满一个浏览器超时才认得出，恢复再花几秒；远小于 Runner 的 60 秒。
	within := func(what string, elapsed time.Duration) {
		t.Helper()
		t.Logf("%s 用时 %s", what, elapsed.Round(time.Millisecond))
		if elapsed > timeout+6*time.Second {
			t.Fatalf("%s 没有在浏览器超时量级内收手：%s", what, elapsed)
		}
	}
	mustOpen := func(what string) {
		t.Helper()
		out, _, err := run("browser_open", map[string]any{"url": base + "/"})
		if err != nil || !strings.Contains(out, "正常页面") {
			t.Fatalf("%s之后同一个浏览器应当还能正常打开网页：%s %v", what, out, err)
		}
	}
	countPages := func() int {
		out, _, err := run("browser_tabs", map[string]any{"action": "list"})
		if err != nil {
			t.Fatal(err)
		}
		var listed struct {
			Tabs []json.RawMessage `json:"tabs"`
		}
		_ = json.Unmarshal([]byte(out), &listed)
		return len(listed.Tabs)
	}
	crashActiveTab := func() {
		t.Helper()
		tool, _ := registry.Get("browser_open")
		active := browserToolBaseOf(tool).session.active()
		targets, err := listBrowserTargets(ctx, cdpURL)
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range targets {
			if target.ID != active {
				continue
			}
			conn, _, err := websocket.DefaultDialer.Dial(target.WebSocketDebuggerURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.WriteJSON(map[string]any{"id": 1, "method": "Page.crash", "params": map[string]any{}})
			time.Sleep(500 * time.Millisecond)
			_ = conn.Close()
			return
		}
		t.Fatalf("找不到当前标签页 %s", active)
	}

	mustOpen("开局")
	before := countPages()

	// 新标签页打开卡死的页面：清楚的原因，新开的页被关掉。
	_, elapsed, err := run("browser_open", map[string]any{"url": base + "/busy", "new_tab": true})
	if err == nil || !strings.Contains(err.Error(), "没有响应") || !strings.Contains(err.Error(), "关闭") || strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("新标签页打开卡死的页面应当报页面没有响应并关掉标签页：%v", err)
	}
	within("new_tab 打开卡死页", elapsed)
	if after := countPages(); after != before {
		t.Fatalf("卡死的新标签页应当被关掉：%d → %d", before, after)
	}
	mustOpen("新标签页卡死")

	// 沿用当前标签页打开卡死的页面：标签页换回空白页，之后照样能用。
	_, elapsed, err = run("browser_open", map[string]any{"url": base + "/busy"})
	if err == nil || !strings.Contains(err.Error(), "没有响应") || !strings.Contains(err.Error(), "空白页") {
		t.Fatalf("沿用当前页打开卡死的页面应当报页面没有响应并换回空白页：%v", err)
	}
	within("沿用当前页打开卡死页", elapsed)
	if after := countPages(); after != before {
		t.Fatalf("沿用当前页不该多出或少掉标签页：%d → %d", before, after)
	}
	mustOpen("当前页卡死")

	// 页面在别的工具眼皮底下卡死：browser_text 按浏览器超时收手，而不是等满 60 秒。
	if _, _, err := run("browser_eval", map[string]any{"script": `setTimeout(() => { while (true) {} }, 50); 1`}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	_, elapsed, err = run("browser_text", nil)
	if err == nil || !strings.Contains(err.Error(), "没有响应") {
		t.Fatalf("卡死的页面上读文本应当报页面没有响应：%v", err)
	}
	within("卡死页上 browser_text", elapsed)
	mustOpen("读卡死页")

	// browser_eval 自己写了死循环：V8 按超时就地打断，页面不受影响。
	_, elapsed, err = run("browser_eval", map[string]any{"script": `while (true) {}`, "timeout_ms": 1000})
	if err == nil || !strings.Contains(err.Error(), "没有跑完") {
		t.Fatalf("死循环脚本应当被中断：%v", err)
	}
	within("死循环脚本", elapsed)
	if out, _, err := run("browser_text", nil); err != nil || !strings.Contains(out, "正常页面") {
		t.Fatalf("脚本被中断之后页面应当原样可用：%s %v", out, err)
	}
	// 一直不结束的 Promise：按调用上限收手，页面留着。
	_, elapsed, err = run("browser_eval", map[string]any{"script": `new Promise(() => {})`, "timeout_ms": 1000})
	if err == nil || !strings.Contains(err.Error(), "执行脚本时") {
		t.Fatalf("不结束的 Promise 应当按上限收手：%v", err)
	}
	within("不结束的 Promise", elapsed)
	if out, _, err := run("browser_text", nil); err != nil || !strings.Contains(out, "正常页面") {
		t.Fatalf("等 Promise 超时之后页面应当还在：%s %v", out, err)
	}

	// 渲染进程崩溃：报「崩溃」，标签页换一个新的渲染进程。
	crashActiveTab()
	_, elapsed, err = run("browser_text", nil)
	if err == nil || !strings.Contains(err.Error(), "崩溃") {
		t.Fatalf("崩掉的页面应当报页面崩溃：%v", err)
	}
	within("崩溃页上 browser_text", elapsed)
	mustOpen("页面崩溃")
	// 沿用一个已经崩掉的标签页打开网页：直接换掉，不用模型重试。
	crashActiveTab()
	mustOpen("沿用崩溃的标签页")
	if after := countPages(); after != before {
		t.Fatalf("恢复过程不该多出或少掉标签页：%d → %d", before, after)
	}
}

func TestCompactCDPValueKeepsChineseAndBigNumbers(t *testing.T) {
	got := string(compactCDPValue(json.RawMessage(`{"text":"第二页 <b>","id":12345678901234567890}`)))
	if got != `{"id":12345678901234567890,"text":"第二页 <b>"}` {
		t.Fatalf("got %s", got)
	}
}

type denyingBrowser struct{ denied string }

func (d denyingBrowser) Endpoint(context.Context) (string, error) { return "", nil }
func (d denyingBrowser) AllowsURL(rawURL string) bool             { return !strings.Contains(rawURL, d.denied) }

// 用户在「浏览器」页填的禁止名单，机器人主动打开时也要认，不能只挡实时画面那一侧。
func TestBrowserOpenHonoursBuiltinDeniedHosts(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterBrowserTools(t.TempDir(), Config{BuiltinBrowser: denyingBrowser{denied: "bank.example"}}.WithDefaults())
	for _, call := range []struct {
		tool  string
		input map[string]any
	}{
		{"browser_open", map[string]any{"url": "https://bank.example/login"}},
		{"browser_tabs", map[string]any{"action": "new", "url": "https://bank.example/"}},
	} {
		tool, _ := registry.Get(call.tool)
		if _, err := tool.Run(context.Background(), call.input); err == nil || !strings.Contains(err.Error(), "禁止名单") {
			t.Fatalf("%s 应当被禁止名单挡下：%v", call.tool, err)
		}
	}
}

// userTabsBrowser 是一个认得「主人自己开的标签」的内置浏览器。
type userTabsBrowser struct {
	endpoint string
	user     string
}

func (b userTabsBrowser) Endpoint(context.Context) (string, error) { return b.endpoint, nil }
func (b userTabsBrowser) UserTab(targetID string) bool             { return targetID == b.user }

// 主人在画面里自己开的标签，机器人自动挑标签时跳过、列表里看不到、也切不过去。
func TestBrowserToolsLeaveUserTabsAlone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/list" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"id": "mine", "type": "page", "url": "https://login.example/", "title": "主人在登录", "webSocketDebuggerUrl": "ws://127.0.0.1/devtools/page/mine"},
			{"id": "bot", "type": "page", "url": "https://news.example/", "title": "机器人的页", "webSocketDebuggerUrl": "ws://127.0.0.1/devtools/page/bot"},
		})
	}))
	t.Cleanup(server.Close)
	base := browserToolBase{
		builtin: userTabsBrowser{endpoint: server.URL, user: "mine"},
		timeout: 5 * time.Second,
		session: &browserSession{key: "chat"},
		tabs:    newBrowserTabRegistry(),
	}
	target, err := base.pickTarget(context.Background(), server.URL, false)
	if err != nil || target.ID != "bot" {
		t.Fatalf("应当挑机器人的标签，实际 %q %v", target.ID, err)
	}
	base.session.setActive("mine")
	if target, _ := base.pickTarget(context.Background(), server.URL, false); target.ID == "mine" {
		t.Fatal("就算之前切过，主人的标签也不该再被挑中")
	}
	tool := &BrowserTabsTool{base: base}
	listed, err := tool.Run(context.Background(), map[string]any{"action": "list"})
	if err != nil || strings.Contains(listed, "mine") || !strings.Contains(listed, "bot") {
		t.Fatalf("列表里不该有主人的标签：%s %v", listed, err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"action": "switch", "tab_id": "mine"}); err == nil || !strings.Contains(err.Error(), "主人") {
		t.Fatalf("切到主人的标签应当被拒，实际 %v", err)
	}
}
