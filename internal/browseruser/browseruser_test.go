// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package browseruser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookupNeverCreatesUsersInTests(t *testing.T) {
	// CI 容器以 root 跑测试，不能真去 useradd。
	if _, ok := Lookup(); ok {
		t.Fatal("测试里 Lookup 给出了账号")
	}
	if DownloadDir() != "" {
		t.Fatal("测试里 DownloadDir 不该指向共享目录")
	}
}

func TestCanReach(t *testing.T) {
	dir := t.TempDir()
	open := filepath.Join(dir, "open")
	locked := filepath.Join(dir, "locked")
	for _, d := range []string{open, locked} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(open, "chrome")
	hidden := filepath.Join(locked, "chrome")
	for _, f := range []string{exe, hidden} {
		if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	me := Account{UID: os.Getuid(), GID: os.Getgid()}
	if !me.CanReach(exe) || !me.CanReach(hidden) {
		t.Fatal("目录主人自己够不着")
	}
	// 外人：t.TempDir() 的上层未必对外人开放，只比较两个兄弟目录的差别。
	stranger := Account{UID: os.Getuid() + 1, GID: os.Getgid() + 1}
	if stranger.CanReach(hidden) {
		t.Fatal("外人穿过了 0700 目录")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if stranger.CanTraverse(filepath.Dir(dir)) && !stranger.CanReach(exe) {
		t.Fatal("外人够不着 0755 目录里的可执行文件")
	}
}

func TestOwnSkipsAlreadyOwned(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	me := Account{UID: os.Getuid(), GID: os.Getgid()}
	if err := me.Own(dir, ""); err != nil {
		t.Fatal(err)
	}
}
