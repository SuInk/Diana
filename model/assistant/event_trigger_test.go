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

func eventTriggerFixture(t *testing.T, spec EventTrigger, createdAt time.Time) Reminder {
	t.Helper()
	return Reminder{
		ID:               "trig-1",
		Kind:             ReminderKindEventTrigger,
		OwnerID:          "10001",
		GroupID:          "20001",
		UserID:           "10001",
		Message:          "该交作业了",
		EventTriggerJSON: encodeEventTrigger(spec),
		CreatedAt:        createdAt,
	}
}

func TestEventTriggerMatchesOnlyFreshEventsAfterCreation(t *testing.T) {
	now := time.Now()
	spec := EventTrigger{Event: eventTriggerEventMessage, UserIDs: []string{"30003"}, Action: eventTriggerActionMessage, DeliverTo: eventTriggerDeliverEvent, ExpiresAt: now.Add(time.Hour)}
	item := eventTriggerFixture(t, spec, now.Add(-time.Minute))
	message := func(userID, groupID string, at time.Time) MessageEvent {
		return MessageEvent{Kind: EventKindGroup, GroupID: groupID, UserID: userID, MessageID: "m1", Time: at.Unix()}
	}

	if !eventTriggerMatches(item, spec, message("30003", "20001", now), "在吗", now) {
		t.Fatal("被盯的人在本群说话应当触发")
	}
	if eventTriggerMatches(item, spec, message("30004", "20001", now), "在吗", now) {
		t.Fatal("别人说话不该触发")
	}
	if eventTriggerMatches(item, spec, message("30003", "20002", now), "在吗", now) {
		t.Fatal("在别的群说话不该触发：默认只盯创建时的会话")
	}
	// 重连回填会把创建之前的消息重新送进来。
	if eventTriggerMatches(item, spec, message("30003", "20001", now.Add(-2*time.Minute)), "在吗", now) {
		t.Fatal("创建之前的消息不该触发")
	}
	stale := eventTriggerFixture(t, spec, now.Add(-time.Hour))
	if eventTriggerMatches(stale, spec, message("30003", "20001", now.Add(-30*time.Minute)), "在吗", now) {
		t.Fatal("太久以前的消息被回放进来时不该触发")
	}
	bot := message("30003", "20001", now)
	bot.SenderIsBot = true
	if eventTriggerMatches(item, spec, bot, "在吗", now) {
		t.Fatal("机器人发言不该触发")
	}
	fired := spec
	fired.FireCount = 1
	if eventTriggerMatches(item, fired, message("30003", "20001", now), "在吗", now) {
		t.Fatal("一次性任务用过之后不该再触发")
	}
	expired := spec
	expired.ExpiresAt = now.Add(-time.Second)
	if eventTriggerMatches(item, expired, message("30003", "20001", now), "在吗", now) {
		t.Fatal("到期后不该触发")
	}
}

func TestEventTriggerKeywordPatternAndCooldown(t *testing.T) {
	now := time.Now()
	spec := EventTrigger{
		Event: eventTriggerEventMessage, UserIDs: []string{eventTriggerAnyUser}, Keywords: []string{"作业", "Homework"},
		Pattern: `\d+`, Action: eventTriggerActionMessage, DeliverTo: eventTriggerDeliverEvent, Repeat: true, CooldownSeconds: 600,
		ExpiresAt: now.Add(time.Hour),
	}
	item := eventTriggerFixture(t, spec, now.Add(-time.Hour))
	event := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "30003", MessageID: "m2", Time: now.Unix()}
	if !eventTriggerMatches(item, spec, event, "HOMEWORK 还差 3 题", now) {
		t.Fatal("关键词不区分大小写，且正则也满足时应触发")
	}
	if eventTriggerMatches(item, spec, event, "作业写完了", now) {
		t.Fatal("关键词和正则同时给时两者都要满足")
	}
	item.LastRunAt = now.Add(-5 * time.Minute)
	if eventTriggerMatches(item, spec, event, "作业 1", now) {
		t.Fatal("冷却期内不该再触发")
	}
	item.LastRunAt = now.Add(-11 * time.Minute)
	if !eventTriggerMatches(item, spec, event, "作业 1", now) {
		t.Fatal("冷却过后应当再次触发")
	}
	spec.LastMessageID = "m2"
	if eventTriggerMatches(item, spec, event, "作业 1", now) {
		t.Fatal("同一条消息被队列重放时不该再触发")
	}
}

func TestEventTriggerCreatePermissions(t *testing.T) {
	now := time.Now()
	group := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "30003"}
	private := MessageEvent{Kind: EventKindPrivate, UserID: "10001"}

	spec, _, err := parseEventTriggerCreate(map[string]any{"message": "交作业"}, group, false, now)
	if err != nil || len(spec.UserIDs) != 1 || spec.UserIDs[0] != "30003" {
		t.Fatalf("不传 users 应默认只盯当前说话的人：spec=%#v err=%v", spec, err)
	}
	if spec.ExpiresAt.Sub(now) != defaultEventTriggerLifetime {
		t.Fatalf("默认有效期 = %s", spec.ExpiresAt.Sub(now))
	}
	cases := []struct {
		name  string
		input map[string]any
		event MessageEvent
		owner bool
		want  string
	}{
		{"非主人盯别人", map[string]any{"message": "x", "users": []any{"30004"}}, group, false, "只有主人"},
		{"非主人盯任何人", map[string]any{"message": "x", "users": "*"}, group, false, "只有主人"},
		{"非主人盯别的群", map[string]any{"message": "x", "where": "group", "group_id": "20002"}, group, false, "只有主人"},
		{"私聊里盯别人", map[string]any{"message": "x", "users": []any{"30004"}}, private, true, "私聊里只有你自己"},
		{"私聊里盯进群", map[string]any{"message": "x", "event": "member_join", "users": "*"}, private, true, "私聊里没有进群事件"},
		{"进群带关键词", map[string]any{"message": "x", "event": "member_join", "users": "*", "keywords": []any{"hi"}}, group, true, "不能带 keywords"},
		{"坏正则", map[string]any{"message": "x", "pattern": "("}, group, false, "不是合法的正则"},
		{"冷却太短", map[string]any{"message": "x", "repeat": true, "cooldown": "10s"}, group, false, "cooldown"},
		{"空内容", map[string]any{}, group, false, "message 不能为空"},
	}
	for _, tc := range cases {
		if _, _, err := parseEventTriggerCreate(tc.input, tc.event, tc.owner, now); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v, want %q", tc.name, err, tc.want)
		}
	}
	spec, _, err = parseEventTriggerCreate(map[string]any{"message": "x", "users": []any{"30004"}, "where": "anywhere", "deliver_to": "origin"}, private, true, now)
	if err != nil || !spec.WatchAnywhere || spec.DeliverTo != eventTriggerDeliverOrigin {
		t.Fatalf("主人可以在私聊里设「他在任何地方说话就告诉我」：spec=%#v err=%v", spec, err)
	}
}

// 事件触发任务的 TriggerAt 为零。定时轮询要是不认它，每秒都会把它当成到期的一次性
// 提醒发一遍「提醒你：……」。
func TestDueReminderLoopSkipsEventTriggers(t *testing.T) {
	now := time.Now()
	spec := EventTrigger{Event: eventTriggerEventMessage, UserIDs: []string{"30003"}, Action: eventTriggerActionMessage, DeliverTo: eventTriggerDeliverEvent, ExpiresAt: now.Add(time.Hour)}
	store := &stubReminderStore{items: []Reminder{eventTriggerFixture(t, spec, now)}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, channel, NewPluginManager(), nil, store, nil, nil)

	runtime.fireDueReminders(context.Background())

	if sent := channel.sentSnapshot(); len(sent) != 0 {
		t.Fatalf("定时轮询把事件触发任务发出去了：%#v", sent)
	}
	if !store.items[0].LastRunAt.IsZero() {
		t.Fatalf("定时轮询动了事件触发任务：%#v", store.items[0])
	}
}

func TestEventTriggerFiresOnceWhenWatchedUserSpeaks(t *testing.T) {
	store := &stubReminderStore{}
	channel := &recordingChannel{}
	// 引用跟着管理员的引用配置走（默认 auto 不挂），这里显式打开来验证链路；@ 总是挂上。
	runtime := NewRuntime(BotConfig{OwnerID: "10001", ReplyReferenceMode: ReplyDecorationOn}, channel, NewPluginManager(), nil, store, nil, nil)
	creator := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001", Time: time.Now().Unix()}
	tool := newDianaEventTriggerTool(runtime, creator)
	raw, err := tool.Run(context.Background(), map[string]any{
		"operation": "create",
		"users":     []any{"30003"},
		"message":   "该交作业了",
	})
	if err != nil {
		t.Fatal(err)
	}
	var created dianaEventTriggerResult
	if err := json.Unmarshal([]byte(raw), &created); err != nil || created.Trigger == nil {
		t.Fatalf("created=%q err=%v", raw, err)
	}
	// 创建和触发落在同一秒以内也要认。
	speak := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "30003", MessageID: "m9", Time: time.Now().Unix()}

	runtime.dispatchEventTriggers(context.Background(), speak, "我来了", true)
	waitForCondition(t, 3*time.Second, func() bool { return len(channel.sentSnapshot()) == 1 })
	sent := channel.sentSnapshot()[0]
	if sent.GroupID != "20001" || !strings.Contains(sent.Text, "该交作业了") || sent.MentionUserID != "30003" || sent.ReplyMessageID != "m9" {
		t.Fatalf("应当在本群引用并 @ 触发者：%#v", sent)
	}

	again := speak
	again.MessageID = "m10"
	runtime.dispatchEventTriggers(context.Background(), again, "还在", true)
	time.Sleep(100 * time.Millisecond)
	if got := len(channel.sentSnapshot()); got != 1 {
		t.Fatalf("一次性任务触发了 %d 次", got)
	}
	spec, _ := EventTriggerSpec(store.items[0])
	if eventTriggerStatus(store.items[0], spec) != "used" || spec.FireCount != 1 {
		t.Fatalf("status=%s spec=%#v", eventTriggerStatus(store.items[0], spec), spec)
	}
}

// 触发任务执行期间再去建触发任务会让一条指令在自己的回复里无限繁殖。
func TestEventTriggerCannotBeCreatedDuringTriggerRun(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	tool := newDianaEventTriggerTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001"})
	_, err := tool.Run(withEventTriggerRun(context.Background()), map[string]any{"operation": "create", "message": "x"})
	if err == nil || len(store.items) != 0 {
		t.Fatalf("err=%v items=%#v", err, store.items)
	}
}

func TestEventTriggerExpiryNotifiesCreatorWhenNeverFired(t *testing.T) {
	now := time.Now()
	spec := EventTrigger{Event: eventTriggerEventMessage, UserIDs: []string{"30003"}, Action: eventTriggerActionMessage, DeliverTo: eventTriggerDeliverEvent, ExpiresAt: now.Add(-time.Second)}
	store := &stubReminderStore{items: []Reminder{eventTriggerFixture(t, spec, now.Add(-time.Hour))}}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, channel, NewPluginManager(), nil, store, nil, nil)

	runtime.expireEventTriggers(context.Background(), now)
	runtime.expireEventTriggers(context.Background(), now)

	sent := channel.sentSnapshot()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "到期") || sent[0].GroupID != "20001" {
		t.Fatalf("到期没触发过应当告诉创建者一次：%#v", sent)
	}
	got, _ := EventTriggerSpec(store.items[0])
	if eventTriggerStatus(store.items[0], got) != "expired" || store.items[0].CancelledAt.IsZero() {
		t.Fatalf("到期后应收掉：%#v", store.items[0])
	}
}

func TestEventTriggerQuotaCountsOnlyArmedTriggers(t *testing.T) {
	store := &stubReminderStore{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, nilChannel{}, NewPluginManager(), nil, store, nil, nil)
	member := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "30003"}
	limit := runtime.relationshipPolicy(context.Background(), member).personalScheduleLimit()
	spec := EventTrigger{Event: eventTriggerEventMessage, UserIDs: []string{"30003"}, Action: eventTriggerActionMessage, DeliverTo: eventTriggerDeliverEvent, ExpiresAt: time.Now().Add(time.Hour)}
	for index := 0; index < limit; index++ {
		if _, err := runtime.addEventTrigger(context.Background(), member, spec, "x"); err != nil {
			t.Fatalf("第 %d 个: %v", index+1, err)
		}
	}
	if _, err := runtime.addEventTrigger(context.Background(), member, spec, "x"); err == nil || !strings.Contains(err.Error(), "额度已满") {
		t.Fatalf("超额应当拒绝：%v", err)
	}
	if _, err := runtime.cancelEventTrigger("30003", store.items[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.addEventTrigger(context.Background(), member, spec, "x"); err != nil {
		t.Fatalf("取消后应释放额度：%v", err)
	}
}

// 走真实入站链路：一条没 @ 机器人的普通群消息，机器人这一轮不回，触发照样执行。
func TestEventTriggerFiresThroughInboundPipeline(t *testing.T) {
	store := &stubReminderStore{}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{OwnerID: "10001"}, channel, NewPluginManager(), nil, store, nil, nil)
	creator := MessageEvent{Kind: EventKindGroup, GroupID: "20001", UserID: "10001"}
	spec, message, err := parseEventTriggerCreate(map[string]any{"message": "该交作业了", "users": []any{"30003"}}, creator, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.addEventTrigger(context.Background(), creator, spec, message); err != nil {
		t.Fatal(err)
	}

	err = runtime.HandleEvent(context.Background(), MessageEvent{
		Kind: EventKindGroup, GroupID: "20001", UserID: "30003", MessageID: "m1", Time: time.Now().Unix(),
		Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": "大家好"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, 3*time.Second, func() bool {
		for _, sent := range channel.sentSnapshot() {
			if strings.Contains(sent.Text, "该交作业了") && sent.MentionUserID == "30003" {
				return true
			}
		}
		return false
	})
}
