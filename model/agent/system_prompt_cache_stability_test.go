// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"strings"
	"testing"
)

// 系统提示词里唯一会在会话中途增长的是「已加载契约」，它必须待在最末尾。
//
// 系统提示词排在整条 prompt 的最前面，中段插入一段会让后面所有字节整体位移，供应商
// 的前缀缓存从该点起全部作废。线上 prompt_cache_divergence 抓到 purpose=unlabeled、
// segment=system 的分叉六小时 165 次，平均 byte_offset≈20744，可复用前缀 0——正是
// 这一段以前夹在工具目录和 Skills 之间造成的。
//
// 判据只有一条：加载新工具之后，加载前那份提示词必须仍是新提示词的前缀。
func TestLoadedContractsDoNotShiftStablePrefix(t *testing.T) {
	registry := NewToolRegistry(
		&countingTool{name: "alpha"},
		&countingTool{name: "beta"},
		&countingTool{name: "agent_finalize"},
	)
	runner, err := NewRunner(&scriptedClient{}, Config{
		WorkDir:   t.TempDir(),
		CoreTools: []string{"agent_finalize"},
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	// loader 平时在 Run() 里建，这里直接装一个，等价于一轮对话中途的状态。
	runner.loader = newDeferredToolLoader(runner.registry, runner.cfg.CoreTools)
	if runner.loader == nil {
		t.Fatal("延迟加载器未启用，这个测试没有意义")
	}

	before := runner.systemPrompt()
	if strings.Contains(before, "已经加载、可直接通过 tools_execute 调用的完整契约") {
		t.Fatalf("还没加载任何工具就出现了契约段:\n%s", before)
	}

	if _, err := runner.loader.Run(t.Context(), map[string]any{"names": []any{"alpha"}}); err != nil {
		t.Fatalf("加载工具失败: %v", err)
	}
	after := runner.systemPrompt()

	if before == after {
		t.Fatal("加载工具后提示词没有变化，说明契约段没有生效")
	}
	if !strings.HasPrefix(after, before) {
		// 找出第一个分叉位置，报出来便于定位是哪一段被推动了。
		n := len(before)
		if len(after) < n {
			n = len(after)
		}
		i := 0
		for i < n && before[i] == after[i] {
			i++
		}
		t.Fatalf("加载工具推动了前面的内容，缓存前缀在第 %d 字节断开\n之前: %q\n之后: %q",
			i, tail(before, i), tail(after, i))
	}
}

func tail(s string, from int) string {
	if from >= len(s) {
		return ""
	}
	end := from + 120
	if end > len(s) {
		end = len(s)
	}
	return s[from:end]
}

// Skill 的开关按机器人、按群分档，装一个卸一个也会改这份清单。它进系统提示词就意味着
// 换个群、装个 skill 就把整条 prompt 的前缀缓存作废，和上面那一段是同一个坑。
//
// 判据：系统提示词对当前有哪些 skill 完全不敏感。
func TestSkillsCatalogDoesNotEnterStablePrompt(t *testing.T) {
	registry := NewToolRegistry(&SkillsReadTool{}, &countingTool{name: "agent_finalize"})
	runner, err := NewRunner(&scriptedClient{}, Config{WorkDir: t.TempDir()}, registry)
	if err != nil {
		t.Fatal(err)
	}

	empty := runner.systemPrompt()
	registry.SetSkills([]SkillMetadata{
		{Name: "demo-skill", Description: "Use demo.", Path: "/tmp/demo/SKILL.md"},
	})
	loaded := runner.systemPrompt()
	if empty != loaded {
		t.Fatalf("装上 skill 改动了系统提示词，前缀缓存会作废\n之前: %q\n之后: %q", empty, loaded)
	}
	if strings.Contains(loaded, "demo-skill") || strings.Contains(loaded, "/tmp/demo/SKILL.md") {
		t.Fatalf("系统提示词里出现了 skill 名称或路径:\n%s", loaded)
	}
	if catalog := RenderSkillsCatalog(registry.Skills(), 8000); !strings.Contains(catalog, "demo-skill") {
		t.Fatalf("目录没渲染出 skill: %s", catalog)
	}
}
