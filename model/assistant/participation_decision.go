// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 意图识别问的本来就是「是不是」「哪一条」「多自然」，只是过去只能让对话模型把
// 答案写成 JSON 再解析回来。下面两张表把同一套判据交给只做判断的模型（Jev 这类
// System One 模型）直接回答，答案按 Path 摆回原来的 JSON 字段，上层的阈值、冷却
// 和日志一行都不用改。对话模型看不到这两张表，走的仍是提示词里的契约。

func participationDecisionSpec() *llm.DecisionSpec {
	return &llm.DecisionSpec{Questions: []llm.DecisionQuestion{
		{
			Key:           "relevance",
			Kind:          llm.DecisionNoul,
			Label:         "在跟机器人说话",
			Instructions:  "当前消息是不是明确在跟机器人说话。\n" + participationRelevanceNote + "\n" + participationSharedNote,
			TrueCriteria:  participationRelevanceTrue,
			FalseCriteria: participationRelevanceFalse,
			Path:          "relevance.directed",
			ReasonPath:    "relevance.reason",
		},
		{
			Key:          "chat_in",
			Kind:         llm.DecisionScore,
			Label:        "闲聊适合度",
			Instructions: "没人找机器人时，机器人插一句是否自然。\n" + participationChatInNote + "\n" + participationSharedNote,
			Levels:       participationChatInLevels,
			LevelValues:  participationChatInLevelValues,
			Min:          0,
			Max:          0.9,
			Path:         "chat_in.score",
			ReasonPath:   "chat_in.reason",
		},
	}}
}

// proactiveReplyDecisionSpec 是旧的 should_reply 契约。target_message_id 是一道单选，
// turn_message_ids 是逐条候选各问一次——判断模型没有「输出一个数组」这种答案，多选
// 只能拆成多道是非题。
func proactiveReplyDecisionSpec(candidates []proactiveReplyCandidate) *llm.DecisionSpec {
	questions := []llm.DecisionQuestion{
		{
			Key:            "should_reply",
			Kind:           llm.DecisionNoul,
			Label:          "应该主动回复",
			Instructions:   "机器人现在应不应该开口回复本批消息里的某一条。默认保持沉默；明确的提问、求助、指派和追问不因为当前不知道答案而拦下。",
			TrueCriteria:   "有消息在要求或明显欢迎机器人回应，且回应能带来实质内容。",
			FalseCriteria:  "没人在找机器人，或插话只能是复读、附和和没有依据的说法。",
			Path:           "should_reply",
			ConfidencePath: "confidence",
			ReasonPath:     "reason",
		},
		{
			Key:          "category",
			Kind:         llm.DecisionChoice,
			Label:        "回复类别",
			Instructions: "这次回应属于哪一类。",
			Options: []llm.DecisionOption{
				{Value: "needs_response", Description: "公开的提问、求助、排错或请求建议，即使没有点名机器人"},
				{Value: "bot_related", Description: "明确在跟机器人说话：@、引用、叫名字，或在接机器人刚才的发言"},
				{Value: "chat_in", Description: "没人找机器人，但顺着话题插一句自然且有内容"},
				{Value: "none", Description: "不回复"},
			},
			Path:       "category",
			ReasonPath: "reason",
		},
		{
			Key:          "directed_at_bot",
			Kind:         llm.DecisionNoul,
			Label:        "在跟机器人说话",
			Instructions: "当前消息是不是明确在跟机器人说话。",
			TrueCriteria: participationRelevanceTrue,
			// 旧契约里这一项和评分契约的 relevance 问的是同一件事，判据也共用一份。
			FalseCriteria: participationRelevanceFalse,
			Path:          "directed_at_bot",
		},
		{
			Key:           "answerable",
			Kind:          llm.DecisionNoul,
			Label:         "答得上来",
			Instructions:  "后续 Agent 带着工具和完整上下文时，这条消息答不答得上来。",
			TrueCriteria:  "凭稳定知识、上下文或可用工具能给出具体可靠的回答。",
			FalseCriteria: "只能猜，或只有当事人自己知道。",
			Path:          "answerable",
		},
		{
			Key:           "substantive",
			Kind:          llm.DecisionNoul,
			Label:         "有实质内容",
			Instructions:  "机器人这次开口能不能带来实质内容。",
			TrueCriteria:  "能给出新信息、纠正、具体建议，或接住正在进行的梗。",
			FalseCriteria: "只能附和、复读、寒暄，或给没有依据的理由。",
			Path:          "substantive",
		},
		{
			Key:           "requests_response",
			Kind:          llm.DecisionNoul,
			Label:         "对方在要回应",
			Instructions:  "发言者这句话本身是不是在要求得到回应。这只描述对方的诉求，和最终回不回是两件事。",
			TrueCriteria:  "在提问、求助、指派任务或继续追问。",
			FalseCriteria: "只是陈述、感慨、通知或结束语。",
			Path:          "requests_response",
		},
		{
			Key:          "blocker",
			Kind:         llm.DecisionChoice,
			Label:        "拦下来的原因",
			Instructions: "如果这次不回复，是被什么拦下的；会回复时选 none。",
			Options: []llm.DecisionOption{
				{Value: proactiveBlockerNone, Description: "没有阻碍，或这次会回复"},
				{Value: proactiveBlockerMissingInfo, Description: "路由阶段上下文不足，正式回复阶段可能补得上"},
				{Value: proactiveBlockerNoCapability, Description: "路由阶段看不到所需能力，正式回复阶段可能有工具"},
				{Value: proactiveBlockerNotAddressed, Description: "没人在找机器人"},
				{Value: proactiveBlockerLowValue, Description: "开口也只能是没有增量的捧场"},
			},
			Path: "blocker",
		},
	}
	if target := proactiveReplyTargetQuestion(candidates); target != nil {
		questions = append(questions, *target)
	}
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.Event.MessageID)
		if id == "" {
			continue
		}
		questions = append(questions, llm.DecisionQuestion{
			Key:           "turn_" + id,
			Kind:          llm.DecisionNoul,
			Label:         "同一轮消息 " + id,
			Instructions:  "消息 " + id + " 是不是和回复目标同属一轮、需要一起回答。",
			TrueCriteria:  "同一发送者连续补充、拆成几条的同一个问题，或直接构成目标消息的前提。",
			FalseCriteria: "旁支闲聊、别人的话题，或与目标无关。",
			Path:          "turn_message_ids",
			AppendValue:   id,
		})
	}
	return &llm.DecisionSpec{Questions: questions}
}

func proactiveReplyTargetQuestion(candidates []proactiveReplyCandidate) *llm.DecisionQuestion {
	options := make([]llm.DecisionOption, 0, len(candidates)+1)
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.Event.MessageID)
		if id == "" {
			continue
		}
		text := strings.TrimSpace(readableEventText(candidate.Event, candidate.Text))
		options = append(options, llm.DecisionOption{Value: id, Description: truncateRunesFromStart(text, 180)})
	}
	if len(options) == 0 {
		return nil
	}
	options = append(options, llm.DecisionOption{Value: proactiveReplyNoTarget, Description: "不回复，不选目标"})
	return &llm.DecisionQuestion{
		Key:          "target_message_id",
		Kind:         llm.DecisionChoice,
		Label:        "回复目标",
		Instructions: "选一条最值得回复的目标消息；不回复时选 none。",
		Options:      options,
		Path:         "target_message_id",
		EmptyOption:  proactiveReplyNoTarget,
	}
}

const proactiveReplyNoTarget = "none"
