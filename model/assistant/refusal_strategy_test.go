// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"testing"
)

// 拒答话术的核心风险不在「拒绝」本身，而在「说明为什么拒绝」：群里一句
// 「这个话题涉及敏感政治，我不方便讲」把触发点原样复述了一遍，比闭嘴还危险。
//
// 这几条测试盯住的是提示词里那条阶梯不被顺手删掉或改软。字符串匹配确实脆，
// 但这里要防的正是「有人重写提示词时把这一段整段丢了」——那种改动不会让任何
// 别的测试变红，线上表现是拒答重新开始点名敏感内容，而且要等出事才发现。

// TestRefusalPromptKeepsEscalationLadder 盯住四档拒答阶梯都还在，顺序没乱。
func TestRefusalPromptKeepsEscalationLadder(t *testing.T) {
	// ④「不要完全不出声」搬进了四档共用的尾巴（promptRefusalTail），不在这条
	// 阶梯里——它对每一档都成立，由 TestRefusalPromptLeavesSilenceToRuntime 盯着。
	ladder := []string{
		"①先试着把话改成能发的",
		"②改不动就看是什么性质",
		"③如果不能答的原因本身敏感",
	}
	previous := -1
	for _, step := range ladder {
		index := strings.Index(refusalStrategyPrompt(RefusalStrategySmart), step)
		if index < 0 {
			t.Fatalf("拒答提示词缺少这一档：%s", step)
		}
		if index <= previous {
			t.Fatalf("拒答阶梯顺序乱了，%q 出现在前一档之前", step)
		}
		previous = index
	}
	if !strings.Contains(refusalStrategyPrompt(RefusalStrategySmart), "能停在前一档就不要往后走") {
		t.Fatal("拒答提示词没有说明这是一条阶梯，模型会把四档当并列选项")
	}
}

// TestRefusalPromptForbidsNamingSensitiveTrigger 是这次改动真正要保住的那一条。
//
// 允许模型说明拒绝原因是可以的——能力不足、超出权限、不想聊都无所谓。不能允许
// 的是它把「因为涉及某某敏感内容所以不能说」讲出来：那句话本身就是要拦的内容。
func TestRefusalPromptForbidsNamingSensitiveTrigger(t *testing.T) {
	for _, want := range []string{
		"不要复述、点名或影射触发拒答的具体内容",
		"不要解释「因为涉及什么所以不能说」",
		"那句解释本身就是风险",
	} {
		if !strings.Contains(refusalStrategyPrompt(RefusalStrategySmart), want) {
			t.Fatalf("拒答提示词丢了这条约束：%s", want)
		}
	}
}

// TestRefusalPromptLeavesSilenceToRuntime 盯住「完全不回应」不是模型的选项。
//
// 累计拒答到阈值后暂停响应是运行时的事（见 reply_suppression.go）。让模型也能
// 自己决定沉默，就有两套互相看不见的静默逻辑，事件记录上分不清是谁干的。
func TestRefusalPromptLeavesSilenceToRuntime(t *testing.T) {
	if !strings.Contains(refusalStrategyPrompt(RefusalStrategySmart), "连续拒答多次后是否暂停响应由运行时自己决定") {
		t.Fatal("拒答提示词没有把暂停响应的决定权划给运行时")
	}
	if !strings.Contains(refusalStrategyPrompt(RefusalStrategySmart), "你不能直接触发") {
		t.Fatal("拒答提示词没有禁止模型自己触发账号处置")
	}
}

// TestAuditCountsRefusalWithoutStatedReason 是上面那条阶梯的必要配套。
//
// 提示词现在要求敏感原因一律模糊带过，于是「明确不答但一个字理由都没给」成了
// 拒答的常态。审核器原先的定义是「拒绝并给出拒绝说明」——照那个定义，风险最高
// 的那类拒答恰好一条都数不到，三次暂停响应也就永远不会触发。
func TestAuditCountsRefusalWithoutStatedReason(t *testing.T) {
	if !strings.Contains(proactiveReplyQualityPrompt, "不说原因的模糊拒答同样算 true") {
		t.Fatal("审核提示词没有把模糊拒答算成拒答，三次阈值会数不到")
	}
	if !strings.Contains(proactiveReplyQualityPrompt, "不能因此判成 false") {
		t.Fatal("审核提示词没有说明「没给理由」不是判 false 的理由")
	}
	// 按①改写后发出去的回复不是拒答：它给了对方能用的东西，不该占用暂停响应的额度。
	if !strings.Contains(proactiveReplyQualityPrompt, "换个说法后给出的回答") {
		t.Fatal("审核提示词没有把「改写后回答」排除出拒答")
	}
}

// TestRefusalPromptIsInjectedIntoSystemPrompt 确认这段话真的进了系统提示词。
//
// 提示词常量写得再对，没拼进去就是白写；这条在 runtime 那边只有一行
// builder.WriteString，删掉不会有任何编译错误。
func TestRefusalPromptIsInjectedIntoSystemPrompt(t *testing.T) {
	runtime := NewRuntime(DefaultBotConfig(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	prompt := runtime.systemPrompt(MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"}, nil)
	if !strings.Contains(prompt, "①先试着把话改成能发的") {
		t.Fatal("系统提示词里没有拒答阶梯")
	}
}

// TestRefusalStrategyNormalizesUnknownToSmart 盯住空值和脏值都落到智能档。
//
// 存量配置里这个字段是空的，归一化要是漏了，拒答规则整段拼不出来——不是报错，
// 是系统提示词里静悄悄少一段，线上表现为拒答行为退回没有任何约束的状态。
func TestRefusalStrategyNormalizesUnknownToSmart(t *testing.T) {
	for _, input := range []RefusalStrategy{"", "nope", "SMART"} {
		if got := normalizeRefusalStrategy(input); got != RefusalStrategySmart {
			t.Fatalf("normalizeRefusalStrategy(%q) = %q，应该退回智能档", input, got)
		}
	}
	if DefaultBotConfig().RefusalStrategy != RefusalStrategySmart {
		t.Fatal("默认配置的拒答策略不是智能档")
	}
	// 空值经过 WithDefaults 也要补成智能档：存量配置就是这条路进来的。
	if got := (BotConfig{}).WithDefaults().RefusalStrategy; got != RefusalStrategySmart {
		t.Fatalf("WithDefaults 后的拒答策略 = %q", got)
	}
}

// TestEachRefusalStrategyProducesItsOwnRule 盯住四档真的不一样。
//
// 每档都要带上共用的头尾，各自的正文互不相同。少了这条，某一档写错常量指向
// 另一档也不会有人发现——两个档位表现一样，配置形同虚设。
func TestEachRefusalStrategyProducesItsOwnRule(t *testing.T) {
	bodies := map[RefusalStrategy]string{
		RefusalStrategySmart:   "①先试着把话改成能发的",
		RefusalStrategyRewrite: "优先把话改成能发的",
		RefusalStrategyExplain: "直接把原因说清楚",
		RefusalStrategyVague:   "任何情况下都不要交代原因",
	}
	seen := map[string]RefusalStrategy{}
	for strategy, marker := range bodies {
		prompt := refusalStrategyPrompt(strategy)
		if !strings.Contains(prompt, marker) {
			t.Fatalf("%q 档没有自己的正文，缺少 %q", strategy, marker)
		}
		if !strings.Contains(prompt, "拒绝回答任何一条消息") || !strings.Contains(prompt, "不要完全不出声") {
			t.Fatalf("%q 档丢了共用的头或尾", strategy)
		}
		if other, dup := seen[prompt]; dup {
			t.Fatalf("%q 和 %q 拼出来的规则一模一样", strategy, other)
		}
		seen[prompt] = strategy
	}
}

// TestNonExplainStrategiesNeverNameTheTrigger 是这次配置化最不能破的一条。
//
// 「说明原因」是用户明确选的，说了就说了。除它以外的三档都必须带着「不要复述、
// 点名或影射」——智能档会走到模糊拒答，改写档兜底也是模糊拒答，模糊档更不用说。
func TestNonExplainStrategiesNeverNameTheTrigger(t *testing.T) {
	for _, strategy := range []RefusalStrategy{RefusalStrategySmart, RefusalStrategyRewrite, RefusalStrategyVague} {
		prompt := refusalStrategyPrompt(strategy)
		for _, want := range []string{
			"不要复述、点名或影射触发拒答的具体内容",
			"不要解释「因为涉及什么所以不能说」",
		} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("%q 档缺少约束：%s", strategy, want)
			}
		}
	}
}

// TestSystemPromptFollowsConfiguredRefusalStrategy 确认配置真的传到了提示词里。
//
// 常量拼得再对，runtime 那边只有一行 WriteString；写死成某一档不会有编译错误，
// 表现是 WebUI 上怎么选都没用。
func TestSystemPromptFollowsConfiguredRefusalStrategy(t *testing.T) {
	event := MessageEvent{Kind: EventKindGroup, GroupID: "g1", UserID: "1"}
	for strategy, marker := range map[RefusalStrategy]string{
		RefusalStrategyExplain: "直接把原因说清楚",
		RefusalStrategyVague:   "任何情况下都不要交代原因",
	} {
		cfg := BotConfig{RefusalStrategy: strategy}.WithDefaults()
		runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
		prompt := runtime.systemPrompt(event, nil)
		if !strings.Contains(prompt, marker) {
			t.Fatalf("系统提示词没有跟随 %q 档", strategy)
		}
		if strategy != RefusalStrategySmart && strings.Contains(prompt, "①先试着把话改成能发的") {
			t.Fatalf("%q 档的提示词里混进了智能档的阶梯", strategy)
		}
	}
}
