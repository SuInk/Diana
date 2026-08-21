// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// 进程管理器种类。空串表示没有管理器托管，重启责任在更新器自己。
const (
	supervisorNone    = ""
	supervisorLaunchd = "launchd"
	supervisorSystemd = "systemd"
)

// serviceSupervisor 记录托管当前服务的进程管理器。
//
// launchd 的 KeepAlive 和 systemd 的 Restart= 会在服务退出后立刻把它拉起来，
// 更新辅助进程如果同时也去启动新二进制，两个实例会抢同一个监听端口，
// 后启动的那个报 address already in use 然后退出——看起来像更新失败，
// 实际是重启职责重复。被托管时重启交给管理器，更新器只负责换文件和体检。
type serviceSupervisor struct {
	Kind string `json:"kind,omitempty"`
	// Label 是 launchd 的 job label 或 systemd 的 unit 名，用来精确地重启这一个服务。
	Label string `json:"label,omitempty"`
	// Domain 是 launchd 的域（gui/<uid> 或 system）；systemd 下为 "user" 或 "system"。
	Domain string `json:"domain,omitempty"`
}

// Managed 表示当前进程由外部管理器托管，重启不该由更新器发起。
func (s serviceSupervisor) Managed() bool {
	return s.Kind != supervisorNone && strings.TrimSpace(s.Label) != ""
}

func (s serviceSupervisor) String() string {
	if !s.Managed() {
		return "unmanaged"
	}
	if s.Domain == "" {
		return s.Kind + ":" + s.Label
	}
	return s.Kind + ":" + s.Domain + "/" + s.Label
}

// detectServiceSupervisor 判断当前进程是否由 launchd 或 systemd 托管。
//
// 必须在被托管的进程里调用：更新辅助进程是 setsid detach 出去的，
// 它自己已经脱离了管理器，所以结果要随更新计划一起传给辅助进程。
func detectServiceSupervisor() serviceSupervisor {
	if forced, ok := supervisorFromEnvironment(); ok {
		return forced
	}
	switch runtime.GOOS {
	case "darwin":
		return detectLaunchdSupervisor()
	case "linux":
		return detectSystemdSupervisor()
	}
	return serviceSupervisor{}
}

// supervisorFromEnvironment 读取安装脚本显式写进服务环境的标识。
// 显式配置优先于探测，也给容器和自定义部署一个关掉这套逻辑的开关。
func supervisorFromEnvironment() (serviceSupervisor, bool) {
	kind := strings.ToLower(strings.TrimSpace(os.Getenv("DIANA_SERVICE_MANAGER")))
	switch kind {
	case "":
		return serviceSupervisor{}, false
	case "none", "off", "0", "false":
		return serviceSupervisor{}, true
	case supervisorLaunchd, supervisorSystemd:
	default:
		return serviceSupervisor{}, false
	}
	label := strings.TrimSpace(os.Getenv("DIANA_SERVICE_LABEL"))
	if label == "" {
		return serviceSupervisor{}, false
	}
	domain := strings.TrimSpace(os.Getenv("DIANA_SERVICE_DOMAIN"))
	if domain == "" {
		domain = defaultSupervisorDomain(kind)
	}
	return serviceSupervisor{Kind: kind, Label: label, Domain: domain}, true
}

func defaultSupervisorDomain(kind string) string {
	if kind == supervisorLaunchd {
		return fmt.Sprintf("gui/%d", os.Getuid())
	}
	return "user"
}

// detectLaunchdSupervisor 认 launchd 注入的 XPC_SERVICE_NAME。
// 从终端手动启动时这个值是未设置或者字面量 "0"，只有真正被 launchd
// 托管的 job 才会拿到自己的 label。
func detectLaunchdSupervisor() serviceSupervisor {
	label := strings.TrimSpace(os.Getenv("XPC_SERVICE_NAME"))
	if label == "" || label == "0" || !strings.Contains(label, ".") {
		return serviceSupervisor{}
	}
	// 用户级 LaunchAgent 在 gui/<uid> 域；root 跑的 LaunchDaemon 在 system 域。
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	if os.Getuid() == 0 {
		domain = "system"
	}
	return serviceSupervisor{Kind: supervisorLaunchd, Label: label, Domain: domain}
}

// detectSystemdSupervisor 认 systemd 注入的 INVOCATION_ID，unit 名从 cgroup 路径里取。
func detectSystemdSupervisor() serviceSupervisor {
	if strings.TrimSpace(os.Getenv("INVOCATION_ID")) == "" {
		return serviceSupervisor{}
	}
	unit := systemdUnitFromCgroup(readFileString("/proc/self/cgroup"))
	if unit == "" {
		return serviceSupervisor{}
	}
	domain := "system"
	if os.Getuid() != 0 {
		domain = "user"
	}
	return serviceSupervisor{Kind: supervisorSystemd, Label: unit, Domain: domain}
}

func readFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// systemdUnitFromCgroup 从 cgroup 路径里取出服务自己的 unit 名。
// cgroup v2 只有一行 "0::/user.slice/.../diana.service"，v1 每个控制器一行。
//
// 取最靠里的那一段：用户级服务的路径上会先经过 user@<uid>.service
// （那是 systemd 的用户管理器，不是我们的服务），只有最后一段才是应用 unit。
func systemdUnitFromCgroup(content string) string {
	for _, line := range strings.Split(content, "\n") {
		path := line
		if fields := strings.SplitN(line, ":", 3); len(fields) == 3 {
			path = fields[2]
		}
		unit := ""
		for _, segment := range strings.Split(path, "/") {
			segment = strings.TrimSpace(segment)
			if !strings.HasSuffix(segment, ".service") || segment == ".service" {
				continue
			}
			if strings.HasPrefix(segment, "user@") {
				continue
			}
			unit = segment
		}
		if unit != "" {
			return unit
		}
	}
	return ""
}

// validateServiceSupervisor 校验计划文件里的管理器标识。
// 这两个值会拼进 launchctl/systemctl 的参数，虽然没有经过 shell、
// 注入不了额外命令，仍然只接受服务名该有的字符，别让一份被改过的
// 计划文件把重启指向别的服务。
func validateServiceSupervisor(supervisor serviceSupervisor) error {
	if supervisor.Kind == supervisorNone {
		if strings.TrimSpace(supervisor.Label) != "" || strings.TrimSpace(supervisor.Domain) != "" {
			return errors.New("updater: service manager label without a kind")
		}
		return nil
	}
	if supervisor.Kind != supervisorLaunchd && supervisor.Kind != supervisorSystemd {
		return fmt.Errorf("updater: unknown service manager %q", supervisor.Kind)
	}
	if !safeSupervisorToken(supervisor.Label) {
		return fmt.Errorf("updater: unsafe service label %q", supervisor.Label)
	}
	if supervisor.Domain != "" && !safeSupervisorToken(supervisor.Domain) {
		return fmt.Errorf("updater: unsafe service domain %q", supervisor.Domain)
	}
	return nil
}

func safeSupervisorToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '@' || r == '/':
		default:
			return false
		}
	}
	return true
}

// restartSupervisedService 让进程管理器把服务重启一遍。
//
// 管理器会先停掉在跑的实例再启动新的，这个先后顺序正是我们要的：
// 端口在同一时刻只被一个实例持有，不会出现两边抢端口。
func restartSupervisedService(supervisor serviceSupervisor) error {
	if !supervisor.Managed() {
		return errors.New("updater: no service manager to restart")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var lastErr error
	for _, args := range supervisorRestartCommands(supervisor) {
		output, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	if lastErr == nil {
		lastErr = errors.New("updater: no restart command for this service manager")
	}
	return lastErr
}

// supervisorRestartCommands 返回按优先级排列的重启命令。
// 域判断可能不准（比如 sudo 环境下的 uid），所以另一个域作为兜底再试一次。
func supervisorRestartCommands(supervisor serviceSupervisor) [][]string {
	switch supervisor.Kind {
	case supervisorLaunchd:
		domains := []string{supervisor.Domain, fmt.Sprintf("gui/%d", os.Getuid()), "system"}
		commands := make([][]string, 0, len(domains))
		for _, domain := range dedupeNonEmpty(domains) {
			commands = append(commands, []string{"launchctl", "kickstart", "-k", domain + "/" + supervisor.Label})
		}
		return commands
	case supervisorSystemd:
		if supervisor.Domain == "system" {
			return [][]string{{"systemctl", "restart", supervisor.Label}, {"systemctl", "--user", "restart", supervisor.Label}}
		}
		return [][]string{{"systemctl", "--user", "restart", supervisor.Label}, {"systemctl", "restart", supervisor.Label}}
	}
	return nil
}

func dedupeNonEmpty(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// supervisedProcess 代表由进程管理器持有的实例。
// 更新器没有它的句柄，也不该去杀它——杀了管理器又会拉起同一个版本，
// 回滚要靠先换回文件再让管理器重启一次。
type supervisedProcess struct{}

func (supervisedProcess) Stop() error    { return nil }
func (supervisedProcess) Release() error { return nil }
