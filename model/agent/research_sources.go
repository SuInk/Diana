// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"encoding/json"
	"strings"
)

// Source stages describe retrieval, not factual support. A read page can still
// be outdated, unofficial, or silent about the condition the user asked about.
const (
	sourceStageSearchCandidates = "search_candidates"
	sourceStageNoSources        = "no_sources"
	sourceStagePageRead         = "page_read"
	sourceStagePagePartial      = "page_partial"
	sourceStagePageEmpty        = "page_empty"
)

func searchSourceStage(result webSearchResult) string {
	if result.SourceStage != "" {
		return result.SourceStage
	}
	if len(result.Sources) == 0 && len(result.Results) == 0 {
		return sourceStageNoSources
	}
	return sourceStageSearchCandidates
}

func renderedPageSourceStage(page RenderedPage) string {
	if strings.TrimSpace(page.Text) == "" {
		return sourceStagePageEmpty
	}
	if page.Truncated {
		return sourceStagePagePartial
	}
	return sourceStagePageRead
}

// This guidance also applies to single questions that do not declare claims.
// Use the untruncated output to distinguish an empty result from lost metadata.
func researchObservationGuidance(tool, rawOutput string, remaining int) string {
	var guidance string
	switch tool {
	case WebSearchToolName:
		var result webSearchResult
		if json.Unmarshal([]byte(rawOutput), &result) != nil {
			guidance = "搜索返回未能解析为来源记录；依据实际可见内容判断，不要把执行成功当成事实核验成功。"
		} else if searchSourceStage(result) == sourceStageNoSources {
			guidance = "本次未取得候选来源，不证明所问事实不存在，也不足以推断提问者弄错了或消息只是传闻。根据查询结果和错误判断检索缺口，尝试不同实体名、范围或原始页面；不要只重复原查询。最终仍无证据时说明查了什么、什么尚未确认，不猜缺少结果的事实原因。"
		} else {
			guidance = "本次找到的是候选来源，搜索停止原因只描述检索进度。对当前状态或有争议的事实，核对原始来源的主体、发布或修订日期和正文覆盖的条件；摘要、转载和查询时间不能代替这些证据。来源之间日期或条件冲突时，不能只选一条摘要收尾。仍有缺口时读取相关原始页面或调整查询，尤其区分事件已发生与特定对象、地点或渠道可用；已有来源直接支持所问条件时即可结束。"
		}
	case "browser_render":
		var page browserRenderPayload
		if json.Unmarshal([]byte(rawOutput), &page) != nil {
			guidance = "依据实际可见的页面内容判断；页面读取成功本身不表示所问事实成立。"
		} else {
			switch browserPayloadSourceStage(page) {
			case sourceStagePageEmpty:
				guidance = "没有读到页面正文，标题或空页面不能确认或否定所问事实。根据错误、跳转和来源地址选择其他原始来源。"
			case sourceStagePagePartial:
				guidance = "正文已读取但被截断；只使用可见内容支持的条件，缺失段落不能当成否定证据。需要的条件不在可见部分时，读取更具体的来源或调整查询。某来源只讨论一个范围时，不能据此断言其他范围尚未开放或全部不支持。"
			default:
				guidance = "已读到页面正文；这不自动表示官方、最新或所问条件已被证实。将正文的主体、日期和适用条件与原说法逐项对应；公开提供与免费、无门槛、覆盖所有对象是不同命题，收费或范围限制不能作为‘未公开’的证据。也不要替原说法添加条件来表示赞同。直接支持的部分可以确认，冲突或未覆盖的部分继续核对或限定表达。证据足够时即可结束。"
			}
		}
	default:
		return ""
	}
	if remaining <= 0 {
		return guidance + " 工具预算已耗尽，按已有证据回答，明确仍未确认的部分，不要承诺继续查询。"
	}
	return guidance + " 需要核对时直接调用工具；信息足够时调用 agent_finalize。"
}

func browserPayloadSourceStage(page browserRenderPayload) string {
	if strings.TrimSpace(page.Text) == "" && len(page.FindMatches) > 0 {
		return sourceStagePagePartial
	}
	return renderedPageSourceStage(page.RenderedPage)
}
