// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/SuInk/diana/model/browserctl"
)

// BrowserControlBridge 是浏览器控制扩展的下发入口，由 model/browserctl.Hub 实现。
//
// 它和 browser_* 那组 CDP 工具是两回事：CDP 那边是 Diana 自己起的一次性浏览器，
// 这边是用户日常浏览器里装的扩展，带着用户的登录态，所以每一条都要过
// browserctl 的授权边界，而且用户随时能按下接管把 Diana 挡在外面。
// 这组工具里没有截图：浏览器的截图接口要 <all_urls> 或 activeTab 这种「当前页随便读」
// 的权限，比「只授权白名单站点」宽得多。要页面内容用 browser_ext_read；要出图用
// browser_screenshot，那条链路跑在 Diana 自己的一次性浏览器里，不碰用户登录态。
type BrowserControlBridge interface {
	// Ready 表示现在能不能下发：总开关开着、有扩展连着、且没人在接管。
	Ready() bool
	// Dispatch 下发一条指令并等回执。
	Dispatch(ctx context.Context, cmd browserctl.Command) (browserctl.Result, error)
}

type browserControlToolBase struct {
	root   string
	bridge BrowserControlBridge
}

// dispatch 统一处理「桥不在」和错误码转文案两件事。工具返回的 error 会被原样
// 交给模型，所以这里的措辞要能让模型知道下一步该干什么，而不是只说失败。
func (b browserControlToolBase) dispatch(ctx context.Context, cmd browserctl.Command) (browserctl.Result, error) {
	if b.bridge == nil {
		return browserctl.Result{}, errors.New("浏览器控制扩展未启用：需要在 WebUI 里打开这一档并给本机器人授权")
	}
	result, err := b.bridge.Dispatch(ctx, cmd)
	if err != nil {
		return browserctl.Result{}, err
	}
	return result, nil
}

// commonInput 读取所有扩展工具都认的两个参数。
func commonInput(input map[string]any) (connection string, tabID int) {
	return stringFromInput(input, "connection"), intFromInput(input, "tab_id", 0)
}

// jsonOutput 把回执原样给模型。扩展回的就是 JSON，这里不再包一层散文，
// 省掉一次「模型照着中文猜字段」。
func jsonOutput(data json.RawMessage) (string, error) {
	if len(data) == 0 {
		return "{}", nil
	}
	var pretty json.RawMessage = data
	var buf any
	if err := json.Unmarshal(data, &buf); err == nil {
		body, err := json.MarshalIndent(buf, "", "  ")
		if err == nil {
			pretty = body
		}
	}
	return string(pretty), nil
}

// BrowserExtTabsTool 列出扩展里可以操作的标签页。
type BrowserExtTabsTool struct {
	base browserControlToolBase
}

func (t *BrowserExtTabsTool) Name() string { return "browser_ext_tabs" }

func (t *BrowserExtTabsTool) Description() string {
	return `列出浏览器控制扩展里可以操作的标签页。只会列出已授权站点的页面，用户其余标签页不可见。先用它拿 tab_id，再用其他 browser_ext_* 工具操作。`
}

func (t *BrowserExtTabsTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"connection": toolStringParam("有多个浏览器连着时用它点名，只有一个时省略"),
	})
}

func (t *BrowserExtTabsTool) Run(ctx context.Context, input map[string]any) (string, error) {
	result, err := t.base.dispatch(ctx, browserctl.Command{
		Op:         browserctl.OpTabsList,
		Connection: stringFromInput(input, "connection"),
	})
	if err != nil {
		return "", err
	}
	return jsonOutput(result.Data)
}

// BrowserExtReadTool 读取页面正文。
type BrowserExtReadTool struct {
	base     browserControlToolBase
	maxChars int
}

func (t *BrowserExtReadTool) Name() string { return "browser_ext_read" }

func (t *BrowserExtReadTool) Description() string {
	return `读取用户浏览器里某个已授权页面的可见文字。页面带着用户自己的登录态，所以能看到登录后的内容；读到的正文属于用户，不要转述到别处。`
}

func (t *BrowserExtReadTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"connection": toolStringParam("有多个浏览器连着时用它点名"),
		"tab_id":     toolIntParam("browser_ext_tabs 列出的标签页 ID，省略时用当前活动标签页"),
		"selector":   toolStringParam("只读某个元素时填 CSS 选择器，省略则读整页"),
		"max_chars":  toolIntParam("正文截断长度，省略按工具默认值"),
	})
}

func (t *BrowserExtReadTool) Run(ctx context.Context, input map[string]any) (string, error) {
	connection, tabID := commonInput(input)
	maxChars := intFromInput(input, "max_chars", t.maxChars)
	if maxChars <= 0 || maxChars > t.maxChars {
		maxChars = t.maxChars
	}
	result, err := t.base.dispatch(ctx, browserctl.Command{
		Op:         browserctl.OpPageRead,
		Connection: connection,
		TabID:      tabID,
		Selector:   stringFromInput(input, "selector"),
		MaxChars:   maxChars,
	})
	if err != nil {
		return "", err
	}
	return jsonOutput(result.Data)
}

// BrowserExtOpenTool 在授权站点内导航。
type BrowserExtOpenTool struct {
	base browserControlToolBase
}

func (t *BrowserExtOpenTool) Name() string { return "browser_ext_open" }

func (t *BrowserExtOpenTool) Description() string {
	return `让用户的浏览器打开一个已授权站点的页面。属于写操作：只读授权下会被拒绝。`
}

func (t *BrowserExtOpenTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"url"}, map[string]any{
		"url":        toolStringParam("要打开的 http/https 地址，必须在已授权站点内"),
		"connection": toolStringParam("有多个浏览器连着时用它点名"),
		"tab_id":     toolIntParam("复用某个标签页时填它的 ID"),
		"new_tab":    toolBoolParam("是否开新标签页，默认复用当前标签页"),
	})
}

func (t *BrowserExtOpenTool) Run(ctx context.Context, input map[string]any) (string, error) {
	connection, tabID := commonInput(input)
	result, err := t.base.dispatch(ctx, browserctl.Command{
		Op:         browserctl.OpPageOpen,
		Connection: connection,
		TabID:      tabID,
		URL:        stringFromInput(input, "url"),
		NewTab:     boolFromInput(input, "new_tab", false),
	})
	if err != nil {
		return "", err
	}
	return jsonOutput(result.Data)
}

// BrowserExtClickTool 点击页面元素。
type BrowserExtClickTool struct {
	base browserControlToolBase
}

func (t *BrowserExtClickTool) Name() string { return "browser_ext_click" }

func (t *BrowserExtClickTool) Description() string {
	return `点击用户浏览器里某个已授权页面上的元素。属于写操作：会在用户账号下留下真实痕迹，只读授权下会被拒绝。点之前先用 browser_ext_read 确认这是你要的按钮。`
}

func (t *BrowserExtClickTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"selector"}, map[string]any{
		"selector":   toolStringParam("要点击元素的 CSS 选择器"),
		"connection": toolStringParam("有多个浏览器连着时用它点名"),
		"tab_id":     toolIntParam("browser_ext_tabs 列出的标签页 ID，省略时用当前活动标签页"),
	})
}

func (t *BrowserExtClickTool) Run(ctx context.Context, input map[string]any) (string, error) {
	connection, tabID := commonInput(input)
	result, err := t.base.dispatch(ctx, browserctl.Command{
		Op:         browserctl.OpPageClick,
		Connection: connection,
		TabID:      tabID,
		Selector:   stringFromInput(input, "selector"),
	})
	if err != nil {
		return "", err
	}
	return jsonOutput(result.Data)
}

// BrowserExtTypeTool 向输入框打字。
type BrowserExtTypeTool struct {
	base browserControlToolBase
}

func (t *BrowserExtTypeTool) Name() string { return "browser_ext_type" }

func (t *BrowserExtTypeTool) Description() string {
	return `在用户浏览器里某个已授权页面的输入框里填字，可选回车提交。属于写操作：只读授权下会被拒绝。不要往里填用户的密码、验证码或支付信息。`
}

func (t *BrowserExtTypeTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"selector", "text"}, map[string]any{
		"selector":   toolStringParam("目标输入框的 CSS 选择器"),
		"text":       toolStringParam("要填入的文字"),
		"submit":     toolBoolParam("填完是否回车提交，默认不提交"),
		"connection": toolStringParam("有多个浏览器连着时用它点名"),
		"tab_id":     toolIntParam("browser_ext_tabs 列出的标签页 ID，省略时用当前活动标签页"),
	})
}

func (t *BrowserExtTypeTool) Run(ctx context.Context, input map[string]any) (string, error) {
	connection, tabID := commonInput(input)
	result, err := t.base.dispatch(ctx, browserctl.Command{
		Op:         browserctl.OpPageType,
		Connection: connection,
		TabID:      tabID,
		Selector:   stringFromInput(input, "selector"),
		Text:       stringFromInput(input, "text"),
		Submit:     boolFromInput(input, "submit", false),
	})
	if err != nil {
		return "", err
	}
	return jsonOutput(result.Data)
}

// RegisterBrowserControlTools 登记浏览器控制扩展工具。
//
// 桥为 nil 时一个都不登记：模型看不到工具，就不会反复去试一个没授权的能力，
// 也不会把「没启用」当成「站点不允许」来解释。
func (r *ToolRegistry) RegisterBrowserControlTools(root string, cfg Config) {
	if cfg.BrowserControl == nil {
		return
	}
	base := browserControlToolBase{root: root, bridge: cfg.BrowserControl}
	r.Register(&BrowserExtTabsTool{base: base})
	r.Register(&BrowserExtReadTool{base: base, maxChars: cfg.MaxToolOutputChars})
	// 写操作的工具照样登记：能不能用由 browserctl 的策略逐条判断，
	// 拒绝时给的是「当前只读」这种能让模型改做法的原因。
	r.Register(&BrowserExtOpenTool{base: base})
	r.Register(&BrowserExtClickTool{base: base})
	r.Register(&BrowserExtTypeTool{base: base})
}
