// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
)

// 跨群检索命中的消息，模型调 around 时漏传 group_id：权限允许就自动去那个群读前后文。
func TestHistoryAroundFindsMessageInOtherGroupWithoutGroupID(t *testing.T) {
	r := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(newSemanticTimelineStore())
	r.remember(chatHistoryTextEvent(100, "alice", "Alice", "before", "前一句"))
	r.remember(chatHistoryTextEvent(110, "alice", "Alice", "target", "那个 dsh 远程项目有 7.6k star"))
	r.remember(chatHistoryTextEvent(120, "bob", "Bob", "after", "后一句"))
	event := MessageEvent{Kind: EventKindGroup, GroupID: "group-2", Time: 200}

	raw, err := newDianaChatHistoryTool(r, event).Run(context.Background(), map[string]any{"operation": "around", "message_id": "target", "before": 1, "after": 1})
	if err != nil {
		t.Fatalf("漏传 group_id 时应自动定位到其他群：%v", err)
	}
	got := decodeHistoryPage(t, raw)
	if len(got.Items) != 3 || got.Items[1].Text != "那个 dsh 远程项目有 7.6k star" {
		t.Fatalf("items=%+v", got.Items)
	}
	if !strings.Contains(got.Message, "group-1") || got.Items[1].GroupID != "group-1" {
		t.Fatalf("结果要标明来自哪个群：message=%q item=%+v", got.Message, got.Items[1])
	}
}

// 没开跨群记忆时不越界，报错提示跨群命中要带 group_id。
func TestHistoryAroundDoesNotLeaveCurrentGroupWithoutCrossGroupMemory(t *testing.T) {
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(newSemanticTimelineStore())
	r.remember(chatHistoryTextEvent(110, "alice", "Alice", "target", "别的群的消息"))
	_, err := newDianaChatHistoryTool(r, MessageEvent{Kind: EventKindGroup, GroupID: "group-2", Time: 200}).Run(context.Background(), map[string]any{"operation": "around", "message_id": "target"})
	if err == nil || !strings.Contains(err.Error(), "找不到消息 target") || !strings.Contains(err.Error(), "group_id") {
		t.Fatalf("err=%v", err)
	}
}

// 同一编号在多个群里都有：不猜，列出候选群让模型带 group_id 重试。
func TestHistoryAroundAmbiguousMessageAcrossGroups(t *testing.T) {
	r := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetMessageHistoryStore(newSemanticTimelineStore())
	first := chatHistoryTextEvent(110, "alice", "Alice", "target", "一群")
	second := chatHistoryTextEvent(120, "bob", "Bob", "target", "三群")
	second.GroupID = "group-3"
	r.remember(first)
	r.remember(second)
	_, err := newDianaChatHistoryTool(r, MessageEvent{Kind: EventKindGroup, GroupID: "group-2", Time: 200}).Run(context.Background(), map[string]any{"operation": "around", "message_id": "target"})
	if err == nil || !strings.Contains(err.Error(), "group-1") || !strings.Contains(err.Error(), "group-3") {
		t.Fatalf("err=%v", err)
	}
}

type prefixLookupHistoryStore struct {
	*semanticTimelineStore
}

func (s prefixLookupHistoryStore) FindMessageEventsBySessionPrefix(_ context.Context, prefix, messageID string, limit int) ([]SessionMessageEvent, error) {
	var out []SessionMessageEvent
	for session, events := range s.events {
		if !strings.HasPrefix(session, prefix) {
			continue
		}
		for _, event := range events {
			if event.MessageID == messageID {
				out = append(out, SessionMessageEvent{Session: session, Event: event})
				break
			}
		}
	}
	return out, nil
}

// 消息只在持久化记录里（内存历史早就滚掉了）时，也能通过存储定位到所在群。
func TestHistoryAroundLocatesOtherGroupThroughStore(t *testing.T) {
	r := NewRuntime(BotConfig{CrossGroupMemoryEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	base := newSemanticTimelineStore()
	target := chatHistoryTextEvent(110, "alice", "Alice", "stored-only", "只在库里")
	if err := base.AppendMessageEvent(context.Background(), sessionKey(target), target); err != nil {
		t.Fatal(err)
	}
	r.SetMessageHistoryStore(prefixLookupHistoryStore{semanticTimelineStore: base})
	if groups := r.groupsContainingMessage(context.Background(), MessageEvent{Kind: EventKindGroup, GroupID: "group-2"}, "stored-only"); len(groups) != 1 || groups[0] != "group-1" {
		t.Fatalf("groups=%v", groups)
	}
	raw, err := newDianaChatHistoryTool(r, MessageEvent{Kind: EventKindGroup, GroupID: "group-2", Time: 200}).Run(context.Background(), map[string]any{"operation": "around", "message_id": "stored-only"})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeHistoryPage(t, raw); len(got.Items) == 0 || got.Items[0].Text != "只在库里" {
		t.Fatalf("got=%+v", got)
	}
}
