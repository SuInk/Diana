// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"log"
	"strings"
	"time"
)

// 关联账号在读取时生效的三处：好感度画像、个人记忆和尾部提示词。写入一律仍落在
// 当前账号上：好感度按增量写（UserMemoryUpdate.FavorabilityDelta），读的时候合并，
// 所以不需要把几份档案搬到一起，解除关联后各自的数据也原样还在。

// linkedUserMemoryProfileLimit 是合并后保留的旧式记忆条数上限，和存储层单个账号的
// 上限（maxUserMemoryItems）同量级的两倍：本人的排前面，关联账号的补在后面。
const linkedUserMemoryProfileLimit = 40

// mergeLinkedUserProfiles 把关联账号的画像并进当前账号的画像。
//
// 好感度：每个账号建档时都自带一份起始分（DefaultUserFavorability），合并时只累加
// 各账号相对起始分的变化，再夹回可写区间。这样合并后的分数等于「这些互动全发生在
// 同一个账号上」时的分数：负分不会因为换个平台就洗白，正分也不会因为多一个号就
// 白送一份起始分。当前账号还没建档时按起始分算。
//
// 互动次数相加；画像和旧式记忆以本人的为准，关联账号的只补本人没有的条目。
func mergeLinkedUserProfiles(current UserMemoryProfile, currentFound bool, linked []UserMemoryProfile) UserMemoryProfile {
	if len(linked) == 0 {
		return current
	}
	score := current.Favorability
	if !currentFound {
		score = DefaultUserFavorability
	}
	seenTrait := map[string]bool{}
	for _, trait := range current.Portrait {
		seenTrait[string(trait.Field)+"\x00"+trait.Value] = true
	}
	seenMemory := map[string]bool{}
	for _, item := range current.Memories {
		seenMemory[item.Text] = true
	}
	current.Portrait = append([]UserPortraitTrait(nil), current.Portrait...)
	current.Memories = append([]UserMemoryItem(nil), current.Memories...)
	for _, profile := range linked {
		score += profile.Favorability - DefaultUserFavorability
		current.MessageCount += profile.MessageCount
		for _, trait := range profile.Portrait {
			key := string(trait.Field) + "\x00" + trait.Value
			if !seenTrait[key] {
				seenTrait[key] = true
				current.Portrait = append(current.Portrait, trait)
			}
		}
		for _, item := range profile.Memories {
			if !seenMemory[item.Text] && len(current.Memories) < linkedUserMemoryProfileLimit {
				seenMemory[item.Text] = true
				current.Memories = append(current.Memories, item)
			}
		}
		if profile.LastSeenAt.After(current.LastSeenAt) {
			current.LastSeenAt = profile.LastSeenAt
		}
	}
	current.Favorability = min(max(score, MinimumFavorability), MaximumFavorability)
	return current
}

// loadLinkedUserMemoryProfiles 读当前发言者关联账号的画像。每个账号按它自己那台机器人
// 的归属读：好感度本来就按机器人分开记。
func (r *Runtime) loadLinkedUserMemoryProfiles(ctx context.Context, store UserMemoryStore, event MessageEvent) []UserMemoryProfile {
	accounts := r.linkedIdentityAccounts(event)
	if len(accounts) == 0 || store == nil {
		return nil
	}
	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out := make([]UserMemoryProfile, 0, len(accounts))
	for _, account := range accounts {
		profile, ok, err := store.GetUserMemory(loadCtx, account.ProfileID, account.UserID)
		if err != nil {
			log.Printf("diana linked user memory load failed: %v", err)
			continue
		}
		if ok {
			out = append(out, profile)
		}
	}
	return out
}

// linkedMemorySubjects 是关联账号的个人记忆所在。多机器人时会话带「机器人 ID:」前缀，
// 关联账号的记忆在它自己那台机器人的命名空间下；当前会话没有命名空间（单机器人、
// 老数据）时没法可靠地分辨来源，只放开同一台机器人上的关联账号。
func (r *Runtime) linkedMemorySubjects(event MessageEvent) []StructuredMemorySubject {
	accounts := r.linkedIdentityAccounts(event)
	if len(accounts) == 0 {
		return nil
	}
	namespaced := strings.TrimSpace(event.ContextNamespace) != ""
	profileID := r.eventProfileID(event)
	out := make([]StructuredMemorySubject, 0, len(accounts))
	for _, account := range accounts {
		namespace := ""
		if namespaced {
			namespace = account.ProfileID + ":"
		} else if account.ProfileID != profileID {
			continue
		}
		out = append(out, StructuredMemorySubject{Namespace: namespace, UserID: account.UserID})
	}
	return out
}

// markLinkedMemorySubjects 给关联账号的个人记忆打标，排序时按「当前用户」对待。
func markLinkedMemorySubjects(query StructuredMemoryQuery, items []StructuredMemoryItem) {
	if len(query.LinkedSubjects) == 0 {
		return
	}
	for index := range items {
		item := &items[index]
		if item.Visibility != MemoryVisibilityUser || item.SubjectUserID == "" {
			continue
		}
		for _, subject := range query.LinkedSubjects {
			if item.SubjectUserID == subject.UserID &&
				(strings.HasPrefix(item.SourceSession, subject.Namespace+"group:") || strings.HasPrefix(item.SourceSession, subject.Namespace+"private:")) {
				item.LinkedSubject = true
				break
			}
		}
	}
}

const promptSpeakerLinkedAccounts = "当前发言者已核实和这些账号是同一个人：{accounts}。好感度和个人记忆已经合在一起看，按同一个人对待；这是他的私人信息，他本人没问起时不要在群里提。"

var promptSpeakerLinkedAccountsSpec = tailSpec("speaker_linked_accounts", "发言者的关联账号", "当前发言者在别的平台上有已核实的关联账号时，紧跟在【当前发言者身份】后面放在尾部。",
	promptSpeakerLinkedAccounts,
	PromptVar{Name: "accounts", Description: "关联账号列表，如「Telegram @alice（12345）」"})

// linkedIdentityPrompt 是尾部那一句关联账号说明，没有关联时为空。只进尾部：它随
// 发言者变化，放进稳定前缀会让每换一个人说话就切断一次缓存。
func (r *Runtime) linkedIdentityPrompt(event MessageEvent, cfg BotConfig) string {
	accounts := r.linkedIdentityAccounts(event)
	if len(accounts) == 0 {
		return ""
	}
	labels := make([]string, 0, len(accounts))
	for _, account := range accounts {
		labels = append(labels, identityAccountLabel(account))
	}
	return cfg.promptf(promptSpeakerLinkedAccountsSpec, map[string]string{"accounts": strings.Join(labels, "、")})
}
