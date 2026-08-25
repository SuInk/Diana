// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 群聊关系图的聊天入口。
//
// 做成工具而不是关键词命令：群里说「画个关系图」「看看谁跟你最熟」的说法太多，
// 关键词表永远补不全，而模型本来就在读这句话。工具只负责取数、渲染、发图。
const dianaGroupRelationsToolName = "diana.group_relations"

// GroupRelationStore 是消息存储里能算关系图的那部分能力。做成可选接口：
// 换一个不支持的存储实现时，工具自己说「这个部署不支持」，而不是整个运行时起不来。
type GroupRelationStore interface {
	GroupRelationGraphFor(ctx context.Context, groupID string, since time.Time, botID string) (GroupRelationGraph, error)
}

type dianaGroupRelationsTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaGroupRelationsTool(runtime *Runtime, event MessageEvent) agent.Tool {
	return &dianaGroupRelationsTool{runtime: runtime, event: event}
}

func (t *dianaGroupRelationsTool) Name() string { return dianaGroupRelationsToolName }

func (t *dianaGroupRelationsTool) Description() string {
	return `画一张本群的关系图并直接发到群里：你在正中间，群友按和你的互动次数围一圈，连线粗细是互动次数，圆点大小是发言量。` +
		`用户想看「群里谁跟谁熟」「关系图」「互动图」这类东西时用它。range 可选 24h、7d、30d、all，默认 7d。` +
		`图由运行时发送，你只要在调用后用一句话说明就行，不要描述图里的具体数字——你看不到那张图。`
}

func (t *dianaGroupRelationsTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"range": map[string]any{
				"type":        "string",
				"enum":        []string{"24h", "7d", "30d", "all"},
				"description": "统计区间，默认 7d",
			},
		},
	}
}

type dianaGroupRelationsResult struct {
	OK           bool   `json:"ok"`
	Message      string `json:"message"`
	Range        string `json:"range,omitempty"`
	Participants int    `json:"participants,omitempty"`
	Messages     int    `json:"messages,omitempty"`
}

func (t *dianaGroupRelationsTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return marshalRelationsResult(dianaGroupRelationsResult{Message: "关系图只能在群聊里画。"}), nil
	}
	rangeID := normalizeRelationRange(configToolString(input, "range"))
	since, ok := relationRangeSince(rangeID, time.Now())
	if !ok {
		return marshalRelationsResult(dianaGroupRelationsResult{Message: "统计区间只支持 24h、7d、30d、all。"}), nil
	}

	t.runtime.mu.RLock()
	store, _ := t.runtime.messageStore.(GroupRelationStore)
	t.runtime.mu.RUnlock()
	if store == nil {
		return marshalRelationsResult(dianaGroupRelationsResult{Message: "这个部署没有消息存储，画不了关系图。"}), nil
	}

	cfg := t.runtime.effectiveConfigForEvent(t.event)
	botID := firstNonEmpty(strings.TrimSpace(t.event.SelfID), strings.TrimSpace(cfg.BotAccount))
	graph, err := store.GroupRelationGraphFor(ctx, t.event.GroupID, since, botID)
	if err != nil {
		return "", fmt.Errorf("统计关系图失败：%w", err)
	}
	if graph.Participants == 0 {
		return marshalRelationsResult(dianaGroupRelationsResult{Message: "这段时间群里没有发言记录，画不出关系图。", Range: rangeID}), nil
	}

	title := fmt.Sprintf("群 %s · 关系图", t.event.GroupID)
	page := RenderGroupRelationHTML(graph, title, relationRangeLabel(rangeID))
	png, err := agent.CaptureHTMLScreenshot(ctx, agent.ScreenshotRequest{
		HTML:    page,
		Width:   relationImageWidth,
		Height:  relationImageHeight,
		Timeout: time.Duration(cfg.AgentBrowserTimeoutMS) * time.Millisecond,
	})
	if err != nil {
		// 渲染要靠无头浏览器，部署里没有就明确说出来，别让模型编一句「已发送」。
		return marshalRelationsResult(dianaGroupRelationsResult{
			Message: "画不出来：这台机器上没有可用的无头浏览器（关系图靠它把图渲染成图片）。",
			Range:   rangeID,
		}), nil
	}

	// 内联成 data URI 发出去：出站会把它转成 base64://，不用落盘，也就不用管清理。
	image := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{ImageURLs: []string{image}})); err != nil {
		return "", fmt.Errorf("发送关系图失败：%w", err)
	}
	return marshalRelationsResult(dianaGroupRelationsResult{
		OK:           true,
		Message:      "关系图已经发到群里了。",
		Range:        rangeID,
		Participants: graph.Participants,
		Messages:     graph.Messages,
	}), nil
}

func marshalRelationsResult(result dianaGroupRelationsResult) string {
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"ok":false,"message":"结果序列化失败"}`
	}
	return string(encoded)
}

func normalizeRelationRange(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "24h", "1d":
		return "24h"
	case "30d":
		return "30d"
	case "all":
		return "all"
	case "", "7d":
		return "7d"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func relationRangeSince(rangeID string, now time.Time) (time.Time, bool) {
	switch rangeID {
	case "24h":
		return now.Add(-24 * time.Hour), true
	case "7d":
		return now.Add(-7 * 24 * time.Hour), true
	case "30d":
		return now.Add(-30 * 24 * time.Hour), true
	case "all":
		return time.Time{}, true
	default:
		return time.Time{}, false
	}
}

func relationRangeLabel(rangeID string) string {
	switch rangeID {
	case "24h":
		return "最近 24 小时"
	case "30d":
		return "最近 30 天"
	case "all":
		return "全部记录"
	default:
		return "最近 7 天"
	}
}
