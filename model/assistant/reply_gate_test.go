package assistant

import (
	"context"
	"errors"
	"testing"
	"time"
)

func boolPtr(v bool) *bool { return &v }

func gateRuntime(t *testing.T, cfg BotConfig, now time.Time) *Runtime {
	t.Helper()
	rt := NewRuntime(cfg, nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	rt.now = func() time.Time { return now }
	return rt
}

// 最重要的一条回归保护：没配任何准入项的老配置，行为必须和改造前完全一致。
func TestReplyGateAbsentKeepsLegacyBehaviour(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		GroupTriggers:  []string{"Diana"},
		BotQQ:          "42",
		DisabledGroups: []string{"999"},
	}, time.Date(2026, 8, 4, 3, 0, 0, 0, time.Local))

	if !rt.shouldHandle(MessageEvent{Kind: EventKindGroup, GroupID: "1", ToMe: true}, "hello") {
		t.Fatal("@机器人 应触发")
	}
	if !rt.shouldHandle(MessageEvent{Kind: EventKindGroup, GroupID: "1"}, "Diana 帮我看看") {
		t.Fatal("触发词应触发")
	}
	if rt.shouldHandle(MessageEvent{Kind: EventKindGroup, GroupID: "1"}, "普通群聊") {
		t.Fatal("普通群聊不该触发")
	}
	if !rt.shouldHandle(MessageEvent{Kind: EventKindPrivate, UserID: "7"}, "hello") {
		t.Fatal("私聊应触发")
	}
	if rt.shouldHandle(MessageEvent{Kind: EventKindGroup, GroupID: "999", ToMe: true}, "hello") {
		t.Fatal("禁用群不该触发")
	}
}

func TestGroupAdmissionWhitelist(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		BotQQ: "42",
		GroupAdmission: GroupAdmission{
			Mode:          GroupAdmissionWhitelist,
			AllowedGroups: []string{"100"},
		},
	}, time.Now())

	if !rt.shouldHandle(MessageEvent{Kind: EventKindGroup, GroupID: "100", ToMe: true}, "hi") {
		t.Fatal("白名单内的群应触发")
	}
	if rt.shouldHandle(MessageEvent{Kind: EventKindGroup, GroupID: "200", ToMe: true}, "hi") {
		t.Fatal("白名单外的群不该触发")
	}
	// 白名单模式下私聊不受影响。
	if !rt.shouldHandle(MessageEvent{Kind: EventKindPrivate, UserID: "7"}, "hi") {
		t.Fatal("私聊不该被群白名单影响")
	}
}

// 白名单和 DisabledGroups 叠加：先过白名单，再过黑名单。
func TestGroupAdmissionWhitelistStillHonoursDisabledGroups(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		BotQQ:          "42",
		DisabledGroups: []string{"100"},
		GroupAdmission: GroupAdmission{
			Mode:          GroupAdmissionWhitelist,
			AllowedGroups: []string{"100", "200"},
		},
	}, time.Now())

	if rt.shouldHandle(MessageEvent{Kind: EventKindGroup, GroupID: "100", ToMe: true}, "hi") {
		t.Fatal("白名单内但被单独禁用的群不该触发")
	}
	if !rt.shouldHandle(MessageEvent{Kind: EventKindGroup, GroupID: "200", ToMe: true}, "hi") {
		t.Fatal("白名单内未禁用的群应触发")
	}
}

func TestActiveHoursSameDayWindow(t *testing.T) {
	gate := ReplyGate{ActiveHoursEnabled: true, ActiveStart: "09:00", ActiveEnd: "23:00"}
	day := func(hour, min int) time.Time {
		return time.Date(2026, 8, 4, hour, min, 0, 0, time.Local)
	}
	cases := []struct {
		at   time.Time
		want bool
	}{
		{day(8, 59), false},
		{day(9, 0), true},
		{day(15, 30), true},
		{day(22, 59), true},
		{day(23, 0), false},
		{day(3, 0), false},
	}
	for _, tc := range cases {
		if got := gate.WithinActiveHours(tc.at); got != tc.want {
			t.Errorf("%s 期望 %v，实际 %v", tc.at.Format("15:04"), tc.want, got)
		}
	}
}

// 跨夜时段是最容易写错的分支：end < start 表示窗口跨过零点。
func TestActiveHoursOvernightWindow(t *testing.T) {
	gate := ReplyGate{ActiveHoursEnabled: true, ActiveStart: "22:00", ActiveEnd: "06:00"}
	day := func(hour, min int) time.Time {
		return time.Date(2026, 8, 4, hour, min, 0, 0, time.Local)
	}
	cases := []struct {
		at   time.Time
		want bool
	}{
		{day(21, 59), false},
		{day(22, 0), true},
		{day(23, 30), true},
		{day(0, 30), true},
		{day(5, 59), true},
		{day(6, 0), false},
		{day(12, 0), false},
	}
	for _, tc := range cases {
		if got := gate.WithinActiveHours(tc.at); got != tc.want {
			t.Errorf("%s 期望 %v，实际 %v", tc.at.Format("15:04"), tc.want, got)
		}
	}
}

func TestActiveHoursRespectsTimezone(t *testing.T) {
	gate := ReplyGate{
		ActiveHoursEnabled: true,
		ActiveStart:        "09:00",
		ActiveEnd:          "18:00",
		Timezone:           "Asia/Shanghai",
	}
	// UTC 02:00 == 上海 10:00，在窗口内。
	if !gate.WithinActiveHours(time.Date(2026, 8, 4, 2, 0, 0, 0, time.UTC)) {
		t.Fatal("上海 10:00 应在窗口内")
	}
	// UTC 14:00 == 上海次日 22:00，在窗口外。
	if gate.WithinActiveHours(time.Date(2026, 8, 4, 14, 0, 0, 0, time.UTC)) {
		t.Fatal("上海 22:00 应在窗口外")
	}
}

// 时区名写错时退回本地时区，而不是静默拦下所有消息。
func TestActiveHoursFallsBackOnBadTimezone(t *testing.T) {
	gate := ReplyGate{
		ActiveHoursEnabled: true,
		ActiveStart:        "00:00",
		ActiveEnd:          "23:59",
		Timezone:           "Not/AZone",
	}
	if !gate.WithinActiveHours(time.Date(2026, 8, 4, 12, 0, 0, 0, time.Local)) {
		t.Fatal("时区非法时应退回本地时区并放行")
	}
}

// 时段格式非法时直接关掉时段限制，避免把机器人静默锁死。
func TestActiveHoursDisabledWhenClockInvalid(t *testing.T) {
	gate := ReplyGate{ActiveHoursEnabled: true, ActiveStart: "9点", ActiveEnd: "23:00"}.WithDefaults()
	if gate.ActiveHoursEnabled {
		t.Fatal("非法时段配置应被关闭")
	}
	if !gate.WithinActiveHours(time.Now()) {
		t.Fatal("非法时段配置不应拦截消息")
	}
}

func TestShouldHandleBlocksOutsideActiveHours(t *testing.T) {
	cfg := BotConfig{
		BotQQ: "42",
		ReplyGate: &ReplyGate{
			ActiveHoursEnabled: true,
			ActiveStart:        "09:00",
			ActiveEnd:          "23:00",
		},
	}
	event := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "7", ToMe: true}

	night := gateRuntime(t, cfg, time.Date(2026, 8, 4, 3, 0, 0, 0, time.Local))
	if night.shouldHandle(event, "hi") {
		t.Fatal("静默时段不该回复")
	}
	day := gateRuntime(t, cfg, time.Date(2026, 8, 4, 12, 0, 0, 0, time.Local))
	if !day.shouldHandle(event, "hi") {
		t.Fatal("活跃时段应回复")
	}
}

func TestGroupReplyGateUsesItsOwnActiveHours(t *testing.T) {
	base := BotConfig{
		BotQQ: "42",
		ReplyGate: &ReplyGate{
			ActiveHoursEnabled: true,
			ActiveStart:        "00:00",
			ActiveEnd:          "23:59",
		},
	}
	rt := gateRuntime(t, base, time.Date(2026, 8, 10, 12, 0, 0, 0, time.Local))
	store := &testWritableGroupConfigStore{}
	_, _ = store.SaveGroupConfig(GroupConfig{
		GroupID:    "100",
		Enabled:    true,
		EnabledSet: true,
		ReplyGate: &ReplyGate{
			ActiveHoursEnabled: true,
			ActiveStart:        "18:00",
			ActiveEnd:          "23:00",
		},
	}, base)
	rt.SetGroupConfigStore(store)

	groupWithOverride := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "7", ToMe: true}
	if rt.shouldHandle(groupWithOverride, "hi") {
		t.Fatal("本群自定义回复时间外不应回复")
	}
	groupFollowingGlobal := MessageEvent{Kind: EventKindGroup, GroupID: "200", UserID: "7", ToMe: true}
	if !rt.shouldHandle(groupFollowingGlobal, "hi") {
		t.Fatal("其他群仍应跟随全局回复时间")
	}
}

func TestGroupReplyGateBlocksQQOnlyInConfiguredGroup(t *testing.T) {
	base := BotConfig{BotQQ: "42"}
	rt := gateRuntime(t, base, time.Now())
	store := &testWritableGroupConfigStore{}
	_, _ = store.SaveGroupConfig(GroupConfig{
		GroupID:    "100",
		Enabled:    true,
		EnabledSet: true,
		ReplyGate:  &ReplyGate{BlockedUsers: []string{"12345"}},
	}, base)
	rt.SetGroupConfigStore(store)

	blockedGroup := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "12345", ToMe: true}
	if rt.shouldHandle(blockedGroup, "hi") {
		t.Fatal("被本群屏蔽的 QQ 不应触发回复")
	}
	otherGroup := MessageEvent{Kind: EventKindGroup, GroupID: "200", UserID: "12345", ToMe: true}
	if !rt.shouldHandle(otherGroup, "hi") {
		t.Fatal("本群屏蔽名单不应影响其他群")
	}
	privateChat := MessageEvent{Kind: EventKindPrivate, UserID: "12345"}
	if !rt.shouldHandle(privateChat, "hi") {
		t.Fatal("本群屏蔽名单不应影响私聊")
	}
}

// 主人不受时段限制，否则配错了就把自己锁在门外。
func TestOwnerBypassesActiveHours(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		BotQQ:   "42",
		OwnerID: "1001",
		ReplyGate: &ReplyGate{
			ActiveHoursEnabled: true,
			ActiveStart:        "09:00",
			ActiveEnd:          "23:00",
		},
	}, time.Date(2026, 8, 4, 3, 0, 0, 0, time.Local))

	owner := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "1001", ToMe: true}
	if !rt.shouldHandle(owner, "hi") {
		t.Fatal("主人应绕过时段限制")
	}
	other := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "2002", ToMe: true}
	if rt.shouldHandle(other, "hi") {
		t.Fatal("非主人不该绕过时段限制")
	}
}

func TestOwnerBypassCanBeDisabled(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		BotQQ:   "42",
		OwnerID: "1001",
		ReplyGate: &ReplyGate{
			ActiveHoursEnabled: true,
			ActiveStart:        "09:00",
			ActiveEnd:          "23:00",
			OwnerBypass:        boolPtr(false),
		},
	}, time.Date(2026, 8, 4, 3, 0, 0, 0, time.Local))

	owner := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "1001", ToMe: true}
	if rt.shouldHandle(owner, "hi") {
		t.Fatal("显式关闭后主人也应受时段限制")
	}
}

func TestLevelGateBlocksLowLevelMember(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		BotQQ:     "42",
		ReplyGate: &ReplyGate{MinGroupLevel: 3},
	}, time.Now())

	low := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "7", SenderLevel: 1, ToMe: true}
	if rt.shouldHandle(low, "hi") {
		t.Fatal("等级不足不该触发")
	}
	high := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "8", SenderLevel: 5, ToMe: true}
	if !rt.shouldHandle(high, "hi") {
		t.Fatal("等级达标应触发")
	}
}

// 等级拿不到时默认放行：OneBot 实现差异很大，宁可漏拦也不能整群失联。
func TestLevelGateFailsOpenWhenUnknown(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		BotQQ:     "42",
		ReplyGate: &ReplyGate{MinGroupLevel: 3},
	}, time.Now())

	unknown := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "7", ToMe: true}
	if !rt.shouldHandle(unknown, "hi") {
		t.Fatal("等级未知时应放行")
	}
}

func TestLevelGateCanFailClosed(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		BotQQ: "42",
		ReplyGate: &ReplyGate{
			MinGroupLevel:      3,
			LevelUnknownPolicy: LevelUnknownDeny,
		},
	}, time.Now())

	unknown := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "7", ToMe: true}
	if rt.shouldHandle(unknown, "hi") {
		t.Fatal("策略为 deny 时等级未知应拦截")
	}
}

// 等级门槛只作用于群聊，私聊没有群等级这回事。
func TestLevelGateDoesNotAffectPrivateChat(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		BotQQ: "42",
		ReplyGate: &ReplyGate{
			MinGroupLevel:      6,
			LevelUnknownPolicy: LevelUnknownDeny,
		},
	}, time.Now())

	if !rt.shouldHandle(MessageEvent{Kind: EventKindPrivate, UserID: "7"}, "hi") {
		t.Fatal("私聊不该受群等级门槛影响")
	}
}

func TestBlockedAndExemptUsers(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		BotQQ: "42",
		ReplyGate: &ReplyGate{
			MinGroupLevel: 5,
			BlockedUsers:  []string{"666"},
			ExemptUsers:   []string{"777"},
		},
	}, time.Now())

	blocked := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "666", SenderLevel: 9, ToMe: true}
	if rt.shouldHandle(blocked, "hi") {
		t.Fatal("黑名单用户即便等级够也不该触发")
	}
	exempt := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "777", SenderLevel: 1, ToMe: true}
	if !rt.shouldHandle(exempt, "hi") {
		t.Fatal("豁免用户应绕过等级门槛")
	}
	// 黑名单在私聊同样生效。
	if rt.shouldHandle(MessageEvent{Kind: EventKindPrivate, UserID: "666"}, "hi") {
		t.Fatal("黑名单用户的私聊也不该触发")
	}
}

// 群级门槛整体替换全局，而不是逐字段合并。
func TestGroupReplyGateOverridesGlobal(t *testing.T) {
	rt := gateRuntime(t, BotConfig{
		BotQQ:     "42",
		ReplyGate: &ReplyGate{MinGroupLevel: 9},
	}, time.Now())
	rt.SetGroupConfigStore(staticGroupConfigStore{cfg: GroupConfig{
		GroupID:    "100",
		Enabled:    true,
		EnabledSet: true,
		ReplyGate:  &ReplyGate{MinGroupLevel: 2},
	}})

	event := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "7", SenderLevel: 3, ToMe: true}
	if !rt.shouldHandle(event, "hi") {
		t.Fatal("群级门槛应覆盖全局的更高要求")
	}
	other := MessageEvent{Kind: EventKindGroup, GroupID: "200", UserID: "7", SenderLevel: 3, ToMe: true}
	if other.SenderLevel >= 9 {
		t.Fatal("测试前提有误")
	}
	if rt.shouldHandle(other, "hi") {
		t.Fatal("没有群级配置的群应沿用全局门槛")
	}
}

type staticGroupConfigStore struct {
	cfg GroupConfig
}

func (s staticGroupConfigStore) ConfigForGroup(groupID string) (GroupConfig, bool) {
	if groupID == s.cfg.GroupID {
		return s.cfg, true
	}
	return GroupConfig{}, false
}

func (s staticGroupConfigStore) SaveGroupConfig(cfg GroupConfig) error { return nil }

func TestQuietNoticeIsRateLimited(t *testing.T) {
	now := time.Date(2026, 8, 4, 3, 0, 0, 0, time.Local)
	rt := gateRuntime(t, BotConfig{BotQQ: "42"}, now)
	event := MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "7"}

	if !rt.allowQuietNotice(event) {
		t.Fatal("首次应允许提示")
	}
	if rt.allowQuietNotice(event) {
		t.Fatal("限频窗口内不该重复提示")
	}
	rt.now = func() time.Time { return now.Add(quietNoticeInterval + time.Minute) }
	if !rt.allowQuietNotice(event) {
		t.Fatal("超过限频窗口应重新允许")
	}
	// 不同群互不影响。
	if !rt.allowQuietNotice(MessageEvent{Kind: EventKindGroup, GroupID: "200", UserID: "7"}) {
		t.Fatal("另一个群应独立计频")
	}
}

// 群等级是 QQ 独有的概念。Telegram 上不该为此调用 get_group_member_info，
// 否则每条未命中缓存的消息都会白发一次注定 404 的请求。
func TestLevelGateSkippedOnNonOneBotPlatform(t *testing.T) {
	calls := 0
	newRT := func(platform string) *Runtime {
		rt := gateRuntime(t, BotConfig{
			Platform: platform,
			BotQQ:    "42",
			ReplyGate: &ReplyGate{
				MinGroupLevel:      5,
				LevelUnknownPolicy: "deny",
			},
		}, time.Date(2026, 8, 4, 12, 0, 0, 0, time.Local))
		rt.members = newMemberCache(func(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
			calls++
			return nil, errors.New("unsupported")
		})
		return rt
	}

	event := MessageEvent{Kind: EventKindGroup, GroupID: "1", UserID: "7", ToMe: true}

	// Telegram：门槛整体跳过，消息照常处理，且不查成员信息。
	if !newRT(PlatformTelegram).shouldHandle(event, "hi") {
		t.Fatal("Telegram 上不应被群等级门槛拦下")
	}
	if calls != 0 {
		t.Fatalf("Telegram 上不该查群成员信息，实际调用 %d 次", calls)
	}

	// OneBot：门槛照常生效，deny 策略下拿不到等级就拦截。
	if newRT(PlatformOneBotV11).shouldHandle(event, "hi") {
		t.Fatal("OneBot 上 deny 策略应拦下未知等级")
	}
}
