// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// Each wrapper and its captured image IDs belong to exactly one reply.
type dianaScreenshotTool struct {
	source  agent.ToolResultPartsTool
	runtime *Runtime
	event   MessageEvent
	mu      sync.Mutex
	images  map[string]string
	parts   []llm.ContentPart
}

func (r *Runtime) wrapScreenshotTools(registry *agent.ToolRegistry, event MessageEvent) {
	for _, name := range []string{"browser_screenshot", "webpage_screenshot"} {
		tool, ok := registry.Get(name)
		if !ok {
			continue
		}
		if _, wrapped := tool.(*dianaScreenshotTool); wrapped {
			continue
		}
		source, ok := tool.(agent.ToolResultPartsTool)
		if !ok {
			continue
		}
		registry.Register(&dianaScreenshotTool{source: source, runtime: r, event: event, images: map[string]string{}})
	}
}

func (t *dianaScreenshotTool) Name() string          { return t.source.Name() }
func (t *dianaScreenshotTool) RepeatableCalls() bool { return true }
func (t *dianaScreenshotTool) Description() string {
	return t.source.Description() + " action=capture（默认）把本次截图交给模型查看，并返回 image_id；确认画面后，action=send 和该 ID 将原图发送到当前会话。图片 ID 仅本轮有效，不能读取任何本机文件，也不能指定其他接收者。"
}
func (t *dianaScreenshotTool) InputSchema() map[string]any {
	base := toolObjectSchema(nil, map[string]any{})
	if typed, ok := t.source.(agent.ToolInputSchema); ok {
		base = typed.InputSchema()
	}
	properties, ok := base["properties"].(map[string]any)
	if !ok {
		properties = map[string]any{}
		base["properties"] = properties
	}
	properties["action"] = toolEnumParam("capture 查看新截图；send 发送本轮已查看的截图", "capture", "send")
	properties["image_id"] = toolStringParam("send 必填：本轮 capture 返回的图片 ID")
	delete(base, "required") // Capture-specific requirements are enforced by the source.
	return base
}
func (t *dianaScreenshotTool) Run(ctx context.Context, input map[string]any) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parts = nil
	switch configToolString(input, "action") {
	case "", "capture":
		if len(t.images) >= 8 {
			return "", fmt.Errorf("本轮最多保留 8 张截图")
		}
		output, err := t.source.Run(ctx, input)
		if err != nil {
			return "", err
		}
		parts := t.source.ToolResultParts(output)
		if len(parts) != 1 || parts[0].Type != llm.ContentPartImageURL || parts[0].ImageURL == "" {
			return "", fmt.Errorf("截图没有返回可查看的真实图片")
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			return "", err
		}
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", err
		}
		id := "screenshot_" + hex.EncodeToString(token[:])
		t.images[id] = parts[0].ImageURL
		t.parts = parts
		result["image_id"] = id
		result["message"] = "真实截图已附加；需要发送原图时用 action=send 和本次 image_id。"
		data, err := json.Marshal(result)
		return string(data), err
	case "send":
		id := configToolString(input, "image_id")
		image, ok := t.images[id]
		if !ok {
			return "", fmt.Errorf("截图 ID 无效，请先在本轮 capture 查看截图")
		}
		shared, paths, err := t.runtime.shareAgentImages(ctx, t.event.Platform, []string{image})
		if err != nil {
			return "", err
		}
		defer func() {
			for _, path := range paths {
				cleanupLocalMediaFile(path)
			}
		}()
		if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{ImageURLs: shared})); err != nil {
			return "", fmt.Errorf("发送截图失败：%w", err)
		}
		delete(t.images, id)
		return `{"status":"sent","message":"截图已发送到当前会话，不要重复发送。"}`, nil
	default:
		return "", fmt.Errorf("action 必须是 capture 或 send")
	}
}
func (t *dianaScreenshotTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}
