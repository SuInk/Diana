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

// 首页的实时事件流只有 EventRecord 这一份文本，事件页那套读时重渲染帮不到它，所以
// 要在记录的时候就把 @ 和引用渲染好。
func TestEventRecordTextRendersMentionsAndReplies(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindGroup, GroupID: "20001", UserID: "10003", MessageID: "m2", SenderName: "阿强",
		Segments: []MessageSegment{
			{Type: "reply", Data: map[string]string{"id": "m1"}},
			atSegment("10002", ""),
			textSegment("你也去吗"),
		},
		Quoted: &QuotedMessage{
			MessageID: "m1", UserID: "10002", SenderName: "阿花",
			Segments: []MessageSegment{textSegment("下周要去上海出差")},
		},
	}
	// 被 @ 的人刚在同一个会话里说过话，昵称从内存历史里就能翻到。
	runtime.remember(MessageEvent{
		Kind: EventKindGroup, GroupID: "20001", UserID: "10002", MessageID: "m1", SenderName: "阿花",
		Segments: []MessageSegment{textSegment("下周要去上海出差")},
	})

	record := runtime.decisionEventRecord(event, PlainText(event.Segments), "replied")
	want := "[回复 阿花：下周要去上海出差] @阿花（10002） 你也去吗"
	if record.Text != want {
		t.Fatalf("record text = %q, want %q", record.Text, want)
	}
}

// 路由途中换过正文的地方要保持原样：控制台登录配对只记一个占位，正文里是配对码，
// 按 segment 重渲染会把它泄到首页事件流上。
func TestEventRecordTextKeepsSubstitutedText(t *testing.T) {
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Kind: EventKindPrivate, UserID: "10003", MessageID: "m1",
		Segments: []MessageSegment{textSegment("配对码 123456")},
	}
	record := runtime.decisionEventRecord(event, "[控制台登录配对]", "replied")
	if record.Text != "[控制台登录配对]" {
		t.Fatalf("record text = %q", record.Text)
	}
}

// QQ 的分享卡片 PlainText 一个字都不写，控制台上只剩一个 [CQ:json,...]。显示层要从
// 卡片里挖出标题和来源。
func TestDisplayEventTextRendersCards(t *testing.T) {
	cases := []struct {
		name  string
		event MessageEvent
		want  string
	}{
		{
			name: "转发的图文卡片取 meta 里的标题和来源",
			event: MessageEvent{Segments: []MessageSegment{{Type: "json", Data: map[string]string{
				"data": `{"app":"com.tencent.structmsg","prompt":"[分享]哥几个这期我是真想退出了","meta":{"news":{"tag":"哔哩哔哩","title":"哥几个这期我是真想退出了","desc":"UP主：渡梦如夏","jumpUrl":"https://b23.tv/x"}}}`,
			}}}},
			want: "[卡片·哔哩哔哩] 哥几个这期我是真想退出了 — UP主：渡梦如夏",
		},
		{
			name: "meta 容器名认不完，按键名排序挨个试，取第一个解得出标题的",
			event: MessageEvent{Segments: []MessageSegment{{Type: "json", Data: map[string]string{
				"data": `{"app":"com.tencent.miniapp_01","meta":{"detail_1":{"title":"某小程序","desc":"点开看看"},"zzz_other":{"title":"不该取到这个"}}}`,
			}}}},
			want: "[卡片] 某小程序 — 点开看看",
		},
		{
			name: "标题和描述一样时不重复写",
			event: MessageEvent{Segments: []MessageSegment{{Type: "json", Data: map[string]string{
				"data": `{"meta":{"news":{"title":"同一句话","desc":"同一句话"}}}`,
			}}}},
			want: "[卡片] 同一句话",
		},
		{
			name: "meta 解不出来时退回 QQ 自己的一行摘要",
			event: MessageEvent{Segments: []MessageSegment{{Type: "json", Data: map[string]string{
				"data": `{"app":"com.tencent.qqav.groupvideo","prompt":"[群视频]邀请你加入","meta":{}}`,
			}}}},
			want: "[卡片] [群视频]邀请你加入",
		},
		{
			name: "卡片解析不了时不写出半截 JSON",
			event: MessageEvent{Segments: []MessageSegment{
				{Type: "json", Data: map[string]string{"data": "{不是合法 JSON"}},
				textSegment("看看这个"),
			}},
			want: "看看这个",
		},
		{
			name: "xml 卡片取 brief——那就是客户端在聊天列表里显示的那句",
			event: MessageEvent{Segments: []MessageSegment{{Type: "xml", Data: map[string]string{
				"data": `<?xml version="1.0"?><msg serviceID="1" title="通用" brief="[聊天记录]群友们的对话"><item/></msg>`,
			}}}},
			want: "[卡片] [聊天记录]群友们的对话",
		},
		{
			name: "合并转发不再摊出那串 resid",
			event: MessageEvent{Segments: []MessageSegment{{Type: "forward", Data: map[string]string{
				"id": "WpH3vkLdEPC7uJdgryfQ0/wjIKA1TybbBHXwIeJDTiOv9grWdElUZL9zn+jeVaES",
			}}}},
			want: "[合并转发]",
		},
		{
			name: "合并转发带摘要时用摘要",
			event: MessageEvent{Segments: []MessageSegment{{Type: "forward", Data: map[string]string{
				"id": "WpH3vk", "summary": "群聊的聊天记录",
			}}}},
			want: "群聊的聊天记录",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := DisplayEventText(testCase.event, nil); got != testCase.want {
				t.Fatalf("text = %q, want %q", got, testCase.want)
			}
		})
	}
}

// 卡片现在也进给模型的正文：以前它一个字都不写，链接解析没命中的卡片（小程序、
// 群视频邀请、音乐分享）模型那边就是一片空白。合并转发的 resid 则维持原样，那是
// PlainText 的既有行为，只有控制台换写法。
func TestPlainTextCarriesCardsAndKeepsForwardID(t *testing.T) {
	event := MessageEvent{Segments: []MessageSegment{
		{Type: "json", Data: map[string]string{"data": `{"meta":{"news":{"title":"标题","tag":"哔哩哔哩"}}}`}},
		{Type: "forward", Data: map[string]string{"id": "WpH3vk"}},
	}}
	if got := PlainText(event.Segments); got != "[卡片·哔哩哔哩] 标题 [合并转发:WpH3vk]" {
		t.Fatalf("plain text = %q", got)
	}
}

// 卡片里什么都挖不出来时保持原样不写：一个空的「[卡片]」只是噪声，还会把本来算
// 空正文的消息变成非空，连带影响触发判断。
func TestPlainTextSkipsUnreadableCard(t *testing.T) {
	event := MessageEvent{Segments: []MessageSegment{
		{Type: "json", Data: map[string]string{"data": "{不是合法 JSON"}},
	}}
	if got := PlainText(event.Segments); got != "" {
		t.Fatalf("plain text = %q, want empty", got)
	}
	if got := DisplayEventText(event, nil); got != "" {
		t.Fatalf("display text = %q, want empty", got)
	}
}
