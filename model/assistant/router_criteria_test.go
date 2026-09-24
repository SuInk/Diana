// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

func routerPromptForConfig(cfg BotConfig) string {
	cfg = cfg.WithDefaults()
	return proactiveReplyRouterPromptForChatIn(cfg.prompt(promptLegacyRouterSpec), cfg.ProactiveReplyExtraCriteria, cfg.chatInSettings(), false)
}

// 这条是这次修复的本体：以前 configured 在 Participation 分支里根本没被读过。
func TestCustomCriteriaReachTheRouterPrompt(t *testing.T) {
	const criteria = "群里叫「糖宝」指的是另一位群友，不是在叫我。"
	prompt := routerPromptForConfig(BotConfig{ProactiveReplyExtraCriteria: criteria})
	if !strings.Contains(prompt, criteria) {
		t.Fatalf("补充判据没进评分提示词，界面上那个文本框又成了摆设")
	}
	if !strings.Contains(prompt, participationScorePrompt) {
		t.Fatalf("补充判据把内置评分提示词顶掉了，应该是追加不是替换")
	}
}

// 评分契约必须是模型读到的最后一句，否则解析失败的代价是整群沉默。
func TestCustomCriteriaKeepTheContractLast(t *testing.T) {
	prompt := routerPromptForConfig(BotConfig{ProactiveReplyExtraCriteria: "本群禁止讨论外服比分。"})
	if !strings.HasSuffix(strings.TrimSpace(prompt), routerCriteriaContractGuard) {
		t.Fatalf("补充判据之后没有重申输出契约，JSON 格式要求退到了倒数第二位")
	}
}

// 空判据不该留下段头，否则模型会对着一个空标题猜内容。
func TestEmptyCriteriaLeaveNoHeading(t *testing.T) {
	for _, configured := range []string{"", "   "} {
		if got := customRouterCriteria(configured); got != "" {
			t.Fatalf("空判据被当成了内容：%q", got)
		}
	}
	if strings.Contains(routerPromptForConfig(BotConfig{}), routerCriteriaHeading) {
		t.Fatalf("没配过补充判据却拼上了段头")
	}
}

// 旧的整段路由提示词已被评分契约取代，补充判据这条新路不得把它重新激活：
// 存量库里那一栏存的多半是历代内置默认值的化石，不是用户写的规则。
func TestLegacyRouterPromptStaysInert(t *testing.T) {
	prompt := routerPromptForConfig(BotConfig{PromptOverrides: PromptOverrides{promptLegacyRouterSpec.Key: "旧规则：没有新信息不能发言"}})
	if strings.Contains(prompt, "旧规则") || strings.Contains(prompt, routerCriteriaHeading) {
		t.Fatalf("被取代的旧路由提示词又被读进提示词了")
	}
}

// 群级判据覆盖机器人级：段头里写的就是「本群」。
func TestGroupCriteriaOverrideBotLevel(t *testing.T) {
	base := BotConfig{ID: "bot-a", ProactiveReplyExtraCriteria: "机器人级判据"}.WithDefaults()
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(GroupConfigSet{Groups: []GroupConfig{{BotProfileID: "bot-a", GroupID: "g", ProactiveReplyExtraCriteria: "本群判据"}}})
	cfg := runtime.effectiveConfigForEvent(MessageEvent{Kind: EventKindGroup, ProfileID: "bot-a", GroupID: "g"})
	if got := customRouterCriteria(cfg.ProactiveReplyExtraCriteria); got != "本群判据" {
		t.Fatalf("群级补充判据没覆盖机器人级：%q", got)
	}
}

func TestCustomCriteriaLengthIsCapped(t *testing.T) {
	cfg := BotConfig{Platform: PlatformOneBotV11, ProactiveReplyExtraCriteria: strings.Repeat("规", routerCriteriaMaxRunes+1)}.WithDefaults()
	if err := cfg.Validate(); err == nil {
		t.Fatalf("超长补充判据应该在保存时被拒绝，而不是悄悄进提示词")
	}
	ok := BotConfig{Platform: PlatformOneBotV11, ProactiveReplyExtraCriteria: strings.Repeat("规", routerCriteriaMaxRunes)}.WithDefaults()
	if err := ok.Validate(); err != nil {
		t.Fatalf("刚好到上限被误拒：%v", err)
	}
}
