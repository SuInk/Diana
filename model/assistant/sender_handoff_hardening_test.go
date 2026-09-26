// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 连发交接的边界：还没预处理完的消息、主人命令、已经在外部留下痕迹的一轮、接手链、
// 落定和收尾的先后，都不能让一条消息谁都不回、或者被回两遍。

func voiceEvent(messageID, userID string, at int64, transcript string) MessageEvent {
	data := map[string]string{"file": messageID + ".amr"}
	if transcript != "" {
		data[voiceSTTTranscriptKey] = transcript
	}
	return MessageEvent{
		Kind: EventKindGroup, GroupID: "123456", UserID: userID, MessageID: messageID, Time: at,
		RawMessage: "[CQ:record,file=" + messageID + ".amr]",
		Segments:   []MessageSegment{{Type: "record", Data: data}},
	}
}

// 语音还在转写（或者转写失败）：历史里只有「[语音]」占位，后一条不能接走它，否则真正
// 的内容永远没人回答。转写完的语音照常接。
func TestSenderBurstNeverAbsorbsUnpreprocessedVoice(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ready      bool
		transcript string
		wantTaken  bool
	}{
		{name: "still transcribing", ready: false, transcript: ""},
		{name: "transcription failed", ready: true, transcript: ""},
		{name: "transcribed", ready: true, transcript: "明天下午三点开会别忘了", wantTaken: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &burstReplyProvider{}
			runtime, _ := burstTestRuntime(provider)
			voice := voiceEvent("20211", "10001", 1_800_000_000, tc.transcript)
			second := directedGroupMessage("20212", "10001", "你听一下")
			second.Time = 1_800_000_003
			runtime.noteSenderTurnArrival(voice)
			runtime.remember(voice)
			if tc.ready {
				runtime.noteSenderTurnReady(voice)
			}
			arriveTogether(runtime, second)

			if outcome, err := runtime.replyAndRecord(context.Background(), second, "你听一下", "replied"); err != nil || outcome != "replied" {
				t.Fatalf("second outcome=%q err=%v", outcome, err)
			}
			_, taken := runtime.senderTurnSupersededBy(voice)
			if taken != tc.wantTaken {
				t.Fatalf("voice taken over=%v, want %v", taken, tc.wantTaken)
			}
			if !tc.wantTaken {
				if outcome, err := runtime.replyAndRecord(context.Background(), voice, "", "replied"); err != nil || outcome != "replied" {
					t.Fatalf("voice must be answered by its own turn, outcome=%q err=%v", outcome, err)
				}
			}
		})
	}
}

// 主人命令和编码任务确认码必须在自己那一轮生效：后一条不能把它们接走。
func TestSenderBurstNeverAbsorbsOwnerCommands(t *testing.T) {
	for _, command := range []string{"提醒 列表", "清空上下文", "订阅 取消 abc"} {
		t.Run(command, func(t *testing.T) {
			provider := &burstReplyProvider{}
			disabled := false
			runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "10001", BotReplyLoopDetectionEnabled: &disabled},
				&recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
			first := privateEvent("10001", "20221", command)
			second := privateEvent("10001", "20222", "今天天气怎么样")
			first.Time, second.Time = 1_800_000_000, 1_800_000_002
			arriveTogether(runtime, first, second)
			if !runtime.wouldHandleOwnerCommand(first, command) {
				t.Fatalf("%q should be recognised as an owner command", command)
			}
			if outcome, err := runtime.replyAndRecord(context.Background(), second, "今天天气怎么样", "replied"); err != nil || outcome != "replied" {
				t.Fatalf("second outcome=%q err=%v", outcome, err)
			}
			if _, taken := runtime.senderTurnSupersededBy(first); taken {
				t.Fatal("an owner command was taken over by a later message")
			}
			if provider.sawPrompt(command) && provider.sawPrompt("你还没有回复") {
				t.Fatal("the later turn's prompt should not carry the owner command over")
			}
		})
	}
	// 别人发同样的字不是主人命令，照常能接。
	runtime := NewRuntime(BotConfig{BotAccount: "42", OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	if runtime.wouldHandleOwnerCommand(privateEvent("10002", "20223", "提醒 列表"), "提醒 列表") {
		t.Fatal("a non-owner message is not an owner command")
	}
}

// sideEffectProvider 在图那一轮的生成里标记「已经写到外部系统」，文字那一轮按测试的
// 安排卡住或出错。
type sideEffectProvider struct {
	photoEntered  chan struct{}
	photoGate     chan struct{}
	questionReady chan struct{}
	questionGate  chan error
	photoOnce     sync.Once
	questionOnce  sync.Once
}

func (p *sideEffectProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	body := requestText(req)
	switch {
	case strings.Contains(body, "请为这张图片生成可复用的客观中文描述"):
		return &llm.GenerateResponse{Model: "test", Text: "一只橘猫"}, nil
	case strings.Contains(body, "send_confidence"):
		return &llm.GenerateResponse{Model: "test", Text: `{"send_confidence":0.99,"reason":"ok"}`}, nil
	case strings.Contains(body, "【同一发言者稍早发的图"):
		p.questionOnce.Do(func() { close(p.questionReady) })
		if err := <-p.questionGate; err != nil {
			return nil, err
		}
		return &llm.GenerateResponse{Model: "test", Text: "这是一只橘猫"}, nil
	case !strings.Contains(body, "橘猫叫什么名字") && !strings.Contains(body, "model down"):
		p.photoOnce.Do(func() { close(p.photoEntered) })
		<-p.photoGate
		// 图那一轮调了一个会写外部系统的工具。
		markExternalSideEffect(ctx)
		return &llm.GenerateResponse{Model: "test", Text: "已经帮你存好这张图"}, nil
	}
	return &llm.GenerateResponse{Model: "test", Text: "好的"}, nil
}

// 被依赖图接走的图那一轮，后来写了外部系统、绕过闸门把回复发了出去：它不是交出去的。
// 文字那一轮没回出去时，图那一轮不能被重新排队再来一遍（工具会再调一次）。
func TestSideEffectTurnIsNeverHandedOff(t *testing.T) {
	provider := &sideEffectProvider{photoEntered: make(chan struct{}), photoGate: make(chan struct{}), questionReady: make(chan struct{}), questionGate: make(chan error, 1)}
	runtime, question := dependencyTestRuntime(t, provider, false, nil)
	question.RawMessage = "橘猫叫什么名字"
	question.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": question.RawMessage}}}
	photo := historyEventByID(t, runtime, question, "photo-1")
	runtime.noteSenderTurnArrival(photo)
	runtime.noteSenderTurnArrival(question)

	photoDone := make(chan string, 1)
	go func() {
		outcome, _ := runtime.replyAndRecord(context.Background(), photo, "", "replied")
		photoDone <- outcome
	}()
	select {
	case <-provider.photoEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("image turn never started generating")
	}
	runtime.noteSenderTurnMergeChecked(question, "photo-1", true)
	questionDone := make(chan string, 1)
	go func() {
		outcome, _ := runtime.replyAndRecord(context.Background(), question, question.RawMessage, "replied")
		questionDone <- outcome
	}()
	select {
	case <-provider.questionReady:
	case <-time.After(5 * time.Second):
		t.Fatal("question turn never started generating")
	}
	if _, taken := runtime.senderTurnSupersededBy(photo); !taken {
		t.Fatal("setup: the text turn should have taken the image over")
	}
	close(provider.photoGate)
	select {
	case outcome := <-photoDone:
		if outcome == inboundOutcomeHandedOffPending || outcome == "superseded_follow_up" {
			t.Fatalf("a turn that wrote to an external system was recorded as handed off: %q", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("image turn never finished")
	}
	provider.questionGate <- errors.New("model down")
	<-questionDone
	// 被重新排队的话，图那一轮会再生成、再发一遍同样的回复。
	time.Sleep(300 * time.Millisecond)
	if _, taken := runtime.senderTurnSupersededBy(photo); taken {
		t.Fatal("the handoff of a turn with an external side effect must be dropped")
	}
	photoReplies := 0
	for _, msg := range runtime.channel.(*recordingChannel).sentSnapshot() {
		if strings.Contains(msg.Text, "已经帮你存好这张图") {
			photoReplies++
		}
	}
	if photoReplies != 1 {
		t.Fatalf("image turn reply sent %d times: %s", photoReplies, describeSentMessages(runtime.channel.(*recordingChannel).sentSnapshot()))
	}
}

// 接手链 A←B←C：B（一张图）已经接了 A，C 不能再通过依赖图接走 B——C 没回出去时
// B 会被放回来，而 A 的交接已经跟着 B 走了，结果 A 会被再答一遍。
func TestDependencyPathSkipsTurnsThatAbsorbedOthers(t *testing.T) {
	runtime, _ := burstTestRuntime(&burstReplyProvider{})
	a := directedGroupMessage("20231", "10001", "这张图是哪里拍的")
	b := photoEvent("20232", "10001", 1_800_000_002)
	b.GroupID = a.GroupID
	c := directedGroupMessage("20233", "10001", "顺便说说天气")
	a.Time, c.Time = 1_800_000_000, 1_800_000_004
	arriveTogether(runtime, a, b, c)
	runtime.enterSenderTurnReply(b, true)
	if carry := runtime.claimCarryOver(context.Background(), b, runtime.contextHistory(b)); len(carry) != 1 {
		t.Fatalf("setup: b should take a over, carry=%#v", carry)
	}
	runtime.enterSenderTurnReply(c, true)
	runtime.supersedeDependencyImageTurns(context.Background(), c, []senderDependencyImage{{Source: b}})
	if _, taken := runtime.senderTurnSupersededBy(b); taken {
		t.Fatal("a turn that already absorbed others must not be taken over")
	}
}

// 落定抢在交出去那一轮收尾之前：收尾不能把 final 改回 pending。
func TestHandoffFinalizeBeforeCompleteStaysFinal(t *testing.T) {
	store := newHandoffInboundStore()
	event := directedGroupMessage("20241", "10001", "问一下")
	id, _, _ := store.EnqueueInboundEvent(context.Background(), sessionKey(event), event)
	if _, ok, _ := store.ClaimNextInboundEvent(context.Background(), "worker", time.Now().Add(time.Minute)); !ok {
		t.Fatal("claim failed")
	}
	ref := InboundHandoffRef{ID: id, Event: event}
	_ = store.MarkInboundHandoff(context.Background(), ref, "absorber")
	_ = store.FinalizeInboundHandoff(context.Background(), ref, "absorber")
	if err := store.CompleteInboundHandoff(context.Background(), id, "worker", "absorber", false); err != nil {
		t.Fatal(err)
	}
	if state := store.handoffStateOf("20241"); state != "final" {
		t.Fatalf("handoff state=%q, want final", state)
	}
	if outcome, _ := store.outcomeAndAttempts(id); outcome != "superseded_follow_up" {
		t.Fatalf("outcome=%q", outcome)
	}
}

// 交接落库走入站事件主键；错误提示、诊断这类发送不算模型回复。
func TestHandoffUsesInboundIDAndIgnoresNonModelSends(t *testing.T) {
	provider := &gatedBurstProvider{entered: make(chan struct{}), release: make(chan error, 1)}
	runtime, _ := burstTestRuntime(provider)
	store := newHandoffInboundStore()
	runtime.SetInboundEventStore(store)
	first := directedGroupMessage("20251", "10001", "帮我看看这个报错")
	second := directedGroupMessage("20252", "10001", "就是登录那个")
	first.Time, second.Time = 1_800_000_000, 1_800_000_003
	firstID, _, _ := store.EnqueueInboundEvent(context.Background(), sessionKey(first), first)
	arriveTogether(runtime, first, second)
	runtime.noteSenderTurnInbound(first, firstID)
	provider.release <- nil
	if outcome, _ := runtime.replyAndRecord(withOutboundTurn(context.Background(), "turn-second"), second, "就是登录那个", "replied"); outcome != "replied" {
		t.Fatalf("second outcome=%q", outcome)
	}
	store.mu.Lock()
	byID := store.byID
	store.mu.Unlock()
	if byID == 0 {
		t.Fatal("handoff persistence should locate the row by its inbound event id")
	}

	third := directedGroupMessage("20253", "10001", "还在吗")
	runtime.noteSenderTurnArrival(third)
	gated := withReplyTriggerGate(context.Background())
	runtime.noteSenderTurnDelivered(withoutCarryOverDelivery(gated), third)
	runtime.replyInterruptMu.Lock()
	delivered := runtime.senderTurnLocked(directReplyMergeKey(third), "20253").delivered
	runtime.replyInterruptMu.Unlock()
	if delivered {
		t.Fatal("an error or diagnostic notice must not count as a delivered model reply")
	}
}
