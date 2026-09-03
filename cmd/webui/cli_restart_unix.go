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
		if os.Geteuid() == 0 {
			command := exec.Command("systemctl", "restart", "diana.service")
			if output, err := command.CombinedOutput(); err != nil {
				return fmt.Errorf("restart systemd system service: %w: %s", err, strings.TrimSpace(string(output)))
			}
			return nil
		}
		// 安装器给这个服务配了免密白名单，先按非交互试一次；装于旧版本或白名单
		// 缺失时再交互重试，并把终端接给 sudo——否则 sudo 拿不到 tty 输密码，
		// 只会抛一句 "no tty present"，看起来像服务坏了。
		quiet := exec.Command("sudo", "-n", "systemctl", "restart", "diana.service")
		if output, err := quiet.CombinedOutput(); err == nil {
			return nil
		} else if !sudoNeedsPassword(string(output)) {
			return fmt.Errorf("restart systemd system service: %w: %s", err, strings.TrimSpace(string(output)))
		}
		interactive := exec.Command("sudo", "systemctl", "restart", "diana.service")
		interactive.Stdin = os.Stdin
		interactive.Stdout = os.Stdout
		interactive.Stderr = os.Stderr
		if err := interactive.Run(); err != nil {
			return fmt.Errorf("restart systemd system service: %w", err)
		}
		return nil
	}
	return fmt.Errorf("no installer-managed Diana service belongs to %s", root)
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
