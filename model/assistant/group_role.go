// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "strings"

// GroupRole 是「这个人在群里是什么身份」，取值与平台无关。
//
// 各家协议的说法本来就不一样：OneBot 用 owner/admin/member，Telegram 用
// creator/administrator/member，钉钉只给一个 isAdmin 布尔。把它们收敛成同一套词
// 有两个理由。
//
// 一是这些值会进提示词和工具结果。原样透传的话，模型在 QQ 群里看到 owner、在
// Telegram 群里看到 creator，得自己猜这两个是不是一回事；接一个新平台就多一种
// 说法，之前所有靠这个字段判断的地方都得跟着补。这和脱敏别名前缀特意取 im_ 而
// 不是 qq_ 是同一条规矩：给模型看的词不带平台名。
//
// 二是光秃秃的 owner 和机器人的主人撞名，模型会把群主说成主人。前缀统一带
// group_ 之后，它和主人的 bot_owner（见 RelationshipOwner）正好成对，看一眼就
// 知道说的是哪一个 owner。
type GroupRole string

const (
	GroupRoleOwner  GroupRole = "group_owner"
	GroupRoleAdmin  GroupRole = "group_admin"
	GroupRoleMember GroupRole = "group_member"
)

// NormalizeGroupRole 把各平台的身份说法收敛成统一取值，认不出来返回空串。
//
// 也认自己的输出：老库里的事件存的是归一化之前的原始值，读回来必须还能对上，
// 所以 group_owner 这类值要原样通过。Telegram 的 left/kicked 表示人已经不在群
// 里，那不是一种身份，归到空串。
func NormalizeGroupRole(raw string) GroupRole {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(GroupRoleOwner), "owner", "creator", "founder":
		return GroupRoleOwner
	case string(GroupRoleAdmin), "admin", "administrator", "manager":
		return GroupRoleAdmin
	// restricted 是 Telegram 里被禁言但仍在群内的成员，身份上还是普通成员。
	case string(GroupRoleMember), "member", "restricted":
		return GroupRoleMember
	}
	return ""
}

// GroupRoleCanConfigure 判断这个身份能不能改本群配置。
func GroupRoleCanConfigure(role GroupRole) bool {
	return role == GroupRoleOwner || role == GroupRoleAdmin
}

// GroupRoleLabel 返回给人看的中文称呼；空串表示身份未知，调用方自己决定怎么写。
func GroupRoleLabel(role GroupRole) string {
	switch role {
	case GroupRoleOwner:
		return "群主"
	case GroupRoleAdmin:
		return "管理员"
	case GroupRoleMember:
		return "群成员"
	}
	return ""
}
