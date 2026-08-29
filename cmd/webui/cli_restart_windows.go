//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func restartInstalledService(root, configPath string) error {
	pid, err := os.ReadFile(filepath.Join(root, ".diana.pid"))
	if err != nil || strings.TrimSpace(string(pid)) == "" {
		return fmt.Errorf("no installer-managed Diana process was found in %s", root)
	}
	executable := filepath.Join(root, "diana-webui.exe")
	script := `$process = Get-CimInstance Win32_Process -Filter ("ProcessId = " + $env:DIANA_CLI_PID); if (-not $process -or -not $process.ExecutablePath -or -not $process.ExecutablePath.StartsWith($env:DIANA_CLI_ROOT, [StringComparison]::OrdinalIgnoreCase)) { throw "PID does not belong to this Diana installation" }; Stop-Process -Id $env:DIANA_CLI_PID -Force; Start-Sleep -Milliseconds 400; $env:DIANA_CONFIG=$env:DIANA_CLI_CONFIG; $started=Start-Process -FilePath $env:DIANA_CLI_EXE -WorkingDirectory $env:DIANA_CLI_ROOT -WindowStyle Hidden -PassThru; Set-Content -Encoding ASCII -Path (Join-Path $env:DIANA_CLI_ROOT ".diana.pid") -Value $started.Id`
	command := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	command.Env = append(os.Environ(), "DIANA_CLI_PID="+strings.TrimSpace(string(pid)), "DIANA_CLI_ROOT="+root, "DIANA_CLI_CONFIG="+configPath, "DIANA_CLI_EXE="+executable)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("restart Windows Diana process: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
