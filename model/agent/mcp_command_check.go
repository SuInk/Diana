// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// 本地进程形态的 MCP 少一个二进制时，整条链路在保存和启用这两步都是绿的：预设
// 表单只验凭据（问的是 Gitea 的 API，根本不碰 gitea-mcp），启用只是写一条覆盖。
// 真正的 exec: "gitea-mcp": executable file not found in $PATH 要等到某次对话里
// 调用工具才冒出来，而且只写进后台日志——用户看到的是一条装好了、也开着、就是
// 不干活的服务。
//
// 保存这一步手上已经有命令名了，在这里查掉最便宜，也最容易说清怎么修。

// resolveLocalMCPCommand 把裸命令名解析成真正要拉起的可执行文件。PATH 里有就用
// PATH 的；没有再看主程序旁边有没有随包发布的同名二进制——镜像和安装包把 gitea-mcp
// 放在主程序旁边，那个目录不在 PATH 里。
//
// 解析放在拉起这一步，而不是只在保存预设时做一次：配置里存的命令名是保存那天写下的，
// 那时没带这份二进制的版本会存成裸名字，升级到带它的版本以后还是拉不起来，而用户
// 从界面上看这条 MCP 什么都没变，只是一直报「PATH 里找不到命令」。
func resolveLocalMCPCommand(command string) string {
	executable, err := os.Executable()
	if err != nil {
		return strings.TrimSpace(command)
	}
	// 一键安装会在 PATH 里放软链，顺着链接找才能落到真正的安装目录。
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return resolveLocalMCPCommandIn(command, filepath.Dir(executable))
}

// resolveLocalMCPCommandIn 是上面那套规则的可测形态：bundledDir 就是主程序所在目录。
func resolveLocalMCPCommandIn(command, bundledDir string) string {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsRune(command, filepath.Separator) || filepath.IsAbs(command) {
		return command
	}
	// PATH 优先：用户自己装过一份的话，那份才是他期望被拉起来的。
	if _, err := exec.LookPath(command); err == nil {
		return command
	}
	if path, ok := bundledCommandPath(bundledDir, command); ok {
		return path
	}
	return command
}

// checkLocalMCPCommand 确认本地进程形态的 MCP 当真有一个可执行文件可以拉起。
// 远程接法（只有 url）返回 nil：它没有本地命令可查。
func checkLocalMCPCommand(server mcpServerConfig) error {
	command := resolveLocalMCPCommand(server.Command)
	if command == "" {
		return nil
	}
	if strings.ContainsRune(command, filepath.Separator) || filepath.IsAbs(command) {
		info, err := os.Stat(command)
		if err != nil {
			return fmt.Errorf("找不到可执行文件 %s", command)
		}
		if info.IsDir() {
			return fmt.Errorf("%s 是一个目录，不是可执行文件", command)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("%s 没有可执行权限", command)
		}
		return nil
	}
	if _, err := exec.LookPath(command); err != nil {
		return fmt.Errorf("PATH 里找不到命令 %s", command)
	}
	return nil
}

// localMCPCommandError 把「命令不在」翻译成用户能照着做的一段话。三条出路都给全：
// 换一个带着这个依赖的镜像／安装包、填自己装好的路径、改接一个已经跑起来的远程实例。
func localMCPCommandError(server mcpServerConfig, err error) error {
	hint := "请确认运行 Diana 的镜像或安装包里带着这个依赖，或在表单的「可执行文件」里填已经装好的绝对路径"
	if presetHasRemoteTransport(server.Preset) {
		hint += "，也可以改用远程地址那一项，接一个已经跑起来的服务"
	}
	return fmt.Errorf("这条 MCP 要拉起本地进程，但%s。%s。", err, hint)
}

// presetHasRemoteTransport 判断这个预设有没有「连远程」那一项，决定要不要把它
// 写进修复建议里——没有的预设提这一句只会让人去找一个不存在的选项。
func presetHasRemoteTransport(presetID string) bool {
	preset, ok := presetByID(strings.TrimSpace(presetID))
	if !ok {
		return false
	}
	for _, transport := range preset.Transports {
		if transport.ID != "stdio" {
			return true
		}
	}
	return false
}

// probeMCPServer 真正把这条 MCP 拉起来一次，确认能连上、能发现工具，然后关掉。
// 这是「保存时强制跑一次连接测试」的那一次：凭据对不代表进程起得来。
func probeMCPServer(ctx context.Context, name string, server mcpServerConfig, cfg Config) ([]string, error) {
	instance, err := startMCPServerRuntime(ctx, name, server, cfg, map[string]bool{})
	if err != nil {
		if cmdErr := checkLocalMCPCommand(server); cmdErr != nil {
			return nil, localMCPCommandError(server, cmdErr)
		}
		return nil, err
	}
	defer instance.Close()
	names := make([]string, 0, len(instance.tools))
	for _, tool := range instance.tools {
		names = append(names, tool.Name())
	}
	return names, nil
}
