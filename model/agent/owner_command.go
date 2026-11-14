package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/internal/procgroup"
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
	cmd.Env = ownerCommandEnv()
	// 输出整份落进临时文件，不在内存里设上限；交给模型时再按整行分页。
	out, err := os.CreateTemp("", "diana-owner-command-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(out.Name()) }()
	defer func() { _ = out.Close() }()
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	_ = procgroup.Kill(cmd)
	result := map[string]any{"command": request, "execution_mode": "host_account", "sandbox": false, "exit_code": 0, "output": "", "truncated": false, "timed_out": ctx.Err() == context.DeadlineExceeded}
	if err != nil {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		result["exit_code"] = code
		result["error"] = err.Error()
		if guidance := ownerCommandPermissionGuidance(runtime.GOOS, ownerCommandTail(out)); guidance != nil {
			guidance["host"], _ = os.Hostname()
			guidance["executable"], _ = os.Executable()
			result["permission_guidance"] = guidance
		}
	}
	skeleton, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	page, err := pageCommandOutput(out, toolBudget(ctx), utf8.RuneCount(skeleton)+64, true, ownerSpill)
	if err != nil {
		return "", err
	}
	result["output"], result["truncated"] = page.Output, page.Truncated
	if page.Truncated {
		// 键名按字母序排在 output 前面，结果再长也不会先被截掉。
		result["notice"], result["full_output_path"] = page.Notice, page.FullOutputPath
	}
	body, err := json.Marshal(result)
	return string(body), err
}

// ownerSpill 把主人命令的完整输出存进系统临时目录，host_access read 读得到。
func ownerSpill(src *os.File) (string, string, error) {
	target, err := spillCommandOutputTo(filepath.Join(os.TempDir(), "diana-command-output"), src)
	if err != nil {
		return "", "", err
	}
	return target, "用 host_access operation=read path=" + target + " 从 offset=0 起读，结果会给出下一段的 next_offset", nil
}

// ownerCommandTail 取输出末尾一段给权限诊断匹配：报错几乎都在尾部。
func ownerCommandTail(file *os.File) string {
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	start := max(info.Size()-64<<10, 0)
	buf := make([]byte, info.Size()-start)
	n, _ := file.ReadAt(buf, start)
	return string(buf[:n])
}

// ownerCommandEnv keeps ordinary host identity/path settings, without implicitly
// exporting Diana's API credentials or interpreter/loader hooks to every program.
func ownerCommandEnv() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TEMP", "TMP", "LANG", "SystemRoot", "WINDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "PATHEXT", "COMSPEC"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	if env == nil {
		env = []string{"PATH=/usr/bin:/bin"}
	}
	return env
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

// OwnerPathProtected keeps credential/control-plane reads on their existing
// redacted diagnostic tools even when the owner reads outside the workspace.
func OwnerPathProtected(cfg Config, path string) bool {
	return ownerCommandProtected(cfg).blocked(path)
}
