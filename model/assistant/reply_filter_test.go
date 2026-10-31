// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestApplyReplyFiltersDropsUnclosedBracketTail(t *testing.T) {
	rules := []string{`（[^）\n]*$`}
	got := applyReplyFilters(rules, "这叫声控开关（摔一个来一个，百试百灵\n全平台都卡（历史一多就掉帧")
	if got != "这叫声控开关\n全平台都卡" {
		t.Fatalf("filtered = %q", got)
	}
	if kept := applyReplyFilters(rules, "先看日志（在 logs 目录里）再说"); kept != "先看日志（在 logs 目录里）再说" {
		t.Fatalf("closed bracket must stay, got %q", kept)
	}
}

func TestApplyReplyFiltersSkipsInvalidRules(t *testing.T) {
	if got := applyReplyFilters([]string{"(", "喵"}, "好耶喵"); got != "好耶" {
		t.Fatalf("filtered = %q", got)
	}
}

func TestValidateReplyFilterRules(t *testing.T) {
	if err := validateReplyFilterRules([]string{`（[^）]*$`}); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	for _, rule := range []string{"(", "x*"} {
		if err := validateReplyFilterRules([]string{rule}); err == nil {
			t.Fatalf("rule %q should be rejected", rule)
		}
	}
	cfg := DefaultBotConfig()
	cfg.ReplyFilterRules = []string{"[unclosed"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "回复过滤规则") {
		t.Fatalf("Validate() = %v, want reply filter error", err)
	}
}

func TestPrepareGeneratedReplyAppliesReplyFilters(t *testing.T) {
	r := &Runtime{}
	cfg := DefaultBotConfig()
	cfg.ReplyFilterRules = []string{`（[^）\n]*$`}
	got, err := r.prepareGeneratedReply(context.Background(), cfg, "还得再熬一天（正好先测稳当")
	if err != nil {
		t.Fatalf("prepareGeneratedReply() error = %v", err)
	}
	if strings.Contains(got, "（") || !strings.Contains(got, "还得再熬一天") {
		t.Fatalf("reply = %q", got)
	}

	cfg.ReplyFilterRules = []string{`^.*$`}
	if _, err := r.prepareGeneratedReply(context.Background(), cfg, "整句都会被删"); !errors.Is(err, errModelSilentFinish) {
		t.Fatalf("fully filtered reply error = %v, want silent finish", err)
	}
}

func TestReplyFilterRulesDefaultIsEditable(t *testing.T) {
	var unset BotConfig
	if got := unset.WithDefaults().ReplyFilterRules; len(got) != 1 || got[0] != defaultReplyFilterRules()[0] {
		t.Fatalf("unset rules = %#v, want default", got)
	}
	cleared := BotConfig{ReplyFilterRules: []string{}}
	if got := cleared.WithDefaults().ReplyFilterRules; got == nil || len(got) != 0 {
		t.Fatalf("cleared rules = %#v, want empty list kept", got)
	}
	data, err := json.Marshal(cleared.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	var reloaded BotConfig
	if err := json.Unmarshal(data, &reloaded); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.WithDefaults().ReplyFilterRules; got == nil || len(got) != 0 {
		t.Fatalf("cleared rules after reload = %#v", got)
	}
	got := applyReplyFilters(defaultReplyFilterRules(), "嗯，那就好 [DIANA_REPLY_SINGLE]\n分开发[[DAIAN_LINES_COMPACT]]吧\n括号照发（")
	if got != "嗯，那就好\n分开发吧\n括号照发（" {
		t.Fatalf("default rule filtered = %q", got)
	}
}

func TestPreviewReplyFiltersReportsEachRule(t *testing.T) {
	got, steps := PreviewReplyFilters([]string{`喵$`, "(", `^\[x\]`}, "[x]好耶喵\n再见")
	if got != "好耶\n再见" {
		t.Fatalf("result = %q", got)
	}
	if len(steps) != 3 || len(steps[0].Removed) != 1 || steps[0].Removed[0] != "喵" {
		t.Fatalf("steps = %#v", steps)
	}
	if steps[1].Error == "" || len(steps[2].Removed) != 1 || steps[2].Removed[0] != "[x]" {
		t.Fatalf("steps = %#v", steps)
	}
}

func TestApplyReplyFiltersKeepsMarkdown(t *testing.T) {
	rules := []string{`（[^（）\n]*$`}
	reply := "## 结论\n\n- **先重启**（试过了  \n- 看 [文档](https://example.com/a_(b))\n\n```go\nfmt.Println(\"（没闭合\")\n```\n用 `echo （` 测（顺手"
	want := "## 结论\n\n- **先重启**\n- 看 [文档](https://example.com/a_(b))\n\n```go\nfmt.Println(\"（没闭合\")\n```\n用 `echo （` 测"
	if got := applyReplyFilters(rules, reply); got != want {
		t.Fatalf("filtered =\n%q\nwant\n%q", got, want)
	}
	// 没被规则删到的行，连行尾表示硬换行的两个空格都不动。
	if got := applyReplyFilters(rules, "第一行  \n第二行（没闭合"); got != "第一行  \n第二行" {
		t.Fatalf("untouched line changed: %q", got)
	}
	// 规则跨过行内代码时只删普通文字；分条标记两边各算一条，`$` 不会吞掉下一条。
	if got := applyReplyFilters(rules, "跑一下（`make test` 看看<diana-message/>下一条"); got != "跑一下`make test`<diana-message/>下一条" {
		t.Fatalf("protected spans changed: %q", got)
	}
	got, steps := PreviewReplyFilters(rules, "跑一下（`make test` 看看")
	if got != "跑一下`make test`" || len(steps) != 1 || strings.Join(steps[0].Removed, "|") != "（| 看看" {
		t.Fatalf("preview = %q, %#v", got, steps)
	}
}
