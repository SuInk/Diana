// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func bindingRole(model string) ModelRole {
	return ModelRole{Group: llm.GroupChat, Model: model}
}

// 用途优先于分组：单独给某个用途指了模型，就该盖过它所属分组的选择。
func TestPurposeBindingOverridesItsGroup(t *testing.T) {
	roles := normalizeModelRoles(map[string]ModelRole{
		"intent":                    bindingRole("cheap-router"),
		PurposeRelationshipEvaluate: bindingRole("careful-judge"),
	})

	role, ok := modelRoleFor(roles, PurposeRelationshipEvaluate, llm.GroupIntent)
	if !ok || role.Model != "careful-judge" {
		t.Fatalf("用途覆盖没生效: role=%#v ok=%v", role, ok)
	}
	// 同一分组里没单独绑的用途，仍然跟着分组走。
	role, ok = modelRoleFor(roles, PurposeSemanticReference, llm.GroupIntent)
	if !ok || role.Model != "cheap-router" {
		t.Fatalf("未覆盖的用途没落到分组绑定: role=%#v ok=%v", role, ok)
	}
}

// 本次调用的分组要压过用途表。最典型的是 reply：平时走对话分组，这一轮带图时
// 调用点会切到 vision，静态表里写死的 chat 不能把它盖回去。
func TestCallSiteGroupBeatsThePurposeTable(t *testing.T) {
	roles := normalizeModelRoles(map[string]ModelRole{
		"chat":   bindingRole("chat-model"),
		"vision": bindingRole("vision-model"),
	})
	if ModelBindingGroupOf(PurposeReply) != "chat" {
		t.Fatalf("这条用例的前提变了：reply 不再归属 chat")
	}

	role, ok := modelRoleFor(roles, PurposeReply, llm.GroupVision)
	if !ok || role.Model != "vision-model" {
		t.Fatalf("带图那轮该用视觉模型，实际 role=%#v ok=%v", role, ok)
	}
}

// 调用分组没有绑定时，才轮到用途归属的分组兜底。
func TestPurposeGroupOnlyFillsInWhenCallSiteGroupHasNoBinding(t *testing.T) {
	roles := normalizeModelRoles(map[string]ModelRole{
		"chat":   bindingRole("expensive-chat"),
		"intent": bindingRole("cheap-router"),
	})

	// embedding 分组没绑定，记忆抽取归属 intent，于是落到 intent 而不是 chat。
	role, ok := modelRoleFor(roles, PurposeMemoryExtract, llm.GroupEmbedding)
	if !ok || role.Model != "cheap-router" {
		t.Fatalf("该落到用途归属的 intent，实际 role=%#v ok=%v", role, ok)
	}
}

// 没有用途时按调用分组找，找不到再落到 chat——这是旧 modelRoleForGroup 的语义，
// 不能因为加了用途层就改掉。
func TestGroupLookupWithoutPurposeKeepsOldBehaviour(t *testing.T) {
	roles := normalizeModelRoles(map[string]ModelRole{"chat": bindingRole("chat-model")})

	if role, ok := modelRoleForGroup(roles, llm.GroupVision); !ok || role.Model != "chat-model" {
		t.Fatalf("视觉没绑定时该落到 chat: role=%#v ok=%v", role, ok)
	}
	if role, ok := modelRoleForGroup(roles, llm.GroupChat); !ok || role.Model != "chat-model" {
		t.Fatalf("default 分组该映射到 chat 键: role=%#v ok=%v", role, ok)
	}
}

// 17 个用途每个都要能当绑定键；分组键也要在。少一个就意味着那个用途没法单独配。
func TestEveryPurposeIsBindable(t *testing.T) {
	keys := ModelBindingKeys()
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			t.Fatalf("绑定键重复: %q", key)
		}
		seen[key] = true
		if !isModelBindingKey(key) {
			t.Fatalf("ModelBindingKeys 给出的 %q 不被 normalizeModelRoles 接受", key)
		}
	}
	for _, group := range modelBindingGroups {
		if !seen[modelRoleKeyForGroup(group)] {
			t.Fatalf("分组 %q 不在可绑定键里", group)
		}
	}
	for purpose := range llmPurposeGroup {
		if !seen[purpose] {
			t.Fatalf("用途 %q 不在可绑定键里", purpose)
		}
		if ModelBindingGroupOf(purpose) == "" {
			t.Fatalf("用途 %q 没有归属分组", purpose)
		}
	}
	// 不认识的键仍然要被拒绝，免得配置里的错别字被静默接受。
	if isModelBindingKey("nonexistent_purpose") {
		t.Fatal("未知键不该被接受")
	}
	if got := normalizeModelRoles(map[string]ModelRole{"nonexistent_purpose": bindingRole("m")}); got != nil {
		t.Fatalf("未知键被写进了绑定表: %#v", got)
	}
}
