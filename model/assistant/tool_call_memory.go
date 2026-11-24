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
// 落库，渲染历史时紧跟那条回复。重启不丢，也不会被同群别人的调用挤掉，回复
// 滚出历史窗口时记录一起走。
//
// 11-24 起记录带着调用的原样参数，渲染成回复前面的真实工具调用加占位结果（见
// agent.HistoryToolMessages），和续接前缀同一个形态；之前落库的记录没有参数，仍渲染成
// 回复后面的那句【运行时记录】。
const (
	replyToolTraceLimit      = 8
	replyToolTraceNotePrefix = "【运行时记录，不是你说过的话】你发上面这条回复前实际调用过："
	replyToolTraceNoteSuffix = "有人问「搜了吗」「查过没」时照这份记录如实回答，不要因为对方质疑就改口说没查，也不要声称调用过记录里没有的工具；没人问时不要复述。"
)

// ReplyToolCall 是一条回复发出前实际执行成功的工具调用，随出站消息落库。Call 和
// Arguments 是发给模型时的调用原样（按需工具是 tools_execute 包着的那一层）；Tool 和
// Summary 是给老形态旁注用的工具名和参数摘要。
type ReplyToolCall struct {
	Tool      string         `json:"tool"`
	Summary   string         `json:"summary,omitempty"`
	Failed    bool           `json:"failed,omitempty"`
	Call      string         `json:"call,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// replyToolTrace 在一次回复生成期间收集工具调用，交给随后发出的第一条消息。
type replyToolTrace struct {
	mu    sync.Mutex
	calls []ReplyToolCall
	seen  map[string]bool
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

// add 记下这次运行里执行成功的工具调用。同一轮整轮重来时，下一版的记录里带着上一版的
// 调用（agent.Request.Redo），按调用 ID 去重。
func (t *replyToolTrace) add(executed []llm.Message) {
	if t == nil {
		return
	}
	calls := agent.SucceededToolCalls(executed)
	if len(calls) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[string]bool{}
	}
	for _, call := range calls {
		if t.seen[call.ID] {
			continue
		}
		t.seen[call.ID] = true
		t.calls = append(t.calls, replyToolCall(call))
	}
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

// replyToolCall 把一次调用写成落库的记录。按需工具经 tools_execute 调用，工具名和参数
// 在它的 name、input 里。
func replyToolCall(call llm.ToolCall) ReplyToolCall {
	tool, input := call.Name, call.Arguments
	if call.Name == agent.ToolsExecuteToolName {
		if name, _ := call.Arguments["name"].(string); strings.TrimSpace(name) != "" {
			tool = strings.TrimSpace(name)
		}
		input, _ = call.Arguments["input"].(map[string]any)
	}
	return ReplyToolCall{Tool: tool, Summary: toolCallInputSummary(input), Call: call.Name, Arguments: call.Arguments}
}

// replyHistoryToolMessages 把回复带着的调用记录渲染成历史里的工具调用加占位结果，放在
// 那条回复前面；记录里没有调用原样（11-24 之前落库的）时返回 nil，由 replyToolTraceNote
// 渲染成旁注。
func replyHistoryToolMessages(id string, calls []ReplyToolCall) []llm.Message {
	var wire []llm.ToolCall
	for _, call := range calls {
		if call.Call != "" && !call.Failed {
			wire = append(wire, llm.ToolCall{Name: call.Call, Arguments: call.Arguments})
		}
	}
	return agent.HistoryToolMessages(id, wire)
}

// replyToolTraceNote 是老形态记录渲染在机器人回复后面的那句旁注；没有老形态的调用时返回 false。
func replyToolTraceNote(calls []ReplyToolCall) (llm.Message, bool) {
	items := make([]string, 0, len(calls))
	for _, call := range calls {
		if call.Call != "" {
			continue
		}
		item := call.Tool
		if call.Summary != "" {
			item += "「" + call.Summary + "」"
		}
		if call.Failed {
			item += "（调用失败）"
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return llm.Message{}, false
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
