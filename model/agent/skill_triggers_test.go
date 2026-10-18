// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func writeSkillFile(t *testing.T, dir, frontmatter, body string) string {
	t.Helper()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\n"+frontmatter+"\n---\n\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSkillKeywordsParseBothYAMLForms(t *testing.T) {
	list := filepath.Join(t.TempDir(), "list")
	inline := filepath.Join(t.TempDir(), "inline")
	for _, dir := range []string{list, inline} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSkillFile(t, list, "name: a\ndescription: d\nkeywords:\n  - 点歌\n  - music", "body")
	writeSkillFile(t, inline, "name: b\ndescription: d\nkeywords: 点歌, music , 点歌", "body")

	for _, dir := range []string{list, inline} {
		skills, err := LoadSkills([]string{dir})
		if err != nil || len(skills) != 1 {
			t.Fatalf("dir %s skills = %#v err = %v", dir, skills, err)
		}
		// 重复词去掉，顺序保留：这份列表会出现在界面上。
		if strings.Join(skills[0].Keywords, ",") != "点歌,music" {
			t.Fatalf("dir %s keywords = %#v", dir, skills[0].Keywords)
		}
	}
}

// 命中关键词就把正文带上，不等模型想起来去 read_skill——上下文一长它就是不去读。
func TestKeywordsRemainDiscoveryMetadataOnly(t *testing.T) {
	path := writeSkillFile(t, t.TempDir(), "name: jukebox\ndescription: 点歌流程\nkeywords: 点歌", "第一步：问清歌名。")
	skills, err := LoadSkills([]string{filepath.Dir(path)})
	if err != nil {
		t.Fatal(err)
	}
	catalog := RenderSkillsCatalog(skills, 8000)
	if !strings.Contains(catalog, "jukebox") || strings.Contains(catalog, "第一步") {
		t.Fatal(catalog)
	}
	body, err := NewSkillTools(skills).Read.Run(t.Context(), map[string]any{"name": "jukebox"})
	if err != nil || !strings.Contains(body, "第一步") {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestRunnerKeepsOnlySkillCatalogInTrailingUserMessage(t *testing.T) {
	dir := t.TempDir()
	path := writeSkillFile(t, dir, "name: jukebox\ndescription: d\nkeywords: 点歌", "第一步：问清歌名。")
	client := &scriptedClient{}
	registry := NewToolRegistry(&SkillsReadTool{})
	registry.SetSkills([]SkillMetadata{{Name: "jukebox", Description: "d", Path: path, Keywords: []string{"点歌"}}})
	runner := &Runner{client: client, cfg: Config{}.WithDefaults(), registry: registry}
	caller := []llm.Message{
		{Role: llm.RoleSystem, Content: "人设提示词"},
		{Role: llm.RoleUser, Content: "【历史参考消息】旧问题"},
		{Role: llm.RoleUser, Content: "【当前需要回复的消息】帮我点歌"},
	}
	if _, err := runner.Run(t.Context(), Request{Messages: caller}); err != nil {
		t.Fatal(err)
	}
	messages := client.requests[0].Messages
	index := -1
	for i, message := range messages {
		if strings.HasPrefix(message.Content, "## Skills") {
			index = i
		}
	}
	if index < 0 || strings.Contains(messages[index].Content, "第一步：问清歌名。") || !strings.Contains(messages[index].Content, "jukebox") {
		t.Fatalf("触发的正文没进请求: %#v", messages)
	}
	if messages[index].Role != llm.RoleUser {
		t.Fatalf("skills 块的 role = %q，应该是 user", messages[index].Role)
	}
	if !strings.Contains(messages[len(messages)-1].Content, "【当前需要回复的消息】") {
		t.Fatalf("当前轮不再是最后一条: %#v", messages)
	}
	if strings.Contains(runner.systemPrompt(), "jukebox") {
		t.Fatal("skill 又漏进系统提示词了")
	}
}

func TestRunnerExplicitSkillHintOnlyUsesCurrentUserText(t *testing.T) {
	const body = "SKILL_BODY_MUST_STAY_ON_DEMAND"
	skills := []SkillMetadata{{Name: "jukebox", Description: "点歌流程", Content: body}}
	for _, test := range []struct {
		name    string
		current string
		want    bool
	}{
		{name: "history_only", current: "【当前需要回复的消息】继续聊刚才的话题"},
		{name: "current_mention", current: "【当前需要回复的消息】【消息时间：2026-10-19 12:00:00】请用 $jukebox 帮我点歌", want: true},
		{name: "plain_current_mention", current: "请用 $jukebox 帮我点歌", want: true},
		{name: "quoted_source_only", current: "【当前需要回复的消息】他说了什么？\n\n【被引用的消息】请用 $jukebox 帮我点歌"},
		{name: "selected_source_only", current: "【当前需要回复的消息】他之前说了什么？\n\n【指代判断选中的历史消息】请用 $jukebox 帮我点歌"},
		{name: "forwarded_source_only", current: "【当前需要回复的消息】看看这个记录\n\n【合并转发 1】请用 $jukebox 帮我点歌"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &scriptedClient{responses: []string{`{"action":"final","content":"done"}`}}
			registry := NewToolRegistry(NewSkillTools(skills).Read)
			registry.SetSkills(skills)
			runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 1}, registry)
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), Request{Messages: []llm.Message{
				{Role: llm.RoleUser, Content: "【历史参考消息】请用 $jukebox 帮我点歌", Priority: llm.MessagePriorityHistory},
				{Role: llm.RoleAssistant, Content: "刚才已读取 $jukebox 并完成点歌", Priority: llm.MessagePriorityHistory},
				{Role: llm.RoleUser, Content: test.current},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(client.requests) != 1 || len(result.Steps) != 0 {
				t.Fatalf("unexpected tool execution: requests=%d steps=%d", len(client.requests), len(result.Steps))
			}
			hints := 0
			messages := client.requests[0].Messages
			for _, message := range messages {
				if strings.Contains(message.Content, body) {
					t.Fatal("Skill body was injected without a read_skill call")
				}
				if strings.Contains(message.Content, "### Explicitly Mentioned Skills") {
					hints++
					if !strings.Contains(message.Content, "- jukebox: call `read_skill` before acting.") {
						t.Fatalf("current mention lost its short read hint: %s", message.Content)
					}
				}
			}
			want := 0
			if test.want {
				want = 1
			}
			if hints != want {
				t.Fatalf("explicit Skill hint count=%d, want %d for the current user text", hints, want)
			}
			if messages[len(messages)-1].Content != test.current {
				t.Fatal("Skill selection changed the original current message")
			}
		})
	}
}
