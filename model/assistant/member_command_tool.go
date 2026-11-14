// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 群成员的 run_command：主人在设置里打开「群成员可运行命令」后，群里的普通成员也能
// 跑一段 Python/Node 生成文件。它和主人的 run_command 同名，但完全是另一套：
// 关在 agent.RunMemberCommand 的成员沙盒里（读取白名单、只写个人目录、断网、限资源），
// 沙盒探不通就拒绝；产物按本群登记访问凭据，模型拿返回的 path 交给 send_file 发出。

// memberCommandRunsDir 是成员命令目录的根，按群、按人再分两级。
const memberCommandRunsDir = "member-runs"

// memberCommandAvailable 判断成员沙盒能不能用。测试里替换它，免得结果取决于跑测试
// 的机器装没装 bubblewrap / sandbox-exec。
var memberCommandAvailable = func() bool {
	kind, _ := agent.MemberCommandAvailable()
	return kind != ""
}

// memberCommandExposed 决定这一轮要不要给非主人挂成员版 run_command：开关打开、
// 不在安全模式、是群聊（产物凭据按群发）、成员沙盒真的可用。
func (r *Runtime) memberCommandExposed(event MessageEvent) bool {
	if r == nil {
		return false
	}
	cfg := r.effectiveConfigForEvent(event)
	if !cfg.AgentMemberCommandEnabled || cfg.agentSafeMode() {
		return false
	}
	return r.workspaceGroupScope(event) != "" && memberCommandAvailable()
}

// memberCommandDir 是这个成员在本群的目录，相对工作区。用户 ID 只以哈希出现在路径里。
func (r *Runtime) memberCommandDir(event MessageEvent) (string, error) {
	scope := r.workspaceGroupScope(event)
	if scope == "" || strings.TrimSpace(event.UserID) == "" {
		return "", errors.New("群成员只能在群聊里运行命令")
	}
	key, _ := json.Marshal([]string{scope, event.UserID})
	return filepath.Join(memberCommandRunsDir, scope, workspaceAccessHash(key)[:32]), nil
}

type dianaMemberCommandTool struct {
	runtime *Runtime
	event   MessageEvent
	run     func(context.Context, agent.MemberCommandRequest) (agent.MemberCommandResult, error)
}

func newDianaMemberCommandTool(r *Runtime, event MessageEvent) *dianaMemberCommandTool {
	return &dianaMemberCommandTool{runtime: r, event: event, run: agent.RunMemberCommand}
}

func (t *dianaMemberCommandTool) Name() string { return "run_command" }

const (
	memberCommandMaxWriteFiles = 16
	memberCommandMaxInputs     = 10
)

func (t *dianaMemberCommandTool) Description() string {
	limits := agent.DefaultMemberCommandLimits
	return fmt.Sprintf("在个人隔离沙盒里离线运行命令生成或处理文件（无 shell、断网、禁读宿主目录）。可用：%s。"+
		"支持 write_files 预写脚本、inputs 传入本群已保存附件、stdin 标准输入；"+
		"Python 预装零依赖库 diana_sandbox（Canvas 导出 PNG/BMP、svg_bar_chart/svg_line_chart/svg_pie_chart、synth_notes/write_wav、MIDIBuilder、describe_numbers）。"+
		"限 %ds/%dMB 内存/%dMB 单文件，产物从 files 取 path 交 send_file 发出。",
		strings.Join(agent.AvailableMemberCommandPrograms(), "、"), int(limits.Wall/time.Second), limits.MemoryBytes>>20,
		limits.FileBytes>>20)
}

func (t *dianaMemberCommandTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"command"}, map[string]any{
		"command":     toolStringParam("程序名（如 python3、node、ffmpeg、sqlite3、jq、sips）"),
		"args":        toolStringArrayParam("参数，不经 shell 解析"),
		"stdin":       toolStringParam("可选标准输入文本，直传给进程"),
		"write_files": map[string]any{"type": "object", "description": "启动前写入个人目录的文件映射（键为相对路径如 \"gen.py\"，值为文本内容）", "additionalProperties": map[string]any{"type": "string"}},
		"inputs":      toolStringArrayParam("本群已授权的工作目录文件路径（如 save_to_workspace 返回的 path），会复制到沙盒 inputs/<文件名> 供读取"),
		"timeout_ms":  toolIntParam("超时毫秒，不超过上限", 1, int(agent.DefaultMemberCommandLimits.Wall/time.Millisecond)),
	})
}

type memberCommandFileOutput struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

func (t *dianaMemberCommandTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if !t.runtime.memberCommandExposed(t.event) {
		return "", errors.New("群成员命令没有打开，或这台机器上的成员沙盒不可用")
	}
	rel, err := t.runtime.memberCommandDir(t.event)
	if err != nil {
		return "", err
	}
	writeFiles, err := memberCommandWriteFiles(input)
	if err != nil {
		return "", err
	}
	stagedInputs, err := t.resolveAuthorizedInputs(input, agent.DefaultMemberCommandLimits.FileBytes)
	if err != nil {
		return "", err
	}
	stdinText, _ := input["stdin"].(string)
	root := AgentWorkspaceDir()
	result, err := t.run(ctx, agent.MemberCommandRequest{
		Dir:        filepath.Join(root, rel),
		Command:    configToolString(input, "command"),
		Args:       memberCommandArgs(input),
		Stdin:      stdinText,
		WriteFiles: writeFiles,
		Inputs:     stagedInputs,
		Timeout:    time.Duration(intFromAny(input["timeout_ms"])) * time.Millisecond,
		Limits:     agent.DefaultMemberCommandLimits,
	})
	if err != nil {
		return "", err
	}
	files, grantErrors := t.grantProduced(rel, result.Files)
	body := map[string]any{
		"command":     result.Command,
		"args":        result.Args,
		"exit_code":   result.ExitCode,
		"timed_out":   result.TimedOut,
		"duration_ms": result.DurationMS,
		"output":      result.Output,
		"truncated":   result.Truncated,
		"sandbox":     result.Sandbox,
		"files":       files,
	}
	if result.LimitExceeded != "" {
		body["limit_exceeded"] = result.LimitExceeded
	}
	if result.MoreFiles > 0 {
		body["more_files"] = result.MoreFiles
	}
	if len(grantErrors) > 0 {
		body["grant_errors"] = grantErrors
	}
	if len(files) > 0 {
		body["next"] = "用 send_file 的 path 发送 files 里的文件"
	}
	encoded, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 || result.LimitExceeded != "" {
		return "", errors.New(string(encoded))
	}
	return string(encoded), nil
}

// grantProduced 给这次新生成的文件登记本群访问凭据。凭据绑的是沙盒那边持锁读出的
// 字节，之后文件被改掉或换成链接，旧凭据就对不上，不能借它读别的东西。
func (t *dianaMemberCommandTool) grantProduced(rel string, produced []agent.MemberCommandFile) ([]memberCommandFileOutput, []string) {
	files := []memberCommandFileOutput{}
	var failures []string
	for _, file := range produced {
		path := filepath.Clean(filepath.Join(rel, filepath.FromSlash(file.Name)))
		if err := t.runtime.grantWorkspaceGroupFile(t.event, path, file.Data); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", file.Name, err))
			continue
		}
		files = append(files, memberCommandFileOutput{Path: filepath.ToSlash(path), Size: file.Size})
	}
	return files, failures
}

// memberCommandArgs 原样取参数：-c 后面的脚本靠缩进和空白分块，不能裁掉。
func memberCommandArgs(input map[string]any) []string {
	args := []string{}
	switch typed := input["args"].(type) {
	case []string:
		args = append(args, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				args = append(args, text)
			}
		}
	}
	return args
}

func memberCommandWriteFiles(input map[string]any) (map[string][]byte, error) {
	raw, exists := input["write_files"]
	if !exists || raw == nil {
		return nil, nil
	}
	out := map[string][]byte{}
	switch typed := raw.(type) {
	case map[string]string:
		if len(typed) > memberCommandMaxWriteFiles {
			return nil, fmt.Errorf("write_files 一次最多 %d 个文件", memberCommandMaxWriteFiles)
		}
		for k, v := range typed {
			out[k] = []byte(v)
		}
	case map[string]any:
		if len(typed) > memberCommandMaxWriteFiles {
			return nil, fmt.Errorf("write_files 一次最多 %d 个文件", memberCommandMaxWriteFiles)
		}
		for k, v := range typed {
			text, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("write_files[%q] 必须是字符串", k)
			}
			out[k] = []byte(text)
		}
	default:
		return nil, errors.New("write_files 必须是文件名到内容的键值对象")
	}
	return out, nil
}

// resolveAuthorizedInputs 校验并读取本群已获授权的工作区文件（如 save_to_workspace 产物），
// 严格核对本群访问哈希，绝不允许跨群或越权读取受保护文件与主人私有文件。
func (t *dianaMemberCommandTool) resolveAuthorizedInputs(input map[string]any, limit int64) (map[string][]byte, error) {
	paths := configToolStringSlice(input, "inputs")
	if len(paths) == 0 {
		return nil, nil
	}
	if len(paths) > memberCommandMaxInputs {
		return nil, fmt.Errorf("inputs 一次最多传入 %d 个文件", memberCommandMaxInputs)
	}
	cfg := agent.Config{WorkDir: AgentWorkspaceDir()}
	if t.runtime != nil {
		cfg.MCPConfigPath = t.runtime.effectiveConfigForEvent(t.event).AgentMCPConfigPath
	}
	staged := make(map[string][]byte, len(paths))
	for _, rawPath := range paths {
		trimmed := strings.TrimSpace(rawPath)
		if trimmed == "" {
			continue
		}
		rel, err := agent.NormalizeWorkspacePath(cfg.WorkDir, trimmed)
		if err != nil {
			return nil, err
		}
		if agent.WorkspaceFileProtected(cfg, rel) {
			return nil, fmt.Errorf("%s 是受保护的运行时配置，不能传入沙盒", trimmed)
		}
		grant, err := t.runtime.workspaceGroupFileGrant(t.event, rel)
		if err != nil {
			return nil, err
		}
		data, err := agent.ReadWorkspaceFile(cfg.WorkDir, rel, limit)
		if err != nil {
			return nil, err
		}
		if workspaceAccessHash(data) != grant {
			return nil, fmt.Errorf("文件 %s 内容已改变，本群原有访问记录不再适用；请从本群原消息重新保存", trimmed)
		}
		base := filepath.Base(rel)
		name := base
		for suffix := 2; ; suffix++ {
			if _, exists := staged[name]; !exists {
				break
			}
			ext := filepath.Ext(base)
			stem := strings.TrimSuffix(base, ext)
			name = fmt.Sprintf("%s_%d%s", stem, suffix, ext)
		}
		staged[name] = data
	}
	return staged, nil
}
