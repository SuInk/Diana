package assistant

import (
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// Detail reduction remains a fallback for images that could not be described.
// It is never used to compensate for a text-only overage.
// lowerOverBudgetImageDetail 在图片超预算时把图真正缩小，缩不动的才退回只标低细节。
//
// 以前这里只改 detail 标签。估算表按标签查（low 1024、high 8192），标一下账面立刻
// 少七千多，可 detail 只有 OpenAI 认——Gemini 的请求里根本没有这个字段，图片字节
// 一个没少。于是预算算出来够了，实际发出去还是那么大，账面和实际脱节。
//
// 现在 data URI 的图先按长边缩到 512 再重编码，字节真的变小之后才标 low，那 1024
// 的估算才对得上。远程链接改不动字节，仍旧只标标签：OpenAI 认这个字段，那条路上
// 省是真省。两条都省不动就维持原样，让上层按超预算处理。
func lowerOverBudgetImageDetail(req llm.GenerateRequest, budget int64) llm.GenerateRequest {
	if llm.PlanInputBudget(req, budget).ImageExcess <= 0 {
		return req
	}
	req.Messages = append([]llm.Message(nil), req.Messages...)
	for mi, m := range req.Messages {
		parts := append([]llm.ContentPart(nil), m.Parts...)
		for pi, p := range parts {
			plan := llm.PlanInputBudget(req, budget)
			if !plan.OverBudget() || plan.ImageExcess <= 0 {
				return req
			}
			if p.Type != llm.ContentPartImageURL || p.ImageURL == "" || p.Detail == "low" {
				continue
			}
			// 能缩就先缩字节，缩不缩都标 low。缩不动有两种：图本来就不超过 512，
			// 那它的尺寸本就在低细节档位内，标 low 不虚报；或者解不开，那和改之前一样
			// 只标标签。上一版在缩不动时直接 continue，连标签也不打，小图于是一直按
			// high 的 8192 计——线上一轮 12 张小图因此被判超预算整轮丢掉。
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.ImageURL)), "data:image/") {
				if shrunk, ok := shrinkDataURLImageLongSide(p.ImageURL, budgetLowDetailLongSide); ok {
					parts[pi].ImageURL = shrunk
				}
			}
			parts[pi].Detail = "low"
			req.Messages[mi].Parts = parts
		}
	}
	return req
}

// budgetDroppedImagePlaceholder 替换被丢弃的图片。写明「有图但没送到」，模型才不会
// 假装看过、也不会把后面图片的编号对错。
const budgetDroppedImagePlaceholder = "【此处原有一张图片，因超出输入预算未发送；不要猜测它的内容】"

// dropOverBudgetImages 把图换成占位，直到图片部分装进预算，返回换了几张。文字超出
// 不在这里处理，换图也腾不出文字的地方。
//
// 顺序：先丢旧消息里的图（从最早那条开始），再丢当前这条用户消息里的图，从最后
// 一张往前丢——用户通常先发主图、后补辅助图。每张换成一句占位文字。
func dropOverBudgetImages(req llm.GenerateRequest, budget int64) (llm.GenerateRequest, int) {
	if !llm.PlanInputBudget(req, budget).OverBudget() {
		return req, 0
	}
	lastUser := -1
	for i, message := range req.Messages {
		if message.Role == llm.RoleUser {
			lastUser = i
		}
	}
	type imageSlot struct{ message, part int }
	var order []imageSlot
	for mi, message := range req.Messages {
		if mi == lastUser {
			continue
		}
		for pi, part := range message.Parts {
			if part.Type == llm.ContentPartImageURL && part.ImageURL != "" {
				order = append(order, imageSlot{mi, pi})
			}
		}
	}
	if lastUser >= 0 {
		parts := req.Messages[lastUser].Parts
		for pi := len(parts) - 1; pi >= 0; pi-- {
			if parts[pi].Type == llm.ContentPartImageURL && parts[pi].ImageURL != "" {
				order = append(order, imageSlot{lastUser, pi})
			}
		}
	}
	if len(order) == 0 {
		return req, 0
	}
	req.Messages = append([]llm.Message(nil), req.Messages...)
	copied := make(map[int]bool)
	dropped := 0
	for _, slot := range order {
		if llm.PlanInputBudget(req, budget).ImageExcess <= 0 {
			break
		}
		if !copied[slot.message] {
			req.Messages[slot.message].Parts = append([]llm.ContentPart(nil), req.Messages[slot.message].Parts...)
			copied[slot.message] = true
		}
		req.Messages[slot.message].Parts[slot.part] = llm.ContentPart{Type: llm.ContentPartText, Text: budgetDroppedImagePlaceholder}
		dropped++
	}
	return req, dropped
}
