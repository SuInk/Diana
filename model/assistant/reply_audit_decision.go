// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "github.com/SuInk/diana/model/llm"

// replyAuditDecisionSpec 把发送前审核那张自由文本 JSON 表摆成逐题的判断表。
//
// 绑的是只做判断的模型（TypeSafe Jev）时，它照这张表作答，答案由
// RenderDecisionAnswers 回填成 parseProactiveReplyQualityDecision 本来就在解析的
// 那个 JSON；绑对话模型时这张表用不上，提示词照旧。判据只写一份——提示词里那套
// 说明是给写字的模型看的，这里的 criteria 是同一套规则的逐题版本。
//
// 只摆这一轮真要问的题：没有空转证据就不问空转，不是私聊收尾就不问收尾。判断模型
// 按题计费也按题思考，多问一题就是多一份钱和一次可能答错的机会。
func replyAuditDecisionSpec(need replyAuditNeed) *llm.DecisionSpec {
	questions := []llm.DecisionQuestion{{
		// send_confidence 是解析器唯一必须拿到的字段，任何一轮都要问。
		Key:          "send_confidence",
		Kind:         llm.DecisionScore,
		Label:        "这条回复可以发出去的把握",
		Instructions: "旧版本兼容字段，固定给出发送置信度；该字段不再参与发送决定。",
		Levels: []string{
			"旧版本兼容字段",
			"旧版本兼容字段",
			"正常发送",
		},
		LevelValues: []float64{0.2, 0.75, 0.96},
		Min:         0,
		Max:         1,
		Decimals:    2,
		Path:        "send_confidence",
	}}

	if need.AccountSafety {
		questions = append(questions,
			llm.DecisionQuestion{
				Key:          "account_safe",
				Kind:         llm.DecisionScore,
				Label:        "这条回复不会让账号被平台处置的把握",
				Instructions: "只看候选回复自己说了什么，别人说了什么不算。这一项独立判断，不要把其他结论混进来。0.10 是放行线，一般性的不确定不该让机器人闭嘴。",
				Levels: []string{
					"明确命中中国大陆涉政、露骨性内容或违法内容",
					"擦边但没有明确命中",
					"与这几类都无关",
				},
				LevelValues: []float64{0.05, 0.5, 0.98},
				Min:         0,
				Max:         1,
				Decimals:    2,
				Path:        "account_safe",
			},
			llm.DecisionQuestion{
				Key:          "account_risk",
				Kind:         llm.DecisionChoice,
				Label:        "命中的账号风险类别",
				Instructions: "只有上一题判为不安全时才选具体类别，否则选 none。",
				Options: []llm.DecisionOption{
					{Value: "none", Description: "没有命中"},
					{Value: "politics", Description: "中国大陆涉政：对大陆现实政治人物、党政机构、敏感时政争议的评价、立场或影射"},
					{Value: "explicit", Description: "露骨性内容"},
					{Value: "illegal", Description: "违法内容"},
				},
				EmptyOption: "none",
				Path:        "account_risk",
				ReasonPath:  "account_risk_reason",
			},
			llm.DecisionQuestion{
				Key:            "count_refusal",
				Kind:           llm.DecisionNoul,
				Label:          "候选回复是不是明确拒绝了当前请求",
				Instructions:   "只看这条回复有没有明确拒绝当前请求。",
				TrueCriteria:   "明确表示不做、不说、不能回答",
				FalseCriteria:  "正常回答、部分回答、要求澄清、说明能力或权限限制、工具故障，或只是想结束话题",
				Path:           "count_refusal",
				ConfidencePath: "refusal_confidence",
				ReasonPath:     "refusal_reason",
			},
		)
	}

	if need.Loop {
		questions = append(questions,
			llm.DecisionQuestion{
				Key:            "reply_loop_meaningless",
				Kind:           llm.DecisionNoul,
				Label:          "这一来一回是不是已经空转",
				Instructions:   "判断这一来一回是不是在为没有内容的消息反复接茬。",
				TrueCriteria:   "当前消息没有实质内容（纯附和、纯复读、只有称呼或标点），候选回复也只是接了句同样没内容的话，且近期消息显示这个模式已重复好几轮",
				FalseCriteria:  "对方在问问题、给信息、表达情绪、玩梗，或者只重复了一两次。真人闲聊本来就允许没有信息量，拿不准一律判否",
				Path:           "reply_loop_meaningless",
				ConfidencePath: "reply_loop_confidence",
				ReasonPath:     "reply_loop_reason",
			},
			llm.DecisionQuestion{
				Key:            "reply_loop_self_repeat",
				Kind:           llm.DecisionNoul,
				Label:          "机器人自己是不是在复读",
				Instructions:   "结合当前请求及引用，比较候选整体与机器人近期答复。部分重复交给语义去重编辑器，不能因此丢掉整条回答。判的是意思不是字，但要明显在原地打转才算，拿不准一律判否。" + replyAfterAnswerRule,
				TrueCriteria:   "候选整体只是换措辞重复近期答复，没有任何新增回答、纠正或必要澄清：反复道别、反复催睡、反复答应同一件事、反复说同一个结论",
				FalseCriteria:  "给出了前面没有的信息、步骤、原因、数字或新的提议，哪怕用词高度重合；连续回答同一个问题、补充细节；实际回应上一条答案的追问、反驳或纠正，给出新解释或必要更正；保留用户要求重述或重新解释所需的内容；仅其中一句玩笑或结尾重复；同类的话只说过一两次。只重复上一条无效玩笑或空话不算重答。拿不准判否",
				Path:           "reply_loop_self_repeat",
				ConfidencePath: "reply_loop_self_repeat_confidence",
				ReasonPath:     "reply_loop_reason",
			},
		)
		if need.Density != nil {
			questions = append(questions, llm.DecisionQuestion{
				Key:            "reply_loop_purposeless",
				Kind:           llm.DecisionNoul,
				Label:          "这串来回有没有明确目的",
				Instructions:   "判断这串密集来回是不是漫无目的。",
				TrueCriteria:   "漫无目的地接戏、斗嘴、复读",
				FalseCriteria:  "下棋、解题、一起做事这类有明确目的的来回",
				Path:           "reply_loop_purposeless",
				ConfidencePath: "reply_loop_purposeless_confidence",
				ReasonPath:     "reply_loop_reason",
			})
		}
	}

	if need.Closing || need.GroupStop {
		questions = append(questions,
			llm.DecisionQuestion{
				Key:            "conversation_closing",
				Kind:           llm.DecisionNoul,
				Label:          "对话是不是已经收尾",
				Instructions:   "判断这段对话是不是已经互相道别、自然结束。",
				TrueCriteria:   "双方已经互相道过别，或者话题明确结束了",
				FalseCriteria:  "对方还在问问题、还在等回答，或者话题仍在继续",
				Path:           "conversation_closing",
				ConfidencePath: "closing_confidence",
				ReasonPath:     "closing_reason",
			},
			llm.DecisionQuestion{
				Key:           "stop_requested",
				Kind:          llm.DecisionNoul,
				Label:         "对方是不是明确要求别再回了",
				Instructions:  "只看对方有没有明说不要再回复。",
				TrueCriteria:  "明确说了别回、不用回、别再说话",
				FalseCriteria: "只是话说完了、没有继续提问，或者语气冷淡",
				Path:          "stop_requested",
			},
		)
	}

	if need.Fatigue {
		questions = append(questions,
			llm.DecisionQuestion{
				Key:          "exchange_novelty",
				Kind:         llm.DecisionScore,
				Label:        "这一轮带来了多少新东西",
				Instructions: "只看当前这一轮（对方这句 + 候选回复）相对前几轮有没有新信息、新问题、新进展或新话题。换个说法重复、接同一个梗、反复自嘲互夸寒暄都算低。",
				Levels:       []string{"没有新东西，在重复或接同一个梗", "有一点新内容", "明显带来了新信息或新话题"},
				LevelValues:  []float64{0.1, 0.5, 0.9},
				Min:          0,
				Max:          1,
				Decimals:     2,
				Path:         "exchange_novelty",
			},
			llm.DecisionQuestion{
				Key:          "exchange_purpose",
				Kind:         llm.DecisionScore,
				Label:        "这串来回是不是在推进一件具体的事",
				Instructions: "提问求答、解题、查资料、做事、下棋这类在推进的给高分；纯闲聊接梗、斗嘴、续剧情给低分。对方这句在明确提问或提出请求时给高分。",
				Levels:       []string{"纯闲聊、接梗、斗嘴", "有点事但不明确", "在明确提问、请求或推进一件事"},
				LevelValues:  []float64{0.1, 0.5, 0.9},
				Min:          0,
				Max:          1,
				Decimals:     2,
				Path:         "exchange_purpose",
			},
		)
	}

	return &llm.DecisionSpec{Questions: questions}
}
