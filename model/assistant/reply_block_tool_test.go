// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/SuInk/diana/model/llm"
)

// replyBlockMemberChannel 让 canConfigureGroup 的实时成员核验有个可控的答案。
type replyBlockMemberChannel struct {
	nilChannel
	role string
}

func (c *replyBlockMemberChannel) GroupMember(_ context.Context, groupID, userID string) (OneBotGroupMemberInfo, error) {
	return OneBotGroupMemberInfo{GroupID: groupID, UserID: userID, Role: c.role, MembershipVerified: true}, nil
}

func (c *replyBlockMemberChannel) GroupMembers(context.Context, string) (GroupMemberDirectory, error) {
	return GroupMemberDirectory{}, nil
}

// replyBlockTestSaver 是机器人级名单的落库端，failure 用来验证「保存失败就不报成功」。
type replyBlockTestSaver struct {
	blocked []string
	saves   int
	failure error
}

func (*replyBlockTestSaver) SaveBotConfig(BotConfig) {}

func (s *replyBlockTestSaver) SaveBlockedUsers(_ string, userIDs []string) error {
	if s.failure != nil {
		return s.failure
	}
	s.saves++
	s.blocked = append([]string(nil), userIDs...)
	return nil
}

func newReplyBlockRuntime(botGate, groupGate *ReplyGate, memberRole string) (*Runtime, *testWritableGroupConfigStore, *replyBlockTestSaver) {
	base := BotConfig{
		ID: "bot-1", OwnerID: "10001", BotAccount: "42", Platform: PlatformOneBotV11,
		Enabled: true, ReplyGate: botGate,
	}
	var channel Channel = nilChannel{}
	if memberRole != "" {
		channel = &replyBlockMemberChannel{role: memberRole}
	}
	saver := &replyBlockTestSaver{}
	runtime := NewRuntime(base, channel, NewPluginManager(), nil, nil, saver, nil)
	runtime.SetProfiles(ProfileSet{ActiveID: "bot-1", Profiles: []BotConfig{base}})
	store := &testWritableGroupConfigStore{}
	if groupGate != nil {
		_, _ = store.SaveGroupConfig(GroupConfig{BotProfileID: "bot-1", GroupID: "g1", Enabled: true, EnabledSet: true, ReplyGate: groupGate}, base)
	}
	runtime.SetGroupConfigStore(store)
	return runtime, store, saver
}

func replyBlockEvent(userID string) MessageEvent {
	return MessageEvent{ProfileID: "bot-1", Platform: PlatformOneBotV11, Kind: EventKindGroup, GroupID: "g1", UserID: userID, SelfID: "42"}
}

type replyBlockResult struct {
	Changed     bool     `json:"changed"`
	Blocked     []string `json:"blocked_users"`
	Inherited   []string `json:"inherited_blocked_users"`
	Message     string   `json:"message"`
	OperatorRle string   `json:"operator_role"`
	Scope       string   `json:"scope"`
}

// TestReplyBlockTool 覆盖这条指令的全部出口：屏蔽、解除、查看、重复屏蔽、
// 拒绝屏蔽主人和本机、非管理员无权、非主人改不了机器人级名单。
func TestReplyBlockTool(t *testing.T) {
	tests := []struct {
		name        string
		botGate     *ReplyGate
		groupGate   *ReplyGate
		memberRole  string
		actor       string
		input       map[string]any
		wantErr     string
		wantChanged bool
		wantBlocked []string
		wantSilent  string
		wantAudible string
		wantMessage string
	}{
		{
			name: "主人屏蔽群友后对方彻底没声音", actor: "10001",
			input:       map[string]any{"operation": "block", "user_id": "20001"},
			wantChanged: true, wantBlocked: []string{"20001"}, wantSilent: "20001",
			wantMessage: "已屏蔽该用户",
		},
		{
			name: "群主经实时核验后可以屏蔽", memberRole: "owner", actor: "30001",
			input:       map[string]any{"operation": "block", "user_id": "20001"},
			wantChanged: true, wantBlocked: []string{"20001"}, wantSilent: "20001",
		},
		{
			name: "解除屏蔽后恢复回复", actor: "10001",
			groupGate:   &ReplyGate{BlockedUsers: []string{"20001", "20002"}},
			input:       map[string]any{"operation": "unblock", "user_id": "20001"},
			wantChanged: true, wantBlocked: []string{"20002"}, wantAudible: "20001",
			wantMessage: "已解除屏蔽",
		},
		{
			name: "重复屏蔽不是错误，只报当前状态", actor: "10001",
			groupGate:   &ReplyGate{BlockedUsers: []string{"20001"}},
			input:       map[string]any{"operation": "block", "user_id": "20001"},
			wantChanged: false, wantBlocked: []string{"20001"}, wantSilent: "20001",
			wantMessage: "此前已在本群屏蔽名单里",
		},
		{
			name: "解除一个本来就没屏蔽的人也不是错误", actor: "10001",
			input:       map[string]any{"operation": "unblock", "user_id": "20009"},
			wantChanged: false, wantAudible: "20009",
			wantMessage: "本来就不在本群屏蔽名单里",
		},
		{
			name: "查看名单要能分出哪些是机器人级并下来的", actor: "10001",
			botGate:     &ReplyGate{BlockedUsers: []string{"20003"}},
			groupGate:   &ReplyGate{BlockedUsers: []string{"20001"}},
			input:       map[string]any{"operation": "list"},
			wantBlocked: []string{"20001"}, wantSilent: "20003",
		},
		{
			name: "机器人级屏蔽的人在群里解不掉", actor: "10001",
			botGate: &ReplyGate{BlockedUsers: []string{"20003"}},
			input:   map[string]any{"operation": "unblock", "user_id": "20003"},
			wantErr: "scope=bot",
		},
		{
			name: "不能屏蔽机器人主人", actor: "10001",
			input:   map[string]any{"operation": "block", "user_id": "10001"},
			wantErr: "不能屏蔽机器人主人",
		},
		{
			name: "不能屏蔽机器人自己", actor: "10001",
			input:   map[string]any{"operation": "block", "user_id": "42"},
			wantErr: "不能屏蔽机器人自己的账号",
		},
		{
			name: "普通群友无权屏蔽别人", memberRole: "member", actor: "30002",
			input:   map[string]any{"operation": "block", "user_id": "20001"},
			wantErr: "只有机器人主人、群主或群管理员可以配置本群",
		},
		{
			name: "群管理员也改不了机器人级名单", memberRole: "admin", actor: "30001",
			input:   map[string]any{"operation": "block", "scope": "bot", "user_id": "20001"},
			wantErr: "只有机器人主人可以修改机器人级屏蔽名单",
		},
		{
			name: "昵称不能当账号 ID 用", actor: "10001",
			input:   map[string]any{"operation": "block", "user_id": "隔壁老王"},
			wantErr: "不能使用昵称",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, _, _ := newReplyBlockRuntime(test.botGate, test.groupGate, test.memberRole)
			raw, err := newDianaReplyBlockTool(runtime, replyBlockEvent(test.actor)).Run(context.Background(), test.input)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v，应该包含 %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var result replyBlockResult
			if err := json.Unmarshal([]byte(raw), &result); err != nil {
				t.Fatal(err)
			}
			if result.Changed != test.wantChanged {
				t.Fatalf("changed = %v，应该是 %v：%s", result.Changed, test.wantChanged, raw)
			}
			if test.wantBlocked != nil && !slices.Equal(result.Blocked, test.wantBlocked) {
				t.Fatalf("blocked_users = %v，应该是 %v", result.Blocked, test.wantBlocked)
			}
			if test.wantMessage != "" && !strings.Contains(result.Message, test.wantMessage) {
				t.Fatalf("message = %q，应该包含 %q", result.Message, test.wantMessage)
			}
			// 名单只是记录，真正要验的是保存之后门禁当场就认：写完还要重新读一次
			// 生效配置，不能只信工具自己返回的那份。
			cfg := runtime.effectiveConfigForEvent(replyBlockEvent(test.actor))
			if test.wantSilent != "" && runtime.replyGateAllows(cfg, replyBlockEvent(test.wantSilent)) {
				t.Fatalf("%s 被屏蔽后仍然能触发回复", test.wantSilent)
			}
			if test.wantAudible != "" && !runtime.replyGateAllows(cfg, replyBlockEvent(test.wantAudible)) {
				t.Fatalf("%s 不该被拦下", test.wantAudible)
			}
		})
	}
}

// TestReplyBlockKeepsBotLevelThresholds 为本群新建门禁不能顺手放开门槛。
//
// 群级门槛是整份替换的（见 ReplyGate.MergedWith）。屏蔽一个人时如果直接新建一份
// 只装名单的空门禁，机器人级的回复时段和等级门槛在这个群就一起归零——本意是「少
// 回一个人」，结果是「这个群从此 24 小时谁都能触发」，而且界面上看不出来。
func TestReplyBlockKeepsBotLevelThresholds(t *testing.T) {
	botGate := &ReplyGate{MinGroupLevel: 5, ActiveHoursEnabled: true, ActiveStart: "09:00", ActiveEnd: "18:00", ExemptUsers: []string{"20008"}}
	runtime, store, _ := newReplyBlockRuntime(botGate, nil, "")
	if _, err := newDianaReplyBlockTool(runtime, replyBlockEvent("10001")).Run(context.Background(), map[string]any{"operation": "block", "user_id": "20001"}); err != nil {
		t.Fatal(err)
	}
	saved, ok := store.set.ConfigForGroup("bot-1", "g1")
	if !ok || saved.ReplyGate == nil {
		t.Fatal("群配置没有写入屏蔽名单")
	}
	if !saved.ReplyGate.IsBlocked("20001") {
		t.Fatalf("屏蔽名单没保存：%#v", saved.ReplyGate)
	}
	if saved.ReplyGate.MinGroupLevel != 5 || !saved.ReplyGate.ActiveHoursEnabled || saved.ReplyGate.ActiveStart != "09:00" {
		t.Fatalf("屏蔽一个人把本群的等级门槛和回复时段一起放开了：%#v", saved.ReplyGate)
	}
	// 名单不抄下来，本群那份只装本群自己加的人；机器人级的豁免仍然靠并集生效。
	if len(saved.ReplyGate.ExemptUsers) != 0 {
		t.Fatalf("机器人级的豁免名单被抄进了群配置：%#v", saved.ReplyGate.ExemptUsers)
	}
	if gate := runtime.effectiveConfigForEvent(replyBlockEvent("20008")).ReplyGate; !gate.IsExempt("20008") {
		t.Fatalf("机器人级豁免没有并进本群：%#v", gate)
	}
}

// TestReplyBlockBotScopeReportsOnlySavedState 机器人级名单：主人能改，保存失败不能报成功。
func TestReplyBlockBotScopeReportsOnlySavedState(t *testing.T) {
	runtime, _, saver := newReplyBlockRuntime(nil, nil, "")
	tool := newDianaReplyBlockTool(runtime, replyBlockEvent("10001"))
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "block", "scope": "bot", "user_id": "20001"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(saver.blocked, []string{"20001"}) {
		t.Fatalf("机器人级名单没落库：%v", saver.blocked)
	}
	// 机器人级屏蔽对私聊同样生效，不只是群里。
	private := MessageEvent{ProfileID: "bot-1", Platform: PlatformOneBotV11, Kind: EventKindPrivate, UserID: "20001"}
	if runtime.admits(runtime.effectiveConfigForEvent(private), private) {
		t.Fatal("机器人级屏蔽在私聊里没生效")
	}
	if runtime.replyGateAllows(runtime.effectiveConfigForEvent(replyBlockEvent("20001")), replyBlockEvent("20001")) {
		t.Fatal("机器人级屏蔽在群里没生效")
	}

	saver.failure = errors.New("disk full")
	if _, err := tool.Run(context.Background(), map[string]any{"operation": "block", "scope": "bot", "user_id": "20002"}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("保存失败却报了成功：%v", err)
	}
	if slices.Contains(saver.blocked, "20002") || runtime.Config().ReplyGate.IsBlocked("20002") {
		t.Fatal("保存失败后名单仍被改了")
	}
}

// TestReplyBlockToolReachesGroupAdmins 群管理员得先看得见这个工具。
//
// 工具自己会核验身份，但非主人的工具表是白名单制的：名字不收录进
// allowedAgentToolNames，群主在群里说「以后别理他」就只能等主人来处理。
func TestReplyBlockToolReachesGroupAdmins(t *testing.T) {
	runtime, _, _ := newReplyBlockRuntime(nil, nil, "owner")
	event := replyBlockEvent("30001")
	registry, err := runtime.newAgentRegistry(context.Background(), runtime.Config(), event,
		RelationshipPolicyFor(UserMemoryProfile{}, "10001", "30001"), newDianaReplyBlockTool(runtime, event))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, ok := registry.Get(replyBlockToolName); !ok {
		t.Fatal("群管理员看不到屏蔽工具")
	}
}

type replyBlockScoringProbe struct {
	mu    sync.Mutex
	calls int
}

func (p *replyBlockScoringProbe) Generate(context.Context, llm.GenerateRequest) (*llm.GenerateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return &llm.GenerateResponse{Text: `{"should_reply":false}`}, nil
}

func (p *replyBlockScoringProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// TestBlockedUserIsDroppedBeforeScoring 被屏蔽的人要在花掉任何一次模型调用之前
// 就被挡掉，事件里记下的理由还得说清是「被屏蔽了」，而不是一句笼统的权限规则。
func TestBlockedUserIsDroppedBeforeScoring(t *testing.T) {
	probe := &replyBlockScoringProbe{}
	base := BotConfig{
		ID: "bot-1", OwnerID: "10001", BotAccount: "42", Platform: PlatformOneBotV11, Enabled: true,
		ReplyGate: &ReplyGate{BlockedUsers: []string{"20001"}},
	}
	runtime := NewRuntime(base, nilChannel{}, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return probe, nil })
	runtime.SetProfiles(ProfileSet{ActiveID: "bot-1", Profiles: []BotConfig{base}})

	message := func(userID, messageID string) MessageEvent {
		event := replyBlockEvent(userID)
		event.MessageID = messageID
		event.RawMessage = "今天天气不错啊"
		event.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": "今天天气不错啊"}}}
		return event
	}

	_, _, handled, outcome := runtime.prepareMessageEvent(context.Background(), message("20001", "m-1"))
	if handled || outcome != "ignored_user_blocked" {
		t.Fatalf("handled=%v outcome=%q", handled, outcome)
	}
	if got := probe.count(); got != 0 {
		t.Fatalf("被屏蔽的人还是触发了 %d 次评分模型调用", got)
	}
	recent := runtime.Status().RecentEvents
	if len(recent) == 0 {
		t.Fatal("没有记录入站事件")
	}
	if recent[0].Outcome != "ignored_user_blocked" || recent[0].Decision != "not_replied" {
		t.Fatalf("事件结论 = %q/%q", recent[0].Decision, recent[0].Outcome)
	}
	if recent[0].Reason != replyBlockedDecisionReason {
		t.Fatalf("事件理由 = %q，应该是 %q", recent[0].Reason, replyBlockedDecisionReason)
	}

	// 对照组：同样一条闲聊，没被屏蔽的人是会走到评分模型的——否则上面那个 0 次
	// 可能只是因为这条消息本来就进不了主动回复判断，证明不了屏蔽拦在了前面。
	if _, _, _, outcome = runtime.prepareMessageEvent(context.Background(), message("20002", "m-2")); outcome == "ignored_user_blocked" {
		t.Fatal("没被屏蔽的人也被当成屏蔽处理了")
	}
	if probe.count() == 0 {
		t.Fatal("没被屏蔽的人也没进评分，这个用例证明不了屏蔽的位置")
	}
}
