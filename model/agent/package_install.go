// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// MaxInstallPackages 限制一次装几个包：要装一长串多半是模型在乱试。
	MaxInstallPackages    = 8
	packageInstallTimeout = 5 * time.Minute
	packageInstallOutput  = 4000
)

// npmPackageSpec 只收注册表里的包名和可选版本，不收 URL、git 地址、本地路径和
// 以 - 开头的参数：那几种都能让 npm 去拉任意来源或改掉安装行为。
var npmPackageSpec = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*(@[A-Za-z0-9.^~<>=*|+ -]{1,40})?$`)

// ValidateNPMPackages 检查包名并去重。
func ValidateNPMPackages(packages []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(packages))
	for _, raw := range packages {
		spec := strings.TrimSpace(raw)
		if spec == "" || seen[spec] {
			continue
		}
		if len(spec) > 214 || !npmPackageSpec.MatchString(spec) || strings.Contains(spec, "..") {
			return nil, fmt.Errorf("不支持的包名 %q：只能写 npm 注册表里的包名，可带 @版本", spec)
		}
		seen[spec] = true
		out = append(out, spec)
	}
	if len(out) == 0 {
		return nil, errors.New("packages is required")
	}
	if len(out) > MaxInstallPackages {
		return nil, fmt.Errorf("一次最多装 %d 个包", MaxInstallPackages)
	}
	return out, nil
}

// InstallNPMPackages 把包装进工作区根目录的 node_modules：工作区里任何脚本都能
// 直接 require，不碰全局目录，也不需要 root。环境变量走和 run_command 相同的凭据过滤。
func InstallNPMPackages(ctx context.Context, cfg Config, packages []string) (string, error) {
	root, mcpConfigPath := cfg.WorkDir, cfg.MCPConfigPath
	packages, err := ValidateNPMPackages(packages)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	env := withExecutionSearchPath(commandEnvironmentFor(os.Environ(), mcpConfigPath))
	npm, err := lookPathIn("npm", env)
	if err != nil {
		return "", errors.New("没有找到 npm：这台机器没装 Node.js，需要主人先安装")
	}
	runCtx, cancel := context.WithTimeout(ctx, packageInstallTimeout)
	defer cancel()
	cmd, err := packageInstallCommand(runCtx, cfg, npm, packages, detectCommandSandbox())
	if err != nil {
		return "", err
	}
	cmd.Dir = root
	cmd.Env = env
	var output cappedBuffer
	output.limit = packageInstallOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	runErr := cmd.Run()
	text := strings.TrimSpace(output.String())
	if runCtx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("安装超过 %s 未完成，已终止", packageInstallTimeout)
	}
	if runErr != nil {
		return text, fmt.Errorf("npm install 失败：%v", runErr)
	}
	return text, nil
}

// NPMModulesDir 是安装落点，告诉模型脚本放哪都能 require 到。
func NPMModulesDir(root string) string { return filepath.Join(root, "node_modules") }

func lookPathIn(name string, env []string) (string, error) {
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, "PATH="); ok {
			for _, dir := range filepath.SplitList(value) {
				if dir == "" || !filepath.IsAbs(dir) {
					continue
				}
				candidate := filepath.Join(dir, name)
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
					return candidate, nil
				}
			}
		}
	}
	return exec.LookPath(name)
}

// 与 run_command 共用沙盒，require 不得降级，网络沿用机器人配置。
func packageInstallCommand(ctx context.Context, cfg Config, npm string, packages []string, sandbox commandSandbox) (*exec.Cmd, error) {
	runner := &RunCommandTool{root: cfg.WorkDir, mcpConfigPath: cfg.MCPConfigPath, sandboxMode: cfg.CommandSandbox, sandboxNetwork: cfg.CommandSandboxAllowNetwork, sandbox: sandbox}
	args := append([]string{"install", "--prefix", cfg.WorkDir, "--cache", filepath.Join(cfg.WorkDir, ".npm-cache"), "--no-audit", "--no-fund", "--no-save", "--loglevel=error", "--"}, packages...)
	cmd, _, err := runner.commandFor(ctx, npm, args)
	return cmd, err
}
