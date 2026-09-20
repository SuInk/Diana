// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"strings"
)

// 按群授权的身份要求。
//
// 「按群」那几项原来是一刀切：群号一填，群里每个人都拿到同样的权限。对「Issue
// 管理人员（按群）」来说这太宽了——它给的是直接写 GitHub 的权力，而一个几百人的
// 群里真正该有这个权力的通常只有群主和管理员。所以每条按群授权可以自己写明要求
// 什么身份：仓库后面跟一个 #group_admin 这样的后缀。
//
// 不写后缀就是老行为（群里所有人都算数），老配置读进来语义不变。机器人主人在哪一
// 档都通行——主人的权限本来就不经过这份名单。
type repositoryAccessRole string

const (
	repositoryAccessRoleAllMembers repositoryAccessRole = "all_members"
	repositoryAccessRoleAdmins     repositoryAccessRole = "group_admin"
	repositoryAccessRoleGroupOwner repositoryAccessRole = "group_owner"
)

// repositoryAccessRoleSeparator 用 # 而不是冒号：owner/repo 里不会出现 #，而完整
// GitHub 链接里的冒号（https://）会和分隔符撞上。
const repositoryAccessRoleSeparator = "#"

func parseRepositoryAccessRole(raw string) (repositoryAccessRole, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(repositoryAccessRoleAllMembers), "all", "member", "members", "group_member":
		return repositoryAccessRoleAllMembers, nil
	case string(repositoryAccessRoleAdmins), "admin", "admins", "administrator":
		return repositoryAccessRoleAdmins, nil
	case string(repositoryAccessRoleGroupOwner), "owner", "creator":
		return repositoryAccessRoleGroupOwner, nil
	}
	return "", fmt.Errorf("unknown group role requirement %q", raw)
}

// satisfiedBy 判断这个身份够不够格。身份未知（空串）时除「所有成员」外一律不放行：
// 平台没给身份就按最严的算，不能因为查不到就当普通成员放过去。
func (requirement repositoryAccessRole) satisfiedBy(role GroupRole) bool {
	switch requirement {
	case repositoryAccessRoleGroupOwner:
		return role == GroupRoleOwner
	case repositoryAccessRoleAdmins:
		return GroupRoleCanConfigure(role)
	default:
		return true
	}
}

// looser 在同一个群、同一个仓库被两条授权同时覆盖时取宽的那条。管理人员本来就自动
// 是草稿人，两边要求不一致时，草稿那侧应该按更宽的算，否则「管理员才是管理人员」
// 会顺带把普通成员的草稿权也收走。
func (requirement repositoryAccessRole) looser(other repositoryAccessRole) repositoryAccessRole {
	if requirement.rank() <= other.rank() {
		return requirement
	}
	return other
}

func (requirement repositoryAccessRole) rank() int {
	switch requirement {
	case repositoryAccessRoleAdmins:
		return 1
	case repositoryAccessRoleGroupOwner:
		return 2
	default:
		return 0
	}
}

// label 是拒绝时说给用户听的话。
func (requirement repositoryAccessRole) label() string {
	switch requirement {
	case repositoryAccessRoleGroupOwner:
		return "群主"
	case repositoryAccessRoleAdmins:
		return "群主或群管理员"
	default:
		return "群成员"
	}
}

// repositoryAccessRoles 是「群 ID → 仓库 → 身份要求」。取不到的组合按所有成员处理，
// 这样老配置和没写后缀的条目都走原来的语义。
type repositoryAccessRoles map[string]map[string]repositoryAccessRole

func (roles repositoryAccessRoles) requirement(scopeID, repository string) repositoryAccessRole {
	if requirement, ok := roles[scopeID][repository]; ok {
		return requirement
	}
	return repositoryAccessRoleAllMembers
}

func (roles repositoryAccessRoles) set(scopeID, repository string, requirement repositoryAccessRole) {
	if roles[scopeID] == nil {
		roles[scopeID] = map[string]repositoryAccessRole{}
	}
	roles[scopeID][repository] = requirement
}

// groupRoleResolver 惰性取当前发言人的群身份。绝大多数部署一条带后缀的授权都没有，
// 这时根本不该为了「万一要判身份」多打一次群成员查询，所以查询推迟到真正用得上时。
type groupRoleResolver func() GroupRole

func resolvedGroupRole(resolve groupRoleResolver) GroupRole {
	if resolve == nil {
		return ""
	}
	return resolve()
}
