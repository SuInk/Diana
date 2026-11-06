// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

//go:build !windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SuInk/diana/internal/browseruser"
)

// fakeBrowserExecutable 在 dir 下放一个可执行文件，并让默认查找找到它。
func fakeBrowserExecutable(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "chrome")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIANA_HEADLESS_BROWSER_EXECUTABLE", path)
	return path
}

func TestRootWithAccountKeepsSandbox(t *testing.T) {
	t.Setenv("PREFIX", "/usr/local")
	t.Setenv("TERMUX_VERSION", "")
	t.Cleanup(func() { SetBrowserSandboxMode(BrowserSandboxOnly) })
	stubBrowserRunningAsRoot(t, true)
	executable := fakeBrowserExecutable(t, t.TempDir())
	// 用当前用户冒充 diana：临时目录归它，够得着。
	t.Cleanup(browseruser.SetLookupForTest(browseruser.Account{UID: os.Getuid(), GID: os.Getgid(), Home: t.TempDir()}, true, ""))

	for _, mode := range []BrowserSandboxMode{BrowserSandboxOnly, BrowserSandboxAuto} {
		SetBrowserSandboxMode(mode)
		if status := CurrentBrowserSandbox(); !status.Active {
			t.Fatalf("%s 档、root 能降权时沙箱被关了：%+v", mode, status)
		}
		if slices.Contains(sandboxedChromeBaseArgsForMode("p", "c", "d", false), "--no-sandbox") {
			t.Fatalf("%s 档、root 能降权时网页渲染带了 --no-sandbox", mode)
		}
		if slices.Contains(htmlCaptureExtraArgs(), "--no-sandbox") {
			t.Fatalf("%s 档、root 能降权时出图仍放行 --no-sandbox", mode)
		}
	}
	// 「自动」档遇到真正的沙箱失败（比如 AppArmor 拦了用户命名空间）照样可以降级。
	if !BrowserSandboxFallback("No usable sandbox!") {
		t.Fatal("root 已降权时自动档失去了降级")
	}

	cmd := exec.Command(executable)
	work := t.TempDir()
	if err := applyBrowserAccount(cmd, executable, []string{"--headless=new"}, work); err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil || int(cmd.SysProcAttr.Credential.Uid) != os.Getuid() {
		t.Fatalf("没换成降权账号启动：%+v", cmd.SysProcAttr)
	}
	if len(cmd.SysProcAttr.Credential.Groups) != 0 {
		t.Fatal("降权后还带着 root 的附加组")
	}
	plain := exec.Command(executable)
	if err := applyBrowserAccount(plain, executable, []string{"--no-sandbox"}, work); err != nil {
		t.Fatal(err)
	}
	if plain.SysProcAttr != nil && plain.SysProcAttr.Credential != nil {
		t.Fatal("本来就不带沙箱的启动不该降权：那条路要读 root 家目录里的字体")
	}
}

func TestRootWithUnreachableExecutableExplains(t *testing.T) {
	t.Setenv("PREFIX", "/usr/local")
	t.Setenv("TERMUX_VERSION", "")
	t.Cleanup(func() { SetBrowserSandboxMode(BrowserSandboxOnly) })
	stubBrowserRunningAsRoot(t, true)
	locked := filepath.Join(t.TempDir(), "root-only")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeBrowserExecutable(t, locked)
	// 换一个不是目录主人的 uid：进不去 0700 的目录，就像 diana 进不去 /root。
	t.Cleanup(browseruser.SetLookupForTest(browseruser.Account{UID: os.Getuid() + 1, GID: os.Getgid() + 1, Home: t.TempDir()}, true, ""))

	reason := browserRootWithoutAccount()
	if !strings.Contains(reason, "重新安装浏览器") {
		t.Fatalf("够不着浏览器时没说怎么办：%q", reason)
	}
	SetBrowserSandboxMode(BrowserSandboxAuto)
	if status := CurrentBrowserSandbox(); status.Active {
		t.Fatalf("自动档、够不着浏览器时还当能带沙箱：%+v", status)
	}
	SetBrowserSandboxMode(BrowserSandboxOnly)
	if !slices.Contains(htmlCaptureExtraArgs(), "--no-sandbox") {
		t.Fatal("降不了权时出图丢了 root 放行")
	}
}

func TestRootWithoutAccountReportsWhy(t *testing.T) {
	t.Setenv("PREFIX", "/usr/local")
	t.Setenv("TERMUX_VERSION", "")
	stubBrowserRunningAsRoot(t, true)
	t.Cleanup(browseruser.SetLookupForTest(browseruser.Account{}, false, "系统里没有 useradd/adduser，建不了用户"))
	if reason := browserRootWithoutAccount(); !strings.Contains(reason, "useradd") {
		t.Fatalf("没带上建不了用户的原因：%q", reason)
	}
}
