// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build browser_integration

package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSandboxElementsAcrossIframeIntegration 用真 Chrome 跑一遍 browser-use 式的操作：
// 主页面只有外壳，题目在另一个域名的 iframe 里（B 站 Toy 就是这样），按编号点选项、
// 按编号往 iframe 里的输入框打字。
func TestSandboxElementsAcrossIframeIntegration(t *testing.T) {
	executable, err := findChromeExecutable("")
	if err != nil {
		t.Skip(err)
	}
	quiz := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><body style="margin:0">
<h1>第 1 题：你更喜欢？</h1>
<div style="cursor:pointer;padding:12px;border:1px solid" onclick="document.getElementById('r').textContent='已选A'"><span>选项A</span></div>
<div style="cursor:pointer;padding:12px;border:1px solid" onclick="document.getElementById('r').textContent='已选B'"><span>选项B</span></div>
<input id="nick" placeholder="昵称" oninput="document.getElementById('n').textContent='昵称:'+this.value">
<p id="r">未选择</p><p id="n"></p></body>`)
	}))
	defer quiz.Close()
	// localhost 和 127.0.0.1 是两个源，iframe 就是跨域的。
	quizURL := strings.Replace(quiz.URL, "127.0.0.1", "localhost", 1)
	shell := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><body style="margin:0"><div>外壳：展开详情</div><iframe src="%s" style="border:0;width:600px;height:500px"></iframe></body>`, quizURL)
	}))
	defer shell.Close()

	endpoint := startSandboxTestChrome(t, executable)

	stub := &sandboxBrowserStub{personalBrowserStub{endpoint: endpoint}}
	base := browserToolBase{personal: stub, sandbox: true, timeout: 15 * time.Second, session: &browserSession{}, tabs: newBrowserTabRegistry()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	run := func(tool Tool, input map[string]any) map[string]any {
		t.Helper()
		out, err := tool.Run(ctx, input)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name(), err)
		}
		var page map[string]any
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatalf("%s: %v\n%s", tool.Name(), err, out)
		}
		return page
	}
	index := func(page map[string]any, label string) int {
		t.Helper()
		match := regexp.MustCompile(`\[(\d+)\]<[^>]*>` + regexp.QuoteMeta(label)).FindStringSubmatch(fmt.Sprint(page["elements"]))
		if match == nil {
			match = regexp.MustCompile(`\[(\d+)\]<[^>]*` + regexp.QuoteMeta(label)).FindStringSubmatch(fmt.Sprint(page["elements"]))
		}
		if match == nil {
			t.Fatalf("no element %q in:\n%s", label, page["elements"])
		}
		n, _ := strconv.Atoi(match[1])
		return n
	}

	// 测试页在本机，open 的公网检查会拦（那正是线上要的），这里直接导航，之后的
	// 读取、点击、输入都走真实工具。
	client, err := base.openClient(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := base.load(ctx, client, shell.URL, true); err != nil {
		t.Fatal(err)
	}
	client.Close()
	page := run(&BrowserTextTool{base: base}, map[string]any{})
	t.Logf("elements:\n%s\ntext:\n%s", page["elements"], page["text"])
	if !strings.Contains(fmt.Sprint(page["text"]), "第 1 题") {
		t.Fatalf("iframe text missing: %v", page["text"])
	}
	page = run(&BrowserClickTool{base: base}, map[string]any{"index": index(page, "选项B")})
	if !strings.Contains(fmt.Sprint(page["text"]), "已选B") {
		t.Fatalf("click by index missed the iframe option: %v", page["text"])
	}
	page = run(&BrowserTypeTool{base: base}, map[string]any{"index": index(page, `placeholder="昵称"`), "text": "嘉然"})
	if !strings.Contains(fmt.Sprint(page["text"]), "昵称:嘉然") {
		t.Fatalf("type by index missed the iframe input: %v", page["text"])
	}
}

// startSandboxTestChrome 照沙箱的启动参数起一个无头 Chrome（关掉站点隔离），返回调试地址。
func startSandboxTestChrome(t *testing.T, executable string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cmd := exec.Command(executable, "--headless=new", "--no-first-run", "--user-data-dir="+t.TempDir(), "--remote-debugging-port="+strconv.Itoa(port),
		"--disable-site-isolation-trials", "--disable-features=IsolateOrigins,site-per-process", "--window-size=800,700", "about:blank")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	for deadline := time.Now().Add(15 * time.Second); ; {
		if response, err := http.Get(endpoint + "/json/version"); err == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("chrome did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return endpoint
}

// TestSandboxBilibiliToyIntegration 打开线上的 B 站 Toy 测试页走一遍：只读外壳时拿不到
// 的「开始测试」要出现在编号清单里，按编号点进去能看到第一题，截图带编号框。要联网。
// go test -tags browser_integration ./model/agent -run TestSandboxBilibiliToyIntegration -args -sandbox-shot=/tmp/x.png
func TestSandboxBilibiliToyIntegration(t *testing.T) {
	executable, err := findChromeExecutable("")
	if err != nil {
		t.Skip(err)
	}
	endpoint := startSandboxTestChrome(t, executable)
	base := browserToolBase{personal: &sandboxBrowserStub{personalBrowserStub{endpoint: endpoint}}, sandbox: true, timeout: 30 * time.Second, session: &browserSession{}, tabs: newBrowserTabRegistry()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := (&BrowserOpenTool{base: base}).Run(ctx, map[string]any{"url": "https://www.bilibili.com/toy/HENTI/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	var page map[string]any
	_ = json.Unmarshal([]byte(out), &page)
	for i := 0; i < 10 && !strings.Contains(fmt.Sprint(page["elements"]), "开始测试"); i++ {
		time.Sleep(time.Second)
		out, err = (&BrowserTextTool{base: base}).Run(ctx, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(out), &page)
	}
	t.Logf("open elements:\n%s", page["elements"])
	match := regexp.MustCompile(`\[(\d+)\]<[^>]*>[^\n]*开始测试`).FindStringSubmatch(fmt.Sprint(page["elements"]))
	if match == nil {
		t.Fatalf("开始测试 not listed:\n%s\ntext:\n%s", page["elements"], page["text"])
	}
	n, _ := strconv.Atoi(match[1])
	out, err = (&BrowserClickTool{base: base}).Run(ctx, map[string]any{"index": n})
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(out), &page)
	t.Logf("after start:\n%s\ntext:\n%s", page["elements"], page["text"])
	// 再点「开始答题」，第一题的选项要能编上号。
	match = regexp.MustCompile(`\[(\d+)\]<[^>]*>开始答题`).FindStringSubmatch(fmt.Sprint(page["elements"]))
	if match == nil {
		t.Fatalf("开始答题 not listed:\n%s", page["elements"])
	}
	n, _ = strconv.Atoi(match[1])
	if out, err = (&BrowserClickTool{base: base}).Run(ctx, map[string]any{"index": n}); err != nil {
		t.Fatal(err)
	}
	// 题目有两三秒的过场动画，像模型那样等题目文字出来再读。
	if out, err = (&BrowserWaitTool{base: base}).Run(ctx, map[string]any{"text": "Q01", "timeout_ms": 10000}); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(out), &page)
	if page["found"] != true {
		t.Fatalf("wait did not see the iframe question: %s", out)
	}
	t.Logf("first question:\n%s\ntext:\n%s", page["elements"], page["text"])
	if len(regexp.MustCompile(`\[\d+\]`).FindAllString(fmt.Sprint(page["elements"]), -1)) <= 10 {
		t.Fatalf("question options not listed:\n%s", page["elements"])
	}
	shot := &BrowserScreenshotTool{base: base, restricted: true}
	out, err = shot.Run(ctx, map[string]any{})
	if err != nil || len(shot.ToolResultParts("")) != 1 {
		t.Fatalf("screenshot: %v %s", err, out)
	}
	if path := *sandboxShotPath; path != "" {
		if data, err := base64.StdEncoding.DecodeString(shot.ToolResultParts("")[0].ImageURL[strings.Index(shot.ToolResultParts("")[0].ImageURL, ",")+1:]); err == nil {
			_ = os.WriteFile(path, data, 0o644)
		}
	}
}

var sandboxShotPath = flag.String("sandbox-shot", "", "TestSandboxBilibiliToyIntegration 把截图存到这里")
