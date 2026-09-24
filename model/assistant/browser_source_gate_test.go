// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/browsersource"
)

// 每一轮只交出一个浏览器：当前来源是哪个，另一个的句柄就不交出去，CDP 工具也跟着内置浏览器走。
func TestBrowserSourceKeepsOnlyOneBrowser(t *testing.T) {
	runtime := &Runtime{}
	runtime.SetBrowserBox(stubBuiltinBrowser{url: "http://127.0.0.1:1234"})
	runtime.SetBrowserControl(stubBrowserControl{})
	source := browsersource.Box
	runtime.SetBrowserSource(func() string { return source })
	cfg := DefaultBotConfig()
	cfg.AgentBrowserControlEnabled = true

	if runtime.browserBoxFor(cfg) == nil || runtime.browserToolsDisabledFor(cfg) {
		t.Fatal("选内置浏览器时应交出内置浏览器并登记 CDP 工具")
	}
	if runtime.browserControlFor(cfg) != nil {
		t.Fatal("选内置浏览器时不该交出扩展控制面")
	}

	source = browsersource.Extension
	if runtime.browserControlFor(cfg) == nil {
		t.Fatal("选扩展时应交出扩展控制面")
	}
	if runtime.browserBoxFor(cfg) != nil || !runtime.browserToolsDisabledFor(cfg) {
		t.Fatal("选扩展时不该交出内置浏览器，也不该登记 CDP 工具")
	}

	source = browsersource.Off
	if runtime.browserBoxFor(cfg) != nil || runtime.browserControlFor(cfg) != nil || !runtime.browserToolsDisabledFor(cfg) {
		t.Fatal("关闭时两边都不该交出，CDP 工具也不登记")
	}
}

// 机器人自己配的外部 CDP 地址是显式指定的浏览器，不跟着全局来源走。
func TestBrowserSourceKeepsExplicitCDPURL(t *testing.T) {
	runtime := &Runtime{}
	runtime.SetBrowserSource(func() string { return browsersource.Off })
	cfg := DefaultBotConfig()
	cfg.AgentBrowserCDPURL = "http://10.0.0.5:9222"
	if runtime.browserToolsDisabledFor(cfg) {
		t.Fatal("配了外部 CDP 地址时不该收走 CDP 工具")
	}
}

// 没注入来源时保持旧行为：两边都按各自的开关。
func TestBrowserSourceUnsetKeepsLegacyBehavior(t *testing.T) {
	runtime := &Runtime{}
	runtime.SetBrowserBox(stubBuiltinBrowser{url: "http://127.0.0.1:1234"})
	runtime.SetBrowserControl(stubBrowserControl{})
	cfg := DefaultBotConfig()
	cfg.AgentBrowserControlEnabled = true
	if runtime.browserBoxFor(cfg) == nil || runtime.browserControlFor(cfg) == nil || runtime.browserToolsDisabledFor(cfg) {
		t.Fatal("没注入来源时应保持旧行为")
	}
}
