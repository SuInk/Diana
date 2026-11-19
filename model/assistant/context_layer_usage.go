// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

// 层内丢弃原因。它们描述的是「这一层自己在拼装时丢了什么」，与 llm 包那组
// ContextBudgetReason 不是一回事：后者是所有层拼完之后、全局预算再裁一刀的结果。
const (
	// contextLayerReasonFits 表示候选全部装进了本层配额。
	contextLayerReasonFits = "fits_layer_budget"
	// contextLayerReasonRankCap 表示是排序阶段的条数上限先卡住的，不是 token 配额。
	contextLayerReasonRankCap = "rank_cap"
	// contextLayerReasonBudget 表示 token 配额用尽，尾部候选没装下。
	contextLayerReasonBudget = "layer_budget"
	// contextLayerReasonSectionCut 表示配额在分段中途用尽，后面整段没渲染。
	// 它比单纯的 layer_budget 更值得报警：分段有固定顺序，前面几条长记忆就能
	// 让后面整段消失，而日志里「这段没内容」和「这段被挤掉了」长得一模一样。
	contextLayerReasonSectionCut = "section_cut"
)

// contextLayerUsage 是某一层在进入全局预算之前的自有账。
//
// 没有它的话，diana.context_budget 里的 reason 只能证明「拼成的这条消息进
// 128K 全局预算后没再挨刀」，证明不了层内配额有没有截断候选——各层在送进全局
// 预算之前就已经按自己的配额裁剪并拼成一条成品消息了。
type contextLayerUsage struct {
	Layer  string `json:"layer"`
	Budget int64  `json:"budget_tokens"`
	// Candidate* 是本层拿到手的全部候选，Ranked* 是排序/去重之后还在竞争配额的部分。
	CandidateItems  int   `json:"candidate_items"`
	CandidateTokens int64 `json:"candidate_tokens"`
	RankedItems     int   `json:"ranked_items"`
	RankedTokens    int64 `json:"ranked_tokens"`
	// Selected* 是真正写进提示词的部分。
	SelectedItems  int    `json:"selected_items"`
	SelectedTokens int64  `json:"selected_tokens"`
	Reason         string `json:"reason"`
	// DroppedSections 记下因配额用尽而整段没渲染的段名。
	DroppedSections []string `json:"dropped_sections,omitempty"`
}

func (u contextLayerUsage) droppedItems() int {
	dropped := u.CandidateItems - u.SelectedItems
	if dropped < 0 {
		return 0
	}
	return dropped
}

func (u contextLayerUsage) droppedTokens() int64 {
	dropped := u.CandidateTokens - u.SelectedTokens
	if dropped < 0 {
		return 0
	}
	return dropped
}

// contextLayerUsageTrace 把各层的账渲染成日志字段。
func contextLayerUsageTrace(layers []contextLayerUsage) []map[string]any {
	trace := make([]map[string]any, 0, len(layers))
	for _, usage := range layers {
		if usage.Layer == "" {
			continue
		}
		entry := map[string]any{
			"layer":            usage.Layer,
			"budget_tokens":    usage.Budget,
			"candidate_items":  usage.CandidateItems,
			"candidate_tokens": usage.CandidateTokens,
			"ranked_items":     usage.RankedItems,
			"ranked_tokens":    usage.RankedTokens,
			"selected_items":   usage.SelectedItems,
			"selected_tokens":  usage.SelectedTokens,
			"dropped_items":    usage.droppedItems(),
			"dropped_tokens":   usage.droppedTokens(),
			"reason":           usage.Reason,
			"reason_text":      contextLayerReasonText(usage.Reason),
		}
		if len(usage.DroppedSections) > 0 {
			entry["dropped_sections"] = usage.DroppedSections
		}
		trace = append(trace, entry)
	}
	return trace
}

func contextLayerReasonText(reason string) string {
	switch reason {
	case contextLayerReasonFits:
		return "候选全部装入本层配额"
	case contextLayerReasonRankCap:
		return "排序阶段的条数上限先卡住，未触及 token 配额"
	case contextLayerReasonBudget:
		return "本层 token 配额用尽，尾部候选未装入"
	case contextLayerReasonSectionCut:
		return "本层配额在分段中途用尽，后续整段未渲染"
	default:
		return reason
	}
}
