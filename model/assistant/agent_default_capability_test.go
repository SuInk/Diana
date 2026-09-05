// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 新建配置装完就能用：命令白名单带默认值，写入默认打开。
func TestDefaultBotConfigShipsUsableAgentCapabilities(t *testing.T) {
	cfg := DefaultBotConfig()
	if len(cfg.AgentCommandAllowlist) == 0 {
		t.Fatal("新建配置的命令白名单是空的，run_command 不会注册")
	}
	if !cfg.AgentFileWriteEnabled {
		t.Fatal("新建配置没有打开写入")
	}
	registry, err := agent.NewDefaultToolRegistry((&Runtime{}).agentRegistryConfig(cfg.WithDefaults(), MessageEvent{}, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run_command", "write_file", "edit_file", "grep", "find_files"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("默认配置下 %s 没有注册", name)
		}
	}
}

// 默认白名单里不能出现能读任意路径、能出网、能写盘的命令——没有沙盒时它们
// 就是以本进程权限直接跑的，白名单只管得到「能不能跑」，管不到「能碰什么」。
func TestDefaultCommandAllowlistStaysReadOnly(t *testing.T) {
	banned := map[string]string{
		"cat": "能读任意路径", "ls": "能列任意目录", "head": "能读任意路径", "tail": "能读任意路径",
		"find": "能遍历任意路径", "grep": "能读任意路径",
		"curl": "能出网", "wget": "能出网", "nc": "能出网", "ssh": "能出网",
		"ps":  "会带上别的进程的完整命令行",
		"git": "能读仓库也能出网", "rm": "会改磁盘", "mv": "会改磁盘", "sh": "是任意执行", "bash": "是任意执行",
	}
	for _, command := range agent.DefaultCommandAllowlist() {
		if reason, bad := banned[command]; bad {
			t.Fatalf("默认白名单里出现了 %q：%s", command, reason)
		}
	}
	if len(agent.DefaultCommandAllowlist()) == 0 {
		t.Fatal("默认白名单不该是空的")
	}
}

// 这条是整件事的安全前提：改默认值只能影响新建配置，不能让已经在跑的部署
// 在升级后凭空多出命令执行和文件写入——当初特意留空的必须仍然是空的。
func TestWithDefaultsDoesNotGrantCapabilitiesToStoredConfigs(t *testing.T) {
	// 两种形态都要测：字段带 omitempty，存量记录里空白名单多半是「整个字段不存在」，
	// 反序列化出来是 nil 而不是空切片。只测空切片会漏掉真正常见的那一种。
	for name, allowlist := range map[string][]string{
		"字段缺失（nil）": nil,
		"空数组":       {},
	} {
		t.Run(name, func(t *testing.T) {
			stored := BotConfig{
				AgentEnabled:          true,
				AgentCommandAllowlist: allowlist,
				AgentFileWriteEnabled: false,
			}.WithDefaults()

			if len(stored.AgentCommandAllowlist) != 0 {
				t.Fatalf("存量配置的空白名单被默认值填上了: %#v", stored.AgentCommandAllowlist)
			}
			if stored.AgentFileWriteEnabled {
				t.Fatal("存量配置的写入开关被默认值打开了")
			}

			registry, err := agent.NewDefaultToolRegistry((&Runtime{}).agentRegistryConfig(stored, MessageEvent{}, false))
			if err != nil {
				t.Fatal(err)
			}
			for _, tool := range []string{"run_command", "write_file", "edit_file"} {
				if _, ok := registry.Get(tool); ok {
					t.Fatalf("存量配置升级后凭空多出了 %s", tool)
				}
			}
		})
	}
}

// 用户自己填过的白名单不会被默认值覆盖或追加。
func TestWithDefaultsKeepsAnExplicitAllowlist(t *testing.T) {
	cfg := BotConfig{AgentCommandAllowlist: []string{"uptime"}}.WithDefaults()
	if len(cfg.AgentCommandAllowlist) != 1 || cfg.AgentCommandAllowlist[0] != "uptime" {
		t.Fatalf("用户填的白名单被改动了: %#v", cfg.AgentCommandAllowlist)
	}
}
