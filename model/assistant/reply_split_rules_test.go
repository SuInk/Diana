// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"reflect"
	"testing"
)

func TestDefaultReplySplitRuleSplitsAfterDanglingParen(t *testing.T) {
	limits := chatSplitLimits{SplitRules: defaultReplySplitRules()}
	cases := []struct {
		name  string
		reply string
		want  []string
	}{
		{"空格硬接两件事", "10个月7990个commit这还菜？（ 分发卡壳先别怪自己，苹果生态没设备确实玩不转", []string{"10个月7990个commit这还菜？（", "分发卡壳先别怪自己，苹果生态没设备确实玩不转"}},
		{"全角空格", "会吗（　我觉得会", []string{"会吗（", "我觉得会"}},
		{"句尾单独的括号不动", "没绷住（", []string{"没绷住（"}},
		{"收口的括号注释不动", "先装依赖（ 注意要 Node 20 ）再跑", []string{"先装依赖（ 注意要 Node 20 ）再跑"}},
		{"没有空格也拆", "三星用户输麻了，土区 IAP 是 iOS 的路（不过安卓也能走土区 Google Play 结算", []string{"三星用户输麻了，土区 IAP 是 iOS 的路（", "不过安卓也能走土区 Google Play 结算"}},
		{"只跟一个字的语气尾巴不拆", "别当它是生发神器（x", []string{"别当它是生发神器（x"}},
		{"句尾括号后面再挂一句", "确实是祖传艺能（o5.5 至少肯干活（", []string{"确实是祖传艺能（", "o5.5 至少肯干活（"}},
		{"一行里两处", "会吗（ 我觉得会（ 反正试试", []string{"会吗（", "我觉得会（", "反正试试"}},
		{"行内代码不拆", "试试 `a（ b`", []string{"试试 `a（ b`"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitChatReply(tc.reply, limits); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitChatReply(%q) = %#v, want %#v", tc.reply, got, tc.want)
			}
		})
	}
}

func TestReplySplitRulesSkipSingleMessageAndEmptyRules(t *testing.T) {
	reply := "这还菜？（ 分发卡壳先别怪自己"
	single := chatSplitLimits{SingleMessage: true, MarkerOnly: true, SplitRules: defaultReplySplitRules()}
	if got := splitChatReply(reply, single); len(got) != 1 {
		t.Fatalf("一次发完模式不该拆：%#v", got)
	}
	if got := splitChatReply(reply, chatSplitLimits{}); len(got) != 1 {
		t.Fatalf("主人清空规则后不该拆：%#v", got)
	}
}

func TestReplySplitRuleWithoutGroupReplacesWholeMatch(t *testing.T) {
	got := splitChatReply("先这样 // 再说别的", chatSplitLimits{SplitRules: []string{` // `}})
	if want := []string{"先这样", "再说别的"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestReplySplitRulesConfigDefaults(t *testing.T) {
	cfg := BotConfig{}.WithDefaults()
	if !reflect.DeepEqual(cfg.ReplySplitRules, defaultReplySplitRules()) {
		t.Fatalf("没设置过应补默认规则：%#v", cfg.ReplySplitRules)
	}
	cleared := BotConfig{ReplySplitRules: []string{}}.WithDefaults()
	if cleared.ReplySplitRules == nil || len(cleared.ReplySplitRules) != 0 {
		t.Fatalf("主人清空后不应补回：%#v", cleared.ReplySplitRules)
	}
	if err := validateReplySplitRules([]string{`x*`}); err == nil {
		t.Fatal("能匹配空文本的规则应拒绝")
	}
	if err := validateReplySplitRules(defaultReplySplitRules()); err != nil {
		t.Fatalf("默认规则应合法：%v", err)
	}
}
