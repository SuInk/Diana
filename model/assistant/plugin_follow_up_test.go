package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

func TestPluginFollowUpAddsNaturalComment(t *testing.T) {
	// 插件把内容发出去之后，机器人应该像群友那样再接一句。
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{"这个我看过，确实好笑"}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1"}
	if err := runtime.sendDirectPluginResponse(context.Background(), event, "链接解析结果", nil, nil); err != nil {
		t.Fatal(err)
	}
	runtime.maybeSendPluginFollowUp(context.Background(), event, PluginResponse{FollowUp: true})

	waitForCondition(t, time.Second, func() bool { return len(channel.sentSnapshot()) == 2 })
	sent := channel.sentSnapshot()
	if sent[0].Text != "链接解析结果" || sent[1].Text != "这个我看过，确实好笑" {
		t.Fatalf("sent = %#v", sent)
	}

	// 跟评必须看得到自己刚发出的那条内容，否则无从评论。
	requests := provider.requestsSnapshot()
	if len(requests) == 0 {
		t.Fatal("follow-up did not reach the model")
	}
	// 历史里的自发消息会带上引用标记前缀，这里只要求内容出现即可。
	var sawSentContent bool
	for _, msg := range requests[len(requests)-1].Messages {
		if msg.Role == llm.RoleAssistant && strings.Contains(msg.Content, "链接解析结果") {
			sawSentContent = true
		}
	}
	if !sawSentContent {
		t.Fatalf("follow-up prompt missed the just-sent content: %#v", requests[len(requests)-1].Messages)
	}
}

func TestPluginFollowUpStaysQuietWhenNotRequested(t *testing.T) {
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{"不该出现"}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1"}
	runtime.maybeSendPluginFollowUp(context.Background(), event, PluginResponse{FollowUp: false})
	if len(channel.sentSnapshot()) != 0 || len(provider.requestsSnapshot()) != 0 {
		t.Fatal("follow-up ran without the plugin asking for it")
	}
}

func TestPluginFollowUpSkipsWhenModelHasNothingToSay(t *testing.T) {
	// 没什么可说时模型回 SKIP，不该把这个词发到群里。
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{"SKIP"}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1"}
	runtime.maybeSendPluginFollowUp(context.Background(), event, PluginResponse{FollowUp: true})
	if len(channel.sentSnapshot()) != 0 {
		t.Fatalf("SKIP was sent to the group: %#v", channel.sentSnapshot())
	}
}

func TestResolverReplyDropsModelFacingLabel(t *testing.T) {
	// Context 的「链接解析结果：」是给模型的来源标签，发到群里就是机器腔。
	resp := PluginResponse{
		Handled: true,
		Context: "链接解析结果：\n某站视频 · 标题",
		Reply:   "某站视频 · 标题",
	}
	if got := directPluginReply(resp); strings.Contains(got, "链接解析结果") {
		t.Fatalf("visible reply leaked the model-facing label: %q", got)
	}
}

func TestResolverStaysSilentWithNothingToSend(t *testing.T) {
	// 触发了却没提取到内容属于诊断信息，不该当成发言播报到群里。
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1"}

	reply, err := runtime.replyWithResolverOnly(context.Background(), event, "https://example.com/nothing")
	if err != nil {
		t.Fatalf("replyWithResolverOnly() error = %v", err)
	}
	if reply != "" {
		t.Fatalf("reply = %q, want silence", reply)
	}
	for _, msg := range channel.sentSnapshot() {
		if strings.Contains(msg.Text, "插件") {
			t.Fatalf("plugin internals were announced to the group: %#v", msg)
		}
	}
}

// 跟评提示词以前清一色是「不要……」，模型被禁完之后无话可说，就抓着分支名、编号
// 这类附带细节凑一句，或者干脆断言效果（「这下就不用担心了」）。这两条都既空洞又
// 越界。提示词必须把 SKIP 摆成默认，并且点名这两种凑话方式。
func TestFollowUpPromptsDefaultToSilenceAndNameTheFillerModes(t *testing.T) {
	channel := &recordingChannel{}
	provider := &sequenceLLMProvider{replies: []string{"SKIP"}}
	runtime := NewRuntime(BotConfig{BotAccount: "42"}, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return provider, nil
	})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1"}
	if err := runtime.sendDirectPluginResponse(context.Background(), event, "链接解析结果", nil, nil); err != nil {
		t.Fatal(err)
	}
	runtime.maybeSendPluginFollowUp(context.Background(), event, PluginResponse{FollowUp: true})
	waitForCondition(t, time.Second, func() bool { return len(provider.requestsSnapshot()) > 0 })

	prompt := ""
	for _, message := range provider.requestsSnapshot()[0].Messages {
		if strings.Contains(message.Content, "刚刚把上面最后那条内容发到了这个会话里") {
			prompt = message.Content
		}
	}
	if prompt == "" {
		t.Fatal("follow-up prompt not found")
	}
	for _, want := range []string{"默认回 SKIP", "附带细节凑话", "不要断言效果"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("follow-up prompt missing %q: %s", want, prompt)
		}
	}
	// SKIP 时不该多发一条。
	if sent := channel.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("SKIP should stay silent, sent = %#v", sent)
	}
}
