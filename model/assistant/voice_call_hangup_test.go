// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/callbridge"
	"github.com/SuInk/diana/model/voicecall"
)

// hang_up 是终结工具：告别那句成为这一轮的回复，同时让通话念完后挂断。
func TestVoiceCallHangupToolFinishesTurnAndRequestsHangup(t *testing.T) {
	call := &VoiceCall{}
	tool := &voiceCallHangupTool{call: call}
	output, err := tool.Run(context.Background(), map[string]any{"text": " 好，那先挂啦，拜拜。 "})
	if err != nil {
		t.Fatal(err)
	}
	text, done := tool.TerminalResult(output)
	if !done || text != "好，那先挂啦，拜拜。" {
		t.Fatalf("TerminalResult = %q, %v", text, done)
	}
	if !call.TakeHangup() {
		t.Fatal("调用 hang_up 后应请求挂断")
	}
	if call.TakeHangup() {
		t.Fatal("挂断请求只取一次")
	}
}

// 通话轮次里 hang_up 是常驻工具，一步到位；平时没有这个工具也不进常驻名单。
func TestAgentCoreToolsIncludeHangupOnlyInCalls(t *testing.T) {
	runtime, _, _ := newVoiceCallTestRuntime(t, "")
	registry := agent.NewToolRegistry(&voiceCallHangupTool{call: &VoiceCall{}})
	call := MessageEvent{ProfileID: "bot", voiceCall: true}
	if core := runtime.agentCoreTools(call, registry); !slices.Contains(core, voiceCallHangupToolName) {
		t.Fatalf("通话里常驻工具应有 hang_up：%v", core)
	}
	if core := runtime.agentCoreTools(MessageEvent{ProfileID: "bot"}, registry); slices.Contains(core, voiceCallHangupToolName) {
		t.Fatalf("不是通话不该常驻 hang_up：%v", core)
	}
	if slices.Contains(replyAgentCoreTools, voiceCallHangupToolName) {
		t.Fatal("不能改到全局常驻名单上")
	}
}

// QQ 那头告别那句还在 agent 的播放缓冲里：等估算的播放时长过去再让 agent 点挂断。
func TestBridgeCallTransportWaitsForFarewellBeforeHangup(t *testing.T) {
	link, got := fakeBridgeCallAgent(t)
	out := &bridgeCallTransport{ctx: context.Background(), link: link, call: "c1", playUntil: time.Now().Add(300 * time.Millisecond)}
	start := time.Now()
	if err := out.WriteJSON(voicecall.ServerMessage{Type: voicecall.ServerHangup}); err != nil {
		t.Fatal(err)
	}
	expectBridgeCallMessage(t, got, callbridge.Message{Type: callbridge.TypeHangup, Call: "c1"})
	if waited := time.Since(start); waited < 250*time.Millisecond {
		t.Fatalf("告别还没播完就挂了：只等了 %s", waited)
	}
}
