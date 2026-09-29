// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 同一个群里被点名和主动接话交替出现是常态。群聊场景那一句以前在 system 头部，
// 每切换一次，后面整段历史的前缀缓存全部作废。两种轮次的请求从头到最后一条历史
// 必须逐字相同，差别只能出现在历史之后的尾部。
func TestGroupScopeSwitchOnlyChangesTheRequestTail(t *testing.T) {
	capture := func(proactive bool) llm.GenerateRequest {
		provider := &capturingLLMProvider{reply: "好"}
		runtime := NewRuntime(BotConfig{OwnerID: "10001", GroupTriggers: []string{"Diana"}}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
			return provider, nil
		})
		// 线上盐落库、跨轮不变；这里两轮各起一个 Runtime，手动钉住同一个盐。
		runtime.aliasSalt = "group-scope-tail"
		for index, text := range []string{"周末去哪玩", "去爬山吧"} {
			runtime.remember(MessageEvent{
				Kind: EventKindGroup, GroupID: "20001", UserID: "10002", SenderName: "路人",
				MessageID: "old-" + itoa(index), Time: int64(1000 + index), RawMessage: text,
				Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
			})
		}
		event := MessageEvent{
			Kind: EventKindGroup, GroupID: "20001", UserID: "10003", SenderName: "群友",
			MessageID: "new-1", Time: 1100, RawMessage: "山上冷不冷",
			Segments:       []MessageSegment{{Type: "text", Data: map[string]string{"text": "山上冷不冷"}}},
			proactiveReply: proactive,
		}
		if _, err := runtime.replyTo(context.Background(), event, "山上冷不冷"); err != nil {
			t.Fatalf("replyTo(proactive=%v) error = %v", proactive, err)
		}
		return provider.requestSnapshot()
	}
	triggered, proactive := capture(false), capture(true)

	lastHistory := -1
	for index, message := range triggered.Messages {
		if strings.Contains(message.Content, "去爬山吧") {
			lastHistory = index
		}
	}
	if lastHistory < 0 {
		t.Fatalf("请求里没有历史消息：%#v", triggered.Messages)
	}
	if len(proactive.Messages) <= lastHistory {
		t.Fatalf("主动接话那轮的消息条数不对：%#v", proactive.Messages)
	}
	for index := 0; index <= lastHistory; index++ {
		if triggered.Messages[index].Role != proactive.Messages[index].Role || triggered.Messages[index].Content != proactive.Messages[index].Content {
			t.Fatalf("第 %d 条消息（历史及之前）随轮次变了：\n%q\n%q", index, triggered.Messages[index].Content, proactive.Messages[index].Content)
		}
	}

	// 两种说法都得还在，只是挪到了历史之后。
	tailOf := func(req llm.GenerateRequest) string {
		var tail strings.Builder
		for _, message := range req.Messages[lastHistory+1:] {
			tail.WriteString(message.Content + "\n")
		}
		return tail.String()
	}
	if tail := tailOf(triggered); !strings.Contains(tail, promptGroupScope) || strings.Contains(tail, promptGroupScopeProactive) {
		t.Fatalf("被点名那轮的尾部缺少场景说明：%q", tail)
	}
	if tail := tailOf(proactive); !strings.Contains(tail, promptGroupScopeProactive) || strings.Contains(tail, promptGroupScope) {
		t.Fatalf("主动接话那轮的尾部缺少场景说明：%q", tail)
	}
}

// 按轮次变化的段落（场景说明、闲聊插话时消失的换行分条说明、插件结果为准）都不进
// head；head 在被点名、主动接话、闲聊插话、带插件结果这几种轮次之间逐字相同。
func TestSystemHeadIsStableAcrossTurnKinds(t *testing.T) {
	lineSplit := true
	runtime := NewRuntime(BotConfig{OwnerID: "10001", ReplyLineSplitEnabled: &lineSplit}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	base := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10002", RawMessage: "在吗"}
	relationship := RelationshipPolicyForConfig(runtime.effectiveConfigForEvent(base), UserMemoryProfile{}, base.UserID)
	parts := func(event MessageEvent, proactive bool, plugins []PluginResponse) (string, string) {
		return runtime.systemPromptPartsWithRelationshipAndAgentTools(event, plugins, proactive, relationship, true, nil)
	}
	triggeredHead, triggeredTail := parts(base, false, nil)
	proactiveEvent := base
	proactiveEvent.proactiveReply = true
	chatInEvent := base
	chatInEvent.proactiveReply, chatInEvent.chatInReply = true, true
	for name, got := range map[string][2]string{
		"proactive": func() [2]string { h, tl := parts(proactiveEvent, true, nil); return [2]string{h, tl} }(),
		"chatIn":    func() [2]string { h, tl := parts(chatInEvent, true, nil); return [2]string{h, tl} }(),
		"plugin": func() [2]string {
			h, tl := parts(base, false, []PluginResponse{{Context: "天气：晴"}})
			return [2]string{h, tl}
		}(),
	} {
		if got[0] != triggeredHead {
			t.Fatalf("%s 那轮的 head 和被点名那轮不同：\n%q\n%q", name, triggeredHead, got[0])
		}
		if got[1] == triggeredTail {
			t.Fatalf("%s 那轮的 tail 应该有差别：%q", name, got[1])
		}
	}
	lineSplitRule := replyLineSplitPrompt(chatSplitLimitsForEvent(runtime.effectiveConfigForEvent(base), base))
	if lineSplitRule == "" || strings.Contains(triggeredHead, lineSplitRule) || !strings.Contains(triggeredTail, lineSplitRule) {
		t.Fatalf("换行分条说明应在尾部：head=%q tail=%q", triggeredHead, triggeredTail)
	}
	if _, tail := parts(chatInEvent, true, nil); !strings.Contains(tail, lineSplitRule) {
		t.Fatalf("闲聊插话那轮也按行分条，尾部应该说明：%q", tail)
	}
	if _, tail := parts(base, false, []PluginResponse{{Context: "天气：晴"}}); !strings.Contains(tail, promptPluginAuthority) {
		t.Fatalf("插件结果为准应在尾部：%q", tail)
	}
}

// 场景说明进了尾部，WebUI 里改过的文案照样生效。
func TestGroupScopeOverrideStillAppliesInTail(t *testing.T) {
	const custom = "当前是群聊，有人叫你。"
	runtime := NewRuntime(BotConfig{OwnerID: "10001", PromptOverrides: PromptOverrides{promptGroupScopeSpec.Key: custom}}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10002", RawMessage: "在吗"}
	head, tail := runtime.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, RelationshipPolicyForConfig(runtime.effectiveConfigForEvent(event), UserMemoryProfile{}, event.UserID), true, nil)
	if strings.Contains(head, custom) || !strings.Contains(tail, custom) {
		t.Fatalf("覆盖后的场景说明应在尾部：head=%q tail=%q", head, tail)
	}
}
