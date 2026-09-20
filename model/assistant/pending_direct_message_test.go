// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// memoryPendingDirectMessageStore 按真实实现的契约来：Take 取出即删除，Count 和
// Take 都只认没过期的。
type memoryPendingDirectMessageStore struct {
	mu    sync.Mutex
	items []PendingDirectMessage
	takes int
}

func (s *memoryPendingDirectMessageStore) SavePendingDirectMessage(_ context.Context, item PendingDirectMessage) (PendingDirectMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	s.items = append(s.items, item)
	return item, nil
}

func (s *memoryPendingDirectMessageStore) CountPendingDirectMessages(_ context.Context, profileID, userID string, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, item := range s.items {
		if item.ProfileID == profileID && item.UserID == userID && item.ExpiresAt.After(now) {
			count++
		}
	}
	return count, nil
}

func (s *memoryPendingDirectMessageStore) TakePendingDirectMessages(_ context.Context, profileID, userID string, now time.Time) ([]PendingDirectMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.takes++
	var taken []PendingDirectMessage
	kept := s.items[:0]
	for _, item := range s.items {
		if item.ProfileID != profileID || item.UserID != userID {
			kept = append(kept, item)
			continue
		}
		if item.ExpiresAt.After(now) {
			taken = append(taken, item)
		}
	}
	s.items = kept
	return taken, nil
}

func (s *memoryPendingDirectMessageStore) PurgeExpiredPendingDirectMessages(_ context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.items[:0]
	removed := 0
	for _, item := range s.items {
		if item.ExpiresAt.After(now) {
			kept = append(kept, item)
			continue
		}
		removed++
	}
	s.items = kept
	return removed, nil
}

func (s *memoryPendingDirectMessageStore) snapshot() []PendingDirectMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PendingDirectMessage(nil), s.items...)
}

// 不是好友、又没有共同群借临时会话时，内容存下来等加好友，而不是当场报个错
// 把活儿丢回给人。
func TestCrossSessionParksWhenTempSessionIsImpossible(t *testing.T) {
	channel := newFriendRosterChannel("888")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "999"})
	store := &memoryPendingDirectMessageStore{}
	runtime.SetPendingDirectMessageStore(store)
	owner := MessageEvent{Kind: EventKindPrivate, UserID: "999", SelfID: "10000", Platform: PlatformOneBotV11}
	out, err := newDianaCrossSessionTool(runtime, owner, true).Run(context.Background(),
		map[string]any{"message": "整理好了", "user_id": "555"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"pending":true`) || !strings.Contains(out, `"delivered":false`) {
		t.Fatalf("结果没有说明是托管而不是已送达：%s", out)
	}
	if got := len(channel.sentSnapshot()); got != 0 {
		t.Fatalf("临时会话开不了时不该白发一次：发出了 %d 条", got)
	}
	parked := store.snapshot()
	if len(parked) != 1 || parked[0].UserID != "555" || parked[0].Message != "整理好了" {
		t.Fatalf("内容没有存下来：%#v", parked)
	}
}

// 临时会话可以被对方的隐私设置关掉：这一步失败同样转成托管，不是真失败。
func TestCrossSessionParksWhenTempSessionSendFails(t *testing.T) {
	channel := newFriendRosterChannel("888")
	channel.sendFailure = errors.New("临时会话不可用")
	// 发送重试是真实链路的一部分，但这里要测的是重试耗尽之后的去向，不必真等。
	runtime := crossSessionTestRuntime(channel, BotConfig{SendRetryAttempts: 1})
	store := &memoryPendingDirectMessageStore{}
	runtime.SetPendingDirectMessageStore(store)
	tool := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false)
	out, err := tool.Run(context.Background(), map[string]any{"message": "整理好了"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"pending":true`) {
		t.Fatalf("临时会话失败后应当转成托管：%s", out)
	}
	if len(store.snapshot()) != 1 {
		t.Fatalf("内容没有存下来：%#v", store.snapshot())
	}
}

// 没有配置存储时不假装存下了：直说发不出去。
func TestCrossSessionFailsWhenParkingIsUnavailable(t *testing.T) {
	channel := newFriendRosterChannel("888")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "999"})
	owner := MessageEvent{Kind: EventKindPrivate, UserID: "999", SelfID: "10000", Platform: PlatformOneBotV11}
	if _, err := newDianaCrossSessionTool(runtime, owner, true).Run(context.Background(),
		map[string]any{"message": "喂", "user_id": "555"}); err == nil {
		t.Fatal("存不下来时应当报错")
	}
}

// 同一个人最多攒几条：攒成一串在加上好友的瞬间一起轰出去比发不出去更吓人。
func TestCrossSessionParkingRespectsPerUserCap(t *testing.T) {
	store := &memoryPendingDirectMessageStore{}
	channel := newFriendRosterChannel("888")
	runtime := crossSessionTestRuntime(channel, BotConfig{OwnerID: "999"})
	runtime.SetPendingDirectMessageStore(store)
	source := MessageEvent{Kind: EventKindPrivate, UserID: "999", SelfID: "10000", Platform: PlatformOneBotV11}
	event := crossSessionPrivateEvent(source, "555", time.Now())
	for i := 0; i < pendingDirectMessagePerUser; i++ {
		if err := runtime.parkPendingDirectMessage(context.Background(), source, event, "第几条都行"); err != nil {
			t.Fatalf("第 %d 条：%v", i+1, err)
		}
	}
	if err := runtime.parkPendingDirectMessage(context.Background(), source, event, "再来一条"); err == nil {
		t.Fatal("超过上限后应当被拒绝")
	}
	if got := len(store.snapshot()); got != pendingDirectMessagePerUser {
		t.Fatalf("存下了 %d 条，上限是 %d", got, pendingDirectMessagePerUser)
	}
}

// 加上好友的那一刻，欠着的话自动补发出去。
func TestFriendAddNoticeFlushesPendingDirectMessages(t *testing.T) {
	channel := newFriendRosterChannel("888")
	runtime := crossSessionTestRuntime(channel, BotConfig{SendRetryAttempts: 1})
	store := &memoryPendingDirectMessageStore{}
	runtime.SetPendingDirectMessageStore(store)
	tool := newDianaCrossSessionTool(runtime, crossSessionGroupSource("555"), false)
	// 先制造一条发不出去的：名册里没有 555，群里也不给临时会话。
	channel.sendFailure = errors.New("临时会话不可用")
	if _, err := tool.Run(context.Background(), map[string]any{"message": "你的口癖整理如下"}); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshot()) != 1 {
		t.Fatalf("前提不成立，内容没被存下：%#v", store.snapshot())
	}

	// 好友加上了：名册里出现 555，通知一到就该把欠着的话发出去。
	channel.friends = []string{"888", "555"}
	channel.sendFailure = nil
	notice := MessageEvent{Kind: EventKindNotice, SubType: "friend_add", UserID: "555", SelfID: "10000", Platform: PlatformOneBotV11}
	if err := runtime.handleNotice(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	sent := channel.sentSnapshot()
	if len(sent) != 1 || sent[0].UserID != "555" || sent[0].Text != "你的口癖整理如下" {
		t.Fatalf("补发的私聊不对：%#v", sent)
	}
	// 加上好友之后就是普通私聊，不该再借临时会话。
	if sent[0].TempSessionGroupID != "" {
		t.Fatalf("补发不该走临时会话：%q", sent[0].TempSessionGroupID)
	}
	if got := len(store.snapshot()); got != 0 {
		t.Fatalf("补发后库里还剩 %d 条", got)
	}

	// 第二条通知（或者主人在控制台又点了一次同意）不该把同一段话再发一遍。
	if err := runtime.handleNotice(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	if got := len(channel.sentSnapshot()); got != 1 {
		t.Fatalf("同一段话被发了 %d 遍", got)
	}
}

// 过期的托管内容安静作废，不会某天突然弹出一条没头没尾的旧消息。
func TestExpiredPendingDirectMessagesAreNotDelivered(t *testing.T) {
	channel := newFriendRosterChannel("555")
	runtime := crossSessionTestRuntime(channel, BotConfig{})
	store := &memoryPendingDirectMessageStore{}
	runtime.SetPendingDirectMessageStore(store)
	now := time.Now()
	if _, err := store.SavePendingDirectMessage(context.Background(), PendingDirectMessage{
		ProfileID: "", UserID: "555", Message: "上周那份整理",
		CreatedAt: now.Add(-8 * 24 * time.Hour), ExpiresAt: now.Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	notice := MessageEvent{Kind: EventKindNotice, SubType: "friend_add", UserID: "555", SelfID: "10000", Platform: PlatformOneBotV11}
	if err := runtime.handleNotice(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	if got := len(channel.sentSnapshot()); got != 0 {
		t.Fatalf("过期内容被发出去了：%d 条", got)
	}
}
