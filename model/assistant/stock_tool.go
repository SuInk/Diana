// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

const dianaStockToolName = "stock_quote"

type dianaStockTool struct {
	runtime  *Runtime
	event    MessageEvent
	settings SettingValues
	client   *stockClient
}

func newDianaStockTool(runtime *Runtime, event MessageEvent, settings SettingValues) agent.Tool {
	return &dianaStockTool{runtime: runtime, event: event, settings: settings, client: newStockClient()}
}

func (t *dianaStockTool) Name() string { return dianaStockToolName }

func (t *dianaStockTool) Description() string {
	return `查股票实时行情：A 股、港股、美股，给代码（600519、00700、AAPL、sh000001）或名称（贵州茅台、腾讯）都行。` +
		`返回现价、涨跌、成交量等；开着走势图时运行时会把近期走势图直接发出去，调用后用一两句话说行情即可，别把字段念一遍。` +
		`只报数据，不给买卖建议。`
}

func (t *dianaStockTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "股票代码或名称，一次一只",
			},
		},
		"required": []string{"query"},
	}
}

type dianaStockResult struct {
	OK        bool        `json:"ok"`
	Message   string      `json:"message"`
	Quote     *StockQuote `json:"quote,omitempty"`
	ChartSent bool        `json:"chart_sent,omitempty"`
}

func (t *dianaStockTool) Run(ctx context.Context, input map[string]any) (string, error) {
	query := strings.TrimSpace(configToolString(input, "query"))
	if query == "" {
		return marshalStockResult(dianaStockResult{Message: "要查哪只股票？给个代码或名称。"}), nil
	}
	symbol, err := t.client.Resolve(ctx, query)
	if err != nil {
		return marshalStockResult(dianaStockResult{Message: firstLineOf(err.Error())}), nil
	}
	quote, err := t.client.Quote(ctx, symbol)
	if err != nil {
		return marshalStockResult(dianaStockResult{Message: firstLineOf(err.Error())}), nil
	}
	result := dianaStockResult{OK: true, Quote: &quote, Message: "行情已查到。"}
	if t.settings.Bool(stockSettingChart, true) {
		result.Message = t.sendChart(ctx, quote)
		result.ChartSent = result.Message == ""
		if result.ChartSent {
			result.Message = "行情已查到，走势图已经发出去了。"
		}
	}
	return marshalStockResult(result), nil
}

// sendChart 画图并发出，成功返回空串；失败只影响图，行情照样返回给模型。
func (t *dianaStockTool) sendChart(ctx context.Context, quote StockQuote) string {
	days := t.settings.Int(stockSettingChartDays, stockDefaultChartDays)
	bars, err := t.client.History(ctx, quote, days)
	if err != nil {
		return "行情已查到，走势图没拿到：" + firstLineOf(err.Error())
	}
	if _, _, err := ensureCJKFont(ctx); err != nil {
		return "行情已查到，走势图画不出来：缺中文字体。"
	}
	png, err := RenderStockChartPNG(quote, bars)
	if err != nil {
		return "行情已查到，走势图画不出来：" + firstLineOf(err.Error())
	}
	if err := t.runtime.sendPNGImage(ctx, t.event, png); err != nil {
		return "行情已查到，走势图发送失败：" + firstLineOf(err.Error())
	}
	return ""
}

func marshalStockResult(result dianaStockResult) string {
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"ok":false,"message":"结果序列化失败"}`
	}
	return string(encoded)
}
