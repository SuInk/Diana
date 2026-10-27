// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 联网收尾前的细节复核。
//
// research 类回放里失败几乎都是同一种：搜到的只覆盖一角（单个赛季公告、名字相近的
// 仓库），正文却把整份更新清单、出处归属写得像查证过。同样的要求已经写在调研策略、
// 检索观察和 send 说明里，gemini 照样凭印象补全；把这句话再加一遍没有效果，
// 让它在收尾信封里自报「哪些没原文」，它也会对自己的记忆填「无」。所以这里换一个
// 只看证据的调用：拿草稿和本轮工具读到的内容逐项对照，列出找不到原文的细节，
// 打回一次让主模型删掉或标成没查实。
//
// 只看工具输出和当前消息，不看群聊历史：它只判断「这条具体细节有没有原文」，
// 不判断对错，也不判断该不该回。复核调用失败或输出看不懂时放行，不拦正常回复。
const (
	researchReviewEvidenceBudget = 24000
	researchReviewStepBudget     = 6000
)

const researchReviewPrompt = `你在核对一条群聊机器人回复草稿的具体细节有没有出处。输入是对方的问题、本轮工具读到的内容（搜索候选和网页正文）以及草稿。
找出草稿里在工具内容中找不到原文支持、却写成确定事实的具体细节：版本号、日期、数字、价格、规格，更新或功能清单里的条目，出处、归属、谁基于谁。
不算问题的：常识背景；对问题的复述；草稿已经说明是印象、推测、没查实的部分；工具内容里能找到的内容（意思一致即可，不要求逐字）。
只输出 JSON：{"unsupported":["细节1","细节2"]}，没有就输出 {"unsupported":[]}。每条用草稿里的原词，最多 8 条。`

// researchedThisRun 报告这一轮有没有成功调用过联网检索或网页读取。
func researchedThisRun(steps []Step) bool {
	for _, step := range steps {
		if (step.Tool == webSearchToolName || step.Tool == browserRenderToolName) && !step.Skipped && step.Error == "" {
			return true
		}
	}
	return false
}

// reviewUnsupportedDetails 返回草稿里没有工具原文支持的细节，用顿号连接；没有或复核失败返回空串。
func (r *Runner) reviewUnsupportedDetails(ctx context.Context, history []llm.Message, draft string, steps []Step) string {
	draft = strings.TrimSpace(draft)
	evidence := researchReviewEvidence(steps)
	if draft == "" || evidence == "" {
		return ""
	}
	question := ""
	if len(history) > 0 {
		question = history[len(history)-1].Content
	}
	resp, err := r.client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: researchReviewPrompt},
		{Role: llm.RoleUser, Content: "【问题】\n" + question + "\n\n【工具内容】\n" + evidence + "\n\n【草稿】\n" + draft},
	}})
	if err != nil || resp == nil {
		return ""
	}
	return parseUnsupportedDetails(resp.Text)
}

func parseUnsupportedDetails(text string) string {
	text = strings.TrimSpace(text)
	if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		text = text[start : end+1]
	}
	var verdict struct {
		Unsupported []string `json:"unsupported"`
	}
	if json.Unmarshal([]byte(text), &verdict) != nil {
		return ""
	}
	var items []string
	for _, item := range verdict.Unsupported {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
		if len(items) == 8 {
			break
		}
	}
	return strings.Join(items, "、")
}

func unsupportedDetailsRepair(unsupported string) string {
	return "复核发现正文里这些细节在本轮读到的工具内容里找不到原文：" + unsupported +
		"。重新 send：能再查就先查；不查的话把它们从正文删掉，或明说是印象、没查实。来源只覆盖一部分（例如单个版本公告、名字相近的另一个项目）时只按它覆盖的范围下结论。查到的部分照常回答，不要因此整条推脱。"
}

// researchReviewEvidence 把本轮检索和读页的输出压成可读文本。web_search 的 JSON 开头
// 全是查询、预算之类的元数据，直接截前缀等于没给证据，所以按字段取标题、摘要和正文。
func researchReviewEvidence(steps []Step) string {
	var b strings.Builder
	for _, step := range steps {
		if step.Skipped || step.Error != "" {
			continue
		}
		var text string
		switch step.Tool {
		case webSearchToolName:
			text = webSearchEvidenceText(step.Output)
		case browserRenderToolName:
			text = browserRenderEvidenceText(step.Output)
		default:
			continue
		}
		if text = strings.TrimSpace(text); text == "" {
			continue
		}
		if r := []rune(text); len(r) > researchReviewStepBudget {
			text = string(r[:researchReviewStepBudget]) + "…"
		}
		b.WriteString("## " + step.Tool + "\n" + text + "\n\n")
		if b.Len() > researchReviewEvidenceBudget*3 {
			break
		}
	}
	out := []rune(b.String())
	if len(out) > researchReviewEvidenceBudget {
		out = out[:researchReviewEvidenceBudget]
	}
	return strings.TrimSpace(string(out))
}

func webSearchEvidenceText(output string) string {
	var result webSearchResult
	if json.Unmarshal([]byte(output), &result) != nil {
		return output
	}
	var b strings.Builder
	if content := strings.TrimSpace(result.Content); content != "" {
		b.WriteString(content + "\n")
	}
	for _, hit := range result.Results {
		b.WriteString("- " + hit.Title + " " + hit.URL + "\n  " + strings.TrimSpace(hit.Snippet) + "\n")
	}
	return b.String()
}

func browserRenderEvidenceText(output string) string {
	pages := browserRenderPages(output)
	if len(pages) == 0 {
		return output
	}
	var b strings.Builder
	for _, page := range pages {
		b.WriteString(page.Title + " " + page.URL + "\n")
		for _, match := range page.FindMatches {
			b.WriteString(match + "\n")
		}
		b.WriteString(page.Text + "\n")
	}
	return b.String()
}
