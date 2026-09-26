// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReminderLoopDelay(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		claimed int
		next    time.Time
		want    time.Duration
	}{
		{"没有任务睡到上限", 0, time.Time{}, reminderLoopMaxIdle},
		{"睡到下一次", 0, now.Add(3 * time.Second), 3 * time.Second},
		{"下一次很远也只睡上限", 0, now.Add(time.Hour), reminderLoopMaxIdle},
		{"已到期但没认领到要退避", 0, now.Add(-time.Second), reminderLoopBusyBackoff},
		{"已到期且刚认领过立刻再看", 2, now.Add(-time.Second), 0},
	}
	for _, tc := range cases {
		if got := reminderLoopDelay(tc.claimed, tc.next, now); got != tc.want {
			t.Fatalf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

// 下一次唤醒只看认领得了的任务：停用机器人的、正在跑的、已经发过的一次性提醒都不算，
// 否则它们「到期却认领不了」会让循环空转。
func TestClaimDueRemindersWithNextSkipsUnclaimable(t *testing.T) {
	now := time.Now()
	soon, later := now.Add(time.Minute), now.Add(time.Hour)
	store := &stubReminderStore{items: []Reminder{
		{ID: "due", Kind: ReminderKindMessage, OwnerID: "1", UserID: "1", Message: "a", TriggerAt: now.Add(-time.Second)},
		{ID: "disabled-soon", Kind: ReminderKindMessage, ProfileID: "off", OwnerID: "1", UserID: "1", Message: "b", TriggerAt: soon},
		{ID: "used-soon", Kind: ReminderKindMessage, OwnerID: "1", UserID: "1", Message: "c", TriggerAt: soon, LastRunAt: now},
		{ID: "cancelled-soon", Kind: ReminderKindMessage, OwnerID: "1", UserID: "1", Message: "d", TriggerAt: soon, CancelledAt: now},
		{ID: "later", Kind: ReminderKindMessage, OwnerID: "1", UserID: "1", Message: "e", TriggerAt: later},
	}}
	runtime := NewRuntime(BotConfig{OwnerID: "1"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	runtime.disabledProfiles = map[string]bool{"off": true}

	due, next := runtime.claimDueRemindersWithNext(now)
	if len(due) != 1 || due[0].ID != "due" {
		t.Fatalf("due = %+v", due)
	}
	if !next.Equal(later) {
		t.Fatalf("next = %s, want %s", next, later)
	}
}

// 新建的提醒不用等轮询：保存时叫醒循环，睡到它的到期时间就发。
func TestReminderLoopWakesOnSaveAndFiresOnTime(t *testing.T) {
	store := &stubReminderStore{}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{ID: "bot", Enabled: true, OwnerID: "10001"}, channel, NewPluginManager(), nil, store, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runtime.runReminderLoop(ctx)
	// 让循环先跑完第一轮、进入 30 秒的空闲睡眠。
	time.Sleep(100 * time.Millisecond)

	due := time.Now().Add(300 * time.Millisecond)
	runtime.reminderMu.Lock()
	err := runtime.reminders.SaveReminders([]Reminder{{
		ID: "soon", Kind: ReminderKindMessage, OwnerID: "10001", UserID: "10001", Message: "喝水", TriggerAt: due, CreatedAt: time.Now(),
	}})
	runtime.reminderMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, message := range channel.sentSnapshot() {
			if strings.Contains(message.Text, "喝水") {
				if time.Now().Before(due) {
					t.Fatal("fired before it was due")
				}
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("reminder saved while the loop was idle did not fire within 5s")
}

// 晚到超过 12 小时的一次性提醒改成「错过的提醒」，注明原定时间，不再戳人。
func TestExecuteClaimedReminderMarksLongOverdueAsMissed(t *testing.T) {
	due := time.Now().Add(-13 * time.Hour)
	store := &stubReminderStore{items: []Reminder{{
		ID: "late", Kind: ReminderKindMessage, OwnerID: "10001", UserID: "10001", Message: "开会", TriggerAt: due, CreatedAt: due,
	}}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{ID: "bot", Enabled: true, OwnerID: "10001"}, channel, NewPluginManager(), nil, store, nil, nil)
	runtime.fireDueReminders(context.Background())
	sent := channel.sentSnapshot()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "错过的提醒") || !strings.Contains(sent[0].Text, due.Local().Format("01-02 15:04")) || !strings.Contains(sent[0].Text, "开会") {
		t.Fatalf("sent = %+v", sent)
	}
	if store.items[0].LastRunAt.IsZero() {
		t.Fatal("missed reminder should still be marked delivered")
	}
}

func TestExecuteClaimedReminderSlightlyLateStaysNormal(t *testing.T) {
	due := time.Now().Add(-2 * time.Hour)
	store := &stubReminderStore{items: []Reminder{{
		ID: "late", Kind: ReminderKindMessage, OwnerID: "10001", UserID: "10001", Message: "开会", TriggerAt: due, CreatedAt: due,
	}}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{ID: "bot", Enabled: true, OwnerID: "10001"}, channel, NewPluginManager(), nil, store, nil, nil)
	runtime.fireDueReminders(context.Background())
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].Text != "提醒你：开会" {
		t.Fatalf("sent = %+v", sent)
	}
}

// 重试会把 TriggerAt 往后挪，迟到要按原定时间算：原定 13 小时前、刚重试过的提醒
// 仍然算错过。
func TestMissedReminderLatenessUsesOriginalTime(t *testing.T) {
	now := time.Now()
	original := now.Add(-13 * time.Hour)
	item := Reminder{TriggerAt: now.Add(-time.Minute), OriginalTriggerAt: original}
	due, late := missedReminderLateness(item, now)
	if !due.Equal(original) || late <= missedReminderGrace {
		t.Fatalf("due = %s late = %s", due, late)
	}
	if got := formatReminderLateness(13 * time.Hour); got != "约 13 小时" {
		t.Fatalf("lateness = %q", got)
	}
	if got := formatReminderLateness(72 * time.Hour); got != "约 3 天" {
		t.Fatalf("lateness = %q", got)
	}
}

func TestRescheduleOneTimeReminderKeepsOriginalTime(t *testing.T) {
	original := time.Now().Add(-time.Minute)
	store := &stubReminderStore{items: []Reminder{{
		ID: "r", Kind: ReminderKindMessage, OwnerID: "1", UserID: "1", Message: "x", TriggerAt: original,
	}}}
	runtime := NewRuntime(BotConfig{OwnerID: "1"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	for range 2 {
		if _, err := runtime.rescheduleOneTimeReminder("r", errOutboundSend); err != nil {
			t.Fatal(err)
		}
	}
	item := store.items[0]
	if !item.OriginalTriggerAt.Equal(original) || !item.TriggerAt.After(original) {
		t.Fatalf("item = %+v", item)
	}
}
