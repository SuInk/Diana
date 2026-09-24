// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 开关要能从 WebUI 存进配置再读回来。配置在这一层是逐字段抄的，
// 漏抄一个字段的表现是「WebUI 上点了，保存后又变回原样」，界面上看不出原因。
func TestBrowserBoxSwitchSurvivesPayloadRoundTrip(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.AgentBrowserBoxDisabled = true

	payload := PayloadFromConfig(cfg)
	if !payload.AgentBrowserBoxDisabled {
		t.Fatal("配置转 payload 时丢了内置浏览器开关")
	}
	restored := ConfigFromPayload(payload, cfg)
	if !restored.AgentBrowserBoxDisabled {
		t.Fatal("payload 转回配置时丢了内置浏览器开关")
	}
}

// 默认就该能用：内置浏览器已经是用户自己在那一页打开的，再要求逐台点一次
// 等于打开了也不能用。
func TestBrowserBoxBridgeOnByDefault(t *testing.T) {
	runtime := &Runtime{}
	runtime.SetBrowserBox(stubBuiltinBrowser{url: "http://127.0.0.1:1234"})

	if runtime.browserBoxFor(DefaultBotConfig()) == nil {
		t.Fatal("默认就该拿到内置浏览器")
	}
	off := DefaultBotConfig()
	off.AgentBrowserBoxDisabled = true
	if runtime.browserBoxFor(off) != nil {
		t.Fatal("显式关掉之后不该再拿到内置浏览器")
	}
}

// 默认开着的前提是身份挡得住：驱动内置浏览器的那组工具不能落进非主人的白名单，
// 否则群成员就能借着主人在浏览器里登录过的账号办事。
func TestBrowserBoxToolsStayOwnerOnly(t *testing.T) {
	allowed := RelationshipPolicy{}.allowedAgentToolNames()
	if allowed == nil {
		t.Fatal("非主人应当拿到一份显式白名单")
	}
	for _, name := range agent.InteractiveBrowserToolNames {
		if allowed[name] {
			t.Fatalf("%s 不该开给非主人：它连的是带登录态的常驻浏览器", name)
		}
	}
	// 一次性无头那条例外：临时 profile、用完即删，不带任何登录态。
	if !allowed["browser_render"] {
		t.Fatal("browser_render 是一次性无头渲染，不该被一起挡掉")
	}
}

type stubBuiltinBrowser struct{ url string }

func (s stubBuiltinBrowser) Endpoint(context.Context) (string, error) { return s.url, nil }

// BrowserFor 让同一个桩同时充当按机器人取浏览器的提供方。
func (s stubBuiltinBrowser) BrowserFor(string) agent.BuiltinBrowserBridge { return s }

// recordingBrowserProvider 记下运行时按哪台机器人取的浏览器。
type recordingBrowserProvider struct{ asked []string }

func (p *recordingBrowserProvider) BrowserFor(botID string) agent.BuiltinBrowserBridge {
	p.asked = append(p.asked, botID)
	return stubBuiltinBrowser{url: "http://127.0.0.1:1234/" + botID}
}

// 每台机器人各用一份登录态：运行时必须按这台机器人自己的 ID 去取浏览器。
func TestBrowserBoxIsPerBot(t *testing.T) {
	provider := &recordingBrowserProvider{}
	runtime := &Runtime{}
	runtime.SetBrowserBox(provider)
	for _, id := range []string{"bot-a", "bot-b"} {
		cfg := DefaultBotConfig()
		cfg.ID = id
		bridge := runtime.browserBoxFor(cfg)
		if bridge == nil {
			t.Fatalf("%s 应拿到内置浏览器", id)
		}
		if url, _ := bridge.Endpoint(context.Background()); url != "http://127.0.0.1:1234/"+id {
			t.Fatalf("%s 拿到的是别人的浏览器：%s", id, url)
		}
	}
	if len(provider.asked) != 2 || provider.asked[0] != "bot-a" || provider.asked[1] != "bot-b" {
		t.Fatalf("运行时没有按机器人取浏览器：%v", provider.asked)
	}
}

// 交互式浏览器按对话分标签页：同一个群的前后几轮接着用同一页，不同群、不同私聊、
// 不同机器人各用各的。
func TestBrowserSessionKeySeparatesConversations(t *testing.T) {
	bot := BotConfig{ID: "bot-a"}
	groupOne := MessageEvent{Kind: EventKindGroup, GroupID: "1", UserID: "owner"}
	if browserSessionKey(bot, groupOne) != browserSessionKey(bot, MessageEvent{Kind: EventKindGroup, GroupID: "1", UserID: "someone"}) {
		t.Fatal("同一个群的不同消息应当接着用同一个标签页")
	}
	distinct := map[string]bool{}
	for _, key := range []string{
		browserSessionKey(bot, groupOne),
		browserSessionKey(bot, MessageEvent{Kind: EventKindGroup, GroupID: "2", UserID: "owner"}),
		browserSessionKey(bot, MessageEvent{Kind: EventKindPrivate, UserID: "owner"}),
		browserSessionKey(BotConfig{ID: "bot-b"}, groupOne),
	} {
		distinct[key] = true
	}
	if len(distinct) != 4 {
		t.Fatalf("不同对话不能共用标签页记录：%v", distinct)
	}
}
