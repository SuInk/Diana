package agent

import (
	"context"
	"errors"
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
		got := withToolResultReminder(messages, "写最终回复前：上面的工具结果只是素材", false)
		if len(got) != len(messages)+1 || !strings.Contains(got[len(got)-1].Content, "修正参数") || strings.Contains(got[len(got)-1].Content, "写最终回复前") {
			t.Fatalf("failure treated as source material: %#v", got)
		}
		if !reflect.DeepEqual(messages, before) {
			t.Fatal("original history mutated")
		}
		// 修复成功后用正常提醒，不让更早的失败一直影响后续回复。
		recovered := append(messages, llm.Message{Role: llm.RoleAssistant}, llm.Message{Role: llm.RoleTool, Content: "真实结果"})
		got = withToolResultReminder(recovered, "R", false)
		if got[len(got)-1].Content != "R" {
			t.Fatal("old error leaked into successful turn")
		}
	}
}

// 只在上一步刚执行过工具时临时接上提醒，不改动原上下文。
func TestWithToolResultReminder(t *testing.T) {
	base := []llm.Message{{Role: llm.RoleUser, Content: "q"}}
	if got := withToolResultReminder(base, "R", false); len(got) != 1 {
		t.Fatalf("reminder added without tool result: %#v", got)
	}
	withTool := append(base, llm.Message{Role: llm.RoleAssistant}, llm.Message{Role: llm.RoleTool, Content: "r"})
	got := withToolResultReminder(withTool, "R", false)
	if len(got) != 4 || got[3].Role != llm.RoleUser || got[3].Content != "R" {
		t.Fatalf("reminder missing: %#v", got)
	}
	if len(withTool) != 3 {
		t.Fatal("original messages mutated")
	}
	if got := withToolResultReminder(withTool, " ", false); len(got) != 3 {
		t.Fatal("empty reminder added")
	}
	answered := append(withTool, llm.Message{Role: llm.RoleAssistant, Content: "a"})
	if got := withToolResultReminder(answered, "R", false); len(got) != 4 {
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

// 点名请求的失败提示要讲明不能静默收尾，非点名的保持原样（主动接话失败了可以不说）。
func TestDirectedToolFailureNoticeForbidsSilence(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "保存gif"},
		{Role: llm.RoleAssistant},
		{Role: llm.RoleTool, ToolError: true, Content: "ffmpeg: No such file or directory"},
	}
	directed := withToolResultReminder(messages, "R", true)
	if notice := directed[len(directed)-1].Content; !strings.Contains(notice, "修正参数") || !strings.Contains(notice, "不能静默收尾") {
		t.Fatalf("directed failure notice = %q", notice)
	}
	undirected := withToolResultReminder(messages, "R", false)
	if notice := undirected[len(undirected)-1].Content; strings.Contains(notice, "静默") {
		t.Fatalf("undirected failure notice mentions silence: %q", notice)
	}
	// 工具成功时点名与否都只接正常提醒。
	ok := append(messages[:2:2], llm.Message{Role: llm.RoleTool, Content: "done"})
	if got := withToolResultReminder(ok, "R", true); got[len(got)-1].Content != "R" {
		t.Fatalf("success reminder = %q", got[len(got)-1].Content)
	}
}

// nativeFailThenSendClient 用原生 function calling：先调一次报错的工具，再 send 收尾。
type nativeFailThenSendClient struct {
	requests []llm.GenerateRequest
}

func (c *nativeFailThenSendClient) Generate(_ context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	c.requests = append(c.requests, req)
	if len(c.requests) == 1 {
		return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "call-error", Name: "error", Arguments: map[string]any{}}}}, nil
	}
	return &llm.GenerateResponse{ToolCalls: []llm.ToolCall{{ID: "call-send", Name: finalizeToolName, Arguments: map[string]any{"text": "没做成：找不到文件。"}}}}, nil
}

// 运行时把 Request.Directed 带进规划请求：点名轮次失败后，模型看到的提示里有不许静默。
func TestRunnerPassesDirectedToFailureNotice(t *testing.T) {
	for _, directed := range []bool{true, false} {
		client := &nativeFailThenSendClient{}
		runner, err := NewRunner(client, Config{WorkDir: t.TempDir(), MaxSteps: 4, ToolResultReminder: "R"}, NewToolRegistry(&errorTestTool{err: errors.New("boom")}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runner.Run(context.Background(), Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "保存gif"}}, Directed: directed}); err != nil {
			t.Fatal(err)
		}
		if len(client.requests) < 2 {
			t.Fatalf("requests = %d", len(client.requests))
		}
		found := false
		for _, message := range client.requests[1].Messages {
			if strings.Contains(message.Content, "【工具调用失败】") {
				found = true
				if got := strings.Contains(message.Content, "不能静默收尾"); got != directed {
					t.Fatalf("directed=%v notice = %q", directed, message.Content)
				}
			}
		}
		if !found {
			t.Fatalf("directed=%v: failure notice missing", directed)
		}
	}
}
