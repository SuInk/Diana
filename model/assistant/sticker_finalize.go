// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/agent"
)

// 让模型收尾时顺手填几个关键词，比让它先 tools_load sticker、再 search、再 send
// 容易得多：线上回放里，只改说明和提示词，模型在适合发表情包的场合一次都没去加载
// 工具（gpt-6-sol 0/180）。sticker 工具保留给「明确要表情包」这种场合。
const (
	stickerFinalizeFieldName = "sticker"
	// stickerOrderFieldName 只在 auto 配置下让模型决定表情包和文字谁先发。
	stickerOrderFieldName = "sticker_order"
	stickerOrderBefore    = "before"
	stickerOrderAfter     = "after"
)

// StickerReplyOrder 固定自动配图的发送顺序；auto 才采纳模型的 sticker_order。
type StickerReplyOrder string

const (
	StickerReplyOrderBefore StickerReplyOrder = "before"
	StickerReplyOrderAfter  StickerReplyOrder = "after"
	StickerReplyOrderAuto   StickerReplyOrder = "auto"
)

func normalizeStickerReplyOrder(order StickerReplyOrder) StickerReplyOrder {
	switch order {
	case StickerReplyOrderBefore, StickerReplyOrderAfter, StickerReplyOrderAuto:
		return order
	default:
		return StickerReplyOrderBefore
	}
}

func stickerReplyBefore(order StickerReplyOrder, modelBefore bool) bool {
	switch normalizeStickerReplyOrder(order) {
	case StickerReplyOrderAuto:
		return modelBefore
	case StickerReplyOrderAfter:
		return false
	default:
		return true
	}
}

// stickerReplyOrderPrompt 只在 auto 时教模型选先后；固定顺序由发送端直接执行，
// 不必让模型知道，也不给它 sticker_order 字段。
func stickerReplyOrderPrompt(order StickerReplyOrder) string {
	if normalizeStickerReplyOrder(order) != StickerReplyOrderAuto {
		return ""
	}
	return "表情包和文字的先后由你在 sticker_order 里选：图是你对别人这句话的反应（笑死、震惊、无语、委屈、被夸得意）就 before，先甩图再补话；图是给你自己这段话收尾点题（晚安、说完一段后叉腰）才 after，别习惯性都放后面。"
}

// stickerFinalizeFields 是收尾时给模型的表情包字段；sticker_order 只在 auto 时提供。
func stickerFinalizeFields(order StickerReplyOrder) []agent.FinalizeField {
	fields := []agent.FinalizeField{stickerFinalizeField()}
	if normalizeStickerReplyOrder(order) == StickerReplyOrderAuto {
		fields = append(fields, stickerOrderField())
	}
	return fields
}

func stickerFinalizeField() agent.FinalizeField {
	return agent.FinalizeField{
		Name: stickerFinalizeFieldName,
		// 「配不配由你判断」「挑不到就不发」是回放里触发率和误发的平衡点，别删。
		Description: "想配表情包时填 2～6 个空格分隔的短关键词，如「得意 叉腰」。有正文时一起发；只回图就 text 留空。挑不到合适的就不发。配不配由你按聊天判断。",
	}
}

func stickerOrderField() agent.FinalizeField {
	return agent.FinalizeField{
		Name:        stickerOrderFieldName,
		Description: "before 先甩图（对别人这句话的反应），after 后甩图（给自己这段话收尾点题）",
		// 以前写的是「after 或留空先说完再甩图」，模型大多直接不填，线上一天 94 张
		// 只有 1 张先发。两个值写成对等的选项，不给留空一个现成的默认。
		// 改成枚举后 9/29–10/1 是 13/78 先发：接梗的「哈哈哈救命」「草」也选 after，
		// 所以 before 写成「对别人的反应」、after 收窄到「给自己这段话收尾」。
		Enum: []string{stickerOrderBefore, stickerOrderAfter},
	}
}

type finalizeStickerKey struct{}

// finalizeSticker 把收尾时填的关键词从 generateReply 带到发送阶段。只有主回复路径会
// 在 ctx 里放它；其他调用 generateReply 的路径（事件触发、后台任务）拿不到就不配图。
type finalizeSticker struct {
	mu     sync.Mutex
	query  string
	before bool
}

func withFinalizeSticker(ctx context.Context) (context.Context, *finalizeSticker) {
	holder := &finalizeSticker{}
	return context.WithValue(ctx, finalizeStickerKey{}, holder), holder
}

func finalizeStickerFromContext(ctx context.Context) *finalizeSticker {
	holder, _ := ctx.Value(finalizeStickerKey{}).(*finalizeSticker)
	return holder
}

func (f *finalizeSticker) set(query, order string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.query = strings.TrimSpace(query)
	f.before = strings.EqualFold(strings.TrimSpace(order), stickerOrderBefore)
	f.mu.Unlock()
}

// take 取出关键词和「是否先于文字发」并清空，同一轮只配一次。
func (f *finalizeSticker) take() (string, bool) {
	if f == nil {
		return "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	query, before := f.query, f.before
	f.query, f.before = "", false
	return query, before
}

const (
	stickerBeforeReplyTimeout = 3 * time.Second
	stickerReplySendReserve   = 5 * time.Second
)

// sendFinalizeSticker 按收尾关键词配一张表情包。before 表示它排在正文前面：这时
// 只短暂等待准备，免得拖住正文；预留预算是进入投递前的判断，不保证平台投递耗时。
// 配不上、到了上限或普通发送错误只记日志，不阻断正文；父上下文取消由调用方尊重。
func (r *Runtime) sendFinalizeSticker(ctx context.Context, event MessageEvent, query string, before bool) {
	if ctx.Err() != nil || strings.TrimSpace(query) == "" {
		return
	}
	prepareCtx := ctx
	if before {
		deadline := time.Now().Add(stickerBeforeReplyTimeout)
		if parentDeadline, ok := ctx.Deadline(); ok {
			latest := parentDeadline.Add(-stickerReplySendReserve)
			if !latest.After(time.Now()) {
				return
			}
			if latest.Before(deadline) {
				deadline = latest
			}
		}
		var cancel context.CancelFunc
		prepareCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	_, settings, enabled := r.pluginWithSettingsForEvent(stickerPluginID, event)
	if !enabled {
		return
	}
	tool := newDianaStickerTool(r, event, settings)
	type preparation struct {
		candidate *stickerCandidate
		err       error
	}
	// 准备放进 goroutine：选图和审核里有模型调用，不一定及时响应取消。
	ready := make(chan preparation, 1)
	go func() {
		defer close(ready)
		defer recoverGoroutinePanic("sticker.prepare")
		candidate, err := tool.prepareBestMatch(prepareCtx, query)
		ready <- preparation{candidate: candidate, err: err}
	}()
	select {
	case <-prepareCtx.Done():
		return
	case result, ok := <-ready:
		if !ok || prepareCtx.Err() != nil {
			return
		}
		if result.err != nil {
			log.Printf("diana finalize sticker prepare failed: message_id=%s err=%v", event.MessageID, result.err)
			return
		}
		if result.candidate == nil {
			return
		}
		// 只有持有发送门锁的调用方可以发送；超时后 worker 的结果只能丢弃。
		// 投递沿用父上下文与平台确认语义，准备 deadline 不撤回已经开始的发送。
		if _, err := tool.deliver(ctx, *result.candidate); err != nil {
			log.Printf("diana finalize sticker failed: message_id=%s err=%v", event.MessageID, err)
		}
	}
}
