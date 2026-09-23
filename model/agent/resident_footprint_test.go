// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 底价要跟着档位走：常驻工具进定义，按需工具只进协议里的目录，Skill 只带常驻档的
// 正文——命中触发词才带的那份随消息变，不算底价。
func TestResidentFootprintFollowsResidencyTiers(t *testing.T) {
	dir := t.TempDir()
	pinned := writeSkillFile(t, dir, "name: pinned\ndescription: d", "常驻正文。")
	keyword := writeSkillFile(t, t.TempDir(), "name: keyword\ndescription: d\nkeywords: 点歌", "触发正文。")
	registry := NewToolRegistry(&countingTool{name: "common"}, &countingTool{name: "rare"})
	resident := true
	registry.SetSkills([]SkillMetadata{
		{Name: "pinned", Description: "d", Path: pinned, Resident: &resident},
		{Name: "keyword", Description: "d", Path: keyword, Keywords: []string{"点歌"}},
	})
	runner, err := NewRunner(&deferredLoadClient{}, Config{CoreTools: []string{"common"}}, registry)
	if err != nil {
		t.Fatal(err)
	}
	footprint := runner.ResidentFootprint()
	names := strings.Join(toolDefinitionNames(footprint.Tools), ",")
	if names != "common,"+ToolsLoadToolName+","+ToolsExecuteToolName+","+finalizeToolName {
		t.Fatalf("resident tools = %s", names)
	}
	if !strings.Contains(footprint.SystemPrompt, "- rare:") || strings.Contains(footprint.SystemPrompt, "- common:") {
		t.Fatalf("deferred catalog wrong:\n%s", footprint.SystemPrompt)
	}
	if !strings.Contains(footprint.SkillsCatalog, "常驻正文") || strings.Contains(footprint.SkillsCatalog, "触发正文") {
		t.Fatalf("skills catalog:\n%s", footprint.SkillsCatalog)
	}
	if runner.loader != nil {
		t.Fatal("probing the footprint must not leave a loader on the runner")
	}
}

// 点名会让正文随目录带上；这时再叫模型先 read_skill 是自相矛盾的两条指令。
// 正文没真正下发（读不出来）时提示要留着。
func TestExplicitHintSkipsSkillsWhoseBodyShipped(t *testing.T) {
	path := writeSkillFile(t, t.TempDir(), "name: jukebox\ndescription: d", "第一步：问清歌名。")
	registry := NewToolRegistry(&SkillsReadTool{})
	registry.SetSkills([]SkillMetadata{
		{Name: "jukebox", Description: "d", Path: path},
		{Name: "ghost", Description: "d", Path: "/nonexistent/SKILL.md"},
	})
	runner := &Runner{cfg: Config{}.WithDefaults(), registry: registry}
	req := Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "用 $jukebox 和 $ghost"}}}
	_, delivered := renderSkillsCatalog(SelectSkillBodies(registry.Skills(), SkillScanText(req.Messages, 0)), 8000)
	hint := runner.explicitSkillPrompt(req, delivered)
	if strings.Contains(hint, "jukebox") || !strings.Contains(hint, "ghost") {
		t.Fatalf("hint = %q delivered = %v", hint, delivered)
	}
}

// 中文不用空格分词：名字后面紧跟汉字也是点名。以前按字节看边界，汉字的 UTF-8
// 首字节被当成拉丁字母，这种写法一律落空。
func TestExplicitMentionAcceptsAdjacentCJK(t *testing.T) {
	for _, text := range []string{"用$jukebox帮我点歌", "请$jukebox", "（$jukebox）", "$jukebox"} {
		if !hasExplicitSkillMention(text, "jukebox") {
			t.Errorf("%q should mention jukebox", text)
		}
	}
	for _, text := range []string{"$jukebox-pro", "$jukeboxes", "a$jukebox"} {
		if hasExplicitSkillMention(text, "jukebox") {
			t.Errorf("%q should not mention jukebox", text)
		}
	}
}
