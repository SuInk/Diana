// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

type describedTestTool struct{ name, description string }

func (t describedTestTool) Name() string        { return t.name }
func (t describedTestTool) Description() string { return t.description }
func (describedTestTool) Run(context.Context, map[string]any) (string, error) {
	return `{"status":"ok"}`, nil
}

// 线上模型把 repository_issues 编成过 github_issue、issue、issue_create、repository_publish。
func TestSuggestToolNamesFindsRealToolForGuessedNames(t *testing.T) {
	registry := NewToolRegistry(
		describedTestTool{"repository_issues", "查看和起草 GitHub 仓库的 issue"},
		describedTestTool{"repository_watch", "关注仓库的 release 和 star"},
		describedTestTool{"browser_render", "渲染网页"},
		describedTestTool{"capabilities", "列出机器人能力"},
	)
	cases := map[string][]string{
		"github_issue":                {"repository_issues"},
		"issue":                       {"repository_issues"},
		"diana.issue_create":          {"repository_issues"},
		"repository_publish":          {"repository_issues", "repository_watch"},
		"official.repository-publish": {"repository_issues", "repository_watch"},
		"list_capabilities":           {"capabilities"},
		"github":                      {"repository_issues"},
		"bot-protocol":                {},
	}
	for requested, want := range cases {
		got := suggestToolNames(registry, requested)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("suggestToolNames(%q) = %v, want %v", requested, got, want)
		}
	}
}

func TestToolsLoadUnknownNameListsSuggestions(t *testing.T) {
	registry := NewToolRegistry(&countingTool{name: "common"}, describedTestTool{"repository_issues", "GitHub issue"})
	loader := newDeferredToolLoader(registry, []string{"common"})
	_, err := loader.Run(context.Background(), map[string]any{"names": []any{"github_issues"}})
	if err == nil || !strings.Contains(err.Error(), "不存在或已禁用") || !strings.Contains(err.Error(), `"repository_issues"`) {
		t.Fatalf("error = %v", err)
	}
}

// 没权限的工具不能出现在候选里：列出来等于把名字漏给模型，它照着调又撞权限。
func TestSuggestToolNamesSkipsDeniedTools(t *testing.T) {
	base := NewToolRegistry(&countingTool{name: "common"}, &MCPTool{serverName: "probe", modelName: "mcp__probe__ping"})
	view, err := base.NewView(Config{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	view.Retain(map[string]bool{"common": true})
	if got := suggestToolNames(view, "probe_ping"); len(got) != 0 {
		t.Fatalf("suggested denied tool: %v", got)
	}
}
