// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "context"

const (
	stickerPluginID = "official.sticker-sender"

	stickerSettingHistoryLimit   = "history_limit"
	stickerSettingSearchResults  = "search_results"
	stickerSettingIncludeGeneric = "include_generic_animated"
	// 共享开关原来叫 cross_group / cross_private、默认关。前端保存时会把默认值一起写进库，
	// 只改默认值的话，保存过设置的实例还是关着；换个键名让旧值作废，统一回到默认开。
	stickerSettingCrossGroup   = "share_groups"
	stickerSettingCrossPrivate = "share_private"
	stickerSettingLibraryLimit = "library_capacity"
	stickerSettingTurnLimit    = "turn_limit"
	stickerSettingHourlyLimit  = "hourly_limit"
)

// StickerPlugin exposes a conversation-local sticker library backed by durable message history.
type StickerPlugin struct{}

func NewStickerPlugin() *StickerPlugin { return &StickerPlugin{} }

func (p *StickerPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          stickerPluginID,
		Name:        "表情包发送",
		Version:     "0.2.4",
		Description: "启用内置 Agent 后，从持久表情资产库中按关键词检索候选；当前会话和其他群聊、私聊（默认共享）各有独立配额。Agent 查看候选的名称、标签与简介后选择一张发送，刚发过的会往后排（在别的群刚发过的也算，一天内逐渐恢复）。支持识图时，收到的表情包在后台识图时就写好简介和标签；以前收的、当时没标上的，检索到时再补。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"message:read", "message:send"},
		Settings: []PluginSettingSpec{
			{
				Key:         stickerSettingHistoryLimit,
				Label:       "每个范围候选上限",
				Description: "当前会话和已开启的共享范围分别最多读取多少个持久表情资产。旧配置继续兼容。",
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
				Key:         stickerSettingTurnLimit,
				Label:       "每轮最多发几张",
				Description: "机器人一次回复里最多发几张表情包。",
				Type:        PluginSettingTypeNumber,
				Default:     1,
				Min:         settingRange(1),
				Max:         settingRange(5),
			},
			{
				Key:         stickerSettingHourlyLimit,
				Label:       "每个会话每小时最多发几张",
				Description: "同一个群聊或私聊在任意一小时内最多发几张表情包，到了上限这段时间只用文字回应。默认 0 不限；哪个会话发得太密再单独调。",
				Type:        PluginSettingTypeNumber,
				Default:     0,
				Min:         settingRange(0),
				Max:         settingRange(120),
			},
			{
				Key:         stickerSettingLibraryLimit,
				Label:       "每个会话表情包上限",
				Description: "每个群聊或私聊最多收录多少张不同的表情包。超出后淘汰最久没人发、机器人也最久没用过的；只移出表情包库，聊天记录里的图片不受影响。",
				Type:        PluginSettingTypeNumber,
				Default:     1000,
				Min:         settingRange(50),
				Max:         settingRange(10000),
			},
			{
				Key:         stickerSettingCrossGroup,
				Label:       "跨群共享表情包",
				Description: "在一个群里收到的表情包，到别的群也能发。默认开启，不会跨机器人配置。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         stickerSettingCrossPrivate,
				Label:       "跨私聊共享表情包",
				Description: "私聊里收到的表情包，到群里和别的私聊也能发。默认开启，不会暴露来源用户。",
				Type:        PluginSettingTypeBool,
				Default:     true,
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
