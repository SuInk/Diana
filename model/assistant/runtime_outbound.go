// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

func (r *Runtime) prepareResolverVideoDelivery(videoURLs []string) resolverVideoDelivery {
	delivery := resolverVideoDelivery{
		Direct:  make([]string, 0, len(videoURLs)),
		Uploads: make([]resolverVideoUpload, 0, 1),
	}
	for _, videoURL := range videoURLs {
		path := localMediaPath(videoURL)
		if path == "" {
			delivery.Direct = append(delivery.Direct, videoURL)
			continue
		}
		upload, ok := resolverVideoUploadFromPath(path)
		if !ok {
			delivery.Direct = append(delivery.Direct, videoURL)
			continue
		}
		if sharedURL, ok := r.shareLocalMedia(path); ok {
			delivery.Direct = append(delivery.Direct, sharedURL)
			delivery.SharedUploads = append(delivery.SharedUploads, upload)
			continue
		}
		delivery.Uploads = append(delivery.Uploads, upload)
	}
	return delivery
}

func (r *Runtime) prepareForwardResolverVideoDelivery(messages []OutgoingMessage) ([]OutgoingMessage, []resolverVideoUpload, []resolverVideoUpload) {
	forwardMessages := make([]OutgoingMessage, 0, len(messages))
	uploads := make([]resolverVideoUpload, 0)
	sharedUploads := make([]resolverVideoUpload, 0)
	for _, msg := range messages {
		delivery := r.prepareResolverVideoDelivery(msg.VideoURLs)
		msg.VideoURLs = delivery.Direct
		if !outgoingMessageEmpty(msg) {
			forwardMessages = append(forwardMessages, msg)
		}
		uploads = append(uploads, delivery.Uploads...)
		sharedUploads = append(sharedUploads, delivery.SharedUploads...)
	}
	return forwardMessages, uploads, sharedUploads
}

func (r *Runtime) deliveryEvidence(event MessageEvent, messageIDs []string) ([]string, bool, error) {
	if len(messageIDs) > 0 {
		return messageIDs, true, nil
	}
	return messageIDs, r.outboundResultAcknowledged(event, nil), nil
}

func (r *Runtime) deliverChunks(ctx context.Context, event MessageEvent, chunks []string, cfg BotConfig, decoration outboundDecoration) ([]string, error) {
	sentChunks := 0
	messageIDs := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		msg := OutgoingMessage{Text: chunk}
		if event.Kind == EventKindGroup {
			msg.GroupID = event.GroupID
			// 语音必须保持为独立 record 段；普通回复仍让第一条带 reply 元数据。
			if sentChunks == 0 && !isStandaloneRecordReply(chunk) {
				// auto 档不在这里补装饰件：模型已经在正文里自行写出引用标记和 @，
				// 运行时再补一遍就又变成每条都带。
				// 原消息已撤回时不挂引用：引用一条不存在的消息要么发送失败，
				// 要么在界面上渲染成怪东西。回复本身照常发出。
				mode := replyReferenceMode(cfg)
				forceBacklogReference := mode == ReplyDecorationAuto && r.autoReferenceBackloggedReply(event)
				if decoration.ReplyToCurrent && (mode == ReplyDecorationOn || forceBacklogReference) && !r.inboundTriggerRecalled(event) {
					msg.ReplyMessageID = event.MessageID
				}
				if decoration.mentionEnabled(cfg) {
					msg.MentionUserID = decoration.MentionUserID
				}
			}
		} else {
			msg.UserID = event.UserID
		}
		sendCtx := ctx
		if sentChunks > 0 {
			interval := chunkSendInterval(cfg, chunk)
			// 这一轮还没发完，等待期间重新点亮输入状态：空着的话，多条回复中间
			// 看上去就是「正在输入」断了。
			typingIndicatorFromContext(ctx).resume()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(interval):
			}
			sendCtx = withContinuousOutboundDelivery(ctx)
		}
		result, err := r.sendOutgoingWithResult(sendCtx, event, msg)
		if err != nil {
			return nil, err
		}
		if messageID := apiMessageID(result); messageID != "" {
			messageIDs = append(messageIDs, messageID)
		}
		sentChunks++
	}
	return messageIDs, nil
}

func (r *Runtime) sendOutgoing(ctx context.Context, event MessageEvent, msg OutgoingMessage) error {
	_, err := r.sendOutgoingWithResult(ctx, event, msg)
	return err
}

func (r *Runtime) sendOutgoingWithResult(ctx context.Context, event MessageEvent, msg OutgoingMessage) (map[string]any, error) {
	msg = routeOutgoingToEvent(event, msg)
	var audioErr error
	msg, audioErr = r.prepareTelegramAudio(msg)
	if audioErr != nil {
		return nil, audioErr
	}
	msg = r.resolveOutgoingLocalImages(msg)
	msg = r.applyOutgoingReplyMarker(ctx, event, msg)
	msg = r.normalizeOutgoingMentions(event, msg)
	msg = r.resolveOutgoingMentionNames(event, msg)
	if blockedErr := r.blockedGroupSendError(event); blockedErr != nil {
		return nil, blockedErr
	}
	if replySuppressionSendGuardEnabled(ctx) && !replySuppressionOutboundGateHeld(ctx) {
		var result map[string]any
		err := r.withReplySuppressionOutboundGate(ctx, event, func(sendCtx context.Context) error {
			var sendErr error
			result, sendErr = r.sendOutgoingWithResult(sendCtx, event, msg)
			return sendErr
		})
		return result, err
	}
	if r.channel == nil {
		return nil, fmt.Errorf("diana: channel is not configured")
	}
	if replySuppressionSendGuardEnabled(ctx) {
		if restriction, blocked := r.activeReplySuppression(event, time.Now()); blocked {
			r.recordReplySuppressionBlocked(event, restriction)
			return nil, errReplySuppressedBeforeSend
		}
	}
	if err := r.interruptedReplyError(ctx, event); err != nil {
		return nil, err
	}
	// 已经写到外部系统的这一轮不能丢：丢了用户就看不到「已经做完了」。
	if turnID, superseded := r.inboundTurnSuperseded(ctx, event); superseded && !hasExternalSideEffect(ctx) {
		r.recordInboundMediaSupersededBeforeSend(ctx, event, turnID)
		return nil, errInboundTurnSuperseded
	}
	if run, ok := proactiveReplyRunFromContext(ctx); ok && run.allowSuperseding {
		if changed, newer := r.proactiveReplyBatchChanged(run.key, run.generation); changed {
			if newer != nil {
				r.recordProactiveReplySuperseded(ctx, event, newer.Event, "before_send")
			}
			return nil, errProactiveReplySuperseded
		}
	}
	action := "send_private_msg"
	if event.Kind == EventKindGroup {
		action = "send_group_msg"
	}
	// 同一条入站事件重跑时，已经成功送达的这一步不再发第二遍。
	stepKey, replayedMessageID, alreadyDelivered := r.claimOutboundStep(ctx, outgoingMessageFingerprint(msg))
	if alreadyDelivered {
		return replayedOutboundResult(replayedMessageID), nil
	}
	r.recordInboundDelivery(event, OutboundDeliveryGenerated, "", "")
	r.recordInboundDelivery(event, OutboundDeliverySendAttempted, "", "")
	ctx = outboundMessageContext(ctx, msg)
	refreshMedia, releaseMedia, err := r.leaseOutgoingMedia(msg)
	if err != nil {
		r.recordInboundDelivery(event, OutboundDeliveryFailed, "", err.Error())
		return nil, err
	}
	defer releaseMedia()
	result, err := r.executeOutboundCall(ctx, event, action, func(callCtx context.Context) (map[string]any, error) {
		if err := refreshMedia(); err != nil {
			return nil, err
		}
		attempts := r.effectiveConfigForEvent(event).SendRetryAttempts
		if replySuppressionSendGuardEnabled(ctx) || event.Kind == EventKindGroup || r.outboundBackoffEnabled(event) {
			attempts = 1
		}
		return r.sendChannelWithRetry(callCtx, msg, attempts, event)
	})
	if err != nil {
		r.recordInboundDelivery(event, OutboundDeliveryFailed, "", err.Error())
		return nil, err
	}
	messageID := apiMessageID(result)
	if r.outboundResultAcknowledged(event, result) {
		r.recordInboundDelivery(event, OutboundDeliveryAcknowledged, messageID, "")
	}
	r.recordOutboundStep(ctx, stepKey, messageID)
	if !telegramMessageNeedsSteps(msg) {
		r.rememberImageModels(event, msg, messageID)
	}
	outboundTurnFromContext(ctx).recordSentMessage(msg)
	if !r.rememberTelegramPhotoResults(ctx, event, msg, result) {
		r.rememberOutgoingWithMessageID(ctx, event, msg, messageID)
	}
	// 回复已经发出去了，「正在输入」到此为止：平台自己会清掉状态，这一轮剩下的
	// 后处理期间不该再刷新，不然最后一条回复之后还会再闪几次。
	typingIndicatorFromContext(ctx).pause()
	return result, nil
}

func (r *Runtime) outboundResultAcknowledged(event MessageEvent, result map[string]any) bool {
	if len(result) > 0 {
		return true
	}
	r.mu.RLock()
	channel := r.channel
	r.mu.RUnlock()
	if multi, ok := channel.(*MultiChannel); ok {
		binding, err := multi.bindingFor(event.ProfileID, event.Platform)
		if err != nil {
			return false
		}
		_, ok := binding.Channel.(ResultChannel)
		return ok
	}
	_, ok := channel.(ResultChannel)
	return ok
}

func (r *Runtime) recordInboundDelivery(event MessageEvent, stage OutboundDeliveryStage, outboundMessageID, detail string) {
	r.mu.RLock()
	store, _ := r.inboundStore.(InboundEventDeliveryAuditStore)
	r.mu.RUnlock()
	if store == nil || strings.TrimSpace(event.MessageID) == "" {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.RecordInboundEventDelivery(auditCtx, event, stage, outboundMessageID, detail); err != nil {
		log.Printf("diana persist outbound delivery stage failed: %v", err)
	}
}

func repositoryWatchDeliveryTargets(item Reminder) []MessageEvent {
	targetValues := decodeReminderDeliveryTargets(item.NotificationTargetsJSON)
	if len(targetValues) == 0 {
		return []MessageEvent{reminderSourceEvent(item)}
	}
	targets := make([]MessageEvent, 0, len(targetValues))
	// 建订阅时已经去过重，但更早写下的订阅没经过这一步；同一个目标出现两次，
	// 一条动态就会原样发两遍。投递前再去一次，代价只有一个 map。
	seen := make(map[string]struct{}, len(targetValues))
	for _, target := range targetValues {
		event := MessageEvent{Kind: EventKindPrivate, Platform: target.Platform, ProfileID: target.ProfileID, ContextNamespace: target.ContextNamespace, UserID: target.UserID}
		if target.GroupID != "" {
			// UserID 保留：群目标的去重键只看群号（见 messageEventDeliveryKey），
			// 留着它是为了投递时能 @ 上当初订阅的人。
			event.Kind, event.GroupID = EventKindGroup, target.GroupID
		}
		key := messageEventDeliveryKey(event)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, event)
	}
	if len(targets) == 0 {
		return []MessageEvent{reminderSourceEvent(item)}
	}
	return targets
}

// messageEventDeliveryKey 把一个投递目标压成可比较的字符串。平台侧的 ID 大小写
// 不敏感（Telegram 用户名、QQ 号都不区分），统一小写再比。
func messageEventDeliveryKey(event MessageEvent) string {
	id := event.UserID
	if event.Kind == EventKindGroup {
		id = event.GroupID
	}
	return strings.ToLower(strings.Join([]string{event.Platform, event.ProfileID, event.ContextNamespace, string(event.Kind), id}, "|"))
}

// chunkOverflowAllowance 返回这个上限下实际允许超出多少。
// 「碎片」是相对分条长度而言的：上限本身只有几个字时（测试里会这么用），
// 固定容差会把一切都吞掉，所以再按上限的四分之一压一道。
func chunkOverflowAllowance(chunkSize int) int {
	if allowance := chunkSize / 4; allowance < chunkOrphanTolerance {
		return allowance
	}
	return chunkOrphanTolerance
}

// chunkTextByLength 是发言和通知共用的长度兜底切分：超过 chunkSize 就在 chunkSize
// 之内找一个体面的断点，空段直接丢掉。两条投递路径的分条规则不同（发言认空行，
// 通知不认），但「怎么切一段超长文本」必须只有一份实现——之前各写一遍，结果修
// 好了发言那边、通知那边还在从人名和链接中间硬切。
func chunkTextByLength(text string, chunkSize int) []string {
	if chunkSize <= 0 {
		if text = strings.TrimSpace(text); text != "" {
			return []string{text}
		}
		return nil
	}
	runes := []rune(strings.TrimSpace(text))
	var out []string
	// 只在明显超长时才切。分条长度是人格偏好，不是平台硬限制（那个是
	// notificationChunkSize，宽得多），为了守住它而多发一条碎片是本末倒置：
	// 162 字撞上 160 的上限，切出来是「…正规零售版5060」加一条「Ti」。
	//
	// 加了这道门槛之后，切出来的尾巴一定长于容差——循环进得来就说明总长超过
	// chunkSize+容差，而切点不会超过 chunkSize，余下的自然更长。所以碎片不只是
	// 这一次不出现，是不可能出现。
	allowance := chunkOverflowAllowance(chunkSize)
	for len(runes) > chunkSize+allowance {
		cut := replyChunkCut(runes, chunkSize)
		if trimmed := strings.TrimSpace(string(runes[:cut])); trimmed != "" {
			out = append(out, trimmed)
		}
		runes = runes[cut:]
	}
	if trimmed := strings.TrimSpace(string(runes)); trimmed != "" {
		out = append(out, trimmed)
	}
	return out
}

// replyChunkCut 给超长段落找一个体面的切分点：优先窗口内最后一个换行，退而
// 求其次找空白，都没有才按字数硬切。硬切会把排行榜这类逐行列表从人名中间
// 劈开，拆成「9. t」和「：0（初识）」两条消息。只在窗口后 2/3 内回退，免得
// 某行特别长时切出一堆碎条。
func replyChunkCut(runes []rune, chunkSize int) int {
	floor := chunkSize / 3
	// 从后往前找断点，同一优先级取最靠后的那个：换行 > 句末 > 分句 > 空白。
	//
	// 只找换行和空白是不够的——中文既没有词间空格，一段纯中文里这两样一个都没有，
	// 于是每次都退回硬切，把「所以不会」和「冒充自己亲身体验过」劈成两条。标点是
	// 中文里唯一的断点，必须认。
	var cuts [3]int
	for i := chunkSize; i > floor; i-- {
		rank := replyCutRank(runes[i-1])
		if rank > 0 && cuts[rank-1] == 0 {
			cuts[rank-1] = i
		}
	}
	for _, cut := range cuts {
		if cut > 0 {
			return cut
		}
	}
	return chunkSize
}
