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
	for _, key := range []string{llm.GroupIntent, llm.GroupReplyAssist, llm.GroupBackground, PurposeReplySendAudit} {
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

// 回复辅助是从后台生成里拆出来的。拆之前给后台生成指过模型的，升级后回复前的
// 那些调用还该用它，不能悄悄换成对话模型。
func TestReplyAssistInheritsBackgroundWhenUnset(t *testing.T) {
	roles := map[string]ModelRole{
		"intent":     {ProfileID: "jev", Model: "jev-latest"},
		"background": {ProfileID: "gemini", Model: "gemini-flash"},
		"chat":       {ProfileID: "terra", Model: "gpt-terra"},
	}
	for _, purpose := range []string{PurposeContextSummary, PurposeReplySemanticDedup, PurposeErrorNotice, PurposeReplyIntentRouter} {
		role, ok := modelRoleFor(roles, purpose, llm.GroupReplyAssist)
		if !ok || role.Model != "gemini-flash" {
			t.Fatalf("%s 没单独配回复辅助时该沿用后台生成：%#v", purpose, role)
		}
	}
}

// 两档各自指定时互不串用：后台那一档慢一点没关系，回复辅助要快。
func TestReplyAssistAndBackgroundBindIndependently(t *testing.T) {
	roles := map[string]ModelRole{
		"background":   {ProfileID: "cheap", Model: "slow-cheap"},
		"reply_assist": {ProfileID: "fast", Model: "fast-flash"},
		"chat":         {ProfileID: "terra", Model: "gpt-terra"},
	}
	if role, _ := modelRoleFor(roles, PurposeSemanticReference, llm.GroupReplyAssist); role.Model != "fast-flash" {
		t.Fatalf("语义指代该走回复辅助：%#v", role)
	}
	if role, _ := modelRoleFor(roles, PurposeMemoryExtract, llm.GroupBackground); role.Model != "slow-cheap" {
		t.Fatalf("记忆抽取该走后台生成：%#v", role)
	}
	// 后台生成不往回找回复辅助。
	delete(roles, "background")
	if role, _ := modelRoleFor(roles, PurposeMemoryExtract, llm.GroupBackground); role.Model != "gpt-terra" {
		t.Fatalf("后台生成没配时该跟随对话，不该借用回复辅助：%#v", role)
	}
}

// 回复辅助显式选了「跟随对话」，就不再往后台生成那一档找。
func TestReplyAssistFollowChatStopsInheritance(t *testing.T) {
	roles := map[string]ModelRole{
		"background":   {ProfileID: "gemini", Model: "gemini-flash"},
		"reply_assist": {FollowChat: true},
		"chat":         {ProfileID: "terra", Model: "gpt-terra"},
	}
	if role, _ := modelRoleFor(roles, PurposeContextSummary, llm.GroupReplyAssist); role.Model != "gpt-terra" {
		t.Fatalf("选了跟随对话该用对话模型：%#v", role)
	}
	if hasDedicatedModelRole(roles, PurposeSemanticTextRef, llm.GroupReplyAssist) {
		t.Fatal("跟随对话不算单独指定")
	}
	delete(roles, "reply_assist")
	if !hasDedicatedModelRole(roles, PurposeSemanticTextRef, llm.GroupReplyAssist) {
		t.Fatal("沿用后台生成那一档算单独指定")
	}
}
