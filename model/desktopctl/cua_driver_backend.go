// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// cuaDriverBackend drives the desktop through trycua's cua-driver (MIT) over
// MCP stdio. It only translates: observation versions, scene hashing, one-shot
// approvals and post-action evidence stay in ProcessAdapter, exactly as with
// the native helper.
//
// Diana window IDs are "<pid>:<window_id>". Element IDs are the driver's row
// index within the latest snapshot of that window; the snapshot token is kept
// here, because every element read supersedes the previous snapshot's tokens
// and ProcessAdapter re-reads elements right before an element action.
type cuaDriverBackend struct {
	session cuaDriverCaller

	mu        sync.Mutex
	snapshots map[string]string // window -> latest snapshot_id with elements
	captures  map[string]string // window -> latest capture_id
	longEdge  map[string]int    // window -> long edge of its bounds in points
}

// cuaDriverCaller is the MCP session; tests substitute a scripted fake.
type cuaDriverCaller interface {
	call(ctx context.Context, name string, args map[string]any) (cuaDriverResult, error)
	Close() error
}

func newCUADriverBackend(path string) (*cuaDriverBackend, error) {
	// --direct runs the driver inside this child process, so macOS attributes
	// Screen Recording / Accessibility to whatever runs Diana — the same grant
	// model as the native helper, and no separate CuaDriver.app to register.
	session, err := newCUADriverSession(path, "mcp", "--direct")
	if err != nil {
		return nil, err
	}
	return &cuaDriverBackend{session: session, snapshots: map[string]string{}, captures: map[string]string{}, longEdge: map[string]int{}}, nil
}

func (b *cuaDriverBackend) close() { _ = b.session.Close() }

type cuaWindowBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func (bounds cuaWindowBounds) longEdge() int {
	edge := bounds.Width
	if bounds.Height > edge {
		edge = bounds.Height
	}
	return int(edge + 0.5)
}

func cuaWindowID(pid, window int64) string { return fmt.Sprintf("%d:%d", pid, window) }

func parseCUAWindowID(id string) (pid, window int64, err error) {
	left, right, ok := strings.Cut(strings.TrimSpace(id), ":")
	if ok {
		pid, err = strconv.ParseInt(left, 10, 64)
		if err == nil {
			window, err = strconv.ParseInt(right, 10, 64)
		}
	}
	if !ok || err != nil || pid <= 0 || window <= 0 {
		return 0, 0, commandError(CodeWindowUnknown, "窗口标识无效，请重新列出窗口")
	}
	return pid, window, nil
}

func (b *cuaDriverBackend) listWindows(ctx context.Context) ([]WindowInfo, error) {
	apps, err := b.session.call(ctx, "list_apps", map[string]any{})
	if err != nil {
		return nil, err
	}
	var appList struct {
		Apps []struct {
			BundleID string `json:"bundle_id"`
			Name     string `json:"name"`
			PID      int64  `json:"pid"`
			Running  bool   `json:"running"`
			Active   bool   `json:"active"`
		} `json:"apps"`
	}
	if json.Unmarshal(apps.Structured, &appList) != nil {
		return nil, commandError(CodeHelper, "cua-driver 返回了无效应用清单")
	}
	type appInfo struct {
		bundle, name string
		active       bool
	}
	byPID := map[int64]appInfo{}
	for _, app := range appList.Apps {
		if app.Running && app.PID > 0 {
			byPID[app.PID] = appInfo{bundle: app.BundleID, name: app.Name, active: app.Active}
		}
	}
	listed, err := b.session.call(ctx, "list_windows", map[string]any{"on_screen_only": true})
	if err != nil {
		return nil, err
	}
	var windows struct {
		Windows []struct {
			AppName  string          `json:"app_name"`
			Title    string          `json:"title"`
			PID      int64           `json:"pid"`
			WindowID int64           `json:"window_id"`
			Layer    int             `json:"layer"`
			ZIndex   *int            `json:"z_index"`
			Bounds   cuaWindowBounds `json:"bounds"`
		} `json:"windows"`
	}
	if json.Unmarshal(listed.Structured, &windows) != nil {
		return nil, commandError(CodeHelper, "cua-driver 返回了无效窗口清单")
	}
	out := make([]WindowInfo, 0, len(windows.Windows))
	edges := map[string]int{}
	frontZ := map[int64]int{}
	for _, window := range windows.Windows {
		if window.Layer == 0 && window.ZIndex != nil && (frontZ[window.PID] == 0 || *window.ZIndex > frontZ[window.PID]) {
			frontZ[window.PID] = *window.ZIndex
		}
	}
	for _, window := range windows.Windows {
		app, ok := byPID[window.PID]
		// Bundle ID is the allowlist key: a window whose owner cannot be named
		// is never offered, so the policy can not be bypassed by an unknown app.
		if !ok || app.bundle == "" || window.Layer != 0 || window.WindowID <= 0 {
			continue
		}
		id := cuaWindowID(window.PID, window.WindowID)
		edges[id] = window.Bounds.longEdge()
		name := app.name
		if name == "" {
			name = window.AppName
		}
		out = append(out, WindowInfo{
			ID:       id,
			AppName:  name,
			BundleID: app.bundle,
			Title:    window.Title,
			Active:   app.active && window.ZIndex != nil && *window.ZIndex == frontZ[window.PID],
		})
	}
	b.mu.Lock()
	b.longEdge = edges
	b.mu.Unlock()
	return out, nil
}

// windowState is the subset of get_window_state Diana reads.
type cuaWindowState struct {
	AppName     string          `json:"app_name"`
	WindowTitle string          `json:"window_title"`
	CaptureID   string          `json:"capture_id"`
	SnapshotID  string          `json:"snapshot_id"`
	PID         int64           `json:"pid"`
	WindowID    int64           `json:"window_id"`
	Truncated   bool            `json:"truncated"`
	Bounds      cuaWindowBounds `json:"window_bounds"`
	Elements    []struct {
		Index   int      `json:"element_index"`
		Role    string   `json:"role"`
		Label   string   `json:"label"`
		Value   any      `json:"value"`
		Actions []string `json:"actions"`
		Frame   struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
			W float64 `json:"w"`
			H float64 `json:"h"`
		} `json:"screenshot_frame"`
	} `json:"elements"`
}

func (b *cuaDriverBackend) imageEdge(ctx context.Context, windowID string) int {
	b.mu.Lock()
	edge := b.longEdge[windowID]
	b.mu.Unlock()
	if edge > 0 {
		return edge
	}
	if _, err := b.listWindows(ctx); err == nil {
		b.mu.Lock()
		edge = b.longEdge[windowID]
		b.mu.Unlock()
	}
	return edge
}

// capture takes a screenshot only. Skipping the accessibility walk keeps the
// current snapshot's element tokens valid, so an element read stays usable
// across the scene checks ProcessAdapter runs before acting.
func (b *cuaDriverBackend) capture(ctx context.Context, windowID, bundle string) (ScreenshotPayload, error) {
	pid, window, err := parseCUAWindowID(windowID)
	if err != nil {
		return ScreenshotPayload{}, err
	}
	args := map[string]any{"pid": pid, "window_id": window, "include_accessibility_tree": false}
	// Long edge capped to the window's size in points: screenshot pixels then
	// equal window-local logical points, which is what Command.X/Y mean.
	if edge := b.imageEdge(ctx, windowID); edge > 0 {
		args["max_image_dimension"] = edge
	}
	result, err := b.session.call(ctx, "get_window_state", args)
	if err != nil {
		return ScreenshotPayload{}, err
	}
	var state cuaWindowState
	_ = json.Unmarshal(result.Structured, &state)
	if len(result.Images) == 0 || len(result.Images[0].Data) == 0 {
		return ScreenshotPayload{}, commandError(CodePermissionDenied, "cua-driver 没有返回截图，请检查屏幕录制权限")
	}
	image := result.Images[0]
	mime := image.Mime
	if mime == "" {
		mime = "image/png"
	}
	b.mu.Lock()
	b.captures[windowID] = state.CaptureID
	b.mu.Unlock()
	return ScreenshotPayload{WindowID: windowID, AppName: state.AppName, BundleID: bundle, Title: state.WindowTitle, Mime: mime, Data: base64.StdEncoding.EncodeToString(image.Data)}, nil
}

func (b *cuaDriverBackend) elements(ctx context.Context, windowID, _ string) (ElementsPayload, error) {
	pid, window, err := parseCUAWindowID(windowID)
	if err != nil {
		return ElementsPayload{}, err
	}
	args := map[string]any{"pid": pid, "window_id": window, "tree_format": "elements", "include_screenshot": false, "max_elements": 400}
	if edge := b.imageEdge(ctx, windowID); edge > 0 {
		args["max_image_dimension"] = edge
	}
	result, err := b.session.call(ctx, "get_window_state", args)
	if err != nil {
		return ElementsPayload{}, err
	}
	var state cuaWindowState
	if json.Unmarshal(result.Structured, &state) != nil || state.SnapshotID == "" {
		return ElementsPayload{}, commandError(CodeHelper, "cua-driver 返回了无效元素清单")
	}
	out := ElementsPayload{WindowID: windowID, Truncated: state.Truncated, Elements: make([]Element, 0, len(state.Elements))}
	for _, el := range state.Elements {
		value := ""
		if el.Value != nil {
			value = fmt.Sprint(el.Value)
		}
		out.Elements = append(out.Elements, Element{
			ID:      strconv.Itoa(el.Index),
			Role:    el.Role,
			Label:   el.Label,
			Value:   value,
			X:       el.Frame.X,
			Y:       el.Frame.Y,
			Width:   el.Frame.W,
			Height:  el.Frame.H,
			Enabled: len(el.Actions) > 0,
		})
	}
	b.mu.Lock()
	b.snapshots[windowID] = state.SnapshotID
	b.mu.Unlock()
	return out, nil
}

// dispatch sends one input. element is the row ProcessAdapter matched in a
// fresh element read; nil means a coordinate or focused-element action.
func (b *cuaDriverBackend) dispatch(ctx context.Context, c Command, element *Element) (ActionResult, error) {
	pid, window, err := parseCUAWindowID(c.WindowID)
	if err != nil {
		return ActionResult{}, err
	}
	args := map[string]any{"pid": pid, "window_id": window}
	b.mu.Lock()
	snapshot, capture := b.snapshots[c.WindowID], b.captures[c.WindowID]
	b.mu.Unlock()
	if element != nil {
		if snapshot == "" {
			return ActionResult{}, commandError(CodeStaleObservation, "元素已经变化，请重新读取")
		}
		args["element_token"] = snapshot + ":" + element.ID
	}
	tool := ""
	switch c.Op {
	case OpWindowClick:
		tool = "click"
		if element == nil {
			if c.X == nil || c.Y == nil {
				return ActionResult{}, commandError(CodeBadRequest, "点击需要坐标")
			}
			args["x"], args["y"] = *c.X, *c.Y
			if capture != "" {
				args["capture_id"] = capture
			}
		}
		if button := strings.TrimSpace(c.Button); button != "" && button != "left" {
			args["button"] = button
		}
	case OpWindowType:
		tool = "type_text"
		args["text"] = c.Text
	case OpWindowKey:
		key, modifiers := splitCUAKey(c.Key)
		if key == "" {
			return ActionResult{}, commandError(CodeBadRequest, "按键不能为空")
		}
		args["key"] = key
		if len(modifiers) > 0 {
			args["modifiers"] = modifiers
			// Menu equivalents (cmd+s, ctrl+…) are not delivered to background
			// apps: front the exact window for one real key press, then the
			// driver restores the previous app. Plain keys stay in the background.
			for _, modifier := range modifiers {
				if modifier != "shift" {
					args["delivery_mode"] = "foreground"
				}
			}
		}
		tool = "press_key"
	case OpWindowScroll:
		tool = "scroll"
		direction, amount := cuaScrollDirection(c.DeltaX, c.DeltaY)
		if direction == "" {
			return ActionResult{}, commandError(CodeBadRequest, "滚动需要 delta_x 或 delta_y")
		}
		args["direction"], args["amount"] = direction, amount
		if element == nil {
			if c.X == nil || c.Y == nil {
				return ActionResult{}, commandError(CodeBadRequest, "滚动需要窗口内 x/y")
			}
			args["x"], args["y"] = *c.X, *c.Y
		}
	default:
		return ActionResult{}, commandError(CodeUnsupportedOp, "cua-driver 不支持指令：%s", c.Op)
	}
	result, err := b.session.call(ctx, tool, args)
	if err != nil {
		return ActionResult{}, err
	}
	// Some refusals (e.g. target_focus_unconfirmed) are normal results with
	// effect "refused": nothing was sent, so it must not read as dispatched.
	var outcome struct {
		Effect string `json:"effect"`
		Reason string `json:"reason"`
		Code   string `json:"code"`
	}
	if json.Unmarshal(result.Structured, &outcome) == nil && outcome.Effect == "refused" {
		detail := strings.TrimSpace(firstNonEmptyString(outcome.Reason, outcome.Code, result.Text))
		return ActionResult{}, commandError(CodeStaleObservation, "cua-driver 拒绝执行（%s），输入未发送，请重新观察后再试", detail)
	}
	// The driver may report effect "unverifiable"; Diana never treats a
	// dispatched input as a completed goal, so the result always needs a check.
	return ActionResult{OK: true}, nil
}

func (b *cuaDriverBackend) permissions(ctx context.Context) (Permissions, error) {
	result, err := b.session.call(ctx, "check_permissions", map[string]any{"prompt": false})
	if err != nil {
		return Permissions{}, err
	}
	var p Permissions
	if json.Unmarshal(result.Structured, &p) != nil {
		return p, commandError(CodeHelper, "cua-driver 权限回执无效")
	}
	return p, nil
}

// splitCUAKey turns Diana's "cmd+shift+s" into key "s" plus modifiers.
func splitCUAKey(raw string) (string, []string) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(raw)), "+")
	var modifiers []string
	for _, part := range parts[:len(parts)-1] {
		switch part = strings.TrimSpace(part); part {
		case "command", "⌘":
			part = "cmd"
		case "control":
			part = "ctrl"
		case "alt", "opt":
			part = "option"
		}
		if part != "" {
			modifiers = append(modifiers, part)
		}
	}
	key := strings.TrimSpace(parts[len(parts)-1])
	switch key {
	case "enter":
		key = "return"
	case "esc":
		key = "escape"
	case "backspace":
		key = "delete"
	}
	return key, modifiers
}

// cuaScrollDirection maps a wheel delta to the driver's direction + notches.
// The dominant axis wins; one notch per 40 points, clamped to the driver's 1–50.
func cuaScrollDirection(dx, dy int) (string, int) {
	direction, magnitude := "", 0
	switch {
	case abs(dy) >= abs(dx) && dy > 0:
		direction, magnitude = "down", dy
	case abs(dy) >= abs(dx) && dy < 0:
		direction, magnitude = "up", -dy
	case dx > 0:
		direction, magnitude = "right", dx
	case dx < 0:
		direction, magnitude = "left", -dx
	}
	amount := (magnitude + 39) / 40
	if amount < 1 {
		amount = 1
	}
	if amount > 50 {
		amount = 50
	}
	return direction, amount
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
