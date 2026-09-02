// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func romanceProfile(favorability, messages int, since time.Time) UserMemoryProfile {
	return UserMemoryProfile{
		UserID:       "10005",
		Favorability: favorability,
		MessageCount: messages,
		Romance:      &UserRomanceState{Active: true, Since: since, StartedBy: "user"},
	}
}

func TestApplyRomancePolicyOverlaysPartnerTier(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)
	since := now.AddDate(0, 0, -9)
	profile := romanceProfile(80, 60, since)

	base := RelationshipPolicyFor(profile, "owner", "10005")
	policy := applyRomancePolicy(base, profile, now)
	if policy.Tier != RelationshipPartner || policy.Name != "恋人" || !policy.Romance {
		t.Fatalf("policy = %#v", policy)
	}
	if policy.RomanceDays != 10 {
		t.Fatalf("days = %d", policy.RomanceDays)
	}
	// 权限不因恋爱扩张：Allow* 和主人身份一个不变，额度按亲近程度落在信赖同档。
	if policy.Owner || policy.AllowDocumentOCR != base.AllowDocumentOCR || policy.AllowImageGeneration != base.AllowImageGeneration {
		t.Fatalf("permissions changed: %#v", policy)
	}
	if policy.personalScheduleLimit() != (RelationshipPolicy{Tier: RelationshipTrusted}).personalScheduleLimit() {
		t.Fatalf("schedule limit = %d", policy.personalScheduleLimit())
	}

	// 好感度掉到冷淡线以下进入冷战，但仍是恋人。
	cold := profile
	cold.Favorability = -30
	policy = applyRomancePolicy(RelationshipPolicyFor(cold, "owner", "10005"), cold, now)
	if policy.Tier != RelationshipPartner || policy.Name != "冷战" {
		t.Fatalf("strained policy = %#v", policy)
	}

	// 没在恋爱时原样返回。
	plain := UserMemoryProfile{UserID: "10005", Favorability: 80, MessageCount: 60}
	if got := applyRomancePolicy(RelationshipPolicyFor(plain, "owner", "10005"), plain, now); got.Romance || got.Tier == RelationshipPartner {
		t.Fatalf("plain profile got romance overlay: %#v", got)
	}
}

func TestApplyRomancePolicyKeepsOwnerIdentity(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)
	profile := romanceProfile(120, 200, now.AddDate(0, -2, 0))
	profile.UserID = "owner"
	policy := applyRomancePolicy(RelationshipPolicyFor(profile, "owner", "owner"), profile, now)
	if !policy.Owner || policy.Tier != RelationshipOwner || policy.Name != "主人" {
		t.Fatalf("owner identity lost: %#v", policy)
	}
	if !policy.Romance || !strings.Contains(policy.Tone, "恋人") {
		t.Fatalf("owner tone missing romance: %#v", policy)
	}
}

func TestRelationshipPolicyForConfigHonorsRomanceGate(t *testing.T) {
	profile := romanceProfile(80, 60, time.Now().AddDate(0, 0, -3))
	// 总开关关着（默认）时，档案里的恋爱状态不生效。
	if policy := RelationshipPolicyForConfig(BotConfig{OwnerID: "owner"}, profile, "10005"); policy.Romance {
		t.Fatalf("romance leaked past disabled gate: %#v", policy)
	}
	enabled := BotConfig{OwnerID: "owner", RomanceEnabled: boolPointer(true)}
	if policy := RelationshipPolicyForConfig(enabled, profile, "10005"); !policy.Romance || policy.Name != "恋人" {
		t.Fatalf("romance not applied: %#v", policy)
	}
}

func TestRomanceMilestoneNote(t *testing.T) {
	since := time.Date(2026, 3, 15, 20, 0, 0, 0, time.Local)
	if note := romanceMilestoneNote(since, time.Date(2027, 3, 15, 9, 0, 0, 0, time.Local)); !strings.Contains(note, "1 周年") {
		t.Fatalf("anniversary note = %q", note)
	}
	if note := romanceMilestoneNote(since, time.Date(2026, 6, 15, 9, 0, 0, 0, time.Local)); !strings.Contains(note, "3 个月") {
		t.Fatalf("monthly note = %q", note)
	}
	// 平常日子和确立当天都不报。
	if note := romanceMilestoneNote(since, time.Date(2026, 6, 16, 9, 0, 0, 0, time.Local)); note != "" {
		t.Fatalf("ordinary day note = %q", note)
	}
	if note := romanceMilestoneNote(since, since.Add(2*time.Hour)); note != "" {
		t.Fatalf("same-day note = %q", note)
	}
	if note := romanceMilestoneNote(time.Time{}, time.Now()); note != "" {
		t.Fatalf("zero since note = %q", note)
	}
}

func TestRomanceContextLineRendersState(t *testing.T) {
	if line := romanceContextLine(RelationshipPolicy{}); line != "" {
		t.Fatalf("non-romance line = %q", line)
	}
	line := romanceContextLine(RelationshipPolicy{Romance: true, RomanceDays: 42, RomanceNote: "今天是你们确立关系满 1 个月的日子。"})
	if !strings.Contains(line, "恋人关系") || !strings.Contains(line, "第 42 天") || !strings.Contains(line, "纪念日") {
		t.Fatalf("line = %q", line)
	}
}

func TestRomanceStartRequiresGateAndThreshold(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["10005"] = UserMemoryProfile{UserID: "10005", Favorability: 10, MessageCount: 5}
	event := MessageEvent{Kind: EventKindPrivate, UserID: "10005"}

	// 总开关关着时直接报错，模型会解释需要主人开启。
	off := NewRuntime(BotConfig{OwnerID: "owner"}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	off.SetUserMemoryStore(memory)
	if _, err := newDianaRelationshipTool(off, event).Run(context.Background(), map[string]any{"operation": "romance_start"}); err == nil {
		t.Fatal("romance_start succeeded with gate closed")
	}

	runtime := NewRuntime(BotConfig{OwnerID: "owner", RomanceEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	tool := newDianaRelationshipTool(runtime, event)

	// 好感度不够被婉拒：这是正常状态，不是错误。
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "romance_start"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Action != "declined" {
		t.Fatalf("result = %#v", result)
	}
	if memory.profiles["10005"].Romance != nil {
		t.Fatal("declined confession still wrote romance state")
	}

	// 门槛达到后确立成功并落库。
	memory.profiles["10005"] = UserMemoryProfile{UserID: "10005", Favorability: 80, MessageCount: 60}
	raw, err = tool.Run(context.Background(), map[string]any{"operation": "romance_start"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "romance_started" || result.Target == nil || !result.Target.Romance {
		t.Fatalf("result = %#v", result)
	}
	state := memory.profiles["10005"].Romance
	if state == nil || !state.Active || state.Since.IsZero() || state.StartedBy != "user" {
		t.Fatalf("state = %#v", state)
	}

	// 已经是恋人时再表白是 noop，不重置纪念日。
	since := state.Since
	raw, err = tool.Run(context.Background(), map[string]any{"operation": "romance_start"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "noop" || !memory.profiles["10005"].Romance.Since.Equal(since) {
		t.Fatalf("repeat confession result = %#v", result)
	}
}

func TestRomanceStartIsMonogamous(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	// 10005 已是恋人；10006 条件完全够，但机器人已经有对象了。
	memory.profiles["10005"] = romanceProfile(90, 100, time.Now().AddDate(0, -2, 0))
	memory.profiles["10006"] = UserMemoryProfile{UserID: "10006", Favorability: 120, MessageCount: 200}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", RomanceEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)

	suitor := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10006"})
	raw, err := suitor.Run(context.Background(), map[string]any{"operation": "romance_start"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Action != "declined" || !strings.Contains(result.Message, "已经有") {
		t.Fatalf("result = %#v", result)
	}
	// 不透露现任是谁，也不落任何状态。
	if strings.Contains(result.Message, "10005") {
		t.Fatalf("partner identity leaked: %q", result.Message)
	}
	if memory.profiles["10006"].Romance != nil {
		t.Fatal("declined confession wrote state")
	}

	// 现任分手之后，同样的表白就能成。
	if _, err := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10005"}).Run(context.Background(), map[string]any{"operation": "romance_end"}); err != nil {
		t.Fatal(err)
	}
	raw, err = suitor.Run(context.Background(), map[string]any{"operation": "romance_start"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "romance_started" || memory.profiles["10006"].Romance == nil {
		t.Fatalf("post-breakup confession failed: %#v", result)
	}
}

func TestRomanceStartOnlyForCurrentSpeaker(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["10005"] = UserMemoryProfile{UserID: "10005", Favorability: 80, MessageCount: 60}
	runtime := NewRuntime(BotConfig{OwnerID: "owner", RomanceEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	// 主人也不能替别人表白。
	tool := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner"})
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "romance_start", "target_user_id": "10005"}); err == nil {
		t.Fatal("romance_start accepted a third-party target")
	}
}

func TestRomanceEndBySelfAndOwner(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["10005"] = romanceProfile(80, 60, time.Now().AddDate(0, 0, -10))
	runtime := NewRuntime(BotConfig{OwnerID: "owner", RomanceEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)

	// 普通用户只能解除自己的。
	stranger := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10006"})
	if _, err := stranger.Run(context.Background(), map[string]any{"operation": "romance_end", "target_user_id": "10005"}); err == nil {
		t.Fatal("stranger ended someone else's romance")
	}

	self := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10005"})
	raw, err := self.Run(context.Background(), map[string]any{"operation": "romance_end"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "romance_ended" || memory.profiles["10005"].Romance != nil {
		t.Fatalf("self breakup result = %#v state = %#v", result, memory.profiles["10005"].Romance)
	}
	// 分手不动好感度和互动记录。
	if memory.profiles["10005"].Favorability != 80 || memory.profiles["10005"].MessageCount != 60 {
		t.Fatalf("breakup changed stats: %#v", memory.profiles["10005"])
	}

	// 主人可以替任何人解除；对非恋人是 noop。
	memory.profiles["10007"] = romanceProfile(70, 40, time.Now().AddDate(0, 0, -3))
	memory.profiles["10007"] = func(p UserMemoryProfile) UserMemoryProfile { p.UserID = "10007"; return p }(memory.profiles["10007"])
	owner := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "owner"})
	if _, err := owner.Run(context.Background(), map[string]any{"operation": "romance_end", "target_user_id": "10007"}); err != nil {
		t.Fatal(err)
	}
	if memory.profiles["10007"].Romance != nil {
		t.Fatal("owner breakup did not clear state")
	}
	raw, err = owner.Run(context.Background(), map[string]any{"operation": "romance_end", "target_user_id": "10007"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "noop" {
		t.Fatalf("repeat breakup result = %#v", result)
	}
}

func TestRelationshipSnapshotCarriesRomance(t *testing.T) {
	memory := newMemoryUserMemoryStore()
	memory.profiles["10005"] = romanceProfile(80, 60, time.Now().AddDate(0, 0, -1))
	runtime := NewRuntime(BotConfig{OwnerID: "owner", RomanceEnabled: boolPointer(true)}, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetUserMemoryStore(memory)
	tool := newDianaRelationshipTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "10005"})

	raw, err := tool.Run(context.Background(), map[string]any{"operation": "get"})
	if err != nil {
		t.Fatal(err)
	}
	var result dianaRelationshipResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Target == nil || !result.Target.Romance || result.Target.RelationshipTier != RelationshipPartner || result.Target.RelationshipName != "恋人" {
		t.Fatalf("target = %#v", result.Target)
	}
	if result.Target.RomanceDays < 1 {
		t.Fatalf("days = %#v", result.Target)
	}
}
