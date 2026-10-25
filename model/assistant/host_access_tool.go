package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/hostinfo"
)

const hostAccessToolName = "host_access"

type hostAccessTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (t *hostAccessTool) Name() string { return hostAccessToolName }
func (t *hostAccessTool) Description() string {
	return `主人专用宿主机读取和执行，须在 WebUI 开启“允许主人操作宿主机”（默认关闭）。read/list/usage/disk 查询无需审批；exec 的每条命令都须单次审批，批准后直接以 Diana 宿主账号执行，支持任意程序、shell/脚本、联网及该账号有权进行的主机操作。没有沙盒或目录写入限制，脚本可能修改 Diana 自身和审批数据，不能保证硬限制。禁止主动执行整机毁坏或绕过身份/审批的操作。总体硬件状态用 host_stats，主人账号用 bot_config。`
}
func (t *hostAccessTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation": toolEnumParam("read 读文件；list 列目录；usage 统计目录逻辑体积（不跟随符号链接）；disk 查磁盘；exec 执行命令", "read", "list", "usage", "disk", "exec"),
		"path":      toolStringParam("读取或统计的绝对路径；相对路径以 Agent 工作区为基准"),
		"offset":    toolIntParam("read 的字节偏移或 list 跳过的条数，默认 0", 0, 1000000000),
		"command":   toolStringParam("exec 的可执行文件绝对路径，如 /bin/sh"),
		"args":      toolStringArrayParam("exec 的原始参数数组，不自动经过 shell"),
		"cwd":       toolStringParam("exec 的绝对工作目录，默认 Agent 工作区"),
	})
}
func (t *hostAccessTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil {
		return "", fmt.Errorf("主机工具未配置")
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	if !cfg.IsOwnerEvent(t.event) && !isAdminChatRun(ctx) {
		return "", fmt.Errorf("只有主人可以访问主机")
	}
	if cfg.agentSafeMode() {
		return "", fmt.Errorf("%s", agentSafeModeDisabledMessage)
	}
	if !cfg.AgentHostAccessEnabled {
		return "", fmt.Errorf("宿主机操作默认关闭，请由管理员在机器人高级设置中开启；不能由工具自行开启")
	}
	acfg := t.runtime.agentRegistryConfig(cfg, t.event, true)
	op := configToolString(input, "operation")
	if op == "exec" {
		return t.execute(ctx, acfg, input)
	}
	path := configToolString(input, "path")
	if path == "" {
		return "", fmt.Errorf("path 必填")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(acfg.WorkDir, path)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if agent.OwnerPathProtected(acfg, path) {
		return "", fmt.Errorf("该路径包含运行时凭据或控制数据，请用 bot_config 等脱敏诊断工具")
	}
	offset := intFromAny(input["offset"])
	if offset < 0 {
		return "", fmt.Errorf("offset 不能为负")
	}
	switch op {
	case "disk":
		return hostAccessJSON(hostStatsPayload(hostinfo.Collect(time.Now(), path)))
	case "usage":
		var size int64
		count, skipped := 0, 0
		truncated := false
		err := filepath.WalkDir(path, func(p string, e os.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if count >= 100000 {
				truncated = true
				return filepath.SkipAll
			}
			count++
			if err != nil {
				skipped++
				return nil
			}
			if agent.OwnerPathProtected(acfg, p) {
				skipped++
				if e.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if e.Type().IsRegular() {
				info, err := e.Info()
				if err != nil {
					skipped++
				} else {
					size += info.Size()
				}
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		return hostAccessJSON(map[string]any{"path": path, "logical_bytes": size, "size": humanBytes(uint64(size)), "entries_scanned": count, "skipped": skipped, "truncated": truncated, "note": "逻辑文件体积，非磁盘实际占用；不跟随符号链接，硬链接可能重复计数"})
	case "read", "list":
		f, err := openHostRead(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return "", err
		}
		if op == "read" {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("read 仅支持普通文件")
			}
			if _, err = f.Seek(int64(offset), io.SeekStart); err != nil {
				return "", err
			}
			body, err := io.ReadAll(io.LimitReader(f, 65537))
			if err != nil {
				return "", err
			}
			more := len(body) > 65536
			if more {
				body = body[:65536]
			}
			return hostAccessJSON(map[string]any{"path": path, "content": string(body), "truncated": more, "next_offset": offset + len(body)})
		}
		if offset > 100000 {
			return "", fmt.Errorf("目录分页最多跳过 100000 项，请缩小目录范围")
		}
		if !info.IsDir() {
			return "", fmt.Errorf("list 需要目录")
		}
		// Bounded paging avoids materializing a huge directory in memory.
		for n := 0; n < offset; {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			step := min(200, offset-n)
			items, err := f.ReadDir(step)
			n += len(items)
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
		}
		entries, err := f.ReadDir(201)
		if err != nil && err != io.EOF {
			return "", err
		}
		more := len(entries) > 200
		if more {
			entries = entries[:200]
		}
		items := []map[string]any{}
		for _, e := range entries {
			items = append(items, map[string]any{"name": e.Name(), "directory": e.IsDir(), "type": e.Type().String(), "protected": agent.OwnerPathProtected(acfg, filepath.Join(path, e.Name()))})
		}
		return hostAccessJSON(map[string]any{"path": path, "entries": items, "truncated": more, "next_offset": offset + len(entries), "note": "目录变化时分页可能变化"})
	default:
		return "", fmt.Errorf("不支持的 operation %q", op)
	}
}
func (t *hostAccessTool) execute(ctx context.Context, cfg agent.Config, input map[string]any) (string, error) {
	if configToolString(input, "writable_dir") != "" {
		return "", fmt.Errorf("宿主执行没有 writable_dir 隔离，请移除该参数并重新申请审批")
	}
	args := []string{}
	switch values := input["args"].(type) {
	case nil:
	case []string:
		args = append(args, values...)
	case []any:
		for _, v := range values {
			s, ok := v.(string)
			if !ok {
				return "", fmt.Errorf("args 必须是字符串数组")
			}
			args = append(args, s)
		}
	default:
		return "", fmt.Errorf("args 必须是字符串数组")
	}
	cwd := configToolString(input, "cwd")
	if cwd == "" {
		cwd = cfg.WorkDir
	}
	request, err := agent.ValidateOwnerCommand(cfg, agent.OwnerCommand{Command: configToolString(input, "command"), Args: args, CWD: cwd})
	if err != nil {
		return "", err
	}
	{
		text, adminScope := agent.RequestAuthorization(ctx)
		scope := hostApprovalScope(t.event)
		if !isAdminChatRun(ctx) && adminScope != scope {
			text = ""
		}
		if isAdminChatRun(ctx) {
			if adminScope == "" {
				return "", fmt.Errorf("管理会话缺少可信审批范围")
			}
			scope = "admin\x00" + t.event.ProfileID + "\x00" + adminScope
		}
		body, _ := json.Marshal(request)
		ok, code, err := t.runtime.hostApprovals.authorize(scope, string(body), text, time.Now())
		if err != nil {
			return "", err
		}
		if !ok {
			return hostAccessJSON(map[string]any{"status": "approval_required", "executed": false, "command": request, "execution_mode": "host_account", "sandbox": false, "confirmation": "批准主机操作 " + code, "validity": "首次申请后 5 分钟失效，重试不续期", "instruction": "向主人完整展示命令、参数、工作目录及影响，明确直接以 Diana 宿主账号执行、可联网且没有沙盒或写入范围限制。等待原样回复后重试相同参数，不得自行批准。"})
		}
	}
	return agent.RunOwnerCommand(ctx, cfg, request)
}
func hostAccessJSON(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }

type hostApproval struct {
	scope, input, code string
	expires            time.Time
}
type hostApprovalStore struct {
	mu      sync.Mutex
	pending []hostApproval
}

func (s *hostApprovalStore) authorize(scope, input, text string, now time.Time) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := s.pending[:0]
	for _, p := range s.pending {
		if now.Before(p.expires) {
			live = append(live, p)
		}
	}
	s.pending = live
	for i, p := range s.pending {
		if p.scope != scope || p.input != input {
			continue
		}
		if strings.TrimSpace(text) == "批准主机操作 "+p.code {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			return true, "", nil
		}
		return false, p.code, nil
	}
	if len(s.pending) >= 128 {
		return false, "", fmt.Errorf("待审批操作过多，请等待过期")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return false, "", err
	}
	code := hex.EncodeToString(token[:])
	s.pending = append(s.pending, hostApproval{scope: scope, input: input, code: code, expires: now.Add(5 * time.Minute)})
	return false, code, nil
}

func hostApprovalScope(event MessageEvent) string {
	return "host:" + strings.Join([]string{event.ProfileID, event.Platform, event.UserID, sessionKey(event)}, "\x00")
}
