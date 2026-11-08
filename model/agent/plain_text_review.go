// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 退回一次后仍写纯文本时的完成度复核。
//
// Config.PlainTextNotSent 原先在第二次纯文本时直接静默，挡的是「在画了，画好自动发
// 出来」这种一个工具都没调的空口承诺。可 11-08 生产一天 9 轮这样静默，全是写好的正常
// 回复：deepseek-v4-pro 85 轮里 30 轮先写纯文本、退回后 7 轮还是纯文本，内容就是把
// 第一次的话换个说法，图片表情的接话、整段客服记录总结、查完资料的结论都丢了。
//
// 所以第二次纯文本不再直接丢，先单独问一次模型：草稿里说正在做、已经做了或马上会做的
// 事，本轮工具记录里有没有对应的调用。没有对应调用才静默；其余按正常 send 收尾，
// 照常过发送前审核和联网细节复核。复核只看本轮工具记录和对方这条消息，不判断回复
// 好不好、该不该回。复核调用失败或输出看不懂时放行：这一步要防的是空口承诺，而
// 发出去之前还有发送前审核；把整条回复吞掉才是这次要修的问题。
const (
	plainTextReviewStepBudget  = 400
	plainTextReviewStepsBudget = 6000
)

const plainTextReviewPrompt = `你在检查一条聊天机器人回复草稿有没有「说了却没做」。输入是对方的消息、机器人本轮实际调用过的工具记录，以及草稿。
找出草稿里声称机器人自己正在做、已经做完或马上会自动完成的操作（画图、搜索查询、设置提醒、修改配置、发送文件、读取记录等），而工具记录里没有对应成功调用的那一项。
不算问题的：普通聊天、接话、表情回应；凭常识或上下文直接回答、总结、解释；向对方提问或请对方补充；拒绝或说明做不到；建议对方自己去做；工具记录里能找到对应调用的操作。
只输出 JSON：{"unfinished":"草稿原话里没做到的那一项"}，没有就输出 {"unfinished":""}。`

// reviewPlainTextCompletion 返回草稿里说了却没做的操作；没有、或复核失败时返回空串。
func (r *Runner) reviewPlainTextCompletion(ctx context.Context, history []llm.Message, draft string, steps []Step) string {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return ""
	}
	resp, err := r.client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: plainTextReviewPrompt},
		{Role: llm.RoleUser, Content: "【对方的消息】\n" + lastUserMessage(history) + "\n\n【本轮工具记录】\n" + plainTextReviewSteps(steps) + "\n\n【草稿】\n" + draft},
	}})
	if err != nil || resp == nil {
		return ""
	}
	return parseUnfinishedClaim(resp.Text)
}

func parseUnfinishedClaim(text string) string {
	text = strings.TrimSpace(text)
	if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		text = text[start : end+1]
	}
	var verdict struct {
		Unfinished string `json:"unfinished"`
	}
	if json.Unmarshal([]byte(text), &verdict) != nil {
		return ""
	}
	return strings.TrimSpace(verdict.Unfinished)
}

// lastUserMessage 取最后一条用户消息：请求末尾常常是运行时追加的 system 提醒。
func lastUserMessage(history []llm.Message) string {
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role == llm.RoleUser {
			return history[index].Content
		}
	}
	return ""
}

// plainTextReviewSteps 把本轮工具调用压成一行一条：工具名、参数、成败和一小段输出。
// 复核只需要知道「做没做」，不需要完整结果。
func plainTextReviewSteps(steps []Step) string {
	var b strings.Builder
	for _, step := range steps {
		status := "成功"
		switch {
		case step.Skipped:
			status = "没有执行"
		case step.Error != "":
			status = "失败：" + step.Error
		}
		input, _ := json.Marshal(step.Input)
		output := step.Output
		if r := []rune(output); len(r) > plainTextReviewStepBudget {
			output = string(r[:plainTextReviewStepBudget]) + "…"
		}
		fmt.Fprintf(&b, "- %s %s（%s）%s\n", step.Tool, input, status, output)
		if b.Len() > plainTextReviewStepsBudget*3 {
			break
		}
	}
	if b.Len() == 0 {
		return "（本轮没有调用任何工具）"
	}
	return b.String()
}
