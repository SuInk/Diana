// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CodingApprovalHookCommand 是 Diana 自己的内部子命令名。编码 CLI 的 PreToolUse
// hook 会以这个名字回调 Diana 的可执行文件，问一句「这一步能不能做」。
//
// 用自己的二进制当 hook 而不是生成一个脚本：不引入 shell 依赖，也不用管脚本的可
// 执行位和解释器路径在各平台上的差别。
const CodingApprovalHookCommand = "__coding-approve"

// codingHookPayload 是 Claude Code 写给 PreToolUse hook 的 stdin。只取要用的字段。
type codingHookPayload struct {
	SessionID string         `json:"session_id"`
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
}

// codingHookDecision 是 hook 写回 stdout 的放行结论。
type codingHookDecision struct {
	HookSpecificOutput codingHookSpecificOutput `json:"hookSpecificOutput"`
}

type codingHookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// RunCodingApprovalHook 是 hook 进程的入口。
//
// 退出码同时承担结论，不只靠 stdout 的 JSON：0 放行、2 拦截并把原因写进 stderr。
// 这样即使对面不认 hookSpecificOutput 这种较新的结构化输出，拦截仍然生效——放行
// 靠「正常退出」，拦截靠「退出码 2」，两种契约都覆盖到。
func RunCodingApprovalHook(policyPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	policy, err := loadCodingApprovalPolicy(policyPath)
	if err != nil {
		// 读不到策略就放行：审批链路自身的故障不该把整个任务卡死，Diana 侧的
		// 运行日志里会留下痕迹。
		fmt.Fprintf(stderr, "diana 审批 hook 读不到策略：%v\n", err)
		return 0
	}
	payload := codingHookPayload{}
	if body, err := io.ReadAll(io.LimitReader(stdin, 1<<20)); err == nil {
		_ = json.Unmarshal(body, &payload)
	}
	detail := codingHookDetail(payload)
	if !codingHookNeedsApproval(policy, payload.ToolName, detail) {
		return 0
	}

	request := codingApprovalRequest{
		ID:        strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		JobID:     policy.JobID,
		Tool:      payload.ToolName,
		Detail:    detail,
		CreatedAt: time.Now(),
	}
	decision, err := requestCodingApproval(policy, request)
	if err != nil {
		fmt.Fprintf(stderr, "diana 没能拿到主人的确认：%v\n", err)
		return 2
	}
	if !decision.Allow {
		reason := strings.TrimSpace(decision.Reason)
		if reason == "" {
			reason = "主人没有同意这个操作"
		}
		writeCodingHookDecision(stdout, "deny", reason)
		fmt.Fprintln(stderr, reason)
		return 2
	}
	writeCodingHookDecision(stdout, "allow", "主人已在聊天里确认")
	return 0
}

func writeCodingHookDecision(stdout io.Writer, decision, reason string) {
	body, err := json.Marshal(codingHookDecision{HookSpecificOutput: codingHookSpecificOutput{
		HookEventName:            "PreToolUse",
		PermissionDecision:       decision,
		PermissionDecisionReason: reason,
	}})
	if err != nil {
		return
	}
	fmt.Fprintln(stdout, string(body))
}

func loadCodingApprovalPolicy(path string) (codingApprovalPolicy, error) {
	body, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return codingApprovalPolicy{}, err
	}
	var policy codingApprovalPolicy
	if err := json.Unmarshal(body, &policy); err != nil {
		return codingApprovalPolicy{}, err
	}
	if strings.TrimSpace(policy.ApprovalDir) == "" {
		return codingApprovalPolicy{}, errors.New("策略里没有信箱目录")
	}
	if policy.TimeoutSeconds <= 0 {
		policy.TimeoutSeconds = defaultCodingApprovalTimeoutMinutes * 60
	}
	policy.Mode = normalizeCodingApprovalMode(policy.Mode)
	if len(policy.Patterns) == 0 {
		policy.Patterns = defaultCodingApprovalPatterns()
	}
	return policy, nil
}

// codingHookDetail 从工具入参里取出「它要做什么」。命令原文是判断危险与否的依据，
// 也是发给主人看的那一行。
func codingHookDetail(payload codingHookPayload) string {
	for _, key := range []string{"command", "file_path", "path", "url", "pattern"} {
		if value, ok := payload.ToolInput[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if len(payload.ToolInput) == 0 {
		return ""
	}
	body, err := json.Marshal(payload.ToolInput)
	if err != nil {
		return ""
	}
	return truncateRunes(string(body), 400)
}

// codingHookNeedsApproval 判断这一步要不要问主人。
func codingHookNeedsApproval(policy codingApprovalPolicy, tool, detail string) bool {
	switch policy.Mode {
	case codingApprovalModeOff:
		return false
	case codingApprovalModeAllWrites:
		return true
	}
	lowered := strings.ToLower(tool + " " + detail)
	for _, pattern := range policy.Patterns {
		if pattern != "" && strings.Contains(lowered, pattern) {
			return true
		}
	}
	return false
}

// requestCodingApproval 把询问投进信箱，然后等 Diana 写回裁决。
//
// 轮询文件而不是连一个本地端口：这个进程是 CLI 的子进程，环境和网络都不由 Diana
// 决定，而信箱目录是它工作目录旁边的东西，一定拿得到。Diana 在等待期间重启也不影响
// ——请求还在，重启后接着问。
func requestCodingApproval(policy codingApprovalPolicy, request codingApprovalRequest) (codingApprovalResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return codingApprovalResponse{}, err
	}
	requestPath := codingApprovalRequestPath(policy.ApprovalDir, request.ID)
	temp := requestPath + ".tmp"
	if err := os.WriteFile(temp, body, 0o600); err != nil {
		return codingApprovalResponse{}, err
	}
	if err := os.Rename(temp, requestPath); err != nil {
		return codingApprovalResponse{}, err
	}
	responsePath := codingApprovalResponsePath(policy.ApprovalDir, request.ID)
	deadline := time.Now().Add(time.Duration(policy.TimeoutSeconds) * time.Second)
	for {
		if raw, err := os.ReadFile(responsePath); err == nil {
			var response codingApprovalResponse
			if err := json.Unmarshal(raw, &response); err == nil {
				// 裁决已经拿到，信箱里的两个文件都不用留。
				_ = os.Remove(responsePath)
				_ = os.Remove(requestPath)
				return response, nil
			}
		}
		if time.Now().After(deadline) {
			_ = os.Remove(requestPath)
			return codingApprovalResponse{}, errors.New("等待确认超时")
		}
		time.Sleep(codingApprovalPollWait)
	}
}
