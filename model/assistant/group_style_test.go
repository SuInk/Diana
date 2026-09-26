// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryGroupStyleStore struct {
	mu     sync.Mutex
	styles map[string]GroupStyle
}

func (s *memoryGroupStyleStore) GroupStyle(_ context.Context, profileID, groupID string) (GroupStyle, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	style, ok := s.styles[groupStyleScopeKey(profileID, groupID)]
	return style, ok, nil
}

func (s *memoryGroupStyleStore) SaveGroupStyle(_ context.Context, style GroupStyle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.styles == nil {
		s.styles = map[string]GroupStyle{}
	}
	s.styles[groupStyleScopeKey(style.ProfileID, style.GroupID)] = style
	return nil
}

func (s *memoryGroupStyleStore) DeleteGroupStyle(_ context.Context, profileID, groupID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.styles, groupStyleScopeKey(profileID, groupID))
	return nil
}

func groupStyleTestRuntime(t *testing.T, messages int, provider *sequenceLLMProvider) (*Runtime, *memoryGroupStyleStore, MessageEvent) {
	t.Helper()
	cfg := BotConfig{ID: "bot", BotAccount: "42", ExpressionLearningEnabled: boolPointer(true)}.WithDefaults()
	runtime := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return provider, nil })
	styles := &memoryGroupStyleStore{}
	runtime.SetGroupStyleStore(styles)
	t.Cleanup(runtime.groupStyles.learning.Wait)
	event := MessageEvent{Kind: EventKindGroup, ProfileID: "bot", GroupID: "g1", SelfID: "42"}
	for i := 0; i < messages; i++ {
		item := event
		item.MessageID = fmt.Sprint(i)
		item.UserID = fmt.Sprintf("1000%d", i%4)
		item.SenderName = fmt.Sprintf("群友%d", i%4)
		item.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "绷不住了哥几个"}}}
		runtime.remember(item)
	}
	// 机器人自己的话不算学习材料。
	bot := event
	bot.MessageID, bot.UserID, bot.Outbound = "bot-1", "42", true
	bot.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "机器人说的话不该进学习材料"}}}
	runtime.remember(bot)
	return runtime, styles, event
}

// 学一次：材料只有群友的话、发言人换成字母，学到的笔记存下来并进回复尾部。
func TestLearnGroupStyleSavesNoteAndInjectsIt(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{"这个群爱说「绷不住了」「哥几个」，一条就一句，不打句号"}}
	runtime, styles, event := groupStyleTestRuntime(t, groupStyleMinMessages, provider)
	style, err := runtime.learnGroupStyle(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if style.Manual || style.SampleCount != groupStyleMinMessages || !strings.Contains(style.Text, "绷不住了") {
		t.Fatalf("style = %#v", style)
	}
	if saved, found, _ := styles.GroupStyle(context.Background(), "bot", "g1"); !found || saved.Text != style.Text {
		t.Fatalf("style not saved: %#v", saved)
	}
	material := provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content
	if !strings.Contains(material, "A：绷不住了哥几个") || strings.Contains(material, "群友0") || strings.Contains(material, "10000") || strings.Contains(material, "机器人说的话") {
		t.Fatalf("learning material leaks identities or bot text: %q", material[:min(len(material), 300)])
	}
	cfg := runtime.effectiveConfigForEvent(event)
	if got := runtime.groupStylePrompt(event, cfg); !strings.Contains(got, "【这个群的说话风格】") || !strings.Contains(got, "绷不住了") {
		t.Fatalf("group style prompt = %q", got)
	}
	off := cfg
	off.ExpressionLearningEnabled = boolPointer(false)
	if runtime.groupStylePrompt(event, off) != "" {
		t.Fatal("style must not be injected when style learning is off")
	}
}

// 群友消息不够时不学，也不花那次模型调用。
func TestLearnGroupStyleNeedsEnoughMessages(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{"不该被调用"}}
	runtime, _, event := groupStyleTestRuntime(t, groupStyleMinMessages-1, provider)
	if _, err := runtime.learnGroupStyle(context.Background(), event); !errors.Is(err, ErrGroupStyleNotEnoughMessages) {
		t.Fatalf("err = %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("made %d model calls without enough messages", len(provider.requests))
	}
}

// 手动写的不被自动学习覆盖；「重新学习」覆盖；清空正文交回自动。
func TestManualGroupStyleSurvivesObserveButNotRelearn(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{"重新学到的风格"}}
	runtime, styles, event := groupStyleTestRuntime(t, groupStyleMinMessages, provider)
	ctx := context.Background()
	if _, found, err := runtime.SaveGroupStyleForProfile(ctx, "bot", "g1", "主人写的风格"); err != nil || !found {
		t.Fatalf("save manual: found=%v err=%v", found, err)
	}
	message := event
	message.MessageID, message.UserID = "new", "10001"
	message.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "来了"}}}
	runtime.observeGroupStyle(message)
	runtime.groupStyles.learning.Wait()
	if saved, _, _ := styles.GroupStyle(ctx, "bot", "g1"); saved.Text != "主人写的风格" || !saved.Manual {
		t.Fatalf("manual style was overwritten by auto learning: %#v", saved)
	}
	relearned, err := runtime.RelearnGroupStyle(ctx, "bot", "g1")
	if err != nil || relearned.Text != "重新学到的风格" || relearned.Manual {
		t.Fatalf("relearn = %#v err=%v", relearned, err)
	}
	if _, found, err := runtime.SaveGroupStyleForProfile(ctx, "bot", "g1", "  "); err != nil || found {
		t.Fatalf("clearing: found=%v err=%v", found, err)
	}
	if _, found, _ := styles.GroupStyle(ctx, "bot", "g1"); found {
		t.Fatal("empty text must hand the group back to auto learning")
	}
}

// 本群单独关掉：不带进回复、不自动学，笔记留着；重新学习后仍然关着；打开接着用。
// 关掉以后交回自动，笔记清空但开关还是关的。
func TestGroupStyleDisabledPerGroup(t *testing.T) {
	provider := &sequenceLLMProvider{replies: []string{"学到的风格", "重新学到的风格"}}
	runtime, styles, event := groupStyleTestRuntime(t, groupStyleMinMessages, provider)
	ctx := context.Background()
	if _, err := runtime.learnGroupStyle(ctx, event); err != nil {
		t.Fatal(err)
	}
	cfg := runtime.effectiveConfigForEvent(event)
	if _, _, err := runtime.SetGroupStyleEnabledForProfile(ctx, "bot", "g1", false); err != nil {
		t.Fatal(err)
	}
	if got := runtime.groupStylePrompt(event, cfg); got != "" {
		t.Fatalf("disabled group still injects style: %q", got)
	}
	if saved, _, _ := styles.GroupStyle(ctx, "bot", "g1"); saved.Text != "学到的风格" || !saved.Disabled {
		t.Fatalf("disabling must keep the note: %#v", saved)
	}

	// 过期了也不自动学。
	stale, _, _ := styles.GroupStyle(ctx, "bot", "g1")
	stale.UpdatedAt = time.Now().Add(-2 * groupStyleRefreshAfter)
	_ = styles.SaveGroupStyle(ctx, stale)
	message := event
	message.MessageID, message.UserID = "new", "10001"
	message.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "来了"}}}
	runtime.observeGroupStyle(message)
	runtime.groupStyles.learning.Wait()
	if len(provider.requests) != 1 {
		t.Fatalf("disabled group was auto-learned: %d model calls", len(provider.requests))
	}

	relearned, err := runtime.RelearnGroupStyle(ctx, "bot", "g1")
	if err != nil || !relearned.Disabled || relearned.Text != "重新学到的风格" {
		t.Fatalf("relearn on a disabled group = %#v err=%v", relearned, err)
	}

	if _, found, err := runtime.SaveGroupStyleForProfile(ctx, "bot", "g1", ""); err != nil || !found {
		t.Fatalf("hand back while disabled: found=%v err=%v", found, err)
	}
	if saved, _, _ := styles.GroupStyle(ctx, "bot", "g1"); saved.Text != "" || !saved.Disabled {
		t.Fatalf("hand back must keep the switch off: %#v", saved)
	}

	if _, found, err := runtime.SetGroupStyleEnabledForProfile(ctx, "bot", "g1", true); err != nil || found {
		t.Fatalf("enabling an empty note should leave nothing: found=%v err=%v", found, err)
	}
}
