// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// 人机恋（恋爱模式）：让用户可以和机器人确立恋人关系。
//
// 它建立在关系系统之上，不另起一套状态机：好感度照常涨落，恋人只是关系等级的
// 一个新分支。三条原则贯穿实现：
//
//  1. 双重同意。部署者先在配置里打开 romance_enabled（默认关），然后由用户本人
//     当面表白、机器人按好感度门槛决定接不接受。任何一环缺了都成不了。
//  2. 只改语气不改权限。恋人没有任何普通用户拿不到的能力——权限跟着身份和
//     等级走的规则一条没动，谈恋爱不是提权手段。
//  3. 随时能退出。分手只需要本人一句话，主人也可以替任何人解除。分手不清空
//     好感度和画像：关系结束了，相处过的事实还在。
const (
	// romanceStartMinFavorability / romanceStartMinMessages 是机器人「答应」的
	// 门槛，取和朋友等级相同的档：连朋友都算不上就答应表白，人设再好也是秒贴。
	romanceStartMinFavorability = 60
	romanceStartMinMessages     = 30
	// romanceStrainedFavorability 之下恋人进入冷战：关系还在，语气先降温。
	romanceStrainedFavorability = -20
)

// RelationshipPartner 是恋人等级。值不叫 lover：partner 在模型语料里更中性，
// 不会把语气一下带到露骨那一侧。
const RelationshipPartner RelationshipTier = "partner"

// UserRomanceState 是一个人与机器人的恋爱关系状态，挂在长期档案上持久化。
type UserRomanceState struct {
	Active bool `json:"active"`
	// Since 是确立关系的时间，纪念日和「在一起第几天」都从它算。
	Since time.Time `json:"since,omitempty"`
	// StartedBy 记录关系怎么来的：user 是本人表白，owner 是主人在控制台里设的。
	StartedBy string `json:"started_by,omitempty"`
	// LastGreetedOn 是上一次纪念日主动问候发出的本地日期（YYYY-MM-DD），
	// 防止轮询和重启把同一天的祝福发成两遍。
	LastGreetedOn string `json:"last_greeted_on,omitempty"`
}

// romanceActive 报告这份档案当前是否处于恋人关系。
func romanceActive(profile UserMemoryProfile) bool {
	return profile.Romance != nil && profile.Romance.Active
}

// currentRomancePartner 找这台机器人当前的恋人（excludeUserID 之外的）。
//
// 恋爱是单偶的：同一时间只有一位。这个约束靠确立时扫一遍档案来守——恋人通常
// 是零或一个，扫描代价可以忽略。存储不支持列表（测试里的简化实现）或扫描出错
// 时返回 false：查不了全局宁可放行，也不拿故障当理由误拒别人的表白。
func (r *Runtime) currentRomancePartner(ctx context.Context, profileID, excludeUserID string) (UserMemoryProfile, bool) {
	r.mu.RLock()
	store := r.userMemory
	r.mu.RUnlock()
	lister, ok := store.(UserMemoryListStore)
	if !ok {
		return UserMemoryProfile{}, false
	}
	excludeUserID = strings.TrimSpace(excludeUserID)
	scanned := 0
	for offset := 0; scanned < romanceGreetingScanLimit; {
		profiles, _, err := lister.ListUserMemories(ctx, strings.TrimSpace(profileID), "", 100, offset)
		if err != nil {
			log.Printf("chatbot romance partner scan failed: %v", err)
			return UserMemoryProfile{}, false
		}
		if len(profiles) == 0 {
			return UserMemoryProfile{}, false
		}
		for _, profile := range profiles {
			scanned++
			if romanceActive(profile) && strings.TrimSpace(profile.UserID) != excludeUserID {
				return profile, true
			}
		}
		offset += len(profiles)
	}
	return UserMemoryProfile{}, false
}

// applyRomancePolicy 把恋人状态叠加到已经算好的关系策略上。
//
// 主人保留主人的等级和权限，只叠加恋人语气；其他人切换到恋人等级。冷战按当前
// 好感度判断——关系状态和相处温度是两件事，闹矛盾不等于分手。
func applyRomancePolicy(policy RelationshipPolicy, profile UserMemoryProfile, now time.Time) RelationshipPolicy {
	if !romanceActive(profile) {
		return policy
	}
	policy.Romance = true
	since := profile.Romance.Since
	if !since.IsZero() && !now.Before(since) {
		policy.RomanceDays = int(now.Sub(since).Hours()/24) + 1
	}
	policy.RomanceNote = romanceMilestoneNote(since, now)
	strained := profile.Favorability <= romanceStrainedFavorability
	if policy.Owner {
		// 主人的名字和权限不动，语气在原有基础上加恋人的那一层。
		policy.Tone = policy.Tone + "同时你们是确立了关系的恋人：可以自然流露亲昵和挂念，但正事仍然是正事。"
		if strained {
			policy.Tone = policy.Tone + "最近好感度很低，你们在闹别扭：语气收着点，别装没事，也别刻薄。"
		}
		return policy
	}
	policy.Tier = RelationshipPartner
	if strained {
		policy.Name = "冷战"
		policy.Tone = "你们仍是恋人，但正在闹别扭：语气克制、有距离，不用亲昵称呼，也不刻薄、不翻旧账；对方先服软就顺着台阶下。"
	} else {
		policy.Name = "恋人"
		policy.Tone = "像稳定交往中的恋人：自然的亲昵、关心和偶尔的撒娇，可以吃醋也可以拌嘴，但保留自己的性格，不无底线迁就。谈正事、讲事实、提示风险时照常认真，不因为是恋人就报喜不报忧。"
	}
	return policy
}

// romanceMilestoneNote 算今天是不是值得一提的日子。只报整年和整月：天天报
// 「在一起第 N 天」会把纪念日说成口头禅。
func romanceMilestoneNote(since time.Time, now time.Time) string {
	if since.IsZero() || now.Before(since) {
		return ""
	}
	since, now = since.Local(), now.Local()
	if since.Day() != now.Day() {
		return ""
	}
	months := (now.Year()-since.Year())*12 + int(now.Month()) - int(since.Month())
	if months <= 0 {
		return ""
	}
	if months%12 == 0 {
		return fmt.Sprintf("今天是你们确立关系 %d 周年的日子。", months/12)
	}
	return fmt.Sprintf("今天是你们确立关系满 %d 个月的日子。", months)
}

// romanceContextLine 是注入长期记忆块的恋爱状态行。
func romanceContextLine(policy RelationshipPolicy) string {
	if !policy.Romance {
		return ""
	}
	line := "恋爱关系：已与当前发言者确立恋人关系"
	if policy.RomanceDays > 0 {
		line += fmt.Sprintf("（在一起的第 %d 天）", policy.RomanceDays)
	}
	if policy.RomanceNote != "" {
		line += policy.RomanceNote + "纪念日可以自然提起，不用等对方先说。"
	}
	return line
}
