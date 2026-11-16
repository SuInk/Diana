// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// pendingOpenGatePlugin 卡住 root 这一轮在「已登记回复、还没开放合并」那段（线上是
// 语义指代解析那次模型调用，约 5 秒），不给结果，root 随后照常走对话路径。
type pendingOpenGatePlugin struct {
	rootID           string
	entered, release chan struct{}
}

func (p pendingOpenGatePlugin) Manifest() PluginManifest {
	return PluginManifest{ID: "pending-open-gate", BuiltIn: true, Name: "pending open gate"}
}

func (p pendingOpenGatePlugin) Handle(ctx context.Context, req PluginRequest) (*PluginResponse, error) {
	if req.Event.MessageID != p.rootID {
		return nil, nil
	}
	close(p.entered)
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
}

// pendingOpenReplyProvider 让回复生成等到后到那条走完自己的入口（或超时），这样
// 合并不合并只取决于入口的判断，不取决于谁先生成完。
type pendingOpenReplyProvider struct {
	followDone <-chan struct{}
	mu         sync.Mutex
	replies    int
}

func (p *pendingOpenReplyProvider) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	body := requestText(req)
	if strings.Contains(body, "发送前审核器") {
		return &llm.GenerateResponse{Model: "test", Text: `{"send_confidence":0.99,"account_safe":true,"reason":"ok"}`}, nil
	}
	if !requestOffersSend(req) {
		return &llm.GenerateResponse{Model: "test", Text: `{"relation":"supplement","confidence":0.9,"reason":"test"}`}, nil
	}
	select {
	case <-p.followDone:
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	p.mu.Lock()
	p.replies++
	p.mu.Unlock()
	return asSendReply(req, &llm.GenerateResponse{Model: "test", Text: "小小的 agent 也想贴贴"}), nil
}

// 线上 11-16 的形状：私聊连发两条，间隔一两秒。第一条进了回复入口、登记了还没开放的
// 回复，正在做语义指代解析；第二条这时到达，路由阶段的合并看到「未开放」直接放行，
// 回复入口也接不走已经在回复的第一条，于是两条各回一遍，内容几乎一样。
// 后到那条应当等第一条表态（开放或结束），再按现有规则判一次合并。
func TestFollowUpWaitsForPendingDirectReplyToOpen(t *testing.T) {
	withFastSendTiming(t)
	first := privateEvent("10001", "344", "这个猫猫")
	second := privateEvent("10001", "345", "还有这个")
	second.Time = first.Time + 1
	gate := pendingOpenGatePlugin{rootID: first.MessageID, entered: make(chan struct{}), release: make(chan struct{})}
	followDone := make(chan struct{})
	provider := &pendingOpenReplyProvider{followDone: followDone}
	disabled := false
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{BotAccount: "42", BotReplyLoopDetectionEnabled: &disabled}, channel, NewPluginManager(gate), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	runtime.noteSenderTurnArrival(first)
	runtime.noteSenderTurnArrival(second)

	firstDone := make(chan string, 1)
	go func() {
		outcome, _ := runtime.replyAndRecord(context.Background(), first, "这个猫猫", "replied")
		firstDone <- outcome
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn never reached the pre-open window")
	}

	secondOutcome := make(chan string, 1)
	go func() {
		defer close(followDone)
		outcome, _ := runtime.replyAndRecord(context.Background(), second, "还有这个", "replied")
		secondOutcome <- outcome
	}()
	// 第一条还没表态：后到那条不能抢先自己开一轮。
	select {
	case outcome := <-secondOutcome:
		t.Fatalf("follow-up finished before the earlier reply opened: %q", outcome)
	case <-time.After(150 * time.Millisecond):
	}
	close(gate.release)

	select {
	case outcome := <-secondOutcome:
		if outcome != "merged_into_reply" {
			t.Fatalf("second outcome = %q, want merged_into_reply", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follow-up stalled")
	}
	select {
	case outcome := <-firstDone:
		if outcome != "replied" {
			t.Fatalf("first outcome = %q", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first reply stalled")
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 {
		t.Fatalf("burst of two should produce exactly one reply: %#v", sent)
	}
}
