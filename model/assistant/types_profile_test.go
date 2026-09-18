// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import "testing"

// 卡片开关只该影响目标机器人：停用一台，其他机器人的启用状态原样保留。
func TestWithProfileEnabledOnlyTouchesTarget(t *testing.T) {
	set := ProfileSet{
		ActiveID: "a",
		Profiles: []BotConfig{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}, {ID: "c", Enabled: false}},
	}
	next, ok := set.WithProfileEnabled("b", false)
	if !ok {
		t.Fatal("WithProfileEnabled(b) = false, want true")
	}
	states := map[string]bool{}
	for _, profile := range next.Profiles {
		states[profile.ID] = profile.Enabled
	}
	if states["a"] != true || states["b"] != false || states["c"] != false {
		t.Fatalf("states = %+v, want a=true b=false c=false", states)
	}
	// 原配置集不被改动。
	if set.Profiles[1].Enabled != true {
		t.Fatal("original set was mutated")
	}
	if _, ok := set.WithProfileEnabled("missing", true); ok {
		t.Fatal("missing profile should return ok=false")
	}
}

// 批量开关把全部机器人统一置为同一状态，且不改动原配置集。
func TestWithAllProfilesEnabled(t *testing.T) {
	set := ProfileSet{
		ActiveID: "a",
		Profiles: []BotConfig{{ID: "a", Enabled: true}, {ID: "b", Enabled: false}, {ID: "c", Enabled: true}},
	}
	next := set.WithAllProfilesEnabled(false)
	for _, profile := range next.Profiles {
		if profile.Enabled {
			t.Fatalf("profile %q still enabled after disable-all", profile.ID)
		}
	}
	next = set.WithAllProfilesEnabled(true)
	for _, profile := range next.Profiles {
		if !profile.Enabled {
			t.Fatalf("profile %q still disabled after enable-all", profile.ID)
		}
	}
	if !set.Profiles[0].Enabled || set.Profiles[1].Enabled {
		t.Fatal("original set was mutated")
	}
}
