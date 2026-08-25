package assistant

import (
	"strings"
	"testing"
)

// 用户在群里看到的是「一整条」：配置里存着旧版默认文案时，提示词整段都在反对分条。
// 升级之后同一份配置必须自愈——旧文案被换掉，内置分条规则出现在提示词里。
func TestStaleConfigStopsSuppressingSegmentation(t *testing.T) {
	stale := BotConfig{
		ReplyStyle:               ReplyStyleAssistant,
		PromptPlaintextRulesText: legacyPromptPlaintextRules[1],
	}.WithDefaults()
	runtime := NewRuntime(stale, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	relationship := RelationshipPolicyFor(UserMemoryProfile{}, stale.OwnerID, "1")
	event := MessageEvent{Kind: EventKindGroup, GroupID: "1", UserID: "1"}
	prompt := runtime.systemPromptWithRelationshipAndAgentTools(event, nil, false, relationship, true, nil)

	if strings.Contains(prompt, "都必须放在同一条 OneBot v11 消息里") {
		t.Fatalf("旧版反分条文案还在提示词里：%q", prompt)
	}
	if !strings.Contains(prompt, "意群边界写 "+notificationSplitMarker) {
		t.Fatalf("提示词里没有分条规则：%q", prompt)
	}

	// 模型照做之后投递侧真的分成两条。
	chunks := splitReply("结论是这样"+notificationSplitMarker+"理由是那样", stale.DirectReplyChunkSize)
	if len(chunks) != 2 || chunks[0] != "结论是这样" || chunks[1] != "理由是那样" {
		t.Fatalf("投递没有按标记分条：%q", chunks)
	}
}
