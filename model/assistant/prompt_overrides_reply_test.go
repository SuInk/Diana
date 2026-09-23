// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 覆盖值要真的进到正式回复的系统提示词里，头部和尾部都算。
func TestReplyPromptOverridesReachSystemPrompt(t *testing.T) {
	cfg := BotConfig{
		GroupTriggers: []string{"Diana"},
		PromptOverrides: PromptOverrides{
			promptToolFindingsSpec.Key:         "查过没找到就直说。",
			promptAliasSpec.Key:                "大家叫你 {aliases}。",
			promptPersonaClosingAnchorSpec.Key: "最后：别忘了你是谁。",
		},
	}
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", RawMessage: "hi"}
	head, tail := runtime.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicyFor(UserMemoryProfile{}, "owner", "u1"), true, nil)
	if !strings.Contains(head, "\n查过没找到就直说。\n") || strings.Contains(head, promptToolFindings) {
		t.Fatalf("工具结果规则没有被覆盖：\n%s", head)
	}
	if !strings.Contains(head, `大家叫你 "Diana"。`) {
		t.Fatalf("别名占位符没有渲染：\n%s", head)
	}
	if !strings.Contains(tail, "最后：别忘了你是谁。\n"+replyDepthClosingAnchor) {
		t.Fatalf("尾部锚点没有被覆盖：\n%s", tail)
	}
}

// 拒答档位的正文可以改，拒答标志那段说明永远跟在后面：运行时靠标志计数。
func TestRefusalOverrideKeepsMarkerContract(t *testing.T) {
	cfg := BotConfig{PromptOverrides: PromptOverrides{promptRefusalSmartSpec.Key: "不想答就换个话题。"}}
	got := refusalStrategyPrompt(RefusalStrategySmart, cfg)
	if got != promptRefusalBase+"不想答就换个话题。"+promptRefusalTail {
		t.Fatalf("拒答规则 = %q", got)
	}
	if refusalStrategyPrompt(RefusalStrategySmart) != promptRefusalBase+promptRefusalSmart+promptRefusalTail {
		t.Fatal("不传配置时应当是内置默认值")
	}

	scope := newIdentityPrivacyScopeWithSalt("salt")
	scope.overrides = PromptOverrides{promptIdentityPrivacySpec.Key: "ID 都换成了别名。"}
	protected := scope.protectRequest(llm.GenerateRequest{Messages: []llm.Message{{Role: llm.RoleSystem, Content: "人设"}}})
	if want := "ID 都换成了别名。" + llmIdentityPrivacyContract + "\n\n人设"; protected.Messages[0].Content != want {
		t.Fatalf("隐私说明 = %q", protected.Messages[0].Content)
	}
}

func TestReplyPromptPlaceholdersRender(t *testing.T) {
	cfg := BotConfig{MaxReplyChars: 120, PromptOverrides: PromptOverrides{promptReplyBudgetSpec.Key: "每条最多 {limit} 字。"}}
	messages := withReplyGenerationBudgetForConfig([]llm.Message{{Role: llm.RoleUser, Content: "问题"}}, cfg)
	if len(messages) != 2 || messages[0].Content != "每条最多 120 字。" {
		t.Fatalf("长度预算 = %#v", messages)
	}

	router := proactiveReplyRouterPromptForChatIn("", "", chatInSettings{Enabled: true, Level: ChatInLevelLow}, false, BotConfig{
		PromptOverrides: PromptOverrides{promptRouterChatInLevelSpec.Key: "档位 {level}（{label}）。"},
	})
	if !strings.HasSuffix(router, "\n\n档位 low（"+ChatInLevelLow.Label()+"）。") {
		t.Fatalf("路由档位说明 = %q", router)
	}
}
