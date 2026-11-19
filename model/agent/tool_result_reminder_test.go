package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func TestToolFailureReminderDoesNotInviteFinalAnswer(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		messages := []llm.Message{
			{Role: llm.RoleUser, Content: "你可以推翻 ETH 理论吗？"},
			{Role: llm.RoleAssistant, Content: "先查证一下"},
			{Role: llm.RoleTool, ToolError: true, Content: "本次调用未执行：input 必须是 JSON 对象"},
		}
		if mixed {
			messages = append(messages, llm.Message{Role: llm.RoleTool, Content: "其他调用成功"})
		}
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: "运行时工具状态"})
		before := append([]llm.Message(nil), messages...)
		got := withToolResultReminder(messages, "写最终回复前：上面的工具结果只是素材")
		if len(got) != len(messages)+1 || !strings.Contains(got[len(got)-1].Content, "修正参数") || strings.Contains(got[len(got)-1].Content, "写最终回复前") {
			t.Fatalf("failure treated as source material: %#v", got)
		}
		if !reflect.DeepEqual(messages, before) {
			t.Fatal("original history mutated")
		}
		// 修复成功后用正常提醒，不让更早的失败一直影响后续回复。
		recovered := append(messages, llm.Message{Role: llm.RoleAssistant}, llm.Message{Role: llm.RoleTool, Content: "真实结果"})
		got = withToolResultReminder(recovered, "R")
		if got[len(got)-1].Content != "R" {
			t.Fatal("old error leaked into successful turn")
		}
	}
}

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

func TestToolResultReminderSwitchesAfterSubtask(t *testing.T) {
	runner := &Runner{cfg: Config{ToolResultReminder: "brief", SubtaskResultReminder: "full"}}
	if got := runner.toolResultReminder([]Step{{Tool: webSearchToolName}}); got != "brief" {
		t.Fatalf("web search only = %q, want brief", got)
	}
	if got := runner.toolResultReminder([]Step{{Tool: subtaskToolName, Error: "boom"}}); got != "brief" {
		t.Fatalf("failed subtask = %q, want brief", got)
	}
	if got := runner.toolResultReminder([]Step{{Tool: webSearchToolName}, {Tool: subtaskToolName}}); got != "full" {
		t.Fatalf("subtask = %q, want full", got)
	}
	runner.cfg.SubtaskResultReminder = ""
	if got := runner.toolResultReminder([]Step{{Tool: subtaskToolName}}); got != "brief" {
		t.Fatalf("no subtask reminder = %q, want brief", got)
	}
}
