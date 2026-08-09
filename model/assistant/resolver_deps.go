package assistant

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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

// ResolverDependency 描述解析器依赖的一个外部命令。
type ResolverDependency struct {
	Name string `json:"name"`
	// Purpose 说明缺了它会失去什么能力，便于用户判断要不要装。
	Purpose     string `json:"purpose"`
	Available   bool   `json:"available"`
	Path        string `json:"path,omitempty"`
	Version     string `json:"version,omitempty"`
	Installable bool   `json:"installable"`
	Installer   string `json:"installer,omitempty"`
}

// ResolverDependencyInstallResult 是安装后返回给控制台的最新依赖状态。
type ResolverDependencyInstallResult struct {
	Dependency ResolverDependency   `json:"dependency"`
	Resolver   []ResolverDependency `json:"resolver"`
	Installer  string               `json:"installer,omitempty"`
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
	if _, ok := resolverDependencySpecByName(name); !ok {
		return ResolverDependencyInstallResult{}, fmt.Errorf("%w：%s", ErrUnknownResolverDependency, name)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	resolverDependencyInstallMu.Lock()
	defer resolverDependencyInstallMu.Unlock()

	deps := RefreshResolverDependencies()
	if dep, ok := resolverDependencyByName(deps, name); ok && dep.Available {
		return ResolverDependencyInstallResult{Dependency: dep, Resolver: deps}, nil
	}

	plan, err := resolverDependencyInstallPlan(name, runtime.GOOS, exec.LookPath)
	if err != nil {
		return ResolverDependencyInstallResult{}, err
	}

	installCtx, cancel := context.WithTimeout(ctx, resolverDependencyInstallTimeout)
	defer cancel()
	for _, command := range plan.commands {
		output := &limitedCommandOutput{remaining: resolverInstallerOutputLimit}
		cmd := exec.CommandContext(installCtx, command.path, command.args...)
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
			return ResolverDependencyInstallResult{}, fmt.Errorf("%s 安装 %s 失败：%w%s", plan.installer, name, err, detail)
		}
	}

	deps = RefreshResolverDependencies()
	dep, ok := resolverDependencyByName(deps, name)
	if !ok || !dep.Available {
		return ResolverDependencyInstallResult{}, fmt.Errorf("%s 已执行，但 %s 仍不在服务进程的 PATH 中", plan.installer, name)
	}
	return ResolverDependencyInstallResult{Dependency: dep, Resolver: deps, Installer: plan.installer}, nil
}

func probeResolverDependencies() []ResolverDependency {
	out := make([]ResolverDependency, 0, len(resolverDependencySpecs))
	for _, spec := range resolverDependencySpecs {
		dep := ResolverDependency{Name: spec.name, Purpose: spec.purpose}
		path, err := exec.LookPath(spec.name)
		if err == nil {
			dep.Available = true
			dep.Path = path
			dep.Version = probeCommandVersion(spec.name, spec.versionArgs)
		} else if plan, planErr := resolverDependencyInstallPlan(spec.name, runtime.GOOS, exec.LookPath); planErr == nil {
			dep.Installable = true
			dep.Installer = plan.installer
		}
		out = append(out, dep)
	}
	return out
}

func resolverDependencyInstallPlan(name, goos string, lookPath func(string) (string, error)) (resolverInstallPlan, error) {
	if _, ok := resolverDependencySpecByName(name); !ok {
		return resolverInstallPlan{}, fmt.Errorf("%w：%s", ErrUnknownResolverDependency, name)
	}

	type managerSpec struct {
		command string
		label   string
		build   func(string, string) []resolverInstallCommand
	}
	brew := managerSpec{command: "brew", label: "Homebrew", build: func(path, dependency string) []resolverInstallCommand {
		return []resolverInstallCommand{{path: path, args: []string{"install", dependency}}}
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

func resolverPackageName(name, manager string) string {
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

// probeCommandVersion 取版本号首行，拿不到就留空；版本只用于展示。
func probeCommandVersion(name string, args []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)[0])
	if len(line) > 120 {
		line = line[:120]
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
