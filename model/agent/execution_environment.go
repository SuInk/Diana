package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// withExecutionSearchPath 在继承的 PATH 后补上 Homebrew 和用户级安装目录：
// 服务进程（launchd、systemd）的 PATH 往往很短，node 装了也找不到。
func withExecutionSearchPath(env []string) []string {
	path := executionSearchPath()
	out := append([]string(nil), env...)
	for i, value := range out {
		if strings.HasPrefix(value, "PATH=") {
			out[i] = "PATH=" + path
			return out
		}
	}
	return append(out, "PATH="+path)
}

func executionSearchPath() string {
	paths := filepath.SplitList(os.Getenv("PATH"))
	paths = append(paths, "/opt/homebrew/bin", "/usr/local/bin")
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".local", "bin"))
	}
	return strings.Join(paths, string(os.PathListSeparator))
}

func (t *RunCommandTool) resolveCommand(name string) (string, error) {
	if strings.ContainsAny(name, `/\\`) {
		return "", errors.New("command must be a binary name, not a path")
	}
	if t.env != nil {
		return exec.LookPath(name)
	}
	// Check ordinary PATH first, including exec.ErrDot protection.
	if path, err := exec.LookPath(name); err == nil {
		return filepath.Abs(path)
	} else if errors.Is(err, exec.ErrDot) {
		return "", err
	}
	for _, dir := range filepath.SplitList(executionSearchPath()) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func executionEnvironment(ctx context.Context, registry *ToolRegistry) map[string]any {
	result := map[string]any{}
	for _, name := range []string{"run_command", "write_file", "extract_archive"} {
		_, ok := registry.Get(name)
		result[name] = ok
	}
	tool, ok := registry.Get("run_command")
	runner, concrete := tool.(*RunCommandTool)
	programs := make([]map[string]any, 0, 4)
	for _, name := range []string{"node", "npm", "tar", "unzip"} {
		entry := map[string]any{"name": name, "authorized": false, "installed": false}
		probe := runner
		if !ok || !concrete {
			probe = &RunCommandTool{}
		}
		path, err := probe.resolveCommand(name)
		entry["installed"] = err == nil
		if ok && concrete && runner.commandAllowed(name) {
			entry["authorized"] = true
			if err == nil {
				entry["path"] = path
			}
		}
		programs = append(programs, entry)
	}
	result["programs"] = programs
	if !ok || !concrete {
		result["sandbox"] = map[string]any{"effective": "blocked", "network": false, "reason": "run_command is not authorized"}
		return result
	}
	mode := normalizeCommandSandboxMode(runner.sandboxMode)
	sandbox := map[string]any{"mode": mode, "effective": "unsandboxed", "network": true}
	// commandFor constructs the actual wrapper but does not start a process.
	_, kind, err := runner.commandFor(ctx, "", nil)
	if err != nil {
		sandbox["effective"] = "blocked"
		sandbox["network"] = false
		sandbox["reason"] = err.Error()
	} else if kind != "" {
		sandbox["effective"] = "sandboxed"
		sandbox["network"] = runner.sandboxNetwork
		sandbox["kind"] = kind
	} else if mode == CommandSandboxOff {
		sandbox["reason"] = "sandbox is configured off"
	} else {
		sandbox["reason"] = "configured sandbox is unavailable"
	}
	result["sandbox"] = sandbox
	result["work_dir"] = runner.root
	return result
}
