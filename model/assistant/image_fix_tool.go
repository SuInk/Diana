// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// image_fix 让主回复模型在群友纠正图片认错时自己记下更正，重写那张图的缓存描述。
//
// 默认看图方式下，对话模型只拿到视觉模型写的描述，描述按图片内容哈希缓存、跨群复用。
// 描述认错了角色，之后每次有人发这张图，机器人都照着错的说。09-29 群友说「这是舞萌的
// 百合咲」，机器人回「已经好好记进小本本了」，可缓存一个字没改，10-01 同一张图又被叫成
// 「阿梓」。嘴上认错没用，得有地方把描述改掉。
//
// 10-01 先试过在 agent_finalize 上加可选字段让模型认错时顺手填：线上回放（gemini-3.8-
// flash-low）机器人 3/3 口头认错，一次都没填。于是改成主回复起跑时并行问一次独立门控。
// 10-22～27 门控在主回复路径上判了 14 条纠正，只有 6 条是真的：它不知道机器人叫什么，
// 群友喊「嘉然今天吃什么」也当成「图里是嘉然」，视觉模型照着改了跨群共用的缓存。主模型
// 知道自己是谁、也看得到整段聊天，10-28 用这批生产请求回放（gpt-6.1-sol，第一步）：明确
// 纠正 12/12 调用，门控误判 0/7、门控判否的 60 条 0/60 误调。描述里那句「准备回『原来是
// X』『我认错了』时就该调用」是关键：没有它只有 7/18，模型口头认错却不调用。
//
// 之后门控只留给机器人不回、但引用了图的消息。10-29～11-20 它跑了 776 次，改写 22 次，
// 一半左右是误改（别的机器人的闲聊、「嘉然这个换成我的头像」、「dario的if线（」），
// 11-21 删掉。群友之间互相纠正、机器人没接话的，不再自动改缓存。
//
// 描述缓存跨群共用，乱「纠正」会污染所有群，所以重写时让视觉模型对照画面核对，和画面
// 明显矛盾的说法不采纳。
const dianaImageFixToolName = "image_fix"

// imageFixMaxImages 是一次更正最多重写几张图。
const imageFixMaxImages = 4

const imageFixInstruction = "\n\n群友指出：「%s」。先对照画面核对这个说法：画面特征对得上，或者画面里没有能否定它的东西，就以它为准重写描述——相关的角色、作品、人物或物品直接按它写，注明「据群友更正」，不再写原来的判断，也不再标「疑似」；画面和它明显矛盾（比如说是猫，画面里是狗），就忽略它照常描述。"

// imageFixPrefix 是视觉模型重写失败时的兜底：原描述前面加一行更正，至少下次读到的
// 人知道这里被纠正过。
const imageFixPrefix = "【群友更正：%s】"

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

// Run 只核对那条消息里确实有图，重写在后台做：视觉模型要对照原图重看一遍，不能
// 拖着这一轮回复。
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
		fixed := t.runtime.rewriteImageDescriptions(context.WithoutCancel(ctx), t.event, store, correction, messageID)
		log.Printf("diana image fix: tool message_id=%s target=%s images=%d", t.event.MessageID, messageID, fixed)
	}()
	return "已记下，后台会对照原图按这条更正重写描述；和画面明显矛盾的说法不会采纳。", nil
}

func (r *Runtime) rewriteImageDescriptions(ctx context.Context, event MessageEvent, store ImageDescriptionStore, correction, messageID string) int {
	finder := newDianaHistoryImagesTool(r, event).withImageInput(true, ImageInputModeAuto)
	source, found, _ := finder.findSourceEvent(ctx, messageID)
	if !found {
		return 0
	}
	cfg := r.effectiveConfigForEvent(event)
	system := cfg.prompt(promptRecallImageSystemSpec)
	instruction := cfg.prompt(promptRecallImageInstructionSpec) + fmt.Sprintf(imageFixInstruction, correction)
	fixed := 0
	seen := map[string]bool{}
	for _, ref := range historicalStillImageRefs(source) {
		if fixed >= imageFixMaxImages {
			break
		}
		segment := r.prepareHistoricalImageSegment(ctx, source, ref)
		hash, ok := imageSegmentContentSHA256(segment)
		if !ok || seen[hash] {
			continue
		}
		seen[hash] = true
		description := ""
		if sources := availableImageURLs([]MessageSegment{segment}); len(sources) > 0 {
			rewritten, err := r.describeCachedImage(ctx, event, sources[0], system, instruction, "image_description_fix")
			if err != nil {
				log.Printf("diana image fix: describe failed: message_id=%s err=%v", messageID, err)
			} else {
				description = rewritten
			}
		}
		if description == "" {
			old, found, err := store.GetImageDescription(ctx, hash)
			if err != nil {
				continue
			}
			description = fmt.Sprintf(imageFixPrefix, correction)
			if found {
				description += strings.TrimSpace(old.Description)
			}
		}
		if err := store.SaveImageDescription(ctx, ImageDescriptionRecord{
			ContentSHA256:   hash,
			Description:     compactRecallImageDescription(description),
			SourceSession:   sessionKey(event),
			SourceMessageID: messageID,
			Source:          "correction",
			Version:         recallImageDescriptionVersion,
		}); err != nil {
			log.Printf("diana image fix: save failed: message_id=%s err=%v", messageID, err)
			continue
		}
		fixed++
	}
	if fixed > 0 {
		r.refreshMessageImageSearchText(ctx, source)
	}
	return fixed
}
