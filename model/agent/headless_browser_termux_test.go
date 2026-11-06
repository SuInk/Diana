// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestSandboxedChromeArgsDisableSandboxOnTermux(t *testing.T) {
	t.Setenv("PREFIX", "/data/data/com.termux/files/usr")
	if args := sandboxedChromeBaseArgsForMode("p", "c", "d", true); !slices.Contains(args, "--no-sandbox") {
		t.Fatalf("Termux 上没关沙盒，Chromium 起不来：%v", args)
	}
	// 常驻的内置浏览器走的是同一套底座参数。
	if args := PersistentBrowserArgs("p", "c", "d", true, 0, 1280, 960); !slices.Contains(args, "--no-sandbox") {
		t.Fatalf("内置浏览器在 Termux 上没关沙盒：%v", args)
	}
}

func TestSandboxedChromeArgsKeepSandboxElsewhere(t *testing.T) {
	t.Setenv("PREFIX", "/usr/local")
	t.Setenv("TERMUX_VERSION", "")
	stubBrowserRunningAsRoot(t, false)
	if args := sandboxedChromeBaseArgsForMode("p", "c", "d", false); slices.Contains(args, "--no-sandbox") {
		t.Fatal("不在 Termux 时把沙盒关掉了")
	}
}

func TestBrowserSandboxModes(t *testing.T) {
	t.Setenv("PREFIX", "/usr/local")
	t.Setenv("TERMUX_VERSION", "")
	t.Cleanup(func() { SetBrowserSandboxMode(BrowserSandboxOnly) })
	for _, tc := range []struct {
		name      string
		mode      BrowserSandboxMode
		root      bool
		noSandbox bool
	}{
		{"仅沙箱、普通用户", BrowserSandboxOnly, false, false},
		// 默认档宁可起不来也不降级：root 下也带沙箱，由探测报错引导主人换档。
		{"仅沙箱、root", BrowserSandboxOnly, true, false},
		{"自动、普通用户先带沙箱", BrowserSandboxAuto, false, false},
		{"自动、root 直接关", BrowserSandboxAuto, true, true},
		{"关闭", BrowserSandboxOff, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubBrowserRunningAsRoot(t, tc.root)
			SetBrowserSandboxMode(tc.mode)
			for name, args := range map[string][]string{
				"网页渲染":  sandboxedChromeBaseArgsForMode("p", "c", "d", false),
				"内置浏览器": PersistentBrowserArgs("p", "c", "d", true, 0, 1280, 960),
			} {
				if got := slices.Contains(args, "--no-sandbox"); got != tc.noSandbox {
					t.Fatalf("%s 带 --no-sandbox = %v，想要 %v", name, got, tc.noSandbox)
				}
			}
		})
	}
}

func TestBrowserSandboxAutoFallsBackOnceOnSandboxFailure(t *testing.T) {
	t.Setenv("PREFIX", "/usr/local")
	t.Setenv("TERMUX_VERSION", "")
	stubBrowserRunningAsRoot(t, false)
	t.Cleanup(func() { SetBrowserSandboxMode(BrowserSandboxOnly) })
	failure := "headless browser exited before CDP startup: exit status 1: No usable sandbox! Update your kernel"

	SetBrowserSandboxMode(BrowserSandboxOnly)
	if BrowserSandboxFallback(failure) {
		t.Fatal("仅沙箱档不该降级")
	}

	SetBrowserSandboxMode(BrowserSandboxAuto)
	if BrowserSandboxFallback("exit status 1: profile in use") {
		t.Fatal("跟沙箱无关的失败不该降级")
	}
	if !BrowserSandboxFallback(failure) {
		t.Fatal("自动档遇到沙箱失败没有降级")
	}
	if !slices.Contains(sandboxedChromeBaseArgsForMode("p", "c", "d", false), "--no-sandbox") {
		t.Fatal("降级之后的启动仍带沙箱")
	}
	// 并发启动时后到的那次也带着沙箱失败了，同样要重试，而不是直接报错。
	if !BrowserSandboxFallback(failure) {
		t.Fatal("已降级后另一次带沙箱的失败没有重试")
	}
	if status := CurrentBrowserSandbox(); status.Active || status.Reason == "" {
		t.Fatalf("降级后状态不对：%+v", status)
	}

	// 换档清掉降级记录。
	SetBrowserSandboxMode(BrowserSandboxOnly)
	SetBrowserSandboxMode(BrowserSandboxAuto)
	if slices.Contains(sandboxedChromeBaseArgsForMode("p", "c", "d", false), "--no-sandbox") {
		t.Fatal("换档后仍沿用旧的降级记录")
	}
}

func TestBrowserSandboxHintOnlyInSandboxOnlyMode(t *testing.T) {
	t.Setenv("PREFIX", "/usr/local")
	t.Setenv("TERMUX_VERSION", "")
	stubBrowserRunningAsRoot(t, true)
	t.Cleanup(func() { SetBrowserSandboxMode(BrowserSandboxOnly) })
	failure := errors.New("headless browser exited before CDP startup: Running as root without --no-sandbox is not supported")

	SetBrowserSandboxMode(BrowserSandboxOnly)
	got := WithBrowserSandboxHint(failure)
	if !errors.Is(got, failure) || !strings.Contains(got.Error(), "root") || !strings.Contains(got.Error(), "自动") {
		t.Fatalf("仅沙箱档没给出换档提示：%v", got)
	}
	if other := errors.New("exit status 1: profile in use"); WithBrowserSandboxHint(other) != other {
		t.Fatal("跟沙箱无关的失败不该加提示")
	}
	SetBrowserSandboxMode(BrowserSandboxOff)
	if WithBrowserSandboxHint(failure) != failure {
		t.Fatal("不是仅沙箱档不该加提示")
	}
}

func TestHTMLCaptureKeepsRootExemptionInSandboxOnlyMode(t *testing.T) {
	t.Setenv("PREFIX", "/usr/local")
	t.Setenv("TERMUX_VERSION", "")
	SetBrowserSandboxMode(BrowserSandboxOnly)
	stubBrowserRunningAsRoot(t, true)
	// 本地出图在 root 下照旧放行，默认档不能让它跟着坏。
	if args := htmlCaptureExtraArgs(); !slices.Contains(args, "--no-sandbox") {
		t.Fatalf("root 下带脚本的 HTML 出图丢了原有的放行：%v", args)
	}
	stubBrowserRunningAsRoot(t, false)
	if args := htmlCaptureExtraArgs(); slices.Contains(args, "--no-sandbox") {
		t.Fatalf("普通用户的 HTML 出图不该关沙箱：%v", args)
	}
}

func TestParseBrowserSandboxMode(t *testing.T) {
	if mode, err := ParseBrowserSandboxMode(""); err != nil || mode != BrowserSandboxOnly {
		t.Fatalf("空值应为仅沙箱：%q %v", mode, err)
	}
	if _, err := ParseBrowserSandboxMode("disable"); err == nil {
		t.Fatal("接受了不存在的档位")
	}
}

func stubBrowserRunningAsRoot(t *testing.T, root bool) {
	t.Helper()
	previous := browserRunningAsRoot
	browserRunningAsRoot = func() bool { return root }
	t.Cleanup(func() { browserRunningAsRoot = previous })
}

func TestBrowserSandboxPrefixShortensDeepTempDir(t *testing.T) {
	// Termux 的临时目录拼上长前缀，Chrome 单实例套接字会超出 108 字节上限。
	if got := browserSandboxPrefix("/data/data/com.termux/files/usr/tmp", "diana-headless-browser-"); got != shortBrowserTempPrefix {
		t.Fatalf("Termux 临时目录下没换短前缀：%q", got)
	}
	if got := browserSandboxPrefix("/tmp", "diana-headless-browser-"); got != "diana-headless-browser-" {
		t.Fatalf("普通临时目录不该改前缀：%q", got)
	}
}
