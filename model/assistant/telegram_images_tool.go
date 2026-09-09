package assistant

import (
	"context"
	"fmt"
	"strings"
)

type dianaTelegramImagesTool struct {
	runtime *Runtime
	event   MessageEvent
}

func (t *dianaTelegramImagesTool) Name() string { return "diana.telegram_images" }
func (t *dianaTelegramImagesTool) Description() string {
	return "按当前会话历史消息 ID 复用 Telegram 原有图片的 file_id，直接发送，不重新下载上传。传一条消息发送单图，2 至 10 条消息发送相册；每条选该消息的第一张照片。不接受猜测的 file_id 或其他会话消息。需要判断图片内容时先用 diana.history_media 看图。"
}
func (t *dianaTelegramImagesTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"message_ids"}, map[string]any{"message_ids": toolStringArrayParam("当前会话内含照片的 1 至 10 个历史消息 ID，按发送顺序。")})
}
func attachmentInputStrings(value any) []string {
	var result []string
	switch values := value.(type) {
	case []string:
		result = append(result, values...)
	case []any:
		for _, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil
			}
			result = append(result, text)
		}
	}
	return result
}
func (t *dianaTelegramImagesTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t.event.Platform != PlatformTelegram {
		return "", fmt.Errorf("此工具仅支持 Telegram")
	}
	ids := attachmentInputStrings(input["message_ids"])
	if len(ids) < 1 || len(ids) > 10 {
		return "", fmt.Errorf("需要 1 至 10 个消息 ID")
	}
	images := make([]string, 0, len(ids))
	for _, id := range ids {
		source, found := t.runtime.findSemanticReferenceEvent(ctx, t.event, strings.TrimSpace(id))
		if quote := t.event.Quoted; quote != nil && quote.MessageID == strings.TrimSpace(id) && (quote.GroupID == "" || quote.GroupID == t.event.GroupID) {
			source = t.event
			source.Segments = quote.Segments
			found = true
		}
		if strings.TrimSpace(id) == t.event.MessageID {
			source = t.event
			found = true
		}
		if !found {
			return "", fmt.Errorf("当前会话找不到图片消息 %s", id)
		}
		fileID := ""
		for _, segment := range source.Segments {
			if segment.Type == "image" && segment.Data["sub_type"] != "telegram_sticker" {
				fileID = strings.TrimSpace(segment.Data["file_id"])
				if fileID != "" {
					break
				}
			}
		}
		if fileID == "" {
			return "", fmt.Errorf("消息 %s 没有可复用的 Telegram 照片 file_id", id)
		}
		images = append(images, fileID)
	}
	if err := t.runtime.sendOutgoing(ctx, t.event, OutgoingMessage{ImageURLs: images, ImageAlbum: len(images) > 1}); err != nil {
		return "", err
	}
	return `{"status":"sent","message":"已复用 Telegram 图片发送到当前会话。"}`, nil
}
