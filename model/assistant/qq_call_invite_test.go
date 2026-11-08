// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
	"github.com/SuInk/diana/model/qqcall"
)

// 生产里实际 OCR 到的群通话来电（群名被截断，后面还混进了一行乱码）。
const testGroupInviteOCR = "Winter\n邀请您加入“Diana种子用户 (sls... “的语音通话\nUs\n"

var testCallGroups = []qqCallGroup{
	{ID: "20001", Name: "Diana交流群"},
	{ID: "20002", Name: "Diana种子用户 (sls测试)"},
}

func listTestCallGroups(context.Context) ([]qqCallGroup, error) { return testCallGroups, nil }

func TestQQCallInviteRulesTellPrivateFromGroup(t *testing.T) {
	invite := parseQQCallInviteRules(testGroupInviteOCR)
	if !invite.incoming || !invite.group || invite.caller != "Winter" {
		t.Fatalf("group invite = %+v", invite)
	}
	if id, _ := matchQQCallGroup(invite.groupName, testCallGroups); id != "20002" {
		t.Fatalf("group name %q matched %q", invite.groupName, id)
	}
	private := parseQQCallInviteRules("主人\n邀请你语音通话\n")
	if !private.incoming || private.group || private.caller != "主人" {
		t.Fatalf("private invite = %+v", private)
	}
	if outgoing := parseQQCallInviteRules("主人\n正在等待对方接听\n"); outgoing.incoming {
		t.Fatalf("outgoing call taken as incoming: %+v", outgoing)
	}
	// 截得太短、几个群都像时不猜。
	if id, _ := matchQQCallGroup("Diana...", testCallGroups); id != "" {
		t.Fatalf("ambiguous prefix matched %q", id)
	}
}

func TestClassifyQQCallInviteAsksModelAndOnlyTrustsListedGroups(t *testing.T) {
	for _, tc := range []struct {
		reply, want string
	}{
		{`{"incoming":true,"kind":"group","caller":"Winter","group_id":"20002"}`, "20002"},
		// 模型说的群号不在群列表里：当没认出来。
		{`{"incoming":true,"kind":"group","caller":"Winter","group_id":"99999"}`, ""},
	} {
		provider := &capturingLLMProvider{reply: tc.reply}
		runtime := NewRuntime(BotConfig{ID: "bot", OwnerID: "10001"}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
			return provider, nil
		})
		invite := runtime.classifyQQCallInvite(context.Background(), "bot", qqcall.Message{Name: "Winter", Text: testGroupInviteOCR}, listTestCallGroups)
		if invite.how != "llm" || !invite.incoming || !invite.group || invite.groupID != tc.want {
			t.Fatalf("reply %s: invite = %+v", tc.reply, invite)
		}
		req := provider.requestSnapshot()
		if len(req.Messages) < 2 || !strings.Contains(req.Messages[1].Content, "Diana种子用户 (sls测试)") || !strings.Contains(req.Messages[1].Content, "邀请您加入") {
			t.Fatalf("model did not get the OCR text and group list: %#v", req.Messages)
		}
	}
}

func TestClassifyQQCallInviteFallsBackWithoutModel(t *testing.T) {
	runtime := NewRuntime(BotConfig{ID: "bot", OwnerID: "10001"}, &recordingChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		return nil, errors.New("没配模型")
	})
	invite := runtime.classifyQQCallInvite(context.Background(), "bot", qqcall.Message{Text: testGroupInviteOCR}, listTestCallGroups)
	if invite.how != "rules" || invite.groupID != "20002" {
		t.Fatalf("invite = %+v", invite)
	}
	// 旧版 agent 不带原文，只报私聊来电。
	legacy := runtime.classifyQQCallInvite(context.Background(), "bot", qqcall.Message{Name: "主人"}, nil)
	if !legacy.incoming || legacy.group || legacy.how != "legacy" {
		t.Fatalf("legacy = %+v", legacy)
	}
}

func TestQQGroupVoiceCallRepliesAsThatGroup(t *testing.T) {
	runtime, channel, provider := newVoiceCallTestRuntime(t, "大家好呀。")
	_, base, err := runtime.ownerChatEvent("bot")
	if err != nil {
		t.Fatal(err)
	}
	base.Kind, base.MessageType, base.GroupID, base.GroupName = EventKindGroup, string(EventKindGroup), "20002", "Diana种子用户"
	base.UserID, base.SenderName, base.voiceCall = qqGroupCallSpeakerID, "群通话", true
	call := &VoiceCall{runtime: runtime, callID: "g1", base: base}
	var mu sync.Mutex
	var emitted []string
	if err := call.Reply(context.Background(), "嘉然你也来啦", func(text string) {
		mu.Lock()
		emitted = append(emitted, text)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(emitted, ""), "大家好呀") {
		t.Fatalf("emitted = %q", emitted)
	}
	channel.mu.Lock()
	sent := len(channel.sent)
	channel.mu.Unlock()
	if sent != 0 {
		t.Fatal("群通话里的回复不该发到群里")
	}
	var system strings.Builder
	for _, message := range provider.requestSnapshot().Messages {
		if message.Role == llm.RoleSystem {
			system.WriteString(message.Content)
		}
	}
	if !strings.Contains(system.String(), "QQ 群语音通话") || strings.Contains(system.String(), "实时语音通话") {
		t.Fatal("群通话应注入群通话提示，不是私聊通话提示")
	}
	call.Finish()
	for _, event := range runtime.history[sessionKey(runtime.bindInboundEventIdentity(MessageEvent{ProfileID: "bot", Kind: EventKindPrivate, UserID: "10001"}))] {
		if isCallHistoryEvent(event) {
			t.Fatal("群通话不该往主人私聊里记通话记录")
		}
	}
}
