// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const dianaIdentityLinksToolName = "identity_links"

// dianaIdentityLinksTool 让主人直接关联或解除两个账号，不必让本人走验证码。
//
// 只给主人，不给管理员：关联到主人的账号会被认作主人，管理员能关联就能把自己升成主人。
type dianaIdentityLinksTool struct {
	runtime *Runtime
	event   MessageEvent
}

func newDianaIdentityLinksTool(runtime *Runtime, event MessageEvent) *dianaIdentityLinksTool {
	return &dianaIdentityLinksTool{runtime: runtime, event: event}
}

func (*dianaIdentityLinksTool) Name() string { return dianaIdentityLinksToolName }

func (*dianaIdentityLinksTool) Description() string {
	return "查看、关联或解除跨平台账号关联（同一个人在不同机器人上的账号），仅主人。关联后好感度和个人记忆合在一起看，关联到主人的账号也按主人认。机器人 ID 和平台先 list 查。"
}

func (*dianaIdentityLinksTool) InputSchema() map[string]any {
	return toolObjectSchema([]string{"operation"}, map[string]any{
		"operation":        toolEnumParam("list 查看全部关联和机器人列表；link 把两个账号关联成同一个人；unlink 解除一个账号的关联", "list", "link", "unlink"),
		"profile_id":       toolStringParam("账号所在的机器人 ID，留空表示当前机器人"),
		"user_id":          toolStringParam("link/unlink 必填：账号的用户 ID"),
		"other_profile_id": toolStringParam("link：另一个账号所在的机器人 ID，留空表示当前机器人"),
		"other_user_id":    toolStringParam("link 必填：另一个账号的用户 ID"),
	})
}

// CanonicalOperation 是 Run 实际执行的操作，安全模式按操作拦截时用同一套换算。
func (*dianaIdentityLinksTool) CanonicalOperation(input map[string]any) string {
	return strings.ToLower(strings.TrimSpace(configToolString(input, "operation")))
}

func (t *dianaIdentityLinksTool) Run(ctx context.Context, input map[string]any) (string, error) {
	if t == nil || t.runtime == nil || t.runtime.identityLinkStore() == nil {
		return "", fmt.Errorf("账号关联存储未配置")
	}
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	if !cfg.IsOwnerEvent(t.event) && !isAdminChatRun(ctx) {
		return "", fmt.Errorf("只有机器人主人可以管理账号关联")
	}
	switch op := t.CanonicalOperation(input); op {
	case "list":
		return t.list(ctx)
	case "link":
		a, err := t.account(input, "profile_id", "user_id")
		if err != nil {
			return "", err
		}
		b, err := t.account(input, "other_profile_id", "other_user_id")
		if err != nil {
			return "", err
		}
		a.Source, b.Source = IdentityLinkSourceOwner, IdentityLinkSourceOwner
		group, err := t.runtime.LinkIdentityAccounts(ctx, a, b)
		if err != nil {
			return "", err
		}
		return marshalIdentityLinksResult(map[string]any{"operation": op, "person": IdentityPerson{PersonID: group[0].PersonID, Accounts: group}})
	case "unlink":
		account, err := t.account(input, "profile_id", "user_id")
		if err != nil {
			return "", err
		}
		removed, err := t.runtime.UnlinkIdentityAccount(ctx, account)
		if err != nil {
			return "", err
		}
		return marshalIdentityLinksResult(map[string]any{"operation": op, "removed": removed, "account": account})
	default:
		return "", fmt.Errorf("不支持的 operation %q", op)
	}
}

func (t *dianaIdentityLinksTool) list(ctx context.Context) (string, error) {
	items, err := t.runtime.ListIdentityLinks(ctx)
	if err != nil {
		return "", err
	}
	type botItem struct {
		ProfileID string `json:"profile_id"`
		Platform  string `json:"platform"`
		Name      string `json:"name,omitempty"`
	}
	bots := []botItem{}
	for _, profile := range t.runtime.ProfileConfigs() {
		bots = append(bots, botItem{ProfileID: profile.ID, Platform: NormalizePlatformID(profile.Platform), Name: profile.Name})
	}
	people := GroupIdentityLinks(items)
	if people == nil {
		people = []IdentityPerson{}
	}
	return marshalIdentityLinksResult(map[string]any{"operation": "list", "bots": bots, "people": people})
}

// account 按输入找出一个账号；机器人 ID 留空时是当前机器人，平台取那台机器人的平台。
func (t *dianaIdentityLinksTool) account(input map[string]any, profileField, userField string) (IdentityAccount, error) {
	userID := strings.TrimSpace(configToolString(input, userField))
	if userID == "" {
		return IdentityAccount{}, fmt.Errorf("%s 必填", userField)
	}
	profileID := strings.TrimSpace(configToolString(input, profileField))
	if profileID == "" {
		profileID = t.runtime.eventProfileID(t.event)
	}
	var profile BotConfig
	found := false
	for _, item := range t.runtime.ProfileConfigs() {
		if item.ID == profileID {
			profile, found = item, true
			break
		}
	}
	if !found {
		return IdentityAccount{}, fmt.Errorf("没有机器人 %q，先 list 查机器人 ID", profileID)
	}
	return IdentityAccount{ProfileID: profile.ID, Platform: NormalizePlatformID(profile.Platform), UserID: userID}, nil
}

func marshalIdentityLinksResult(result map[string]any) (string, error) {
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
