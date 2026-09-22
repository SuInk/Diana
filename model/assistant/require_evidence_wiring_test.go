// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// needs_evidence 搭的是每轮都跑的功能路由器的车，不额外加一次模型调用。
func TestParseReplyIntentDecisionReadsNeedsEvidence(t *testing.T) {
	raw := `{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":false,"needs_evidence":true}`
	_, scope, ok := parseReplyIntentDecision(raw, agent.NewToolRegistry())
	if !ok || !scope.Routed {
		t.Fatalf("ok=%v scope=%+v", ok, scope)
	}
	if !scope.NeedsEvidence {
		t.Fatal("needs_evidence=true 没有被读出来")
	}
}

// 模型漏填这个字段时只按 false 处理，不能把整个路由结果判废——否则上下文裁剪和
// 工具选择会一起失效，代价比漏搜一次大得多。
func TestParseReplyIntentDecisionToleratesMissingNeedsEvidence(t *testing.T) {
	raw := `{"action":"none","prompt":"","tools":[],"context_message_ids":[],"keep_older_summary":true}`
	_, scope, ok := parseReplyIntentDecision(raw, agent.NewToolRegistry())
	if !ok || !scope.Routed {
		t.Fatalf("字段缺失不该让路由失效: ok=%v scope=%+v", ok, scope)
	}
	if scope.NeedsEvidence {
		t.Fatal("缺失时应按 false 处理")
	}
	if !scope.KeepContextSummary {
		t.Fatal("其余字段应当照常生效")
	}
}

func TestRequireEvidenceContextRoundTrip(t *testing.T) {
	if requireEvidenceFromContext(context.Background()) {
		t.Fatal("没有标记时不该要求证据")
	}
	if !requireEvidenceFromContext(withRequireEvidence(context.Background())) {
		t.Fatal("标记没有传下去")
	}
}

// 提示词必须把两种严格度分开写：tools 宽选（拿不准就保留），needs_evidence 严选
// （拿不准就 false）。混用会让闲聊也被逼着检索。
func TestReplyIntentPromptSeparatesEvidenceFromToolSelection(t *testing.T) {
	systemPrompt, _ := replyIntentPrompts(agent.NewToolRegistry())
	for _, expected := range []string{
		`"needs_evidence":false`,
		"tools 拿不准就保留，needs_evidence 拿不准就填 false",
		"聊天记录里别人提过某件事不等于已经核实",
		"讲原理、讲概念、写代码、创作、闲聊",
	} {
		if !strings.Contains(systemPrompt, expected) {
			t.Fatalf("路由提示词缺少 %q", expected)
		}
	}
}

// 没有注册表时是纯图片路由，不该混进工具选择和证据要求那一段。
func TestReplyIntentPromptWithoutRegistryStaysVisualOnly(t *testing.T) {
	systemPrompt, _ := replyIntentPrompts(nil)
	if strings.Contains(systemPrompt, "needs_evidence") {
		t.Fatal("纯图片路由不该出现 needs_evidence")
	}
}
