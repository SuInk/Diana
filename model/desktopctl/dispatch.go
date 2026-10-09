// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package desktopctl

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Command 是一条待下发的指令。
type Command struct {
	Connection       string `json:"connection,omitempty"`
	Op               string `json:"op"`
	WindowID         string `json:"window_id,omitempty"`
	ExpectedBundleID string `json:"expected_bundle_id,omitempty"`
	// JobID 把本步记入持久任务；Observation 由执行器/任务观察版本使用。
	JobID       string `json:"job_id,omitempty"`
	Observation int64  `json:"observation,omitempty"`
	// IdempotencyKey 相同且已完成的步骤不再下发，避免恢复后重复提交。
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// 点击：相对目标窗口左上角的坐标（逻辑像素）。指针为 nil 表示未提供。
	X *float64 `json:"x,omitempty"`
	Y *float64 `json:"y,omitempty"`
	// Button 默认 left；可选 right / middle。
	Button string `json:"button,omitempty"`
	// Text 用于 window.type。
	Text string `json:"text,omitempty"`
	// Key 用于 window.key，例如 Return、Tab、cmd+c。
	Key       string `json:"key,omitempty"`
	ElementID string `json:"element_id,omitempty"`
	DeltaX    int    `json:"delta_x,omitempty"`
	DeltaY    int    `json:"delta_y,omitempty"`
}

// Dispatch 核对授权边界后下发指令并等回执。
func (h *Hub) Dispatch(ctx context.Context, cmd Command) (Result, error) {
	if h == nil {
		return Result{}, commandError(CodeDisabled, "桌面控制不可用")
	}
	if h.registry.EmergencyStop() {
		return Result{}, commandError(CodeTakeover, "人工接管中，请在控制台结束接管")
	}
	policy := h.Policy()
	if !policy.Enabled {
		return Result{}, commandError(CodeDisabled, "桌面控制未启用；先打开总开关并连接执行器")
	}
	op := strings.TrimSpace(cmd.Op)
	cmd.Op = op
	if !KnownOp(op) {
		return Result{}, commandError(CodeUnsupportedOp, "不支持的桌面控制指令：%s", op)
	}
	if IsWriteOp(op) && !policy.WriteEnabled {
		return Result{}, commandError(CodeWriteDisabled, "桌面控制当前只读，点击和输入都没有授权；先打开 write_enabled")
	}

	var stepID string
	if h.jobs != nil && strings.TrimSpace(cmd.JobID) != "" {
		skip, cached, sid, err := h.jobs.AuthorizeDispatch(ctx, cmd)
		if err != nil {
			return Result{}, err
		}
		if skip {
			return cached, nil
		}
		stepID = sid
		jobCtx, cancel := context.WithCancel(ctx)
		h.jobs.BindCancel(cmd.JobID, stepID, cancel)
		defer func() {
			h.jobs.UnbindCancel(cmd.JobID, stepID)
			cancel()
		}()
		ctx = jobCtx
	}

	conn, err := h.pickConnection(cmd.Connection)
	if err != nil {
		if stepID != "" && h.jobs != nil {
			h.jobs.RecordDispatch(ctx, cmd.JobID, stepID, cmd, Result{}, err)
		}
		return Result{}, err
	}
	if takeover, reason := conn.Takeover(); takeover {
		message := "用户正在人工接管桌面，本轮不下发任何操作"
		if reason != "" {
			message += "：" + reason
		}
		err := commandError(CodeTakeover, "%s", message)
		if stepID != "" && h.jobs != nil {
			h.jobs.RecordDispatch(ctx, cmd.JobID, stepID, cmd, Result{}, err)
		}
		return Result{}, err
	}
	if err := conn.reserve(policy, h.nowOrDefault()); err != nil {
		if stepID != "" && h.jobs != nil {
			h.jobs.RecordDispatch(ctx, cmd.JobID, stepID, cmd, Result{}, err)
		}
		return Result{}, err
	}

	if refresher, ok := conn.conn.(interface{ Refresh(context.Context) error }); ok {
		refreshCtx, cancel := context.WithTimeout(ctx, time.Duration(policy.CommandTimeoutMS)*time.Millisecond)
		err := refresher.Refresh(refreshCtx)
		cancel()
		if err != nil {
			if stepID != "" && h.jobs != nil {
				h.jobs.RecordDispatch(ctx, cmd.JobID, stepID, cmd, Result{}, err)
			}
			return Result{}, err
		}
	}
	if op == OpWindowsList {
		windows := conn.Windows(policy)
		data, err := json.Marshal(WindowsPayload{Windows: windows})
		if err != nil {
			err = commandError(CodeHelper, "窗口清单序列化失败：%v", err)
			if stepID != "" && h.jobs != nil {
				h.jobs.RecordDispatch(ctx, cmd.JobID, stepID, cmd, Result{}, err)
			}
			return Result{}, err
		}
		result := Result{OK: true, Data: data}
		if stepID != "" && h.jobs != nil {
			h.jobs.RecordDispatch(ctx, cmd.JobID, stepID, cmd, result, nil)
		}
		return result, nil
	}

	target, err := conn.resolveWindow(policy, cmd.WindowID)
	if err != nil {
		if stepID != "" && h.jobs != nil {
			h.jobs.RecordDispatch(ctx, cmd.JobID, stepID, cmd, Result{}, err)
		}
		return Result{}, err
	}
	cmd.WindowID = target.ID
	cmd.ExpectedBundleID = target.BundleID
	if err := validateCommandParams(op, cmd); err != nil {
		if stepID != "" && h.jobs != nil {
			h.jobs.RecordDispatch(ctx, cmd.JobID, stepID, cmd, Result{}, err)
		}
		return Result{}, err
	}
	timeout := time.Duration(policy.CommandTimeoutMS) * time.Millisecond
	result, err := conn.send(ctx, op, cmd, timeout)
	if stepID != "" && h.jobs != nil {
		h.jobs.RecordDispatch(ctx, cmd.JobID, stepID, cmd, result, err)
	}
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (c *Connection) resolveWindow(policy Policy, windowID string) (WindowInfo, error) {
	windows := c.Windows(policy)
	windowID = strings.TrimSpace(windowID)
	if windowID == "" {
		for _, win := range windows {
			if win.Active {
				return win, nil
			}
		}
		if len(windows) == 1 {
			return windows[0], nil
		}
		if len(windows) == 0 {
			return WindowInfo{}, commandError(CodeAppDenied, "当前没有任何已授权应用的窗口")
		}
		return WindowInfo{}, commandError(CodeBadRequest, "请用 window_id 指定 desktop_windows 列出的窗口")
	}
	for _, win := range windows {
		if win.ID == windowID {
			return win, nil
		}
	}
	return WindowInfo{}, commandError(CodeWindowUnknown,
		"窗口 %s 不存在，或不在已授权的应用范围内；先用 desktop_windows 看一眼", windowID)
}

func validateCommandParams(op string, cmd Command) error {
	switch op {
	case OpWindowClick:
		if cmd.ElementID != "" {
			if cmd.Button != "" && strings.ToLower(strings.TrimSpace(cmd.Button)) != "left" {
				return commandError(CodeBadRequest, "元素点击只支持左键；右键请用坐标")
			}
			if cmd.Observation <= 0 {
				return commandError(CodeBadRequest, "元素点击需要 observation")
			}
			break
		}
		if cmd.X == nil || cmd.Y == nil {
			return commandError(CodeBadRequest, "window.click 需要 x 与 y（相对窗口左上角）")
		}
		if math.IsNaN(*cmd.X) || math.IsNaN(*cmd.Y) || math.IsInf(*cmd.X, 0) || math.IsInf(*cmd.Y, 0) || *cmd.X < 0 || *cmd.Y < 0 {
			return commandError(CodeBadRequest, "window.click 的坐标不能为负")
		}
		switch strings.ToLower(strings.TrimSpace(cmd.Button)) {
		case "", "left", "right", "middle":
		default:
			return commandError(CodeBadRequest, "window.click 的 button 只支持 left/right/middle")
		}
	case OpWindowScroll:
		if cmd.X == nil || cmd.Y == nil || math.IsNaN(*cmd.X) || math.IsNaN(*cmd.Y) || math.IsInf(*cmd.X, 0) || math.IsInf(*cmd.Y, 0) || *cmd.X < 0 || *cmd.Y < 0 {
			return commandError(CodeBadRequest, "滚动需要窗口内有限坐标 x/y")
		}
		if cmd.DeltaX < -1000 || cmd.DeltaX > 1000 || cmd.DeltaY < -1000 || cmd.DeltaY > 1000 || (cmd.DeltaX == 0 && cmd.DeltaY == 0) {
			return commandError(CodeBadRequest, "滚动每轴须在 -1000 到 1000 像素内，且不能全为零")
		}
	case OpWindowType:
		if cmd.Text == "" {
			return commandError(CodeBadRequest, "window.type 需要 text")
		}
	case OpWindowKey:
		if strings.TrimSpace(cmd.Key) == "" {
			return commandError(CodeBadRequest, "window.key 需要 key")
		}
	}
	return nil
}

func (c *Connection) reserve(policy Policy, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return commandError(CodeNotConnected, "桌面控制连接已断开")
	}
	if len(c.pending) >= maxPendingCommands {
		return commandError(CodeRateLimited, "桌面控制有 %d 条指令还没回执，先等这一批结束", len(c.pending))
	}
	if c.windowFrom.IsZero() || now.Sub(c.windowFrom) >= time.Minute {
		c.windowFrom = now
		c.windowUsed = 0
	}
	if c.windowUsed >= policy.CommandsPerMinute {
		return commandError(CodeRateLimited,
			"桌面控制每分钟最多 %d 条指令，已经用完；等下一分钟再来", policy.CommandsPerMinute)
	}
	c.windowUsed++
	c.commands++
	return nil
}

func (c *Connection) send(ctx context.Context, op string, cmd Command, timeout time.Duration) (Result, error) {
	if timeout <= 0 {
		timeout = DefaultCommandTimeoutMS * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	params := rawJSON(cmd)
	if params == nil {
		return Result{}, commandError(CodeBadRequest, "指令参数无法序列化")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return Result{}, commandError(CodeNotConnected, "桌面控制连接已断开")
	}
	if c.takeover {
		c.mu.Unlock()
		return Result{}, commandError(CodeTakeover, "用户正在人工接管桌面")
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
	frame := Frame{Type: FrameCommand, ID: id, Op: op, Params: params, JobID: cmd.JobID, Observation: cmd.Observation}
	var sendErr error
	if sender, ok := c.conn.(interface {
		SendContext(context.Context, Frame) error
	}); ok {
		sendErr = sender.SendContext(ctx, frame)
	} else {
		sendErr = c.conn.Send(frame)
	}
	if err := sendErr; err != nil {
		forget()
		return Result{}, commandError(CodeNotConnected, "指令没能发给执行器：%v", err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-ch:
		if !result.OK {
			return Result{}, &CommandError{Code: result.Code, Message: helperErrorMessage(result)}
		}
		return result, nil
	case <-timer.C:
		forget()
		return Result{}, commandError(CodeTimeout, "执行器在 %s 内没有回执", timeout)
	case <-ctx.Done():
		forget()
		return Result{}, ctx.Err()
	}
}

func helperErrorMessage(result Result) string {
	message := strings.TrimSpace(result.Error)
	if message == "" {
		message = "执行器执行失败"
	}
	return message
}
