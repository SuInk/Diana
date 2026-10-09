// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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

// ProcessAdapter runs a locally installed helper without a shell. The path is
// operator configuration, never a model/tool parameter.
type ProcessAdapter struct {
	path         string
	gate         chan struct{}
	sequence     chan struct{}
	observations map[int64]processObservation
	approvals    map[string]ActionApproval
}

func NewProcessAdapter(path string) (*ProcessAdapter, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("desktop helper requires an absolute path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("desktop helper is not executable")
	}
	return &ProcessAdapter{path: path, gate: make(chan struct{}, 1), sequence: make(chan struct{}, 1), observations: map[int64]processObservation{}, approvals: map[string]ActionApproval{}}, nil
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
	raw, err := a.run(ctx, "", "", "list")
	if err != nil {
		return nil, err
	}
	var p WindowsPayload
	if err = json.Unmarshal(raw, &p); err != nil {
		return nil, commandError(CodeHelper, "helper 返回了无效窗口清单")
	}
	return p.Windows, nil
}
func (a *ProcessAdapter) Screenshot(ctx context.Context, id string) (ScreenshotPayload, error) {
	return a.screenshot(ctx, id, "")
}
func (a *ProcessAdapter) capture(ctx context.Context, id, bundle string) (ScreenshotPayload, error) {
	raw, err := a.run(ctx, "", bundle, "screenshot", id)
	if err != nil {
		return ScreenshotPayload{}, err
	}
	if len(raw) < 8 || !bytes.Equal(raw[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return ScreenshotPayload{}, commandError(CodeHelper, "helper 没有返回 PNG")
	}
	return ScreenshotPayload{WindowID: id, BundleID: bundle, Mime: "image/png", Data: base64.StdEncoding.EncodeToString(raw)}, nil
}
func (a *ProcessAdapter) Click(ctx context.Context, c Command) (ActionResult, error) {
	if c.ElementID != "" {
		return a.action(ctx, c, "", "element-click", c.WindowID, c.ElementID)
	}
	if c.X == nil || c.Y == nil {
		return ActionResult{}, commandError(CodeBadRequest, "点击需要坐标")
	}
	return a.action(ctx, c, "", "click", c.WindowID, strconv.FormatFloat(*c.X, 'f', -1, 64), strconv.FormatFloat(*c.Y, 'f', -1, 64), c.Button)
}
func (a *ProcessAdapter) TypeText(ctx context.Context, c Command) (ActionResult, error) {
	return a.action(ctx, c, c.Text, "type", c.WindowID, "--stdin")
}
func (a *ProcessAdapter) PressKey(ctx context.Context, c Command) (ActionResult, error) {
	return a.action(ctx, c, "", "key", c.WindowID, c.Key)
}
func (a *ProcessAdapter) action(ctx context.Context, c Command, input string, args ...string) (ActionResult, error) {
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
	if sha256.Sum256([]byte(current.Data)) != before.hash {
		delete(a.observations, c.Observation)
		return ActionResult{}, commandError(CodeStaleObservation, "窗口画面已经变化，请重新观察后再操作")
	}
	if c.ElementID != "" {
		if before.elements == nil {
			return ActionResult{}, commandError(CodeStaleObservation, "元素点击须使用 desktop_elements 的 observation")
		}
		raw, e := a.run(ctx, "", c.ExpectedBundleID, "elements", c.WindowID)
		if e != nil {
			return ActionResult{}, e
		}
		var currentElements ElementsPayload
		if json.Unmarshal(raw, &currentElements) != nil {
			return ActionResult{}, commandError(CodeHelper, "元素数据无效")
		}
		if !bytes.Equal(rawJSON(currentElements.Elements), rawJSON(before.elements)) {
			return ActionResult{}, commandError(CodeStaleObservation, "元素已经变化，请重新读取")
		}
		found := false
		for _, el := range before.elements {
			if el.ID == c.ElementID && el.Enabled {
				found = true
				input = string(rawJSON(el))
			}
		}
		if !found {
			return ActionResult{}, commandError(CodeBadRequest, "元素不存在或不可操作")
		}
	}
	if err := a.authorizeAction(c, current, before.elements); err != nil {
		return ActionResult{}, err
	}
	// Consume before sending: a timeout or missing receipt must never permit replay.
	a.observations = map[int64]processObservation{}
	raw, err := a.run(ctx, input, c.ExpectedBundleID, args...)
	if err != nil {
		return ActionResult{}, err
	}
	var result ActionResult
	if json.Unmarshal(raw, &result) != nil || !result.OK {
		return ActionResult{}, commandError(CodeHelper, "helper 返回了无效操作回执")
	}
	result.Op = c.Op
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
	hash           [32]byte
	at             time.Time
	elements       []Element
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
	a.observations[id] = processObservation{window: p.WindowID, bundle: p.BundleID, hash: sha256.Sum256([]byte(p.Data)), at: time.Now(), elements: elements}
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
	raw, err := a.run(ctx, "", c.ExpectedBundleID, "elements", c.WindowID)
	if err != nil {
		return ElementsPayload{}, err
	}
	var p ElementsPayload
	if json.Unmarshal(raw, &p) != nil {
		return p, commandError(CodeHelper, "helper 返回了无效元素清单")
	}
	after, err := a.capture(ctx, c.WindowID, c.ExpectedBundleID)
	if err != nil {
		return p, err
	}
	if before.Data != after.Data {
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
	return a.action(ctx, c, "", "scroll", c.WindowID, strconv.FormatFloat(*c.X, 'f', -1, 64), strconv.FormatFloat(*c.Y, 'f', -1, 64), strconv.Itoa(c.DeltaX), strconv.Itoa(c.DeltaY))
}
func (a *ProcessAdapter) Permissions(ctx context.Context) (Permissions, error) {
	raw, err := a.run(ctx, "", "", "permissions")
	if err != nil {
		return Permissions{}, err
	}
	var p Permissions
	if json.Unmarshal(raw, &p) != nil {
		return p, commandError(CodeHelper, "helper 权限回执无效")
	}
	return p, nil
}
