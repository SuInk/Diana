// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
	"time"
)

// SetInboundEventStore enables durable ingest, restart recovery, and history backfill.
func (r *Runtime) SetInboundEventStore(store InboundEventStore) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inboundStore = store
}

// privateAdmissionAllows 判断用户的私聊是否准入。all（默认）直接放行；
// owner_only 只放行主人；whitelist 放行主人与白名单。空用户 ID 只在 all 下放行。
func (r *Runtime) privateAdmissionAllows(event MessageEvent) bool {
	return privateAdmissionAllowsConfig(r.effectiveConfigForEvent(event), event)
}

// privateAdmissionAllowsConfig 按统一的主人判定放行主人：Telegram 主人填的是用户名时，
// 私聊发来的是数字 ID，直接比对 OwnerID 会把主人自己挡在外面。
func privateAdmissionAllowsConfig(cfg BotConfig, event MessageEvent) bool {
	return cfg.IsOwnerEvent(event) || cfg.PrivateAdmission.Allows(event.UserID, cfg.OwnerID)
}

// HandleEvent 处理 OneBot 消息或通知事件。
func (r *Runtime) HandleEvent(ctx context.Context, event MessageEvent) error {
	if r.profileDisabled(event.ProfileID) {
		// 停用的机器人一条都不处理：不记聊天记录、不进队列、不回。
		return nil
	}
	event = r.bindInboundEventIdentity(event)
	if event.Kind == EventKindRequest {
		return r.handleOneBotRequest(ctx, event)
	}
	if isRecallNotice(event) && r.isBotOwnRecall(event) {
		return nil
	}
	// 机器人自己被禁言的通知 user_id 就是机器人，放在自发消息过滤之前处理。
	if r.observeBotMuteNotice(ctx, event) {
		return nil
	}
	if event.selfSent {
		// message_sent：接入端推回来的机器人自己发出的消息。只用来确认送达和记
		// self_echo_at，不回复、不再记一遍聊天记录——Diana 自己发的消息发送路径
		// 已经按真实 message_id 记过了（rememberOutgoingWithMessageID），这里再走
		// observeSelfMessage 会让插件把同一条看两遍。
		r.observeOutboundEcho(event)
		r.recordInboundSelfEcho(event)
		return nil
	}
	if !isRecallNotice(event) && r.isSelfMessage(event) {
		r.observeOutboundEcho(event)
		r.observeSelfMessage(ctx, event)
		return nil
	}
	if event.Kind == EventKindNotice {
		if reaction, ok := messageReactionFromEvent(event, event.Platform); ok {
			// 表情回应只用来统计，不进回复流程，也不当成一条消息记进聊天记录。
			r.recordMessageReaction(reaction)
			return nil
		}
		if isRecallNotice(event) {
			event = r.enrichRecallNotice(ctx, event)
			// 撤回通知不走队列、即时到达：登记后还没送出的回复会在发送前放弃。
			r.noteRecalledInbound(event)
		}
		if r.plugins != nil {
			event = r.plugins.ObserveEventWithOverrides(ctx, event, r.pluginOverridesForEvent(event))
		}
		if isRecallNotice(event) {
			r.persistMessageEvent(event)
			r.recordNoticeEvent(event)
		}
		if eventTriggerEventType(event) == eventTriggerEventMemberJoin && r.admitsNotice(r.effectiveConfigForEvent(event), event) {
			_, muted := r.botMutedForReply(eventTriggerDeliveryEvent(event))
			r.dispatchEventTriggers(ctx, event, "", !muted)
		}
		return r.handleNotice(ctx, event)
	}
	if event.Kind != EventKindGroup && event.Kind != EventKindPrivate {
		return nil
	}
	if r.members != nil {
		r.members.Observe(event)
	}
	if event.Kind == EventKindPrivate {
		text := PlainText(event.Segments)
		if text == "" {
			text = event.RawMessage
		}
		r.mu.RLock()
		interceptor := r.privateMessageInterceptor
		r.mu.RUnlock()
		if interceptor != nil && interceptor(ctx, event, text) {
			record := r.decisionEventRecord(event, "[控制台登录配对]", "replied")
			record.Reason = "私聊消息完成了控制台登录配对"
			r.record(record)
			return nil
		}
		// 私聊准入在配对之后、入队之前拦截：非准入用户的私聊整条丢弃，
		// 不登记直呼、不进预处理队列、不调模型。配对是带自身鉴权的特权流程，
		// 不受准入限制（否则 owner_only 模式下主人没配对过就没法登录控制台）。
		if !r.privateAdmissionAllows(event) {
			record := r.decisionEventRecord(event, "[私聊准入]", "ignored_private_admission")
			record.Reason = "私聊准入模式限制，该用户的私聊被静默忽略"
			r.record(record)
			return nil
		}
	}

	// 在入队之前登记直呼消息，保证旧消息处理时能看到更新的直呼。
	r.noteDirectedInbound(event)

	r.mu.RLock()
	inboundStore := r.inboundStore
	r.mu.RUnlock()
	if inboundStore != nil {
		// Do not bind the durable ingest to the socket lifecycle. A concurrent restart
		// may cancel ctx while this event is already in our hands.
		ingestCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _, err := inboundStore.EnqueueInboundEvent(ingestCtx, sessionKey(event), event, r.inboundPriority(event))
		cancel()
		if err != nil {
			return r.retainFailedInbound(event, err)
		}
		r.observeLiveGroupSeq(event)
		r.wakeInboundWorkers()
		return nil
	}

	ctx = withLLMUsageContext(ctx, event)
	ctx = r.withDebugTraceContext(ctx, event)
	ctx = withContextBudgetCap(ctx, r.effectiveConfigForEvent(event).MaxContextTokens)
	prepared, text, handled, outcome := r.prepareMessageEvent(ctx, event)
	if !handled {
		return nil
	}
	return r.startReplyWorker(ctx, prepared, text, outcome)
}

// bindInboundEventIdentity restores Diana's internal routing identity when a
// transport event does not carry it. OneBot history responses never contain
// ProfileID, so reconnect backfill has to补一个。
//
// 身份只能按消息的来源绑。以前拿 WebUI 里当时选中的那台机器人去补，于是选中 Telegram
// 那台之后触发的一次 OneBot 重连回填，把三十多条真实 QQ 群消息全部写成了 Telegram：
// 正文带着 CQ 码、QQ 多媒体域名和正数 QQ 群号，事件页却按 Telegram 分类。
//
// 所以按来源平台绑：调用方说清楚这批事件是哪个平台来的，身份就从那个平台的通道绑定
// 上取。只有一台机器人时身份就是它。
func (r *Runtime) bindInboundEventIdentity(event MessageEvent) MessageEvent {
	return r.bindInboundEventIdentityForPlatform(event, "")
}

// bindInboundEventIdentityForPlatform 按来源平台补身份；sourcePlatform 为空表示
// 「不知道来源」，此时只在只有一台机器人时补（活跃通道事件本来就自带身份，走不到这里）。
func (r *Runtime) bindInboundEventIdentityForPlatform(event MessageEvent, sourcePlatform string) MessageEvent {
	r.mu.RLock()
	sole, hasSole := r.lookupProfileLocked("")
	channel := r.channel
	r.mu.RUnlock()

	// 只有一台机器人时身份就是它；多台时从通道绑定里找真正负责这个来源平台的那台。
	// 找不到就把身份留空——宁可事件没有归属，也不要挂到一台根本不在这个平台上的机器人名下。
	profileID, platform := "", ""
	if hasSole {
		profileID, platform = strings.TrimSpace(sole.ID), NormalizePlatformID(sole.Platform)
	}
	if sourcePlatform = NormalizePlatformID(sourcePlatform); sourcePlatform != "" && sourcePlatform != platform {
		profileID, platform = "", ""
		if multi, ok := channel.(*MultiChannel); ok {
			if binding, found := multi.bindingForPlatform(sourcePlatform); found {
				profileID = strings.TrimSpace(binding.ProfileID)
				platform = NormalizePlatformID(binding.Platform)
			}
		}
	}

	if strings.TrimSpace(event.ProfileID) == "" {
		event.ProfileID = profileID
	}
	if strings.TrimSpace(event.Platform) == "" {
		event.Platform = platform
	}
	if _, ok := channel.(*MultiChannel); ok {
		event.ContextNamespace = event.ProfileID
	}
	return event
}

func (r *Runtime) recordInboundSelfEcho(event MessageEvent) {
	r.mu.RLock()
	store, _ := r.inboundStore.(InboundEventDeliveryAuditStore)
	r.mu.RUnlock()
	if store == nil || strings.TrimSpace(event.MessageID) == "" {
		return
	}
	observedAt := time.Now()
	if event.Time > 0 {
		observedAt = time.Unix(event.Time, 0)
	}
	auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.RecordInboundEventSelfEcho(auditCtx, event, observedAt); err != nil {
		log.Printf("diana persist outbound self echo failed: %v", err)
	}
}
