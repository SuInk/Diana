// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/agent"
)

// 新加的浏览器动作同样进操作记录：脚本记开头一段，按了什么键、切了哪个标签页都有。
func TestBrowserActionEntryCoversNewTools(t *testing.T) {
	event := MessageEvent{ProfileID: "bot", UserID: "owner"}
	entry, ok := browserActionEntry(event, agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "browser_eval", ToolInput: map[string]any{"script": "document.title"}})
	if !ok || entry.Message != "机器人在内置浏览器里执行脚本" || entry.Metadata["script"] != "document.title" || entry.Metadata["script_chars"] != 14 {
		t.Fatalf("eval entry = %+v", entry)
	}
	entry, ok = browserActionEntry(event, agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "browser_press_key", ToolInput: map[string]any{"key": "PageDown"}})
	if !ok || entry.Metadata["key"] != "PageDown" {
		t.Fatalf("key entry = %+v", entry)
	}
	for _, name := range agent.InteractiveBrowserToolNames {
		if _, ok := browserActionVerbs[name]; !ok {
			t.Fatalf("%s 没有进操作记录", name)
		}
	}
}

func TestUnlimitedAgentProgress(t *testing.T) {
	_, label, current, total, percent := formatAgentProgress(agent.RunEvent{ToolCall: 40})
	if label != "已调用 40 次工具" || current != 40 || total != 0 || percent != 0 {
		t.Fatalf("%s %d %d %d", label, current, total, percent)
	}
}
