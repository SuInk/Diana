// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"strings"
	"testing"
)

// TestGroupProfilesAndReorder 验证对应功能场景。
func TestGroupProfilesAndReorder(t *testing.T) {
	set := ProfileSet{
		Profiles: []Profile{
			{ID: "a", Name: "A", Group: "default", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", Model: "m1"}},
			{ID: "v1", Name: "V1", Group: "vision", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", Model: "v-a"}},
			{ID: "b", Name: "B", Group: "", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", Model: "m2"}},
			{ID: "v2", Name: "V2", Group: "vision", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", Model: "v-b"}},
		},
	}
	vision := set.GroupProfiles("vision")
	if len(vision) != 2 || vision[0].ID != "v1" || vision[1].ID != "v2" {
		t.Fatalf("vision group = %+v", vision)
	}
	// 空分组归入 default。
	if chat := set.GroupProfiles("default"); len(chat) != 2 || chat[1].ID != "b" {
		t.Fatalf("default group = %+v", chat)
	}
	if none := set.GroupProfiles("image"); len(none) != 0 {
		t.Fatalf("image group should be empty, got %+v", none)
	}

	// 重排：交换 vision 组两条的顺序，其余保持。
	reordered := set.Reorder([]string{"a", "v2", "b", "v1"})
	ids := []string{}
	for _, profile := range reordered.Profiles {
		ids = append(ids, profile.ID)
	}
	if strings.Join(ids, ",") != "a,v2,b,v1" {
		t.Fatalf("reordered = %v", ids)
	}
	// 部分 ID 列表：命中的按给定顺序排前，未提到的保持原顺序在后。
	partial := set.Reorder([]string{"v2"})
	if partial.Profiles[0].ID != "v2" || len(partial.Profiles) != 4 {
		t.Fatalf("partial reorder = %+v", partial.Profiles)
	}
}

// TestGroupProfilesKeepsListOrder 验证组内候选严格按列表顺序，不受任何隐藏状态影响。
//
// 这里原来测的是 ActiveGroupProfiles：从「激活中」那条开始在组内绕圈。那个概念去掉
// 之后，列表顺序就是唯一的降级顺序——界面上写的「组内顺序即降级优先级」这才是真的。
func TestGroupProfilesKeepsListOrder(t *testing.T) {
	set := ProfileSet{
		Profiles: []Profile{
			{ID: "a", Name: "A", Group: "chat", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key-a", Model: "gp5.5"}},
			{ID: "b", Name: "B", Group: "chat", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key-b", Model: "gp5.5"}},
			{ID: "c", Name: "C", Group: "vision", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key-c", Model: "gp5.5"}},
			{ID: "d", Name: "D", Group: "chat", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key-d", Model: "gp5.5"}},
		},
	}

	got := set.GroupProfiles("chat")
	want := []string{"a", "b", "d"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].ID != want[i] || got[i].Group != "chat" {
			t.Fatalf("got[%d] = %#v, want id=%q group=chat", i, got[i], want[i])
		}
	}
}

// TestFirstProfileIsTheListHead 验证兜底取的是列表第一条，与分组无关。
func TestFirstProfileIsTheListHead(t *testing.T) {
	set := ProfileSet{Profiles: []Profile{
		{ID: "head", Group: "vision", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", Model: "m"}},
		{ID: "tail", Group: "chat", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "k", Model: "m"}},
	}}
	first, ok := set.FirstProfile()
	if !ok || first.ID != "head" {
		t.Fatalf("first = %#v ok=%v", first, ok)
	}
	if _, ok := (ProfileSet{}).FirstProfile(); ok {
		t.Fatal("空配置集不该有兜底配置")
	}
}

// TestProfileSetWithDefaultsAssignsDefaultGroup 验证旧配置会进入默认分组。
func TestProfileSetWithDefaultsAssignsDefaultGroup(t *testing.T) {
	set := ProfileSet{
		Profiles: []Profile{
			{ID: "a", Name: "A", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key-a", Model: "gp5.5"}},
		},
	}.WithDefaults()

	if set.Profiles[0].Group != DefaultProfileGroup {
		t.Fatalf("group = %q, want %q", set.Profiles[0].Group, DefaultProfileGroup)
	}
}
