// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.
package desktopctl

import (
	"bytes"
	"context"
	"encoding/base64"
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
	path string
	gate chan struct{}
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
	return &ProcessAdapter{path: path, gate: make(chan struct{}, 1)}, nil
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
		case CodePermissionDenied, CodeWindowUnknown, CodeBadRequest, CodeUnsupportedOp:
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
func (a *ProcessAdapter) screenshot(ctx context.Context, id, bundle string) (ScreenshotPayload, error) {
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
	raw, err := a.run(ctx, input, c.ExpectedBundleID, args...)
	if err != nil {
		return ActionResult{}, err
	}
	var result ActionResult
	if json.Unmarshal(raw, &result) != nil || !result.OK {
		return ActionResult{}, commandError(CodeHelper, "helper 返回了无效操作回执")
	}
	result.WindowID = c.WindowID
	result.BundleID = c.ExpectedBundleID
	return result, nil
}

var _ io.Writer = (*boundedOutput)(nil)
