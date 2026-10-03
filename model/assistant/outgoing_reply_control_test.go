package assistant

import (
	"context"
	"strings"
	"testing"
)

func TestOutgoingReplyControlModesAndInvalidTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   ReplyDecorationMode
		prefix string
		wantID string
	}{
		{"auto historical target", ReplyDecorationAuto, "[diana-reply:106019]", "106019"},
		{"on ignores historical target", ReplyDecorationOn, "[diana-reply:106019]", "106020"},
		{"off ignores valid target", ReplyDecorationOff, "[diana-reply:106019]", ""},
		{"unknown alias", ReplyDecorationAuto, "[diana-reply:im_message_702fc1baad94]", ""},
		{"missing target", ReplyDecorationAuto, "[diana-reply:999999]", ""},
		{"cross session target", ReplyDecorationAuto, "[diana-reply:106018]", ""},
		{"invalid numeric target", ReplyDecorationAuto, "[diana-reply:106020oops]", ""},
		{"multiple targets", ReplyDecorationAuto, "[diana-reply:106019][diana-reply:106020]", ""},
		{"whitespace", ReplyDecorationAuto, " \n[diana-reply:106019] ", "106019"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := NewRuntime(BotConfig{ReplyReferenceMode: tc.mode}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
			event := MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "10001", MessageID: "106020"}
			runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "group-1", UserID: "10001", MessageID: "106019", RawMessage: "old"})
			runtime.remember(MessageEvent{Kind: EventKindGroup, GroupID: "group-2", UserID: "10001", MessageID: "106018", RawMessage: "other group"})
			msg := runtime.applyOutgoingReplyMarker(context.Background(), event, OutgoingMessage{
				Text: tc.prefix + "正文保留", ReplyMessageID: event.MessageID,
			})
			if msg.Text != "正文保留" || msg.ReplyMessageID != tc.wantID {
				t.Fatalf("text=%q reply=%q, want %q", msg.Text, msg.ReplyMessageID, tc.wantID)
			}
		})
	}
}

func TestReplyAliasRestorationRequiresExactIdentifier(t *testing.T) {
	scope := newIdentityPrivacyScope()
	alias := scope.registerMessageID("106020")
	for _, text := range []string{alias + "7", alias + "_stale", "im_message_702fc1baad94"} {
		input := replyMarkerPrefix + text + "]正文"
		if got := scope.restoreText(input); got != input {
			t.Fatalf("unknown alias partially restored: %q -> %q", input, got)
		}
	}
	if got := scope.restoreText(replyMarkerPrefix + alias + "]正文"); got != "[diana-reply:106020]正文" {
		t.Fatalf("known alias not restored: %q", got)
	}
	// A late sending path can still resolve a known alias from the same scope.
	runtime := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g", UserID: "10001", MessageID: "106020"}
	for _, value := range []string{alias, alias + "7"} {
		msg := runtime.applyOutgoingReplyMarker(withIdentityPrivacyScope(context.Background(), scope), event,
			OutgoingMessage{Text: replyMarkerPrefix + value + "]正文"})
		want := ""
		if value == alias {
			want = event.MessageID
		}
		if msg.ReplyMessageID != want || strings.Contains(msg.Text, "im_") || msg.Text != "正文" {
			t.Fatalf("alias=%q result=%#v", value, msg)
		}
	}
}

// qqOfficialProbeID 是 QQ 官方 d.id 的真实形态：ROBOT1.0_ 前缀，字母数字加点、
// 下划线、连字符、感叹号（base64 系符号）。
const qqOfficialProbeID = "ROBOT1.0_r9EgT7Tp3-ckkhsU6Z3mKHt.xuRyI.weUjL5a88B.Y1GWIyMA9GFtSVtRy7ytn612jIpW1u5oCyJP9ghG2IfA.pYgaf4C5XyaLdtdKVzqgg!"

// QQ 官方的消息 ID 不是纯数字，数字校验会把它整个拦掉——这正是出站引用失效的
// 根因。auto 档下模型按提示词写出的标记必须原样落成 ReplyMessageID。
func TestOutgoingReplyControlAcceptsQQOfficialMarker(t *testing.T) {
	runtime := NewRuntime(BotConfig{ReplyReferenceMode: ReplyDecorationAuto}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	event := MessageEvent{
		Platform: PlatformQQOfficial, Kind: EventKindGroup,
		GroupID: "g-qq", UserID: "member-1", MessageID: qqOfficialProbeID,
	}
	msg := runtime.applyOutgoingReplyMarker(context.Background(), event, OutgoingMessage{
		Text:           replyMarkerPrefix + qqOfficialProbeID + "]就是这条",
		ReplyMessageID: qqOfficialProbeID,
	})
	if msg.ReplyMessageID != qqOfficialProbeID || msg.Text != "就是这条" {
		t.Fatalf("qq official marker dropped: %#v", msg)
	}

	// 引用历史里的旧消息：历史按 d.id 存储，同样要认。
	old := "ROBOT1.0_0wt33G5adbvP5OLbYGAoW.OLDMSG-ID!"
	runtime.remember(MessageEvent{Platform: PlatformQQOfficial, Kind: EventKindGroup, GroupID: "g-qq", UserID: "member-1", MessageID: old, RawMessage: "旧消息"})
	msg = runtime.applyOutgoingReplyMarker(context.Background(), event, OutgoingMessage{
		Text:           replyMarkerPrefix + old + "]翻到这条了",
		ReplyMessageID: qqOfficialProbeID,
	})
	if msg.ReplyMessageID != old || msg.Text != "翻到这条了" {
		t.Fatalf("qq official historical marker dropped: %#v", msg)
	}

	// 本会话查不到的目标只剥标记照发，不指向空消息。
	msg = runtime.applyOutgoingReplyMarker(context.Background(), event, OutgoingMessage{
		Text:           replyMarkerPrefix + "ROBOT1.0_not-in-history!]找不到就算了",
		ReplyMessageID: qqOfficialProbeID,
	})
	if msg.ReplyMessageID != "" || msg.Text != "找不到就算了" {
		t.Fatalf("missing qq official target = %#v", msg)
	}
}

// 平台感知只给 QQ 官方开口子：其余平台（含没有 Platform 的旧事件，按 OneBot
// 语义处理）仍走纯数字校验，数字尾巴、别名、编造的 id 照旧全部拒绝。
func TestReplyMarkerIDAcceptableKeepsOtherPlatformsNumeric(t *testing.T) {
	for _, platform := range []string{"", PlatformOneBotV11, PlatformTelegram, PlatformFeishu} {
		if replyMarkerIDAcceptable(platform, qqOfficialProbeID) {
			t.Fatalf("platform %q must not accept the qq official id", platform)
		}
		if !replyMarkerIDAcceptable(platform, "1244393238") {
			t.Fatalf("platform %q must accept numeric ids as before", platform)
		}
	}
	for _, id := range []string{"im_message_702fc1baad94", "[diana-reply:30006]", "who am i"} {
		if replyMarkerIDAcceptable(PlatformQQOfficial, id) {
			t.Fatalf("qq official must reject unusable payload %q", id)
		}
	}
	// 字符集合法但历史里查不到的载荷（数字尾巴混字母）语法上放行——能不能真发
	// 由 lookupQuotedMessage 的历史回查把关，上面 applyOutgoingReplyMarker 的
	// missing 用例已经验证过查不到时只剥标记照发。
	if !replyMarkerIDAcceptable(PlatformQQOfficial, "106020oops") {
		t.Fatal("qq official accepts well-formed but unknown payloads at the syntax gate")
	}
	if !replyMarkerIDAcceptable(PlatformQQOfficial, qqOfficialProbeID) {
		t.Fatal("qq official must accept its own message id form")
	}
}

// 模型写歪的 QQ 官方标记（外壳、分隔符不对）也要能扶正，残骸要能清掉。
func TestOutgoingNormalizesQQOfficialReplyMarkerVariants(t *testing.T) {
	got := normalizeDianaReplyVariants("[ diana-reply : " + qqOfficialProbeID + " ]喵？这张图这次没送进我眼里")
	if want := replyMarkerPrefix + qqOfficialProbeID + "]喵？这张图这次没送进我眼里"; got != want {
		t.Fatalf("输入 %q\n得到 %q\n期望 %q", "[ diana-reply : "+qqOfficialProbeID+" ]喵？", got, want)
	}
	if id, _, ok := consumeOutgoingReplyControl(got); !ok || id != qqOfficialProbeID {
		t.Fatalf("扶正后仍然消费不了：%q", got)
	}

	// 数字尾巴不能被截成前几位：长分支在前，纯数字落第二分支，两边都取完整载荷。
	if id, _, ok := consumeOutgoingReplyControl(normalizeDianaReplyVariants("[diana-reply:106020oops]")); !ok || id != "106020oops" {
		t.Fatalf("mixed payload must be kept whole, got id=%q ok=%v", id, ok)
	}

	// 正文中间的 QQ 官方长标记残骸（发送层只认开头那一个）要整段清掉。
	residue := "这句话里 [diana-reply:" + qqOfficialProbeID + "] 混了个引用标记"
	if got := dropResidualDianaReplyMarkers(residue); got != "这句话里 混了个引用标记" {
		t.Fatalf("residue survived: %q", got)
	}
}
