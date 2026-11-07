// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"reflect"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// 预裁剪按整轮丢历史：机器人发言的 send 调用和「已发送」结果永远在同一个单元里，
// 和同一轮的群友消息一起走；没有轮次分组时也不会把结果单独丢掉。
func TestBudgetPretrimKeepsHistoryReplyPairsInOneUnit(t *testing.T) {
	history := func(content, group string) llm.Message {
		return llm.Message{Role: llm.RoleUser, Content: content, Priority: llm.MessagePriorityHistory, ContextGroup: group}
	}
	reply := func(id, text, group string) []llm.Message {
		pair := agent.HistoryReplyMessages(id, text)
		for index := range pair {
			pair[index].Priority, pair[index].ContextGroup = llm.MessagePriorityHistory, group
		}
		return pair
	}
	var messages []llm.Message
	messages = append(messages, history("[历史] 甲: 早", "t1"))
	messages = append(messages, reply("a", "早呀", "t1")...)
	messages = append(messages, reply("b", "今天好冷", "")...)
	messages = append(messages, history("[历史] 乙: 是啊", "t2"))
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "当前", Priority: llm.MessagePriorityCurrent})

	var got [][]int
	for _, unit := range budgetPretrimHistoryUnits(messages, len(messages)-1) {
		got = append(got, unit.indexes)
	}
	if want := [][]int{{0, 1, 2}, {3, 4}, {5}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("units = %v, want %v", got, want)
	}
}
