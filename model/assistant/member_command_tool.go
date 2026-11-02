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

func (t *dianaMemberCommandTool) Description() string {
	limits := agent.DefaultMemberCommandLimits
	return fmt.Sprintf("在你的个人临时目录里运行短时命令生成文件，不经过 shell。只能用 %s；"+
		"复杂逻辑用 python3 -c 或 node -e 写进脚本文件再跑。断网，只能读系统库和自己目录，"+
		"单次最长 %d 秒、内存 %d MB、单个文件 %d MB，文件保留 %d 小时。"+
		"结果的 files 列出新生成的文件，把其中的 path 交给 send_file 发出。",
		strings.Join(agent.MemberCommandPrograms(), "、"), int(limits.Wall/time.Second), limits.MemoryBytes>>20,
		limits.FileBytes>>20, int(limits.Retention/time.Hour))
}

func (t *dianaMemberCommandTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"command"}, map[string]any{
		"command":    toolStringParam("程序名"),
		"args":       toolStringArrayParam("参数，不经 shell 解析"),
		"timeout_ms": toolIntParam("超时毫秒，不超过上限", 1, int(agent.DefaultMemberCommandLimits.Wall/time.Millisecond)),
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
	root := AgentWorkspaceDir()
	result, err := t.run(ctx, agent.MemberCommandRequest{
		Dir:     filepath.Join(root, rel),
		Command: configToolString(input, "command"),
		Args:    memberCommandArgs(input),
		Timeout: time.Duration(intFromAny(input["timeout_ms"])) * time.Millisecond,
		Limits:  agent.DefaultMemberCommandLimits,
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
