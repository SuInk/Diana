// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// 生图、语音这类调用失败也进冷却：单次超时按模型请求超时记，成功就清掉；不是候选
// 自己的问题（审核拒绝、调用方已经取消）不记。
func TestNoteRouteCallFeedsCooldowns(t *testing.T) {
	r := &Runtime{}
	profile := llm.Profile{ID: "img", Config: llm.ProviderConfig{Model: "gpt-image"}}
	cooling := func() bool { return r.llmCooldowns.snapshot([]llm.Profile{profile})[0].remaining > 0 }
	ctx := context.Background()
	timeout := fmt.Errorf("image request: %w", context.DeadlineExceeded)
	for range 3 {
		r.noteRouteCall(ctx, llm.GroupImage, profile, timeout, false)
	}
	if !cooling() {
		t.Fatal("repeated image timeouts must put the route into cooldown")
	}
	r.noteRouteCall(ctx, llm.GroupImage, profile, nil, false)
	if cooling() {
		t.Fatal("a successful call must clear the cooldown")
	}
	for range 3 {
		r.noteRouteCall(ctx, llm.GroupImage, profile, errors.New("moderation_blocked"), true)
	}
	if cooling() {
		t.Fatal("content rejections are not the route's fault")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	for range 3 {
		r.noteRouteCall(canceled, llm.GroupImage, profile, timeout, false)
	}
	if cooling() {
		t.Fatal("a canceled turn must not cool the route down")
	}
}

// 智能切换据冷却表把冷却中的生图候选挪到后面：以前生图只给主备，主模型挂了每张图都
// 先在它那里等满超时。
func TestSmartImageRoutingSkipsCoolingRoute(t *testing.T) {
	set := llm.ProfileSet{Profiles: []llm.Profile{
		{ID: "a", Name: "a", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "img-a", APIKey: "k"}},
		{ID: "b", Name: "b", Config: llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, Model: "img-b", APIKey: "k"}},
	}}.WithDefaults()
	roles := map[string]ModelRole{llm.GroupImage: {ProfileID: "a", Model: "img-a", Fallbacks: []ModelRole{{ProfileID: "b", Model: "img-b"}}}}
	r := NewRuntime(BotConfig{}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	ctx := context.Background()
	for range 3 {
		r.noteRouteCall(ctx, llm.GroupImage, imageRouteProfile(set.Profiles[0].Config), fmt.Errorf("upstream: %w", context.DeadlineExceeded), false)
	}
	profiles, err := r.scheduledRoleProfiles(ctx, "image", set, llm.GroupImage, normalizeModelRoles(roles))
	if err != nil || len(profiles) != 2 || profiles[0].ID != "b" {
		t.Fatalf("profiles=%v err=%v; the cooling primary must move behind the fallback", profiles, err)
	}
}
