// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/SuInk/diana/scripts"
)

func handleCLI(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) != 1 {
			return true, fmt.Errorf("help does not accept additional arguments")
		}
		printCLIHelp(os.Stdout)
		return true, nil
	case "version", "--version", "-v":
		if len(args) != 1 {
			return true, fmt.Errorf("version does not accept additional arguments")
		}
		_, err := fmt.Fprintln(os.Stdout, runtimeVersion)
		return true, err
	}
	switch args[0] {
	case "status":
		return true, runStatusCommand(args[1:], os.Stdout)
	case "restart":
		return true, runRestartCommand(args[1:], os.Stdout)
	case "doctor":
		return true, runDoctorCommand(args[1:], os.Stdout)
	case "config":
		return true, runConfigCommand(args[1:], os.Stdout)
	case "passwd":
		return true, runResetPasswordCommand(args[1:], cliPrompt{input: os.Stdin, interactive: stdinIsTerminal()}, os.Stdout)
	}
	if args[0] == "logs" {
		return true, runLogsCommand(args[1:], os.Stdout)
	}
	if args[0] != "uninstall" {
		return false, nil
	}
	for _, arg := range args[1:] {
		if arg != "--purge" && arg != "--yes" && arg != "-y" && arg != "--help" && arg != "-h" {
			return true, fmt.Errorf("unknown uninstall option: %s", arg)
		}
	}
	root, err := installationRoot()
	if err != nil {
		return true, err
	}
	script, err := uninstallScriptPath(root)
	if err != nil {
		return true, err
	}
	return true, runUninstallScript(script, root, args[1:])
}

func printCLIHelp(output io.Writer) {
	_, _ = fmt.Fprint(output, `Diana command line

Usage:
  diana [command]

Commands:
  status                 Show service health, version, address, and uptime
  restart                Restart an installer-managed Diana service
  doctor                 Check configuration, paths, assets, and health
  config path|check      Locate or validate config.yaml
  logs [--lines N] [-f]  Show or follow the configured Diana log
  passwd [--username NAME] [-y]
                         Reset the administrator password and sign out every
                         session; keeps other data; stops and restarts an
                         installer-managed service around the reset
  uninstall [--purge]    Remove Diana, preserving data unless --purge is used
  version                Print the Diana version
  help                   Show this help

Run Diana without a command to start the WebUI service.
`)
}

func uninstallScriptPath(root string) (string, error) {
	if runtime.GOOS == "windows" {
		return windowsUninstallScript(root)
	}
	script := filepath.Join(root, "uninstall.sh")
	info, err := os.Stat(script)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("uninstall tool not found at %s; this command is available in one-click installations", script)
		}
		return "", fmt.Errorf("inspect uninstall tool: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("uninstall tool is not a regular file: %s", script)
	}
	return script, nil
}

// windowsUninstallScript 把编进程序的卸载脚本写到临时目录。Windows 只发一个 exe，
// 安装目录里没有脚本；用安装器写下的版本标记确认这是一键安装，免得在下载目录里
// 直接双击运行的程序把旁边的文件当安装删掉。
func windowsUninstallScript(root string) (string, error) {
	marker := filepath.Join(root, ".installed-version")
	if _, err := os.Stat(marker); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no installation marker at %s; this command is available in one-click installations", marker)
		}
		return "", fmt.Errorf("inspect installation marker: %w", err)
	}
	file, err := os.CreateTemp("", "diana-uninstall-*.ps1")
	if err != nil {
		return "", fmt.Errorf("stage uninstall tool: %w", err)
	}
	_, writeErr := file.Write(scripts.UninstallPowerShell)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("stage uninstall tool: %w", err)
	}
	return file.Name(), nil
}

func installationRoot() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate Diana executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve Diana executable: %w", err)
	}
	root := filepath.Dir(executable)
	if runtime.GOOS == "darwin" && filepath.Base(root) == "MacOS" && filepath.Base(filepath.Dir(root)) == "Contents" {
		root = filepath.Dir(filepath.Dir(filepath.Dir(root)))
	}
	return root, nil
}

func uninstallCommand(script, root string, args []string) *exec.Cmd {
	var commandArgs []string
	if runtime.GOOS == "windows" {
		commandArgs = append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-InstallDir", root}, windowsUninstallArgs(args)...)
		return exec.Command("powershell.exe", commandArgs...)
	}
	commandArgs = append([]string{script}, args...)
	return exec.Command("/bin/sh", commandArgs...)
}

func windowsUninstallArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "--purge":
			out = append(out, "-Purge")
		case "--yes", "-y":
			out = append(out, "-Yes")
		case "--help", "-h":
			out = append(out, "-?")
		}
	}
	return out
}
