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
const stickerFinalizeFieldName = "sticker"

func stickerFinalizeField() agent.FinalizeField {
	return agent.FinalizeField{
		Name: stickerFinalizeFieldName,
		Description: "想在这句回复后面配一张表情包时，填 2 到 6 个空格分隔的短关键词（情绪、动作、场景和同义说法），例如“得意 叉腰”“晚安 摸头”。" +
			"正文发出后会自动挑一张贴切的跟在后面，挑不到就不发。只在闲聊、接梗、调侃、吐槽、安慰、庆祝、道谢、道晚安这类以情绪为主的接话里填；认真回答问题、做任务时留空。",
	}
}

type finalizeStickerKey struct{}

// finalizeSticker 把收尾时填的关键词从 generateReply 带到发送之后。只有主回复路径会
// 在 ctx 里放它；其他调用 generateReply 的路径（事件触发、后台任务）拿不到就不配图。
type finalizeSticker struct {
	mu    sync.Mutex
	query string
}

func withFinalizeSticker(ctx context.Context) (context.Context, *finalizeSticker) {
	holder := &finalizeSticker{}
	return context.WithValue(ctx, finalizeStickerKey{}, holder), holder
}

func finalizeStickerFromContext(ctx context.Context) *finalizeSticker {
	holder, _ := ctx.Value(finalizeStickerKey{}).(*finalizeSticker)
	return holder
}

func (f *finalizeSticker) set(query string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.query = strings.TrimSpace(query)
	f.mu.Unlock()
}

// take 取出关键词并清空，同一轮只配一次。
func (f *finalizeSticker) take() string {
	if f == nil {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	query := f.query
	f.query = ""
	return query
}

// sendFinalizeSticker 在正文发出后按关键词配一张表情包。配不上、到了上限或发送失败都
// 只记日志：正文已经发出去了，表情包是锦上添花。
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
