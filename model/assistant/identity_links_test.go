// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// memoryIdentityLinkStore 是和 SQLite 实现同语义的内存版：一边有归属就并进那一边，
// 两边分属不同的人时拒绝。
type memoryIdentityLinkStore struct {
	mu   sync.Mutex
	next int
	rows map[string]IdentityAccount
}

func newMemoryIdentityLinkStore() *memoryIdentityLinkStore {
	return &memoryIdentityLinkStore{rows: map[string]IdentityAccount{}}
}

func (s *memoryIdentityLinkStore) ListIdentityLinks(context.Context) ([]IdentityAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]IdentityAccount, 0, len(s.rows))
	for _, row := range s.rows {
		out = append(out, row)
	}
	return out, nil
}

func (s *memoryIdentityLinkStore) group(person string) []IdentityAccount {
	var out []IdentityAccount
	for _, row := range s.rows {
		if row.PersonID == person {
			out = append(out, row)
		}
	}
	return out
}

func (s *memoryIdentityLinkStore) LinkIdentityAccounts(_ context.Context, a, b IdentityAccount) ([]IdentityAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, b = a.normalized(), b.normalized()
	pa, pb := s.rows[a.key()].PersonID, s.rows[b.key()].PersonID
	person := pa
	switch {
	case pa != "" && pb != "" && pa != pb:
		return nil, ErrIdentityLinkConflict
	case pa == "" && pb == "":
		s.next++
		person = fmt.Sprintf("person-%d", s.next)
	case pa == "":
		person = pb
	}
	for _, account := range []IdentityAccount{a, b} {
		if _, ok := s.rows[account.key()]; !ok {
			account.PersonID = person
			account.CreatedAt = time.Now()
			s.rows[account.key()] = account
		}
	}
	return s.group(person), nil
}

func (s *memoryIdentityLinkStore) UnlinkIdentityAccount(_ context.Context, account IdentityAccount) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account = account.normalized()
	row, ok := s.rows[account.key()]
	if !ok {
		return false, nil
	}
	delete(s.rows, account.key())
	if rest := s.group(row.PersonID); len(rest) < 2 {
		for _, item := range rest {
			delete(s.rows, item.key())
		}
	}
	return true, nil
}

func (s *memoryIdentityLinkStore) LinkedIdentityAccounts(_ context.Context, account IdentityAccount) ([]IdentityAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[account.normalized().key()]
	if !ok {
		return nil, nil
	}
	return s.group(row.PersonID), nil
}

// profileUserMemoryStore 按 机器人+账号 存画像，和真库的归属规则一致。
type profileUserMemoryStore struct {
	profiles map[string]UserMemoryProfile
}

func (s *profileUserMemoryStore) UpdateUserMemory(_ context.Context, event MessageEvent, update UserMemoryUpdate) (UserMemoryProfile, error) {
	key := event.ProfileID + "\x00" + event.UserID
	profile, ok := s.profiles[key]
	if !ok {
		profile = UserMemoryProfile{BotProfileID: event.ProfileID, UserID: event.UserID, Favorability: DefaultUserFavorability}
	}
	profile.Favorability += update.FavorabilityDelta
	s.profiles[key] = profile
	return profile, nil
}

func (s *profileUserMemoryStore) GetUserMemory(_ context.Context, botProfileID, userID string) (UserMemoryProfile, bool, error) {
	profile, ok := s.profiles[botProfileID+"\x00"+userID]
	return profile, ok, nil
}

const (
	identityTestQQOwner   = "10001"
	identityTestQQBot     = "20002"
	identityTestTGUser    = "30003"
	identityTestTGBot     = "40004"
	identityTestStranger  = "50005"
	identityTestMarkedBot = "60006"
)

// newIdentityLinkTestRuntime 起一个两台机器人的实例：QQ（OneBot，配了主人）和
// Telegram（没配主人）。
func newIdentityLinkTestRuntime(t *testing.T, mutate func(qq, tg *BotConfig)) (*Runtime, *recordingChannel, *memoryIdentityLinkStore) {
	t.Helper()
	channel := &recordingChannel{}
	qq := BotConfig{ID: "qq", Platform: PlatformOneBotV11, Enabled: true, OwnerID: identityTestQQOwner, BotAccount: identityTestQQBot, MarkedBotIDs: []string{identityTestMarkedBot}}
	tg := BotConfig{ID: "tg", Platform: PlatformTelegram, Enabled: true, BotAccount: identityTestTGBot}
	if mutate != nil {
		mutate(&qq, &tg)
	}
	r := NewRuntime(qq, channel, NewPluginManager(), nil, nil, nil, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{qq, tg}})
	store := newMemoryIdentityLinkStore()
	r.SetIdentityLinkStore(store)
	return r, channel, store
}

func identityTestEvent(profile, platform string, kind EventKind, userID, text string) MessageEvent {
	event := MessageEvent{
		Kind: kind, ProfileID: profile, ContextNamespace: profile, Platform: platform, UserID: userID,
		SenderName: "Alice", MessageID: fmt.Sprintf("m-%s-%s-%d", profile, userID, time.Now().UnixNano()),
		RawMessage: text, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}},
	}
	if kind == EventKindGroup {
		event.GroupID = "77001"
		event.ToMe = true
	}
	return event
}

func lastIdentityReply(t *testing.T, channel *recordingChannel) string {
	t.Helper()
	sent := channel.sentSnapshot()
	if len(sent) == 0 {
		t.Fatal("bot sent nothing")
	}
	return sent[len(sent)-1].Text
}

func identityBindCodeFrom(t *testing.T, reply string) string {
	t.Helper()
	code := regexp.MustCompile(`\d{6}`).FindString(reply)
	if code == "" {
		t.Fatalf("no code in reply %q", reply)
	}
	return code
}

// bindQQOwnerToTelegram 走一遍自助绑定：QQ 私聊取码，Telegram 私聊兑码。
func bindQQOwnerToTelegram(t *testing.T, r *Runtime, channel *recordingChannel) string {
	t.Helper()
	ctx := context.Background()
	if !r.handleIdentityBind(ctx, identityTestEvent("qq", PlatformOneBotV11, EventKindPrivate, identityTestQQOwner, "绑定账号")) {
		t.Fatal("issue command not handled")
	}
	code := identityBindCodeFrom(t, lastIdentityReply(t, channel))
	redeem := identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestTGUser, "绑定账号 "+code)
	redeem.SenderUsername = "alice_tg"
	if !r.handleIdentityBind(ctx, redeem) {
		t.Fatal("redeem command not handled")
	}
	return lastIdentityReply(t, channel)
}

func TestIdentityBindLinksAccountsAcrossBotsAndSharesOwner(t *testing.T) {
	r, channel, store := newIdentityLinkTestRuntime(t, nil)
	tgEvent := identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestTGUser, "你好")
	if r.withLinkedOwner(tgEvent).linkedOwner {
		t.Fatal("unlinked account must not be owner")
	}

	reply := bindQQOwnerToTelegram(t, r, channel)
	if !strings.Contains(reply, "已关联") || !strings.Contains(reply, "QQ") || !strings.Contains(reply, "主人") {
		t.Fatalf("unexpected redeem reply %q", reply)
	}
	rows, _ := store.ListIdentityLinks(context.Background())
	if len(rows) != 2 || rows[0].PersonID != rows[1].PersonID {
		t.Fatalf("store rows = %#v", rows)
	}

	// 关联到 QQ 机器人的主人后，在没配主人的 Telegram 机器人上按主人认，各条权限入口都一致。
	marked := r.withLinkedOwner(tgEvent)
	cfg := r.effectiveConfigForEvent(marked)
	if !cfg.IsOwnerEvent(marked) || !cfg.IsManagerEvent(marked) || !r.relationshipPolicy(context.Background(), marked).Owner {
		t.Fatal("linked account should be owner on the Telegram bot")
	}
	if cfg.IsAdminEvent(marked) {
		t.Fatal("linked owner must not also count as admin")
	}
	// 主人配置本身不被改写：私聊通知仍然发给配置里的那个号。
	if r.profileConfig("tg").OwnerID != "" {
		t.Fatal("owner config must not be rewritten")
	}
	// 机器人消息、自己发出的消息不认。
	botEvent := tgEvent
	botEvent.SenderIsBot = true
	if r.withLinkedOwner(botEvent).linkedOwner {
		t.Fatal("bot messages must not inherit owner")
	}
	// 关联标记只对它算出来的那台机器人有效。
	if r.profileConfig("qq").IsOwnerEvent(marked) {
		t.Fatal("linked flag must not apply to another bot's config")
	}

	// 解除关联后重新进门就不再是主人。
	if _, err := r.UnlinkIdentityAccount(context.Background(), IdentityAccount{ProfileID: "tg", Platform: PlatformTelegram, UserID: identityTestTGUser}); err != nil {
		t.Fatal(err)
	}
	if r.withLinkedOwner(tgEvent).linkedOwner {
		t.Fatal("unlinked account must lose owner")
	}
}

func TestIdentityBindLinkedOwnerRespectsConfiguredOwners(t *testing.T) {
	// Telegram 机器人配了别的主人：关联到 QQ 主人不能顶替它。
	r, channel, _ := newIdentityLinkTestRuntime(t, func(_, tg *BotConfig) { tg.OwnerID = "39999" })
	bindQQOwnerToTelegram(t, r, channel)
	tgEvent := identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestTGUser, "你好")
	if r.withLinkedOwner(tgEvent).linkedOwner {
		t.Fatal("configured owner of the Telegram bot must win over a linked owner of another bot")
	}

	// 实例里还有另一位主人时，没配主人的机器人不交给其中任何一位。
	r, channel, _ = newIdentityLinkTestRuntime(t, nil)
	r.SetProfiles(ProfileSet{Profiles: []BotConfig{
		r.profileConfig("qq"), r.profileConfig("tg"),
		{ID: "qq2", Platform: PlatformOneBotV11, Enabled: true, OwnerID: "10777"},
	}})
	bindQQOwnerToTelegram(t, r, channel)
	if r.withLinkedOwner(tgEvent).linkedOwner {
		t.Fatal("ownerless bot must not be inherited when the instance has several owners")
	}
}

func TestIdentityBindCodeExpiresAndIsSingleUse(t *testing.T) {
	r, channel, store := newIdentityLinkTestRuntime(t, nil)
	now := time.Unix(1_790_000_000, 0)
	r.now = func() time.Time { return now }
	ctx := context.Background()
	r.handleIdentityBind(ctx, identityTestEvent("qq", PlatformOneBotV11, EventKindPrivate, identityTestQQOwner, "绑定账号"))
	code := identityBindCodeFrom(t, lastIdentityReply(t, channel))
	now = now.Add(identityBindCodeTTL + time.Second)
	r.handleIdentityBind(ctx, identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestTGUser, "绑定账号 "+code))
	if reply := lastIdentityReply(t, channel); !strings.Contains(reply, "已过期") {
		t.Fatalf("expired code reply %q", reply)
	}
	if rows, _ := store.ListIdentityLinks(ctx); len(rows) != 0 {
		t.Fatalf("expired code must not link, rows=%#v", rows)
	}

	// 同一台机器人上兑自己的码不行，码同时作废。
	r.handleIdentityBind(ctx, identityTestEvent("qq", PlatformOneBotV11, EventKindPrivate, identityTestQQOwner, "绑定账号"))
	code = identityBindCodeFrom(t, lastIdentityReply(t, channel))
	r.handleIdentityBind(ctx, identityTestEvent("qq", PlatformOneBotV11, EventKindPrivate, identityTestQQOwner, "绑定账号 "+code))
	if reply := lastIdentityReply(t, channel); !strings.Contains(reply, "另一个平台") {
		t.Fatalf("self redeem reply %q", reply)
	}
	r.handleIdentityBind(ctx, identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestTGUser, "绑定账号 "+code))
	if reply := lastIdentityReply(t, channel); !strings.Contains(reply, "已过期") {
		t.Fatalf("used code must be gone, reply %q", reply)
	}
}

func TestIdentityBindRateLimitsWrongCodes(t *testing.T) {
	r, channel, _ := newIdentityLinkTestRuntime(t, nil)
	ctx := context.Background()
	r.handleIdentityBind(ctx, identityTestEvent("qq", PlatformOneBotV11, EventKindPrivate, identityTestQQOwner, "绑定账号"))
	code := identityBindCodeFrom(t, lastIdentityReply(t, channel))
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	for range identityBindAccountFailureLimit {
		r.handleIdentityBind(ctx, identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestStranger, "绑定账号 "+wrong))
	}
	r.handleIdentityBind(ctx, identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestStranger, "绑定账号 "+code))
	if reply := lastIdentityReply(t, channel); !strings.Contains(reply, "次数太多") {
		t.Fatalf("guessing account should be locked out, reply %q", reply)
	}
}

func TestIdentityBindNotIssuedInGroupAndLeakedCodeRevoked(t *testing.T) {
	r, channel, store := newIdentityLinkTestRuntime(t, nil)
	ctx := context.Background()
	if !r.handleIdentityBind(ctx, identityTestEvent("qq", PlatformOneBotV11, EventKindGroup, identityTestQQOwner, "绑定账号")) {
		t.Fatal("group command addressed to the bot should be answered")
	}
	if reply := lastIdentityReply(t, channel); regexp.MustCompile(`\d{6}`).MatchString(reply) || !strings.Contains(reply, "私聊") {
		t.Fatalf("group reply must not carry a code: %q", reply)
	}
	// 没 @ 机器人的群消息不回话。
	before := len(channel.sentSnapshot())
	quiet := identityTestEvent("qq", PlatformOneBotV11, EventKindGroup, identityTestQQOwner, "绑定账号")
	quiet.ToMe = false
	if r.handleIdentityBind(ctx, quiet) || len(channel.sentSnapshot()) != before {
		t.Fatal("unaddressed group message must pass through silently")
	}

	r.handleIdentityBind(ctx, identityTestEvent("qq", PlatformOneBotV11, EventKindPrivate, identityTestQQOwner, "绑定账号"))
	code := identityBindCodeFrom(t, lastIdentityReply(t, channel))
	r.handleIdentityBind(ctx, identityTestEvent("tg", PlatformTelegram, EventKindGroup, identityTestTGUser, "绑定账号 "+code))
	if reply := lastIdentityReply(t, channel); !strings.Contains(reply, "已作废") {
		t.Fatalf("leaked code reply %q", reply)
	}
	r.handleIdentityBind(ctx, identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestTGUser, "绑定账号 "+code))
	if rows, _ := store.ListIdentityLinks(ctx); len(rows) != 0 {
		t.Fatalf("code leaked in a group must not link, rows=%#v", rows)
	}
}

func TestIdentityBindRejectsBotAccounts(t *testing.T) {
	r, channel, store := newIdentityLinkTestRuntime(t, nil)
	ctx := context.Background()
	for _, id := range []string{identityTestQQBot, identityTestMarkedBot} {
		r.handleIdentityBind(ctx, identityTestEvent("qq", PlatformOneBotV11, EventKindPrivate, id, "绑定账号"))
		if reply := lastIdentityReply(t, channel); !strings.Contains(reply, "机器人账号") {
			t.Fatalf("bot account %s got reply %q", id, reply)
		}
	}
	_, err := r.LinkIdentityAccounts(ctx,
		IdentityAccount{ProfileID: "qq", Platform: PlatformOneBotV11, UserID: identityTestQQOwner},
		IdentityAccount{ProfileID: "tg", Platform: PlatformTelegram, UserID: identityTestTGBot})
	if err == nil || !strings.Contains(err.Error(), "机器人账号") {
		t.Fatalf("linking a bot account err=%v", err)
	}
	if rows, _ := store.ListIdentityLinks(ctx); len(rows) != 0 {
		t.Fatalf("bot accounts must not be stored, rows=%#v", rows)
	}
}

func TestIdentityLinksToolOwnerOnly(t *testing.T) {
	r, _, store := newIdentityLinkTestRuntime(t, func(qq, _ *BotConfig) { qq.AdminIDs = []string{"10888"} })
	ctx := context.Background()
	input := map[string]any{"operation": "link", "user_id": identityTestQQOwner, "other_profile_id": "tg", "other_user_id": identityTestTGUser}

	for _, id := range []string{identityTestStranger, "10888"} {
		tool := newDianaIdentityLinksTool(r, identityTestEvent("qq", PlatformOneBotV11, EventKindPrivate, id, "关联"))
		if _, err := tool.Run(ctx, input); err == nil {
			t.Fatalf("%s must not manage identity links", id)
		}
	}
	if rows, _ := store.ListIdentityLinks(ctx); len(rows) != 0 {
		t.Fatalf("rejected calls must not write, rows=%#v", rows)
	}

	owner := newDianaIdentityLinksTool(r, identityTestEvent("qq", PlatformOneBotV11, EventKindPrivate, identityTestQQOwner, "关联"))
	out, err := owner.Run(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var linked struct {
		Person IdentityPerson `json:"person"`
	}
	if err := json.Unmarshal([]byte(out), &linked); err != nil || len(linked.Person.Accounts) != 2 {
		t.Fatalf("link result %s err=%v", out, err)
	}
	out, err = owner.Run(ctx, map[string]any{"operation": "list"})
	if err != nil || !strings.Contains(out, identityTestTGUser) || !strings.Contains(out, `"profile_id":"tg"`) {
		t.Fatalf("list result %s err=%v", out, err)
	}
	if _, err := owner.Run(ctx, map[string]any{"operation": "link", "user_id": identityTestQQOwner, "other_profile_id": "nope", "other_user_id": "1"}); err == nil {
		t.Fatal("unknown bot must be rejected")
	}
	if _, err := owner.Run(ctx, map[string]any{"operation": "unlink", "profile_id": "tg", "user_id": identityTestTGUser}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := store.ListIdentityLinks(ctx); len(rows) != 0 {
		t.Fatalf("unlink should drop the lone account too, rows=%#v", rows)
	}
}

func TestIdentityLinksToolRegisteredForOwnerOnlyAndBlockedInSafeMode(t *testing.T) {
	r, _, _ := newIdentityLinkTestRuntime(t, func(qq, _ *BotConfig) {
		qq.AgentEnabled = true
		qq.AdminIDs = []string{"10888"}
	})
	ctx := context.Background()
	has := func(userID string) bool {
		event := identityTestEvent("qq", PlatformOneBotV11, EventKindPrivate, userID, "hi")
		cfg := r.effectiveConfigForEvent(event)
		registry, err := r.newReplyAgentRegistry(ctx, cfg, event, r.relationshipPolicy(ctx, event), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, ok := registry.Get(dianaIdentityLinksToolName)
		return ok
	}
	if !has(identityTestQQOwner) {
		t.Fatal("owner should get identity_links")
	}
	if has("10888") || has(identityTestStranger) {
		t.Fatal("admins and members must not get identity_links")
	}
	found := false
	for _, rule := range AgentSafeModeRules {
		if rule.Tool == dianaIdentityLinksToolName {
			found = strings.Join(rule.Operations, ",") == "link,unlink"
		}
	}
	if !found {
		t.Fatal("safe mode must block identity_links link/unlink")
	}
}

func TestIdentityLinksMergeFavorabilityAndPrompt(t *testing.T) {
	r, channel, _ := newIdentityLinkTestRuntime(t, nil)
	memory := &profileUserMemoryStore{profiles: map[string]UserMemoryProfile{
		"qq\x00" + identityTestQQOwner: {BotProfileID: "qq", UserID: identityTestQQOwner, Favorability: 60, MessageCount: 5,
			Portrait: []UserPortraitTrait{{Field: "city", Value: "杭州"}}},
		"tg\x00" + identityTestTGUser: {BotProfileID: "tg", UserID: identityTestTGUser, Favorability: 25, MessageCount: 2},
	}}
	r.SetUserMemoryStore(memory)
	bindQQOwnerToTelegram(t, r, channel)

	event := r.withLinkedOwner(identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestTGUser, "你好"))
	profile, ok := r.loadUserMemoryProfile(context.Background(), event)
	// 25 + (60 - 10)：只累加关联账号相对起始分的变化。
	if !ok || profile.Favorability != 75 || profile.MessageCount != 7 || len(profile.Portrait) != 1 {
		t.Fatalf("merged profile = %#v ok=%v", profile, ok)
	}
	// 写仍只写当前账号。
	if _, ok := r.writeUserMemory(event, UserMemoryUpdate{FavorabilityDelta: 3}); !ok {
		t.Fatal("write failed")
	}
	if memory.profiles["qq\x00"+identityTestQQOwner].Favorability != 60 || memory.profiles["tg\x00"+identityTestTGUser].Favorability != 28 {
		t.Fatalf("write must stay on the current account: %#v", memory.profiles)
	}

	// 当前账号还没建档：按起始分算。
	fresh := &profileUserMemoryStore{profiles: map[string]UserMemoryProfile{
		"qq\x00" + identityTestQQOwner: {BotProfileID: "qq", UserID: identityTestQQOwner, Favorability: -40, MessageCount: 9},
	}}
	r.SetUserMemoryStore(fresh)
	profile, _ = r.loadUserMemoryProfile(context.Background(), event)
	if profile.Favorability != -40 {
		t.Fatalf("negative favorability must carry over, got %d", profile.Favorability)
	}

	prompt := r.linkedIdentityPrompt(event, r.effectiveConfigForEvent(event))
	if !strings.Contains(prompt, "QQ Alice（"+identityTestQQOwner+"）") {
		t.Fatalf("linked prompt = %q", prompt)
	}
	stranger := identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestStranger, "你好")
	if r.linkedIdentityPrompt(stranger, r.effectiveConfigForEvent(stranger)) != "" {
		t.Fatal("unlinked speakers get no linked-account line")
	}
	// 只进尾部：头部不随关联变化。
	head, tail := r.systemPromptPartsWithRelationshipAndAgentTools(event, nil, false, r.relationshipPolicy(context.Background(), event), false, nil)
	if strings.Contains(head, identityTestQQOwner) || !strings.Contains(tail, "QQ Alice（"+identityTestQQOwner+"）") {
		t.Fatalf("linked accounts must live in the tail only")
	}
}

func TestIdentityLinksPersonalMemoryQueryAndRanking(t *testing.T) {
	r, channel, _ := newIdentityLinkTestRuntime(t, nil)
	bindQQOwnerToTelegram(t, r, channel)
	event := identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestTGUser, "喝什么")
	query := r.memoryQueryForEvent(event, "喝什么", 20)
	if len(query.LinkedSubjects) != 1 || query.LinkedSubjects[0] != (StructuredMemorySubject{Namespace: "qq:", UserID: identityTestQQOwner}) {
		t.Fatalf("linked subjects = %#v", query.LinkedSubjects)
	}
	stranger := identityTestEvent("tg", PlatformTelegram, EventKindPrivate, identityTestStranger, "喝什么")
	if subjects := r.memoryQueryForEvent(stranger, "喝什么", 20).LinkedSubjects; len(subjects) != 0 {
		t.Fatalf("stranger must not read linked memories: %#v", subjects)
	}

	items := []StructuredMemoryItem{
		{ID: "own", SubjectUserID: identityTestQQOwner, Visibility: MemoryVisibilityUser, SourceSession: "qq:private:" + identityTestQQOwner,
			Kind: MemoryKindInstruction, Content: "回复永远带颜文字", Confidence: 0.95, Importance: 0.9},
		{ID: "session", SubjectUserID: identityTestQQOwner, Visibility: MemoryVisibilitySession, SourceSession: "qq:group:77001",
			Kind: MemoryKindInstruction, Content: "群里的说法", Confidence: 0.95, Importance: 0.9},
	}
	markLinkedMemorySubjects(query, items)
	if !items[0].LinkedSubject || items[1].LinkedSubject {
		t.Fatalf("only the linked account's user-visible memory is marked: %#v", items)
	}
	ranked := rankStructuredMemories(items[:1], event, "喝什么", time.Now())
	if len(ranked) != 1 {
		t.Fatalf("linked core memory should be kept as the speaker's own, got %#v", ranked)
	}
}
