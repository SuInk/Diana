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
