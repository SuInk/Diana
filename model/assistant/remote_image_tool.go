package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/llm"
)

const dianaRemoteImageToolName = "diana.remote_image"

// The cache belongs to one reply, so send cannot access another conversation's images.
type dianaRemoteImageTool struct {
	runtime *Runtime
	event   MessageEvent
	mu      sync.Mutex
	images  map[string]string
	parts   []llm.ContentPart
}

func newDianaRemoteImageTool(r *Runtime, event MessageEvent) *dianaRemoteImageTool {
	return &dianaRemoteImageTool{runtime: r, event: event, images: make(map[string]string)}
}

func (t *dianaRemoteImageTool) Name() string { return dianaRemoteImageToolName }

func (t *dianaRemoteImageTool) Description() string {
	return "读取或发送网上现有图片，不生成图片。先用 action=view 和图片直链加载真实画面，附件会交给下一轮模型；网页文字、文件名不能证明图片内容。查看确认符合用户要求后，用 action=send 和返回的 image_id 发送单图；用 action=send_album 和 image_ids 将已查看的图片作为 Telegram 相册发送。不要只输出链接冒充发图。不能读取本机文件。"
}

func (t *dianaRemoteImageTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"action"}, map[string]any{
		"action":    toolEnumParam("先看图，再发送已查看的图；send_album 将多图合成 Telegram 相册。", "view", "send", "send_album"),
		"url":       toolStringParam("view 必填：公网 HTTP(S) 图片直链，不是网页地址。"),
		"image_id":  toolStringParam("send 必填：本轮 view 返回的图片 ID。"),
		"image_ids": toolStringArrayParam("send_album 必填：本轮 view 返回的 2 至 8 个图片 ID，按发送顺序排列。"),
	})
}

func (t *dianaRemoteImageTool) Run(ctx context.Context, input map[string]any) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parts = nil
	switch configToolString(input, "action") {
	case "view":
		if len(t.images) >= 8 {
			return "", fmt.Errorf("本轮最多读取 8 张远程图片")
		}
		url := strings.TrimSpace(configToolString(input, "url"))
		image, err := fetchImageAsDataURL(ctx, url)
		if err != nil {
			return "", fmt.Errorf("读取图片失败，不能据此描述画面：%w", err)
		}
		id := fmt.Sprintf("remote_image_%d", len(t.images)+1)
		t.images[id] = image
		t.parts = []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: image, Detail: "high"}}
		result, _ := json.Marshal(map[string]any{"image_id": id, "status": "loaded", "message": "真实画面已附加，请先检查内容再决定是否发送。"})
		return string(result), nil
	case "send", "send_album":
		album := configToolString(input, "action") == "send_album"
		ids := []string{configToolString(input, "image_id")}
		if album {
			if t.event.Platform != PlatformTelegram {
				return "", fmt.Errorf("相册目前只支持 Telegram")
			}
			ids = attachmentInputStrings(input["image_ids"])
			if len(ids) < 2 || len(ids) > 8 {
				return "", fmt.Errorf("相册需要 2 至 8 张已查看的图片")
			}
		}
		images := make([]string, 0, len(ids))
		for _, id := range ids {
			image, ok := t.images[id]
			if !ok {
				return "", fmt.Errorf("图片 ID 无效，请先 view 读取图片")
			}
			images = append(images, image)
		}
		shared, paths, err := t.runtime.shareAgentImages(ctx, t.event.Platform, images)
		if err != nil {
			return "", err
		}
		defer func() {
			for _, path := range paths {
				cleanupLocalMediaFile(path)
			}
		}()
		if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{ImageURLs: shared, ImageAlbum: album})); err != nil {
			return "", fmt.Errorf("发送图片失败：%w", err)
		}
		return `{"status":"sent","message":"图片已发送到当前会话，不要重复发送。"}`, nil
	default:
		return "", fmt.Errorf("action 必须是 view、send 或 send_album")
	}
}

func (t *dianaRemoteImageTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}
