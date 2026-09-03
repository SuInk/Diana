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

// 连续失败时的错误提示节流。
//
// 一条消息回复失败就发一条「出错了：……」，单看没问题；但掉线重连后补历史消息
// 时，几十条积压消息会在几秒内一起失败，于是群里刷出几十条一模一样的报错。这里
// 按会话把失败归成「一次爆发」：
//
//	本轮第一条失败        立刻发，故障要第一时间看得见
//	爆发期内的后续失败    不单独发，只计数
//	爆发结束（安静下来）  补一条汇总，说清还有多少条没能回复
//	超过新鲜期的旧消息    从不单独发，只进汇总计数
//
// 关键是汇总一定会发出去：合并的目的是把 N 条压成 1 条，不是把 N 条压成 0 条，
// 否则就分不清「偶发故障」和「一直在坏」了。
const (
	// defaultErrorNoticeBurstQuiet 是判定爆发结束需要的安静时长。
	defaultErrorNoticeBurstQuiet = time.Minute
	// defaultErrorNoticeBurstMaxWait 是汇总最多憋多久。持续故障不会一直安静
	// 下来，没有这个上限的话汇总会被后续失败无限往后推。
	defaultErrorNoticeBurstMaxWait = 5 * time.Minute
	// defaultErrorNoticeFreshWindow 是「这条消息还算新鲜」的界限。补历史消息
	// 时事件时间是原始发送时间，超过这个窗口就说明是补的旧消息，不值得为它单
	// 独打断当前的群聊。
	defaultErrorNoticeFreshWindow = 30 * time.Minute
	// errorNoticeSummaryTimeout 给汇总投递留的时间预算。
	errorNoticeSummaryTimeout = 30 * time.Second
)

// errorNoticeBurst 是一个会话当前这轮连续失败的状态。
type errorNoticeBurst struct {
	// startedAt 是本轮计时的起点，用来卡 maxWait；每次发出汇总后重新计。
	startedAt time.Time
	// pending 是还没被任何一条提示交代过的失败条数。
	pending int
	// announced 表示这轮已经发过提示了，汇总因此用「另外还有」的口吻。
	announced bool
	// detail 留最后一次的失败原因：连续失败通常同因，最新的一条最接近现状。
	detail string
	// event 只用来定位发到哪个会话，汇总不引用其中任何一条具体消息。
	event    MessageEvent
	timer    *time.Timer
	timerSeq uint64
	flushing bool
}

// claimErrorNotice 判断这次失败该不该立刻发提示。
//
// 返回 true 表示调用方照常发「出错了：……」；返回 false 表示这条已经并进汇总，
// 调用方不要再单独发。
func (r *Runtime) claimErrorNotice(event MessageEvent, detail string) bool {
	r.mu.RLock()
	ctx := r.runCtx
	running := r.running
	r.mu.RUnlock()
	// 运行时没在跑就没有能兜底发汇总的地方，退回「每条都发」的老行为，
	// 免得把提示合并成谁也收不到。
	if !running || ctx == nil {
		return true
	}

	now := time.Now()
	stale := r.errorNoticeEventStale(event, now)
	key := sessionKey(event)

	r.errorNoticeMu.Lock()
	defer r.errorNoticeMu.Unlock()
	burst := r.errorNoticeBursts[key]
	if burst == nil {
		burst = &errorNoticeBurst{startedAt: now}
		r.errorNoticeBursts[key] = burst
		if !stale {
			burst.announced = true
			burst.detail = detail
			burst.event = event
			r.scheduleErrorNoticeFlushLocked(ctx, key, burst, now)
			return true
		}
	}
	burst.pending++
	burst.detail = detail
	burst.event = event
	r.scheduleErrorNoticeFlushLocked(ctx, key, burst, now)
	return false
}

// noteErrorNoticeSendFailed 用于立刻发的那条提示自己也没发出去的情况：这轮不能
// 算已经说过了，这条失败要重新排进汇总。
func (r *Runtime) noteErrorNoticeSendFailed(event MessageEvent, detail string) {
	key := sessionKey(event)
	r.errorNoticeMu.Lock()
	defer r.errorNoticeMu.Unlock()
	burst := r.errorNoticeBursts[key]
	if burst == nil {
		return
	}
	burst.announced = false
	burst.pending++
	if strings.TrimSpace(detail) != "" {
		burst.detail = detail
	}
	burst.event = event
}

// errorNoticeEventStale 判断这条消息是不是补历史带上来的旧消息。
func (r *Runtime) errorNoticeEventStale(event MessageEvent, now time.Time) bool {
	if event.Time <= 0 {
		return false
	}
	window := r.errorNoticeFreshWindow
	if window <= 0 {
		window = defaultErrorNoticeFreshWindow
	}
	return now.Sub(time.Unix(event.Time, 0)) > window
}

func (r *Runtime) scheduleErrorNoticeFlushLocked(ctx context.Context, key string, burst *errorNoticeBurst, now time.Time) {
	if burst.timer != nil {
		burst.timer.Stop()
	}
	wait := r.errorNoticeQuiet
	if wait <= 0 {
		wait = defaultErrorNoticeBurstQuiet
	}
	maxWait := r.errorNoticeMaxWait
	if maxWait <= 0 {
		maxWait = defaultErrorNoticeBurstMaxWait
	}
	if remaining := maxWait - now.Sub(burst.startedAt); remaining < wait {
		wait = remaining
	}
	if wait < 0 {
		wait = 0
	}
	burst.timerSeq++
	seq := burst.timerSeq
	burst.timer = time.AfterFunc(wait, func() {
		r.flushErrorNoticeBurst(ctx, key, seq)
	})
}

// flushErrorNoticeBurst 在爆发结束或憋到上限时补一条汇总。
func (r *Runtime) flushErrorNoticeBurst(ctx context.Context, key string, seq uint64) {
	now := time.Now()
	r.errorNoticeMu.Lock()
	burst := r.errorNoticeBursts[key]
	// timerSeq 对不上说明这个定时器已经被后来的失败重排掉了，交给新的那个。
	if burst == nil || burst.timerSeq != seq || burst.flushing {
		r.errorNoticeMu.Unlock()
		return
	}
	if burst.pending == 0 || (ctx != nil && ctx.Err() != nil) {
		// 没有欠着的提示（或者运行时已经停了）：清掉状态，下一次失败重新
		// 算「本轮第一条」，立刻发。
		if burst.timer != nil {
			burst.timer.Stop()
		}
		delete(r.errorNoticeBursts, key)
		r.errorNoticeMu.Unlock()
		return
	}
	count := burst.pending
	announced := burst.announced
	text := errorNoticeSummaryText(count, announced, burst.detail)
	event := burst.event
	burst.pending = 0
	burst.announced = true
	burst.startedAt = now
	burst.flushing = true
	r.errorNoticeMu.Unlock()

	sendCtx, cancel := context.WithTimeout(ctx, errorNoticeSummaryTimeout)
	err := r.sendErrorNoticeSummary(sendCtx, event, text)
	cancel()
	if err != nil {
		log.Printf("diana error notice summary for %s failed: %v", key, err)
	}

	r.errorNoticeMu.Lock()
	// 汇总发完还要再等一个安静期：这期间的失败继续并进下一条汇总，不会因为
	// 刚说过一次就又立刻单独发一条。
	if burst = r.errorNoticeBursts[key]; burst != nil {
		burst.flushing = false
		r.scheduleErrorNoticeFlushLocked(ctx, key, burst, time.Now())
	}
	r.errorNoticeMu.Unlock()
}

// errorNoticeSummaryText 拼汇总正文。
func errorNoticeSummaryText(count int, announced bool, detail string) string {
	var builder strings.Builder
	if announced {
		fmt.Fprintf(&builder, "另外还有 %d 条消息也没能回复", count)
	} else {
		fmt.Fprintf(&builder, "有 %d 条消息没能回复", count)
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		builder.WriteString("：")
		builder.WriteString(detail)
	}
	return builder.String()
}

// sendErrorNoticeSummary 投递汇总。
//
// 汇总说的是一批消息，不是当前这条，所以不挂引用也不 @：引用其中任意一条都会
// 让人以为只有那条出了问题。
func (r *Runtime) sendErrorNoticeSummary(ctx context.Context, event MessageEvent, text string) error {
	cfg := r.effectiveConfigForEvent(event)
	_, err := r.deliverChunks(ctx, event, splitReply(text, notificationChunkSize), cfg, outboundDecoration{})
	return err
}

func (r *Runtime) clearErrorNoticeBursts() {
	r.errorNoticeMu.Lock()
	for key, burst := range r.errorNoticeBursts {
		if burst.timer != nil {
			burst.timer.Stop()
		}
		delete(r.errorNoticeBursts, key)
	}
	r.errorNoticeMu.Unlock()
}
