// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"testing"

	"github.com/SuInk/diana/model/llm"
)

func routeModels(role ModelRole) []string {
	models := []string{}
	for _, route := range modelRoleRouteList(role) {
		name := route.Model
		if route.FollowChatRoute {
			name = "<follow>"
		}
		if route.Standby {
			name += "(standby)"
		}
		models = append(models, name)
	}
	return models
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 「跟随对话」排在独立模型后面：先用自己的，再就地展开成对话那一档的全部候选。
func TestFollowChatRouteExpandsInPlace(t *testing.T) {
	chat := bindingRole("chat-a")
	chat.Fallbacks = []ModelRole{bindingRole("chat-b")}
	tone := bindingRole("tone-own")
	tone.RoutingStrategy = "failover"
	tone.Fallbacks = []ModelRole{{FollowChatRoute: true}}
	roles := normalizeModelRoles(map[string]ModelRole{"chat": chat, PurposeReplyTone: tone})

	role, ok := modelRoleFor(roles, PurposeReplyTone, llm.GroupChat)
	if !ok {
		t.Fatal("没找到回复口吻的绑定")
	}
	if got, want := routeModels(role), []string{"tone-own", "chat-a", "chat-b"}; !sameStrings(got, want) {
		t.Fatalf("展开顺序不对: got=%v want=%v", got, want)
	}
	if role.RoutingStrategy != "failover" {
		t.Fatalf("调度策略应沿用这一档自己的: %q", role.RoutingStrategy)
	}
}

// 「跟随对话」排第一、标成后备的独立模型在后：对话候选在前，后备标记各归各。
func TestFollowChatRouteFirstKeepsOwnStandby(t *testing.T) {
	roles := normalizeModelRoles(map[string]ModelRole{
		"chat": bindingRole("chat-a"),
		PurposeReplyTone: {FollowChatRoute: true, Fallbacks: []ModelRole{
			{Group: llm.GroupChat, Model: "tone-own", Standby: true},
		}},
	})
	if roles[PurposeReplyTone].FollowChat {
		t.Fatal("带独立模型的跟随不该被规范化成整档跟随")
	}
	role, _ := modelRoleFor(roles, PurposeReplyTone, llm.GroupChat)
	if got, want := routeModels(role), []string{"chat-a", "tone-own(standby)"}; !sameStrings(got, want) {
		t.Fatalf("展开结果不对: got=%v want=%v", got, want)
	}
}

// 「跟随对话」这一项标成后备时，展开出的对话候选整体都是后备。
func TestFollowChatRouteStandbyAppliesToExpandedRoutes(t *testing.T) {
	chat := bindingRole("chat-a")
	chat.Fallbacks = []ModelRole{bindingRole("chat-b")}
	tone := bindingRole("tone-own")
	tone.Fallbacks = []ModelRole{{FollowChatRoute: true, Standby: true}}
	roles := normalizeModelRoles(map[string]ModelRole{"chat": chat, PurposeReplyTone: tone})

	role, _ := modelRoleFor(roles, PurposeReplyTone, llm.GroupChat)
	if got, want := routeModels(role), []string{"tone-own", "chat-a(standby)", "chat-b(standby)"}; !sameStrings(got, want) {
		t.Fatalf("后备标记没带到展开的候选上: got=%v want=%v", got, want)
	}
}

// 对话没配好、或这一项停用时跳过它，独立模型照常可用；纯跟随仍报跟随失败。
func TestFollowChatRouteSkippedWhenChatMissingOrDisabled(t *testing.T) {
	tone := bindingRole("tone-own")
	tone.Fallbacks = []ModelRole{{FollowChatRoute: true}}
	roles := normalizeModelRoles(map[string]ModelRole{PurposeReplyTone: tone})
	role, _ := modelRoleFor(roles, PurposeReplyTone, llm.GroupChat)
	if got, want := routeModels(role), []string{"tone-own"}; !sameStrings(got, want) {
		t.Fatalf("对话没配时应只剩独立模型: got=%v", got)
	}

	tone.Fallbacks = []ModelRole{{FollowChatRoute: true, Disabled: true}}
	roles = normalizeModelRoles(map[string]ModelRole{"chat": bindingRole("chat-a"), PurposeReplyTone: tone})
	role, _ = modelRoleFor(roles, PurposeReplyTone, llm.GroupChat)
	if got, want := routeModels(role), []string{"tone-own"}; !sameStrings(got, want) {
		t.Fatalf("停用的跟随项不该展开: got=%v", got)
	}

	roles = normalizeModelRoles(map[string]ModelRole{PurposeReplyTone: {FollowChatRoute: true}})
	role, _ = modelRoleFor(roles, PurposeReplyTone, llm.GroupChat)
	if !role.FollowChat {
		t.Fatalf("纯跟随在对话没配时应原样返回跟随，由调用处报错: %#v", role)
	}
}

// 对话自己和音视频插槽不能跟随对话：混排里的跟随项保存时去掉，独立模型留下。
func TestFollowChatRouteDroppedForChatAndMediaSlots(t *testing.T) {
	chat := bindingRole("chat-a")
	chat.Fallbacks = []ModelRole{{FollowChatRoute: true}}
	tts := ModelRole{FollowChatRoute: true, Fallbacks: []ModelRole{{ProfileID: "speech", Model: "voice-a"}}}
	roles := normalizeModelRoles(map[string]ModelRole{"chat": chat, mediaSlotTTS: tts})

	if got := routeModels(roles["chat"]); !sameStrings(got, []string{"chat-a"}) {
		t.Fatalf("对话里的跟随项没去掉: %v", got)
	}
	if got := routeModels(roles[mediaSlotTTS]); !sameStrings(got, []string{"voice-a"}) {
		t.Fatalf("语音合成里的跟随项没去掉: %v", got)
	}
}

// 一张候选表里「跟随对话」只留一项；纯跟随仍规范化成最简形式。
func TestNormalizeFollowChatRouteDeduplicates(t *testing.T) {
	role := normalizeModelRole(ModelRole{Group: llm.GroupChat, Model: "own", Fallbacks: []ModelRole{
		{FollowChatRoute: true, Weight: 5000, Model: "ignored"},
		{FollowChatRoute: true},
	}})
	if got := routeModels(role); !sameStrings(got, []string{"own", "<follow>"}) {
		t.Fatalf("重复的跟随项没去重: %v", got)
	}
	if role.Fallbacks[0].Weight != 1000 || role.Fallbacks[0].Model != "" {
		t.Fatalf("跟随项应只带启停、权重和后备标记: %#v", role.Fallbacks[0])
	}
	if pure := normalizeModelRole(ModelRole{FollowChatRoute: true, Model: "x", Weight: 3}); pure.Model != "" || pure.Weight != 0 || !pure.FollowChat || pure.FollowChatRoute {
		t.Fatalf("表里只剩跟随一行时应规范化成整档跟随: %#v", pure)
	}
	// 旧配置里整档跟随带着残留后备：照旧丢掉残留，不当成混排。
	if legacy := normalizeModelRole(ModelRole{FollowChat: true, Fallbacks: []ModelRole{{ProfileID: "old", Model: "old"}}}); len(legacy.Fallbacks) != 0 || !legacy.FollowChat {
		t.Fatalf("残留后备被当成了混排: %#v", legacy)
	}
}

// 新模型提到最前时，混排里的跟随项留在原位。
func TestPrioritizeModelRoleKeepsFollowChatRoute(t *testing.T) {
	role := normalizeModelRole(ModelRole{FollowChatRoute: true, Fallbacks: []ModelRole{bindingRole("own")}})
	got := prioritizeModelRole(role, bindingRole("picked"))
	if models := routeModels(got); !sameStrings(models, []string{"picked", "<follow>", "own"}) {
		t.Fatalf("跟随项丢了: %v", models)
	}
}
