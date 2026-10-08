// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 下一轮的上下文里只有机器人上一条回复的正文，看不到它当时调用过哪些工具。线上 09-15：
// 群友问 iOS 更新的 iCloud 协议，机器人 20:06 确实调了 web_search，回复开头还写着「这次
// 我老老实实联网搜过了」；20:21 被追问「搜索了吗」，它顺着回答「没搜，凭记忆答的」。
//
// 起初把调用记录放在内存里按会话注入，10-06 又出了同样的事：16:01 搜过，16:04 进程重启，
// 16:05 被问「你搜索了吗」时记录已经没了。所以现在把记录挂在回复本身上——随出站消息
// 落库，渲染历史时紧跟在那条回复后面。重启不丢，也不会被同群别人的调用挤掉，回复
// 滚出历史窗口时记录一起走。
const (
	replyToolTraceLimit      = 8
	replyToolTraceNotePrefix = "【运行时记录，不是你说过的话】你发上面这条回复前实际调用过："
	replyToolTraceNoteSuffix = "有人问「搜了吗」「查过没」时照这份记录如实回答，不要因为对方质疑就改口说没查，也不要声称调用过记录里没有的工具；没人问时不要复述。"
)

// ReplyToolCall 是一条回复发出前实际执行过的工具调用，随出站消息落库。
type ReplyToolCall struct {
	Tool    string `json:"tool"`
	Summary string `json:"summary,omitempty"`
	Failed  bool   `json:"failed,omitempty"`
}

// toolCallMemoryIgnored 是不值得记的内部工具：它们不是「去查了什么」。
var toolCallMemoryIgnored = map[string]bool{
	"agent_finalize": true,
	"tools_load":     true,
	"tools_execute":  true,
}

// replyToolTrace 在一次回复生成期间收集工具调用，交给随后发出的第一条消息。
type replyToolTrace struct {
	mu    sync.Mutex
	calls []ReplyToolCall
}

type replyToolTraceKey struct{}

func withReplyToolTrace(ctx context.Context) context.Context {
	return context.WithValue(ctx, replyToolTraceKey{}, &replyToolTrace{})
}

func replyToolTraceFromContext(ctx context.Context) *replyToolTrace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(replyToolTraceKey{}).(*replyToolTrace)
	return trace
}

// add 记下实际执行过的工具调用，跳过的调用不算。生成失败重来时两次的调用都是真的，所以累加。
func (t *replyToolTrace) add(steps []agent.Step) {
	if t == nil {
		return
	}
	calls := replyToolCalls(steps)
	if len(calls) == 0 {
		return
	}
	t.mu.Lock()
	t.calls = append(t.calls, calls...)
	t.mu.Unlock()
}

// take 取走记录：一轮拆成多条发送时只挂在第一条上。
func (t *replyToolTrace) take() []ReplyToolCall {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	calls := t.calls
	t.calls = nil
	if len(calls) > replyToolTraceLimit {
		calls = calls[len(calls)-replyToolTraceLimit:]
	}
	return calls
}

func replyToolCalls(steps []agent.Step) []ReplyToolCall {
	calls := make([]ReplyToolCall, 0, len(steps))
	for _, step := range steps {
		tool := strings.TrimSpace(step.Tool)
		if tool == "" || step.Skipped || toolCallMemoryIgnored[tool] {
			continue
		}
		calls = append(calls, ReplyToolCall{
			Tool:    tool,
			Summary: toolCallInputSummary(step.Input),
			Failed:  strings.TrimSpace(step.Error) != "",
		})
	}
	return calls
}

// replyToolTraceNote 是渲染在机器人回复后面的那条记录；没有调用时返回 false。
func replyToolTraceNote(calls []ReplyToolCall) (llm.Message, bool) {
	if len(calls) == 0 {
		return llm.Message{}, false
	}
	items := make([]string, 0, len(calls))
	for _, call := range calls {
		item := call.Tool
		if call.Summary != "" {
			item += "「" + call.Summary + "」"
		}
		if call.Failed {
			item += "（调用失败）"
		}
		items = append(items, item)
	}
	return llm.Message{
		Role:     llm.RoleUser,
		Content:  replyToolTraceNotePrefix + strings.Join(items, "；") + "。" + replyToolTraceNoteSuffix,
		Priority: llm.MessagePriorityHistory,
	}, true
}

// toolCallInputSummary 只取能说明「查了什么」的那几个参数，不把整份入参塞进上下文。
func toolCallInputSummary(input map[string]any) string {
	parts := make([]string, 0, 3)
	for _, key := range []string{"query", "queries", "url", "action", "operation", "message_ids", "keyword", "user_id"} {
		value, ok := input[key]
		if !ok {
			continue
		}
		text := strings.TrimSpace(toolCallInputValue(value))
		if text == "" {
			continue
		}
		parts = append(parts, text)
		if len(parts) == 3 {
			break
		}
	}
	return truncateRunes(strings.Join(parts, " · "), 80)
}

func toolCallInputValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, fmt.Sprint(item))
		}
		return strings.Join(items, ",")
	case []string:
		return strings.Join(typed, ",")
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}
