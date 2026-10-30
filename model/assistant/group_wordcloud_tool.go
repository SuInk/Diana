// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 群聊词云的聊天入口，和关系图挂在同一个插件下：都是「拿本群历史画张图发出来」，
// 开关、默认区间和字体依赖都是同一套，没必要让部署方再管一个插件。
const dianaGroupWordCloudToolName = "group_wordcloud"

// GroupWordCloudStore 是消息存储里能数词频的那部分能力。
type GroupWordCloudStore interface {
	GroupWordCloudFor(ctx context.Context, groupID string, since time.Time, botID string) (GroupWordCloud, error)
}

type dianaGroupWordCloudTool struct {
	runtime  *Runtime
	event    MessageEvent
	settings SettingValues
}

func newDianaGroupWordCloudTool(runtime *Runtime, event MessageEvent, settings SettingValues) agent.Tool {
	return &dianaGroupWordCloudTool{runtime: runtime, event: event, settings: settings}
}

func (t *dianaGroupWordCloudTool) Name() string { return dianaGroupWordCloudToolName }

func (t *dianaGroupWordCloudTool) Description() string {
	return `画本群词云并直接发到群里：群友这段时间说得最多的词，字越大说得越多，你自己的发言不算。` +
		`图由运行时发送，调用后一句话交代即可；结果里的 top_words 是排名前几的词，可以顺口提一两个。`
}

func (t *dianaGroupWordCloudTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"range": map[string]any{
				"type":        "string",
				"enum":        []string{"24h", "7d", "30d", "all"},
				"description": "统计区间，默认按插件设置",
			},
		},
	}
}

type dianaGroupWordCloudResult struct {
	OK       bool     `json:"ok"`
	Message  string   `json:"message"`
	Range    string   `json:"range,omitempty"`
	Messages int      `json:"messages,omitempty"`
	TopWords []string `json:"top_words,omitempty"`
}

func (t *dianaGroupWordCloudTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return marshalWordCloudResult(dianaGroupWordCloudResult{Message: "词云只能在群聊里画。"}), nil
	}
	rangeID := strings.TrimSpace(configToolString(input, "range"))
	if rangeID == "" {
		rangeID = t.settings.String(groupRelationsSettingDefaultRange, "7d")
	}
	rangeID = normalizeRelationRange(rangeID)
	since, ok := relationRangeSince(rangeID, time.Now())
	if !ok {
		return marshalWordCloudResult(dianaGroupWordCloudResult{Message: "统计区间只支持 24h、7d、30d、all。", Range: rangeID}), nil
	}

	t.runtime.mu.RLock()
	store, _ := t.runtime.messageStore.(GroupWordCloudStore)
	t.runtime.mu.RUnlock()
	if store == nil {
		return marshalWordCloudResult(dianaGroupWordCloudResult{Message: "这个部署没有消息存储，画不了词云。", Range: rangeID}), nil
	}

	cfg := t.runtime.effectiveConfigForEvent(t.event)
	botID := firstNonEmpty(strings.TrimSpace(t.event.SelfID), strings.TrimSpace(cfg.BotAccount))
	cloud, err := store.GroupWordCloudFor(ctx, t.event.GroupID, since, botID)
	if err != nil {
		return "", fmt.Errorf("统计词云失败：%w", err)
	}
	if len(cloud.Words) == 0 {
		return marshalWordCloudResult(dianaGroupWordCloudResult{Message: "这段时间群里的发言太少，凑不出词云。", Range: rangeID, Messages: cloud.Messages}), nil
	}

	if _, _, err := ensureCJKFont(ctx); err != nil {
		return marshalWordCloudResult(dianaGroupWordCloudResult{Message: "画不出来：" + firstLineOf(err.Error()) + "。装一个中文字体就好。", Range: rangeID}), nil
	}
	png, err := RenderGroupWordCloudPNG(cloud, fmt.Sprintf("群 %s · 词云", t.event.GroupID), relationRangeLabel(rangeID))
	if err != nil {
		return marshalWordCloudResult(dianaGroupWordCloudResult{Message: "画不出来：" + firstLineOf(err.Error()), Range: rangeID}), nil
	}
	if err := t.runtime.sendPNGImage(ctx, t.event, png); err != nil {
		return "", fmt.Errorf("发送词云失败：%w", err)
	}
	result := dianaGroupWordCloudResult{OK: true, Message: "词云已经发到群里了。", Range: rangeID, Messages: cloud.Messages}
	for index := 0; index < len(cloud.Words) && index < 5; index++ {
		result.TopWords = append(result.TopWords, cloud.Words[index].Text)
	}
	return marshalWordCloudResult(result), nil
}

func marshalWordCloudResult(result dianaGroupWordCloudResult) string {
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"ok":false,"message":"结果序列化失败"}`
	}
	return string(encoded)
}
