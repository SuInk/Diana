// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"sort"
	"testing"
)

// 这个文件盯的是「某个平台悄悄少实现了一样东西」。
//
// 测试一直是按平台切的：telegram_test.go 测 Telegram、feishu_test.go 测飞书，
// 各测各的。这种切法测得出「我实现的那部分对不对」，测不出「我压根没实现」——
// 一个平台漏掉某项能力时，没有任何一个用例会红。Telegram 缺 ResultChannel 就是
// 这么一路漏到线上的：出站消息以空 message_id 入库，别人引用 Diana 时回查落空，
// 而所有 Telegram 用例都是绿的。
//
// 所以这里把能力横过来列成一张表：每个平台必须表态支持什么。新增平台时表里没有
// 对应行就直接失败，逼着人做决定，而不是默默继承「什么都没实现」。

// platformCapabilities 是一个平台在运行时应当具备的能力。
type platformCapabilities struct {
	// ResultChannel 表示出站要能拿回平台侧的 message_id。
	//
	// 它是「别人引用 Diana 的消息，Diana 知道自己说过什么」的前提：出站消息带着
	// 平台 ID 入库，引用回查才对得上。只有当平台的入站事件确实带引用关系、且出站
	// 返回的 ID 与入站 ID 同属一个空间时才要求实现——把另一个空间的值记进去比不记更糟。
	ResultChannel bool
	// RichText 表示出站适配器能把 Markdown 渲染出来。
	RichText bool
	// InboundQuote 表示入站事件里带得到引用关系。
	InboundQuote bool
}

// 每个平台的能力声明。改这张表之前先确认适配器真的做得到，别为了让测试变绿而改声明。
var expectedPlatformCapabilities = map[string]platformCapabilities{
	PlatformOneBotV11: {ResultChannel: true, RichText: false, InboundQuote: true},
	PlatformTelegram:  {ResultChannel: true, RichText: true, InboundQuote: true},
	PlatformQQOfficial: {
		// 开放平台发送后返回 id，入站的 message_reference.message_id 与之同空间。
		ResultChannel: true, RichText: false, InboundQuote: true,
	},
	PlatformFeishu: {
		// 发送返回 data.message_id，入站的 ParentID 与之同属 om_ 空间。
		ResultChannel: true, RichText: true, InboundQuote: true,
	},
	PlatformDingTalk: {
		// 钉钉的入站回调里没有引用关系，主动推送也只返回 processQueryKey，
		// 那不是消息 ID。记下来只会往历史里塞一个对不上的值，所以不实现。
		ResultChannel: false, RichText: true, InboundQuote: false,
	},
	PlatformWeCom: {
		// 企业微信入站同样没有引用概念；message/send 返回的 msgid 是撤回句柄，
		// 和入站 MsgID 不是一个空间。
		ResultChannel: false, RichText: true, InboundQuote: false,
	},
}

// 新增平台却忘了在能力表里表态时，这里先红。
func TestPlatformCapabilityMatrixCoversEveryPlatform(t *testing.T) {
	declared := make([]string, 0, len(expectedPlatformCapabilities))
	for id := range expectedPlatformCapabilities {
		declared = append(declared, id)
	}
	sort.Strings(declared)

	registered := make([]string, 0, len(SupportedPlatforms()))
	for _, def := range SupportedPlatforms() {
		registered = append(registered, def.ID)
		if _, ok := expectedPlatformCapabilities[def.ID]; !ok {
			t.Errorf("平台 %q 没有在能力表里表态——新增平台时必须逐项声明支持什么", def.ID)
		}
	}
	sort.Strings(registered)
	for _, id := range declared {
		if _, ok := PlatformByID(id); !ok {
			t.Errorf("能力表里的 %q 已经不是受支持的平台了，该删掉", id)
		}
	}
}

// 富文本能力位必须和注册表一致：两处各写一份，改的时候必然漏一边。
func TestPlatformCapabilityMatrixMatchesRichTextRegistry(t *testing.T) {
	for id, want := range expectedPlatformCapabilities {
		if got := PlatformSupportsRichText(id); got != want.RichText {
			t.Errorf("%s 富文本能力：注册表 = %v，能力表 = %v", id, got, want.RichText)
		}
	}
}

// 出站能不能拿回 message_id，逐个平台对着声明验。
func TestPlatformCapabilityMatrixResultChannel(t *testing.T) {
	for id, want := range expectedPlatformCapabilities {
		channel := newCapabilityProbeChannel(t, id)
		if channel == nil {
			continue
		}
		_, got := channel.(ResultChannel)
		if got == want.ResultChannel {
			continue
		}
		if want.ResultChannel {
			t.Errorf("%s 应当实现 ResultChannel：否则出站消息以空 message_id 入库，"+
				"别人引用 Diana 时回查必然落空", id)
			continue
		}
		t.Errorf("%s 实现了 ResultChannel，但能力表声明它不该实现。"+
			"如果平台确实能给出与入站同空间的消息 ID，请改能力表并写明依据", id)
	}
}

// newCapabilityProbeChannel 造一个仅用于探测能力的通道实例。
// OneBot 由调用方注入、工厂造不出来，单独处理。
func newCapabilityProbeChannel(t *testing.T, platform string) Channel {
	t.Helper()
	switch platform {
	case PlatformOneBotV11:
		return NewOneBotChannel(OneBotConfig{})
	case PlatformTelegram:
		return NewChannelForConfig(BotConfig{Platform: platform, TelegramBotToken: "t"})
	case PlatformQQOfficial:
		return NewChannelForConfig(BotConfig{Platform: platform, QQAppID: "a", QQAppSecret: "s"})
	case PlatformDingTalk:
		return NewChannelForConfig(BotConfig{Platform: platform, DingTalkClientID: "a", DingTalkClientSecret: "s"})
	case PlatformFeishu:
		return NewChannelForConfig(BotConfig{Platform: platform, FeishuAppID: "a", FeishuAppSecret: "s"})
	case PlatformWeCom:
		return NewChannelForConfig(BotConfig{Platform: platform, WeComCorpID: "c", WeComAgentID: "1", WeComSecret: "s"})
	}
	t.Errorf("平台 %q 没有对应的探测构造，能力矩阵覆盖不到它", platform)
	return nil
}
