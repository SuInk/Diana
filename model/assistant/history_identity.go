// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"strings"
)

// historyIdentityRoleNotice 只讲角色语义，两种承载方式共用。
const historyIdentityRoleNotice = "这些角色由运行时按账号确认，优先于旧记忆里的身份猜测。身份标识仅供内部理解，不要在回复里照抄。"

// historyIdentityNotice 对应 chat_history 工具返回的结构化字段。工具结果本来就是
// JSON，字段名对模型是可见的，照旧说明。
const historyIdentityNotice = "历史昵称可能是旧昵称或不同群名片；同平台相同 sender_user_id 表示同一账号，不要仅凭昵称拆成不同的人。sender_role=bot 表示你自己，bot_owner 表示机器人主人（不是群主），user 表示其他账号。引用消息按 quoted_sender_user_id 和 quoted_sender_role 识别。" + historyIdentityRoleNotice

// historyPromptIdentityNotice 对应提示词里直接渲染的历史行。
//
// 这两种承载方式以前共用一段说明，因为提示词里也跟着一份同样的 JSON。那份 JSON 是
// 纯冗余：发送者别名在同一行的「昵称（别名）」里已经写过一遍，线上抽样 369 条历史
// 全部如此，无一例外。一条历史为此要多付三十多个 token，369 条就是一万二。
//
// 现在角色改成跟在发送者后面的短标记，且只标非 user 的两种；别名仍在括号里，同一
// 账号仍然认得出来。按线上真实角色分布（user 207、bot_owner 158、bot 4）算，这段
// 开销从 12493 token 降到 644，省掉 95%，语义一点没少。
const historyPromptIdentityNotice = "历史昵称可能是旧昵称或不同群名片；同平台括号里相同的账号标识表示同一账号，不要仅凭昵称拆成不同的人。发送者后面跟 " + historySenderTagOwner + " 表示机器人主人（不是群主），跟 " + historySenderTagBot + " 表示你自己，没有标记就是其他账号。" + historyIdentityRoleNotice

const (
	historySenderTagOwner = "[主人]"
	historySenderTagBot   = "[我]"
)

// summaryIdentityPrompt 是压缩摘要专用的结构化身份，必须保留 JSON。
//
// 摘要比它的原始事件活得久，落进提示词时隐私 scope 里往往没有对应的注册记录。
// 脱敏靠 identityPrivacyJSONIDPattern 匹配 "…user_id": "…" 来发现待替换的标识，
// 非数字账号（飞书的 ou_xxx、staff-1 这类）在「昵称（ID）」这种写法里没有任何模式
// 可匹配——去掉这段 JSON，真实 ID 就会原样发给模型。
//
// 历史行没有这个问题：它们的事件都注册过，所以那边只留短角色标记。摘要每轮只有
// 一条，多这几十个 token 换的是脱敏不漏，值得。
func summaryIdentityPrompt(event MessageEvent, configs ...BotConfig) string {
	if strings.TrimSpace(event.UserID) == "" {
		return ""
	}
	identity := struct {
		UserID string `json:"sender_user_id"`
		Role   string `json:"sender_role,omitempty"`
	}{strings.TrimSpace(event.UserID), historySenderRole(event, configs...)}
	body, _ := json.Marshal(identity)
	return " 【这条历史的发言者身份】" + string(body)
}

// historySenderTag 返回跟在发送者后面的角色标记。占多数的普通账号不标，靠「没有
// 标记」表达，这是这段开销能降一个数量级的主要原因。
func historySenderTag(event MessageEvent, configs ...BotConfig) string {
	switch historySenderRole(event, configs...) {
	case "bot":
		return historySenderTagBot
	case "bot_owner":
		return historySenderTagOwner
	default:
		return ""
	}
}

// Use configured account identity when available, including for old events that
// predate outbound tracking. A nickname never grants a trusted role.
func historySenderRole(event MessageEvent, configs ...BotConfig) string {
	userID := strings.TrimSpace(event.UserID)
	if userID == "" {
		return ""
	}
	botID := strings.TrimSpace(event.SelfID)
	ownerID := ""
	if len(configs) > 0 {
		cfg := configs[0]
		if event.Platform != "" && cfg.Platform != "" && NormalizePlatformID(event.Platform) != NormalizePlatformID(cfg.Platform) {
			return "user"
		}
		botID = strings.TrimSpace(firstNonEmpty(cfg.BotAccount, event.SelfID))
		ownerID = strings.TrimSpace(cfg.OwnerID)
	}
	if userID == botID {
		return "bot"
	}
	if userID == ownerID {
		return "bot_owner"
	}
	if len(configs) == 0 {
		return ""
	}
	return "user"
}

func quotedHistoryIdentityEvent(event MessageEvent) MessageEvent {
	quoted := event.Quoted
	if quoted == nil {
		return MessageEvent{}
	}
	return MessageEvent{Platform: event.Platform, SelfID: event.SelfID, UserID: quoted.UserID}
}
