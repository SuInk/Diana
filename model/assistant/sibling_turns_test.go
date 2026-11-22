// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 两人前后脚要同一张图，两轮并行：后开的那轮要知道前一轮在回什么，前一轮中途
// 发的图要在后一轮下一步规划前接进来；自己这轮发的不算。
func TestSiblingTurnsSeeEachOtherInSameGroup(t *testing.T) {
	r := NewRuntime(BotConfig{}, nil, NewPluginManager(), nil, nil, nil, nil)
	first := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20001", SenderName: "Winter", MessageID: "1",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "嘉然你查下他的头像"}}}}
	second := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20002", SenderName: "egan", MessageID: "2",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "把头像发出来"}}}}

	firstCtx := withOutboundTurn(context.Background(), "turn-1")
	observer, stopFirst := r.startReplyProgress(firstCtx, first, nil)
	defer stopFirst()
	observer(firstCtx, agent.RunEvent{Phase: agent.RunPhaseToolStarted, Tool: "remote_image"})

	note := r.siblingRepliesNote(second)
	if !strings.Contains(note, "Winter") || !strings.Contains(note, "嘉然你查下他的头像") || !strings.Contains(note, "处理图片") {
		t.Fatalf("sibling note = %q", note)
	}
	if note := r.siblingRepliesNote(first); note != "" {
		t.Fatalf("own turn listed as sibling: %q", note)
	}
	// 同一个人的上一轮交给 runningReplyNote，这里不重复。
	sameSender := second
	sameSender.UserID, sameSender.MessageID = first.UserID, "3"
	if note := r.siblingRepliesNote(sameSender); note != "" {
		t.Fatalf("same sender listed as sibling: %q", note)
	}
	otherGroup := second
	otherGroup.GroupID = "10002"
	if note := r.siblingRepliesNote(otherGroup); note != "" {
		t.Fatalf("other group sees sibling: %q", note)
	}

	secondCtx := withOutboundTurn(context.Background(), "turn-2")
	interjections := r.startSiblingSendInterjections(secondCtx, second)
	if interjections.Pending() {
		t.Fatal("pending before anything was sent")
	}
	// 自己这轮发的不插回来。
	r.sessionSends.note(second, "turn-2", OutgoingMessage{Text: "我看看"}, time.Now())
	if interjections.Pending() {
		t.Fatal("own send treated as sibling send")
	}
	r.sessionSends.note(first, "turn-1", OutgoingMessage{ImageURLs: []string{"https://example.com/a.jpg"}}, time.Now())
	r.sessionSends.note(first, "turn-1", OutgoingMessage{Text: "<diana-quote id=\"1\"/>这就是他的 GitHub 头像"}, time.Now())
	if !interjections.Pending() {
		t.Fatal("sibling send not pending")
	}
	messages := interjections.Take()
	if len(messages) != 1 {
		t.Fatalf("messages = %+v", messages)
	}
	content := messages[0].Content
	if !strings.Contains(content, "图片×1") || !strings.Contains(content, "这就是他的 GitHub 头像") || strings.Contains(content, "diana-quote") || !strings.Contains(content, "同一张图也别再发") {
		t.Fatalf("interjection = %q", content)
	}
	if interjections.Pending() || len(interjections.Take()) != 0 {
		t.Fatal("same sends delivered twice")
	}
}

func TestCombineInterjectionsSkipsNil(t *testing.T) {
	if combineInterjections(nil, nil) != nil {
		t.Fatal("empty combination should be nil")
	}
	r := NewRuntime(BotConfig{}, nil, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "10001"}
	sibling := r.startSiblingSendInterjections(withOutboundTurn(context.Background(), "turn-1"), event)
	combined := combineInterjections(nil, sibling)
	if combined != sibling {
		t.Fatal("single source should be returned as is")
	}
	// 后台任务没有 outboundTurn，不接。
	if r.startSiblingSendInterjections(context.Background(), event) != nil {
		t.Fatal("background run got sibling interjections")
	}
}

// 2026-11-22：两个人前后脚夸了一句，两轮几乎同时在跑，都在对方真正发出去之前收尾。
// 前一轮定稿就记「正在发」，后到那条的一轮收尾前看得到；前一轮不看后一轮；真发出去的
// 那句不再记第二遍；没发出去的定稿随那一轮结束撤掉。
func TestLaterSiblingSeesEarlierTurnDraftBeforeSend(t *testing.T) {
	r := NewRuntime(BotConfig{}, nil, NewPluginManager(), nil, nil, nil, nil)
	earlier := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20001", SenderName: "Winter", MessageID: "1",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "你是懂篮球的"}}}}
	later := MessageEvent{Kind: EventKindGroup, GroupID: "10001", UserID: "20002", SenderName: "egan", MessageID: "2",
		Segments: []MessageSegment{{Type: "at", Data: map[string]string{"qq": "10000"}}}}
	r.noteSenderTurnArrival(earlier)
	time.Sleep(2 * time.Millisecond)
	r.noteSenderTurnArrival(later)

	// 后到的那条先开跑（生产里先到的那条多做了一次话题判断）。
	laterInterjections := r.startSiblingSendInterjections(withOutboundTurn(context.Background(), "turn-later"), later)
	earlierInterjections := r.startSiblingSendInterjections(withOutboundTurn(context.Background(), "turn-earlier"), earlier)
	if laterInterjections.Pending() {
		t.Fatal("pending before anything was drafted")
	}
	r.sessionSends.announce(earlier, "turn-earlier", "嘿嘿收下这个夸奖", time.Now())
	if !laterInterjections.Pending() {
		t.Fatal("earlier draft not visible to the later turn before it was sent")
	}
	messages := laterInterjections.Take()
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "嘿嘿收下这个夸奖") || !strings.Contains(messages[0].Content, "正在发") {
		t.Fatalf("interjection = %+v", messages)
	}

	// 前一轮不看后一轮。
	r.sessionSends.announce(later, "turn-later", "嘿嘿被夸了", time.Now())
	if earlierInterjections.Pending() {
		t.Fatal("earlier turn sees the later turn's draft")
	}
	// 前一轮真发出去的就是定稿那句，不再算一条新的。
	r.sessionSends.note(earlier, "turn-earlier", OutgoingMessage{Text: "嘿嘿收下这个夸奖"}, time.Now())
	if laterInterjections.Pending() {
		t.Fatal("sent draft delivered to the later turn twice")
	}
	// 定稿还没发出去不算复读：两轮定稿一样时不能互相拿「正在发」当理由都不发。
	ctx := withReplyTurnStart(withOutboundTurn(context.Background(), "turn-third"), time.Now().Add(-time.Second))
	r.sessionSends.announce(later, "turn-later", "这球打得是真的漂亮啊大家", time.Now())
	if r.sessionSends.repeatsRecentSibling(ctx, later, "这球打得是真的漂亮啊大家", time.Now()) {
		t.Fatal("unsent draft treated as a sent duplicate")
	}
	// 后一轮没发出去，定稿随它结束撤掉；前一轮发出去的留着。
	r.sessionSends.endTurn(later, "turn-later")
	r.sessionSends.endTurn(earlier, "turn-earlier")
	var turns []string
	for _, record := range r.sessionSends.after(later, time.Time{}, "", time.Time{}) {
		turns = append(turns, record.turnID)
	}
	if strings.Join(turns, ",") != "turn-earlier" {
		t.Fatalf("records after turn end = %v", turns)
	}
}
