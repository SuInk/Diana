// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

func atSegment(qq string, name string) MessageSegment {
	data := map[string]string{"qq": qq}
	if name != "" {
		data["name"] = name
	}
	return MessageSegment{Type: "at", Data: data}
}

func textSegment(text string) MessageSegment {
	return MessageSegment{Type: "text", Data: map[string]string{"text": text}}
}

// 「最近发言」是给人翻的，@ 和引用不能停在一串号码上。
func TestDisplayEventTextResolvesMentionsAndReplies(t *testing.T) {
	names := map[string]string{"3129583166": "小明", "10002": "阿花"}
	resolve := func(userID string) string { return names[userID] }

	cases := []struct {
		name  string
		event MessageEvent
		want  string
	}{
		{
			name: "平台没给昵称时按档案补上",
			event: MessageEvent{Segments: []MessageSegment{
				atSegment("3129583166", ""), textSegment("看看这个"),
			}},
			want: "@小明（3129583166） 看看这个",
		},
		{
			name: "平台自己带了昵称就不查档案",
			event: MessageEvent{Segments: []MessageSegment{
				atSegment("3129583166", "群里的小明"), textSegment("在吗"),
			}},
			want: "@群里的小明（3129583166） 在吗",
		},
		{
			name: "查不到昵称时退回号码，和改动前一致",
			event: MessageEvent{Segments: []MessageSegment{
				atSegment("99999", ""), textSegment("你好"),
			}},
			want: "@99999 你好",
		},
		{
			name: "引用写清回的是谁的哪句话",
			event: MessageEvent{
				Segments: []MessageSegment{
					{Type: "reply", Data: map[string]string{"id": "m1"}},
					textSegment("我也去"),
				},
				Quoted: &QuotedMessage{
					MessageID: "m1", UserID: "10002", SenderName: "阿花",
					Segments: []MessageSegment{textSegment("下周要去上海出差")},
				},
			},
			want: "[回复 阿花：下周要去上海出差] 我也去",
		},
		{
			name: "被引用的人没有昵称时也按档案补",
			event: MessageEvent{
				Segments: []MessageSegment{
					{Type: "reply", Data: map[string]string{"id": "m1"}},
					textSegment("同意"),
				},
				Quoted: &QuotedMessage{
					MessageID: "m1", UserID: "10002",
					Segments: []MessageSegment{textSegment("这家不错")},
				},
			},
			want: "[回复 阿花：这家不错] 同意",
		},
		{
			name: "原消息不在事件上时只写「回复」，不写那串号码",
			event: MessageEvent{Segments: []MessageSegment{
				{Type: "reply", Data: map[string]string{"id": "m1"}},
				textSegment("收到"),
			}},
			want: "[回复] 收到",
		},
		{
			name: "引用的是另一条消息时不拿手头这条冒充",
			event: MessageEvent{
				Segments: []MessageSegment{
					{Type: "reply", Data: map[string]string{"id": "m9"}},
					textSegment("收到"),
				},
				Quoted: &QuotedMessage{
					MessageID: "m1", SenderName: "阿花",
					Segments: []MessageSegment{textSegment("完全无关的一句")},
				},
			},
			want: "[回复] 收到",
		},
		{
			name: "被引用的消息自己也是条回复时不再往下展开",
			event: MessageEvent{
				Segments: []MessageSegment{
					{Type: "reply", Data: map[string]string{"id": "m2"}},
					textSegment("对"),
				},
				Quoted: &QuotedMessage{
					MessageID: "m2", SenderName: "阿花",
					Segments: []MessageSegment{
						{Type: "reply", Data: map[string]string{"id": "m1"}},
						textSegment("是这个意思"),
					},
				},
			},
			want: "[回复 阿花：[回复] 是这个意思] 对",
		},
		{
			name: "被引用的原话太长时截断",
			event: MessageEvent{
				Segments: []MessageSegment{
					{Type: "reply", Data: map[string]string{"id": "m1"}},
					textSegment("嗯"),
				},
				Quoted: &QuotedMessage{
					MessageID: "m1", SenderName: "阿花",
					Segments: []MessageSegment{textSegment("一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十还有更多")},
				},
			},
			want: "[回复 阿花：一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十…] 嗯",
		},
		{
			name:  "段解不出内容时退回平台原串",
			event: MessageEvent{RawMessage: "  原样  的一串  "},
			want:  "原样 的一串",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := DisplayEventText(testCase.event, resolve); got != testCase.want {
				t.Fatalf("text = %q, want %q", got, testCase.want)
			}
		})
	}
}

// 没有解析器时行为要和改动前一样，只是引用不再写成 [diana-reply:ID]。
func TestDisplayEventTextWithoutResolver(t *testing.T) {
	event := MessageEvent{Segments: []MessageSegment{
		atSegment("3129583166", ""), textSegment("在吗"),
	}}
	if got := DisplayEventText(event, nil); got != "@3129583166 在吗" {
		t.Fatalf("text = %q", got)
	}
}

// 给模型看的 PlainText 不受影响：出站适配器要靠 [diana-reply:ID] 还原引用关系。
func TestPlainTextKeepsReplyMarkerForTheModel(t *testing.T) {
	event := MessageEvent{Segments: []MessageSegment{
		{Type: "reply", Data: map[string]string{"id": "m1"}},
		textSegment("收到"),
	}}
	if got := PlainText(event.Segments); got != "[diana-reply:m1]收到" {
		t.Fatalf("plain text = %q", got)
	}
}
