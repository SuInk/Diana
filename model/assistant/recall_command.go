// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// /recall 是群里的原生撤回指令：引用要撤的那条，再 @机器人 发 /recall，直接撤，不过模型。
//
// 撤回本来就能让模型调 platform 工具做，但模型会凭印象说「我只能撤回自己的消息」——
// 2026-11-21 有人引用自己的消息 @机器人「帮我撤回这条消息」，模型一个工具都没调就拒了，
// 对方追一句「？」才去撤。指令把这条路固定下来。
//
// 权限不另立一套，和 platform 工具完全一致：
//   - 引用机器人自己发的：谁都能让它撤（等同 recall 操作），受平台撤回时限约束。
//   - 引用别人的：走 recall_messages 的门禁——主人、实时核验的群主/群管理员可以撤，
//     管理员动不了群主和其他管理员；普通成员只能撤自己发的；机器人必须是群管理员。
//
// 必须同时有引用和真正的 @ 段：光打 /recall 可能只是在聊这个指令，只认引用又会
// 把回复别人时顺手写的 /recall 当真。
const recallCommandText = "/recall"

// isRecallCommand 判断这条消息是不是 @机器人 发的 /recall，不看有没有引用：
// 没引用时也要接住并提示用法，不能交给模型去猜。
func (r *Runtime) isRecallCommand(event MessageEvent, text string) bool {
	if r == nil || event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" {
		return false
	}
	cfg := r.effectiveConfigForEvent(event)
	if !eventExplicitlyMentionsBot(event, cfg) {
		return false
	}
	botID := firstNonEmpty(strings.TrimSpace(event.SelfID), strings.TrimSpace(cfg.BotAccount))
	if mentionsSomeoneElseFor(event, botID) {
		return false
	}
	segments := event.Segments
	if len(segments) == 0 {
		segments = CQToSegments(firstNonEmpty(event.RawMessage, text))
	}
	// 只允许引用段、@机器人 和文本：带了别的 @、图片之类的就不是这条指令。
	var body strings.Builder
	for _, segment := range segments {
		switch {
		case segment.Type == "text":
			body.WriteString(segment.Data["text"])
		case segment.Type == "reply":
		case segment.Type == "at" && strings.TrimSpace(segment.Data["qq"]) == botID:
		default:
			return false
		}
	}
	cleaned := strings.TrimSpace(body.String())
	for _, mention := range event.MentionTargets {
		if mention.Target == "self" && strings.TrimSpace(mention.Username) != "" {
			cleaned = strings.TrimSpace(strings.ReplaceAll(cleaned, "@"+strings.TrimPrefix(strings.TrimSpace(mention.Username), "@"), ""))
		}
	}
	if strings.EqualFold(cleaned, recallCommandText) {
		return true
	}
	for _, mention := range event.MentionTargets {
		if mention.Target == "self" && strings.TrimSpace(mention.Username) != "" {
			if strings.EqualFold(strings.TrimSpace(body.String()), recallCommandText+"@"+strings.TrimPrefix(strings.TrimSpace(mention.Username), "@")) {
				return true
			}
		}
	}
	return false
}

// handleRecallCommand 执行 /recall。成功时不回话：被引用的那条消失就是结果，
// 再补一句「已撤回」只会在群里多留一条。失败时说清原因。
func (r *Runtime) handleRecallCommand(ctx context.Context, event MessageEvent, text string) (string, bool) {
	if !r.isRecallCommand(event, text) {
		return "", false
	}
	if event.Quoted == nil || strings.TrimSpace(event.Quoted.MessageID) == "" || event.Quoted.Semantic {
		return "请先回复要撤回的那条消息，再 @我 发 /recall。", true
	}
	if group := strings.TrimSpace(event.Quoted.GroupID); group != "" && group != strings.TrimSpace(event.GroupID) {
		return "只能撤回本群的消息。", true
	}
	if err := r.runRecallCommand(ctx, event); err != nil {
		return "撤回失败：" + err.Error(), true
	}
	return "", true
}

func (r *Runtime) runRecallCommand(ctx context.Context, event MessageEvent) error {
	tool := newDianaPlatformTool(ctx, r, event)
	messageID := strings.TrimSpace(event.Quoted.MessageID)
	if explicitlyRepliesToBot(event, r.effectiveConfigForEvent(event)) {
		return r.recallOwnMessageByCommand(ctx, tool, messageID)
	}
	_, err := tool.Run(ctx, map[string]any{"operation": platformOpRecallMessages, "message_id": messageID})
	if err != nil {
		// 工具的报错写给模型看，带着操作名；指令直接给人看，去掉这层前缀。
		return fmt.Errorf("%s", strings.TrimPrefix(err.Error(), platformOpRecallMessages+" 执行失败："))
	}
	return nil
}

// recallOwnMessageByCommand 撤机器人自己发的那条。不直接调工具的 recall 操作：那条
// 路的报错是写给模型的改口指引（「不要声称已经撤回……」），原样发到群里不像话。
// 门禁与它一致：平台接口开着、平台支持撤回、目标确实是机器人发出的、有真实消息 ID。
func (r *Runtime) recallOwnMessageByCommand(ctx context.Context, tool *dianaPlatformTool, messageID string) error {
	event := tool.event
	owner := tool.owner
	access := platformActor{owner: owner}.access()
	fail := func(err error) error {
		r.recordPlatformInterfaceOperation(event, platformOpRecall, access, owner, messageID, err)
		return err
	}
	if !r.platformInterfaceEnabled(event) {
		return fail(fmt.Errorf("平台接口未启用"))
	}
	if !platformSupportsRecall(r.currentPlatform(event)) {
		return fail(fmt.Errorf("当前平台不支持撤回消息"))
	}
	target, err := r.resolveRecallTarget(ctx, event, messageID, "")
	if err != nil || recallableOutboundMessageID(target) == "" {
		return fail(fmt.Errorf("本会话记录里找不到这条消息的平台消息 ID"))
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := r.deletePlatformMessage(callCtx, event, recallableOutboundMessageID(target)); err != nil {
		return fail(fmt.Errorf("%w（可能已超过平台的撤回时限）", err))
	}
	r.recordPlatformInterfaceOperation(event, platformOpRecall, access, owner, messageID, nil)
	return nil
}
