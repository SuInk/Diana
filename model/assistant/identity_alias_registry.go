// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"strings"
	"sync"
)

// identityAliasRegistryLimit 是全局记住的号码上限，超出时丢最早登记的。
const identityAliasRegistryLimit = 200000

const identityAliasRegistryMinDigits = 7

// identityAliasLocalLimit 是每个会话记住的短号码上限，超出时丢最早登记的。
const identityAliasLocalLimit = 5000

// identityAliasRegistry 记住所有换过别名的数字号码（QQ 号、群号、消息 ID），整个进程共用。
//
// 别名早就由全局盐确定，同一个号码在哪都一样；但代理只替换它「认得」的号码——正文里
// 的数字不能见一个换一个，价格、日期也是数字——而 scope 每轮新建，只认得本轮消息和
// 历史窗口里结构化出现的那些。只追加续接把前几轮的请求原样带着走，旧消息或跨群行
// 掉出窗口后，里面的号码这一轮没人认得，就以真实号码发出去：上一轮同一处还是
// im_user_…，前缀从那里断开，后面整段缓存失效（10-28 线上 terra 续接后命中率仍只有
// 一半），号码也漏给了上游。所以见过一次就一直认。
//
// 替换时不拿名单挨个去正文里找（名单会越来越长），而是扫正文里的每串数字回来查表，
// 耗时只跟正文长度有关。7 位以上的纯数字号码进全局名单。更短的（五六位的老 QQ 号、
// Telegram 那种递增消息 ID）只记在出现它的会话里：放进全局名单，别的群正文里碰巧
// 同样的金额、编号也会被换掉；可只按本轮登记又会让前几轮留下的旧块变回真实号码，
// 只追加的前缀从那里断开。会话内的误伤和本轮登记本来就有的一样。带字母的平台 ID
// 仍只按本轮登记替换。
type identityAliasRegistry struct {
	mu     sync.RWMutex
	global identityAliasSet
	// local 按会话（sessionKey）记短号码。
	local map[string]*identityAliasSet
}

type identityAliasSet struct {
	roles map[string]string
	order []string
}

func (set *identityAliasSet) add(realID, role string, limit int) {
	if set.roles == nil {
		set.roles = map[string]string{}
	}
	if _, ok := set.roles[realID]; ok {
		return
	}
	set.roles[realID] = role
	set.order = append(set.order, realID)
	if over := len(set.order) - limit; over > 0 {
		for _, id := range set.order[:over] {
			delete(set.roles, id)
		}
		set.order = append([]string(nil), set.order[over:]...)
	}
}

func (g *identityAliasRegistry) remember(conversation, realID, role string) {
	if g == nil || !isLikelyMessageID(realID) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(strings.TrimPrefix(realID, "-")) >= identityAliasRegistryMinDigits {
		g.global.add(realID, role, identityAliasRegistryLimit)
		return
	}
	if conversation == "" {
		return
	}
	if g.local == nil {
		g.local = map[string]*identityAliasSet{}
	}
	set := g.local[conversation]
	if set == nil {
		set = &identityAliasSet{}
		g.local[conversation] = set
	}
	set.add(realID, role, identityAliasLocalLimit)
}

func (g *identityAliasRegistry) role(conversation, realID string) (string, bool) {
	if g == nil {
		return "", false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if role, ok := g.global.roles[realID]; ok {
		return role, true
	}
	if set := g.local[conversation]; set != nil {
		role, ok := set.roles[realID]
		return role, ok
	}
	return "", false
}

// replaceKnownNumbers 把正文里登记过的数字号码换成别名，顺带登记进本轮 scope，
// 模型照抄别名调工具时才还原得回来。
func (s *identityPrivacyScope) replaceKnownNumbers(text string) string {
	if s.known == nil {
		return text
	}
	s.mu.Lock()
	conversation := s.conversation
	s.mu.Unlock()
	var builder strings.Builder
	last := 0
	for i := 0; i < len(text); {
		if text[i] < '0' || text[i] > '9' {
			i++
			continue
		}
		start := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		digits := text[start:i]
		from, id := start, digits
		if start > 0 && text[start-1] == '-' {
			if role, ok := s.known.role(conversation, "-"+digits); ok && role == "message" {
				from, id = start-1, "-"+digits
			}
		}
		role, ok := s.known.role(conversation, id)
		if !ok {
			continue
		}
		var alias string
		if role == "message" {
			alias = s.registerMessageID(id)
		} else {
			alias = s.register(id, role)
		}
		if alias == id {
			continue
		}
		builder.WriteString(text[last:from])
		builder.WriteString(alias)
		last = i
	}
	if last == 0 {
		return text
	}
	builder.WriteString(text[last:])
	return builder.String()
}
