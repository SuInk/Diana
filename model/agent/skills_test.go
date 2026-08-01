package agent

import (
	"context"
	"strings"
	"testing"
)

// TestLoadSkillsAndReadTool 验证 Codex-style skill 发现和完整读取。
func TestLoadSkillsAndReadTool(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "demo/SKILL.md", "---\nname: demo-skill\ndescription: Use this demo skill.\n---\n\nDo the demo workflow.")
	skills, err := LoadSkills([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Name != "demo-skill" {
		t.Fatalf("skills = %#v", skills)
	}
	prompt := RenderSkillsPrompt(skills, 8000)
	if !strings.Contains(prompt, "demo-skill") || !strings.Contains(prompt, "skills.read") {
		t.Fatalf("prompt did not include skill guidance: %s", prompt)
	}
	tools := NewSkillTools(skills)
	got, err := tools.Read.Run(context.Background(), map[string]any{"name": "demo-skill"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Do the demo workflow.") {
		t.Fatalf("read output = %s", got)
	}
}

// TestSelectExplicitSkillsRequiresBoundary 验证 $skill 显式触发边界。
func TestSelectExplicitSkillsRequiresBoundary(t *testing.T) {
	skills := []SkillMetadata{{Name: "demo-skill", Description: "demo", Path: "/tmp/SKILL.md"}}
	if got := SelectExplicitSkills(skills, "run $demo-skill please"); len(got) != 1 {
		t.Fatalf("selected = %#v, want one", got)
	}
	if got := SelectExplicitSkills(skills, "run $demo-skill-extra please"); len(got) != 0 {
		t.Fatalf("selected = %#v, want none", got)
	}
}
