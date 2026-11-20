// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 收尾前的完成度复核：回复里说在做、做完了或马上会自动完成的事，本轮工具记录里
// 有没有对应的调用（Config.UnfinishedClaimReview）。
//
// 11-08 起它只管「退回一次后仍写纯文本」：原先第二次纯文本直接静默，挡的是「在画了，
// 画好自动发出来」这种一个工具都没调的空口承诺，可生产一天 9 轮这样静默全是写好的正常
// 回复，所以改成先复核、说了没做才静默。
//
// 11-20 起中途发言（send 填 continue、和工具同一步的 send）整个去掉，一轮只在做完以后
// 发一次：11-18～20 线上 1239 步只调 send 填了 continue，下一步真去调工具的只有 100 步，
// 其余是把整条回复当中途发言发出去，再续一步说一遍或改成不说话，群里看着一会儿一句、
// 多出来的步骤约占回复输入 token 的三成。可光去掉 continue，「我查一下」就成了最终回复
// （10-27 线上出过），所以复核从纯文本扩到每次要发出去的收尾：查出来先退回去，让模型
// 要么现在调用工具去做、要么照实说；退回后还有就这一轮不说话。复核只看本轮工具记录
// 和对方这条消息，不判断回复好不好、该不该回。一轮只发一次以后，「先说下天气」这种
// 说了还有下文的收尾也是空口承诺（11-20 重庆攻略只发出天气一段），一起查。复核调用失败或输出看不懂时放行：发出去
// 之前还有发送前审核，把整条回复吞掉不该是复核出错的代价。
const (
	plainTextReviewStepBudget  = 400
	plainTextReviewStepsBudget = 6000
	// 近期对话取最后这么多条非 system 消息，每条压到 claimReviewMessageBudget 字：复核只需要
	// 知道前几轮发起过什么任务、说过什么，不需要完整历史。
	claimReviewRecentMessages = 16
	claimReviewMessageBudget  = 300
)

const plainTextReviewPrompt = `你在检查一条聊天机器人回复草稿有没有「说了却没做」。输入是近期对话（含机器人前几轮的发言和工具调用）、对方这条消息、机器人本轮实际调用过的工具记录，以及草稿。
找出草稿里声称机器人自己正在做、已经做完或马上会自动完成的操作（画图、搜索查询、设置提醒、修改配置、发送文件、读取记录等），而本轮工具记录和近期对话里都没有对应调用的那一项。
草稿是机器人这一轮唯一、也是最后一条消息，发完就不会再说。所以草稿说还有下文、却没写出来的也算（比如「先说下天气」后面只有天气，「景点和住宿下一条发」「剩下的稍后补上」），输出那句话。
不算问题的：普通聊天、接话、表情回应；看了、听了、读了对方发来的图片、表情、语音或文字（机器人直接就能看到，不需要工具）；凭常识或上下文直接回答、总结、解释；向对方提问或请对方补充；拒绝或说明做不到；建议对方自己去做；本轮工具记录里能找到对应调用的操作；近期对话里已经发起、还在后台进行的任务（比如之前排队的图片「还在画」「好了会自动发出来」）。
只输出 JSON：{"unfinished":"草稿原话里没做到的那一项"}，没有就输出 {"unfinished":""}。`

// unfinishedClaimReason 是退回后收尾仍说了却没做、这一轮不说话时记下的原因前缀。
const unfinishedClaimReason = "回复说了却没做，退回一次后仍是这样，没有发出："

// unfinishedClaimRepair 是复核查出说了却没做时退回给模型的话。
func unfinishedClaimRepair(unfinished string) string {
	return "这条没有发出：「" + unfinished + "」本轮并没有做。要做就现在调用对应工具（按需工具用 tools_execute），做完再 send；说了还有下文的，把下文写进这一条一起 send；做不到或不打算做就照实说，不要说正在做、做好了或会做。"
}

// reviewUnfinishedClaim 返回草稿里说了却没做的操作；没有、或复核失败时返回空串。
func (r *Runner) reviewUnfinishedClaim(ctx context.Context, history []llm.Message, draft string, steps []Step) string {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return ""
	}
	resp, err := r.client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: plainTextReviewPrompt},
		{Role: llm.RoleUser, Content: "【近期对话】\n" + claimReviewRecent(history) + "\n\n【对方的消息】\n" + lastUserMessage(history) + "\n\n【本轮工具记录】\n" + plainTextReviewSteps(steps) + "\n\n【草稿】\n" + draft},
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

// claimReviewRecent 把历史末尾几条非 system 消息压成一行一条：前几轮机器人发起的任务
// （图片排队、提醒）要在这里看得到，不然「还在画，好了会自动发出来」会被当成没做。
func claimReviewRecent(history []llm.Message) string {
	var lines []string
	for index := len(history) - 1; index >= 0 && len(lines) < claimReviewRecentMessages; index-- {
		message := history[index]
		if message.Role == llm.RoleSystem {
			continue
		}
		var parts []string
		if text := strings.TrimSpace(message.Content); text != "" {
			parts = append(parts, text)
		}
		for _, part := range message.Parts {
			if part.Type == llm.ContentPartText && strings.TrimSpace(part.Text) != "" {
				parts = append(parts, strings.TrimSpace(part.Text))
			}
		}
		for _, call := range message.ToolCalls {
			input, _ := json.Marshal(call.Arguments)
			parts = append(parts, "调用 "+call.Name+" "+string(input))
		}
		if len(parts) == 0 {
			continue
		}
		line := strings.Join(parts, " ")
		if runes := []rune(line); len(runes) > claimReviewMessageBudget {
			line = string(runes[:claimReviewMessageBudget]) + "…"
		}
		lines = append(lines, "- "+string(message.Role)+"："+line)
	}
	if len(lines) == 0 {
		return "（没有）"
	}
	slices.Reverse(lines)
	return strings.Join(lines, "\n")
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
