package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SuInk/diana/internal/procgroup"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// OwnerCommand identifies the executable, exact arguments and working directory.
// This is deliberately unsandboxed host-account execution.
type OwnerCommand struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	CWD     string   `json:"cwd"`
}

func ValidateOwnerCommand(_ Config, request OwnerCommand) (OwnerCommand, error) {
	if !filepath.IsAbs(request.Command) || !filepath.IsAbs(request.CWD) {
		return request, fmt.Errorf("command and cwd must be absolute paths")
	}
	var err error
	request.Command, err = filepath.EvalSymlinks(request.Command)
	if err != nil {
		return request, err
	}
	request.CWD, err = filepath.EvalSymlinks(request.CWD)
	if err != nil {
		return request, err
	}
	info, err := os.Stat(request.CWD)
	if err != nil {
		return request, err
	}
	if !info.IsDir() {
		return request, fmt.Errorf("cwd must be a directory")
	}
	if request.Args == nil {
		request.Args = []string{}
	}
	if obviousDestructiveHostCommand(request) {
		return request, fmt.Errorf("direct whole-machine destruction is prohibited")
	}
	return request, nil
}

// Best-effort rejection of obvious destructive invocations, NOT a security boundary
// against arbitrary scripts, interpreters, aliases, or privileged host services.
func obviousDestructiveHostCommand(r OwnerCommand) bool {
	name := strings.ToLower(filepath.Base(r.Command))
	switch {
	case strings.HasPrefix(name, "mkfs"), name == "format.com", name == "format":
		return true
	case name == "diskutil":
		for _, a := range r.Args {
			if strings.HasPrefix(strings.ToLower(a), "erase") {
				return true
			}
		}
	case name == "dd":
		for _, a := range r.Args {
			if strings.HasPrefix(a, "of=/dev/") {
				return true
			}
		}
	case name == "rm":
		recursive := false
		for _, a := range r.Args {
			if a == "--recursive" || (strings.HasPrefix(a, "-") && strings.ContainsAny(a, "rR")) {
				recursive = true
			}
		}
		if recursive {
			for _, a := range r.Args {
				switch filepath.Clean(a) {
				case "/", "/System", "/Windows", "/usr", "/etc", "/bin", "/sbin", "/Library":
					return true
				}
			}
		}
	}
	return false
}

func ownerCommandProtected(cfg Config) protectedFiles {
	protected := agentProtectedFiles(cfg)
	if executable, err := os.Executable(); err == nil {
		protected.add(executable, false)
	}
	return protected
}

func RunOwnerCommand(ctx context.Context, cfg Config, request OwnerCommand) (string, error) {
	validated, err := ValidateOwnerCommand(cfg, request)
	if err != nil {
		return "", err
	}
	if !ownerCommandsEqual(request, validated) {
		return "", fmt.Errorf("执行路径已改变，请重新检查命令")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := procgroup.CommandContext(ctx, request.Command, request.Args...)
	cmd.Dir = request.CWD
	// Keep ordinary host identity/path settings, without implicitly exporting Diana's
	// API credentials or interpreter/loader hooks to every program.
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TEMP", "TMP", "LANG", "SystemRoot", "WINDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "PATHEXT", "COMSPEC"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if cmd.Env == nil {
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
	}
	out := &ownerCommandOutput{}
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	_ = procgroup.Kill(cmd)
	result := map[string]any{"command": request, "execution_mode": "host_account", "sandbox": false, "exit_code": 0, "output": string(out.data), "truncated": out.truncated, "timed_out": ctx.Err() == context.DeadlineExceeded}
	if err != nil {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		result["exit_code"] = code
		result["error"] = err.Error()
		if guidance := ownerCommandPermissionGuidance(runtime.GOOS, string(out.data)); guidance != nil {
			guidance["host"], _ = os.Hostname()
			guidance["executable"], _ = os.Executable()
			result["permission_guidance"] = guidance
		}
	}
	body, err := json.Marshal(result)
	return string(body), err
}

// These are diagnostic hints from command output, not an OS permission probe.
// In particular, a missing display can produce the same error as denied capture.
func ownerCommandPermissionGuidance(goos, output string) map[string]any {
	output = strings.ToLower(output)
	if strings.Contains(output, "sudo: a password is required") ||
		strings.Contains(output, "sudo: a terminal is required") ||
		strings.Contains(output, "must be run as root") {
		return map[string]any{
			"kind": "administrator", "status": "manual_action_required",
			"message": "命令报告需要管理员权限。请告诉用户在运行 Diana 的主机上手动执行所需诊断；不要索取或接收 sudo 密码，也不要自动配置免密 sudo。授权或人工诊断完成前不要反复重试，不要声称已取得数据。",
		}
	}
	if goos == "darwin" && strings.Contains(output, "could not create image from display") {
		return map[string]any{
			"kind": "screen_recording", "status": "needs_verification",
			"message": "屏幕捕获失败，可能缺少录屏权限，也可能没有可捕获的登录桌面；此错误本身不能确认权限被拒绝。请明确告诉用户：在运行 Diana 的 Mac 上检查系统设置 → 隐私与安全 → 录屏与系统录音，为实际启动 Diana 的程序授权（后台部署通常为 executable，终端启动可能归属终端），并确认已登录桌面。授权后可能需要重启 Diana；用户确认完成后再重试，不能自动授予权限或承诺已授权。截图不需要完全磁盘访问权限。",
		}
	}
	return nil
}
func ownerCommandsEqual(a, b OwnerCommand) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}

type ownerCommandOutput struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (w *ownerCommandOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	remaining := 65536 - len(w.data)
	if n > remaining {
		w.truncated = true
		p = p[:remaining]
	}
	w.data = append(w.data, p...)
	return n, nil
}

// OwnerPathProtected keeps credential/control-plane reads on their existing
// redacted diagnostic tools even when the owner reads outside the workspace.
func OwnerPathProtected(cfg Config, path string) bool {
	return ownerCommandProtected(cfg).blocked(path)
}
