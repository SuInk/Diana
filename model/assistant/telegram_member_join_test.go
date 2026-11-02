// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func newTelegramWelcomeRuntime(t *testing.T) (*fakeTelegramAPI, *TelegramChannel) {
	t.Helper()
	api := newFakeTelegramAPI(t, map[string]any{"sendMessage": map[string]any{"message_id": 501}})
	channel := api.channel()
	runtime := NewRuntime(BotConfig{Platform: PlatformTelegram, BotAccount: "8888", WelcomeEnabled: true, WelcomeMessage: "欢迎 {nickname}"}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	channel.handler = runtime.HandleEvent
	return api, channel
}

func telegramSentTexts(t *testing.T, api *fakeTelegramAPI) []string {
	t.Helper()
	var texts []string
	for _, call := range api.callsOf("sendMessage") {
		text, _ := call.Params["text"].(string)
		raw, _ := json.Marshal(call.Params["entities"])
		if !strings.Contains(string(raw), "text_mention") {
			t.Fatalf("欢迎应 @ 新人（新人可能没有用户名，只能用 text_mention）：%#v", call.Params)
		}
		texts = append(texts, text)
	}
	return texts
}

// 一次拉多个人进群：每个真人各欢迎一次，被拉进来的机器人不欢迎；
// 管理员机器人随后收到的同一次入群的 chat_member 更新不再欢迎第二遍。
func TestTelegramWelcomesEveryNewHumanMemberOnce(t *testing.T) {
	api, channel := newTelegramWelcomeRuntime(t)
	chat := &telegramChat{ID: -100777, Type: "supergroup", Title: "测试群"}
	channel.dispatch(context.Background(), telegramUpdate{UpdateID: 1, Message: &telegramMessage{
		MessageID: 9, Date: 1_800_000_000, Chat: chat,
		From: &telegramUser{ID: 70001, FirstName: "邀请人"},
		NewChatMembers: []telegramUser{
			{ID: 42421, FirstName: "Alice"},
			{ID: 42422, FirstName: "OtherBot", IsBot: true},
			{ID: 42423, FirstName: "Bob"},
		},
	}})
	channel.dispatch(context.Background(), telegramUpdate{UpdateID: 2, ChatMember: &telegramMemberUpdate{
		Chat: *chat, From: telegramUser{ID: 70001}, Date: 1_800_000_000,
		Old: telegramChatMember{User: telegramUser{ID: 42421, FirstName: "Alice"}, Status: "left"},
		New: telegramChatMember{User: telegramUser{ID: 42421, FirstName: "Alice"}, Status: "member"},
	}})

	texts := telegramSentTexts(t, api)
	if len(texts) != 2 || !strings.Contains(texts[0], "Alice") || !strings.Contains(texts[1], "Bob") {
		t.Fatalf("应各欢迎 Alice、Bob 一次，实际 %q", texts)
	}
}

// 大群或开了「隐藏入群消息」时没有服务消息，只能靠 chat_member 更新发现新人。
func TestTelegramWelcomesHiddenJoinFromChatMemberUpdate(t *testing.T) {
	api, channel := newTelegramWelcomeRuntime(t)
	chat := telegramChat{ID: -100777, Type: "supergroup"}
	channel.dispatch(context.Background(), telegramUpdate{UpdateID: 3, ChatMember: &telegramMemberUpdate{
		Chat: chat, From: telegramUser{ID: 42424}, Date: 1_800_000_000,
		Old: telegramChatMember{User: telegramUser{ID: 42424, FirstName: "Carol"}, Status: "left"},
		New: telegramChatMember{User: telegramUser{ID: 42424, FirstName: "Carol"}, Status: "member"},
	}})
	// 已在群里的人权限变化不是入群。
	channel.dispatch(context.Background(), telegramUpdate{UpdateID: 4, ChatMember: &telegramMemberUpdate{
		Chat: chat, From: telegramUser{ID: 70001}, Date: 1_800_000_001,
		Old: telegramChatMember{User: telegramUser{ID: 42425, FirstName: "Dave"}, Status: "member"},
		New: telegramChatMember{User: telegramUser{ID: 42425, FirstName: "Dave"}, Status: "restricted", IsMember: true},
	}})

	texts := telegramSentTexts(t, api)
	if len(texts) != 1 || !strings.Contains(texts[0], "Carol") {
		t.Fatalf("应只欢迎 Carol 一次，实际 %q", texts)
	}
}

func TestTelegramChatMemberJoinEventMarksInvite(t *testing.T) {
	event, ok := telegramChatMemberJoinEvent(&telegramMemberUpdate{
		Chat: telegramChat{ID: -100777, Type: "supergroup"}, From: telegramUser{ID: 70001},
		Old: telegramChatMember{User: telegramUser{ID: 42426}, Status: "kicked"},
		New: telegramChatMember{User: telegramUser{ID: 42426}, Status: "restricted", IsMember: true},
	}, "8888")
	if !ok || event.Platform != PlatformTelegram || event.SubType != "group_increase" || event.UserID != "42426" || event.OperatorID != "70001" {
		t.Fatalf("event = %#v", event)
	}
	if data := noticeSegmentData(event); data["sub_type"] != "invite" || data["operator_id"] != "70001" {
		t.Fatalf("notice data = %#v", data)
	}
}
