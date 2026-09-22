// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

// 绑「判断类」要盖过笼统的 intent 一档，否则这个键形同虚设。
func TestDecisionClassBindingBeatsGroup(t *testing.T) {
	roles := map[string]ModelRole{
		"intent":           {ProfileID: "gemini", Model: "gemini-flash"},
		RoleDecisionJudges: {ProfileID: "jev", Model: "jev-latest"},
		"chat":             {ProfileID: "terra", Model: "gpt-terra"},
	}
	role, ok := modelRoleFor(roles, PurposeReplySendAudit, "intent")
	if !ok || role.Model != "jev-latest" {
		t.Fatalf("发送前审核该走判断类：%#v", role)
	}
	// 要写字的那一拨不能被判断类捎带上。
	role, ok = modelRoleFor(roles, PurposeMemoryExtract, "intent")
	if !ok || role.Model != "gemini-flash" {
		t.Fatalf("记忆抽取不该走判断类：%#v", role)
	}
}

// 单独绑某个用途仍然优先于它所属的那一拨。
func TestPurposeBindingBeatsClass(t *testing.T) {
	roles := map[string]ModelRole{
		"intent":                    {ProfileID: "gemini", Model: "gemini-flash"},
		RoleDecisionJudges:          {ProfileID: "jev", Model: "jev-latest"},
		PurposeProactiveReplyRouter: {ProfileID: "terra", Model: "gpt-terra"},
	}
	role, ok := modelRoleFor(roles, PurposeProactiveReplyRouter, "intent")
	if !ok || role.Model != "gpt-terra" {
		t.Fatalf("单独绑的用途该最优先：%#v", role)
	}
}

// 文本类绑了、判断类没绑时，判断类用途落回 intent，不该掉到文本类上。
func TestTextClassDoesNotCaptureDecisionPurposes(t *testing.T) {
	roles := map[string]ModelRole{
		"intent":       {ProfileID: "gemini", Model: "gemini-flash"},
		RoleTextJudges: {ProfileID: "terra", Model: "gpt-terra"},
	}
	role, ok := modelRoleFor(roles, PurposeReplySendAudit, "intent")
	if !ok || role.Model != "gemini-flash" {
		t.Fatalf("判断类没绑时该回落 intent：%#v", role)
	}
	role, ok = modelRoleFor(roles, PurposeMemoryExtract, "intent")
	if !ok || role.Model != "gpt-terra" {
		t.Fatalf("文本类用途该走文本类绑定：%#v", role)
	}
}

// 两个新键必须是合法的可绑定键，否则界面存进来会被当成未知用途拒掉。
func TestClassKeysAreBindable(t *testing.T) {
	for _, key := range []string{RoleDecisionJudges, RoleTextJudges} {
		if !isModelBindingKey(key) {
			t.Fatalf("%s 不是可绑定键", key)
		}
	}
	keys := ModelBindingKeys()
	found := map[string]bool{}
	for _, key := range keys {
		found[key] = true
	}
	if !found[RoleDecisionJudges] || !found[RoleTextJudges] || !found[PurposeReplySendAudit] {
		t.Fatalf("可绑定键里缺了新加的几个：%v", keys)
	}
}
