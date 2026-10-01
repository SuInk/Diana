//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func restartInstalledService(root, _ string) error {
	if runtime.GOOS == "darwin" {
		plist := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "com.suink.diana.plist")
		if fileContains(plist, root) {
			command := exec.Command("launchctl", "kickstart", "-k", "gui/"+strconv.Itoa(os.Getuid())+"/com.suink.diana")
			if output, err := command.CombinedOutput(); err != nil {
				return fmt.Errorf("restart launchd service: %w: %s", err, strings.TrimSpace(string(output)))
			}
			return nil
		}
	}
	unit := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user", "diana.service")
	if fileContains(unit, root) {
		command := exec.Command("systemctl", "--user", "restart", "diana.service")
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("restart systemd service: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	systemUnit := "/etc/systemd/system/diana.service"
	if fileContains(systemUnit, root) {
		return systemServiceAction("restart")
	}
	return fmt.Errorf("no installer-managed Diana service belongs to %s", root)
}

// systemServiceAction 对系统级 diana.service 执行 start/stop/restart。
func systemServiceAction(action string) error {
	if os.Geteuid() == 0 {
		command := exec.Command("systemctl", action, "diana.service")
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("%s systemd system service: %w: %s", action, err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	// 安装器给这个服务配了免密白名单，先按非交互试一次；装于旧版本或白名单
	// 缺失时再交互重试，并把终端接给 sudo——否则 sudo 拿不到 tty 输密码，
	// 只会抛一句 "no tty present"，看起来像服务坏了。
	quiet := exec.Command("sudo", "-n", "systemctl", action, "diana.service")
	if output, err := quiet.CombinedOutput(); err == nil {
		return nil
	} else if !sudoNeedsPassword(string(output)) {
		return fmt.Errorf("%s systemd system service: %w: %s", action, err, strings.TrimSpace(string(output)))
	}
	interactive := exec.Command("sudo", "systemctl", action, "diana.service")
	interactive.Stdin = os.Stdin
	interactive.Stdout = os.Stdout
	interactive.Stderr = os.Stderr
	if err := interactive.Run(); err != nil {
		return fmt.Errorf("%s systemd system service: %w", action, err)
	}
	return nil
}

// findInstalledService 按安装器留下的痕迹找出管理 root 这份安装的服务：launchd、
// systemd 用户/系统服务，或没有服务管理器时的后台进程。
func findInstalledService(root, dbPath, _ string) *installedService {
	if runtime.GOOS == "darwin" {
		plist := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "com.suink.diana.plist")
		if fileContains(plist, root) {
			domain := "gui/" + strconv.Itoa(os.Getuid())
			return &installedService{
				name:  "launchd com.suink.diana",
				stop:  func() error { return runServiceCommand("launchctl", "bootout", domain+"/com.suink.diana") },
				start: func() error { return runServiceCommand("launchctl", "bootstrap", domain, plist) },
			}
		}
	}
	if fileContains(filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user", "diana.service"), root) {
		return &installedService{
			name:  "systemd user service diana.service",
			stop:  func() error { return runServiceCommand("systemctl", "--user", "stop", "diana.service") },
			start: func() error { return runServiceCommand("systemctl", "--user", "start", "diana.service") },
		}
	}
	if fileContains("/etc/systemd/system/diana.service", root) {
		return &installedService{
			name:  "systemd service diana.service",
			stop:  func() error { return systemServiceAction("stop") },
			start: func() error { return systemServiceAction("start") },
		}
	}
	// 没有服务管理器时安装器用 nohup 拉起后台进程并记下进程号。
	pidFile := filepath.Join(root, ".diana.pid")
	if pid, ok := installerPIDMatchesLock(pidFile, dbPath); ok {
		return &installedService{
			name:  "background process",
			stop:  func() error { return syscall.Kill(pid, syscall.SIGTERM) },
			start: func() error { return startBackgroundProcess(root, pidFile) },
		}
	}
	return nil
}

// startBackgroundProcess 按安装器的方式重新拉起后台进程：脱离当前终端，输出
// 追加到 logs/installer-service.log，进程号写回 .diana.pid。
func startBackgroundProcess(root, pidFile string) error {
	logFile, err := os.OpenFile(filepath.Join(root, "logs", "installer-service.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	command := exec.Command(filepath.Join(root, "start-installed.sh"))
	command.Dir = root
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(command.Process.Pid)+"\n"), 0o644); err != nil {
		return err
	}
	return command.Process.Release()
}

func runServiceCommand(name string, args ...string) error {
	if output, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

// sudoNeedsPassword 区分「这次要密码」和「真的失败了」：前者值得换成交互式再来
// 一次，后者（比如服务不存在）重试也没有意义。
func sudoNeedsPassword(output string) bool {
	lowered := strings.ToLower(output)
	return strings.Contains(lowered, "password is required") ||
		strings.Contains(lowered, "no tty present") ||
		strings.Contains(lowered, "terminal is required")
}

func fileContains(path, value string) bool {
	content, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(content), value)
}
