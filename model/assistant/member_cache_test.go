package assistant

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func groupEvent(groupID string, userID string, level int) MessageEvent {
	return MessageEvent{
		Kind:        EventKindGroup,
		GroupID:     groupID,
		UserID:      userID,
		SenderLevel: level,
		SenderRole:  "member",
	}
}

func TestMemberCacheObservesLevelFromEvent(t *testing.T) {
	cache := newMemberCache(nil)
	cache.Observe(groupEvent("100", "42", 5))

	info, ok := cache.lookup("100", "42")
	if !ok {
		t.Fatal("期望命中缓存")
	}
	if info.Level != 5 {
		t.Fatalf("期望等级 5，实际 %d", info.Level)
	}
}

// 群等级按群独立累积，缓存键必须带群号。只按 QQ 号缓存会让 A 群刷到高等级的人
// 在 B 群也被判定为高等级，等级门槛直接失效。
func TestMemberCacheIsScopedPerGroup(t *testing.T) {
	cache := newMemberCache(nil)
	cache.Observe(groupEvent("100", "42", 6))

	if _, ok := cache.lookup("200", "42"); ok {
		t.Fatal("同一个 QQ 在别的群不应命中缓存")
	}

	level, ok := cache.LevelFor(groupEvent("200", "42", 0))
	if ok {
		t.Fatalf("别的群应判定为不可信，实际返回等级 %d", level)
	}
}

func TestMemberCacheExpiresByTTL(t *testing.T) {
	now := time.Now()
	cache := newMemberCache(nil)
	cache.now = func() time.Time { return now }
	cache.Observe(groupEvent("100", "42", 5))

	now = now.Add(defaultMemberCacheTTL + time.Second)
	if _, ok := cache.lookup("100", "42"); ok {
		t.Fatal("过期后不应命中")
	}
}

// 事件不带等级时返回 false 而不是 0，让调用方能区分「查不到」和「等级为 0」。
func TestMemberCacheReportsUnknownWhenEventHasNoLevel(t *testing.T) {
	cache := newMemberCache(nil)
	level, ok := cache.LevelFor(groupEvent("100", "42", 0))
	if ok {
		t.Fatalf("期望判定为查不到，实际返回等级 %d", level)
	}
}

// 事件不带等级时走 get_group_member_info 兜底，异步回填后下次命中。
func TestMemberCacheFallsBackToAPI(t *testing.T) {
	var calls int
	var mu sync.Mutex
	done := make(chan struct{})
	cache := newMemberCache(func(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		if action != "get_group_member_info" {
			t.Errorf("非预期的 action：%s", action)
		}
		defer close(done)
		return map[string]any{"level": "7", "role": "member"}, nil
	})

	if _, ok := cache.LevelFor(groupEvent("100", "42", 0)); ok {
		t.Fatal("首次查询应 fail-open 返回不可信")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("兜底查询未触发")
	}
	// 等回填落盘。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if info, ok := cache.lookup("100", "42"); ok && info.Level == 7 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("回填后应命中等级 7")
}

// 群里刷屏时同一个人会连续触发查询，必须去重。
func TestMemberCacheDedupesInflightFetches(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	cache := newMemberCache(func(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return map[string]any{"level": "3"}, nil
	})

	for i := 0; i < 10; i++ {
		cache.LevelFor(groupEvent("100", "42", 0))
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	got := calls
	mu.Unlock()
	close(release)
	if got != 1 {
		t.Fatalf("期望只发 1 次查询，实际 %d 次", got)
	}
}

// 实现返回不了等级时不要写空值，否则下次还会白跑一趟。
func TestMemberCacheSkipsEmptyAPIResult(t *testing.T) {
	done := make(chan struct{})
	cache := newMemberCache(func(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
		defer close(done)
		return map[string]any{}, nil
	})
	cache.LevelFor(groupEvent("100", "42", 0))
	<-done
	time.Sleep(20 * time.Millisecond)
	if _, ok := cache.lookup("100", "42"); ok {
		t.Fatal("空结果不应写入缓存")
	}
}

func TestMemberCacheEvictsWhenOverCapacity(t *testing.T) {
	cache := newMemberCache(nil)
	cache.maxEntries = 10
	for i := 0; i < 50; i++ {
		cache.Observe(groupEvent("100", fmt.Sprint(i), 3))
	}
	cache.mu.RLock()
	size := len(cache.entries)
	cache.mu.RUnlock()
	if size > cache.maxEntries {
		t.Fatalf("缓存超过上限：%d > %d", size, cache.maxEntries)
	}
}

// 各 OneBot 实现的 level 类型不一致，解析不出一律记 0 交给 fail-open 处理。
func TestParseGroupLevel(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{"6", 6},
		{float64(4), 4},
		{nil, 0},
		{"", 0},
		{"潜水", 0},
		{"-1", 0},
	}
	for _, tc := range cases {
		if got := parseGroupLevel(tc.in); got != tc.want {
			t.Errorf("parseGroupLevel(%#v) = %d，期望 %d", tc.in, got, tc.want)
		}
	}
}
