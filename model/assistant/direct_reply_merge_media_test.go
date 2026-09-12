// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func imageSegment(file string) MessageSegment {
	return MessageSegment{Type: "image", Data: map[string]string{"file": file, "url": "https://example.invalid/" + file}}
}

// 追发合并进来的消息，图片也要一起进这一轮。
//
// 以前只有 updatedReplyRequestText 把补充消息的文字并进当前问题，图片段留在各自
// 事件里没人取：「先发一张图、再补一张图问哪个好」时模型只收到一张，而正文里写着
// 两张，于是既答不准也说不清该处理哪一张。
func TestSupplementMediaJoinsTheTurn(t *testing.T) {
	root := MessageEvent{Kind: EventKindGroup, GroupID: "g", MessageID: "m1", Segments: []MessageSegment{
		{Type: "text", Data: map[string]string{"text": "这个怎么样"}},
		imageSegment("first.png"),
	}}
	supplements := []proactiveReplyCandidate{
		{Event: MessageEvent{Kind: EventKindGroup, GroupID: "g", MessageID: "m2", Segments: []MessageSegment{imageSegment("second.png")}}},
	}

	merged := attachInboundTurnMedia(root, directReplySupplementEvents(supplements))
	if got := imageSegmentCount(merged.Segments); got != 2 {
		t.Fatalf("合并后图片数 = %d，两条消息各一张都该在", got)
	}
	// 补充图片要带上来源消息号，后面按编号指代才对得上。
	var tagged bool
	for _, segment := range merged.Segments {
		if segment.Type == "image" && segment.Data["file"] == "second.png" {
			tagged = segment.Data["source_message_id"] == "m2"
		}
	}
	if !tagged {
		t.Fatal("补充进来的图片没有标上来源消息号")
	}
}

// 同一张图重复出现只算一张：合并规则和入站那条一致，不能因为补充而把图翻倍。
func TestSupplementMediaDeduplicates(t *testing.T) {
	root := MessageEvent{Kind: EventKindGroup, GroupID: "g", MessageID: "m1", Segments: []MessageSegment{imageSegment("same.png")}}
	supplements := []proactiveReplyCandidate{
		{Event: MessageEvent{Kind: EventKindGroup, GroupID: "g", MessageID: "m2", Segments: []MessageSegment{imageSegment("same.png")}}},
	}
	merged := attachInboundTurnMedia(root, directReplySupplementEvents(supplements))
	if got := imageSegmentCount(merged.Segments); got != 1 {
		t.Fatalf("同一张图合并后 = %d 张，应当只留一张", got)
	}
}

// 没有追发时这一轮原样不动。
func TestNoSupplementLeavesTurnUnchanged(t *testing.T) {
	root := MessageEvent{Kind: EventKindGroup, GroupID: "g", MessageID: "m1", Segments: []MessageSegment{imageSegment("only.png")}}
	merged := attachInboundTurnMedia(root, directReplySupplementEvents(nil))
	if imageSegmentCount(merged.Segments) != 1 || len(merged.Segments) != 1 {
		t.Fatalf("没有追发时不该改动这一轮：%+v", merged.Segments)
	}
}
