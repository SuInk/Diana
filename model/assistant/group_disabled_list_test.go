// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// 控制台关掉的群写在群配置里，「群 列表」和配置快照都要立刻看到，不能只读
// 机器人配置里迁移后就空着的 DisabledGroups。
func TestDisabledGroupListFollowsGroupConfigImmediately(t *testing.T) {
	base := BotConfig{ID: "bot-a", BotAccount: "42", OwnerID: "90001"}
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	store := &testWritableGroupConfigStore{}
	runtime.SetGroupConfigStore(store)
	cfg := runtime.ProfileConfig("bot-a")
	event := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-a", UserID: "90001"}

	if got := runtime.renderDisabledGroups(event); !strings.Contains(got, "当前没有被禁用的群") {
		t.Fatalf("初始列表 = %q", got)
	}
	_, _ = store.SaveGroupConfig(GroupConfig{BotProfileID: "bot-a", GroupID: "30002", Enabled: false, EnabledSet: true}, cfg)
	_, _ = store.SaveGroupConfig(GroupConfig{BotProfileID: "bot-a", GroupID: "30001", Enabled: true, EnabledSet: true}, cfg)
	// 别的机器人在同一个群停用，不算这台的。
	_, _ = store.SaveGroupConfig(GroupConfig{BotProfileID: "bot-b", GroupID: "30001", Enabled: false, EnabledSet: true}, cfg)

	if !runtime.isGroupDisabled("bot-a", "30002") {
		t.Fatal("群配置停用后运行时应立刻按停用处理")
	}
	got := runtime.renderDisabledGroups(event)
	if !strings.Contains(got, "- 30002") || strings.Contains(got, "30001") {
		t.Fatalf("停用后列表 = %q", got)
	}
	if ids := runtime.disabledGroupIDs("bot-a"); !slices.Equal(ids, []string{"30002"}) {
		t.Fatalf("disabledGroupIDs = %v", ids)
	}
	body, _ := json.Marshal(runtime.dianaConfigSnapshot(event))
	if !strings.Contains(string(body), `"disabled_groups":["30002"]`) {
		t.Fatalf("配置快照没带上群配置里的停用群：%s", body)
	}

	_, _ = store.SaveGroupConfig(GroupConfig{BotProfileID: "bot-a", GroupID: "30002", Enabled: true, EnabledSet: true}, cfg)
	if runtime.isGroupDisabled("bot-a", "30002") {
		t.Fatal("重新启用后运行时应立刻恢复")
	}
	if got := runtime.renderDisabledGroups(event); !strings.Contains(got, "当前没有被禁用的群") {
		t.Fatalf("重新启用后列表 = %q", got)
	}
}

func TestDisabledGroupListMentionsNewGroupDefault(t *testing.T) {
	base := BotConfig{ID: "bot-a", GroupAdmission: GroupAdmission{Mode: GroupAdmissionWhitelist}}
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetGroupConfigStore(&testWritableGroupConfigStore{})
	got := runtime.renderDisabledGroups(MessageEvent{Kind: EventKindPrivate, ProfileID: "bot-a"})
	if !strings.Contains(got, "新群默认也不工作") {
		t.Fatalf("白名单模式下应提示新群默认不工作：%q", got)
	}
}
