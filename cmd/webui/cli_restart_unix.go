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
	return fmt.Errorf("no installer-managed Diana service belongs to %s", root)
}

func fileContains(path, value string) bool {
	content, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(content), value)
}
