package assistant

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestReplyLayoutAndDeliveryAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name, input      string
		preserve, single bool
		want             []string
	}{
		{"plain", "结论\n解释\n补充", false, false, []string{"结论，解释，补充"}},
		{"punctuation", "对吗？\n是的！\n原因如下。\n结尾", false, false, []string{"对吗？是的！原因如下。结尾"}},
		{"english", "First thought\nSecond thought", false, false, []string{"First thought Second thought"}},
		{"preserve", "结论\n\n解释", true, false, []string{"结论\n\n解释"}},
		{"list", "1. 检查\n2. 重启\n3. 验证", false, false, []string{"1. 检查\n2. 重启\n3. 验证"}},
		{"explicit inline list", replyLinesCompactMarker + "1. 检查\n2. 重启", true, false, []string{"1. 检查\n2. 重启"}},
		{"tea", "大吉岭\n1. 春摘\n2. 夏摘\n适合清饮\n\n锡兰\n1. 高地\n2. 低地\n适合调饮", false, false, []string{"大吉岭\n1. 春摘\n2. 夏摘\n适合清饮\n\n锡兰\n1. 高地\n2. 低地\n适合调饮"}},
		{"marker", "结论[diana-br]解释\n补充", false, false, []string{"结论", "解释，补充"}},
		{"disabled", "结论[diana-br]解释", false, true, []string{"结论，解释"}},
		{"single preserve", replySingleMarker + replyLinesPreserveMarker + "第一行\n第二行\n第三行", false, false, []string{"第一行\n第二行\n第三行"}},
		{"auto compact", replyLinesCompactMarker + replyAutoMarker + "第一段\n解释[diana-br]第二段\n解释", true, true, []string{"第一段，解释", "第二段，解释"}},
		{"code", "```text\n[diana-br]\n\n keep\n```", false, false, []string{"```text\n[diana-br]\n\n keep\n```"}},
		{"quote", "> 原文\n> 保留", false, false, []string{"> 原文\n> 保留"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.input = strings.ReplaceAll(tc.input, "\n", notificationLineMarker)
			if tc.name != "code" {
				tc.input = strings.ReplaceAll(tc.input, "[diana-br]", notificationSplitMarker)
			}
			cfg := BotConfig{Platform: PlatformTelegram, NaturalReplySplitEnabled: boolPointer(!tc.single), ReplyPreserveLineBreaks: boolPointer(tc.preserve)}
			for _, split := range []func(string, chatSplitLimits) []string{splitChatReply, splitForwardReply} {
				if got := split(tc.input, chatSplitLimitsFrom(cfg)); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("got=%q want=%q", got, tc.want)
				}
			}
		})
	}
}

func TestReplyLayoutMetadataAndGroupInheritance(t *testing.T) {
	cfg := BotConfig{NaturalReplySplitEnabled: boolPointer(false), ReplyPreserveLineBreaks: boolPointer(true)}
	r := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	r.SetGroupConfigStore(&stubGroupConfigStore{configs: map[string]GroupConfig{"override": {GroupID: "override", ReplyPreserveLineBreaks: boolPointer(false)}}})
	for _, group := range []string{"inherit", "override"} {
		event := MessageEvent{Kind: EventKindGroup, GroupID: group}
		effective := r.effectiveConfigForEvent(event)
		if boolValue(effective.ReplyPreserveLineBreaks, false) != (group == "inherit") {
			t.Fatal("layout inheritance lost")
		}
		input := replyLinesPreserveMarker + replyAutoMarker + "甲" + notificationLineMarker + "乙" + notificationSplitMarker + "丙"
		prepared, err := r.prepareGeneratedReply(context.Background(), effective, input, event)
		if err != nil {
			t.Fatal(err)
		}
		body, intent := consumeReplyControlIntent(prepared)
		if intent.LineBreakMode != replyLinesPreserve || intent.DeliveryMode != replyDeliveryAuto {
			t.Fatalf("lost metadata: %+v", intent)
		}
		event.replyDeliveryMode = intent.DeliveryMode
		event.replyLineBreakMode = intent.LineBreakMode
		if got := splitEventChatReply(body, effective, event); !reflect.DeepEqual(got, []string{"甲\n乙", "丙"}) {
			t.Fatalf("override failed: %q", got)
		}
		if strings.Contains(body, "[[DIANA_") {
			t.Fatal("metadata leaked")
		}
	}
}

func TestDisabledMultiMessageSkipsForwardCard(t *testing.T) {
	withFastSendTiming(t)
	channel := &recordingChannel{}
	cfg := BotConfig{NaturalReplySplitEnabled: boolPointer(false), ForwardReplyThreshold: 1, ForwardReplyChunkThreshold: 1}
	r := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "u", SelfID: "42"}
	if _, err := r.sendDecorated(context.Background(), event, "第一句[diana-br]第二句", outboundDecoration{}); err != nil {
		t.Fatal(err)
	}
	if len(channel.sentSnapshot()) != 1 || len(channel.callsSnapshot()) != 0 {
		t.Fatal("disabled split triggered card")
	}
}
