// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
)

// image_fix 让主回复模型在群友纠正图片认错时自己记下更正，重写那张图的缓存描述。
//
// 以前主回复起跑时并行问一次独立门控（见 image_fix_gate.go），不靠主模型自觉。10-22～27
// 门控在主回复路径上判了 14 条纠正，只有 6 条是真的：它不知道机器人叫什么，群友喊
// 「嘉然今天吃什么」也当成「图里是嘉然」，视觉模型照着改了跨群共用的缓存。主模型知道
// 自己是谁、也看得到整段聊天，10-28 用这批生产请求回放（gpt-6.1-sol，第一步）：明确
// 纠正 12/12 调用，门控误判 0/7、门控判否的 60 条 0/60 误调。描述里那句「准备回『原来是
// X』『我认错了』时就该调用」是关键：没有它只有 7/18，模型口头认错却不调用。
//
// 机器人不回的消息不跑主模型，引用了图的那些仍走门控（startQuotedImageFixGate）。
const dianaImageFixToolName = "image_fix"

type dianaImageFixTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaImageFixTool(r *Runtime, event MessageEvent) *dianaImageFixTool {
	return &dianaImageFixTool{runtime: r, event: event}
}

func (t *dianaImageFixTool) Name() string { return dianaImageFixToolName }

func (t *dianaImageFixTool) Description() string {
	return "更正图片摘要。聊天里图片的摘要是视觉模型猜的，常认错角色、人物、作品、物品。群友说出某张图里其实是谁、是什么、出自哪里，而摘要写错了或没认出来时，必须调用它记下正确说法，否则下次有人发这张图你还会认错。你准备回「原来是X」「我认错了」「那我猜错了」时，就是该调用的时候，和回复一起调用。叫你的名字、评价、玩梗、提问、要表情包、说法和摘要一致都不算。"
}

func (t *dianaImageFixTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"message_id", "fix"}, map[string]any{
		"message_id": toolStringParam("那张图所在消息的 message_id，照抄【媒体 message_id=…】里的值"),
		"fix":        toolStringParam("一句完整的正确说法，有旧说法一并点出，如「这是舞萌的百合咲，不是蔚蓝档案的白洲梓」"),
	})
}

// Run 只核对那条消息里确实有图，重写在后台做：视觉模型要对照原图重看一遍，最长
// imageFixTimeout，不能拖着这一轮回复。
func (t *dianaImageFixTool) Run(ctx context.Context, input map[string]any) (string, error) {
	messageID := stringFromAny(input["message_id"])
	correction := truncateRunes(stringFromAny(input["fix"]), 200)
	if messageID == "" || correction == "" {
		return "", fmt.Errorf("message_id 和 fix 都要填")
	}
	store := t.runtime.recallImageDescriptionStore()
	if store == nil {
		return "", fmt.Errorf("没有图片描述缓存，无需更正")
	}
	finder := newDianaHistoryImagesTool(t.runtime, t.event)
	source, found, _ := finder.findSourceEvent(ctx, messageID)
	if !found || len(historicalStillImageRefs(source)) == 0 {
		return "", fmt.Errorf("消息 %s 里找不到图片，message_id 照抄那张图所在消息的", messageID)
	}
	go func() {
		defer recoverGoroutinePanic("image_fix")
		fixCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), imageFixTimeout)
		defer cancel()
		fixed := t.runtime.rewriteImageDescriptions(fixCtx, t.event, store, correction, messageID)
		log.Printf("diana image fix: tool message_id=%s target=%s images=%d", t.event.MessageID, messageID, fixed)
	}()
	return "已记下，后台会对照原图按这条更正重写描述；和画面明显矛盾的说法不会采纳。", nil
}
