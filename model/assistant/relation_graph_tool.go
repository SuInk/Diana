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

	"github.com/SuInk/diana/model/applog"

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
	runtime  *Runtime
	event    MessageEvent
	settings SettingValues
}

func newDianaGroupRelationsTool(runtime *Runtime, event MessageEvent, settings SettingValues) agent.Tool {
	return &dianaGroupRelationsTool{runtime: runtime, event: event, settings: settings}
}

// defaultRange 是插件设置里的统计区间；用户说了看多久就听用户的。
func (t *dianaGroupRelationsTool) defaultRange() string {
	return normalizeRelationRange(t.settings.String(groupRelationsSettingDefaultRange, "7d"))
}

func (t *dianaGroupRelationsTool) Name() string { return dianaGroupRelationsToolName }

func (t *dianaGroupRelationsTool) Description() string {
	return `画一张本群的关系图并直接发到群里：你在正中间，群友按和你的互动次数围一圈，连线粗细是互动次数，圆点大小是发言量。` +
		`用户想看「群里谁跟谁熟」「关系图」「互动图」这类东西时用它。range 可选 24h、7d、30d、all，不填按插件设置里的默认区间。` +
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
		// 私聊里画不了，这个不算故障，不用惊动运行记录。
		return marshalRelationsResult(dianaGroupRelationsResult{Message: "关系图只能在群聊里画。"}), nil
	}
	rangeID := strings.TrimSpace(configToolString(input, "range"))
	if rangeID == "" {
		rangeID = t.defaultRange()
	}
	rangeID = normalizeRelationRange(rangeID)
	since, ok := relationRangeSince(rangeID, time.Now())
	if !ok {
		result := dianaGroupRelationsResult{Message: "统计区间只支持 24h、7d、30d、all。", Range: rangeID}
		t.recordRelationsOutcome(ctx, result, "")
		return marshalRelationsResult(result), nil
	}

	t.runtime.mu.RLock()
	store, _ := t.runtime.messageStore.(GroupRelationStore)
	t.runtime.mu.RUnlock()
	if store == nil {
		result := dianaGroupRelationsResult{Message: "这个部署没有消息存储，画不了关系图。", Range: rangeID}
		t.recordRelationsOutcome(ctx, result, "")
		return marshalRelationsResult(result), nil
	}

	cfg := t.runtime.effectiveConfigForEvent(t.event)
	botID := firstNonEmpty(strings.TrimSpace(t.event.SelfID), strings.TrimSpace(cfg.BotAccount))
	graph, err := store.GroupRelationGraphFor(ctx, t.event.GroupID, since, botID)
	if err != nil {
		return "", fmt.Errorf("统计关系图失败：%w", err)
	}
	if graph.Participants == 0 {
		result := dianaGroupRelationsResult{Message: "这段时间群里没有发言记录，画不出关系图。", Range: rangeID}
		t.recordRelationsOutcome(ctx, result, "")
		return marshalRelationsResult(result), nil
	}

	title := fmt.Sprintf("群 %s · 关系图", t.event.GroupID)
	maxSeats := t.settings.Int(groupRelationsSettingMaxMembers, relationImageDefaultSeats)
	rangeLabel := relationRangeLabel(rangeID)

	// 先在进程里直接画。它不需要浏览器，也就不用等一次冷启动——一台机器上装不装
	// 得起 Chrome，和有没有中文字体，是两件独立的事，两条路都试才不至于一个环境
	// 缺件就彻底没图。
	png, rasterErr := RenderGroupRelationPNG(graph, title, rangeLabel, maxSeats)
	var browserErr error
	if rasterErr != nil {
		page := RenderGroupRelationHTML(graph, title, rangeLabel, maxSeats)
		png, browserErr = agent.CaptureHTMLScreenshot(ctx, agent.ScreenshotRequest{
			HTML:    page,
			Width:   relationImageWidth,
			Height:  relationImageHeight,
			Timeout: time.Duration(cfg.AgentBrowserTimeoutMS) * time.Millisecond,
		})
	}
	if rasterErr != nil && browserErr != nil {
		// 两条路都断了才算失败，而且要把两边各自卡在哪说清楚——只说一句「画不出来」
		// 的话，人不知道该去装字体还是装浏览器。
		result := dianaGroupRelationsResult{
			Message:      relationRenderFailureMessage(ctx, rasterErr, browserErr),
			Range:        rangeID,
			Participants: graph.Participants,
			Messages:     graph.Messages,
		}
		t.recordRelationsOutcome(ctx, result, fmt.Sprintf("直接渲染：%v；浏览器渲染：%v", rasterErr, browserErr))
		return marshalRelationsResult(result), nil
	}

	// 内联成 data URI 发出去：出站会把它转成 base64://，不用落盘，也就不用管清理。
	image := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	if err := t.runtime.sendOutgoing(ctx, t.event, routeOutgoingToEvent(t.event, OutgoingMessage{ImageURLs: []string{image}})); err != nil {
		return "", fmt.Errorf("发送关系图失败：%w", err)
	}
	result := dianaGroupRelationsResult{
		OK:           true,
		Message:      "关系图已经发到群里了。",
		Range:        rangeID,
		Participants: graph.Participants,
		Messages:     graph.Messages,
	}
	t.recordRelationsOutcome(ctx, result, "")
	return marshalRelationsResult(result), nil
}

// recordRelationsOutcome 把这次画图的结果写进运行记录。
//
// agentRunObserver 已经无条件记下了「工具调用完成」，但这个工具的几种失败——没有
// relationRenderFailureMessage 按实际卡住的环节说话。
//
// 这里原来一律回「这台机器上没有可用的无头浏览器」，可渲染失败有三种方式——找不到
// 可执行文件、渲染超时、浏览器自己报错——只有第一种才是「没有浏览器」。装了浏览器的
// 机器上看到那句话，人只会跑去查一个根本没问题的东西，而真正的原因（超时、崩溃、
// 缺依赖库）还留在事件记录里没人看。
//
// 所以先探一次活：探不到就照实说探测结果，探得到就承认浏览器在、是这次渲染没成，
// 并把原始错误带上。
func relationRenderFailureMessage(ctx context.Context, rasterErr, browserErr error) string {
	browserDetail := strings.TrimSpace(firstLineOf(browserErr.Error()))
	if status := agent.ProbeHeadlessBrowser(ctx, ""); !status.Available {
		if detail := strings.TrimSpace(status.Detail); detail != "" {
			browserDetail = detail
		}
	}
	if browserDetail == "" {
		browserDetail = "浏览器没有产出图片"
	}
	return fmt.Sprintf("画不出来，两条路都没走通：直接渲染——%s；浏览器渲染——%s。装一个中文字体或一个 Chrome/Chromium 都能救。",
		firstLineOf(rasterErr.Error()), browserDetail)
}

// firstLineOf 只取错误的第一行：浏览器的报错常常拖着一整屏堆栈，群里发不下。
func firstLineOf(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// 无头浏览器、这段时间没人说话——都是正常返回的 ok:false，在那条记录里和成功长得
// 一模一样。而「这台机器没装浏览器」正是生产里最可能撞上的那个，看不见就只能靠
// 群里没出图去猜。
func (t *dianaGroupRelationsTool) recordRelationsOutcome(ctx context.Context, result dianaGroupRelationsResult, detail string) {
	writer := t.runtime.appLogWriter()
	if writer == nil {
		return
	}
	kind, level := applog.KindOperation, applog.LevelInfo
	if !result.OK {
		kind, level = applog.KindError, applog.LevelError
	}
	logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, applog.Entry{
		Kind:    kind,
		Level:   level,
		Action:  "assistant.group_relations",
		Message: result.Message,
		Detail:  detail,
		Actor:   oneBotEventActor(t.event),
		Target:  t.event.GroupID,
		Metadata: map[string]any{
			"group_id":     t.event.GroupID,
			"range":        result.Range,
			"participants": result.Participants,
			"messages":     result.Messages,
		},
	})
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
