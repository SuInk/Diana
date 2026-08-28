// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// forwardChannel 按 get_forward_msg 的 id 分别应答，用来构造转发套转发的场景。
type forwardChannel struct {
	mu        sync.Mutex
	responses map[string]map[string]any
	requested []string
}

func (c *forwardChannel) Connect(context.Context, EventHandler) error { return nil }
func (c *forwardChannel) Send(context.Context, OutgoingMessage) error { return nil }
func (c *forwardChannel) Status() ChannelStatus                       { return ChannelStatus{} }
func (c *forwardChannel) Close() error                                { return nil }

func (c *forwardChannel) CallAPI(_ context.Context, action string, params map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if action != "get_forward_msg" {
		return map[string]any{"message_id": int64(42)}, nil
	}
	id := stringFromAny(params["id"])
	c.requested = append(c.requested, id)
	if response, ok := c.responses[id]; ok {
		return response, nil
	}
	return nil, fmt.Errorf("unknown forward id %q", id)
}

func (c *forwardChannel) requestedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.requested...)
}

// textNode 造一个普通的转发节点。
func textNode(name, text string) map[string]any {
	return map[string]any{
		"type": "node",
		"data": map[string]any{
			"name":    name,
			"content": []any{map[string]any{"type": "text", "data": map[string]any{"text": text}}},
		},
	}
}

// forwardRefNode 造一个「里面又是一张转发卡片」的节点：只给 id，内容要再取一次。
func forwardRefNode(name, forwardID, summary string) map[string]any {
	return map[string]any{
		"type": "node",
		"data": map[string]any{
			"name": name,
			"content": []any{map[string]any{
				"type": "forward",
				"data": map[string]any{"id": forwardID, "summary": summary},
			}},
		},
	}
}

// 转发卡片里再放一张转发卡片时，内层只有一个 [聊天记录] 占位。不接着展开，
// 模型拿到的就只是这个占位，做不了任何判断。
func TestRuntimeExpandsNestedForwardMessages(t *testing.T) {
	channel := &forwardChannel{responses: map[string]map[string]any{
		"forward-outer": {"messages": []any{
			forwardRefNode("碎月", "forward-inner", "[聊天记录]"),
			textNode("碎月", "吓哭了"),
		}},
		"forward-inner": {"messages": []any{
			textNode("Alice", "服务器今天崩了三次"),
			textNode("Bob", "确实，我这边也连不上"),
		}},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)

	event := runtime.enrichForwardMessages(context.Background(), MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "123",
		UserID:    "10001",
		MessageID: "new",
		Segments:  []MessageSegment{{Type: "forward", Data: map[string]string{"id": "forward-outer"}}},
	})

	text := PlainText(event.Segments)
	for _, want := range []string{"吓哭了", "Alice: 服务器今天崩了三次", "Bob: 确实，我这边也连不上"} {
		if !strings.Contains(text, want) {
			t.Fatalf("展开结果缺少 %q：%s", want, text)
		}
	}
	// 内层区块要写清楚嵌套关系：正文里的占位可能只剩「[聊天记录]」这种摘要，
	// 不点明谁套着谁，模型对不上号。
	if !strings.Contains(text, "【合并转发 forward-inner】（嵌套在 forward-outer 内）") {
		t.Fatalf("内层区块没有标出嵌套关系：%s", text)
	}
	if got := channel.requestedIDs(); len(got) != 2 || got[0] != "forward-outer" || got[1] != "forward-inner" {
		t.Fatalf("requested = %#v", got)
	}
	// 原始文本也要带上内层内容，按纯文本读历史的地方才看得到。
	if !strings.Contains(event.RawMessage, "服务器今天崩了三次") {
		t.Fatalf("raw message = %q", event.RawMessage)
	}
}

// 引用一条合并转发再问机器人，是这个功能最常见的用法：内容挂在被引用的消息上。
func TestRuntimeExpandsNestedForwardInQuotedMessage(t *testing.T) {
	channel := &forwardChannel{responses: map[string]map[string]any{
		"forward-outer": {"messages": []any{forwardRefNode("碎月", "forward-inner", "[聊天记录]")}},
		"forward-inner": {"messages": []any{textNode("Alice", "内层的真实内容")}},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)

	event := runtime.enrichForwardMessages(context.Background(), MessageEvent{
		Kind:      EventKindGroup,
		GroupID:   "123",
		UserID:    "10001",
		MessageID: "new",
		Segments:  []MessageSegment{{Type: "text", Data: map[string]string{"text": "嘉然做事实核查"}}},
		Quoted: &QuotedMessage{
			MessageID: "quoted",
			UserID:    "20002",
			Segments:  []MessageSegment{{Type: "forward", Data: map[string]string{"id": "forward-outer"}}},
		},
	})

	if event.Quoted == nil {
		t.Fatal("quoted message was dropped")
	}
	if text := PlainText(event.Quoted.Segments); !strings.Contains(text, "Alice: 内层的真实内容") {
		t.Fatalf("被引用的转发没有展开到底：%s", text)
	}
}

// 已经内联了内容的内层转发不该再取一次，否则同样的内容会被贴两遍。
func TestRuntimeDoesNotRefetchInlinedNestedForward(t *testing.T) {
	channel := &forwardChannel{responses: map[string]map[string]any{
		"forward-outer": {"messages": []any{
			map[string]any{
				"type": "node",
				"data": map[string]any{
					"name": "碎月",
					"content": []any{map[string]any{
						"type": "forward",
						"data": map[string]any{
							"id":      "forward-inline",
							"content": []any{textNode("Alice", "已经内联的内容")},
						},
					}},
				},
			},
		}},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)

	runtime.enrichForwardMessages(context.Background(), MessageEvent{
		Kind:     EventKindGroup,
		GroupID:  "123",
		UserID:   "10001",
		Segments: []MessageSegment{{Type: "forward", Data: map[string]string{"id": "forward-outer"}}},
	})

	if got := channel.requestedIDs(); len(got) != 1 || got[0] != "forward-outer" {
		t.Fatalf("内联内容被重复拉取：%#v", got)
	}
}

// 互相引用的转发不能把展开拖进死循环。
func TestRuntimeStopsOnCyclicForwardReferences(t *testing.T) {
	channel := &forwardChannel{responses: map[string]map[string]any{
		"forward-a": {"messages": []any{forwardRefNode("甲", "forward-b", "[聊天记录]")}},
		"forward-b": {"messages": []any{forwardRefNode("乙", "forward-a", "[聊天记录]")}},
	}}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)

	runtime.enrichForwardMessages(context.Background(), MessageEvent{
		Kind:     EventKindGroup,
		GroupID:  "123",
		UserID:   "10001",
		Segments: []MessageSegment{{Type: "forward", Data: map[string]string{"id": "forward-a"}}},
	})

	got := channel.requestedIDs()
	if len(got) != 2 {
		t.Fatalf("每条转发只该取一次，实际 = %#v", got)
	}
}

// 深层嵌套要在上限处停住，不能让一条消息触发无限多次 OneBot 调用。
func TestRuntimeLimitsNestedForwardDepth(t *testing.T) {
	responses := map[string]map[string]any{}
	for depth := range 10 {
		id := fmt.Sprintf("forward-%d", depth)
		next := fmt.Sprintf("forward-%d", depth+1)
		responses[id] = map[string]any{"messages": []any{forwardRefNode("甲", next, "[聊天记录]")}}
	}
	channel := &forwardChannel{responses: responses}
	runtime := NewRuntime(BotConfig{}, channel, NewPluginManager(), nil, nil, nil, nil)

	runtime.enrichForwardMessages(context.Background(), MessageEvent{
		Kind:     EventKindGroup,
		GroupID:  "123",
		UserID:   "10001",
		Segments: []MessageSegment{{Type: "forward", Data: map[string]string{"id": "forward-0"}}},
	})

	// 第 0 层是入口，往下最多再展 maxForwardExpandDepth 层。
	if got := channel.requestedIDs(); len(got) != maxForwardExpandDepth+1 {
		t.Fatalf("展开层数 = %d，期望 %d：%#v", len(got), maxForwardExpandDepth+1, got)
	}
}
