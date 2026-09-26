// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 正文里的账号数字（「123456789 是谁」）以前只有在历史里出现过同一个人时才会被换成
// 别名；没出现过的就以原样进上下文，模型手里同时有别名和真号，对不上是不是同一个人。
//
// 这里补的是「没出现过、但确实是本群成员」的那部分：只换核实过的成员，其余数字
// （订单号、手机号、年份）原样保留——把它们也当账号换掉，模型就读不懂正文了。

const (
	// 核实结果要跨轮保留：这一轮换成了别名、下一轮变回真号，同一段历史前后两轮逐字
	// 不同，前缀缓存就白做了。成员关系变化慢，一天足够；查不到的记短一点，平台抖一下
	// 不至于让一个真成员一小时都换不上。
	bodyAccountMemberTTL    = 24 * time.Hour
	bodyAccountNonMemberTTL = 10 * time.Minute
	bodyAccountMaxEntries   = 5000
	// 每轮最多为正文数字问几次平台。群里贴一串号码时不能把主链路拖住。
	bodyAccountLookupsPerTurn = 3
)

var identityPrivacyBodyDigitsPattern = regexp.MustCompile(`[0-9]+`)

type bodyAccountVerdict struct {
	member bool
	at     time.Time
}

// bodyAccountMembership 缓存「某个平台某个群里，这个号是不是成员」。零值可用。
type bodyAccountMembership struct {
	mu      sync.Mutex
	entries map[string]bodyAccountVerdict
}

func bodyAccountKey(platform, profileID, groupID, userID string) string {
	return platform + "\x00" + profileID + "\x00" + groupID + "\x00" + userID
}

func (m *bodyAccountMembership) lookup(key string, now time.Time) (bool, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	verdict, ok := m.entries[key]
	if !ok {
		return false, false
	}
	ttl := bodyAccountNonMemberTTL
	if verdict.member {
		ttl = bodyAccountMemberTTL
	}
	if now.Sub(verdict.at) > ttl {
		delete(m.entries, key)
		return false, false
	}
	return verdict.member, true
}

func (m *bodyAccountMembership) store(key string, member bool, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]bodyAccountVerdict{}
	}
	if len(m.entries) >= bodyAccountMaxEntries {
		// 软缓存，丢掉的下次再问一遍就回来了，淘汰精度不重要。
		for existing := range m.entries {
			delete(m.entries, existing)
			if len(m.entries) < bodyAccountMaxEntries/2 {
				break
			}
		}
	}
	m.entries[key] = bodyAccountVerdict{member: member, at: now}
}

func llmIdentityBodyAccountMappingEnabled(cfg BotConfig) bool {
	cfg = cfg.WithDefaults()
	return llmIdentityMaskingEnabled(cfg) && boolValue(cfg.LLMIdentityBodyAccountMappingEnabled, true)
}

// bodyAccountDigitsAllowed 按平台给出正文里「长得像账号」的数字长度。只认账号本来
// 就是数字的平台；飞书、钉钉、企微的账号是字母串，正文里的数字不可能是它们的账号。
func bodyAccountDigitsAllowed(platform string) (int, int, bool) {
	switch NormalizePlatformID(platform) {
	case PlatformOneBotV11:
		// QQ 号目前 5 到 10 位。11 位起多半是手机号、订单号、时间戳，不去平台白问。
		return 5, 10, true
	case PlatformTelegram:
		// Telegram 用户 ID 是正整数，目前最长 10 位，留出增长余量。
		return 5, 13, true
	}
	return 0, 0, false
}

// bodyAccountCandidates 挑出正文里独立出现、长度合适的数字。紧贴字母、小数点、
// 斜杠的数字是链接、版本号、文件名的一部分，不是有人在报账号。
func bodyAccountCandidates(text string, minLen, maxLen int) []string {
	if text == "" {
		return nil
	}
	embedded := func(value byte) bool {
		return value == '.' || value == '/' || value == '_' || value == '-' || value == '=' || value == '&' || value == '?' ||
			(value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
	}
	var out []string
	for _, span := range identityPrivacyBodyDigitsPattern.FindAllStringIndex(text, -1) {
		start, end := span[0], span[1]
		if end-start < minLen || end-start > maxLen || text[start] == '0' {
			continue
		}
		if (start > 0 && embedded(text[start-1])) || (end < len(text) && embedded(text[end])) {
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

// registerBodyAccounts 把正文里核实为本群成员的账号数字登记进别名表，之后
// protectText 会把整段上下文里出现的这个号一并换掉，模型填回别名时照常还原。
//
// 只有当前这条消息里的新号码会去平台问；历史里的只看缓存。历史在一轮轮往下滚，每轮
// 把整段历史的号码都问一遍，调用量跟着历史长度涨，而它们在当时是当前消息的那一轮
// 已经问过、记进了缓存。
func (r *Runtime) registerBodyAccounts(ctx context.Context, cfg BotConfig, scope *identityPrivacyScope, event MessageEvent, history []MessageEvent) {
	if r == nil || scope == nil || !llmIdentityBodyAccountMappingEnabled(cfg) {
		return
	}
	groupID := strings.TrimSpace(event.GroupID)
	if event.Kind != EventKindGroup || groupID == "" {
		// 私聊里除了双方没有可核实的「成员」，双方的号早已登记过。
		return
	}
	platform := firstNonEmpty(NormalizePlatformID(event.Platform), NormalizePlatformID(cfg.Platform))
	minLen, maxLen, ok := bodyAccountDigitsAllowed(platform)
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
	// 几次查询共用一份时限：这一步在路由之前，所有群消息都会经过，不能叠加超时拖住它。
	lookupCtx, cancel := context.WithTimeout(ctx, memberFetchTimeout)
	defer cancel()
	resolve := func(userID string, mayAsk bool, asked *int) {
		if scope.knows(userID) {
			return
		}
		key := bodyAccountKey(platform, event.ProfileID, groupID, userID)
		if member, cached := r.bodyAccounts.lookup(key, now); cached {
			if member {
				scope.register(userID, "user")
			}
			return
		}
		// 被动成员缓存是群里有人说话时顺手记下的，不花一次调用。
		if _, seen := r.memberCacheLookup(groupID, userID); seen {
			r.bodyAccounts.store(key, true, now)
			scope.register(userID, "user")
			return
		}
		if !mayAsk || *asked >= bodyAccountLookupsPerTurn {
			return
		}
		if lookupCtx.Err() != nil {
			return
		}
		*asked++
		member, err := r.getGroupMemberInfoForEvent(lookupCtx, event, groupID, userID)
		if err != nil && lookupCtx.Err() != nil {
			// 是本轮预算用完了，不是平台说不在群，别记成非成员。
			return
		}
		// 查询失败和「不在群」在多数实现里是同一种报错，一并按非成员短暂记下；
		// 群身份为空说明平台没认出这个人，同样不算。
		isMember := err == nil && NormalizeGroupRole(member.Role) != ""
		r.bodyAccounts.store(key, isMember, now)
		if isMember {
			scope.register(userID, "user")
		}
	}

	asked := 0
	for _, text := range bodyAccountTexts(event) {
		for _, candidate := range bodyAccountCandidates(text, minLen, maxLen) {
			resolve(candidate, true, &asked)
		}
	}
	for _, item := range history {
		if strings.TrimSpace(item.GroupID) != groupID {
			continue
		}
		for _, text := range bodyAccountTexts(item) {
			for _, candidate := range bodyAccountCandidates(text, minLen, maxLen) {
				resolve(candidate, false, &asked)
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
