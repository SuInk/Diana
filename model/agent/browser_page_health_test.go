// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 这组用假浏览器验证：页面主线程卡死、渲染进程崩溃时 Runtime.evaluate 永远不回，
// 工具按浏览器超时收手、说清原因、把标签页关掉或救回来。真 Chrome 上的同一组场景见
// TestBrowserToolsStuckPageIntegration。

const stuckTestTimeout = 300 * time.Millisecond

// 恢复要另开几条会话、确认页面能回话，给它留几秒；和修之前的 60 秒不是一个量级。
func assertQuick(t *testing.T, what string, started time.Time) {
	t.Helper()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("%s 没有按浏览器超时收手，用了 %s", what, elapsed)
	}
}

func TestBrowserOpenBusyPageInNewTabClosesIt(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", stuckTestTimeout)
	if _, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://example.com/"}); err != nil {
		t.Fatal(err)
	}
	before := f.pages()
	started := time.Now()
	_, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://busy.example/", "new_tab": true})
	var failure *browserPageError
	if !errors.As(err, &failure) || failure.crashed || !strings.Contains(err.Error(), "打开 https://busy.example/ 后页面") ||
		!strings.Contains(err.Error(), "没有响应") || !strings.Contains(err.Error(), "已关闭这个新开的标签页") {
		t.Fatalf("应当报页面没有响应并关掉新开的页：%v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("卡死仍应认得出是超时：%v", err)
	}
	assertQuick(t, "新标签页打开卡死页", started)
	if f.pages() != before {
		t.Fatalf("卡死的新标签页应当被关掉：%d → %d", before, f.pages())
	}
	if out, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://example.com/next"}); err != nil || !strings.Contains(out, "example.com/next") {
		t.Fatalf("之后应当还能正常打开网页：%s %v", out, err)
	}
}

func TestBrowserOpenBusyPageInCurrentTabResetsIt(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", stuckTestTimeout)
	started := time.Now()
	_, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://busy.example/"})
	if err == nil || !strings.Contains(err.Error(), "没有响应") || !strings.Contains(err.Error(), "换回空白页") {
		t.Fatalf("沿用当前页打开卡死的页面应当报没有响应并换回空白页：%v", err)
	}
	assertQuick(t, "当前页打开卡死页", started)
	if f.pages() != 1 {
		t.Fatalf("沿用当前页不该关掉或多开标签页：%d", f.pages())
	}
	f.mu.Lock()
	current := f.targets["T1"]
	f.mu.Unlock()
	if current != "about:blank" {
		t.Fatalf("卡死的那一页应当换回空白页：%s", current)
	}
	if out, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://example.com/next"}); err != nil || !strings.Contains(out, "example.com/next") {
		t.Fatalf("之后同一页应当还能用：%s %v", out, err)
	}
}

// 页面在别的工具底下卡死：先打断卡住的脚本，页面能回话就留着。
func TestBrowserTextOnBusyPageTerminatesScript(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/busy")
	f.busy["T1"] = true
	f.terminable = true
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", stuckTestTimeout)
	started := time.Now()
	_, err := runBrowserTool(context.Background(), registry, "browser_text", nil)
	if err == nil || !strings.Contains(err.Error(), "没有响应") || !strings.Contains(err.Error(), "已打断页面上卡住的脚本") {
		t.Fatalf("应当报没有响应并说明已打断脚本：%v", err)
	}
	assertQuick(t, "卡死页上读文本", started)
	f.mu.Lock()
	current, terminated := f.targets["T1"], f.terminated
	f.mu.Unlock()
	if terminated != 1 || current != "https://example.com/busy" {
		t.Fatalf("打断脚本之后页面应当留着：terminated=%d url=%s", terminated, current)
	}
	if out, err := runBrowserTool(context.Background(), registry, "browser_text", nil); err != nil || !strings.Contains(out, "example.com/busy") {
		t.Fatalf("打断之后应当能照常读页面：%s %v", out, err)
	}
}

// 打断不了（新会话挂不到卡住的渲染进程上）就换回空白页，不让下一次调用接着卡。
func TestBrowserTextOnBusyPageFallsBackToBlank(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/busy")
	f.busy["T1"] = true
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", stuckTestTimeout)
	started := time.Now()
	_, err := runBrowserTool(context.Background(), registry, "browser_text", nil)
	if err == nil || !strings.Contains(err.Error(), "没有响应") || !strings.Contains(err.Error(), "换回空白页") {
		t.Fatalf("打断不了时应当换回空白页：%v", err)
	}
	assertQuick(t, "卡死页上读文本", started)
	if _, err := runBrowserTool(context.Background(), registry, "browser_text", nil); err != nil {
		t.Fatalf("换回空白页之后应当能继续用：%v", err)
	}
}

// 渲染进程崩了：Inspector.targetCrashed 一到就报「崩溃」，不用等超时。
func TestBrowserTextOnCrashedPageReportsCrash(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	f.crashed["T1"] = true
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", 30*time.Second)
	started := time.Now()
	_, err := runBrowserTool(context.Background(), registry, "browser_text", nil)
	var failure *browserPageError
	if !errors.As(err, &failure) || !failure.crashed || !strings.Contains(err.Error(), "页面崩溃了") || !strings.Contains(err.Error(), "换回空白页") {
		t.Fatalf("崩溃的页面应当报崩溃并换回空白页：%v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("崩溃不是超时：%v", err)
	}
	assertQuick(t, "崩溃页上读文本", started)
	if _, err := runBrowserTool(context.Background(), registry, "browser_text", nil); err != nil {
		t.Fatalf("换回空白页之后应当能继续用：%v", err)
	}
}

// 沿用一个已经崩掉的标签页打开网页：先换掉再跳转，不用模型重试。
func TestBrowserOpenOnCrashedTabStillOpens(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	f.crashed["T1"] = true
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", 30*time.Second)
	out, err := runBrowserTool(context.Background(), registry, "browser_open", map[string]any{"url": "https://example.com/next"})
	if err != nil || !strings.Contains(out, "example.com/next") {
		t.Fatalf("崩掉的当前页应当被换掉后照常打开：%s %v", out, err)
	}
}

// 走一遍 Runner：模型拿到的是「页面没有响应」，不是「工具执行超时（上限 60000ms）」。
func TestBusyPageReachesModelThroughRunner(t *testing.T) {
	f := newFakeCDP(t, "https://example.com/")
	registry := browserToolsFor(t, f, newBrowserTabRegistry(), "bot\x00group:1", stuckTestTimeout)
	client := &scriptedClient{responses: []string{
		`{"action":"tool","tool":"browser_open","input":{"url":"https://busy.example/","new_tab":true}}`,
		`{"action":"final","content":"打不开"}`,
	}}
	runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 1, ToolTimeoutMS: 60_000}, registry)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	resp, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "打开"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Steps) != 1 || !strings.Contains(resp.Steps[0].Error, "没有响应（脚本卡住或页面崩溃）") || strings.Contains(resp.Steps[0].Error, "工具执行超时") {
		t.Fatalf("模型应当拿到页面没有响应的原因：%#v", resp.Steps)
	}
	assertQuick(t, "Runner 里打开卡死页", started)
}

// browser_eval 的时限：默认一个浏览器超时，要求更久时最多放宽到 maxBrowserWaitMS。
func TestBrowserEvalLimit(t *testing.T) {
	base := browserToolBase{timeout: 15 * time.Second}
	cases := []struct {
		requested int
		want      time.Duration
	}{
		{0, 15 * time.Second},
		{1000, time.Second},
		{25_000, 25 * time.Second},
		{120_000, maxBrowserWaitMS * time.Millisecond},
	}
	for _, c := range cases {
		if got := base.evalLimit(c.requested); got != c.want {
			t.Fatalf("evalLimit(%d) = %s, want %s", c.requested, got, c.want)
		}
	}
	long := browserToolBase{timeout: 45 * time.Second}
	if got := long.evalLimit(120_000); got != 45*time.Second {
		t.Fatalf("浏览器超时更长时以它为准：%s", got)
	}
}
