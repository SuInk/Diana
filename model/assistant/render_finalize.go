// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"regexp"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/agent"
)

// 榜单、参数对比这类行列数据，模型本来就会写成 Markdown 表格，发到 QQ 却散成一堆竖线。
// render 工具能画，但它是按需加载的：线上 9/20～9/27 该出图的回复一次都没去加载它。
// 收尾时顺手把表格填进这个字段就便宜得多：线上收尾请求回放（gpt-6-sol，每条两次），
// 真表格和榜单 8/8 出图，盲标为不该出图的 126 条 0/252 误填。
const renderFinalizeFieldName = "render"

func renderFinalizeField() agent.FinalizeField {
	return agent.FinalizeField{
		Name: renderFinalizeFieldName,
		Description: "可选。回复里有多列对比、排行榜/榜单、参数对照这类行列数据时，把这部分写成 GitHub 风格 Markdown 表格放在这里，" +
			"运行时会渲染成一张图片随回复发出（聊天窗口不渲染 Markdown，表格发成文字会散成竖线）。" +
			"填了它，content 只写一两句结论或点评，不要把表里的内容再抄一遍。" +
			"普通聊天、解释说明、要对方复制的草稿/代码/命令、三五条的短列表都不要填。",
	}
}

// finalizeRenderPNG 是出图入口；测试替换它，免得起真浏览器。
var finalizeRenderPNG = (*Runtime).renderContentPNG

type finalizeRenderKey struct{}

// finalizeRender 把收尾时画好的表格图从 generateReply 带到正文发出之后。只有主回复
// 路径会在 ctx 里放它；其他路径拿不到，表格直接接回正文发文字。
type finalizeRender struct {
	mu  sync.Mutex
	png []byte
}

func withFinalizeRender(ctx context.Context) (context.Context, *finalizeRender) {
	holder := &finalizeRender{}
	return context.WithValue(ctx, finalizeRenderKey{}, holder), holder
}

func finalizeRenderFromContext(ctx context.Context) *finalizeRender {
	holder, _ := ctx.Value(finalizeRenderKey{}).(*finalizeRender)
	return holder
}

func (f *finalizeRender) set(png []byte) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.png = png
	f.mu.Unlock()
}

// take 取出图片并清空，同一轮只发一次。
func (f *finalizeRender) take() []byte {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	png := f.png
	f.png = nil
	return png
}

// offersRenderFinalizeField 只在这一轮真的画得出来时才给模型这个字段：render 工具挂上了、
// 「网页渲染」插件开着。插件开关按群生效，同一个会话里字段集合保持稳定。
func (r *Runtime) offersRenderFinalizeField(event MessageEvent, registry *agent.ToolRegistry) bool {
	if registry == nil {
		return false
	}
	if _, ok := registry.Get(dianaRenderToolName); !ok {
		return false
	}
	return r.sandboxedBrowserEnabled(event)
}

// applyFinalizeRender 在正文整理之前把收尾时填的表格画成图。画好了交给 ctx 里的
// finalizeRender，正文发出后再发；没有挂点、太长或画失败，就把表格接回正文——
// 退回到现在的纯文本表格，数据不能因为出图失败丢掉。
func (r *Runtime) applyFinalizeRender(ctx context.Context, event MessageEvent, text, markdown string) string {
	// 识图重试会再跑一次 generateReply，上一次画的图不能留到这一次。
	finalizeRenderFromContext(ctx).set(nil)
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return text
	}
	if holder := finalizeRenderFromContext(ctx); holder != nil && len([]rune(markdown)) <= renderImageMaxContent {
		png, err := finalizeRenderPNG(r, ctx, event, renderFormatMarkdown, markdown, "")
		if err == nil {
			holder.set(png)
			return text
		}
		log.Printf("diana finalize render failed, sending table as text: message_id=%s err=%v", event.MessageID, err)
	}
	return appendRenderFallbackText(text, markdown)
}

// appendRenderFallbackText 把表格另起一条接在正文后面。纯文本平台不认 Markdown 表格，
// 这里就地降成「单元格 | 单元格」一行一行，去掉 |---| 分隔行和首尾竖线；回复协议
// 不认字面换行，行与行之间用 [diana-line]。
func appendRenderFallbackText(text, markdown string) string {
	var rows []string
	for _, line := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || renderTableSeparatorRow.MatchString(line) {
			continue
		}
		if strings.HasPrefix(line, "|") && strings.HasSuffix(line, "|") && len(line) > 1 {
			cells := strings.Split(strings.Trim(line, "|"), "|")
			for index := range cells {
				cells[index] = strings.TrimSpace(cells[index])
			}
			line = strings.Join(cells, " | ")
		}
		rows = append(rows, line)
	}
	table := strings.Join(rows, notificationLineMarker)
	if strings.TrimSpace(text) == "" {
		return table
	}
	return strings.TrimSpace(text) + notificationSplitMarker + table
}

var renderTableSeparatorRow = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$`)

// sendFinalizeRender 在正文发出后补发表格图。发送失败只记日志：正文已经发出去了。
func (r *Runtime) sendFinalizeRender(ctx context.Context, event MessageEvent, png []byte) {
	if len(png) == 0 {
		return
	}
	if err := r.sendPNGImage(ctx, event, png); err != nil {
		log.Printf("diana finalize render send failed: message_id=%s err=%v", event.MessageID, err)
	}
}
