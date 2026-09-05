// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 写入开关要真的从机器人配置走到 agent.Config。
func TestAgentRegistryConfigCarriesTheFileWriteSwitch(t *testing.T) {
	runtime := &Runtime{}
	for _, enabled := range []bool{false, true} {
		cfg := BotConfig{AgentFileWriteEnabled: enabled}.WithDefaults()
		if got := runtime.agentRegistryConfig(cfg, MessageEvent{}, false).FileWriteEnabled; got != enabled {
			t.Fatalf("AgentFileWriteEnabled=%v reached agent config as %v", enabled, got)
		}
	}
}

func TestConfigPayloadRoundTripKeepsTheFileWriteSwitch(t *testing.T) {
	cfg := BotConfig{AgentFileWriteEnabled: true}.WithDefaults()
	if !ConfigFromPayload(PayloadFromConfig(cfg), BotConfig{}).AgentFileWriteEnabled {
		t.Fatal("round trip lost the file write switch")
	}
}

// 本地文件工具一个都不该出现在非主人的白名单里。
//
// 身份这一层挡的是「谁的会话里能出现这些工具」。它挡不住提示注入——群里别人的
// 消息和主人的消息在同一个上下文里，模型是读完那些之后才决定调用的——所以配置
// 那一层（写入开关、命令白名单）才是真正的边界。但这一层仍然必须在：少了它，
// 任何人跟机器人说话都能让它读写宿主机上的文件。
func TestLocalFileToolsAreOwnerOnly(t *testing.T) {
	allowed := RelationshipPolicy{}.allowedAgentToolNames()
	if allowed == nil {
		t.Fatal("a non-owner policy returned the unfiltered (owner) allowlist")
	}
	for _, name := range []string{"read_file", "list_files", "grep", "find_files", "write_file", "edit_file", "run_command"} {
		if allowed[name] {
			t.Fatalf("%s is reachable by non-owners", name)
		}
	}
	// 反过来确认主人这一侧不过滤，否则上面那条会因为「大家都没有」而假通过。
	if (RelationshipPolicy{Owner: true}).allowedAgentToolNames() != nil {
		t.Fatal("the owner policy is no longer unfiltered; this test's premise changed")
	}
}

// 关掉写入开关时，注册表里不该有写工具，但只读那几个必须还在。
func TestOwnerRegistryHonoursTheFileWriteSwitch(t *testing.T) {
	runtime := &Runtime{}
	for _, enabled := range []bool{false, true} {
		cfg := BotConfig{AgentFileWriteEnabled: enabled}.WithDefaults()
		registry, err := agent.NewDefaultToolRegistry(runtime.agentRegistryConfig(cfg, MessageEvent{}, false))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"write_file", "edit_file"} {
			if _, ok := registry.Get(name); ok != enabled {
				t.Fatalf("AgentFileWriteEnabled=%v: %s registered = %v", enabled, name, ok)
			}
		}
		for _, name := range []string{"grep", "find_files"} {
			if _, ok := registry.Get(name); !ok {
				t.Fatalf("AgentFileWriteEnabled=%v: read-only %s went missing", enabled, name)
			}
		}
	}
}
