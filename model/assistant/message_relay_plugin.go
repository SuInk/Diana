// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
)

const (
	messageRelayPluginID  = "official.message-relay"
	messageRelayEndpoints = "endpoints"
)

// MessageRelayPlugin mirrors messages between explicitly selected platform
// conversations. It never asks the LLM for a reply.
type MessageRelayPlugin struct{}

func NewMessageRelayPlugin() *MessageRelayPlugin { return &MessageRelayPlugin{} }

func (p *MessageRelayPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID: messageRelayPluginID, Name: "消息互通", Version: "0.1.0",
		Description: "在选定的平台群聊或私聊之间双向转发消息。",
		Official:    true, BuiltIn: true, DefaultDisabled: true,
		Permissions: []string{"读取选定会话的消息", "向选定群聊或私聊发送消息"},
		Settings: []PluginSettingSpec{{
			Key: messageRelayEndpoints, Label: "互通端点", Type: PluginSettingTypeRelayEndpoints,
			Default:     []map[string]any{},
			Description: "至少选择两个端点；任一端点收到的消息会转发到其余端点。",
		}},
	}
}

func (*MessageRelayPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}

type messageRelayEndpoint struct{ ProfileID, Platform, Kind, TargetID string }

func (p *MessageRelayPlugin) RelayEvent(ctx context.Context, req PluginRequest) error {
	event := req.Event
	if req.Channel == nil || (event.Kind != EventKindGroup && event.Kind != EventKindPrivate) {
		return nil
	}
	// Most transports do not echo a bot's own messages, but OneBot can. Never
	// relay a self echo or two bridge endpoints can amplify each other forever.
	if strings.TrimSpace(event.SelfID) != "" && strings.TrimSpace(event.UserID) == strings.TrimSpace(event.SelfID) {
		return nil
	}
	endpoints := relayEndpoints(req.Settings[messageRelayEndpoints])
	if len(endpoints) < 2 {
		return nil
	}
	source := relayEndpointForEvent(endpoints, event)
	if source < 0 {
		return nil
	}
	text := strings.TrimSpace(PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	images, videos := relayMedia(event.Segments)
	if text == "" && len(images) == 0 && len(videos) == 0 {
		return nil
	}
	prefix := fmt.Sprintf("【%s · %s】", relayPlatformLabel(event.Platform), event.SenderNameOrID())
	if text == "" {
		text = prefix
	} else {
		text = prefix + "\n" + text
	}
	var failures []string
	for index, endpoint := range endpoints {
		if index == source {
			continue
		}
		msg := OutgoingMessage{Platform: endpoint.Platform, ProfileID: endpoint.ProfileID, Text: text, ImageURLs: images, VideoURLs: videos}
		if endpoint.Kind == "group" {
			msg.GroupID = endpoint.TargetID
		} else {
			msg.UserID = endpoint.TargetID
		}
		if err := req.Channel.Send(ctx, msg); err != nil {
			failures = append(failures, endpoint.Platform+"/"+endpoint.TargetID+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("消息互通部分端点发送失败：%s", strings.Join(failures, "; "))
	}
	return nil
}

func relayEndpoints(raw any) []messageRelayEndpoint {
	items, ok := raw.([]map[string]any)
	if !ok {
		if generic, genericOK := raw.([]any); genericOK {
			items = make([]map[string]any, 0, len(generic))
			for _, item := range generic {
				if value, valueOK := item.(map[string]any); valueOK {
					items = append(items, value)
				}
			}
		}
	}
	out := make([]messageRelayEndpoint, 0, len(items))
	for _, item := range items {
		endpoint := messageRelayEndpoint{
			ProfileID: strings.TrimSpace(fmt.Sprint(item["profile_id"])), Platform: NormalizePlatformID(fmt.Sprint(item["platform"])),
			Kind: strings.TrimSpace(fmt.Sprint(item["kind"])), TargetID: strings.TrimSpace(fmt.Sprint(item["target_id"])),
		}
		if endpoint.ProfileID != "" && endpoint.Platform != "" && endpoint.TargetID != "" {
			out = append(out, endpoint)
		}
	}
	return out
}

func relayEndpointForEvent(endpoints []messageRelayEndpoint, event MessageEvent) int {
	target := event.UserID
	if event.Kind == EventKindGroup {
		target = event.GroupID
	}
	for index, endpoint := range endpoints {
		if endpoint.ProfileID == event.ProfileID && endpoint.Platform == NormalizePlatformID(event.Platform) && endpoint.Kind == string(event.Kind) && endpoint.TargetID == target {
			return index
		}
	}
	return -1
}

func relayMedia(segments []MessageSegment) (images, videos []string) {
	for _, segment := range segments {
		source := firstNonEmpty(segment.Data["cached_file"], segment.Data["url"], segment.Data["file"])
		if strings.TrimSpace(source) == "" {
			continue
		}
		switch segment.Type {
		case "image":
			images = append(images, source)
		case "video":
			videos = append(videos, source)
		}
	}
	return
}

func relayPlatformLabel(platform string) string {
	switch NormalizePlatformID(platform) {
	case PlatformTelegram:
		return "Telegram"
	case PlatformOneBotV11:
		return "QQ"
	case PlatformQQOfficial:
		return "QQ 官方"
	case PlatformFeishu:
		return "飞书"
	case PlatformDingTalk:
		return "钉钉"
	case PlatformWeCom:
		return "企业微信"
	}
	return strings.TrimSpace(platform)
}
