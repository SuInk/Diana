// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 沙盒模式要真的从机器人配置走到 agent.Config。这两个字段以前在 agent.Config 里
// 存在但没有任何地方赋值，于是永远是 auto，require 模式接不上——这条用例就是钉住
// 那根线还在。
func TestAgentRegistryConfigCarriesTheSandboxSettings(t *testing.T) {
	runtime := &Runtime{}
	cfg := BotConfig{
		AgentCommandAllowlist:           []string{"uptime"},
		AgentCommandSandbox:             agent.CommandSandboxRequire,
		AgentCommandSandboxAllowNetwork: true,
	}.WithDefaults()

	agentCfg := runtime.agentRegistryConfig(cfg, MessageEvent{}, false)
	if agentCfg.CommandSandbox != agent.CommandSandboxRequire {
		t.Fatalf("CommandSandbox = %q, want require", agentCfg.CommandSandbox)
	}
	if !agentCfg.CommandSandboxAllowNetwork {
		t.Fatal("CommandSandboxAllowNetwork did not reach the agent config")
	}
}

// 未知值按 auto 归一化，不是静默关掉：配置写错不该表现为「沙盒没了」。
func TestWithDefaultsNormalizesTheSandboxMode(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", agent.CommandSandboxAuto},
		{"  REQUIRE ", agent.CommandSandboxRequire},
		{"off", agent.CommandSandboxOff},
		{"绝对安全", agent.CommandSandboxAuto},
	} {
		cfg := BotConfig{AgentCommandSandbox: tc.in}.WithDefaults()
		if cfg.AgentCommandSandbox != tc.want {
			t.Fatalf("WithDefaults(%q) = %q, want %q", tc.in, cfg.AgentCommandSandbox, tc.want)
		}
	}
}

// 配置往返不能把这两项丢掉：控制台改完保存一次就复原，等于开关是坏的。
func TestConfigPayloadRoundTripKeepsTheSandboxSettings(t *testing.T) {
	cfg := BotConfig{
		AgentCommandSandbox:             agent.CommandSandboxRequire,
		AgentCommandSandboxAllowNetwork: true,
	}.WithDefaults()

	restored := ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{})
	if restored.AgentCommandSandbox != agent.CommandSandboxRequire || !restored.AgentCommandSandboxAllowNetwork {
		t.Fatalf("round trip lost the sandbox settings: %q / %v",
			restored.AgentCommandSandbox, restored.AgentCommandSandboxAllowNetwork)
	}
}
