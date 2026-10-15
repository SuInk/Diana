// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"testing"
	"time"
)

type repositoryTargetFailureChannel struct {
	recordingChannel
	failProfile string
}

func (c *repositoryTargetFailureChannel) SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error) {
	if msg.ProfileID == c.failProfile {
		return nil, errors.New("target unavailable")
	}
	return c.recordingChannel.SendWithResult(ctx, msg)
}

func TestRepositoryWatchRetriesOnlyFailedTargets(t *testing.T) {
	const message = "GitHub 动态：acme/demo\nStar +1（21 → 22）"
	channel := &repositoryTargetFailureChannel{failProfile: "b"}
	item := Reminder{
		ID: "watch", Kind: ReminderKindRepositoryWatch, Repository: "acme/demo", OwnerID: "owner",
		PendingDelivery: message, IntervalSeconds: 60,
		NotificationTargetsJSON: encodeReminderDeliveryTargets([]ReminderDeliveryTarget{
			{Platform: PlatformOneBotV11, ProfileID: "a", GroupID: "100"},
			{Platform: PlatformOneBotV11, ProfileID: "b", GroupID: "200"},
		}),
	}
	store := &stubReminderStore{items: []Reminder{item}}
	runtime := NewRuntime(BotConfig{ID: "a", SendRetryAttempts: 1}, channel, NewDefaultPluginManager(), nil, store, nil, nil)
	runtime.profileConfigs["b"] = BotConfig{ID: "b", Platform: PlatformOneBotV11, SendRetryAttempts: 1}

	if err := runtime.sendRepositoryWatch(context.Background(), item, message); err == nil {
		t.Fatal("target failure was hidden")
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || sent[0].GroupID != "100" {
		t.Fatalf("first delivery = %#v, want only group 100", sent)
	}
	if got := store.items[0].PendingDeliveredTargets; len(got) != 1 {
		t.Fatalf("successful target was not persisted: %#v", got)
	}

	channel.failProfile = ""
	if err := runtime.sendRepositoryWatch(context.Background(), store.items[0], message); err != nil {
		t.Fatal(err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 2 || sent[0].GroupID != "100" || sent[1].GroupID != "200" {
		t.Fatalf("retry resent a completed target: %#v", sent)
	}
}

func TestRepositoryWatchSkipsGroupThatBotHasLeft(t *testing.T) {
	const message = "GitHub 动态：acme/demo\nStar +1（21 → 22）"
	channel := &recordingChannel{}
	item := Reminder{
		ID: "watch", Kind: ReminderKindRepositoryWatch, Repository: "acme/demo", OwnerID: "owner",
		PendingDelivery: message, IntervalSeconds: 60,
		NotificationTargetsJSON: encodeReminderDeliveryTargets([]ReminderDeliveryTarget{
			{Platform: PlatformOneBotV11, ProfileID: "a", GroupID: "left"},
			{Platform: PlatformOneBotV11, ProfileID: "a", GroupID: "live"},
		}),
	}
	store := &stubReminderStore{items: []Reminder{item}}
	runtime := NewRuntime(BotConfig{ID: "a", SendRetryAttempts: 1}, channel, NewDefaultPluginManager(), nil, store, nil, nil)
	left := MessageEvent{Kind: EventKindGroup, Platform: PlatformOneBotV11, ProfileID: "a", GroupID: "left"}
	runtime.unavailableGroups[runtime.outboundGroupKey(left)] = unavailableGroupSend{BlockedAt: time.Now(), Reason: "机器人已被移出该群"}

	blocked := runtime.blockedGroupSendError(left)
	if !errors.Is(blocked, errGroupSendUnavailable) || !errors.Is(blocked, ErrDeliveryTargetDisabled) {
		t.Fatalf("left-group error classification = %v", blocked)
	}
	if err := runtime.sendRepositoryWatch(context.Background(), item, message); err != nil {
		t.Fatal(err)
	}
	if sent := channel.sentSnapshot(); len(sent) != 1 || sent[0].GroupID != "live" {
		t.Fatalf("delivery = %#v, want only live group", sent)
	}
}
