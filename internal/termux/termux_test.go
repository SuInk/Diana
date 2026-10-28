// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package termux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrefixFromEnvironment(t *testing.T) {
	if _, err := os.Stat(defaultPrefix); err == nil {
		t.Skip("这台机器本身就是 Termux")
	}
	prefix := "/data/data/com.termux/files/usr"
	t.Setenv("PREFIX", prefix)
	t.Setenv("TERMUX_VERSION", "")
	if !Detected() || Prefix() != prefix {
		t.Fatalf("PREFIX 指向 Termux 却没认出来：%q", Prefix())
	}
	want := []string{filepath.Join(prefix, "bin", "chromium-browser"), filepath.Join(prefix, "bin", "chromium")}
	if got := ChromiumPaths(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Chromium 路径不对：%v", got)
	}

	// 服务管理器洗掉 PREFIX 时，TERMUX_VERSION 还在就照样认。
	t.Setenv("PREFIX", "")
	t.Setenv("TERMUX_VERSION", "0.118.1")
	if Prefix() != defaultPrefix {
		t.Fatalf("只有 TERMUX_VERSION 时没认出来：%q", Prefix())
	}
}

func TestPrefixOutsideTermux(t *testing.T) {
	if _, err := os.Stat(defaultPrefix); err == nil {
		t.Skip("这台机器本身就是 Termux")
	}
	// 普通 Linux 也常设 PREFIX（比如 /usr/local），不能当成 Termux。
	t.Setenv("PREFIX", "/usr/local")
	t.Setenv("TERMUX_VERSION", "")
	if Detected() || ChromiumPaths() != nil {
		t.Fatal("普通 Linux 被当成了 Termux")
	}
}
