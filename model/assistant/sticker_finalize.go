// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
	"sync"

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

func stickerReplyOrderPrompt(order StickerReplyOrder) string {
	switch normalizeStickerReplyOrder(order) {
	case StickerReplyOrderAuto:
		return ""
	case StickerReplyOrderAfter:
		return "表情包顺序已固定为 after：有正文时先发正文再发表情包，优先于自主选择顺序的指导；sticker_order 填 after。是否配图和纯图回复仍由你判断。"
	default:
		return "表情包顺序已固定为 before：有正文时先发表情包再发正文，优先于自主选择顺序的指导；sticker_order 填 before。是否配图和纯图回复仍由你判断。"
	}
}

func stickerFinalizeField() agent.FinalizeField {
	return agent.FinalizeField{
		Name: stickerFinalizeFieldName,
		// 「配不配由你判断」「挑不到就不发」是回放里触发率和误发的平衡点，别删。
		Description: "想配表情包时填 2～6 个空格分隔的短关键词，如「得意 叉腰」。有正文时一起发，先后遵循固定配置，auto 时看 sticker_order；只回图填 silent=true、content 留空。挑不到合适的就不发。配不配由你按聊天判断。",
	}
}

func stickerOrderField() agent.FinalizeField {
	return agent.FinalizeField{
		Name:        stickerOrderFieldName,
		Description: "固定顺序配置优先；仅 auto 时自主二选一：before 先甩图（对别人这句话的反应），after 后甩图（给自己这段话收尾点题）",
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

// sendFinalizeSticker 按关键词配一张表情包，发送顺序由调用方决定。
// 配不上、到了上限或普通发送错误只记日志，不阻断正文；父上下文取消由调用方尊重。
func (r *Runtime) sendFinalizeSticker(ctx context.Context, event MessageEvent, query string) {
	if strings.TrimSpace(query) == "" {
		return
	}
	_, settings, enabled := r.pluginWithSettingsForEvent(stickerPluginID, event)
	if !enabled {
		return
	}
	if _, err := newDianaStickerTool(r, event, settings).sendBestMatch(ctx, query); err != nil {
		log.Printf("diana finalize sticker failed: message_id=%s err=%v", event.MessageID, err)
	}
}
