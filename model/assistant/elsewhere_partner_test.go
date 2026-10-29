// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func elsewhereFixture(now int64) (BotConfig, []MessageEvent) {
	cfg := BotConfig{BotAccount: "90001"}
	reply := func(id, user, name, to, toName string, at int64) MessageEvent {
		return MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: user, SenderName: name, MessageID: id, Time: at,
			Quoted: &QuotedMessage{UserID: to, SenderName: toName}}
	}
	return cfg, []MessageEvent{
		reply("m1", "10001", "黑蛋", "20002", "简（zero）", now-120),
		reply("m2", "20002", "简（zero）", "10001", "黑蛋", now-90),
		{Kind: EventKindGroup, GroupID: "g", UserID: "10001", SenderName: "黑蛋", MessageID: "m3", Time: now - 30},
	}
}

// 线上那次：群主一直在跟另一个机器人一问一答，一句没点名的「你回复短一点」。
func TestElsewherePartnerFindsOtherBotConversation(t *testing.T) {
	now := int64(1_700_000_000)
	cfg, history := elsewhereFixture(now)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "10001", MessageID: "m4", Time: now, RawMessage: "你回复还是短一点"}
	got := elsewherePartnerPrompt(cfg, event, append(history, event))
	if !strings.Contains(got, "简（zero）") || !strings.Contains(got, "silent=true") {
		t.Fatalf("应提醒这句多半是说给简（zero）的：%q", got)
	}
}

// 本人比那段往来说话更晚：这句多半接着本人说，不提醒。
func TestElsewherePartnerSkipsWhenBotAnsweredSenderLast(t *testing.T) {
	now := int64(1_700_000_000)
	cfg, history := elsewhereFixture(now)
	// 本人的跟进不引用也不 @，照样算本人在这段对话里。
	history = append(history, MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "90001", Outbound: true, MessageID: "b1", Time: now - 10})
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "10001", MessageID: "m4", Time: now, RawMessage: "那你再说说"}
	if got := elsewherePartner(cfg, event, append(history, event)); got != "" {
		t.Fatalf("本人刚回过他，不该提醒，得到 %q", got)
	}
}

// 叫了本人的名、@ 或引用了本人，结构上已经清楚，不提醒。
func TestElsewherePartnerSkipsWhenCurrentAddressesSelf(t *testing.T) {
	now := int64(1_700_000_000)
	cfg, history := elsewhereFixture(now)
	cfg.GroupTriggers = []string{"嘉然"}
	named := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "10001", MessageID: "m4", Time: now, RawMessage: "嘉然你回复短一点"}
	if got := elsewherePartner(cfg, named, append(history, named)); got != "" {
		t.Fatalf("点了名不该提醒，得到 %q", got)
	}
	at := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "10001", MessageID: "m5", Time: now, RawMessage: "你回复短一点",
		Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": "90001"}}}}
	if got := elsewherePartner(cfg, at, append(history, at)); got != "" {
		t.Fatalf("@ 了本人不该提醒，得到 %q", got)
	}
}

// 只有一条孤立的往来、或往来已经是十几分钟前的事，都不足以说明这句不是给本人的。
func TestElsewherePartnerNeedsRecentRepeatedExchange(t *testing.T) {
	now := int64(1_700_000_000)
	cfg, history := elsewhereFixture(now)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "10001", MessageID: "m4", Time: now, RawMessage: "你回复短一点"}
	if got := elsewherePartner(cfg, event, append(history[1:], event)); got != "" {
		t.Fatalf("一条往来不该提醒，得到 %q", got)
	}
	later := event
	later.Time = now + int64(elsewherePartnerWindow.Seconds())
	if got := elsewherePartner(cfg, later, append(history, later)); got != "" {
		t.Fatalf("窗口外的往来不该提醒，得到 %q", got)
	}
}
