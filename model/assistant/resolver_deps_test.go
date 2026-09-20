// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestResolverDependenciesProbes(t *testing.T) {
	deps := ResolverDependencies()
	if len(deps) != len(resolverDependencySpecs) {
		t.Fatalf("期望探测 %d 个依赖，实际 %d", len(resolverDependencySpecs), len(deps))
	}
	for _, dep := range deps {
		if dep.Name == "" || dep.Purpose == "" {
			t.Fatalf("依赖信息不完整：%+v", dep)
		}
		if dep.Available && dep.Path == "" {
			t.Fatalf("%s 可用但没有路径", dep.Name)
		}
		t.Logf("%s available=%v version=%q", dep.Name, dep.Available, dep.Version)
	}
}

func TestResolverDependencyInstallPlanUsesHomebrewWhitelist(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "brew" {
			return "/opt/homebrew/bin/brew", nil
		}
		return "", fmt.Errorf("missing %s", name)
	}
	plan, err := resolverDependencyInstallPlan("yt-dlp", "darwin", lookPath)
	if err != nil {
		t.Fatalf("resolverDependencyInstallPlan() error = %v", err)
	}
	if plan.installer != "Homebrew" || len(plan.commands) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.commands[0].path != "/opt/homebrew/bin/brew" || !reflect.DeepEqual(plan.commands[0].args, []string{"install", "yt-dlp"}) {
		t.Fatalf("command = %#v", plan.commands[0])
	}
}

func TestResolverDependencyInstallPlanRejectsUnknownName(t *testing.T) {
	_, err := resolverDependencyInstallPlan("anything; rm -rf", "darwin", func(string) (string, error) {
		return "/opt/homebrew/bin/brew", nil
	})
	if !errors.Is(err, ErrUnknownResolverDependency) {
		t.Fatalf("error = %v, want ErrUnknownResolverDependency", err)
	}
}

func TestShortVersionKeepsOnlyTheVersionToken(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		// ffmpeg 把版权信息塞在同一行，整行进徽章会被截成「ffmpeg versio...」。
		{"ffmpeg", "ffmpeg version 8.1.2 Copyright (c) 2000-2026 the FFmpeg developers", "8.1.2"},
		{"ffmpeg", "ffmpeg version n7.1-4-gd8f7a0f Copyright (c) 2000-2024", "n7.1-4-gd8f7a0f"},
		// yt-dlp 和 node 本来就只打印版本号，不该被动到。
		{"yt-dlp", "2026.07.04", "2026.07.04"},
		{"node", "v26.3.1", "v26.3.1"},
		// 认不出的格式保持原样，宁可显示得长一点也不要猜错。
		{"ffmpeg", "some unexpected banner text", "some unexpected banner text"},
		{"ffmpeg", "", ""},
	}
	for _, tc := range cases {
		if got := shortVersion(tc.name, tc.line); got != tc.want {
			t.Errorf("shortVersion(%q, %q) = %q，期望 %q", tc.name, tc.line, got, tc.want)
		}
	}
}

// 浏览器和 yt-dlp 走同一套安装机制，但包名各家都不一样，Homebrew 那边还得加
// --cask（装的是 .app 不是命令行包，少了会直接报 "No available formula"）。
func TestResolverDependencyInstallPlanCoversBrowser(t *testing.T) {
	tests := []struct {
		name      string
		goos      string
		manager   string
		path      string
		installer string
		want      [][]string
	}{
		{
			name: "Homebrew 走 cask", goos: "darwin", manager: "brew", path: "/opt/homebrew/bin/brew",
			installer: "Homebrew", want: [][]string{{"install", "--cask", "google-chrome"}},
		},
		{
			name: "apk 装 chromium", goos: "linux", manager: "apk", path: "/sbin/apk",
			installer: "apk", want: [][]string{{"add", "--no-cache", "chromium", "font-noto-cjk"}},
		},
		{
			name: "apt 装 chromium", goos: "linux", manager: "apt-get", path: "/usr/bin/apt-get",
			installer: "apt", want: [][]string{{"update"}, {"install", "-y", "chromium", "fonts-noto-cjk"}},
		},
		{
			name: "pacman 装 chromium", goos: "linux", manager: "pacman", path: "/usr/bin/pacman",
			installer: "pacman", want: [][]string{{"-Sy", "--noconfirm", "chromium"}},
		},
		{
			name: "winget 装 Google.Chrome", goos: "windows", manager: "winget", path: `C:\winget.exe`,
			installer: "winget", want: [][]string{{
				"install", "--id", "Google.Chrome", "--exact",
				"--accept-package-agreements", "--accept-source-agreements",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookPath := func(name string) (string, error) {
				if name == test.manager {
					return test.path, nil
				}
				return "", fmt.Errorf("missing %s", name)
			}
			plan, err := resolverDependencyInstallPlan(browserDependencyName, test.goos, lookPath)
			if err != nil {
				t.Fatalf("resolverDependencyInstallPlan() error = %v", err)
			}
			if plan.installer != test.installer || len(plan.commands) != len(test.want) {
				t.Fatalf("plan = %#v", plan)
			}
			for index, command := range plan.commands {
				if command.path != test.path || !reflect.DeepEqual(command.args, test.want[index]) {
					t.Fatalf("command[%d] = %#v, want %#v", index, command, test.want[index])
				}
			}
		})
	}
}

// 白名单之外的名字必须挡住：可执行文件和参数只能由安装计划生成，不能由请求决定。
func TestResolverDependencyInstallPlanRejectsUnknownNames(t *testing.T) {
	lookPath := func(string) (string, error) { return "/usr/bin/apt-get", nil }
	for _, name := range []string{"curl", "chromium", "", "chrome; rm -rf /"} {
		if _, err := resolverDependencyInstallPlan(name, "linux", lookPath); !errors.Is(err, ErrUnknownResolverDependency) {
			t.Fatalf("name=%q err=%v", name, err)
		}
	}
}

func TestDependencyInstallPermissionDenied(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "apk 权限拒绝", output: "apk: Permission denied", want: true},
		{name: "apt 需要 root", output: "E: Could not open lock file - open (13: Permission denied)", want: true},
		{name: "macOS 未授权", output: "installer: not authorized", want: true},
		{name: "只读文件系统", output: "mkdir: can't create directory: Read-only file system", want: true},
		{name: "网络错误不算权限问题", output: "apk: network error (try again)", want: false},
		{name: "找不到包不算权限问题", output: "ERROR: No such package: chromium", want: false},
		{name: "空输出", output: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dependencyInstallPermissionDenied(tc.output); got != tc.want {
				t.Fatalf("dependencyInstallPermissionDenied(%q) = %v，期望 %v", tc.output, got, tc.want)
			}
		})
	}
}

func TestRunDependencyInstallPlanSurfacesManualCommandOnPermissionDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过需要 /bin/sh 的集成测试")
	}
	plan := resolverInstallPlan{
		installer: "sh",
		commands: []resolverInstallCommand{{
			path: "/bin/sh",
			args: []string{"-c", "echo apk: Permission denied; exit 1"},
		}},
	}
	err := runDependencyInstallPlan(t.Context(), plan, "browser-renderer")
	if err == nil {
		t.Fatal("期望安装失败，实际成功")
	}
	if !strings.Contains(err.Error(), "没有包管理器权限") {
		t.Fatalf("错误里缺少权限提示：%v", err)
	}
	if !strings.Contains(err.Error(), "/bin/sh -c") {
		t.Fatalf("错误里缺少手动执行命令：%v", err)
	}
}
