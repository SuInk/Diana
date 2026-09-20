// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strconv"
	"strings"
	"testing"
)

// 记忆段排在历史之后，里面任何「每轮都变」的内容都会推动它后面的所有段落
// （媒体索引、来源、工具记录、对话对象、时钟），把供应商的前缀缓存从这里切断。
//
// 累计互动次数每条消息加一，是其中最糟的一个：本机实测连发两条，前 70 条消息逐字
// 相同，却只命中到系统提示词结束，历史段一个 token 都没算进去。
//
// 好感度数值同理，而且它在提示词里从来不该被引用：promptLongTermMemory 规定「不要
// 报出好感度数值，除非用户明确问起」，真被问起时 relationship 工具又要求必须调用。
func TestMemoryContextHasNoPerTurnCounters(t *testing.T) {
	policy := RelationshipPolicy{Owner: true}
	build := func(messages, favorability int) string {
		profile := UserMemoryProfile{
			UserID:       "100001",
			DisplayName:  "Winter",
			MessageCount: messages,
			Favorability: favorability,
		}
		text, _, _ := formatStructuredMemoryContextWithTokenBudgetDetailed(profile, policy, nil, 3200)
		return text
	}

	first := build(43, 100)
	for _, forbidden := range []string{"累计互动", "好感度", "关系等级"} {
		if strings.Contains(first, forbidden) {
			t.Fatalf("记忆段不得包含每轮变化的 %q（会推动后面所有段落，切断前缀缓存）: %s", forbidden, first)
		}
	}
	if strings.Contains(first, strconv.Itoa(43)) {
		t.Fatalf("互动次数的数值仍出现在记忆段里: %s", first)
	}

	// 同一个人，互动次数和好感度都变了，这一段必须逐字不变。
	if second := build(44, 120); second != first {
		t.Fatalf("互动次数或好感度变化后记忆段跟着变了，缓存会从这里断\n先: %q\n后: %q", first, second)
	}
}
