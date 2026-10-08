package agent

import (
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 只在上一步刚执行过工具时临时接上提醒，不改动原上下文。
func TestWithToolResultReminder(t *testing.T) {
	base := []llm.Message{{Role: llm.RoleUser, Content: "q"}}
	if got := withToolResultReminder(base, "R"); len(got) != 1 {
		t.Fatalf("reminder added without tool result: %#v", got)
	}
	withTool := append(base, llm.Message{Role: llm.RoleAssistant}, llm.Message{Role: llm.RoleTool, Content: "r"})
	got := withToolResultReminder(withTool, "R")
	if len(got) != 4 || got[3].Role != llm.RoleUser || got[3].Content != "R" {
		t.Fatalf("reminder missing: %#v", got)
	}
	if len(withTool) != 3 {
		t.Fatal("original messages mutated")
	}
	if got := withToolResultReminder(withTool, " "); len(got) != 3 {
		t.Fatal("empty reminder added")
	}
	answered := append(withTool, llm.Message{Role: llm.RoleAssistant, Content: "a"})
	if got := withToolResultReminder(answered, "R"); len(got) != 4 {
		t.Fatal("reminder added after assistant turn")
	}
}
