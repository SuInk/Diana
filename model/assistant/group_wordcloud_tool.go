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
	GroupWordCloudFor(ctx context.Context, namespace, groupID string, since time.Time, botID string) (GroupWordCloud, error)
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

// defaultGroupWordCloudTopWords 是结果里默认带的词数，是默认值不是上限。
const defaultGroupWordCloudTopWords = 5

func (t *dianaGroupWordCloudTool) Description() string {
	return `画本群词云并直接发到群里：群友这段时间说得最多的词，字越大说得越多，你自己的发言不算。` +
		`图由运行时发送，调用后一句话交代即可；结果里的 top_words 按次数从高到低，默认前 ` + itoa(defaultGroupWordCloudTopWords) + ` 个，` +
		`要全部就把 limit 调大；续读传 offset，续读只给词频、不会再发一次图。`
}

func (t *dianaGroupWordCloudTool) InputSchema() map[string]any {
	return toolObjectSchema(nil, map[string]any{
		"range":  toolEnumParam("统计区间，默认按插件设置", "24h", "7d", "30d", "all"),
		"limit":  toolCountParam("top_words 词数，默认 "+itoa(defaultGroupWordCloudTopWords)+"，不设上限；超出单次结果上限时按整条分页", 1),
		"offset": toolCountParam("从第几个词起（0 起），续读时填上次的 next_offset；大于 0 时只给词频，不再画图发送", 0),
	})
}

type dianaGroupWordCloudResult struct {
	OK       bool                 `json:"ok"`
	Message  string               `json:"message"`
	Range    string               `json:"range,omitempty"`
	Messages int                  `json:"messages,omitempty"`
	TopWords []GroupWordCloudWord `json:"top_words,omitempty"`
	// Page 写明 top_words 一共多少个词、这一页是哪几个、怎么接着读。
	Page *toolResultPage `json:"page,omitempty"`
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
	cloud, err := store.GroupWordCloudFor(ctx, t.event.ContextNamespace, t.event.GroupID, since, botID)
	if err != nil {
		return "", fmt.Errorf("统计词云失败：%w", err)
	}
	if len(cloud.Words) == 0 {
		return marshalWordCloudResult(dianaGroupWordCloudResult{Message: "这段时间群里的发言太少，凑不出词云。", Range: rangeID, Messages: cloud.Messages}), nil
	}

	offset := toolPageOffset(input)
	limit := chatHistoryCount(input, "limit", defaultGroupWordCloudTopWords)
	message := "词云已经发到群里了。"
	scope := ""
	if cloud.Truncated {
		scope += "这段时间消息太多，只统计了最近的一部分。"
	}
	if len(cloud.Words) >= GroupWordCloudMaxWords {
		scope += fmt.Sprintf("词云只收出现最多的 %d 个词，total 是这 %d 个，不是全部不同的词。", GroupWordCloudMaxWords, GroupWordCloudMaxWords)
	}
	message += scope
	if offset > 0 {
		// 续读词频：图第一次调用时已经发过，不再重画重发。
		return t.wordsPage(ctx, dianaGroupWordCloudResult{OK: true, Message: "这是接着上次的词频，图之前已经发过，这次没有再发。" + scope, Range: rangeID, Messages: cloud.Messages}, cloud.Words, offset, limit)
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
	return t.wordsPage(ctx, dianaGroupWordCloudResult{OK: true, Message: message, Range: rangeID, Messages: cloud.Messages}, cloud.Words, offset, limit)
}

// wordsPage 把词频按整条分页放进 top_words，带上次数。
func (t *dianaGroupWordCloudTool) wordsPage(ctx context.Context, result dianaGroupWordCloudResult, words []GroupWordCloudWord, offset, limit int) (string, error) {
	return marshalToolResultPage(ctx, len(words), offset, limit, func(n int, page toolResultPage) any {
		start := min(page.Offset, len(words))
		out := result
		out.Page = &page
		out.TopWords = words[start : start+n]
		return out
	})
}

func marshalWordCloudResult(result dianaGroupWordCloudResult) string {
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"ok":false,"message":"结果序列化失败"}`
	}
	return string(encoded)
}
