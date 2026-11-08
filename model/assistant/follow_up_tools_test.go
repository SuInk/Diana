// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
)

type followUpStubTool struct{ name string }

func (t followUpStubTool) Name() string        { return t.name }
func (t followUpStubTool) Description() string { return t.name }
func (t followUpStubTool) Run(context.Context, map[string]any) (string, error) {
	return "ok", nil
}

func TestFollowUpRegistryKeepsOnlyReadOnlyTools(t *testing.T) {
	registry := agent.NewToolRegistry()
	for _, name := range []string{agent.WebSearchToolName, sandboxedBrowserToolName, dianaChatHistoryToolName, dianaGitHubToolName, "run_command", "write_file", dianaCrossSessionToolName, "memory"} {
		registry.Register(followUpStubTool{name: name})
	}
	restrictFollowUpRegistry(registry)
	for _, name := range []string{"run_command", "write_file", dianaCrossSessionToolName, "memory"} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("跟评不该拿到有副作用的工具 %s", name)
		}
	}
	got := strings.Join(followUpToolNames(registry), ",")
	if got != "web_search,browser_render,chat_history,github" {
		t.Fatalf("只读白名单不对：%s", got)
	}
	if err := registry.OperationDisabledError(dianaGitHubToolName, map[string]any{"operation": "comment"}); err == nil {
		t.Fatal("跟评里 GitHub 写操作必须关掉")
	}
	if err := registry.OperationDisabledError(dianaGitHubToolName, map[string]any{"operation": "get"}); err != nil {
		t.Fatalf("GitHub 读操作应照常可用：%v", err)
	}
}

func TestFollowUpInstructionIntroducesReleases(t *testing.T) {
	reference := "Release v0.9.7 更新说明：\n- 群聊不再频繁 @\n- 新增本地群"
	text := followUpInstructionWithTools("GitHub 动态：SuInk/Diana\nRelease v0.9.7", reference, true)
	if !strings.Contains(text, promptFollowUpRelease) {
		t.Fatal("带 Release 更新说明时要求介绍版本重点")
	}
	if !strings.Contains(text, promptFollowUpTools) {
		t.Fatal("装上只读工具时要告诉模型可以先查")
	}
	plain := followUpInstructionWithTools("GitHub 动态：SuInk/Diana\nCommit abc", "Commit abc 改动：...", false)
	if strings.Contains(plain, promptFollowUpRelease) || strings.Contains(plain, promptFollowUpTools) {
		t.Fatal("普通提交、没装工具时不追加版本介绍和工具说明")
	}
}
