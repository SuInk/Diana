// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/SuInk/diana/model/agent"
	"github.com/SuInk/diana/model/llm"
)

// toolExecutionRecord 是本轮一次工具调用的回执。
type toolExecutionRecord struct {
	Tool   string            `json:"tool"`
	Status string            `json:"status"`
	Target map[string]string `json:"target,omitempty"`
}

// Keep execution state separate from the model's account of what it has done.
// This temporary request context is rebuilt from actual callbacks; it is never
// saved as user speech, a notebook fact, or another source for history search.
// It is evidence guidance, not a semantic factuality classifier.
func newReplyRunHooks() agent.RunHooks {
	var records []toolExecutionRecord
	// noted 是已经写进对话的记录条数：附注发出去就留在对话里（请求只追加），每次只补新的。
	noted := 0
	return agent.RunHooks{
		AfterToolCall: func(_ context.Context, step agent.Step) {
			status := "success"
			if step.Error != "" {
				status = "failed"
			}
			if step.Skipped {
				status = "not_executed"
			}
			target := make(map[string]string)
			// Do not copy arbitrary inputs, credentials, long documents, or output.
			for _, key := range []string{"operation", "scope", "group_id", "target_user_id", "user_id"} {
				if value, ok := step.Input[key].(string); ok && value != "" {
					target[key] = truncateChatHistoryText(value, 100)
				}
			}
			records = append(records, toolExecutionRecord{Tool: step.Tool, Status: status, Target: target})
		},
		PrepareRequest: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateRequest, error) {
			if len(records) > noted {
				var err error
				if req, err = withToolExecutionStatus(req, records[noted:], noted); err != nil {
					return req, err
				}
				noted = len(records)
			}
			return req, nil
		},
	}
}

// withToolExecutionStatus 把本轮工具回执插在最后一条用户消息之前，原生工具批次不拆开。
//
// 本轮还没执行过工具时不加。以前第一步也带一条 total=0：它插在当前消息前面，第二步
// 当前消息后面接上了工具调用，这条挪到工具结果之后，于是从第一步的这个位置起，当前
// 消息和之后的内容第二步全要重发。生产 11-16 同一轮后续步骤重新组装的部分里，从这里
// 断开的约占三分之一。
//
// 附注发出去就留在对话里、下一轮也在前缀里（请求只追加），所以只写这一步新执行的：
// before 是之前各步已经写过的条数。
func withToolExecutionStatus(req llm.GenerateRequest, records []toolExecutionRecord, before int) (llm.GenerateRequest, error) {
	// 一步调了很多工具时只列最后 12 条，其余记个数。
	start := max(0, len(records)-12)
	data, err := json.Marshal(struct {
		Total   int                   `json:"total"`
		Omitted int                   `json:"omitted,omitempty"`
		New     []toolExecutionRecord `json:"new"`
	}{before + len(records), start, records[start:]})
	if err != nil {
		return req, err
	}
	note := llm.Message{Role: llm.RoleSystem, Priority: llm.MessagePrioritySystem, Content: "【本轮工具执行状态，由运行时生成】\n" + string(data)}
	// Keep the current user input last, but never split a native tool batch.
	at := len(req.Messages)
	if at > 0 && req.Messages[at-1].Role == llm.RoleUser {
		at--
	}
	messages := make([]llm.Message, 0, len(req.Messages)+1)
	messages = append(messages, req.Messages[:at]...)
	messages = append(messages, note)
	req.Messages = append(messages, req.Messages[at:]...)
	return req, nil
}

// replyTailReminder 是主回复每一步请求最后那一条【输出前检查】（agent.Request.TailReminder）：
// 先查再答、群聊篇幅、别复述原话、找不到来由的指代先查、没查过别说查过、算数先算对。
// 只给那一步看，不进前缀，下一步、下一轮重新接一份。
//
// 11-17 起请求只追加，这几段挪进系统头部只发一次（#1580、#1582），离生成越来越远。v0.9.11
// 11-19 06:05 上线后嘉然（swe-2）开头照抄群友刚说的话、原样重发自己说过的话：同群 06:05
// 后 42 条回复有 6 条以对方原话开头，之前三天每天四五百条里只有 2～10 条。swe-2 只认
// 贴着生成的说明，同一句放系统头部几乎不起作用（见 promptReplyUnknownReferenceSpec 的
// 回放），所以挪回尾部。只给当步看不伤缓存：terra 合成请求每轮拿掉上一轮的提醒和一直
// 留着它，命中一样。
func replyTailReminder(cfg BotConfig, event MessageEvent) string {
	rules := []string{replyVerifyRule}
	if event.Kind == EventKindGroup {
		rules = append(rules, cfg.prompt(promptNoteGroupBrevitySpec))
	}
	for _, spec := range []*PromptSpec{promptReplyNoEchoSpec, promptReplyUnknownReferenceSpec, promptReplyClaimedLookupSpec, promptReplyArithmeticSpec} {
		rules = append(rules, cfg.prompt(spec))
	}
	var kept []string
	for _, rule := range rules {
		if rule = strings.TrimSpace(rule); rule != "" {
			kept = append(kept, rule)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return "【输出前检查】\n" + strings.Join(kept, "\n")
}

// promptReplyNoEchoSpec 跟在【输出前检查】里。v0.9.11 上线当天 swe-2 在群里接话，开头先把
// 对方那句原样抄一遍（「不是说能操比吗，GTA6 可能还是得……」），或者照抄图片描述里的句子、
// 把自己几分钟前的回复再发一遍。
var promptReplyNoEchoSpec = registerPrompt(PromptSpec{
	Key:     "reply.check.no_echo",
	Group:   PromptGroupReplyRules,
	Title:   "输出前检查 · 别复述原话",
	Usage:   "每步请求最后都带：回复开头别照抄对方刚说的话、聊天记录或图片描述里的原句，也别把自己发过的话再发一遍。",
	Default: "回复别以对方刚说的那句话开头，也别照抄聊天记录、图片描述里的原句；你自己发过的话不要再发一遍。直接说你的新内容。",
})

// promptReplyUnknownReferenceSpec 跟在【输出前检查】后面。群友提起好几天前聊过、已经不在
// 上下文里的事（11-15「还好没买眼镜 可惜没买杯子」指的是 11-10 的对赌单），chat_history
// 目录里写着「引用指代、上文不够时先查」，swe-2 照样 0/10 去查，顺着同一条消息里的截图
// 描述作答或按字面瞎猜。同一句话放进系统提示词 1/10、改 chat_history 描述 0/10、放进
// 当前消息 6~7/10，放在这里 9/10；十五条不需要查的群聊各 3 次，只多查了 1 次。不加
// 「不用限定时间」时模型常带 days:3，五天前的对话查不到。
var promptReplyUnknownReferenceSpec = registerPrompt(PromptSpec{
	Key:     "reply.check.unknown_reference",
	Group:   PromptGroupReplyRules,
	Title:   "输出前检查 · 找不到来由的指代",
	Usage:   "每步请求最后都带，跟在括号使用的检查后面：对方提到的事在上下文里找不到来由时，先用 chat_history 查更早的记录，别按图片或字面猜。",
	Default: "消息里提到的事、东西或说法在上面的聊天里找不到来由时（没头没尾的名词、「那个」「上次说的」），先用 chat_history 按关键词查更早的聊天记录（不用限定时间）再回，别拿图片内容或字面意思去猜。",
})

// promptReplyClaimedLookupSpec 跟在【输出前检查】后面。11-17 群友晒三只松鼠鸡翅订单，swe-2
// 一步就 send「这款我查过口味还行」，本轮和之前都没查过。头部 toolExecutionStatusRule
// 早就写了「total=0 不能声称本轮已经查询」，离生成位置太远管不住。
var promptReplyClaimedLookupSpec = registerPrompt(PromptSpec{
	Key:     "reply.check.claimed_lookup",
	Group:   PromptGroupReplyRules,
	Title:   "输出前检查 · 没查过别说查过",
	Usage:   "每步请求最后都带：回复里要说「我查过」「看了评价」这类话，先对照工具执行状态和聊天记录，没真查过就先查或直说是印象。",
	Default: "回复里要说「我查过」「看了下评价」「搜了一下」「试过」这类自己做过的事之前，对照上面的工具执行状态和聊天记录：没有真查过、真做过的记录就别这么说，要么先用工具查，要么直说是印象或听说。",
})

// promptReplyArithmeticSpec 跟在【输出前检查】后面。同一条回复把 10.88 元「约5小袋」算成
// 「一块九毛八一根」：除数不对（10.88÷5.5），单位也从袋换成了根。模型心算价格、折算常错。
var promptReplyArithmeticSpec = registerPrompt(PromptSpec{
	Key:     "reply.check.arithmetic",
	Group:   PromptGroupReplyRules,
	Title:   "输出前检查 · 算数先算再说",
	Usage:   "每步请求最后都带：回复里要报算出来的数（单价、折扣、换算、合计）时先用 codemode 算，单位和原文对上。",
	Default: "回复里要报自己算出来的数（单价、均价、折扣、换算、合计、差价）时，先用 codemode 算出来再写，别心算；分母和单位照原文（袋、根、斤、克不能混），原文没给出要换算的单位就直说算不出。",
})

// toolExecutionStatusRule 说明「本轮工具执行状态」怎么读，逐字不变，放在稳定头部；
// 每步变化的回执 JSON 留在尾部。
const toolExecutionStatusRule = "「本轮工具执行状态」由运行时生成，不是资料正文。success 仅表示对应工具返回成功，具体结论仍须核对其结果、对象、实际范围和完整性；failed/not_executed 不算查证成功。没有这一段表示本轮还没有工具执行回执，不表示已有历史事实都无效，但不能声称本轮已经查询或完成操作。旧回复中的自述不等于执行回执，也不是人物资料或统计的独立证据。"
