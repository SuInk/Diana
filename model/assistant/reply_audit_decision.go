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
		Instructions: "给候选回复的准确性打分：越高表示越该发出去。简短、只答要点、不展开举例都不是缺陷；口吻、篇幅和有没有新增信息不算准确性问题。拿不准倾向给高分——缺少上下文不是错误证据。",
		Levels: []string{
			"明确矛盾、答非所问或被截断",
			"措辞偏绝对或缺少限定，但没有明确错误",
			"没有发现问题",
		},
		LevelValues: []float64{0.2, 0.75, 0.96},
		Min:         0,
		Max:         1,
		Decimals:    2,
		Path:        "send_confidence",
	}}

	if need.Quality {
		questions = append(questions, llm.DecisionQuestion{
			Key:          "accuracy_issue",
			Kind:         llm.DecisionChoice,
			Label:        "准确性问题的类别",
			Instructions: "把准确性结论归到一个类别。运行时只按类别决定拦不拦，wording 不拦截，不要把它升级成错误。",
			Options: []llm.DecisionOption{
				{Value: "none", Description: "没发现问题"},
				{Value: "wording", Description: "措辞偏绝对、不够严谨或缺少限定条件，但没有明确矛盾、答非所问或被截断"},
				{Value: "contradiction", Description: "明确矛盾"},
				{Value: "off_topic", Description: "答非所问"},
				{Value: "truncated", Description: "被截断"},
				{Value: "harmful_advice", Description: "给出照做就可能伤身或造成财产损失的具体指令；只是讨论健康或金融话题归 wording"},
			},
			Path:       "accuracy_issue",
			ReasonPath: "reason",
		})
	}

	if need.AccountSafety {
		questions = append(questions,
			llm.DecisionQuestion{
				Key:          "account_safe",
				Kind:         llm.DecisionScore,
				Label:        "这条回复不会让账号被平台处置的把握",
				Instructions: "只看候选回复自己说了什么，别人说了什么不算。这一项和准确性互相独立，不要把准确性结论混进来。0.10 是放行线，一般性的不确定不该让机器人闭嘴。",
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
				Key:           "reply_loop_automated_ai",
				Kind:          llm.DecisionNoul,
				Label:         "对方是不是另一个自动应答的机器人",
				Instructions:  "只在高置信时判是。",
				TrueCriteria:  "文本像助手在对上一条逐项回应，带模板化确认、规则复述或待命表述，且同一发送者近期多次保持相似的助手人格和应答结构",
				FalseCriteria: "真人的简短问答、吐槽、争论、玩梗、角色扮演、口癖、表情或正常连续聊天。引用、@、点名或回复很快都不是证据，文字通顺或很长也不是",
				Path:          "reply_loop_automated_ai",
			},
			llm.DecisionQuestion{
				Key:            "reply_loop_self_repeat",
				Kind:           llm.DecisionNoul,
				Label:          "机器人自己是不是在复读",
				Instructions:   "看候选回复和机器人近期几条回复是不是在重复同一个意思。判的是意思不是字，但要明显在原地打转才算，拿不准一律判否。",
				TrueCriteria:   "候选和前面某几条是同一个意思、同一个动作，只换了措辞，每条都没有推进：反复道别、反复催睡、反复答应同一件事、反复说同一个结论",
				FalseCriteria:  "给出了前面没有的信息、步骤、原因、数字或新的提议，哪怕用词高度重合；连续回答同一个问题、补充细节；对方追问或坚持同一个请求时回应这次追问；同类的话只说过一两次",
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

	return &llm.DecisionSpec{Questions: questions}
}
