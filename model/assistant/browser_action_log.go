// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/applog"
)

// BrowserActionLogAction 是「机器人在带登录态的浏览器里做了什么」那一类操作记录。
//
// 通用的 agent_tool 记录只留参数名，不留网址和选择器，因为大多数工具的参数不该进
// 普通日志。浏览器这组不一样：它带着主人的登录态，主人得能事后查清机器人打开过哪个
// 网址、点过哪里，所以单独记一条，并带上机器人 ID，浏览器页按机器人筛。
const BrowserActionLogAction = "browser_action"

// browserActionVerbs 是记进日志的动作名。browser_render 不在里面：它是一次性无头
// 渲染、不带登录态，插件自己已经记了一条。
var browserActionVerbs = map[string]string{
	"browser_open":       "打开网页",
	"browser_text":       "读取页面",
	"browser_click":      "点击",
	"browser_type":       "输入",
	"browser_screenshot": "截图",
	"browser_tabs":       "管理标签页",
	"browser_scroll":     "滚动",
	"browser_press_key":  "按键",
	"browser_navigate":   "前进后退",
	"browser_select":     "选择下拉项",
	"browser_wait":       "等待页面",
	"browser_eval":       "执行脚本",
	"browser_handoff":    "请你接管",
	"browser_ext_tabs":   "查看标签页",
	"browser_ext_read":   "读取页面",
	"browser_ext_open":   "打开网页",
	"browser_ext_click":  "点击",
	"browser_ext_type":   "输入",
}

// browserActionEntry 把一次浏览器工具调用整理成操作记录。不是浏览器工具时返回 false。
//
// 输入的文字只记字数不记内容：机器人可能在往登录框里填东西，日志不是存这些的地方。
func browserActionEntry(event MessageEvent, runEvent agent.RunEvent) (applog.Entry, bool) {
	if runEvent.Phase != agent.RunPhaseToolCompleted {
		return applog.Entry{}, false
	}
	verb, ok := browserActionVerbs[runEvent.Tool]
	if !ok {
		return applog.Entry{}, false
	}
	source := "box"
	who := "机器人在内置浏览器里"
	if strings.HasPrefix(runEvent.Tool, "browser_ext_") {
		source = "extension"
		who = "机器人在你的 Chrome 里"
	}
	pageURL := strings.TrimSpace(inputString(runEvent.ToolInput, "url"))
	selector := strings.TrimSpace(inputString(runEvent.ToolInput, "selector"))
	target := pageURL
	if target == "" {
		target = selector
	}
	// 请你接管的那一条，要看的是请你做什么。
	if runEvent.Tool == dianaBrowserHandoffToolName {
		target = strings.TrimSpace(inputString(runEvent.ToolInput, "reason"))
	}
	metadata := map[string]any{
		"profile_id":  event.ProfileID,
		"tool":        runEvent.Tool,
		"source":      source,
		"trace_id":    runEvent.TraceID,
		"group_id":    event.GroupID,
		"duration_ms": runEvent.DurationMS,
	}
	if pageURL != "" {
		metadata["url"] = pageURL
	}
	if selector != "" {
		metadata["selector"] = selector
	}
	if text := inputString(runEvent.ToolInput, "text"); text != "" {
		metadata["text_chars"] = utf8.RuneCountInString(text)
	}
	for _, key := range []string{"action", "tab_id", "key", "direction"} {
		if value := strings.TrimSpace(inputString(runEvent.ToolInput, key)); value != "" {
			metadata[key] = value
		}
	}
	// 脚本能读能改整个页面，主人得能事后看清它做了什么，所以记开头一段；和输入文字
	// 不同，脚本是模型写的，不是从登录框里抄来的。
	if script := inputString(runEvent.ToolInput, "script"); script != "" {
		metadata["script_chars"] = utf8.RuneCountInString(script)
		metadata["script"] = truncateRunes(script, 500)
	}
	entry := applog.Entry{
		Kind:     applog.KindOperation,
		Level:    applog.LevelInfo,
		Action:   BrowserActionLogAction,
		Message:  who + verb,
		Actor:    oneBotEventActor(event),
		Target:   target,
		Metadata: metadata,
	}
	if runEvent.Error != "" {
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Message += "失败"
		entry.Detail = runEvent.Error
	}
	return entry, true
}

func inputString(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

// recordBrowserAction 在浏览器工具调用完成时补记一条操作记录。
func (r *Runtime) recordBrowserAction(writer applog.Writer, event MessageEvent, runEvent agent.RunEvent) {
	entry, ok := browserActionEntry(event, runEvent)
	if !ok || writer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(ctx, entry)
}
