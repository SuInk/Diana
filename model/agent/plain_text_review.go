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

// 「不记得、别问我」也算推托：11-21 Telegram 上问 OpenCode 订阅给多少，回复没调工具就说
// 「不记得具体数字了……别问我」，复核当成拒绝放过了。swe-2 回放这条，只写「没有能力」
// 时打回 1/5，加上「没查就答不记得 / 不知道」后次次打回；线上另抽 20 条复核草稿重跑，
// 本轮查过、照实说没查到的要靠后半句才不被误打回，其余打回次数和改前相当。
//
// 漏答也一起查（missing）：11-21 群里问「幸福者退让的来源和含义」，搜索结果里写着含义，
// 回复只答了来源，复核只看「说了没做」放过了。terra 重放这条复核 6/6 判出漏答「含义」；
// 线上另抽 40 条复核草稿，没有一条被判漏答。
const plainTextReviewPrompt = `你在检查一条聊天机器人回复草稿有没有「说了却没做」，以及有没有漏答对方问到的事。输入是近期对话（含机器人前几轮的发言和工具调用）、对方这条消息、机器人本轮实际调用过的工具记录、机器人这一轮能用的工具，以及草稿。
找出草稿里声称机器人自己正在做、已经做完或马上会自动完成的操作（画图、搜索查询、跑代码或算出结果、设置提醒、修改配置、发送文件、读取记录等），而本轮工具记录和近期对话里都没有对应调用的那一项。
草稿说自己没有某个能力、用不了某样东西（比如「没沙盒跑不了」「查不了」「没法发文件」），或者对方问的事实没查就答「不记得」「不知道」「没记准」，而能用的工具里明明有能做这件事、查得到的，也算，输出那句话；本轮已经查过、照实说没查到的不算。
草稿是机器人这一轮唯一、也是最后一条消息，发完就不会再说。所以草稿说还有下文、却没写出来的也算（比如「先说下天气」后面只有天气，「景点和住宿下一条发」「剩下的稍后补上」），输出那句话。
对方这条消息明确问了几件事（比如「来源和含义」「多少钱、在哪买」「A 和 B 有什么区别」），草稿只答了其中一部分、漏掉的那件既没回答也没说为什么不答，把漏掉的那一问写进 missing（比如「含义」）。只看对方这条消息里明确问到的；闲聊、接话、顺口一提的不算，一句话把几问都答到了不算，答得简短也不算漏。
不算问题的：普通聊天、接话、表情回应；看了、听了、读了对方发来的图片、表情、语音或文字（机器人直接就能看到，不需要工具）；凭常识或上下文直接回答、总结、解释；向对方提问或请对方补充；能用的工具里确实没有、如实说做不到；拒绝；建议对方自己去做；本轮工具记录里能找到对应调用的操作；近期对话里已经发起、还在后台进行的任务（比如之前排队的图片「还在画」「好了会自动发出来」）。
只输出 JSON：{"unfinished":"草稿原话里没做到的那一项","missing":"对方问了、草稿漏答的那一问"}，没有的字段填空串。`

// unfinishedClaimReason 是退回后收尾仍说了却没做、这一轮不说话时记下的原因前缀。
const unfinishedClaimReason = "回复说了却没做，退回一次后仍是这样，没有发出："

// missingAnswerRepair 是复核查出漏答对方问到的事时退回给模型的话。漏答只退回一次，
// 第二次照发：答了一半比不说话好。
func missingAnswerRepair(missing string) string {
	return "这条没有发出：对方还问了「" + missing + "」，草稿没有回答。把它补进这一条一起 send，已经答了的不用改；确实答不了就照实说一句。"
}

// claimVerdict 是收尾复核的结论：Unfinished 是说了却没做的那句，Missing 是漏答的那一问。
type claimVerdict struct {
	Unfinished string `json:"unfinished"`
	Missing    string `json:"missing"`
}

// unfinishedClaimRepair 是复核查出说了却没做时退回给模型的话。
func unfinishedClaimRepair(unfinished string) string {
	return "这条没有发出：「" + unfinished + "」本轮并没有做。要做就现在调用对应工具（按需工具用 tools_execute），做完再 send；说了还有下文的，把下文写进这一条一起 send；做不到或不打算做就照实说，不要说正在做、做好了或会做。"
}

// reviewUnfinishedClaim 返回草稿里说了却没做的操作和漏答的问题；没有、或复核失败时为空。
func (r *Runner) reviewUnfinishedClaim(ctx context.Context, history []llm.Message, draft string, steps []Step) claimVerdict {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return claimVerdict{}
	}
	resp, err := r.client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: plainTextReviewPrompt},
		{Role: llm.RoleUser, Content: "【近期对话】\n" + claimReviewRecent(history) + "\n\n【对方的消息】\n" + lastUserMessage(history) + "\n\n【本轮工具记录】\n" + plainTextReviewSteps(steps) + "\n\n【能用的工具】\n" + claimReviewTools(r.registry) + "\n\n【草稿】\n" + draft},
	}})
	if err != nil || resp == nil {
		return claimVerdict{}
	}
	return parseUnfinishedClaim(resp.Text)
}

// claimReviewTools 列出这一轮能用的工具，每个带描述的第一句：复核要靠它分辨「没沙盒
// 跑不了」是实话还是推托。11-20 线上 run_command 一直可用，回复却说没沙盒，还把
// 别人消息里的数字说成自己算的，复核看不到工具清单，当成「说明做不到」放过了。
func claimReviewTools(registry *ToolRegistry) string {
	var lines []string
	for _, name := range registry.Names() {
		tool, ok := registry.Get(name)
		if !ok || name == finalizeToolName {
			continue
		}
		lines = append(lines, "- "+name+"："+firstClause(tool.Description()))
	}
	if len(lines) == 0 {
		return "（无）"
	}
	return strings.Join(lines, "\n")
}

// firstClause 取描述到第一个句号为止，最多 60 字。
func firstClause(description string) string {
	description = strings.TrimSpace(description)
	if index := strings.IndexAny(description, "。.\n"); index > 0 {
		description = description[:index]
	}
	if runes := []rune(description); len(runes) > 60 {
		description = string(runes[:60])
	}
	return description
}

func parseUnfinishedClaim(text string) claimVerdict {
	text = strings.TrimSpace(text)
	if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		text = text[start : end+1]
	}
	var verdict claimVerdict
	if json.Unmarshal([]byte(text), &verdict) != nil {
		return claimVerdict{}
	}
	return claimVerdict{Unfinished: strings.TrimSpace(verdict.Unfinished), Missing: strings.TrimSpace(verdict.Missing)}
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
