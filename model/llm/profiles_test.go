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
		ActiveID: "a",
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

// TestProfileSetActiveGroupProfilesRotatesWithinGroup 验证只在当前分组内按顺序轮换。
func TestProfileSetActiveGroupProfilesRotatesWithinGroup(t *testing.T) {
	set := ProfileSet{
		ActiveID: "b",
		Profiles: []Profile{
			{ID: "a", Name: "A", Group: "chat", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key-a", Model: "gp5.5"}},
			{ID: "b", Name: "B", Group: "chat", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key-b", Model: "gp5.5"}},
			{ID: "c", Name: "C", Group: "vision", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key-c", Model: "gp5.5"}},
			{ID: "d", Name: "D", Group: "chat", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key-d", Model: "gp5.5"}},
		},
	}

	got := set.ActiveGroupProfiles()
	want := []string{"b", "d", "a"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].ID != want[i] || got[i].Group != "chat" {
			t.Fatalf("got[%d] = %#v, want id=%q group=chat", i, got[i], want[i])
		}
	}
}

// TestProfileSetWithDefaultsAssignsDefaultGroup 验证旧配置会进入默认分组。
func TestProfileSetWithDefaultsAssignsDefaultGroup(t *testing.T) {
	set := ProfileSet{
		ActiveID: "a",
		Profiles: []Profile{
			{ID: "a", Name: "A", Config: ProviderConfig{Provider: ProviderOpenAICompatible, APIKey: "key-a", Model: "gp5.5"}},
		},
	}.WithDefaults()

	if set.Profiles[0].Group != DefaultProfileGroup {
		t.Fatalf("group = %q, want %q", set.Profiles[0].Group, DefaultProfileGroup)
	}
}
