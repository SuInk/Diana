// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package webui

import (
	"context"
	"log"
	"strings"

	"github.com/SuInk/diana/model/assistant"
	"github.com/SuInk/diana/model/storage"
)

// RepairSeedLLMProfileRefs 把机器人配置里指向旧种子提供商 ID 的引用改回现在的 ID。
//
// 修复前，只靠 config.yaml 播种的提供商配置档每次启动都换一个随机 ID。机器人配置在
// WebUI 保存过的话，它的 model_roles 记着保存那次启动的 ID，下次启动就成了
// 「model role profile ... was not found」，机器人起不来。
//
// 只在提供商配置集从没保存过时做：那样的库里从来只有过这一份种子配置档，任何一个
// 对不上的提供商 ID 都只能是它在某次启动时的旧号，改回来不会串到别的提供商上。
// 保存过的库不碰：对不上的 ID 也可能属于已经删掉的配置档，悄悄改指到另一个提供商
// 等于把请求（和里面的对话）发给用户没选过的那家，还不如留着报错让人在界面上重选。
//
// 必须在机器人配置的存储读库之前调用，否则它手里还是旧数据。没保存过的机器人配置
// 每次从 config.yaml 读，这里不碰，也不会因此把它落库。
func RepairSeedLLMProfileRefs(ctx context.Context, store *storage.SQLiteStore, seedID string) error {
	seedID = strings.TrimSpace(seedID)
	if store == nil || seedID == "" {
		return nil
	}
	profiles, ok, err := store.LoadBotProfiles(ctx)
	if err != nil {
		return err
	}
	// 引用提供商配置档的只有机器人配置里的 model_roles 和回复规则；群配置不带模型绑定。
	if ok {
		changed := 0
		for i := range profiles.Profiles {
			var n int
			profiles.Profiles[i].ModelRoles, n = retargetModelRoles(profiles.Profiles[i].ModelRoles, seedID)
			changed += n
			profiles.Profiles[i].ReplyRules, n = retargetReplyRules(profiles.Profiles[i].ReplyRules, seedID)
			changed += n
		}
		if changed > 0 {
			if err := store.SaveBotProfiles(ctx, profiles); err != nil {
				return err
			}
			log.Printf("diana: %d model binding(s) in bot profiles pointed at an old seeded llm profile id; repointed to %s", changed, seedID)
		}
	}
	return nil
}

// retargetModelRoles 返回改好的新 map 和改动的条数；没有改动时原样返回。
func retargetModelRoles(roles map[string]assistant.ModelRole, seedID string) (map[string]assistant.ModelRole, int) {
	if len(roles) == 0 {
		return roles, 0
	}
	out := make(map[string]assistant.ModelRole, len(roles))
	changed := 0
	for key, role := range roles {
		var n int
		out[key], n = retargetModelRole(role, seedID)
		changed += n
	}
	if changed == 0 {
		return roles, 0
	}
	return out, changed
}

// retargetModelRole 改一条绑定及其备用路线。按分组（Group）绑定的不带 ID，不动。
func retargetModelRole(role assistant.ModelRole, seedID string) (assistant.ModelRole, int) {
	changed := 0
	if id := strings.TrimSpace(role.ProfileID); id != "" && id != seedID {
		role.ProfileID = seedID
		changed++
	}
	if id := strings.TrimSpace(role.ProviderID); id != "" && id != seedID {
		// ModelID 是「提供商 ID:模型名」，前缀跟着换。
		if model, ok := strings.CutPrefix(strings.TrimSpace(role.ModelID), id+":"); ok {
			role.ModelID = seedID + ":" + model
		}
		role.ProviderID = seedID
		changed++
	}
	if len(role.Fallbacks) > 0 {
		fallbacks := make([]assistant.ModelRole, len(role.Fallbacks))
		for i, fallback := range role.Fallbacks {
			var n int
			fallbacks[i], n = retargetModelRole(fallback, seedID)
			changed += n
		}
		role.Fallbacks = fallbacks
	}
	return role, changed
}

// retargetReplyRules 改回复规则里指定的提供商配置档。
func retargetReplyRules(rules []assistant.ReplyRule, seedID string) ([]assistant.ReplyRule, int) {
	changed := 0
	var out []assistant.ReplyRule
	for i, rule := range rules {
		if id := strings.TrimSpace(rule.LLMProfileID); id == "" || id == seedID {
			continue
		}
		if out == nil {
			out = append([]assistant.ReplyRule(nil), rules...)
		}
		out[i].LLMProfileID = seedID
		changed++
	}
	if changed == 0 {
		return rules, 0
	}
	return out, changed
}
