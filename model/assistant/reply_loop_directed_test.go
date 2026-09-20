package assistant

import "testing"

func directedLoopRuntime(t *testing.T) *Runtime {
	t.Helper()
	return NewRuntime(BotConfig{
		BotAccount:    "90001",
		OwnerID:       "owner",
		GroupTriggers: []string{"嘉然", "然然", "Diana"},
	}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
}

func directedLoopEvent(text string) MessageEvent {
	return MessageEvent{
		Kind:       EventKindGroup,
		GroupID:    "20003",
		UserID:     "30004",
		MessageID:  "yuki-1",
		SenderRole: "member",
		RawMessage: text,
		Segments:   []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
}

// 2026-09-20 深夜 20003 群里另一台机器人和 Diana 互道晚安刷了十几轮，每条的
// 接话评分都是「在跟机器人说话：是」，但正文里既没有 @、引用也没有名字，于是
// bot_reply_loop_classification 从 23:56 起再没跑过一次，回复欲望衰减的密度计数
// 一直是空的，没有任何一层能刹车。
func TestBotReplyLoopCandidateSeesSemanticallyDirectedMessage(t *testing.T) {
	runtime := directedLoopRuntime(t)
	event := directedLoopEvent("晚安宝宝喵")

	if _, ok := runtime.botReplyLoopCandidate(event, event.RawMessage); ok {
		t.Fatal("没有任何触发点的群消息不该进空转判断")
	}

	event.routingDirected = true
	candidate, ok := runtime.botReplyLoopCandidate(event, event.RawMessage)
	if !ok {
		t.Fatal("评分模型判定在跟机器人说话，这条必须进空转判断")
	}
	if candidate.TriggerKind != "directed" {
		t.Fatalf("trigger kind = %q, want directed", candidate.TriggerKind)
	}
}

// 语义触发不越过既有的前置条件：停用的群、空正文都照旧不判。
func TestBotReplyLoopCandidateDirectedKeepsExistingGuards(t *testing.T) {
	runtime := directedLoopRuntime(t)
	empty := directedLoopEvent("")
	empty.routingDirected = true
	if _, ok := runtime.botReplyLoopCandidate(empty, ""); ok {
		t.Fatal("空正文不该进空转判断")
	}

	self := directedLoopEvent("晚安宝宝喵")
	self.routingDirected = true
	self.UserID = "90001"
	if _, ok := runtime.botReplyLoopCandidate(self, self.RawMessage); ok {
		t.Fatal("机器人自己的消息不该进空转判断")
	}
}

// 结构触发仍然优先：@、引用和名字各自的 trigger_kind 不能被语义那一支吃掉，
// 日志里要分得清这次是模型判出来的还是消息本身带的。
func TestBotReplyLoopCandidateStructuralTriggersKeepTheirKind(t *testing.T) {
	runtime := directedLoopRuntime(t)
	alias := directedLoopEvent("然然 晚安")
	alias.routingDirected = true
	candidate, ok := runtime.botReplyLoopCandidate(alias, alias.RawMessage)
	if !ok {
		t.Fatal("叫了名字的消息本来就是候选")
	}
	if candidate.TriggerKind != "alias" {
		t.Fatalf("trigger kind = %q, want alias", candidate.TriggerKind)
	}
}
