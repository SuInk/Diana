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
func TestKeywordHitShipsTheBodyWithoutReadSkill(t *testing.T) {
	dir := t.TempDir()
	path := writeSkillFile(t, dir, "name: jukebox\ndescription: 点歌流程\nkeywords: 点歌", "第一步：问清歌名。")
	skills := []SkillMetadata{{Name: "jukebox", Description: "点歌流程", Path: path, Keywords: []string{"点歌"}}}

	hit := RenderSkillsCatalog(SelectSkillBodies(skills, "帮我点歌"), 8000)
	if !strings.Contains(hit, "第一步：问清歌名。") {
		t.Fatalf("命中关键词没带正文:\n%s", hit)
	}
	miss := RenderSkillsCatalog(SelectSkillBodies(skills, "今天天气不错"), 8000)
	if strings.Contains(miss, "第一步") {
		t.Fatalf("没命中却带了正文:\n%s", miss)
	}
	if !strings.Contains(miss, "jukebox") {
		t.Fatalf("没命中时目录行也该在:\n%s", miss)
	}
}

// 档位优先于关键词：按到某一档就是不想再让它自己变。
func TestConfiguredTierOverridesKeywords(t *testing.T) {
	dir := t.TempDir()
	path := writeSkillFile(t, dir, "name: jukebox\ndescription: d\nkeywords: 点歌", "第一步：问清歌名。")
	deferred, resident := false, true

	off := RenderSkillsCatalog(SelectSkillBodies([]SkillMetadata{{Name: "jukebox", Description: "d", Path: path, Keywords: []string{"点歌"}, Resident: &deferred}}, "帮我点歌"), 8000)
	if strings.Contains(off, "第一步") {
		t.Fatalf("配成按需却因为关键词带上了正文:\n%s", off)
	}
	on := RenderSkillsCatalog(SelectSkillBodies([]SkillMetadata{{Name: "jukebox", Description: "d", Path: path, Resident: &resident}}, "今天天气不错"), 8000)
	if !strings.Contains(on, "第一步") {
		t.Fatalf("配成常驻却没带正文:\n%s", on)
	}
}

// $skill 点名等于用户已经说清要用哪个，再走一次 read_skill 纯属多跑一轮。
func TestExplicitMentionShipsTheBody(t *testing.T) {
	dir := t.TempDir()
	path := writeSkillFile(t, dir, "name: jukebox\ndescription: d", "第一步：问清歌名。")
	skills := []SkillMetadata{{Name: "jukebox", Description: "d", Path: path}}
	got := RenderSkillsCatalog(SelectSkillBodies(skills, "用一下 $jukebox"), 8000)
	if !strings.Contains(got, "第一步") {
		t.Fatalf("点名没带正文:\n%s", got)
	}
}

// 只扫最近几条：很久以前提过一次的词不该让这份正文从此每轮都在。
func TestScanTextStopsAtTheConfiguredDepth(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "点歌"},
		{Role: llm.RoleAssistant, Content: "好"},
		{Role: llm.RoleUser, Content: "中间一句"},
		{Role: llm.RoleUser, Content: "现在几点"},
	}
	if text := SkillScanText(messages, 2); strings.Contains(text, "点歌") {
		t.Fatalf("扫描超出了深度: %q", text)
	}
	if text := SkillScanText(messages, 3); !strings.Contains(text, "点歌") {
		t.Fatalf("深度够却没扫到: %q", text)
	}
	// 助手消息不参与匹配：机器人自己复述过的词不该把正文钉在上下文里。
	if text := SkillScanText([]llm.Message{{Role: llm.RoleAssistant, Content: "点歌"}}, 2); strings.TrimSpace(text) != "" {
		t.Fatalf("扫到了助手消息: %q", text)
	}
}

// 关键词命中要走完整条路：目录挂在尾部的 user role 消息里，当前轮仍是最后一条。
func TestRunnerShipsTriggeredSkillBodyInTrailingUserMessage(t *testing.T) {
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
		if strings.Contains(message.Content, "### Available skills") {
			index = i
		}
	}
	if index < 0 || !strings.Contains(messages[index].Content, "第一步：问清歌名。") {
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
