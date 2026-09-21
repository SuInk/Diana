// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package browserctl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Command 是一条待下发的指令。字段是协议的一部分，扩展按同一套字段名解析。
type Command struct {
	// Connection 指定用哪条连接。只有一条连接时可以留空。
	Connection string `json:"connection,omitempty"`
	Op         string `json:"op"`
	// TabID 指定目标标签页；留空表示用当前活动标签页。
	TabID int `json:"tab_id,omitempty"`
	// URL 只用于 page.open。
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	// Submit 让 page.type 在输入后回车提交。
	Submit bool `json:"submit,omitempty"`
	// MaxChars 限制 page.read 返回的正文长度。
	MaxChars int `json:"max_chars,omitempty"`
	// NewTab 让 page.open 开新标签页而不是复用当前页。
	NewTab bool `json:"new_tab,omitempty"`
}

// Dispatch 逐项核对授权边界后下发一条指令，并等它的回执。
//
// 核对顺序是有意的：先看总开关和指令本身，再看连接与接管，最后才看站点。
// 这样被拒时给出的错误码指向真正需要改的那一项，而不是一路掉到「站点不允许」。
func (h *Hub) Dispatch(ctx context.Context, cmd Command) (Result, error) {
	if h == nil {
		return Result{}, commandError(CodeDisabled, "浏览器控制不可用")
	}
	policy := h.registry.Policy()
	if !policy.Enabled {
		return Result{}, commandError(CodeDisabled, "浏览器控制未启用，先在 WebUI 里打开并授权站点")
	}
	op := strings.TrimSpace(cmd.Op)
	if !KnownOp(op) {
		return Result{}, commandError(CodeUnsupportedOp, "不支持的浏览器控制指令：%s", op)
	}
	if IsWriteOp(op) && !policy.WriteEnabled {
		return Result{}, commandError(CodeWriteDisabled, "浏览器控制当前只读，点击、输入和导航都没有授权")
	}
	conn, err := h.pickConnection(cmd.Connection)
	if err != nil {
		return Result{}, err
	}
	if takeover, reason := conn.Takeover(); takeover {
		message := "用户正在人工接管浏览器，本轮不下发任何操作"
		if reason != "" {
			message += "：" + reason
		}
		return Result{}, commandError(CodeTakeover, "%s", message)
	}
	if err := conn.reserve(policy, h.nowOrDefault()); err != nil {
		return Result{}, err
	}

	// tabs.list 不下发到扩展：标签页清单本来就在控制面的缓存里，而且必须先按
	// 策略筛一遍再给模型看，免得把用户其余标签页的地址一起送出去。
	if op == OpTabsList {
		tabs := conn.Tabs(policy)
		data, err := json.Marshal(TabsPayload{Tabs: tabs})
		if err != nil {
			return Result{}, commandError(CodeExtension, "标签页清单序列化失败：%v", err)
		}
		return Result{OK: true, Data: data}, nil
	}

	target, err := conn.resolveTarget(policy, cmd)
	if err != nil {
		return Result{}, err
	}
	cmd.TabID = target.ID
	if err := validateCommandParams(op, cmd); err != nil {
		return Result{}, err
	}

	timeout := time.Duration(policy.CommandTimeoutMS) * time.Millisecond
	result, err := conn.send(ctx, op, cmd, timeout)
	if err != nil {
		return Result{}, err
	}
	// 页面可能在导航后跳到白名单外的站点。扩展也会拦，但控制面不能只信扩展：
	// 回执里报的地址不在范围内就只回地址，不回正文。
	if result.OK {
		if pageURL, ok := resultPageURL(result.Data); ok && !policy.HostAllowed(pageURL) {
			return Result{}, commandError(CodeHostDenied,
				"页面已跳转到未授权站点 %s，本次结果不返回正文", pageURL)
		}
	}
	return result, nil
}

// targetTab 是本次指令实际作用的标签页。
func (c *Connection) resolveTarget(policy Policy, cmd Command) (TabInfo, error) {
	tabs := c.Tabs(policy)
	if cmd.Op == OpPageOpen {
		// 导航的目标由 URL 决定，先把地址本身核一遍。
		if !policy.HostAllowed(cmd.URL) {
			return TabInfo{}, hostDeniedError(cmd.URL)
		}
		if cmd.NewTab || cmd.TabID == 0 {
			// 新标签页还不存在，交给扩展在允许的站点上新建。
			return TabInfo{ID: cmd.TabID}, nil
		}
	}
	if cmd.TabID != 0 {
		for _, tab := range tabs {
			if tab.ID == cmd.TabID {
				return tab, nil
			}
		}
		// 找不到有两种可能：标签页关了，或者它在白名单外。两者都不该继续，
		// 也都不该告诉模型「那个标签页现在是什么站点」。
		return TabInfo{}, commandError(CodeTabUnknown,
			"标签页 %d 不存在，或不在已授权的站点范围内；先用 browser_ext_tabs 看一眼", cmd.TabID)
	}
	for _, tab := range tabs {
		if tab.Active {
			return tab, nil
		}
	}
	if len(tabs) == 1 {
		return tabs[0], nil
	}
	if len(tabs) == 0 {
		return TabInfo{}, commandError(CodeHostDenied,
			"当前没有任何已授权站点的标签页；先用 browser_ext_open 打开一个白名单内的页面")
	}
	return TabInfo{}, commandError(CodeBadRequest,
		"当前活动标签页不在授权范围内，请用 tab_id 指定 browser_ext_tabs 列出的标签页")
}

func hostDeniedError(rawURL string) error {
	host, ok := policyHost(rawURL)
	if !ok {
		return commandError(CodeHostDenied, "只允许操作 http/https 页面，这个地址不行：%s", rawURL)
	}
	return commandError(CodeHostDenied, "站点 %s 不在浏览器控制的白名单内", host)
}

// validateCommandParams 拦下少填参数的指令，省掉一次往返。
func validateCommandParams(op string, cmd Command) error {
	switch op {
	case OpPageOpen:
		if strings.TrimSpace(cmd.URL) == "" {
			return commandError(CodeBadRequest, "page.open 需要 url")
		}
	case OpPageClick:
		if strings.TrimSpace(cmd.Selector) == "" {
			return commandError(CodeBadRequest, "page.click 需要 selector")
		}
	case OpPageType:
		if strings.TrimSpace(cmd.Selector) == "" {
			return commandError(CodeBadRequest, "page.type 需要 selector")
		}
		if cmd.Text == "" {
			return commandError(CodeBadRequest, "page.type 需要 text")
		}
	}
	return nil
}

// resultPageURL 从回执里取出页面地址。没有这个字段时返回 false，
// 调用方据此跳过跳转复核（截图之类的回执不一定带地址）。
func resultPageURL(data json.RawMessage) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	var payload struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return "", false
	}
	payload.URL = strings.TrimSpace(payload.URL)
	if payload.URL == "" {
		return "", false
	}
	return payload.URL, true
}

// reserve 做固定窗口限流。窗口重开即清零，实现最简单，对「防模型打转」够用。
func (c *Connection) reserve(policy Policy, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return commandError(CodeNotConnected, "浏览器控制连接已断开")
	}
	if len(c.pending) >= maxPendingCommands {
		return commandError(CodeRateLimited, "浏览器控制有 %d 条指令还没回执，先等这一批结束", len(c.pending))
	}
	if c.windowFrom.IsZero() || now.Sub(c.windowFrom) >= time.Minute {
		c.windowFrom = now
		c.windowUsed = 0
	}
	if c.windowUsed >= policy.CommandsPerMinute {
		return commandError(CodeRateLimited,
			"浏览器控制每分钟最多 %d 条指令，已经用完；等下一分钟再来", policy.CommandsPerMinute)
	}
	c.windowUsed++
	c.commands++
	return nil
}

// send 下发一条指令并等回执。超时只作用在这一条指令上，不影响连接本身：
// 一次点击没回执不代表扩展掉线了。
func (c *Connection) send(ctx context.Context, op string, cmd Command, timeout time.Duration) (Result, error) {
	params := rawJSON(cmd)
	if params == nil {
		return Result{}, commandError(CodeBadRequest, "指令参数无法序列化")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return Result{}, commandError(CodeNotConnected, "浏览器控制连接已断开")
	}
	c.seq++
	id := fmt.Sprintf("%s-%d", c.id, c.seq)
	ch := make(chan Result, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	if err := c.conn.Send(Frame{Type: FrameCommand, ID: id, Op: op, Params: params}); err != nil {
		forget()
		return Result{}, commandError(CodeNotConnected, "指令没能发给扩展：%v", err)
	}
	if timeout <= 0 {
		timeout = DefaultCommandTimeoutMS * time.Millisecond
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-ch:
		if !result.OK {
			return Result{}, &CommandError{Code: result.Code, Message: browserErrorMessage(result)}
		}
		return result, nil
	case <-timer.C:
		forget()
		return Result{}, commandError(CodeTimeout, "扩展在 %s 内没有回执", timeout)
	case <-ctx.Done():
		forget()
		return Result{}, ctx.Err()
	}
}

func browserErrorMessage(result Result) string {
	message := strings.TrimSpace(result.Error)
	if message == "" {
		message = "扩展执行失败"
	}
	return message
}
