// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 整体风格从各群笔记归纳：关掉的群和整体风格自己不进材料；群里有自己的笔记用自己的，
// 没学到的群和私聊用整体风格兜底，单独关掉风格的群什么都不带。
func TestGlobalStyleLearnsFromGroupNotesAndFillsGaps(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{"大家都爱说「绷不住了」，一条就一句"}}
	runtime, styles, event := groupStyleTestRuntime(t, 0, provider)
	ctx := context.Background()
	now := time.Now()
	for _, style := range []GroupStyle{
		{ProfileID: "bot", GroupID: "g1", Text: "一群爱说绷不住了", UpdatedAt: now},
		{ProfileID: "bot", GroupID: "g2", Text: "二群也说绷不住了", UpdatedAt: now.Add(-time.Hour)},
		{ProfileID: "bot", GroupID: "g3", Text: "三群关掉了", Disabled: true, UpdatedAt: now},
		{ProfileID: "other", GroupID: "g9", Text: "别的机器人的群", UpdatedAt: now},
	} {
		_ = styles.SaveGroupStyle(ctx, style)
	}

	global, err := runtime.RelearnGroupStyle(ctx, "bot", GlobalStyleGroupID)
	if err != nil || global.GroupID != GlobalStyleGroupID || global.SampleCount != 2 {
		t.Fatalf("global = %#v err=%v", global, err)
	}
	material := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	if !strings.Contains(material, "一群爱说绷不住了") || !strings.Contains(material, "二群也说") || strings.Contains(material, "三群关掉了") || strings.Contains(material, "别的机器人") {
		t.Fatalf("global learning material = %q", material)
	}

	cfg := runtime.effectiveConfigForEvent(event)
	if got := runtime.groupStylePrompt(event, cfg); !strings.Contains(got, "【这个群的说话风格】") || !strings.Contains(got, "一群爱说") {
		t.Fatalf("group with its own note = %q", got)
	}
	fresh := event
	fresh.GroupID = "g-new"
	if got := runtime.groupStylePrompt(fresh, cfg); !strings.Contains(got, "【你平时的说话风格】") || !strings.Contains(got, "大家都爱说") {
		t.Fatalf("new group fallback = %q", got)
	}
	private := MessageEvent{Kind: EventKindPrivate, ProfileID: "bot", UserID: "10001"}
	if got := runtime.groupStylePrompt(private, cfg); !strings.Contains(got, "【你平时的说话风格】") {
		t.Fatalf("private fallback = %q", got)
	}
	disabled := event
	disabled.GroupID = "g3"
	if got := runtime.groupStylePrompt(disabled, cfg); got != "" {
		t.Fatalf("group with style turned off still gets the global style: %q", got)
	}

	if _, _, err := runtime.SetGroupStyleEnabledForProfile(ctx, "bot", GlobalStyleGroupID, false); err != nil {
		t.Fatal(err)
	}
	if got := runtime.groupStylePrompt(fresh, cfg); got != "" {
		t.Fatalf("disabled global style still injected: %q", got)
	}
}

// 只有一个群学到风格时不归纳：那时的「共同点」就是这个群自己的梗。
func TestGlobalStyleNeedsTwoGroups(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{"不该被调用"}}
	runtime, styles, _ := groupStyleTestRuntime(t, 0, provider)
	ctx := context.Background()
	_ = styles.SaveGroupStyle(ctx, GroupStyle{ProfileID: "bot", GroupID: "g1", Text: "只有一个群", UpdatedAt: time.Now()})
	if _, err := runtime.learnGlobalStyle(ctx, "bot"); !errors.Is(err, ErrGlobalStyleNotEnoughGroups) {
		t.Fatalf("err = %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("made %d model calls with one group note", len(provider.requests))
	}
}

// 群消息进来时顺带检查整体风格：过期的自动重新归纳，手动写的不动。
func TestObserveRefreshesStaleGlobalStyleButKeepsManual(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{"重新归纳的整体风格"}}
	runtime, styles, event := groupStyleTestRuntime(t, 0, provider)
	ctx := context.Background()
	now := time.Now()
	_ = styles.SaveGroupStyle(ctx, GroupStyle{ProfileID: "bot", GroupID: "g1", Text: "一群", UpdatedAt: now})
	_ = styles.SaveGroupStyle(ctx, GroupStyle{ProfileID: "bot", GroupID: "g2", Text: "二群", UpdatedAt: now})
	_ = styles.SaveGroupStyle(ctx, GroupStyle{ProfileID: "bot", GroupID: GlobalStyleGroupID, Text: "旧的", UpdatedAt: now.Add(-2 * groupStyleRefreshAfter)})

	runtime.observeGroupStyle(event)
	runtime.groupStyles.learning.Wait()
	if saved, _, _ := styles.GroupStyle(ctx, "bot", GlobalStyleGroupID); saved.Text != "重新归纳的整体风格" {
		t.Fatalf("stale global style not refreshed: %#v", saved)
	}

	_, _, _ = runtime.SaveGroupStyleForProfile(ctx, "bot", GlobalStyleGroupID, "主人写的整体风格")
	manual, _, _ := styles.GroupStyle(ctx, "bot", GlobalStyleGroupID)
	manual.UpdatedAt = now.Add(-2 * groupStyleRefreshAfter)
	_ = styles.SaveGroupStyle(ctx, manual)
	other := event
	other.GroupID = "g2"
	runtime.observeGroupStyle(other)
	runtime.groupStyles.learning.Wait()
	if saved, _, _ := styles.GroupStyle(ctx, "bot", GlobalStyleGroupID); saved.Text != "主人写的整体风格" || len(provider.requests) != 1 {
		t.Fatalf("manual global style overwritten: %#v calls=%d", saved, len(provider.requests))
	}
}
