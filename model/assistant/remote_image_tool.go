package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const dianaRemoteImageToolName = "remote_image"

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

// 几条约束的来历：网页文字和文件名曾被当成图片内容；模型曾自己编头像链接（#532）；
// 别人说机器人头像换了时，模型凭印象答错，所以先 view_avatar 看 bot_avatar，
// updated_at/change_note 比印象可靠（#808）；只贴链接冒充发图。
func (t *dianaRemoteImageTool) Description() string {
	return "查看或发送网上现有图片和头像，不生成图片、不读本机文件。先 view 加载直链看真实画面，网页文字和文件名不能证明图片内容；确认后 send / send_album 发送，不只贴链接冒充发图。" +
		"看头像用 view_avatar，不编头像链接；有人说你头像换了先看 bot_avatar，结果里的 updated_at、change_note 比你的印象可靠。"
}

func (t *dianaRemoteImageTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"action"}, map[string]any{
		"action":        toolEnumParam("send_album 为 Telegram 相册", "view", "view_avatar", "send", "send_album"),
		"url":           toolStringParam("view 专用，公网图片直链，不是网页地址；从 browser_render 的 images 找，美股 K 线可用 https://finviz.com/chart.ashx?t=代码&p=d"),
		"avatar_source": toolStringParam(avatarSourceBot + `、` + avatarSourceSender + `、` + avatarSourceGroup + `、` + avatarSourceMemberPrefix + `<id>`),
		"image_id":      toolStringParam("send 专用，本轮 view 返回的图片 ID"),
		"image_ids":     toolStringArrayParam("send_album 专用，本轮 view 返回的 2～8 个图片 ID，按发送顺序"),
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
		result, _ := json.Marshal(map[string]any{"image_id": id, "status": "loaded", "message": remoteImageViewMessage(id)})
		return string(result), nil
	case "view_avatar":
		if len(t.images) >= 8 {
			return "", fmt.Errorf("本轮最多读取 8 张图片")
		}
		source := strings.TrimSpace(configToolString(input, "avatar_source"))
		if source == "" {
			return "", fmt.Errorf("view_avatar 需要 avatar_source")
		}
		urls := t.runtime.avatarIdentityImageURLs(ctx, t.event, []string{source})
		if len(urls) == 0 {
			return "", fmt.Errorf("取不到这个头像：来源无效、user_id 不对，或对方没有可见头像，不能据此描述画面")
		}
		now := t.runtime.clock()
		ready, meta, err := loadAvatarImage(ctx, urls[0], now)
		if err != nil {
			// 报错原文要带上：以前只回一句「读取或解码失败」，线上解析超时时谁也看不出原因。
			log.Printf("diana remote_image view_avatar failed: message_id=%s source=%s err=%v", t.event.MessageID, source, err)
			return "", fmt.Errorf("头像读取或解码失败，不能据此描述画面：%w", err)
		}
		id := fmt.Sprintf("remote_image_%d", len(t.images)+1)
		t.images[id] = ready
		t.parts = []llm.ContentPart{{Type: llm.ContentPartImageURL, ImageURL: ready, Detail: "high"}}
		// 只有一张图时，模型分不出「一直是这样」和「刚换成这样」，只好顺着自己之前的说法讲。
		// 平台记录的更新时间和上次看到的指纹一并交给它。
		fields := t.runtime.avatarFreshnessFields(t.event, source, meta, now)
		fields["image_id"] = id
		fields["status"] = "loaded"
		fields["avatar_source"] = source
		fields["message"] = "头像真实画面已附加；需要发出去时用 action=send 和这个 image_id。"
		result, _ := json.Marshal(fields)
		return string(result), nil
	case "send", "send_album":
		album := configToolString(input, "action") == "send_album"
		ids := []string{configToolString(input, "image_id")}
		if album {
			if t.event.Platform != PlatformTelegram {
				return "", fmt.Errorf("相册目前只支持 Telegram")
			}
			ids = attachmentInputStrings(input["image_ids"])
			if len(ids) == 0 {
				// 回放里模型会把相册的 ID 列表填进 image_id。
				ids = attachmentInputStrings(input["image_id"])
			}
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
		finish, since, ok := t.runtime.reserveImageSend(t.event, images)
		if !ok {
			return remoteImageAlreadySentResult(since), nil
		}
		sent := false
		defer func() { finish(sent) }()
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
		sent = true
		return `{"status":"sent","message":"图片已发送到当前会话，不要重复发送。"}`, nil
	default:
		return "", fmt.Errorf("action 必须是 view、view_avatar、send 或 send_album")
	}
}

// remoteImageAlreadySentResult 告诉模型同一张图刚发过、这次没重发。图确实在聊天里，
// 可以照常说「上面这张」，只是别再调 send。
func remoteImageAlreadySentResult(since time.Duration) string {
	result, _ := json.Marshal(map[string]any{
		"status":  "already_sent",
		"message": fmt.Sprintf("同一张图 %d 秒前已经发到当前会话（可能是同时在跑的另一条回复发的），这次没有重发；图就在聊天里，接着用文字说就行，不要再调 send。", int(since.Seconds())),
	})
	return string(result)
}

// remoteImageViewMessage 把「看过」和「发出」分开说清。运行时在每个工具结果后追加
// 「用 send 回复并结束本轮」，回复工具也叫 send：模型曾看完图只用回复工具收尾，
// 还说「上面是宣传图」，群里其实一张图都没有。
func remoteImageViewMessage(id string) string {
	return "真实画面只附加给你看，还没发到聊天，对方看不到。要发就再调 remote_image，action=send、image_id=" + id +
		"（多张用 send_album）；回复工具 send 只发文字，不会带上这张图，没发图就别说「上面这张」。"
}

func (t *dianaRemoteImageTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}
