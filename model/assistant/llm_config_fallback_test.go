// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func testProfileInGroup(group string) llm.Profile {
	return llm.Profile{ID: "picked", Name: "Picked", Group: group}
}

func modelSwitchRuntimeWithFallbacks(t *testing.T, chat ModelRole) (*Runtime, *scopedRoleTestSaver, MessageEvent) {
	t.Helper()
	r, saver, _, event := modelSwitchTestRuntime(t)
	bot := saver.configs["b"]
	roles := map[string]ModelRole{}
	for key, role := range bot.ModelRoles {
		roles[key] = role
	}
	roles["chat"] = chat
	bot.ModelRoles = roles
	saver.configs["b"] = bot
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{saver.configs["a"], bot}})
	return r, saver, event
}

// 主人在聊天里只说「换个模型」，没点名供应商，那就只是换模型。后备路由是在
// WebUI 里一条条排出来的，不能被这句话顺手清掉，顺序也不能变。
func TestChatModelSwitchKeepsFallbackRoutes(t *testing.T) {
	fallbacks := []ModelRole{{ProfileID: "two", Model: "shared"}, {ProfileID: "one", Model: "shared"}}
	r, saver, event := modelSwitchRuntimeWithFallbacks(t, ModelRole{ProfileID: "one", Model: "shared", Fallbacks: fallbacks})
	reply, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"model": "other"})
	if err != nil {
		t.Fatal(err)
	}
	saved := saver.configs["b"].ModelRoles["chat"]
	if saved.ProfileID != "one" || saved.Model != "other" {
		t.Fatalf("primary route not switched: %+v", saved)
	}
	if len(saved.Fallbacks) != 2 || saved.Fallbacks[0].ProfileID != "two" || saved.Fallbacks[1].ProfileID != "one" {
		t.Fatalf("fallback routes lost or reordered: %+v", saved.Fallbacks)
	}
	if strings.Contains(reply, "后备路由已被") {
		t.Fatalf("reply claims a replacement that did not happen: %s", reply)
	}
	// 运行态也要拿到同一份，否则下一次调用还按旧的走。
	if live := r.effectiveConfigForEvent(event).ModelRoles["chat"]; len(live.Fallbacks) != 2 {
		t.Fatalf("runtime lost the fallback routes: %+v", live)
	}
}

// 点名换到另一家供应商时，原有的后备路由确实会作废——那是设计好的，但必须在
// 回执里说出来，不能等主模型挂了才发现没有兜底。
func TestCrossProviderSwitchReportsDroppedFallbacks(t *testing.T) {
	fallbacks := []ModelRole{{ProfileID: "two", Model: "shared"}}
	r, saver, event := modelSwitchRuntimeWithFallbacks(t, ModelRole{ProfileID: "one", Model: "shared", Fallbacks: fallbacks})
	reply, err := newDianaLLMConfigTool(r, event).Run(context.Background(), map[string]any{"provider_id": "two", "model": "other"})
	if err != nil {
		t.Fatal(err)
	}
	saved := saver.configs["b"].ModelRoles["chat"]
	if saved.ProfileID != "two" || len(saved.Fallbacks) != 0 {
		t.Fatalf("cross-provider switch should rebind to a single provider: %+v", saved)
	}
	if !strings.Contains(reply, "1 条后备路由已被这次绑定替换") {
		t.Fatalf("dropped fallbacks not reported: %s", reply)
	}
}

// 分组绑定那一档本来就带故障转移，只换模型时保持不变，也不该报「替换了后备」。
func TestGroupBoundRoleKeepsGroupAndReportsNothing(t *testing.T) {
	role, replaced := nextModelRole(ModelRole{Group: "chat", Model: "shared"}, testProfileInGroup("chat"), "other", false)
	if role.Group != "chat" || role.Model != "other" || replaced {
		t.Fatalf("group binding not preserved: %+v replaced=%t", role, replaced)
	}
	// 同一档改成点名某家供应商，分组绑定作废，这件事要报出来。
	role, replaced = nextModelRole(ModelRole{Group: "chat", Model: "shared"}, testProfileInGroup("chat"), "other", true)
	if role.Group != "" || role.ProfileID != "picked" || !replaced {
		t.Fatalf("explicit provider should replace the group binding: %+v replaced=%t", role, replaced)
	}
}
