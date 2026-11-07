// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

// 机器人自己挂电话。主人说「先挂了」「拜拜」时，机器人说完告别就该挂；以前只能等
// 主人那头挂，机器人说了「拜拜」电话还通着。
//
// hang_up 只在通话轮次里注册，而且是常驻工具（见 agentCoreTools）：按需目录里的工具
// 要先 tools_execute 再收尾，多一轮模型调用，电话里就是多几秒冷场。它和 tts 一样是
// 终结工具：调用就等于给出这一轮的最终回复，告别那句交给通话念，念完由通话引擎下发
// hangup 并关闭（voicecall.HangupBackend）。
const voiceCallHangupToolName = "hang_up"

type voiceCallContextKey struct{}

func withVoiceCall(ctx context.Context, call *VoiceCall) context.Context {
	return context.WithValue(ctx, voiceCallContextKey{}, call)
}

func voiceCallFromContext(ctx context.Context) *VoiceCall {
	call, _ := ctx.Value(voiceCallContextKey{}).(*VoiceCall)
	return call
}

type voiceCallHangupTool struct {
	call *VoiceCall
}

type voiceCallHangupResult struct {
	OK     bool   `json:"ok"`
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
}

func (t *voiceCallHangupTool) Name() string { return voiceCallHangupToolName }

func (t *voiceCallHangupTool) Description() string {
	return "挂断这通语音电话。对方道别、说要挂了、让你挂，或者聊完了该结束时用；text 是挂断前最后说的那句告别，念完就挂。" +
		"只是聊天停顿、对方没说要走时不用。调用后这一轮就结束了，不要再写别的回复。"
}

func (t *voiceCallHangupTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"text": toolStringParam("挂断前说的最后一句，口语，不用 Markdown；可以空着直接挂"),
	})
}

func (t *voiceCallHangupTool) Run(_ context.Context, input map[string]any) (string, error) {
	text := strings.TrimSpace(configToolString(input, "text"))
	if t != nil && t.call != nil {
		t.call.requestHangup()
	}
	body, err := json.Marshal(voiceCallHangupResult{OK: true, Action: "hang_up", Text: text})
	return string(body), err
}

// TerminalResult 让告别那句成为这一轮的回复，交给通话念出来。
func (t *voiceCallHangupTool) TerminalResult(output string) (string, bool) {
	var result voiceCallHangupResult
	if err := json.Unmarshal([]byte(output), &result); err != nil || !result.OK {
		return "", false
	}
	return result.Text, true
}

var _ agent.TerminalResultTool = (*voiceCallHangupTool)(nil)
