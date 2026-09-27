// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 正文里的账号数字（「12345 是谁」）以前只有在历史里出现过同一个人时才会被换成
// 别名；没出现过的就以原样进上下文，模型手里同时有别名和真号，对不上是不是同一个人。
//
// 这里补的是「没出现过、但确实是本群成员」的那部分：只换核实过的成员，其余数字
// （订单号、手机号、年份）原样保留——把它们也当账号换掉，模型就读不懂正文了。

const (
	// 平台明确说不在群的号记 10 分钟；查询本身出错（重连、限流、超时）只记 30 秒，
	// 避免一次抖动让真成员十分钟都换不上，又不至于同一条消息反复打平台。
	bodyAccountAbsentTTL    = 10 * time.Minute
	bodyAccountTransientTTL = 30 * time.Second
	// 内存里最多记这么多个群。成员结论落了库，丢掉的群下次从库里读回来，别名不变。
	bodyAccountMaxGroups = 1000
	// 每轮最多为正文数字问几次平台。群里贴一串号码时不能把主链路拖住。
	bodyAccountLookupsPerTurn = 3
	bodyAccountPersistTimeout = 2 * time.Second
)

var identityPrivacyBodyDigitsPattern = regexp.MustCompile(`[0-9]+`)

// IdentityBodyAccountStore 持久化「这个号核实过是这个群的成员」。
//
// 成员结论必须跨重启：某一轮把正文里的号换成了别名，这段话进了历史；重启后要是只剩
// 内存缓存的空白，历史里那个号就变回真号——既把真号漏给模型，又让整段历史前后逐字
// 不同，前缀缓存作废。只存成员，不存「不在群」：后者会变，而且没有泄露的问题。
type IdentityBodyAccountStore interface {
	LoadIdentityBodyAccounts(ctx context.Context, platform, profileID, groupID string) ([]string, error)
	SaveIdentityBodyAccount(ctx context.Context, platform, profileID, groupID, userID string) error
}

type bodyAccountGroup struct {
	loaded  bool
	members map[string]bool
	// misses 是暂时不当成员看的号，值是到期时间。
	misses  map[string]time.Time
	touched time.Time
}

// bodyAccountMembership 缓存「某个平台某个群里，这个号是不是成员」。零值可用。
type bodyAccountMembership struct {
	mu     sync.Mutex
	groups map[string]*bodyAccountGroup
}

type bodyAccountState int

const (
	bodyAccountUnknown bodyAccountState = iota
	bodyAccountMember
	bodyAccountMiss
)

func bodyAccountGroupKey(platform, profileID, groupID string) string {
	return platform + "\x00" + profileID + "\x00" + groupID
}

// groupLocked 取出一个群的记录，必要时新建；超出上限时丢掉最久没用过的群。
func (m *bodyAccountMembership) groupLocked(key string, now time.Time) *bodyAccountGroup {
	if m.groups == nil {
		m.groups = map[string]*bodyAccountGroup{}
	}
	group := m.groups[key]
	if group == nil {
		if len(m.groups) >= bodyAccountMaxGroups {
			oldestKey, oldest := "", time.Time{}
			for existing, item := range m.groups {
				if oldestKey == "" || item.touched.Before(oldest) {
					oldestKey, oldest = existing, item.touched
				}
			}
			delete(m.groups, oldestKey)
		}
		group = &bodyAccountGroup{members: map[string]bool{}, misses: map[string]time.Time{}}
		m.groups[key] = group
	}
	group.touched = now
	return group
}

func (m *bodyAccountMembership) needsLoad(key string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.groupLocked(key, now).loaded
}

func (m *bodyAccountMembership) markLoaded(key string, members []string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	group := m.groupLocked(key, now)
	group.loaded = true
	for _, userID := range members {
		group.members[strings.TrimSpace(userID)] = true
	}
}

func (m *bodyAccountMembership) state(key, userID string, now time.Time) bodyAccountState {
	m.mu.Lock()
	defer m.mu.Unlock()
	group := m.groupLocked(key, now)
	if group.members[userID] {
		return bodyAccountMember
	}
	if until, ok := group.misses[userID]; ok {
		if now.Before(until) {
			return bodyAccountMiss
		}
		delete(group.misses, userID)
	}
	return bodyAccountUnknown
}

func (m *bodyAccountMembership) markMember(key, userID string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	group := m.groupLocked(key, now)
	group.members[userID] = true
	delete(group.misses, userID)
}

func (m *bodyAccountMembership) markMiss(key, userID string, until, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.groupLocked(key, now).misses[userID] = until
}

func llmIdentityBodyAccountMappingEnabled(cfg BotConfig) bool {
	cfg = cfg.WithDefaults()
	return llmIdentityMaskingEnabled(cfg) && boolValue(cfg.LLMIdentityBodyAccounts, true)
}

// bodyAccountDigitsAllowed 按平台给出正文里「长得像账号」的数字长度，以及数字前面
// 能不能紧贴 QQ 字样。只认账号本来就是数字的平台；飞书、钉钉、企微的账号是字母串，
// 正文里的数字不可能是它们的账号。
func bodyAccountDigitsAllowed(platform string) (minLen, maxLen int, qqPrefix bool, ok bool) {
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11:
		// QQ 号目前 5 到 10 位。11 位起多半是手机号、订单号、时间戳，不去平台白问。
		return 5, 10, true, true
	case PlatformTelegram:
		// Telegram 用户 ID 是正整数，目前最长 10 位，留出增长余量。
		return 5, 13, false, true
	}
	return 0, 0, false, false
}

// bodyAccountCandidates 挑出正文里独立出现、长度合适的数字。紧贴字母、小数点、
// 斜杠的数字是链接、版本号、文件名的一部分，不是有人在报账号。唯一的例外是 QQ 平台
// 上紧贴的 Q／QQ（「qq12345」），那是在报 QQ 号。
func bodyAccountCandidates(text string, minLen, maxLen int, qqPrefix bool) []string {
	if text == "" {
		return nil
	}
	isLetter := func(value byte) bool { return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') }
	embedded := func(value byte) bool {
		return value == '.' || value == '/' || value == '_' || value == '-' || value == '=' || value == '&' || value == '?' || isLetter(value)
	}
	// 数字前面那串字母恰好是 Q 或 QQ（不分大小写），且字母串前面不再是标识符的一部分。
	qqPrefixed := func(start int) bool {
		letters := 0
		for start-letters-1 >= 0 && isLetter(text[start-letters-1]) {
			letters++
		}
		if letters == 0 || letters > 2 || !strings.EqualFold(text[start-letters:start], strings.Repeat("q", letters)) {
			return false
		}
		return start-letters == 0 || !embedded(text[start-letters-1])
	}
	var out []string
	for _, span := range identityPrivacyBodyDigitsPattern.FindAllStringIndex(text, -1) {
		start, end := span[0], span[1]
		if end-start < minLen || end-start > maxLen || text[start] == '0' {
			continue
		}
		if start > 0 && embedded(text[start-1]) && !(qqPrefix && qqPrefixed(start)) {
			continue
		}
		if end < len(text) && embedded(text[end]) {
			continue
		}
		out = append(out, text[start:end])
	}
	return out
}

func (s *identityPrivacyScope) knows(realID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.realToAlias[realID]
	return ok
}

// groupMemberAbsent 判断成员查询的失败是不是平台明确说「不在群」。
//
// 超时、断线、限流这类只说明这次没问到，记成非成员会让一个真成员好几分钟换不上
// 别名。OneBot 的桥接端明确回了失败（带 retcode）而报错里又看不出是超时类，才按
// 不在群算——NapCat 查非成员就是这么回的。
func groupMemberAbsent(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"timeout", "timed out", "超时", "rate", "too many", "频繁", "繁忙", "busy", "disconnect", "closed", "reconnect", "network", "eof", "unavailable", "not connected", "未连接"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	for _, marker := range []string{"not a current chat member", "not in group", "not a member", "user not found", "member not found", "participant", "不在群", "不是群成员", "成员不存在", "不存在"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	var actionErr *oneBotActionError
	return errors.As(err, &actionErr)
}

// registerBodyAccounts 把正文里核实为本群成员的账号数字登记进别名表，之后
// protectText 会把整段上下文里出现的这个号一并换掉，模型填回别名时照常还原。
//
// verify 为 false 时只看已有结论（内存、库、被动成员缓存），不向平台发起查询：路由
// 之前那次每条群消息都要经过，大多数根本不回，不能为它们等平台。确定要回复时再以
// true 调一次，只为当前这条消息里的新号码去问。历史里的号码任何时候都只看已有结论。
func (r *Runtime) registerBodyAccounts(ctx context.Context, cfg BotConfig, scope *identityPrivacyScope, event MessageEvent, history []MessageEvent, verify bool) {
	if r == nil || scope == nil || !llmIdentityBodyAccountMappingEnabled(cfg) {
		return
	}
	groupID := strings.TrimSpace(event.GroupID)
	if event.Kind != EventKindGroup || groupID == "" {
		// 私聊里除了双方没有可核实的「成员」，双方的号早已登记过。
		return
	}
	platform := firstNonEmpty(NormalizePlatformID(event.Platform), NormalizePlatformID(cfg.Platform))
	minLen, maxLen, qqPrefix, ok := bodyAccountDigitsAllowed(platform)
	if !ok {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	key := bodyAccountGroupKey(platform, event.ProfileID, groupID)
	store, _ := r.messageStore.(IdentityBodyAccountStore)
	if r.bodyAccounts.needsLoad(key, now) {
		if store == nil {
			r.bodyAccounts.markLoaded(key, nil, now)
		} else {
			loadCtx, cancel := context.WithTimeout(ctx, bodyAccountPersistTimeout)
			members, err := store.LoadIdentityBodyAccounts(loadCtx, platform, event.ProfileID, groupID)
			cancel()
			if err == nil {
				r.bodyAccounts.markLoaded(key, members, now)
			} else {
				log.Printf("diana identity privacy: 读取正文账号核实结果失败: %v", err)
			}
		}
	}
	remember := func(userID string) {
		r.bodyAccounts.markMember(key, userID, now)
		scope.register(userID, "user")
		if store == nil {
			return
		}
		saveCtx, cancel := context.WithTimeout(ctx, bodyAccountPersistTimeout)
		defer cancel()
		if err := store.SaveIdentityBodyAccount(saveCtx, platform, event.ProfileID, groupID, userID); err != nil {
			log.Printf("diana identity privacy: 正文账号核实结果落库失败，重启后历史里的这个号会变回真号: %v", err)
		}
	}

	// 几次查询共用一份时限，不叠加超时。
	var lookupCtx context.Context
	var cancelLookup context.CancelFunc = func() {}
	defer func() { cancelLookup() }()
	asked := 0
	resolve := func(userID string, mayAsk bool) {
		if scope.knows(userID) {
			return
		}
		switch r.bodyAccounts.state(key, userID, now) {
		case bodyAccountMember:
			scope.register(userID, "user")
			return
		case bodyAccountMiss:
			return
		}
		// 被动成员缓存是群里有人说话时顺手记下的，不花一次调用。
		if _, seen := r.memberCacheLookup(groupID, userID); seen {
			remember(userID)
			return
		}
		if !mayAsk || asked >= bodyAccountLookupsPerTurn {
			return
		}
		if lookupCtx == nil {
			lookupCtx, cancelLookup = context.WithTimeout(ctx, memberFetchTimeout)
		}
		if lookupCtx.Err() != nil {
			return
		}
		asked++
		member, err := r.getGroupMemberInfoForEvent(lookupCtx, event, groupID, userID)
		switch {
		case err == nil && NormalizeGroupRole(member.Role) != "":
			remember(userID)
		case err == nil || groupMemberAbsent(err):
			// 平台回了话但认不出这个人，或明确说不在群。
			r.bodyAccounts.markMiss(key, userID, now.Add(bodyAccountAbsentTTL), now)
		case lookupCtx.Err() != nil:
			// 是本轮预算用完了，不是平台的结论，什么都不记。
		default:
			r.bodyAccounts.markMiss(key, userID, now.Add(bodyAccountTransientTTL), now)
		}
	}

	for _, text := range bodyAccountTexts(event) {
		for _, candidate := range bodyAccountCandidates(text, minLen, maxLen, qqPrefix) {
			resolve(candidate, verify)
		}
	}
	for _, item := range history {
		if strings.TrimSpace(item.GroupID) != groupID {
			continue
		}
		for _, text := range bodyAccountTexts(item) {
			for _, candidate := range bodyAccountCandidates(text, minLen, maxLen, qqPrefix) {
				resolve(candidate, false)
			}
		}
	}
}

func bodyAccountTexts(event MessageEvent) []string {
	texts := []string{historyPlainText(event)}
	if event.Quoted != nil {
		texts = append(texts, firstNonEmpty(strings.TrimSpace(PlainText(event.Quoted.Segments)), event.Quoted.RawMessage))
	}
	return texts
}

func (r *Runtime) memberCacheLookup(groupID, userID string) (memberInfo, bool) {
	if r.members == nil {
		return memberInfo{}, false
	}
	return r.members.lookup(groupID, userID)
}
