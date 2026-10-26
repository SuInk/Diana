// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 新建配置装完就能用：主人有 run_command，写入默认打开，标准模式下直接生效
// （这两项在安全模式下不挂，见 agent_safe_mode_test.go）。成员拿不到 run_command。
func TestDefaultBotConfigShipsUsableAgentCapabilities(t *testing.T) {
	cfg := standardModeBotConfig()
	if !cfg.AgentFileWriteEnabled {
		t.Fatal("新建配置没有打开写入")
	}
	registry, err := agent.NewDefaultToolRegistry((&Runtime{}).agentRegistryConfig(cfg.WithDefaults(), MessageEvent{}, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run_command", "write_file", "edit_file", "grep", "find_files"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("默认配置下 %s 没有注册", name)
		}
	}
	member, err := agent.NewDefaultToolRegistry((&Runtime{}).agentRegistryConfig(cfg.WithDefaults(), MessageEvent{}, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := member.Get("run_command"); ok {
		t.Fatal("成员的注册表里构造了 run_command")
	}
}

// 写入开关的默认值只作用于新建配置：存量部署当初关掉的，升级后仍然是关的。
func TestWithDefaultsDoesNotGrantFileWriteToStoredConfigs(t *testing.T) {
	stored := BotConfig{AgentEnabled: true, AgentFileWriteEnabled: false}.WithDefaults()
	if stored.AgentFileWriteEnabled {
		t.Fatal("存量配置的写入开关被默认值打开了")
	}
	registry, err := agent.NewDefaultToolRegistry((&Runtime{}).agentRegistryConfig(stored, MessageEvent{}, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"write_file", "edit_file"} {
		if _, ok := registry.Get(tool); ok {
			t.Fatalf("存量配置升级后凭空多出了 %s", tool)
		}
	}
}

// 命令白名单已删除：旧配置里的 agent_command_allowlist 读取时忽略（空白名单也不再
// 关掉 run_command），保存时不再输出。
func TestLegacyCommandAllowlistIsIgnored(t *testing.T) {
	for _, raw := range []string{`{"agent_command_allowlist":[]}`, `{"agent_command_allowlist":["uptime"]}`} {
		var cfg BotConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		cfg.AgentMode = AgentModeStandard
		registry, err := agent.NewDefaultToolRegistry((&Runtime{}).agentRegistryConfig(cfg.WithDefaults(), MessageEvent{}, true))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := registry.Get("run_command"); !ok {
			t.Fatalf("%s: 旧白名单仍在影响 run_command", raw)
		}
		body, err := json.Marshal(PayloadFromConfig(cfg.WithDefaults()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "agent_command_allowlist") {
			t.Fatal("removed setting still serialized")
		}
	}
}

func TestLegacyStepSettingIsIgnored(t *testing.T) {
	var cfg BotConfig
	if err := json.Unmarshal([]byte(`{"agent_max_steps":1}`), &cfg); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(cfg.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	for _, owner := range []bool{false, true} {
		if got := runtime.agentRegistryConfig(cfg.WithDefaults(), MessageEvent{}, owner).WithDefaults().MaxSteps; got != 0 {
			t.Fatalf("owner=%v budget=%d", owner, got)
		}
	}
	body, err := json.Marshal(PayloadFromConfig(cfg.WithDefaults()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "agent_max_steps") {
		t.Fatal("removed setting still serialized")
	}
}
