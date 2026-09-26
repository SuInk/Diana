// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// roleTestChannel 按账号给出群身份：规则防御既要查机器人自己，也要查被罚的人。
type roleTestChannel struct {
	*recordingChannel
	roles map[string]string
}

func newRoleTestChannel(roles map[string]string) *roleTestChannel {
	return &roleTestChannel{recordingChannel: &recordingChannel{apiResponses: map[string]map[string]any{}}, roles: roles}
}

func (c *roleTestChannel) GroupMember(_ context.Context, groupID, userID string) (OneBotGroupMemberInfo, error) {
	role := c.roles[userID]
	if role == "" {
		role = "member"
	}
	return OneBotGroupMemberInfo{GroupID: groupID, UserID: userID, Role: role, Nickname: "昵称" + userID, MembershipVerified: true}, nil
}

func (c *roleTestChannel) GroupMembers(_ context.Context, groupID string) (GroupMemberDirectory, error) {
	return GroupMemberDirectory{Source: "test"}, nil
}

func governanceRuntime(t *testing.T, gov *GroupGovernance, roles map[string]string) (*Runtime, *roleTestChannel) {
	t.Helper()
	channel := newRoleTestChannel(roles)
	runtime := NewRuntime(BotConfig{OwnerID: "owner", BotAccount: "10000", Platform: PlatformOneBotV11}, channel, NewDefaultPluginManager(), nil, nil, nil, nil)
	runtime.SetAppLogWriter(&captureAppLogs{})
	runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"123": {GroupID: "123", Governance: gov}}})
	return runtime, channel
}

func TestGovernanceTrackerFloodRepeatAndKeyword(t *testing.T) {
	gov := GroupGovernance{AntiSpamEnabled: true, SpamWindowSeconds: 10, SpamMaxMessages: 3, SpamMaxRepeats: 3, KeywordFilterEnabled: true, KeywordRules: []string{"加微信", `re:\d{5,}\s*群`}}.Normalized()
	var tracker governanceTracker
	now := time.Unix(1_700_000_000, 0)

	// 窗口内第 4 条（超过 3 条）判刷屏，要撤的是整个窗口，新的在前。
	for i := 1; i <= 3; i++ {
		if _, violated := tracker.observe("u1", now.Add(time.Duration(i)*time.Second), fmt.Sprintf("消息%d", i), fmt.Sprintf("m%d", i), gov); violated {
			t.Fatalf("message %d flagged too early", i)
		}
	}
	verdict, violated := tracker.observe("u1", now.Add(4*time.Second), "消息4", "m4", gov)
	if !violated || verdict.reason != governanceReasonFlood || verdict.strike != 1 || strings.Join(verdict.recallIDs, ",") != "m4,m3,m2,m1" {
		t.Fatalf("flood verdict = %#v", verdict)
	}
	// 同一波刷屏只罚一次：窗口已清空，下一条不会连着升档。
	if _, violated := tracker.observe("u1", now.Add(5*time.Second), "消息5", "m5", gov); violated {
		t.Fatal("same burst punished twice")
	}

	// 同一内容重复 3 次判复读，不看大小写和空白。
	tracker.observe("u2", now, "Hello  World", "r1", gov)
	tracker.observe("u2", now.Add(time.Second), "hello world", "r2", gov)
	if verdict, violated := tracker.observe("u2", now.Add(2*time.Second), "HELLO WORLD", "r3", gov); !violated || verdict.reason != governanceReasonRepeat {
		t.Fatalf("repeat verdict = %#v, %v", verdict, violated)
	}

	// 窗口外的旧消息不算数。
	tracker.observe("u3", now, "a", "o1", gov)
	tracker.observe("u3", now.Add(time.Second), "b", "o2", gov)
	tracker.observe("u3", now.Add(2*time.Second), "c", "o3", gov)
	if _, violated := tracker.observe("u3", now.Add(30*time.Second), "d", "o4", gov); violated {
		t.Fatal("messages outside the window were counted")
	}

	// 违规词：子串不区分大小写，re: 开头按正则；只撤当前这条。
	if verdict, violated := tracker.observe("u4", now, "有意者加微信", "k1", gov); !violated || verdict.reason != governanceReasonKeyword || verdict.rule != "加微信" || strings.Join(verdict.recallIDs, ",") != "k1" {
		t.Fatalf("keyword verdict = %#v", verdict)
	}
	if verdict, violated := tracker.observe("u5", now, "快来 123456 群", "k2", gov); !violated || verdict.rule != `re:\d{5,}\s*群` {
		t.Fatalf("regex verdict = %#v", verdict)
	}
}

func TestGovernanceStrikesEscalateAndReset(t *testing.T) {
	gov := GroupGovernance{KeywordFilterEnabled: true, KeywordRules: []string{"广告"}, PenaltyLadderSeconds: []int{60, 600}, StrikeResetMinutes: 60}.Normalized()
	var tracker governanceTracker
	now := time.Unix(1_700_000_000, 0)
	var strikes []int
	for i := 0; i < 4; i++ {
		verdict, _ := tracker.observe("u", now.Add(time.Duration(i)*time.Minute), "广告", "", gov)
		strikes = append(strikes, gov.penaltyForStrike(verdict.strike))
	}
	// 第一次只警告，之后按阶梯，超出的按最后一档。
	if fmt.Sprint(strikes) != "[0 60 600 600]" {
		t.Fatalf("penalties = %v", strikes)
	}
	// 距上次违规超过有效期，从头算。
	verdict, _ := tracker.observe("u", now.Add(3*time.Hour), "广告", "", gov)
	if verdict.strike != 1 {
		t.Fatalf("strike after reset = %d", verdict.strike)
	}
}

func TestGovernanceValidateRejectsBadRegex(t *testing.T) {
	if err := (GroupGovernance{KeywordRules: []string{"re:(unclosed"}}).Validate(); err == nil || !strings.Contains(err.Error(), "正则") {
		t.Fatalf("Validate error = %v", err)
	}
	if err := (GroupGovernance{PenaltyLadderSeconds: []int{oneBotMaxMuteSeconds + 1}}).Validate(); err == nil {
		t.Fatal("ladder above platform cap accepted")
	}
	if got := (GroupGovernance{KeywordRules: []string{"re:(unclosed", "ok"}}).Normalized().KeywordRules; len(got) != 1 || got[0] != "ok" {
		t.Fatalf("Normalized kept invalid rule: %v", got)
	}
}

func TestGovernancePenaltyWarnsThenMutes(t *testing.T) {
	gov := GroupGovernance{KeywordFilterEnabled: true, KeywordRules: []string{"广告"}}.Normalized()
	runtime, channel := governanceRuntime(t, &gov, map[string]string{"10000": "admin"})
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, SelfID: "10000", GroupID: "123", UserID: "555", SenderName: "小明", MessageID: "bad-1"}

	runtime.applyGovernancePenalty(context.Background(), event, gov, governanceVerdict{reason: governanceReasonKeyword, strike: 1, recallIDs: []string{"bad-1"}})
	calls := channel.callsSnapshot()
	if got := recordedCallsByAction(calls, "delete_msg"); len(got) != 1 || fmt.Sprint(got[0].params["message_id"]) != "bad-1" {
		t.Fatalf("delete_msg calls = %#v", got)
	}
	if got := recordedCallsByAction(calls, "set_group_ban"); len(got) != 0 {
		t.Fatalf("first strike must only warn: %#v", got)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].MentionUserID != "555" || !strings.Contains(sent[0].Text, "小明") || !strings.Contains(sent[0].Text, "再犯会被禁言") {
		t.Fatalf("warning = %#v", sent)
	}

	runtime.applyGovernancePenalty(context.Background(), event, gov, governanceVerdict{reason: governanceReasonKeyword, strike: 2, recallIDs: []string{"bad-2"}})
	ban := recordedCallsByAction(channel.callsSnapshot(), "set_group_ban")
	if len(ban) != 1 || intFromAny(ban[0].params["duration"]) != 600 {
		t.Fatalf("second strike set_group_ban = %#v", ban)
	}
	if sent := channel.sentSnapshot(); !strings.Contains(sent[len(sent)-1].Text, "已禁言 10 分钟") {
		t.Fatalf("second warning = %q", sent[len(sent)-1].Text)
	}
}

// 机器人不是管理员、或对方是管理员时什么都不做，只记日志。
func TestGovernancePenaltySkipsWithoutAuthority(t *testing.T) {
	gov := GroupGovernance{KeywordFilterEnabled: true, KeywordRules: []string{"广告"}}.Normalized()
	verdict := governanceVerdict{reason: governanceReasonKeyword, strike: 3, recallIDs: []string{"bad"}}
	event := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, SelfID: "10000", GroupID: "123", UserID: "555", MessageID: "bad"}
	for name, roles := range map[string]map[string]string{
		"bot is member":   {"10000": "member"},
		"target is admin": {"10000": "admin", "555": "admin"},
		"target is owner": {"10000": "admin", "555": "owner"},
	} {
		runtime, channel := governanceRuntime(t, &gov, roles)
		runtime.applyGovernancePenalty(context.Background(), event, gov, verdict)
		if calls := channel.callsSnapshot(); len(recordedCallsByAction(calls, "delete_msg"))+len(recordedCallsByAction(calls, "set_group_ban")) != 0 || len(channel.sentSnapshot()) != 0 {
			t.Fatalf("%s: acted anyway: calls=%#v sent=%#v", name, calls, channel.sentSnapshot())
		}
	}
}

func TestEnforceGroupGovernanceExemptionsAndInterception(t *testing.T) {
	gov := GroupGovernance{KeywordFilterEnabled: true, KeywordRules: []string{"广告"}}
	runtime, channel := governanceRuntime(t, &gov, map[string]string{"10000": "admin"})
	base := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, SelfID: "10000", GroupID: "123", Time: time.Now().Unix(), RawMessage: "广告", Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "广告"}}}}

	owner := base
	owner.UserID, owner.MessageID = "owner", "o1"
	admin := base
	admin.UserID, admin.MessageID, admin.SenderRole = "777", "a1", "admin"
	stale := base
	stale.UserID, stale.MessageID, stale.Time = "555", "s1", time.Now().Add(-10*time.Minute).Unix()
	otherGroup := base
	otherGroup.UserID, otherGroup.MessageID, otherGroup.GroupID = "555", "g1", "999"
	for name, event := range map[string]MessageEvent{"owner": owner, "group admin": admin, "stale replay": stale, "group without rules": otherGroup} {
		if runtime.enforceGroupGovernance(context.Background(), event) {
			t.Fatalf("%s should be exempt", name)
		}
	}

	member := base
	member.UserID, member.MessageID = "555", "bad-1"
	if !runtime.enforceGroupGovernance(context.Background(), member) {
		t.Fatal("keyword message was not intercepted")
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(recordedCallsByAction(channel.callsSnapshot(), "delete_msg")) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := recordedCallsByAction(channel.callsSnapshot(), "delete_msg"); len(got) != 1 {
		t.Fatalf("delete_msg calls = %#v", got)
	}
}

func TestMemberLeaveAuditNotifiesOwner(t *testing.T) {
	gov := GroupGovernance{MemberLeaveAuditEnabled: true}
	runtime, channel := governanceRuntime(t, &gov, nil)
	channel.apiResponses["get_group_info"] = map[string]any{"group_id": int64(123), "group_name": "测试群"}
	event := MessageEvent{
		Kind: EventKindNotice, SubType: "group_decrease", Platform: PlatformOneBotV11, SelfID: "10000", GroupID: "123", UserID: "555", OperatorID: "888",
		Segments: []MessageSegment{{Type: "notice", Data: map[string]string{"notice_type": "group_decrease", "sub_type": "kick", "operator_id": "888"}}},
	}
	if err := runtime.handleNotice(context.Background(), event); err != nil {
		t.Fatalf("handleNotice error = %v", err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].UserID != "owner" || sent[0].GroupID != "" {
		t.Fatalf("audit notification = %#v", sent)
	}
	for _, want := range []string{"测试群（123）", "555", "被 888 移出"} {
		if !strings.Contains(sent[0].Text, want) {
			t.Fatalf("audit text %q missing %q", sent[0].Text, want)
		}
	}

	// 没开审计的群不通知。
	off, offChannel := governanceRuntime(t, &GroupGovernance{}, nil)
	_ = off.handleNotice(context.Background(), event)
	if len(offChannel.sentSnapshot()) != 0 {
		t.Fatalf("audit disabled but notified: %#v", offChannel.sentSnapshot())
	}
}

func TestTelegramLeftChatMemberBecomesGroupDecrease(t *testing.T) {
	kicked := telegramMessageToEvent(&telegramMessage{
		MessageID: 5, Date: 1, Chat: &telegramChat{ID: -100, Type: "supergroup", Title: "群"},
		From: &telegramUser{ID: 9}, LeftChatMember: &telegramUser{ID: 7, FirstName: "离开"},
	}, "42", "bot")
	if kicked.Kind != EventKindNotice || kicked.SubType != "group_decrease" || kicked.UserID != "7" || kicked.OperatorID != "9" || noticeSegmentData(kicked)["sub_type"] != "kick" {
		t.Fatalf("kicked event = %#v", kicked)
	}
	left := telegramMessageToEvent(&telegramMessage{
		MessageID: 6, Date: 1, Chat: &telegramChat{ID: -100, Type: "supergroup"},
		From: &telegramUser{ID: 7}, LeftChatMember: &telegramUser{ID: 7},
	}, "42", "bot")
	if noticeSegmentData(left)["sub_type"] != "leave" || left.OperatorID != "" {
		t.Fatalf("left event = %#v", left)
	}
}

func TestWelcomePlaceholdersExpandNicknameAndGroup(t *testing.T) {
	runtime, channel := governanceRuntime(t, nil, nil)
	channel.apiResponses["get_group_info"] = map[string]any{"group_id": int64(123), "group_name": "测试群"}
	event := MessageEvent{Kind: EventKindNotice, SubType: "group_increase", Platform: PlatformOneBotV11, SelfID: "10000", GroupID: "123", UserID: "555"}
	got := runtime.renderWelcome(context.Background(), BotConfig{WelcomeMessage: "欢迎 {nickname}（{user_id}）加入 {group}（{group_id}）"}, event)
	if got != "欢迎 昵称555（555）加入 测试群（123）" {
		t.Fatalf("welcome = %q", got)
	}
}
