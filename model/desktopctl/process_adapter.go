// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ProcessAdapter owns the safety contract of local desktop input: observation
// versions bound to a captured scene, one-shot approvals and post-action evidence.
// The executor behind it is a desktopBackend — the native macOS helper or
// cua-driver — whose path is operator configuration, never a model parameter.
type ProcessAdapter struct {
	backend      desktopBackend
	path         string
	gate         chan struct{}
	sequence     chan struct{}
	observations map[int64]processObservation
	approvals    map[string]ActionApproval
}

// desktopBackend only executes. It must not decide whether an action is
// allowed: ProcessAdapter checks observation, scene and approval first.
type desktopBackend interface {
	listWindows(ctx context.Context) ([]WindowInfo, error)
	// capture returns a PNG of exactly one window.
	capture(ctx context.Context, windowID, bundle string) (ScreenshotPayload, error)
	elements(ctx context.Context, windowID, bundle string) (ElementsPayload, error)
	// dispatch sends one input; element is the matched row of a fresh read.
	dispatch(ctx context.Context, c Command, element *Element) (ActionResult, error)
	permissions(ctx context.Context) (Permissions, error)
}

func newProcessAdapter(backend desktopBackend, path string) *ProcessAdapter {
	return &ProcessAdapter{backend: backend, path: path, gate: make(chan struct{}, 1), sequence: make(chan struct{}, 1), observations: map[int64]processObservation{}, approvals: map[string]ActionApproval{}}
}

// NewProcessAdapter drives the native macOS helper (desktopctl-helper).
func NewProcessAdapter(path string) (*ProcessAdapter, error) {
	if err := checkExecutable(path, "desktop helper"); err != nil {
		return nil, err
	}
	a := newProcessAdapter(nil, path)
	a.backend = helperBackend{adapter: a}
	return a, nil
}

// NewCUADriverAdapter drives trycua's cua-driver over MCP stdio.
func NewCUADriverAdapter(path string) (*ProcessAdapter, error) {
	backend, err := newCUADriverBackend(path)
	if err != nil {
		return nil, err
	}
	return newProcessAdapter(backend, path), nil
}

// Close stops a long-lived executor process; the native helper has none.
func (a *ProcessAdapter) Close() {
	if closer, ok := a.backend.(interface{ close() }); ok {
		closer.close()
	}
}

func checkExecutable(path, label string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s requires an absolute path", label)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("%s is not executable", label)
	}
	return nil
}

// helperBackend speaks the native helper's argv/stdout protocol.
type helperBackend struct{ adapter *ProcessAdapter }

func (h helperBackend) listWindows(ctx context.Context) ([]WindowInfo, error) {
	raw, err := h.adapter.run(ctx, "", "", "list")
	if err != nil {
		return nil, err
	}
	var p WindowsPayload
	if err = json.Unmarshal(raw, &p); err != nil {
		return nil, commandError(CodeHelper, "helper 返回了无效窗口清单")
	}
	return p.Windows, nil
}

func (h helperBackend) capture(ctx context.Context, id, bundle string) (ScreenshotPayload, error) {
	raw, err := h.adapter.run(ctx, "", bundle, "screenshot", id)
	if err != nil {
		return ScreenshotPayload{}, err
	}
	if len(raw) < 8 || !bytes.Equal(raw[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return ScreenshotPayload{}, commandError(CodeHelper, "helper 没有返回 PNG")
	}
	return ScreenshotPayload{WindowID: id, BundleID: bundle, Mime: "image/png", Data: base64.StdEncoding.EncodeToString(raw)}, nil
}

func (h helperBackend) elements(ctx context.Context, id, bundle string) (ElementsPayload, error) {
	raw, err := h.adapter.run(ctx, "", bundle, "elements", id)
	if err != nil {
		return ElementsPayload{}, err
	}
	var p ElementsPayload
	if json.Unmarshal(raw, &p) != nil {
		return p, commandError(CodeHelper, "helper 返回了无效元素清单")
	}
	return p, nil
}

func (h helperBackend) dispatch(ctx context.Context, c Command, element *Element) (ActionResult, error) {
	var input string
	var args []string
	switch c.Op {
	case OpWindowClick:
		if element != nil {
			input, args = string(rawJSON(*element)), []string{"element-click", c.WindowID, c.ElementID}
		} else {
			if c.X == nil || c.Y == nil {
				return ActionResult{}, commandError(CodeBadRequest, "点击需要坐标")
			}
			args = []string{"click", c.WindowID, strconv.FormatFloat(*c.X, 'f', -1, 64), strconv.FormatFloat(*c.Y, 'f', -1, 64), c.Button}
		}
	case OpWindowType:
		input, args = c.Text, []string{"type", c.WindowID, "--stdin"}
	case OpWindowKey:
		args = []string{"key", c.WindowID, c.Key}
	case OpWindowScroll:
		if c.X == nil || c.Y == nil {
			return ActionResult{}, commandError(CodeBadRequest, "滚动需要窗口内 x/y")
		}
		args = []string{"scroll", c.WindowID, strconv.FormatFloat(*c.X, 'f', -1, 64), strconv.FormatFloat(*c.Y, 'f', -1, 64), strconv.Itoa(c.DeltaX), strconv.Itoa(c.DeltaY)}
	default:
		return ActionResult{}, commandError(CodeUnsupportedOp, "helper 不支持指令：%s", c.Op)
	}
	raw, err := h.adapter.run(ctx, input, c.ExpectedBundleID, args...)
	if err != nil {
		return ActionResult{}, err
	}
	var result ActionResult
	if json.Unmarshal(raw, &result) != nil || !result.OK {
		return ActionResult{}, commandError(CodeHelper, "helper 返回了无效操作回执")
	}
	return result, nil
}

func (h helperBackend) permissions(ctx context.Context) (Permissions, error) {
	raw, err := h.adapter.run(ctx, "", "", "permissions")
	if err != nil {
		return Permissions{}, err
	}
	var p Permissions
	if json.Unmarshal(raw, &p) != nil {
		return p, commandError(CodeHelper, "helper 权限回执无效")
	}
	return p, nil
}

type boundedOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedOutput) Len() int       { return b.buffer.Len() }
func (b *boundedOutput) String() string { return b.buffer.String() }
func (b *boundedOutput) Bytes() []byte  { return b.buffer.Bytes() }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("desktop helper output exceeds limit")
	}
	return b.buffer.Write(p)
}
func (a *ProcessAdapter) run(ctx context.Context, input string, bundle string, args ...string) ([]byte, error) {
	select {
	case a.gate <- struct{}{}:
		defer func() { <-a.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, a.path, args...)
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(os.Environ(), "DIANA_TARGET_BUNDLE="+bundle)
	out := &boundedOutput{limit: 32 << 20}
	errout := &boundedOutput{limit: 16 << 10}
	cmd.Stdout = out
	cmd.Stderr = errout
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(errout.String())
		code, _, _ := strings.Cut(msg, ":")
		switch code {
		case CodePermissionDenied, CodeWindowUnknown, CodeBadRequest, CodeUnsupportedOp, CodeTakeover, CodeStaleObservation:
		default:
			code = CodeHelper
		}
		if msg == "" {
			msg = "桌面 helper 执行失败"
		}
		return nil, commandError(code, "%s", msg)
	}
	return out.Bytes(), nil
}
func (a *ProcessAdapter) ListWindows(ctx context.Context) ([]WindowInfo, error) {
	return a.backend.listWindows(ctx)
}
func (a *ProcessAdapter) Screenshot(ctx context.Context, id string) (ScreenshotPayload, error) {
	return a.screenshot(ctx, id, "")
}
func (a *ProcessAdapter) capture(ctx context.Context, id, bundle string) (ScreenshotPayload, error) {
	return a.backend.capture(ctx, id, bundle)
}
func (a *ProcessAdapter) Click(ctx context.Context, c Command) (ActionResult, error) {
	if c.ElementID == "" && (c.X == nil || c.Y == nil) {
		return ActionResult{}, commandError(CodeBadRequest, "点击需要坐标")
	}
	return a.action(ctx, c, OpWindowClick)
}
func (a *ProcessAdapter) TypeText(ctx context.Context, c Command) (ActionResult, error) {
	return a.action(ctx, c, OpWindowType)
}
func (a *ProcessAdapter) PressKey(ctx context.Context, c Command) (ActionResult, error) {
	return a.action(ctx, c, OpWindowKey)
}

// action runs op for c. The approval key hashes c as the caller sent it, so op
// is passed beside it rather than written into the command.
func (a *ProcessAdapter) action(ctx context.Context, c Command, op string) (ActionResult, error) {
	if err := a.lockSequence(ctx); err != nil {
		return ActionResult{}, err
	}
	defer a.unlockSequence()
	before, ok := a.observations[c.Observation]
	if !ok || before.window != c.WindowID || before.bundle != c.ExpectedBundleID || time.Since(before.at) > 2*time.Minute {
		return ActionResult{}, commandError(CodeStaleObservation, "先截图或读取元素，再传入对应 observation；观察有效期为两分钟")
	}
	current, err := a.capture(ctx, c.WindowID, c.ExpectedBundleID)
	if err != nil {
		return ActionResult{}, err
	}
	if !sameScene(before.scene, current.Data) {
		delete(a.observations, c.Observation)
		return ActionResult{}, commandError(CodeStaleObservation, "窗口画面已经变化，请重新观察后再操作")
	}
	var target *Element
	if c.ElementID != "" {
		if before.elements == nil {
			return ActionResult{}, commandError(CodeStaleObservation, "元素点击须使用 desktop_elements 的 observation")
		}
		currentElements, e := a.backend.elements(ctx, c.WindowID, c.ExpectedBundleID)
		if e != nil {
			return ActionResult{}, e
		}
		if !bytes.Equal(rawJSON(currentElements.Elements), rawJSON(before.elements)) {
			return ActionResult{}, commandError(CodeStaleObservation, "元素已经变化，请重新读取")
		}
		for index, el := range before.elements {
			if el.ID == c.ElementID && el.Enabled {
				target = &before.elements[index]
			}
		}
		if target == nil {
			return ActionResult{}, commandError(CodeBadRequest, "元素不存在或不可操作")
		}
	}
	if err := a.authorizeAction(c, current, before.elements); err != nil {
		return ActionResult{}, err
	}
	// Consume before sending: a timeout or missing receipt must never permit replay.
	a.observations = map[int64]processObservation{}
	sent := c
	sent.Op = op
	result, err := a.backend.dispatch(ctx, sent, target)
	if err != nil {
		return ActionResult{}, err
	}
	result.OK = true
	result.Op = op
	result.WindowID = c.WindowID
	result.BundleID = c.ExpectedBundleID
	result.NeedsVerification = true
	// A failed post-action capture does not turn dispatched input into a retryable failure.
	evidence, captureErr := a.capture(ctx, c.WindowID, c.ExpectedBundleID)
	if captureErr != nil {
		result.VerificationError = captureErr.Error()
	} else {
		evidence.Observation = a.remember(evidence, nil)
		result.Observation = evidence.Observation
		result.Evidence = &evidence
	}
	return result, nil
}

var _ io.Writer = (*boundedOutput)(nil)

type processObservation struct {
	window, bundle string
	// scene is the capture the observation was taken from, compared with
	// sameScene so a blinking caret does not invalidate it.
	scene    string
	at       time.Time
	elements []Element
}

func (a *ProcessAdapter) lockSequence(ctx context.Context) error {
	select {
	case a.sequence <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-a.sequence
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (a *ProcessAdapter) unlockSequence() { <-a.sequence }
func (a *ProcessAdapter) remember(p ScreenshotPayload, elements []Element) int64 {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0
	}
	id := int64(binary.BigEndian.Uint64(raw[:]) & ((1 << 53) - 1))
	if id == 0 {
		id = 1
	}
	// One latest observation per window. Bound total in-memory retention.
	for key, old := range a.observations {
		if old.window == p.WindowID || time.Since(old.at) > 2*time.Minute || len(a.observations) >= 64 {
			delete(a.observations, key)
		}
	}
	a.observations[id] = processObservation{window: p.WindowID, bundle: p.BundleID, scene: p.Data, at: time.Now(), elements: elements}
	return id
}
func (a *ProcessAdapter) screenshot(ctx context.Context, id, bundle string) (ScreenshotPayload, error) {
	if err := a.lockSequence(ctx); err != nil {
		return ScreenshotPayload{}, err
	}
	defer a.unlockSequence()
	p, err := a.capture(ctx, id, bundle)
	if err == nil {
		p.Observation = a.remember(p, nil)
	}
	return p, err
}
func (a *ProcessAdapter) Elements(ctx context.Context, c Command) (ElementsPayload, error) {
	if err := a.lockSequence(ctx); err != nil {
		return ElementsPayload{}, err
	}
	defer a.unlockSequence()
	// Capture both sides of the tree read to reject a moving scene.
	before, err := a.capture(ctx, c.WindowID, c.ExpectedBundleID)
	if err != nil {
		return ElementsPayload{}, err
	}
	p, err := a.backend.elements(ctx, c.WindowID, c.ExpectedBundleID)
	if err != nil {
		return p, err
	}
	after, err := a.capture(ctx, c.WindowID, c.ExpectedBundleID)
	if err != nil {
		return p, err
	}
	if !sameScene(before.Data, after.Data) {
		return ElementsPayload{}, commandError(CodeStaleObservation, "读取期间窗口变化，请重新观察")
	}
	p.WindowID = c.WindowID
	p.Observation = a.remember(after, p.Elements)
	return p, nil
}
func (a *ProcessAdapter) Scroll(ctx context.Context, c Command) (ActionResult, error) {
	if c.X == nil || c.Y == nil {
		return ActionResult{}, commandError(CodeBadRequest, "滚动需要窗口内 x/y")
	}
	return a.action(ctx, c, OpWindowScroll)
}
func (a *ProcessAdapter) Permissions(ctx context.Context) (Permissions, error) {
	return a.backend.permissions(ctx)
}
