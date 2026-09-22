// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"sort"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// 每一次 LLM 调用都属于某个用途。用途以前只用来记账（withLLMUsagePurpose），选模型
// 时看不到——模型来自 provider 上的「默认模型」，于是 17 个用途共用同一个隐含选择。
//
// provider 配置只该回答「我是谁、我能提供哪些模型」，用哪个模型是调用方的事。所以
// 用途在这里升级成一等概念：每个用途都能单独绑定，绑不到就用它所属分组的绑定。
const (
	PurposeMediaParse            = "media_parse"
	PurposeReply                 = "reply"
	PurposeSubagent              = "subagent"
	PurposeSubtask               = "subtask"
	PurposeReplyIntentRouter     = "reply_intent_router"
	PurposeReplyRuleRouter       = "reply_rule_router"
	PurposeProactiveReplyRouter  = "proactive_reply_router"
	PurposeProactiveReplyQuality = "proactive_reply_quality"
	PurposeSemanticReference     = "semantic_reference"
	PurposeInboundMediaReference = "inbound_media_reference"
	PurposeContextSummary        = "context_summary_compaction"
	PurposeMemoryExtract         = "memory_extract"
	PurposeMemorySummary         = "memory_summary"
	PurposeRelationshipEvaluate  = "relationship_evaluate"
	PurposeForwardContentSafety  = "forward_content_safety"
	PurposeReplyAccountSafety    = "reply_account_safety"
	// PurposeReplySendAudit 是实际发出这次审核调用时用的用途名。它以前只是
	// proactive_reply_quality.go 里的一个字面量，没进这张表，于是界面上指不了、
	// 也没法单独绑——而它是量最大的判定之一。
	PurposeReplySendAudit   = "reply_send_audit"
	PurposeReplySuppression = "reply_suppression_notice"
	PurposeBotReplyLoop     = "bot_reply_loop_detection"
	// 三种发送前提示的改写。它们以前只是散在代码里的字面量，没进这张表，
	// 于是在模型绑定界面上看不见也指不了，只能跟着调用函数走。
	PurposeUpstreamRejectionNotice = "upstream_rejection_notice"
	PurposeAccountSafetyNotice     = "account_safety_notice"
	PurposeErrorNotice             = "error_notice"
)

// llmPurposeGroup 把用途归到分组。这张表以前是隐式的——某个用途走哪个分组，取决于
// 它调用的是 runLLMProvider 还是 runLLMRouterProvider，从配置界面上完全看不出来。
// 摊开写成表，用途才谈得上「可覆盖」：没单独绑就落到所属分组的绑定。
var llmPurposeGroup = map[string]string{
	PurposeMediaParse: llm.GroupVision,
	PurposeReply:      llm.GroupChat,
	PurposeSubagent:   llm.GroupChat,
	PurposeSubtask:    llm.GroupChat,

	// 路由、判定这类调用短、频次高，值得单独指一个便宜快的模型。
	PurposeReplyIntentRouter:     llm.GroupIntent,
	PurposeReplyRuleRouter:       llm.GroupIntent,
	PurposeProactiveReplyRouter:  llm.GroupIntent,
	PurposeProactiveReplyQuality: llm.GroupIntent,
	PurposeSemanticReference:     llm.GroupIntent,
	PurposeInboundMediaReference: llm.GroupIntent,
	PurposeContextSummary:        llm.GroupIntent,
	PurposeMemoryExtract:         llm.GroupIntent,
	PurposeMemorySummary:         llm.GroupIntent,
	PurposeRelationshipEvaluate:  llm.GroupIntent,
	PurposeForwardContentSafety:  llm.GroupIntent,
	PurposeReplyAccountSafety:    llm.GroupIntent,
	PurposeReplySendAudit:        llm.GroupIntent,
	PurposeReplySuppression:      llm.GroupIntent,
	PurposeBotReplyLoop:          llm.GroupIntent,

	// 改写只是把一句固定文案换个说法，和审核、路由同档，用便宜快的那个就够。
	PurposeUpstreamRejectionNotice: llm.GroupIntent,
	PurposeAccountSafetyNotice:     llm.GroupIntent,
	PurposeErrorNotice:             llm.GroupIntent,
}

// 判定类用途分成两拨：备了判断题表的可以绑只做判断的模型（TypeSafe Jev 这类），
// 要写字的不能。这两个键摆在用途和分组之间——按用途逐个配太细（十几行），按分组
// 配又太粗（一绑就把要写字的那几个一起绑坏），能用和不能用才是这里真正的分界线。
const (
	RoleDecisionJudges = "decision_judges"
	RoleTextJudges     = "text_judges"
)

// llmPurposeClass 只收 intent 分组底下的用途：别的分组没有这个分界。
// 判断模型答不了的那几个必须留在 text_judges——它们要的是成段文字，不是选项。
var llmPurposeClass = map[string]string{
	// 这一拨在发请求时真的带上了判断题表，绑判断模型不会先失败一次。
	PurposeProactiveReplyRouter:  RoleDecisionJudges,
	PurposeProactiveReplyQuality: RoleDecisionJudges,
	PurposeReplySendAudit:        RoleDecisionJudges,

	// 意图路由、规则路由、防循环问的也都是是非题，但眼下还没有各自的判断题表：
	// 归进判断类只会让它们每次先失败一次再降级。等题表补上再挪过来。
	PurposeReplyIntentRouter: RoleTextJudges,
	PurposeReplyRuleRouter:   RoleTextJudges,
	PurposeBotReplyLoop:      RoleTextJudges,

	PurposeSemanticReference:       RoleTextJudges,
	PurposeInboundMediaReference:   RoleTextJudges,
	PurposeContextSummary:          RoleTextJudges,
	PurposeMemoryExtract:           RoleTextJudges,
	PurposeMemorySummary:           RoleTextJudges,
	PurposeRelationshipEvaluate:    RoleTextJudges,
	PurposeForwardContentSafety:    RoleTextJudges,
	PurposeReplyAccountSafety:      RoleTextJudges,
	PurposeReplySuppression:        RoleTextJudges,
	PurposeUpstreamRejectionNotice: RoleTextJudges,
	PurposeAccountSafetyNotice:     RoleTextJudges,
	PurposeErrorNotice:             RoleTextJudges,
}

// ModelBindingClassOf 返回用途所属的那一拨，供界面回答「不单独配的话跟着谁」。
func ModelBindingClassOf(purpose string) string {
	return llmPurposeClass[strings.TrimSpace(purpose)]
}

// modelBindingGroups 是必须绑定的分组。它们就是「用途的归属地」，缺一个就有一批
// 用途没有模型可用。
var modelBindingGroups = []string{
	llm.GroupChat, llm.GroupVision, llm.GroupIntent, llm.GroupImage, llm.GroupEmbedding,
}

// modelRoleKeyForGroup 返回分组在 model_roles 里用的键。默认分组的键历史上是
// "chat" 而不是 "default"，存量配置都按这个写，不能改。
func modelRoleKeyForGroup(group string) string {
	if key := llm.NormalizeProfileGroup(group); key != llm.GroupChat {
		return key
	}
	return "chat"
}

// ModelBindingKeys 返回所有可绑定的键：先是分组，再是用途。前端按这个顺序渲染。
func ModelBindingKeys() []string {
	keys := make([]string, 0, len(modelBindingGroups)+len(llmPurposeGroup)+2)
	for _, group := range modelBindingGroups {
		keys = append(keys, modelRoleKeyForGroup(group))
	}
	keys = append(keys, RoleDecisionJudges, RoleTextJudges)
	purposes := make([]string, 0, len(llmPurposeGroup))
	for purpose := range llmPurposeGroup {
		purposes = append(purposes, purpose)
	}
	sort.Strings(purposes)
	return append(keys, purposes...)
}

// ModelBindingGroupOf 返回某个用途归属的分组键，供前端说明「不配就跟着谁」。
func ModelBindingGroupOf(purpose string) string {
	group, ok := llmPurposeGroup[strings.TrimSpace(purpose)]
	if !ok {
		return ""
	}
	return modelRoleKeyForGroup(group)
}

func isModelBindingKey(key string) bool {
	if _, ok := llmPurposeGroup[key]; ok {
		return true
	}
	if key == RoleDecisionJudges || key == RoleTextJudges {
		return true
	}
	for _, group := range modelBindingGroups {
		if modelRoleKeyForGroup(group) == key {
			return true
		}
	}
	return false
}

// modelRoleFor 按「用途 → 本次调用的分组 → 用途归属的分组 → chat」的顺序找绑定。
//
// 用途排在最前：单独给某个用途指了模型，就该盖过一切。
//
// 但**本次调用的分组要排在用途归属的分组之前**——调用点比这张静态表知道得多。
// 最典型的是 reply：它平时走对话分组，这一轮带图时调用点会切到 vision。要是让
// llmPurposeGroup 里写死的 chat 盖过去，配好的视觉模型就永远用不上（这条是被
// TestRestoredModelRoleProfileBindingsAndVisionFallback 抓出来的）。
//
// 于是 llmPurposeGroup 只在调用分组没有绑定时才兜一下，主要作用是给界面回答
// 「这个用途不单独配的话跟着谁」。
func modelRoleFor(roles map[string]ModelRole, purpose string, group string) (ModelRole, bool) {
	if len(roles) == 0 {
		return ModelRole{}, false
	}
	// A dedicated media parser wins even when general vision follows chat.
	if isMediaParsePurpose(purpose) {
		if role, ok := roles[PurposeMediaParse]; ok {
			return resolveIfFollowChat(roles, role)
		}
	}
	groupKey := modelRoleKeyForGroup(group)
	// 本次调用的分组声明了「跟随对话」时直接落到对话绑定，不再往下看用途：用途上
	// 绑的多半是纯文本模型，套到这一轮（典型是带图的 vision 调用）上跑不通。
	if groupKey != "chat" && roles[groupKey].FollowChat {
		return resolveFollowChatRole(roles)
	}
	if purpose = strings.TrimSpace(purpose); purpose != "" {
		if role, ok := roles[purpose]; ok {
			return resolveIfFollowChat(roles, role)
		}
	}
	// 类排在分组之前：绑了「判断类」就该盖过笼统的 intent 一档，否则这两个键
	// 形同虚设。
	if purpose != "" {
		if class := ModelBindingClassOf(purpose); class != "" {
			if role, ok := roles[class]; ok {
				return resolveIfFollowChat(roles, role)
			}
		}
	}
	if role, ok := roles[groupKey]; ok {
		return resolveIfFollowChat(roles, role)
	}
	if purpose != "" {
		if owner := ModelBindingGroupOf(purpose); owner != "" {
			if role, ok := roles[owner]; ok {
				return resolveIfFollowChat(roles, role)
			}
		}
	}
	role, ok := roles["chat"]
	return role, ok
}

// resolveIfFollowChat 把「跟随对话」翻成实际的对话绑定，其余原样返回。
func resolveIfFollowChat(roles map[string]ModelRole, role ModelRole) (ModelRole, bool) {
	if !role.FollowChat {
		return role, true
	}
	return resolveFollowChatRole(roles)
}

// resolveFollowChatRole 返回对话那一档的绑定。对话本身没配好时原样返回 FollowChat，
// 由 profilesForModelRole 报「选了跟随对话但没有可用的对话模型」——比在这里静默滑到
// 全局激活配置好，那种滑动表现为「聊着聊着换了个模型」而日志看着一切正常。
func resolveFollowChatRole(roles map[string]ModelRole) (ModelRole, bool) {
	if chat, ok := roles["chat"]; ok && !chat.FollowChat && modelRoleConfigured(chat) {
		return chat, true
	}
	return ModelRole{FollowChat: true}, true
}
