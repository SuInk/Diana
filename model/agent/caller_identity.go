// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"strings"
)

// CallerIdentity 是触发这次 Agent 运行的那条消息的真实身份，由运行时写入。
//
// 隐私代理只挡模型：模型看到的是 im_user_xxx，工具参数在执行前才换回真实 ID。
// 可工具只能拿到模型选择填进参数的那部分，「当前是谁在调用」它无从得知。MCP
// 服务是所有会话共用的长连接，本地命令也不知道自己替谁跑，所以身份只能随每次
// 调用带过去，而且不能经过模型——模型填的值谁都能伪造。
type CallerIdentity struct {
	Platform  string
	BotID     string
	UserID    string
	GroupID   string
	MessageID string
	// ChatType 是 group 或 private。
	ChatType string
	// IsOwner 由运行时按配置判定，与工具鉴权同一条规则，不看提示词里的说法。
	IsOwner bool
}

type callerIdentityContextKey struct{}

// WithCallerIdentity 把本次运行的真实身份挂到 ctx 上，工具执行时读取。
func WithCallerIdentity(ctx context.Context, identity CallerIdentity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, callerIdentityContextKey{}, identity)
}

// CallerIdentityFromContext 取出本次运行的真实身份；没有触发消息的运行（定时任务、
// 后台整理）返回 false。
func CallerIdentityFromContext(ctx context.Context) (CallerIdentity, bool) {
	if ctx == nil {
		return CallerIdentity{}, false
	}
	identity, ok := ctx.Value(callerIdentityContextKey{}).(CallerIdentity)
	if !ok || strings.TrimSpace(identity.UserID) == "" {
		return CallerIdentity{}, false
	}
	return identity, true
}

// mcpCallerMetaKey 是 tools/call 请求 _meta 里的键。MCP 规定 _meta 的键可以带
// 「标签/」前缀，modelcontextprotocol 和 mcp 开头的前缀归协议保留。
const mcpCallerMetaKey = "diana/caller"

func (identity CallerIdentity) mcpMeta() map[string]any {
	caller := map[string]any{
		"platform":  identity.Platform,
		"bot_id":    identity.BotID,
		"user_id":   identity.UserID,
		"chat_type": identity.ChatType,
		"is_owner":  identity.IsOwner,
	}
	if identity.GroupID != "" {
		caller["group_id"] = identity.GroupID
	}
	if identity.MessageID != "" {
		caller["message_id"] = identity.MessageID
	}
	return map[string]any{mcpCallerMetaKey: caller}
}

// environment 给本地命令用的 DIANA_CALLER_* 变量。群号、消息 ID 为空时整项不设，
// 脚本用「变量是否存在」区分群聊和私聊，不必解析空串。
func (identity CallerIdentity) environment() []string {
	env := []string{
		"DIANA_CALLER_PLATFORM=" + identity.Platform,
		"DIANA_CALLER_BOT_ID=" + identity.BotID,
		"DIANA_CALLER_USER_ID=" + identity.UserID,
		"DIANA_CALLER_CHAT_TYPE=" + identity.ChatType,
	}
	if identity.GroupID != "" {
		env = append(env, "DIANA_CALLER_GROUP_ID="+identity.GroupID)
	}
	if identity.MessageID != "" {
		env = append(env, "DIANA_CALLER_MESSAGE_ID="+identity.MessageID)
	}
	if identity.IsOwner {
		env = append(env, "DIANA_CALLER_IS_OWNER=1")
	} else {
		env = append(env, "DIANA_CALLER_IS_OWNER=0")
	}
	return env
}

// environmentWithCaller 先去掉宿主环境里同名的 DIANA_CALLER_*，再追加本次调用者。
// 不去掉的话，Diana 进程自己的环境里要是有同名变量，子进程会看到两份，取哪份看实现。
func environmentWithCaller(ctx context.Context, environ []string) []string {
	out := make([]string, 0, len(environ)+7)
	for _, item := range environ {
		if !strings.HasPrefix(item, "DIANA_CALLER_") {
			out = append(out, item)
		}
	}
	if identity, ok := CallerIdentityFromContext(ctx); ok {
		out = append(out, identity.environment()...)
	}
	return out
}
