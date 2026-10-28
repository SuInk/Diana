// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"slices"
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
	if args := sandboxedChromeBaseArgsForMode("p", "c", "d", false); slices.Contains(args, "--no-sandbox") {
		t.Fatal("不在 Termux 时把沙盒关掉了")
	}
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
