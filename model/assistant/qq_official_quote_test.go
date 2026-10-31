// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQQOfficialQuotedMediaKeepsCaptionInPrompt(t *testing.T) {
	for _, kind := range []string{"image/jpeg", "video/mp4", "voice", "file"} {
		t.Run(kind, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{
				"id": "ROBOT1.0_quote", "group_openid": "G", "content": "帮我看一下", "message_type": qqMessageTypeQuote,
				"message_scene": map[string]any{"ext": []string{"ref_msg_idx=REFIDX_original"}},
				"msg_elements": []map[string]any{{
					"content":     "这是第一个附件，请检查报错",
					"attachments": []map[string]any{{"content_type": kind, "filename": "a", "url": "https://example.com/a"}},
				}, {"content": "这是补充说明"}},
				"author": map[string]any{"member_openid": "member"},
			})
			if err != nil {
				t.Fatal(err)
			}
			event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot")
			if !ok || event.Quoted == nil {
				t.Fatal("quote not parsed")
			}
			caption := "这是第一个附件，请检查报错\n这是补充说明"
			for name, text := range map[string]string{
				"raw": event.Quoted.RawMessage, "prompt": quotedPromptText(event.Quoted), "routing": quotedPlainText(event.Quoted),
			} {
				if !strings.Contains(text, caption) {
					t.Errorf("%s lost the quoted caption: %q", name, text)
				}
			}
			if len(event.Quoted.Segments) != 2 || event.Quoted.Segments[0].Type != "text" {
				t.Fatalf("quoted segments = %+v, want caption followed by media", event.Quoted.Segments)
			}
		})
	}
}

func TestQQOfficialQuotedChatRecordKeepsMediaSegments(t *testing.T) {
	imageURL := "https://multimedia.nt.qq.com.cn/download?fileid=a&rkey=image"
	videoURL := "https://multimedia.nt.qq.com.cn/download?fileid=b&rkey=video"
	content := strings.Join([]string{
		"[群聊的聊天记录]",
		"=== 消息 1 ===",
		"[消息内容] 请检查这张图片",
		"[发送者] 张三",
		"[附件1] 类型:图片 文件名:a.jpg 尺寸:1x1 大小:1KB URL:" + imageURL,
		"=== 消息 2 ===",
		`[消息内容] <faceType=6,faceId="0",ext="eyJ0ZXh0IjoiIn0=">`,
		"[发送者] 张三",
		"[附件1] 类型:视频 文件名:b.mp4 尺寸:1x1 大小:1KB URL:" + videoURL,
	}, "\n")
	data, err := json.Marshal(map[string]any{
		"id": "ROBOT1.0_quote", "group_openid": "G", "content": "看看记录里的图片", "message_type": qqMessageTypeQuote,
		"message_scene": map[string]any{"ext": []string{"ref_msg_idx=TMP_record"}},
		"msg_elements":  []map[string]any{{"content": content}},
		"author":        map[string]any{"member_openid": "member"},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, ok := qqOfficialEventFromDispatch("GROUP_MESSAGE_CREATE", data, "bot")
	if !ok || event.Quoted == nil {
		t.Fatal("quoted record not parsed")
	}
	segments := event.Quoted.Segments
	if len(segments) != 3 || segments[0].Type != "text" || segments[1].Type != "image" || segments[2].Type != "video" {
		t.Fatalf("quoted segments = %+v, want text/image/video", segments)
	}
	if segments[1].Data["url"] != imageURL || segments[2].Data["url"] != videoURL {
		t.Fatalf("quoted segments lost the media addresses: %+v", segments)
	}
	if strings.Contains(event.Quoted.RawMessage, "rkey=") || strings.Contains(event.Quoted.RawMessage, "<faceType") {
		t.Fatalf("quoted text kept media URLs or face protocol: %q", event.Quoted.RawMessage)
	}
	if !strings.Contains(quotedPromptText(event.Quoted), "请检查这张图片") {
		t.Fatal("quoted record lost its readable text")
	}
}

func TestQQOfficialQuoteUsesPlatformIDForHistoryAndOutboundReference(t *testing.T) {
	for _, outbound := range []bool{false, true} {
		name := "member"
		if outbound {
			name = "bot"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			api := newQQFakeAPI(t)
			channel := api.channel()
			channel.setStatus(true, "bot-1", "")
			runtime := NewRuntime(BotConfig{BotAccount: "bot-1", ReplyReferenceMode: ReplyDecorationAuto}, channel, NewPluginManager(), nil, nil, nil, nil)
			runtime.SetMessageHistoryStore(newSemanticTimelineStore())
			original := MessageEvent{
				Platform: PlatformQQOfficial, Kind: EventKindGroup, GroupID: "G", UserID: "member", SelfID: "bot-1",
				MessageID: qqOfficialProbeID, RawMessage: "原消息", Time: time.Now().Add(-time.Second).Unix(), Outbound: outbound,
				Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "原消息"}}, {
					Type: "image", Data: map[string]string{"cached_file": filepath.Join(t.TempDir(), "original.png")},
				}},
			}
			if outbound {
				original.UserID = "bot-1"
				channel.refs.recordOutbound("REFIDX_original", original.MessageID, original.UserID, time.Now())
			} else {
				channel.refs.recordInbound("REFIDX_original", original.MessageID, original.UserID, false, time.Now())
			}
			runtime.remember(original)
			// 清掉内存副本，确保引用 ID 也能命中持久化历史。
			runtime.mu.Lock()
			runtime.history[sessionKey(original)] = nil
			runtime.mu.Unlock()
			var quote MessageEvent
			channel.handler = func(_ context.Context, event MessageEvent) error {
				quote = runtime.enrichReplyReference(ctx, event)
				return nil
			}
			channel.handleDispatch(ctx, qqGatewayPayload{T: "GROUP_MESSAGE_CREATE", Data: json.RawMessage(`{
			  "id":"ROBOT1.0_quote","group_openid":"G","content":"展开这条前后的讨论","message_type":103,
			  "message_scene":{"ext":["ref_msg_idx=REFIDX_original"]},
			  "msg_elements":[{"content":"原消息"}],"author":{"member_openid":"member"}
			}`)})
			if quote.Quoted == nil || quote.Quoted.MessageID != original.MessageID || quote.Quoted.UserID != original.UserID {
				t.Fatalf("quoted message = %+v, want the original platform ID and sender", quote.Quoted)
			}
			if got := quote.Quoted.Segments; len(got) != 2 || got[1].Data["cached_file"] != original.Segments[1].Data["cached_file"] {
				t.Fatalf("quote failed to restore cached historical media: %+v", got)
			}
			raw, err := newDianaChatHistoryTool(runtime, quote).Run(ctx, map[string]any{"operation": "around"})
			if err != nil {
				t.Fatal(err)
			}
			var history dianaChatHistoryResult
			if err := json.Unmarshal([]byte(raw), &history); err != nil || history.AnchorMessageID != original.MessageID {
				t.Fatalf("history anchor = %q, err = %v", history.AnchorMessageID, err)
			}
			if _, err := runtime.sendOutgoingWithResult(ctx, quote, OutgoingMessage{
				Text: replyMarkerFor(quote.Quoted.MessageID) + "回答",
			}); err != nil {
				t.Fatal(err)
			}
			if len(api.messages) != 1 {
				t.Fatalf("sent messages = %+v, want one reply", api.messages)
			}
			body := api.messages[0]
			ref, ok := body["message_reference"].(map[string]any)
			if !ok || ref["message_id"] != "REFIDX_original" || body["content"] != "回答" || body["msg_id"] != quote.MessageID {
				t.Fatalf("outbound body = %+v, want the original quote and current passive reply ID", body)
			}
		})
	}
}

func TestQQOfficialOtherBotQuoteRemainsContextOnly(t *testing.T) {
	ctx := context.Background()
	channel := &QQOfficialChannel{}
	channel.setStatus(true, "bot-1", "")
	channel.refs.recordOutbound("REFIDX_bot", qqOfficialProbeID, "bot-1", time.Now())
	runtime := NewRuntime(BotConfig{BotAccount: "bot-1", GroupTriggers: []string{"Diana"}}, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) {
		t.Fatal("another QQ bot must not start reply generation or judgment")
		return nil, nil
	})
	var received MessageEvent
	channel.handler = func(_ context.Context, event MessageEvent) error {
		received = event
		return nil
	}
	channel.handleDispatch(ctx, qqGatewayPayload{T: "GROUP_MESSAGE_CREATE", Data: json.RawMessage(`{
	  "id":"ROBOT1.0_otherbot","group_openid":"G","content":"Diana 继续回答","message_type":103,
	  "message_scene":{"ext":["ref_msg_idx=REFIDX_bot"]},"msg_elements":[{"content":"机器人回复"}],
	  "author":{"id":"other-bot","bot":true}
	}`)})
	if !received.SenderIsBot || received.ToMe || received.Quoted == nil || received.Quoted.UserID != "bot-1" {
		t.Fatalf("received event = %+v, want bot identity and complete quote without ToMe", received)
	}
	// 入站队列持久化再恢复后，同样必须保留来源机器人身份。
	body, err := json.Marshal(received)
	if err != nil {
		t.Fatal(err)
	}
	var restored MessageEvent
	if err := json.Unmarshal(body, &restored); err != nil {
		t.Fatal(err)
	}
	_, _, handled, outcome := runtime.prepareMessageEvent(ctx, restored)
	if handled || outcome != "ignored_bot_message" {
		t.Fatalf("handled = %v, outcome = %q, want context only", handled, outcome)
	}
	if runtime.lookupQuotedMessage(ctx, restored, restored.MessageID) == nil {
		t.Fatal("other bot's message was not retained in history")
	}
}
