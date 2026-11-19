// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func imageQuestionEvent(text string) MessageEvent {
	segments := []MessageSegment{{Type: "image", Data: map[string]string{"file": "a.png"}}}
	if text != "" {
		segments = append(segments, MessageSegment{Type: "text", Data: map[string]string{"text": text}})
	}
	return MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1", Segments: segments}
}

func TestVisionFocusedTurnDetectsSelfContainedImageQuestions(t *testing.T) {
	if !visionFocusedTurn(imageQuestionEvent("这是什么"), "这是什么") {
		t.Fatal("a plain image question was not treated as vision-focused")
	}

	// 引用一张图追问同样自足。
	quoted := MessageEvent{
		Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m2",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "这个怎么读"}}},
		Quoted: &QuotedMessage{
			MessageID: "m1",
			Segments:  []MessageSegment{{Type: "image", Data: map[string]string{"file": "a.png"}}},
		},
	}
	if !visionFocusedTurn(quoted, "这个怎么读") {
		t.Fatal("quoting a bare image was not treated as vision-focused")
	}
}

func TestVisionFocusedTurnRejectsContextDependentRequests(t *testing.T) {
	// 没有图：普通聊天不该被收紧历史。
	textOnly := MessageEvent{
		Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "这是什么"}}},
	}
	if visionFocusedTurn(textOnly, "这是什么") {
		t.Fatal("a text-only message was treated as vision-focused")
	}

	// 长请求通常在描述任务而不是问图。
	long := "帮我把这张图里的表格提取出来，然后跟我们上周确认过的那版预算逐行核对，" +
		"不一致的地方标出来并说明可能的原因，最后整理成一段可以直接发群里的说明"
	if visionFocusedTurn(imageQuestionEvent(long), long) {
		t.Fatal("a long task description was treated as vision-focused")
	}

	// 引用里带文字说明用户在接着那条消息说话，不只是指一张图。
	quotedWithText := imageQuestionEvent("这个呢")
	quotedWithText.Quoted = &QuotedMessage{
		MessageID: "m0",
		Segments: []MessageSegment{
			{Type: "text", Data: map[string]string{"text": "刚才那版预算我改了几处"}},
		},
	}
	if visionFocusedTurn(quotedWithText, "这个呢") {
		t.Fatal("a reply to a text message was treated as vision-focused")
	}

	// 只发图不提问，交给正常路径决定要不要搭话。
	if visionFocusedTurn(imageQuestionEvent(""), "") {
		t.Fatal("a bare image upload was treated as a vision question")
	}
}

func TestVisionFocusedTurnRespectsResolvedSemanticReference(t *testing.T) {
	event := imageQuestionEvent("这个呢")
	setEventSemanticSourceMessageIDs(&event, []string{"older-1"})

	// 指代已经把这一轮接到更早的消息上，它就不自足了。
	if visionFocusedTurn(event, "这个呢") {
		t.Fatal("a turn with a resolved semantic reference was treated as self-contained")
	}
}

func TestVisionFocusedHistoryBudgetOnlyNarrows(t *testing.T) {
	event := imageQuestionEvent("这是什么")

	if got := visionFocusedHistoryBudget(16000, event, "这是什么"); got != visionFocusedHistoryTokens {
		t.Fatalf("vision-focused budget = %d, want %d", got, visionFocusedHistoryTokens)
	}
	// 小窗口下已经比上限更紧时不能被放宽。
	if got := visionFocusedHistoryBudget(800, event, "这是什么"); got != 800 {
		t.Fatalf("narrow budget was widened to %d", got)
	}
	// 非看图轮次原样返回。
	textOnly := MessageEvent{
		Kind: EventKindGroup, GroupID: "g1", UserID: "u1", MessageID: "m1",
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "在吗"}}},
	}
	if got := visionFocusedHistoryBudget(16000, textOnly, "在吗"); got != 16000 {
		t.Fatalf("ordinary turn budget was narrowed to %d", got)
	}
}
