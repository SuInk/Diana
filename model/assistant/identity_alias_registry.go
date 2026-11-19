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
// 耗时只跟正文长度有关。只收 7 位以上的纯数字号码：名单全局共用，Telegram 那种
// 六位的递增消息 ID 收进来，正文里碰巧同样的金额、编号也会被换掉。更短的号码和
// 带字母的平台 ID 仍只按本轮登记替换。
type identityAliasRegistry struct {
	mu    sync.RWMutex
	roles map[string]string
	order []string
}

func (g *identityAliasRegistry) remember(realID, role string) {
	if g == nil || !isLikelyMessageID(realID) || len(strings.TrimPrefix(realID, "-")) < identityAliasRegistryMinDigits {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.roles == nil {
		g.roles = map[string]string{}
	}
	if _, ok := g.roles[realID]; ok {
		return
	}
	g.roles[realID] = role
	g.order = append(g.order, realID)
	if over := len(g.order) - identityAliasRegistryLimit; over > 0 {
		for _, id := range g.order[:over] {
			delete(g.roles, id)
		}
		g.order = append([]string(nil), g.order[over:]...)
	}
}

func (g *identityAliasRegistry) role(realID string) (string, bool) {
	if g == nil {
		return "", false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	role, ok := g.roles[realID]
	return role, ok
}

// replaceKnownNumbers 把正文里登记过的数字号码换成别名，顺带登记进本轮 scope，
// 模型照抄别名调工具时才还原得回来。
func (s *identityPrivacyScope) replaceKnownNumbers(text string) string {
	if s.known == nil {
		return text
	}
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
			if role, ok := s.known.role("-" + digits); ok && role == "message" {
				from, id = start-1, "-"+digits
			}
		}
		role, ok := s.known.role(id)
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
