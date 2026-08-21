// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	resolverDependencyInstallTimeout = 15 * time.Minute
	resolverInstallerOutputLimit     = 64 * 1024
)

var (
	ErrUnknownResolverDependency    = errors.New("未知的解析器依赖")
	ErrResolverInstallerUnavailable = errors.New("当前系统没有受支持的包管理器")
	resolverDependencyInstallMu     sync.Mutex
	resolverDepsMu                  sync.RWMutex
	resolverDepsCache               []ResolverDependency
)

// resolverCommandSearchDirs 是当前 PATH 之外还要搜的常见安装目录。
// launchd 和 systemd 启动的服务进程通常只继承 /usr/bin:/bin:/usr/sbin:/sbin，
// 而 Homebrew 装在 /opt/homebrew/bin（Apple Silicon）或 /usr/local/bin（Intel），
// pipx 装在 ~/.local/bin。少了这些目录，明明装好的 yt-dlp/ffmpeg/node 在服务
// 里会全部「找不到」，连包管理器都探测不到，界面上只剩「需手动安装」。
func resolverCommandSearchDirs() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}
	if runtime.GOOS == "linux" {
		dirs = append(dirs, "/snap/bin", "/home/linuxbrew/.linuxbrew/bin")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"))
	}
	return dirs
}

// lookResolverCommand 先按 PATH 查，再退回常见安装目录，返回可直接执行的绝对路径。
func lookResolverCommand(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, dir := range resolverCommandSearchDirs() {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

// resolverCommandEnv 把上面这些目录补进子进程的 PATH。
// 只给出绝对路径不够：yt-dlp 会自己去调 ffmpeg，子进程照样得找得到。
func resolverCommandEnv() []string {
	dirs := resolverCommandSearchDirs()
	if len(dirs) == 0 {
		return nil
	}
	env := os.Environ()
	for index, entry := range env {
		if !strings.HasPrefix(entry, "PATH=") {
			continue
		}
		existing := strings.Split(strings.TrimPrefix(entry, "PATH="), string(os.PathListSeparator))
		seen := make(map[string]bool, len(existing))
		for _, item := range existing {
			seen[item] = true
		}
		merged := existing
		for _, dir := range dirs {
			if !seen[dir] {
				merged = append(merged, dir)
			}
		}
		env[index] = "PATH=" + strings.Join(merged, string(os.PathListSeparator))
		return env
	}
	return append(env, "PATH="+strings.Join(dirs, string(os.PathListSeparator)))
}

// ResolverDependency 描述解析器依赖的一个外部命令。
type ResolverDependency struct {
	Name string `json:"name"`
	// Purpose 说明缺了它会失去什么能力，便于用户判断要不要装。
	Purpose   string `json:"purpose"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	// Detail 在不可用时说明卡在哪一步。有的依赖没法一键安装，只说一句
	// 「需手动安装」等于让用户自己去猜是没装、装错架构还是路径没配。
	Detail      string `json:"detail,omitempty"`
	Installable bool   `json:"installable"`
	Installer   string `json:"installer,omitempty"`
}

// ResolverDependencyInstallResult 是安装后返回给控制台的最新依赖状态。
type ResolverDependencyInstallResult struct {
	Dependency ResolverDependency `json:"dependency"`
	// Resolver 是链接解析那一组，保留给旧字段。
	Resolver []ResolverDependency `json:"resolver"`
	// Plugins 按插件 ID 分组，界面据此只更新受影响的那一组。
	Plugins   map[string][]ResolverDependency `json:"plugins,omitempty"`
	Installer string                          `json:"installer,omitempty"`
}

type resolverDependencySpec struct {
	name        string
	purpose     string
	versionArgs []string
}

// resolverDependencySpecs 是链接解析插件允许探测和安装的完整白名单。
var resolverDependencySpecs = []resolverDependencySpec{
	{name: "yt-dlp", purpose: "YouTube / X 等平台的视频下载", versionArgs: []string{"--version"}},
	{name: "ffmpeg", purpose: "B 站音视频分离流的合并", versionArgs: []string{"-version"}},
	{name: "node", purpose: "抖音接口签名（a-bogus）", versionArgs: []string{"--version"}},
}

type resolverInstallCommand struct {
	path string
	args []string
}

type resolverInstallPlan struct {
	installer string
	commands  []resolverInstallCommand
}

// ResolverDependencies 返回缓存的外部依赖探测结果。
func ResolverDependencies() []ResolverDependency {
	resolverDepsMu.RLock()
	if resolverDepsCache != nil {
		out := cloneResolverDependencies(resolverDepsCache)
		resolverDepsMu.RUnlock()
		return out
	}
	resolverDepsMu.RUnlock()
	return RefreshResolverDependencies()
}

// RefreshResolverDependencies 重新探测外部命令，供安装完成后立即刷新页面状态。
func RefreshResolverDependencies() []ResolverDependency {
	deps := probeResolverDependencies()
	resolverDepsMu.Lock()
	resolverDepsCache = cloneResolverDependencies(deps)
	resolverDepsMu.Unlock()
	return deps
}

// InstallResolverDependency 使用受控的系统包管理器安装白名单中的依赖。
// 用户输入永远不会被拼进 shell；每条可执行文件和参数都由安装计划生成。
func InstallResolverDependency(ctx context.Context, name string) (ResolverDependencyInstallResult, error) {
	name = strings.TrimSpace(name)
	if !installableDependency(name) {
		return ResolverDependencyInstallResult{}, fmt.Errorf("%w：%s", ErrUnknownResolverDependency, name)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	resolverDependencyInstallMu.Lock()
	defer resolverDependencyInstallMu.Unlock()

	if name == browserDependencyName {
		return installBrowserDependency(ctx)
	}

	deps := RefreshResolverDependencies()
	if dep, ok := resolverDependencyByName(deps, name); ok && dep.Available {
		return ResolverDependencyInstallResult{Dependency: dep, Resolver: deps, Plugins: resolverDependencyGroup(deps)}, nil
	}

	plan, err := resolverDependencyInstallPlan(name, runtime.GOOS, lookResolverCommand)
	if err != nil {
		return ResolverDependencyInstallResult{}, err
	}

	if err := runDependencyInstallPlan(ctx, plan, name); err != nil {
		return ResolverDependencyInstallResult{}, err
	}

	deps = RefreshResolverDependencies()
	dep, ok := resolverDependencyByName(deps, name)
	if !ok || !dep.Available {
		return ResolverDependencyInstallResult{}, fmt.Errorf("%s 已执行，但 %s 仍不在服务进程的 PATH 中", plan.installer, name)
	}
	return ResolverDependencyInstallResult{Dependency: dep, Resolver: deps, Plugins: resolverDependencyGroup(deps), Installer: plan.installer}, nil
}

func resolverDependencyGroup(deps []ResolverDependency) map[string][]ResolverDependency {
	return map[string][]ResolverDependency{ResolverPluginID: deps}
}

// runDependencyInstallPlan 按顺序执行安装计划。失败时把包管理器自己的输出带出来——
// 「apt 安装 chromium 失败」不说清是没有这个包还是没权限，用户无从下手。
func runDependencyInstallPlan(ctx context.Context, plan resolverInstallPlan, name string) error {
	installCtx, cancel := context.WithTimeout(ctx, resolverDependencyInstallTimeout)
	defer cancel()
	for _, command := range plan.commands {
		output := &limitedCommandOutput{remaining: resolverInstallerOutputLimit}
		cmd := exec.CommandContext(installCtx, command.path, command.args...)
		cmd.Env = resolverCommandEnv()
		cmd.Stdout = output
		cmd.Stderr = output
		if err := cmd.Run(); err != nil {
			if installCtx.Err() != nil {
				err = installCtx.Err()
			}
			detail := strings.TrimSpace(output.String())
			if detail != "" {
				detail = "：" + detail
			}
			return fmt.Errorf("%s 安装 %s 失败：%w%s", plan.installer, name, err, detail)
		}
	}
	return nil
}

func probeResolverDependencies() []ResolverDependency {
	out := make([]ResolverDependency, 0, len(resolverDependencySpecs))
	for _, spec := range resolverDependencySpecs {
		dep := ResolverDependency{Name: spec.name, Purpose: spec.purpose}
		// 可用性以「版本命令真的跑通」为准，而不是文件存在：架构不匹配、
		// 符号链接悬空、缺动态库的命令都能骗过存在性检查，却一执行就失败。
		if path, version, ok := probeResolverCommand(spec.name, spec.versionArgs); ok {
			dep.Available = true
			dep.Path = path
			dep.Version = version
		} else if plan, planErr := resolverDependencyInstallPlan(spec.name, runtime.GOOS, lookResolverCommand); planErr == nil {
			dep.Installable = true
			dep.Installer = plan.installer
		}
		out = append(out, dep)
	}
	return out
}

func resolverDependencyInstallPlan(name, goos string, lookPath func(string) (string, error)) (resolverInstallPlan, error) {
	if !installableDependency(name) {
		return resolverInstallPlan{}, fmt.Errorf("%w：%s", ErrUnknownResolverDependency, name)
	}

	type managerSpec struct {
		command string
		label   string
		build   func(string, string) []resolverInstallCommand
	}
	brew := managerSpec{command: "brew", label: "Homebrew", build: func(path, dependency string) []resolverInstallCommand {
		args := []string{"install"}
		// 浏览器在 Homebrew 里是 cask（装的是 .app，不是命令行包），少了 --cask
		// 会直接报 "No available formula"。
		if resolverBrewCask(dependency) {
			args = append(args, "--cask")
		}
		return []resolverInstallCommand{{path: path, args: append(args, resolverPackageName(dependency, "brew"))}}
	}}
	linuxManagers := []managerSpec{
		{command: "apk", label: "apk", build: func(path, dependency string) []resolverInstallCommand {
			return []resolverInstallCommand{{path: path, args: []string{"add", "--no-cache", resolverPackageName(dependency, "apk")}}}
		}},
		{command: "apt-get", label: "apt", build: func(path, dependency string) []resolverInstallCommand {
			return []resolverInstallCommand{
				{path: path, args: []string{"update"}},
				{path: path, args: []string{"install", "-y", resolverPackageName(dependency, "apt")}},
			}
		}},
		{command: "dnf", label: "dnf", build: func(path, dependency string) []resolverInstallCommand {
			return []resolverInstallCommand{{path: path, args: []string{"install", "-y", resolverPackageName(dependency, "dnf")}}}
		}},
		{command: "yum", label: "yum", build: func(path, dependency string) []resolverInstallCommand {
			return []resolverInstallCommand{{path: path, args: []string{"install", "-y", resolverPackageName(dependency, "yum")}}}
		}},
		{command: "pacman", label: "pacman", build: func(path, dependency string) []resolverInstallCommand {
			return []resolverInstallCommand{{path: path, args: []string{"-Sy", "--noconfirm", resolverPackageName(dependency, "pacman")}}}
		}},
		brew,
	}
	windowsManagers := []managerSpec{
		{command: "winget", label: "winget", build: func(path, dependency string) []resolverInstallCommand {
			return []resolverInstallCommand{{path: path, args: []string{
				"install", "--id", resolverPackageName(dependency, "winget"), "--exact",
				"--accept-package-agreements", "--accept-source-agreements",
			}}}
		}},
		{command: "choco", label: "Chocolatey", build: func(path, dependency string) []resolverInstallCommand {
			return []resolverInstallCommand{{path: path, args: []string{"install", resolverPackageName(dependency, "choco"), "-y"}}}
		}},
	}

	var managers []managerSpec
	switch goos {
	case "darwin":
		managers = []managerSpec{brew}
	case "linux":
		managers = linuxManagers
	case "windows":
		managers = windowsManagers
	}
	for _, manager := range managers {
		path, err := lookPath(manager.command)
		if err == nil {
			return resolverInstallPlan{installer: manager.label, commands: manager.build(path, name)}, nil
		}
	}
	return resolverInstallPlan{}, fmt.Errorf("%w，无法自动安装 %s", ErrResolverInstallerUnavailable, name)
}

// installableDependency 是允许自动安装的白名单。参数永远不会被拼进 shell，
// 但仍然要挡住白名单之外的名字：可执行文件和参数都只能由安装计划生成。
func installableDependency(name string) bool {
	if _, ok := resolverDependencySpecByName(name); ok {
		return true
	}
	return name == browserDependencyName
}

// resolverBrewCask 标出 Homebrew 里属于 cask 的依赖。
func resolverBrewCask(name string) bool {
	return name == browserDependencyName
}

func resolverPackageName(name, manager string) string {
	if name == browserDependencyName {
		// 各家包名不一样，Linux 上一律装 chromium：google-chrome 只在 Google 自己的
		// 源里，为了装个浏览器去偷偷加第三方 apt 源不合适。
		switch manager {
		case "apk", "apt", "dnf", "yum", "pacman":
			return "chromium"
		case "brew":
			return "google-chrome"
		case "winget":
			return "Google.Chrome"
		case "choco":
			return "googlechrome"
		}
	}
	if name == "node" {
		switch manager {
		case "apk", "apt", "dnf", "yum", "pacman":
			return "nodejs"
		case "winget":
			return "OpenJS.NodeJS.LTS"
		case "choco":
			return "nodejs-lts"
		}
	}
	if manager == "winget" {
		switch name {
		case "yt-dlp":
			return "yt-dlp.yt-dlp"
		case "ffmpeg":
			return "Gyan.FFmpeg"
		}
	}
	return name
}

func resolverDependencySpecByName(name string) (resolverDependencySpec, bool) {
	for _, spec := range resolverDependencySpecs {
		if spec.name == name {
			return spec, true
		}
	}
	return resolverDependencySpec{}, false
}

func resolverDependencyByName(deps []ResolverDependency, name string) (ResolverDependency, bool) {
	for _, dep := range deps {
		if dep.Name == name {
			return dep, true
		}
	}
	return ResolverDependency{}, false
}

func cloneResolverDependencies(deps []ResolverDependency) []ResolverDependency {
	out := make([]ResolverDependency, len(deps))
	copy(out, deps)
	return out
}

// probeResolverCommand 定位命令并执行它的版本参数：跑通才算可用。
// 顺带把绝对路径和版本一起带回来，省掉「先 LookPath 再执行」的重复查找。
func probeResolverCommand(name string, versionArgs []string) (string, string, bool) {
	path, err := lookResolverCommand(name)
	if err != nil {
		return "", "", false
	}
	version, ok := runCommandVersion(path, name, versionArgs)
	if !ok {
		return "", "", false
	}
	return path, version, true
}

// probeCommandVersion 取版本号首行，拿不到就留空；版本只用于展示。
func probeCommandVersion(name string, args []string) string {
	path, err := lookResolverCommand(name)
	if err != nil {
		return ""
	}
	version, _ := runCommandVersion(path, name, args)
	return version
}

// runCommandVersion 执行版本参数。第二个返回值区分「跑通了但没打印版本」和
// 「根本跑不起来」：前者仍算可用，后者不算。
func runCommandVersion(path, name string, args []string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = resolverCommandEnv()
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)[0])
	line = shortVersion(name, line)
	if len(line) > 120 {
		line = line[:120]
	}
	return line, true
}

// shortVersion 从版本首行里挑出版本号本身。yt-dlp 和 node 直接就打印版本号，
// 但 ffmpeg 打的是「ffmpeg version 8.1.2 Copyright (c) 2000-2026 ...」，整行
// 交给界面只会被截断成「ffmpeg versio...」，版本号反而一个字都看不见。
func shortVersion(name, line string) string {
	fields := strings.Fields(line)
	if len(fields) >= 3 && strings.EqualFold(fields[1], "version") &&
		(strings.EqualFold(fields[0], name) || strings.EqualFold(fields[0], filepath.Base(name))) {
		return fields[2]
	}
	return line
}

type limitedCommandOutput struct {
	mu        sync.Mutex
	builder   strings.Builder
	remaining int
	truncated bool
}

func (w *limitedCommandOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := len(data)
	if w.remaining <= 0 {
		w.truncated = true
		return written, nil
	}
	keep := len(data)
	if keep > w.remaining {
		keep = w.remaining
		w.truncated = true
	}
	_, _ = w.builder.Write(data[:keep])
	w.remaining -= keep
	return written, nil
}

func (w *limitedCommandOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.builder.String()
	if w.truncated {
		out += "\n[输出已截断]"
	}
	return out
}
