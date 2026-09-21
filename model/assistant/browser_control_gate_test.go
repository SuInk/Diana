// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/browserctl"
)

type stubBrowserControl struct{}

func (stubBrowserControl) Ready() bool { return true }

func (stubBrowserControl) Dispatch(context.Context, browserctl.Command) (browserctl.Result, error) {
	return browserctl.Result{OK: true}, nil
}

func TestBrowserControlGateRequiresBothSwitches(t *testing.T) {
	runtime := &Runtime{}
	enabled := BotConfig{AgentBrowserControlEnabled: true}
	disabled := BotConfig{}

	// 没注入控制面：哪台机器人开了都拿不到。
	if runtime.browserControlFor(enabled) != nil {
		t.Fatal("没有控制面时不该交出句柄")
	}

	runtime.SetBrowserControl(stubBrowserControl{})
	if runtime.browserControlFor(disabled) != nil {
		t.Fatal("机器人那档没开时不该交出句柄")
	}
	if runtime.browserControlFor(enabled) == nil {
		t.Fatal("两边都开时应交出句柄")
	}
}

func TestBrowserControlGateDefaultsOff(t *testing.T) {
	cfg := BotConfig{}.WithDefaults()
	if cfg.AgentBrowserControlEnabled {
		t.Fatal("浏览器控制默认必须关闭")
	}
	// 新建机器人的推荐默认值同样不该顺手打开这一档。
	if DefaultBotConfig().AgentBrowserControlEnabled {
		t.Fatal("新建机器人的默认值不该打开浏览器控制")
	}
}

func TestBrowserControlGateSurvivesPayloadRoundTrip(t *testing.T) {
	cfg := BotConfig{AgentBrowserControlEnabled: true}.WithDefaults()
	payload := PayloadFromConfig(cfg)
	if !payload.AgentBrowserControlEnabled {
		t.Fatal("配置转 payload 时丢了浏览器控制开关")
	}
	restored := ConfigFromPayload(payload, cfg)
	if !restored.AgentBrowserControlEnabled {
		t.Fatal("payload 转配置时丢了浏览器控制开关")
	}
}

func TestAgentRegistryConfigCarriesBridgeOnlyWhenEnabled(t *testing.T) {
	runtime := &Runtime{}
	runtime.SetBrowserControl(stubBrowserControl{})
	var _ agent.BrowserControlBridge = stubBrowserControl{}

	off := runtime.agentRegistryConfig(BotConfig{}.WithDefaults(), MessageEvent{}, false)
	if off.BrowserControl != nil {
		t.Fatal("机器人没开这一档时，注册表配置里不该带控制面")
	}
	on := runtime.agentRegistryConfig(BotConfig{AgentBrowserControlEnabled: true}.WithDefaults(), MessageEvent{}, false)
	if on.BrowserControl == nil {
		t.Fatal("机器人开了这一档时，注册表配置里应带上控制面")
	}
}
