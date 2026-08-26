// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseRenderedPageExtractsVisibleContent(t *testing.T) {
	raw := []byte(`<!doctype html><html><head><title>测试站点</title><meta name="description" content="站点描述"><link rel="canonical" href="/home"></head><body><nav>导航</nav><main><h1>欢迎</h1><script>secret()</script><p>动态正文</p><p hidden>隐藏内容</p></main></body></html>`)
	page, err := parseRenderedPage(raw, "https://example.com/start", 1000, false)
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "测试站点" || page.Description != "站点描述" || page.URL != "https://example.com/home" {
		t.Fatalf("page = %#v", page)
	}
	if !strings.Contains(page.Text, "欢迎") || !strings.Contains(page.Text, "动态正文") || strings.Contains(page.Text, "secret") || strings.Contains(page.Text, "隐藏内容") {
		t.Fatalf("text = %q", page.Text)
	}
}

func TestSandboxedBrowserURLRejectsLocalTargets(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1:8080",
		"http://[::1]/",
		"http://192.168.1.1/",
		"http://localhost/",
		"http://host.docker.internal/",
		"https://" + "user:pass@" + "example.com/",
	} {
		if err := validateSandboxedBrowserURL(context.Background(), rawURL); err == nil {
			t.Fatalf("validateSandboxedBrowserURL(%q) error = nil", rawURL)
		}
	}
	if err := validateSandboxedBrowserURL(context.Background(), "https://1.1.1.1/"); err != nil {
		t.Fatalf("public address rejected: %v", err)
	}
}

func TestSandboxedChromeArgsKeepChromeSandboxEnabled(t *testing.T) {
	args := sandboxedChromeArgs("/tmp/profile", "/tmp/cache", "/tmp/crash", sandboxedBrowserConfigWithDefaults(SandboxedBrowserConfig{}))
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--no-sandbox") {
		t.Fatalf("args disable Chrome sandbox: %s", joined)
	}
	for _, want := range []string{"--headless=new", "--remote-debugging-port=0", "--user-data-dir=/tmp/profile", "--disable-extensions"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
}

func TestBrowserRenderToolUsesRenderer(t *testing.T) {
	tool := NewBrowserRenderTool(PageRendererFunc(func(_ context.Context, rawURL string) (RenderedPage, error) {
		return RenderedPage{RequestedURL: rawURL, URL: rawURL, Title: "Rendered", Text: "hello", Sandboxed: true}, nil
	}))
	output, err := tool.Run(context.Background(), map[string]any{"url": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"title": "Rendered"`) || !strings.Contains(output, `"sandboxed": true`) {
		t.Fatalf("output = %s", output)
	}
}

func TestRenderReadinessWaitsForObservationWindow(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	cfg := sandboxedBrowserConfigWithDefaults(SandboxedBrowserConfig{
		VirtualTimeBudget: 8 * time.Second,
		StabilityWindow:   time.Second,
		NetworkIdleWindow: 500 * time.Millisecond,
	})
	readiness := newRenderReadiness(started)
	activity := browserActivitySnapshot{
		LastNavigation: started,
		LastNetwork:    started,
	}
	probe := browserDOMProbe{
		URL:               "https://example.com",
		ReadyState:        "complete",
		Title:             "Example",
		TextLength:        100,
		SemanticSignature: "first",
		DOMChanges:        3,
	}
	_ = readiness.observe(started, probe, activity, cfg)
	before := readiness.observe(started.Add(2*time.Second), probe, activity, cfg)
	if !before.ContentStable || before.Complete || before.Reason != "observing_for_delayed_changes" {
		t.Fatalf("before = %#v", before)
	}
	after := readiness.observe(started.Add(9*time.Second), probe, activity, cfg)
	if !after.Complete || after.Reason != "dom_and_network_stable" || after.DOMChanges != 3 {
		t.Fatalf("after = %#v", after)
	}
}

func TestRenderReadinessResetsWhenPageChanges(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	cfg := sandboxedBrowserConfigWithDefaults(SandboxedBrowserConfig{
		VirtualTimeBudget: time.Second,
		StabilityWindow:   time.Second,
		NetworkIdleWindow: 500 * time.Millisecond,
	})
	readiness := newRenderReadiness(started)
	activity := browserActivitySnapshot{LastNavigation: started, LastNetwork: started}
	probe := browserDOMProbe{URL: "https://example.com", ReadyState: "complete", Title: "Before", TextLength: 20, SemanticSignature: "before", DOMChanges: 2}
	_ = readiness.observe(started, probe, activity, cfg)
	if decision := readiness.observe(started.Add(2*time.Second), probe, activity, cfg); !decision.Complete {
		t.Fatalf("initial decision = %#v", decision)
	}

	changedAt := started.Add(3 * time.Second)
	activity.LastNavigation = changedAt
	activity.LastNetwork = changedAt
	probe.URL = "https://redirect.example/final"
	probe.Title = "After"
	probe.SemanticSignature = "after"
	probe.DOMChanges = 5
	changed := readiness.observe(changedAt, probe, activity, cfg)
	if changed.ContentStable || changed.Complete {
		t.Fatalf("changed = %#v", changed)
	}
	settled := readiness.observe(changedAt.Add(2*time.Second), probe, activity, cfg)
	if !settled.Complete || settled.DOMChanges != 7 {
		t.Fatalf("settled = %#v", settled)
	}
}

func TestRenderReadinessAcceptsStableInteractiveSPA(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	cfg := sandboxedBrowserConfigWithDefaults(SandboxedBrowserConfig{
		VirtualTimeBudget: time.Second,
		StabilityWindow:   time.Second,
		NetworkIdleWindow: 500 * time.Millisecond,
	})
	readiness := newRenderReadiness(started)
	activity := browserActivitySnapshot{
		LastNavigation:  started,
		LastNetwork:     started.Add(3500 * time.Millisecond),
		Loading:         true,
		PendingRequests: 8,
	}
	probe := browserDOMProbe{
		URL:               "https://spa.example/app",
		ReadyState:        "interactive",
		Title:             "Loaded app",
		TextLength:        12_000,
		SemanticSignature: "stable-primary-content",
		DOMChanges:        500,
	}
	_ = readiness.observe(started, probe, activity, cfg)
	decision := readiness.observe(started.Add(4*time.Second), probe, activity, cfg)
	if !decision.ContentStable || !decision.Complete {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestRenderReadinessAcceptsStableLoadingDocumentAfterGrace(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	cfg := sandboxedBrowserConfigWithDefaults(SandboxedBrowserConfig{
		VirtualTimeBudget: time.Second,
		StabilityWindow:   800 * time.Millisecond,
		NetworkIdleWindow: 500 * time.Millisecond,
	})
	readiness := newRenderReadiness(started)
	activity := browserActivitySnapshot{
		LastNavigation:  started,
		LastNetwork:     started.Add(5500 * time.Millisecond),
		Loading:         true,
		PendingRequests: 6,
	}
	probe := browserDOMProbe{
		URL:               "https://video.example/watch",
		ReadyState:        "loading",
		Title:             "Video title",
		Description:       "Primary metadata is available",
		TextLength:        3000,
		SemanticSignature: "stable-loading-content",
		DOMChanges:        900,
	}
	_ = readiness.observe(started, probe, activity, cfg)
	decision := readiness.observe(started.Add(6*time.Second), probe, activity, cfg)
	if !decision.ContentStable || !decision.Complete {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestSandboxedHeadlessBrowserIntegration(t *testing.T) {
	if os.Getenv("DIANA_HEADLESS_BROWSER_INTEGRATION") != "1" {
		t.Skip("set DIANA_HEADLESS_BROWSER_INTEGRATION=1 to run Chrome integration")
	}
	targetURL := strings.TrimSpace(os.Getenv("DIANA_HEADLESS_BROWSER_TEST_URL"))
	if targetURL == "" {
		targetURL = "https://example.com"
	}
	renderer := NewSandboxedHeadlessBrowser(SandboxedBrowserConfig{
		Timeout:           30 * time.Second,
		VirtualTimeBudget: 3 * time.Second,
	})
	page, err := renderer.Render(context.Background(), targetURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rendered title=%q url=%q text=%q", page.Title, page.URL, truncateText(page.Text, 500))
	if !page.Sandboxed || (page.Title == "" && page.Text == "") {
		t.Fatalf("page = %#v", page)
	}
	if targetURL == "https://example.com" && (page.Title != "Example Domain" || !strings.Contains(page.Text, "documentation examples")) {
		t.Fatalf("page = %#v", page)
	}
}

func TestSandboxedHeadlessBrowserDelayedRedirectIntegration(t *testing.T) {
	if os.Getenv("DIANA_HEADLESS_BROWSER_REDIRECT_INTEGRATION") != "1" {
		t.Skip("set DIANA_HEADLESS_BROWSER_REDIRECT_INTEGRATION=1 to run delayed redirect integration")
	}
	renderer := NewSandboxedHeadlessBrowser(SandboxedBrowserConfig{})
	page, err := renderer.Render(context.Background(), "https://67movies.xyz")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rendered stable=%v waited=%d url=%q title=%q chain=%#v previous=%#v", page.Stable, page.WaitedMS, page.URL, page.Title, page.NavigationChain, page.PreviousPages)
	if !page.Stable || !strings.Contains(page.URL, "youtube.com/watch") {
		t.Fatalf("final page = %#v", page)
	}
	if len(page.NavigationChain) < 2 || len(page.PreviousPages) == 0 || !strings.Contains(page.PreviousPages[0].Text, "Welcome to 67movies") {
		t.Fatalf("navigation evidence missing: %#v", page)
	}
}

// 插件在控制台上是「已启用」，机器上没装浏览器也照样是「已启用」——以前只有等
// 有人发了链接才会知道。探测要能分清「没找到」和「找到了但跑不起来」，这两种
// 的处理方式完全不同。
func TestProbeHeadlessBrowserReportsWhyItIsUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上 chrome.exe 不往标准输出打印版本，探测走的是另一条路径")
	}
	dir := t.TempDir()

	missing := filepath.Join(dir, "nope")
	status := ProbeHeadlessBrowser(context.Background(), missing)
	if status.Available || !strings.Contains(status.Detail, missing) {
		t.Fatalf("status=%#v", status)
	}

	// 存在但一执行就失败：架构不匹配、缺动态库都是这个形状，不能算可用。
	broken := filepath.Join(dir, "broken")
	writeExecutable(t, broken, "#!/bin/sh\necho 'error while loading shared libraries' >&2\nexit 127\n")
	status = ProbeHeadlessBrowser(context.Background(), broken)
	if status.Available || status.Path != broken || !strings.Contains(status.Detail, "执行失败") {
		t.Fatalf("status=%#v", status)
	}

	working := filepath.Join(dir, "chromium")
	writeExecutable(t, working, "#!/bin/sh\necho 'Chromium 120.0.6099.109'\n")
	status = ProbeHeadlessBrowser(context.Background(), working)
	if !status.Available || status.Path != working || status.Version != "Chromium 120.0.6099.109" {
		t.Fatalf("status=%#v", status)
	}
	if status.Detail != "" {
		t.Fatalf("available browser still carried a failure detail: %q", status.Detail)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

// TestHeadlessBrowserExecutableHonoursEnvironment 盯住三条路径认同一份浏览器配置。
//
// 环境变量原先只写在 SandboxedBrowserConfig 的默认值里，于是只有网页渲染认得它；
// 截图和可用性探测走 findHeadlessBrowserExecutable 这条入口，读不到。浏览器装在
// 非标准路径、靠环境变量指过去的机器上，症状就是网页渲染能用、关系图却报「这台
// 机器上没有可用的无头浏览器」。
func TestHeadlessBrowserExecutableHonoursEnvironment(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "fake-chrome")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho fake\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DIANA_HEADLESS_BROWSER_EXECUTABLE", fake)
	got, err := findHeadlessBrowserExecutable("")
	if err != nil || got != fake {
		t.Fatalf("findHeadlessBrowserExecutable(\"\") = %q, %v，想要 %q", got, err, fake)
	}

	// 另一个环境变量名同样认。
	t.Setenv("DIANA_HEADLESS_BROWSER_EXECUTABLE", "")
	t.Setenv("DIANA_AGENT_BROWSER_EXECUTABLE", fake)
	if got, err := findHeadlessBrowserExecutable(""); err != nil || got != fake {
		t.Fatalf("备用环境变量没生效：%q, %v", got, err)
	}

	// 显式传进来的路径优先：环境变量只是兜底，不能反过来盖掉调用方的选择。
	other := filepath.Join(t.TempDir(), "explicit-chrome")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := findHeadlessBrowserExecutable(other); err != nil || got != other {
		t.Fatalf("显式路径被环境变量盖掉了：%q, %v", got, err)
	}

	// 探测走的是同一个入口，结论必须跟着一致。
	if status := ProbeHeadlessBrowser(context.Background(), ""); status.Path != fake {
		t.Fatalf("探测没有认环境变量：%#v", status)
	}
}

// TestBothBrowserPathsShareSandboxHardening 盯住两条渲染路径的沙盒强度一致。
//
// 截图那条路原先手抄了一份参数子集，抄漏了 --host-resolver-rules——也就是网页渲染
// 挡住了 localhost 和内网域名，截图那边没挡。同一个进程里跑的两个浏览器，沙盒不该
// 有强弱之分。
func TestBothBrowserPathsShareSandboxHardening(t *testing.T) {
	base := sandboxedChromeBaseArgs("/tmp/p", "/tmp/c", "/tmp/x")
	for _, want := range []string{
		"--headless=new",
		"--host-resolver-rules=MAP localhost ~NOTFOUND, MAP *.localhost ~NOTFOUND, MAP *.local ~NOTFOUND, MAP host.docker.internal ~NOTFOUND, MAP gateway.docker.internal ~NOTFOUND",
		"--disable-sync",
		"--no-pings",
		"--password-store=basic",
		"--user-data-dir=/tmp/p",
	} {
		if !slices.Contains(base, want) {
			t.Fatalf("共用底座缺少 %q：%v", want, base)
		}
	}

	// 带模式色彩的参数不能混进底座：远程调试端口只属于 CDP 那条路，
	// 截图那条路开着它等于白起一个调试端口。
	for _, unwanted := range []string{"--remote-debugging-port=0", "--screenshot=", "--window-size=1280,960"} {
		for _, arg := range base {
			if strings.HasPrefix(arg, unwanted) {
				t.Fatalf("底座里混进了模式专用参数 %q", arg)
			}
		}
	}

	// CDP 那条路是在底座上追加自己的参数，不是另起一份。
	cdp := sandboxedChromeArgs("/tmp/p", "/tmp/c", "/tmp/x", SandboxedBrowserConfig{})
	for _, arg := range base {
		if !slices.Contains(cdp, arg) {
			t.Fatalf("CDP 参数丢了底座里的 %q", arg)
		}
	}
	if !slices.Contains(cdp, "--remote-debugging-port=0") {
		t.Fatalf("CDP 参数没有远程调试端口：%v", cdp)
	}
}

// TestBrowserSandboxDirsArePrivateAndCleaned 临时目录组两条路共用，权限和清理
// 写错一次就是一个留在 /tmp 里的可读 profile。
func TestBrowserSandboxDirsArePrivateAndCleaned(t *testing.T) {
	dirs, err := newBrowserSandboxDirs("diana-test-")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{dirs.root, dirs.profile, dirs.cache, dirs.crash} {
		info, statErr := os.Stat(dir)
		if statErr != nil {
			t.Fatalf("%s 没建出来：%v", dir, statErr)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("%s 权限是 %o，想要 700", dir, perm)
		}
	}
	dirs.remove()
	if _, err := os.Stat(dirs.root); !os.IsNotExist(err) {
		t.Fatalf("临时目录没删干净：%v", err)
	}
}
