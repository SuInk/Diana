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
