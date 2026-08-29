// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "context"

// 群聊关系图做成插件，而不是常开的工具。
//
// 它有插件该有的每一样东西：不是每个群都想让机器人画这个（人际关系图在有些群里
// 挺敏感），渲染要占一次无头浏览器，还有「默认看多久」「最多画多少人」这种该让
// 部署方自己定的参数。这些正是插件页的开关和设置在管的事。
//
// 插件本身不处理消息——它声明能力和设置，真正干活的是 diana.group_relations 工具，
// 只在本群启用了这个插件时才注册给模型。仓库里提供商配置那个插件也是这个形状。
const (
	groupRelationsPluginID = "group_relations"

	groupRelationsSettingDefaultRange = "default_range"
	groupRelationsSettingMaxMembers   = "max_members"
)

// GroupRelationsPluginID 对外导出，WebUI 用它把字体与浏览器探测挂到正确的插件卡片。
const GroupRelationsPluginID = groupRelationsPluginID

// GroupRelationsPlugin 声明群聊关系图这项能力。
type GroupRelationsPlugin struct{}

func NewGroupRelationsPlugin() *GroupRelationsPlugin { return &GroupRelationsPlugin{} }

func (p *GroupRelationsPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          groupRelationsPluginID,
		Name:        "群聊关系图",
		Version:     "0.1.1",
		Description: "在群里画一张以机器人为中心的关系图：群友按与它的互动次数围成一圈，连线粗细是互动次数，圆点大小是发言量。数据取自本群的历史消息，不额外记录任何东西。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"message:read", "message:send", "browser:headless"},
		Settings: []PluginSettingSpec{
			{
				Key:         groupRelationsSettingDefaultRange,
				Label:       "默认统计区间",
				Description: "用户没说看多久时按这个算。说了就听用户的。",
				Type:        PluginSettingTypeSelect,
				Default:     "7d",
				Options: []PluginSettingOption{
					{Value: "24h", Label: "最近 24 小时"},
					{Value: "7d", Label: "最近 7 天（默认）"},
					{Value: "30d", Label: "最近 30 天"},
					{Value: "all", Label: "全部记录"},
				},
			},
			{
				Key:         groupRelationsSettingMaxMembers,
				Label:       "最多画多少人",
				Description: "发到群里的是一张定死的位图，没法悬停也没法缩放，人太多就只剩一圈看不清的小字。",
				Type:        PluginSettingTypeNumber,
				Default:     relationImageDefaultSeats,
				Min:         settingRange(6),
				Max:         settingRange(40),
			},
		},
	}
}

// Handle 不处理消息：这个插件只提供开关和设置，画图由工具完成。
func (p *GroupRelationsPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}
