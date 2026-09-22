// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/SuInk/diana/model/agent"
)

const dianaExtensionAccessToolName = "extension_access"

// dianaExtensionAccessTool 让主人在对话里读写扩展的开放范围：机器人默认档、某个群
// 的档位，以及本群白名单和黑名单。装卸扩展仍走 mcp_install 那几个工具，这里只管
// 「谁能用」。
type dianaExtensionAccessTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaExtensionAccessTool(runtime *Runtime, event MessageEvent) *dianaExtensionAccessTool {
	return &dianaExtensionAccessTool{runtime: runtime, event: event}
}

func (t *dianaExtensionAccessTool) Name() string { return dianaExtensionAccessToolName }

func (t *dianaExtensionAccessTool) Description() string {
	return `读写 MCP 服务和 Skill 的开放范围，仅主人可用。list 查看本群生效档位（tier）、机器人默认档和本群覆盖；bot_tier 改机器人默认档；group_tier 改某个群的档位；allow / deny 改某个群的白名单、黑名单。档位取值 off（停用）、owner（仅主人）、admins（群主和群管理员）、members（群成员）；group_tier 传空档位表示这个群跟随机器人，会连本群名单一起清掉。判定顺序是停用 > 黑名单 > 白名单 > 档位，白名单里的人不看档位和群身份，黑名单一律不给，两份名单都不作用于主人。写操作第一次会被拒绝并给出确认码，等用户原样回复后再重发。`
}

func (t *dianaExtensionAccessTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"action"}, map[string]any{
		"action": toolEnumParam("要做的事，省略 id 时只有 list 可用。",
			"list", "bot_tier", "group_tier", "allow", "deny"),
		"id":       toolStringParam(`扩展 ID，形如 mcp:notes 或 skill:daily-summary，取自 list 结果。`),
		"tier":     toolStringParam("档位：off、owner、admins、members；group_tier 传空表示跟随机器人。"),
		"user_id":  toolStringParam("allow / deny 的目标账号 ID，不认昵称。"),
		"group_id": toolStringParam("目标群号，省略表示当前群。"),
		"remove":   toolBoolParam("true 表示从名单里移除，默认是加入。"),
	})
}

// ExplicitUserRequestKind 只有写操作要确认码，读不拦。
func (t *dianaExtensionAccessTool) ExplicitUserRequestKind(input map[string]any) string {
	if strings.TrimSpace(configToolString(input, "action")) == "list" {
		return ""
	}
	return "extension_access"
}

func (t *dianaExtensionAccessTool) Run(ctx context.Context, input map[string]any) (string, error) {
	base, err := t.runtime.modelConfigForEvent(t.event)
	if err != nil {
		return "", err
	}
	// 注册时已经按主人筛过一次，这里再核一次：两道闸都在。
	if !base.IsOwnerEvent(t.event) {
		return "", fmt.Errorf("只有机器人主人可以改扩展的开放范围")
	}
	action := strings.TrimSpace(configToolString(input, "action"))
	if action == "" {
		action = "list"
	}
	id := strings.TrimSpace(configToolString(input, "id"))
	if action != "list" && id == "" {
		return "", fmt.Errorf("请给出扩展 ID，形如 mcp:notes，可先用 action=list 查")
	}
	switch action {
	case "list":
		return t.list(ctx, base, strings.TrimSpace(configToolString(input, "group_id")))
	case "bot_tier":
		return t.setBotTier(ctx, base, id, configToolString(input, "tier"))
	case "group_tier", "allow", "deny":
		return t.editGroup(ctx, base, action, id, input)
	}
	return "", fmt.Errorf("不支持的 action")
}

func (t *dianaExtensionAccessTool) list(ctx context.Context, base BotConfig, groupID string) (string, error) {
	states, err := t.extensionStates(ctx, base)
	if err != nil {
		return "", err
	}
	if groupID == "" {
		groupID = strings.TrimSpace(t.event.GroupID)
	}
	group, _ := t.groupConfig(base, groupID)
	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
		// Tier 是这个群当前真正生效的档位。回答「这里能不能用」只看它：以前只给
		// bot_tier 和 group_tier 两个值，模型就把两档一起念出来，听的人还得自己
		// 推谁说了算。
		Tier     string   `json:"tier"`
		BotTier  string   `json:"bot_tier"`
		GroupSet string   `json:"group_tier,omitempty"`
		Allow    []string `json:"group_allow,omitempty"`
		Deny     []string `json:"group_deny,omitempty"`
	}
	items := make([]item, 0, len(states))
	for _, state := range states {
		access := group.ExtensionAccess[state.ID]
		botTier := extensionStateTier(state)
		effective := botTier
		if access.Tier != "" {
			effective = access.Tier
		}
		items = append(items, item{
			ID:       state.ID,
			Name:     state.Name,
			Kind:     string(state.Kind),
			Tier:     effective,
			BotTier:  botTier,
			GroupSet: access.Tier,
			Allow:    access.Allow,
			Deny:     access.Deny,
		})
	}
	return toolJSON(map[string]any{
		"group_id":    groupID,
		"extensions":  items,
		"tier_order":  []string{"off", "owner", "admins", "members"},
		"explanation": "tier 是这个群当前生效的档位，回答能不能用只看它；bot_tier 是机器人默认档，group_tier 非空表示这个群单独设过并已经盖掉默认档。判定顺序：停用 > 黑名单 > 白名单 > 档位。",
	})
}

func (t *dianaExtensionAccessTool) setBotTier(ctx context.Context, base BotConfig, id, rawTier string) (string, error) {
	tier, err := agent.NormalizeExtensionTier(rawTier)
	if err != nil {
		return "", err
	}
	if tier == "" {
		return "", fmt.Errorf("机器人默认档必须是 off、owner、admins 或 members")
	}
	kind, name, err := splitExtensionID(id)
	if err != nil {
		return "", err
	}
	audiences, err := agent.LoadExtensionAudiences(AgentWorkspaceDir(), base.ID)
	if err != nil {
		return "", err
	}
	audience := audiences[id]
	req := agent.ExtensionAdminRequest{Kind: kind, Name: name, ProfileID: base.ID}
	// 先收紧再放开：任一步失败都不会停在「已放开但门槛还没设上」。
	if tier == agent.ExtensionTierOff || tier == agent.ExtensionTierOwner {
		if err := t.administer(ctx, req, "members", false, nil); err != nil {
			return "", err
		}
	}
	if tier == agent.ExtensionTierAdmins || tier == agent.ExtensionTierMembers {
		audience.MinRole = ""
		if tier == agent.ExtensionTierAdmins {
			audience.MinRole = agent.MemberRoleAdmin
		}
		if err := t.administer(ctx, req, "audience", false, &audience); err != nil {
			return "", err
		}
	}
	if err := t.administer(ctx, req, "enabled", tier != agent.ExtensionTierOff, nil); err != nil {
		return "", err
	}
	if tier == agent.ExtensionTierAdmins || tier == agent.ExtensionTierMembers {
		if err := t.administer(ctx, req, "members", true, nil); err != nil {
			return "", err
		}
	}
	return toolJSON(map[string]any{
		"id":       id,
		"bot_tier": tier,
		"message":  fmt.Sprintf("已把 %s 的机器人默认档设为「%s」，后续会话生效", id, extensionTierWord(tier)),
	})
}

func (t *dianaExtensionAccessTool) editGroup(ctx context.Context, base BotConfig, action, id string, input map[string]any) (string, error) {
	groupID := strings.TrimSpace(configToolString(input, "group_id"))
	if groupID == "" {
		groupID = strings.TrimSpace(t.event.GroupID)
	}
	if groupID == "" {
		return "", fmt.Errorf("请给出群号，或在目标群里操作")
	}
	states, err := t.extensionStates(ctx, base)
	if err != nil {
		return "", err
	}
	known := false
	for _, state := range states {
		if state.ID == id {
			known = true
		}
	}
	if !known {
		return "", fmt.Errorf("扩展 %q 不存在，可先用 action=list 查", id)
	}
	group, ok := t.groupConfig(base, groupID)
	if !ok {
		group = DefaultGroupConfig(groupID, base)
	}
	group.BotProfileID = base.ID
	access := map[string]GroupExtensionAccess{}
	for key, value := range group.ExtensionAccess {
		access[key] = value
	}
	entry := access[id]
	message := ""
	switch action {
	case "group_tier":
		tier, err := agent.NormalizeExtensionTier(configToolString(input, "tier"))
		if err != nil {
			return "", err
		}
		if tier == "" {
			// 跟随就是本群不干预，名单一起清掉，不留看不见的配置。
			delete(access, id)
			message = fmt.Sprintf("已把 %s 在群 %s 改回跟随机器人，本群名单一并清空", id, groupID)
		} else {
			entry.Tier = tier
			access[id] = entry
			message = fmt.Sprintf("已把 %s 在群 %s 设为「%s」", id, groupID, extensionTierWord(tier))
		}
	case "allow", "deny":
		target := strings.TrimSpace(configToolString(input, "user_id"))
		if target == "" && t.event.Quoted != nil {
			target = strings.TrimSpace(t.event.Quoted.UserID)
		}
		if err := validateAccountID(target); err != nil {
			return "", err
		}
		if entry.Tier == "" {
			return "", fmt.Errorf("这个群还没单独设档位，名单不会生效；先用 group_tier 给群 %s 选一档", groupID)
		}
		remove := boolInput(input, "remove")
		listName := "白名单"
		if action == "deny" {
			listName = "黑名单"
		}
		if action == "allow" {
			entry.Allow = editAccountList(entry.Allow, target, remove)
		} else {
			entry.Deny = editAccountList(entry.Deny, target, remove)
		}
		access[id] = entry
		verb := "加入"
		if remove {
			verb = "移出"
		}
		message = fmt.Sprintf("已把 %s %s群 %s 里 %s 的%s", target, verb, groupID, id, listName)
	}
	if len(access) == 0 {
		group.ExtensionAccess = nil
	} else {
		group.ExtensionAccess = access
	}
	t.runtime.mu.RLock()
	writer, ok := t.runtime.groupConfigs.(GroupConfigWriter)
	t.runtime.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("当前未接入可写的群配置存储")
	}
	saved, err := writer.SaveGroupConfig(group, base)
	if err != nil {
		return "", err
	}
	entry = saved.ExtensionAccess[id]
	return toolJSON(map[string]any{
		"id":         id,
		"group_id":   groupID,
		"group_tier": entry.Tier,
		"allow":      entry.Allow,
		"deny":       entry.Deny,
		"message":    message + "，后续会话生效",
	})
}

func (t *dianaExtensionAccessTool) administer(ctx context.Context, req agent.ExtensionAdminRequest, operation string, enabled bool, audience *agent.ExtensionAudience) error {
	req.Operation = operation
	req.Enabled = enabled
	if audience != nil {
		req.Audience = *audience
	}
	_, err := t.runtime.AdministerExtensions(ctx, req)
	return err
}

func (t *dianaExtensionAccessTool) extensionStates(ctx context.Context, base BotConfig) ([]agent.ExtensionState, error) {
	result, err := t.runtime.AdministerExtensions(ctx, agent.ExtensionAdminRequest{Operation: "list", ProfileID: base.ID})
	if err != nil {
		return nil, err
	}
	payload, _ := result.(map[string]any)
	states, _ := payload["items"].([]agent.ExtensionState)
	out := make([]agent.ExtensionState, 0, len(states))
	for _, state := range states {
		if state.Kind == agent.ExtensionKindMCP || state.Kind == agent.ExtensionKindSkill {
			out = append(out, state)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (t *dianaExtensionAccessTool) groupConfig(base BotConfig, groupID string) (GroupConfig, bool) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return GroupConfig{}, false
	}
	t.runtime.mu.RLock()
	store := t.runtime.groupConfigs
	t.runtime.mu.RUnlock()
	if store == nil {
		return GroupConfig{}, false
	}
	cfg, ok := store.ConfigForGroup(base.ID, groupID)
	if !ok {
		return GroupConfig{}, false
	}
	return cfg.WithDefaults(groupID, base), true
}

// extensionStateTier 把目录里那几个字段折算成档位，和后端 BotExtensionTier 同一套规则。
func extensionStateTier(state agent.ExtensionState) string {
	if !state.Enabled {
		return agent.ExtensionTierOff
	}
	if state.MembersEnabled == nil || !*state.MembersEnabled {
		return agent.ExtensionTierOwner
	}
	if state.MemberAudience != nil && state.MemberAudience.RequiresGroupAdmin() {
		return agent.ExtensionTierAdmins
	}
	return agent.ExtensionTierMembers
}

func extensionTierWord(tier string) string {
	switch tier {
	case agent.ExtensionTierOff:
		return "停用"
	case agent.ExtensionTierAdmins:
		return "群管"
	case agent.ExtensionTierMembers:
		return "群成员"
	default:
		return "仅主人"
	}
}

func splitExtensionID(id string) (kind, name string, err error) {
	parts := strings.SplitN(strings.TrimSpace(id), ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("扩展 ID 形如 mcp:notes 或 skill:daily-summary")
	}
	kind = strings.TrimSpace(parts[0])
	if kind != string(agent.ExtensionKindMCP) && kind != string(agent.ExtensionKindSkill) {
		return "", "", fmt.Errorf("只支持 mcp: 和 skill: 开头的扩展 ID")
	}
	return kind, strings.TrimSpace(parts[1]), nil
}

func validateAccountID(value string) error {
	if value == "" {
		return fmt.Errorf("请指定目标账号 ID，或引用对方的消息")
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char == '_' || char == '-') {
			return fmt.Errorf("目标必须是账号 ID，不能使用昵称")
		}
	}
	return nil
}

func editAccountList(list []string, target string, remove bool) []string {
	out := make([]string, 0, len(list)+1)
	for _, item := range list {
		if strings.TrimSpace(item) == target {
			continue
		}
		out = append(out, item)
	}
	if !remove {
		out = append(out, target)
	}
	sort.Strings(out)
	return out
}

func toolJSON(payload map[string]any) (string, error) {
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}
