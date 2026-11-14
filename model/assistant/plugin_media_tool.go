// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

const dianaPluginMediaToolName = "plugin_media"

// 下载结果只属于本轮会话；模型只能用返回的 ID 查看或发送，不能指定本地路径。
type dianaPluginMediaTool struct {
	runtime *Runtime
	event   MessageEvent
	mu      sync.Mutex
	results map[string]PluginResponse
	sent    map[string]bool
	parts   []llm.ContentPart
}

func (t *dianaPluginMediaTool) Name() string { return dianaPluginMediaToolName }

func (t *dianaPluginMediaTool) Description() string {
	return "主动解析社交链接、下载视频或图集、抽帧读取视频、发送解析结果。resolve 下载但不发送，返回 media_id；view_video 用该 ID 或公网视频直链读取真实画面；send 用 ID 发到当前会话。历史视频用 history_media；获取和发表情包用 sticker。遵守插件开关和大小限制，画面不含音轨，不能凭标题猜视频内容。"
}

func (t *dianaPluginMediaTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"action"}, map[string]any{
		"action":   toolEnumParam("先解析，再按需读取或发送", "resolve", "view_video", "send"),
		"url":      toolStringParam("resolve 的社交网页链接，或 view_video 的公网视频直链；不接受本地路径"),
		"media_id": toolStringParam("本轮 resolve 返回的 ID；send 必填，view_video 与 url 二选一"),
	})
}

func (t *dianaPluginMediaTool) Run(ctx context.Context, input map[string]any) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parts = nil
	if t.runtime == nil {
		return "", fmt.Errorf("媒体工具未配置")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	action := configToolString(input, "action")
	id := strings.TrimSpace(configToolString(input, "media_id"))
	raw := strings.TrimSpace(configToolString(input, "url"))
	encode := func(value any) (string, error) { data, err := json.Marshal(value); return string(data), err }
	check := func(pluginID string) error {
		plugin, _, enabled := t.runtime.pluginWithSettingsForEvent(pluginID, t.event)
		if !enabled || plugin == nil || !pluginSupportsPlatform(withBuiltinPlatformSupport(plugin.Manifest()), t.runtime.currentPlatform(t.event)) {
			return fmt.Errorf("当前会话未启用或不支持插件 %s", pluginID)
		}
		return nil
	}
	if action != "view_video" || id != "" {
		if err := check(resolverPluginID); err != nil {
			return "", err
		}
	}
	switch action {
	case "resolve":
		if err := validatePluginMediaURL(raw); err != nil {
			return "", err
		}
		if len(t.results) >= 5 {
			return "", fmt.Errorf("本轮最多解析 5 个媒体链接")
		}
		// 保留真实身份与群等级，但不把原消息里的其它链接重新解析一次。
		event := t.event
		event.Platform = t.runtime.currentPlatform(t.event)
		event.RawMessage, event.Segments, event.Quoted = "", nil, nil
		resp, err := t.runtime.plugins.RunOneWithGroupOverrides(ctx, resolverPluginID, PluginRequest{
			Event: event, Text: raw, OwnerID: t.runtime.effectiveConfigForEvent(t.event).OwnerIDForEvent(t.event),
			Channel: t.runtime.channel, AppLogs: t.runtime.appLogWriter(),
			TranslateResolver:       t.runtime.translateResolverText,
			SandboxedBrowserEnabled: t.runtime.sandboxedBrowserEnabled(t.event),
		}, t.runtime.pluginOverridesForEvent(t.event), t.runtime.pluginSettingOverridesForEvent(t.event))
		if err != nil {
			return "", err
		}
		if resp == nil || (resolverVisibleReply(*resp) == "" && len(resp.ImageURLs)+len(resp.VideoURLs)+len(resp.ForwardMessages) == 0) {
			return encode(map[string]any{"ok": false, "message": "没有可用解析结果；可能受平台或群等级限制。未发送任何内容。"})
		}
		if t.results == nil {
			t.results = map[string]PluginResponse{}
		}
		id = fmt.Sprintf("media_%d", len(t.results)+1)
		t.results[id] = *resp
		cleanupLocalMediaFilesLater(resp.VideoURLs, resolverLocalMediaTTL)
		return encode(map[string]any{"ok": true, "media_id": id, "context": resp.Context, "videos": len(resp.VideoURLs), "images": len(resp.ImageURLs), "message": "已解析，尚未发送；视频内容需 view_video 查看。"})
	case "send":
		resp, ok := t.results[id]
		if !ok {
			return "", fmt.Errorf("media_id 不属于本轮解析结果，请先 resolve")
		}
		if t.sent[id] {
			return encode(map[string]any{"ok": true, "message": "该结果已发送，不再重复发送。"})
		}
		if _, err := t.runtime.deliverResolverResponse(ctx, t.event, resp); err != nil {
			return "", err
		}
		if t.sent == nil {
			t.sent = map[string]bool{}
		}
		t.sent[id] = true
		return encode(map[string]any{"ok": true, "message": "解析结果已发送到当前会话，不要重复发送。"})
	case "view_video":
		if err := check(fileParserPluginID); err != nil {
			return "", err
		}
		var sources []string
		if id != "" {
			if raw != "" {
				return "", fmt.Errorf("media_id 与 url 二选一")
			}
			resp, ok := t.results[id]
			if !ok {
				return "", fmt.Errorf("media_id 不属于本轮解析结果，请先 resolve")
			}
			sources = resp.VideoURLs
		} else {
			if err := validatePluginMediaURL(raw); err != nil {
				return "", err
			}
			sources = []string{raw}
		}
		if len(sources) == 0 {
			return "", fmt.Errorf("解析结果不含可读取的视频")
		}
		ctx, cancel := context.WithTimeout(t.runtime.withFileParserVideoLimit(ctx, t.event), 2*time.Minute)
		defer cancel()
		frames, reason := extractVideoContextFramesDetailed(ctx, sources, 0)
		// 抽帧目录还含暂存的视频副本，转成 data URL 后整目录删除。
		defer cleanupVideoContextFrames(frames)
		for _, image := range llmReadyImageURLs(ctx, frames) {
			t.parts = append(t.parts, llm.ContentPart{Type: llm.ContentPartImageURL, ImageURL: image, Detail: "high"})
		}
		if len(t.parts) == 0 {
			return "", fmt.Errorf("视频未读出画面：%s", firstNonEmpty(reason, "画面加载失败"))
		}
		return encode(map[string]any{"ok": true, "frames": len(t.parts), "message": "视频采样画面已附加，不包含音轨，也不代表完整逐帧内容。"})
	default:
		return "", fmt.Errorf("action 必须是 resolve、view_video 或 send")
	}
}

func validatePluginMediaURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("url 必须是 HTTP(S) 公网链接，不接受本地路径或带凭据的 URL")
	}
	// DNS、重定向和内网地址由下载层的 netguard 校验。
	return nil
}

func (t *dianaPluginMediaTool) ToolResultParts(string) []llm.ContentPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]llm.ContentPart(nil), t.parts...)
}
