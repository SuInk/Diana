// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

// 开关要能从 WebUI 存进配置再读回来。配置在这一层是逐字段抄的，
// 漏抄一个字段的表现是「WebUI 上点了，保存后又变回关着」，界面上看不出原因。
func TestBrowserBoxSwitchSurvivesPayloadRoundTrip(t *testing.T) {
	cfg := DefaultBotConfig()
	cfg.AgentBrowserBoxEnabled = true

	payload := PayloadFromConfig(cfg)
	if !payload.AgentBrowserBoxEnabled {
		t.Fatal("配置转 payload 时丢了内置浏览器开关")
	}
	restored := ConfigFromPayload(payload, cfg)
	if !restored.AgentBrowserBoxEnabled {
		t.Fatal("payload 转回配置时丢了内置浏览器开关")
	}
}

// 关着的时候不该把句柄交出去：那等于绕过了「逐台机器人显式打开」。
func TestBrowserBoxBridgeRequiresPerBotSwitch(t *testing.T) {
	runtime := &Runtime{}
	runtime.SetBrowserBox(stubBuiltinBrowser{url: "http://127.0.0.1:1234"})

	off := DefaultBotConfig()
	if runtime.browserBoxFor(off) != nil {
		t.Fatal("机器人没开这一档时不该拿到内置浏览器")
	}
	on := DefaultBotConfig()
	on.AgentBrowserBoxEnabled = true
	if runtime.browserBoxFor(on) == nil {
		t.Fatal("机器人开了这一档就该拿到内置浏览器")
	}
}

type stubBuiltinBrowser struct{ url string }

func (s stubBuiltinBrowser) AgentCDPURL() string { return s.url }
func (s stubBuiltinBrowser) Unavailable() string { return "" }
