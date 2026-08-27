// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "context"

const (
	stickerPluginID = "official.sticker-sender"

	stickerSettingHistoryLimit   = "history_limit"
	stickerSettingSearchResults  = "search_results"
	stickerSettingIncludeGeneric = "include_generic_animated"
	stickerSettingCrossGroup     = "cross_group"
	stickerSettingCrossPrivate   = "cross_private"
)

// StickerPlugin exposes a conversation-local sticker library backed by durable message history.
type StickerPlugin struct{}

func NewStickerPlugin() *StickerPlugin { return &StickerPlugin{} }

func (p *StickerPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          stickerPluginID,
		Name:        "表情包发送",
		Version:     "0.1.0",
		Description: "启用内置 Agent 后，从当前群或私聊历史中检索已经缓存的表情包；Agent 先查看候选名称与描述，再选择一张发送。不会跨会话搬运图片。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"message:read", "message:send"},
		Settings: []PluginSettingSpec{
			{
				Key:         stickerSettingHistoryLimit,
				Label:       "扫描最近消息数",
				Description: "从当前会话最近多少条历史消息中寻找表情包。数值越大，候选越多，首次搜索也会稍慢。",
				Type:        PluginSettingTypeNumber,
				Default:     1000,
				Min:         settingRange(100),
				Max:         settingRange(4096),
			},
			{
				Key:         stickerSettingSearchResults,
				Label:       "候选返回数量",
				Description: "每次搜索最多交给 Agent 多少个候选。图片不会在搜索阶段发送。",
				Type:        PluginSettingTypeNumber,
				Default:     8,
				Min:         settingRange(3),
				Max:         settingRange(20),
			},
			{
				Key:         stickerSettingCrossGroup,
				Label:       "跨群共享表情包",
				Description: "允许从同一机器人会话命名空间下的其他群聊检索表情包。默认关闭，不会跨机器人配置。",
				Type:        PluginSettingTypeBool,
				Default:     false,
			},
			{
				Key:         stickerSettingCrossPrivate,
				Label:       "跨私聊共享表情包",
				Description: "允许从同一机器人会话命名空间下的其他私聊检索表情包。默认关闭，不会暴露来源用户。",
				Type:        PluginSettingTypeBool,
				Default:     false,
			},
			{
				Key:         stickerSettingIncludeGeneric,
				Label:       "收录未命名动画表情",
				Description: "把名称只有“动画表情”的图片也纳入随机候选；关闭后只使用带具体名称的表情包。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
		},
	}
}

func (p *StickerPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}
