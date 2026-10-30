// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "context"

// 股票行情做成插件而不是常开的工具：它要连外网的行情接口，不想让机器人聊股票的
// 部署方可以在插件页关掉。默认开着，行情源不要 Key，开箱即用。和关系图一样，插件只声明开关与设置，
// 干活的是 stock_quote 工具，本群启用时才注册给模型。
const (
	stockPluginID = "official.stock-quote"

	stockSettingChart     = "chart"
	stockSettingChartDays = "chart_days"
)

// StockPlugin 声明股票行情查看这项能力。
type StockPlugin struct{}

func NewStockPlugin() *StockPlugin { return &StockPlugin{} }

func (p *StockPlugin) Manifest() PluginManifest {
	return PluginManifest{
		ID:          stockPluginID,
		Name:        "股票行情",
		Version:     "0.1.0",
		Description: "群友让机器人查股票时，按代码或名称查 A 股、港股、美股的现价、涨跌幅、成交量等行情，可附一张近期日线走势图。行情来自腾讯财经公开接口，无需 API Key；美股有延迟，仅供参考。",
		Official:    true,
		BuiltIn:     true,
		Permissions: []string{"network:outbound", "message:send"},
		Settings: []PluginSettingSpec{
			{
				Key:         stockSettingChart,
				Label:       "附走势图",
				Description: "查询时把近期日线收盘走势画成图一起发出。需要中文字体。",
				Type:        PluginSettingTypeBool,
				Default:     true,
			},
			{
				Key:         stockSettingChartDays,
				Label:       "走势图天数",
				Description: "走势图画最近多少个交易日。",
				Type:        PluginSettingTypeNumber,
				Default:     stockDefaultChartDays,
				Min:         settingRange(10),
				Max:         settingRange(250),
			},
		},
	}
}

// Handle 不处理消息：这个插件只提供开关和设置，查询由工具完成。
func (p *StockPlugin) Handle(context.Context, PluginRequest) (*PluginResponse, error) {
	return nil, nil
}
