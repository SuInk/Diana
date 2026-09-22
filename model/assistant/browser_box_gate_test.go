// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

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
	for _, name := range []string{"browser_open", "browser_text", "browser_click", "browser_type", "browser_screenshot"} {
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

func (s stubBuiltinBrowser) AgentCDPURL() string { return s.url }
func (s stubBuiltinBrowser) Unavailable() string { return "" }
