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
	PurposeContextSummary        = "context_summary_compaction"
	PurposeMemoryExtract         = "memory_extract"
	PurposeMemorySummary         = "memory_summary"
	PurposeRelationshipEvaluate  = "relationship_evaluate"
	// PurposeGroupStyle 是风格学习：读群聊写一段「这个群怎么说话」。
	PurposeGroupStyle           = "group_style"
	PurposeForwardContentSafety = "forward_content_safety"
	PurposeReplyAccountSafety   = "reply_account_safety"
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
	// 下面几个以前只是调用点里的字面量，没进这张表：界面上指不了，也没法单独绑，
	// 实际跟着「本次调用的分组」跑。它们问的都要写出成段文字（RSS 那个还要写出
	// 发给用户的通知正文），归后台生成或回复辅助。
	PurposeRSSWatchJudge      = "rss_watch_judge"
	PurposeDirectReplyTopic   = "direct_reply_topic"
	PurposeReplySemanticDedup = "reply_semantic_dedup"
	PurposeSemanticTextRef    = "semantic_text_reference"
	// 下面三个也是写给用户看的话，以前不在表里：旁路调用查不到归属就按意图识别
	// 取模型，意图识别绑了只做判断的模型时，它们每次都先失败一次再降级。
	PurposePokeReply        = "poke_reply"
	PurposeWelcomeGenerator = "welcome_generator"
	PurposeRomanceGreeting  = "romance_greeting"
)

// llmPurposeGroup 把用途归到分组。这张表以前是隐式的——某个用途走哪个分组，取决于
// 它调用的是 runLLMProvider 还是 runLLMRouterProvider，从配置界面上完全看不出来。
// 摊开写成表，用途才谈得上「可覆盖」：没单独绑就落到所属分组的绑定。
var llmPurposeGroup = map[string]string{
	PurposeMediaParse: llm.GroupVision,
	PurposeReply:      llm.GroupChat,
	PurposeSubagent:   llm.GroupChat,
	PurposeSubtask:    llm.GroupChat,

	// 意图识别：判定当前这一轮该不该说话、说出去的这句能不能发。问的都是是非、
	// 单选和打分，发请求时带着判断题表，所以这一档可以绑只做判断的模型。
	PurposeProactiveReplyRouter:  llm.GroupIntent,
	PurposeProactiveReplyQuality: llm.GroupIntent,
	PurposeReplySendAudit:        llm.GroupIntent,

	// 回复辅助：这一轮回复发出之前同步跑的旁路调用，模型慢，回复就跟着慢。
	// 语义指代、话题合并、语义去重和上下文摘要要写出文字；提示改写的结果直接
	// 发进聊天，而且往往是对话模型刚出错的时候——再绕回对话模型最不稳，所以也
	// 留在这里，人设由 withUserFacingPersona 补上。
	PurposeSemanticReference:       llm.GroupReplyAssist,
	PurposeSemanticTextRef:         llm.GroupReplyAssist,
	PurposeDirectReplyTopic:        llm.GroupReplyAssist,
	PurposeReplySemanticDedup:      llm.GroupReplyAssist,
	PurposeContextSummary:          llm.GroupReplyAssist,
	PurposeForwardContentSafety:    llm.GroupReplyAssist,
	PurposeReplyAccountSafety:      llm.GroupReplyAssist,
	PurposeReplySuppression:        llm.GroupReplyAssist,
	PurposeUpstreamRejectionNotice: llm.GroupReplyAssist,
	PurposeAccountSafetyNotice:     llm.GroupReplyAssist,
	PurposeErrorNotice:             llm.GroupReplyAssist,
	// 戳一戳的回应就是一次回复，对方戳完在等。
	PurposePokeReply: llm.GroupReplyAssist,

	// 下面这些也是判定，但眼下还没有各自的判断题表，先留在回复辅助：归进意图
	// 识别只会让它们在绑判断模型时每次先失败一次再降级。题表补上再挪过去。
	PurposeReplyIntentRouter: llm.GroupReplyAssist,
	PurposeReplyRuleRouter:   llm.GroupReplyAssist,
	PurposeBotReplyLoop:      llm.GroupReplyAssist,

	// 后台生成：好感度、长期记忆、RSS 判定和主动问候都要写出成段文字，判断模型答不了；
	// 它们也都在回复之外异步跑，可以指一个便宜的慢模型。
	PurposeRelationshipEvaluate: llm.GroupBackground,
	PurposeGroupStyle:           llm.GroupBackground,
	PurposeMemoryExtract:        llm.GroupBackground,
	PurposeMemorySummary:        llm.GroupBackground,
	PurposeRSSWatchJudge:        llm.GroupBackground,
	// 入群欢迎和纪念日问候是机器人自己起的头，没人在等，慢一点没关系。
	PurposeWelcomeGenerator: llm.GroupBackground,
	PurposeRomanceGreeting:  llm.GroupBackground,
}

// modelBindingGroups 是所有分组。它们就是「用途的归属地」，缺一个就有一批用途没有
// 模型可用。
var modelBindingGroups = []string{
	llm.GroupChat, llm.GroupVision, llm.GroupIntent, llm.GroupReplyAssist, llm.GroupBackground, llm.GroupImage, llm.GroupEmbedding,
}

// modelBindingGroupParent 是分组没绑定时先去找的上一档，找不到才落到 chat。
//
// 回复辅助是从后台生成里拆出来的：拆之前给后台生成指过模型的，升级后这些用途
// 还该用那个模型，不能悄悄换成对话模型。
var modelBindingGroupParent = map[string]string{
	llm.GroupReplyAssist: llm.GroupBackground,
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
	for _, group := range modelBindingGroups {
		if modelRoleKeyForGroup(group) == key {
			return true
		}
	}
	return false
}

// modelRoleFor 按「用途 → 本次调用的分组 → 用途归属的分组 → 上一档分组 → chat」
// 的顺序找绑定。
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
	if role, ok := roles[groupKey]; ok {
		return resolveIfFollowChat(roles, role)
	}
	if role, ok := inheritedGroupRole(roles, purpose, groupKey); ok {
		return resolveIfFollowChat(roles, role)
	}
	role, ok := roles["chat"]
	return role, ok
}

// inheritedGroupRole 顺着「用途归属的分组 → 它的上一档」和「本次调用的分组 → 它的
// 上一档」找绑定。都没绑时返回 false，由调用方落到 chat。
func inheritedGroupRole(roles map[string]ModelRole, purpose, groupKey string) (ModelRole, bool) {
	for _, key := range []string{ModelBindingGroupOf(purpose), groupKey} {
		for ; key != ""; key = modelBindingGroupParent[key] {
			if role, ok := roles[key]; ok {
				return role, true
			}
		}
	}
	return ModelRole{}, false
}

// hasDedicatedModelRole 说明这个用途有没有落到对话以外的模型上。可选的预处理靠它
// 决定跑不跑：只能用对话模型时宁可不跑，别让一道可有可无的工序花主回复的钱。
func hasDedicatedModelRole(roles map[string]ModelRole, purpose, group string) bool {
	groupKey := modelRoleKeyForGroup(group)
	if groupKey == "chat" || roles[groupKey].FollowChat {
		return false
	}
	role, ok := roles[strings.TrimSpace(purpose)]
	if !ok {
		role, ok = inheritedGroupRole(roles, purpose, groupKey)
	}
	return ok && !role.FollowChat && modelRoleConfigured(role)
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
