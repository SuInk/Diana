package assistant

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLookResolverCommandFindsBrewOutsideServicePath(t *testing.T) {
	// launchd 启动的服务只继承 /usr/bin:/bin:/usr/sbin:/sbin，Homebrew 却装在
	// /opt/homebrew/bin。只查 PATH 会让装好的依赖全部显示「需手动安装」。
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不走这些目录")
	}
	dir := t.TempDir()
	name := "diana-fake-dep"
	binary := filepath.Join(dir, name)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 把候选目录指向临时目录：用 HOME/bin 这条，不依赖机器上真有 Homebrew。
	t.Setenv("HOME", dir)
	homeBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(homeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(binary, filepath.Join(homeBin, name)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")

	if _, err := exec.LookPath(name); err == nil {
		t.Fatal("precondition: command should be invisible via PATH")
	}
	got, err := lookResolverCommand(name)
	if err != nil {
		t.Fatalf("lookResolverCommand() error = %v, want the command found outside PATH", err)
	}
	if got != filepath.Join(homeBin, name) {
		t.Fatalf("lookResolverCommand() = %q", got)
	}
}

func TestLookResolverCommandStillReportsMissing(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	if _, err := lookResolverCommand("diana-definitely-not-installed"); err == nil {
		t.Fatal("missing command should stay missing")
	}
}

func TestResolverCommandEnvAppendsSearchDirs(t *testing.T) {
	// 给出绝对路径还不够：yt-dlp 会自己去调 ffmpeg，子进程也得找得到。
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不走这些目录")
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	var pathEntry string
	for _, entry := range resolverCommandEnv() {
		if strings.HasPrefix(entry, "PATH=") {
			pathEntry = entry
		}
	}
	if pathEntry == "" {
		t.Fatal("resolverCommandEnv() produced no PATH")
	}
	if !strings.Contains(pathEntry, "/usr/bin") {
		t.Fatalf("existing PATH was dropped: %q", pathEntry)
	}
	if !strings.Contains(pathEntry, "/opt/homebrew/bin") {
		t.Fatalf("PATH missing Homebrew dir: %q", pathEntry)
	}
}

func TestResolverDependencyInstallPlanUsesResolvedLookup(t *testing.T) {
	// 包管理器同样可能不在服务 PATH 里；找得到就应该给出安装计划。
	if runtime.GOOS != "darwin" {
		t.Skip("这条断言针对 macOS 的 Homebrew 路径")
	}
	plan, err := resolverDependencyInstallPlan("ffmpeg", "darwin", func(string) (string, error) {
		return "/opt/homebrew/bin/brew", nil
	})
	if err != nil {
		t.Fatalf("resolverDependencyInstallPlan() error = %v", err)
	}
	if plan.installer != "Homebrew" || len(plan.commands) != 1 || plan.commands[0].path != "/opt/homebrew/bin/brew" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestProbeResolverCommandRequiresTheCommandToRun(t *testing.T) {
	// 只看文件存在会把跑不起来的命令当成已安装：架构不匹配、悬空符号链接、
	// 缺动态库都属于这种。以版本命令跑通为准才不会骗人。
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的可执行判定不同")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	homeBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(homeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")

	broken := "diana-broken-dep"
	if err := os.WriteFile(filepath.Join(homeBin, broken), []byte("not a real program"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := probeResolverCommand(broken, []string{"--version"}); ok {
		t.Fatal("a file that cannot execute must not count as available")
	}

	working := "diana-working-dep"
	if err := os.WriteFile(filepath.Join(homeBin, working), []byte("#!/bin/sh\necho 1.2.3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, version, ok := probeResolverCommand(working, []string{"--version"})
	if !ok || version != "1.2.3" || path != filepath.Join(homeBin, working) {
		t.Fatalf("probeResolverCommand() = (%q, %q, %t)", path, version, ok)
	}
}
