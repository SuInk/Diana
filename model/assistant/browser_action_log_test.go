// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
)

// 机器人往浏览器里打的字只记字数：可能是在填登录框。
func TestBrowserActionEntryNeverLogsTypedText(t *testing.T) {
	entry, ok := browserActionEntry(MessageEvent{ProfileID: "bot-a"}, agent.RunEvent{
		Phase:     agent.RunPhaseToolCompleted,
		Tool:      "browser_type",
		ToolInput: map[string]any{"selector": "#password", "text": "hunter2"},
	})
	if !ok {
		t.Fatal("browser_type 应该记一条")
	}
	if entry.Action != BrowserActionLogAction || entry.Target != "#password" || entry.Metadata["profile_id"] != "bot-a" {
		t.Fatalf("记录内容不对：%+v", entry)
	}
	if entry.Metadata["text_chars"] != 7 {
		t.Fatalf("应只记字数：%+v", entry.Metadata)
	}
	for _, value := range []string{entry.Message, entry.Detail, entry.Target} {
		if strings.Contains(value, "hunter2") {
			t.Fatal("输入的内容不该进日志")
		}
	}
	for key, value := range entry.Metadata {
		if s, ok := value.(string); ok && strings.Contains(s, "hunter2") {
			t.Fatalf("输入的内容出现在 metadata.%s", key)
		}
	}
}

// 扩展那组记成「你的 Chrome」，失败的记成错误并带上原因；无头渲染和只在开始阶段的事件不记。
func TestBrowserActionEntrySourcesAndFailures(t *testing.T) {
	entry, ok := browserActionEntry(MessageEvent{ProfileID: "bot-a"}, agent.RunEvent{
		Phase:     agent.RunPhaseToolCompleted,
		Tool:      "browser_ext_open",
		ToolInput: map[string]any{"url": "https://example.com"},
		Error:     "站点不在白名单",
	})
	if !ok || entry.Metadata["source"] != "extension" || entry.Target != "https://example.com" {
		t.Fatalf("扩展那组记录不对：%+v", entry)
	}
	if entry.Kind != applog.KindError || entry.Detail != "站点不在白名单" {
		t.Fatalf("失败应记成错误并带原因：%+v", entry)
	}
	if _, ok := browserActionEntry(MessageEvent{}, agent.RunEvent{Phase: agent.RunPhaseToolCompleted, Tool: "browser_render"}); ok {
		t.Fatal("无头渲染由插件自己记，这里不该重复")
	}
	if _, ok := browserActionEntry(MessageEvent{}, agent.RunEvent{Phase: agent.RunPhaseToolStarted, Tool: "browser_open"}); ok {
		t.Fatal("开始阶段不记，只在完成时记一条")
	}
}
