// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 两台机器人的人设必须互不串门：批量归一化以前只拿到一份 base，于是「保存 A 的
// 某个群」会顺手把 A 的人设写进 B 的群里。
func personaTestProfiles() (BotConfig, BotConfig, BotConfigResolver) {
	alpha := BotConfig{ID: "bot-alpha", SystemPrompt: "阿尔法的人设正文"}
	beta := BotConfig{ID: "bot-beta", SystemPrompt: "贝塔的人设正文"}
	resolve := func(profileID string) (BotConfig, bool) {
		switch strings.TrimSpace(profileID) {
		case alpha.ID:
			return alpha, true
		case beta.ID:
			return beta, true
		}
		return BotConfig{}, false
	}
	return alpha, beta, resolve
}

func TestGroupConfigSetWithDefaultsResolvedKeepsPersonaOnOwnBot(t *testing.T) {
	alpha, beta, resolve := personaTestProfiles()
	set := GroupConfigSet{Groups: []GroupConfig{
		// 没写人设也没有老风格：留空，运行时按所属机器人解析。
		{BotProfileID: beta.ID, GroupID: "100"},
		// 老数据显式选过风格：迁移要保住这份意图，但只能从自己那台取正文。
		{BotProfileID: beta.ID, GroupID: "200", ReplyStyle: ReplyStyleGentle},
		{BotProfileID: alpha.ID, GroupID: "300", ReplyStyle: ReplyStyleConcise},
		// 档案已被删除：解析不到就什么都不种，绝不拿手上这份 base 顶替。
		{BotProfileID: "bot-gone", GroupID: "400", ReplyStyle: ReplyStyleGentle},
	}}

	got := set.WithDefaultsResolved(alpha, resolve)
	byGroup := map[string]GroupConfig{}
	for _, cfg := range got.Groups {
		byGroup[cfg.GroupID] = cfg
	}

	if prompt := byGroup["100"].SystemPrompt; prompt != "" {
		t.Fatalf("群 100 的人设应保持为空以跟随机器人，实际 = %q", prompt)
	}
	for _, groupID := range []string{"100", "200"} {
		if prompt := byGroup[groupID].SystemPrompt; strings.Contains(prompt, "阿尔法") {
			t.Fatalf("群 %s 属于 bot-beta，却被写进了阿尔法的人设：%q", groupID, prompt)
		}
	}
	if prompt := byGroup["200"].SystemPrompt; !strings.Contains(prompt, "贝塔的人设正文") {
		t.Fatalf("群 200 选过老风格，应从自己那台机器人继承人设，实际 = %q", prompt)
	}
	if prompt := byGroup["300"].SystemPrompt; !strings.Contains(prompt, "阿尔法的人设正文") {
		t.Fatalf("群 300 选过老风格，应从自己那台机器人继承人设，实际 = %q", prompt)
	}
	if prompt := byGroup["400"].SystemPrompt; prompt != "" {
		t.Fatalf("机器人档案已不存在的群不该被种入任何人设，实际 = %q", prompt)
	}
}

// 没有解析器时保持改造前的行为：仍然只有「这个群就是这台机器人的」才继承。
func TestGroupConfigSetWithDefaultsWithoutResolverNeverBorrowsPersona(t *testing.T) {
	alpha, beta, _ := personaTestProfiles()
	set := GroupConfigSet{Groups: []GroupConfig{
		{BotProfileID: beta.ID, GroupID: "200", ReplyStyle: ReplyStyleGentle},
	}}
	got := set.WithDefaults(alpha)
	if prompt := got.Groups[0].SystemPrompt; prompt != "" {
		t.Fatalf("拿错 base 时宁可留空，实际 = %q", prompt)
	}
}

func TestGroupConfigSetUpsertResolvedLeavesOtherProfilesAlone(t *testing.T) {
	alpha, beta, resolve := personaTestProfiles()
	set := GroupConfigSet{Groups: []GroupConfig{
		{BotProfileID: beta.ID, GroupID: "200", SystemPrompt: "贝塔这个群自己写的人设"},
	}}
	set = set.UpsertResolved(GroupConfig{BotProfileID: alpha.ID, GroupID: "200", ReplyStyle: ReplyStyleGentle}, alpha, resolve)

	betaCfg, ok := set.ConfigForGroup(beta.ID, "200")
	if !ok || betaCfg.SystemPrompt != "贝塔这个群自己写的人设" {
		t.Fatalf("写 alpha 的群不该动 beta 的那份：%+v", betaCfg)
	}
	alphaCfg, ok := set.ConfigForGroup(alpha.ID, "200")
	if !ok || !strings.Contains(alphaCfg.SystemPrompt, "阿尔法的人设正文") {
		t.Fatalf("alpha 的群应从 alpha 继承人设：%+v", alphaCfg)
	}
}

// 空人设走完归一化仍然是空的，运行时才会回落到机器人的系统提示词。
func TestEmptyGroupPersonaStaysEmptySoRuntimeInherits(t *testing.T) {
	alpha, _, resolve := personaTestProfiles()
	cfg := GroupConfig{BotProfileID: alpha.ID, GroupID: "100"}.WithDefaultsResolved("100", alpha, resolve)
	if cfg.SystemPrompt != "" {
		t.Fatalf("没写人设的群不该被补上正文，实际 = %q", cfg.SystemPrompt)
	}

	runtimeCfg := alpha.WithDefaults()
	if strings.TrimSpace(cfg.SystemPrompt) != "" {
		runtimeCfg.SystemPrompt = cfg.SystemPrompt
	}
	if !strings.Contains(runtimeCfg.SystemPrompt, "阿尔法的人设正文") {
		t.Fatalf("运行时应沿用机器人的人设，实际 = %q", runtimeCfg.SystemPrompt)
	}
}
