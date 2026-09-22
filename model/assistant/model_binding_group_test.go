// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 意图识别那一档现在只剩能接判断模型的几个判定；写成段文字的后台生成单独成档，
// 不能被意图识别上的判断模型捎带上。
func TestBackgroundGroupSplitsFromIntent(t *testing.T) {
	roles := map[string]ModelRole{
		"intent":     {ProfileID: "jev", Model: "jev-latest"},
		"background": {ProfileID: "gemini", Model: "gemini-flash"},
		"chat":       {ProfileID: "terra", Model: "gpt-terra"},
	}
	role, ok := modelRoleFor(roles, PurposeReplySendAudit, llm.GroupIntent)
	if !ok || role.Model != "jev-latest" {
		t.Fatalf("发送前审核该走意图识别：%#v", role)
	}
	role, ok = modelRoleFor(roles, PurposeMemoryExtract, llm.GroupBackground)
	if !ok || role.Model != "gemini-flash" {
		t.Fatalf("记忆抽取该走后台：%#v", role)
	}
	role, ok = modelRoleFor(roles, PurposeRelationshipEvaluate, llm.GroupBackground)
	if !ok || role.Model != "gemini-flash" {
		t.Fatalf("好感度评估该走后台：%#v", role)
	}
}

// 后台生成没单独配时跟随对话——和界面上其余几档的默认一致。
//
// 这条是刻意的行为变化：拆分之前这些调用跟着 intent 跑。升级之后想保持原样，
// 就把「后台生成」显式指到原来那一档。
func TestBackgroundFollowsChatWhenUnset(t *testing.T) {
	roles := map[string]ModelRole{
		"intent": {ProfileID: "jev", Model: "jev-latest"},
		"chat":   {ProfileID: "terra", Model: "gpt-terra"},
	}
	for _, purpose := range []string{PurposeMemoryExtract, PurposeRelationshipEvaluate, PurposeContextSummary, PurposeRSSWatchJudge} {
		role, ok := modelRoleFor(roles, purpose, llm.GroupBackground)
		if !ok || role.Model != "gpt-terra" {
			t.Fatalf("%s 没单独配时该跟随对话：%#v", purpose, role)
		}
	}
}

// 单独绑某个用途仍然最优先。
func TestPurposeBindingBeatsGroup(t *testing.T) {
	roles := map[string]ModelRole{
		"intent":                    {ProfileID: "jev", Model: "jev-latest"},
		"background":                {ProfileID: "gemini", Model: "gemini-flash"},
		PurposeProactiveReplyRouter: {ProfileID: "terra", Model: "gpt-terra"},
	}
	role, ok := modelRoleFor(roles, PurposeProactiveReplyRouter, llm.GroupIntent)
	if !ok || role.Model != "gpt-terra" {
		t.Fatalf("单独绑的用途该最优先：%#v", role)
	}
}

// 两档都要是合法的可绑定键，否则界面存进来会被当成未知用途拒掉。
func TestGroupKeysAreBindable(t *testing.T) {
	keys := ModelBindingKeys()
	found := map[string]bool{}
	for _, key := range keys {
		found[key] = true
	}
	for _, key := range []string{llm.GroupIntent, llm.GroupBackground, PurposeReplySendAudit} {
		if !found[key] || !isModelBindingKey(key) {
			t.Fatalf("%s 不在可绑定键里：%v", key, keys)
		}
	}
}

// 归属关系要能回答「不单独配的话跟着谁」，界面靠它写提示。
func TestBackgroundPurposesReportTheirGroup(t *testing.T) {
	for _, purpose := range []string{PurposeMemoryExtract, PurposeMemorySummary, PurposeRelationshipEvaluate} {
		if got := ModelBindingGroupOf(purpose); got != llm.GroupBackground {
			t.Fatalf("%s 归属 = %q", purpose, got)
		}
	}
	for _, purpose := range []string{PurposeProactiveReplyRouter, PurposeReplySendAudit} {
		if got := ModelBindingGroupOf(purpose); got != llm.GroupIntent {
			t.Fatalf("%s 归属 = %q", purpose, got)
		}
	}
}
