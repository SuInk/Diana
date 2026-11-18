// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"sync"
)

// privacySentMemoEntries 是每一代记住的正文段数，满了整代轮换（同 magpie 的 allowkept）。
const privacySentMemoEntries = 1 << 15

// privacySentMemo 记住隐私代理在某个会话里发出去过的每段正文：同一段原文再发时，
// 照当时发出去的样子一字不改地发，只有没发过的正文按本轮的对照表替换。
//
// 替换规则本身每轮会变：账号别名只换号码表认得的号码，号码表在内存里、重启清空后
// 边见边学；密钥靠上下文认出来的值，要等后面的消息说出「密码是 xxx」才知道。同一段
// 摘要上一轮按原号发、这一轮换成 im_user_…，只追加续接的前缀从那里断开，后面整段
// 缓存失效（11-18 canary.33 重启后几个小时，主回复命中率从七成多掉到四成，断点几乎
// 都是这种号码）。做法照 magpie 的隐私脱敏：同一个值的占位符永远一样（按盐算），
// 发过的文本照发过的样子再发（它的 Options.Route / sentSpans）。上一轮按原样发出去的
// 值已经发出去了，这一轮改成别名也收不回来，只会让缓存白白失效。
//
// 只在内存里，按会话和原文的哈希记，不落盘；重启后续接记录本来就没了，第一轮整份
// 重发，之后照常记。restore 是那段正文里的占位符还原回去要用的对照，沿用旧正文时
// 合进本轮的对照表，模型照抄旧正文里的占位符去调工具也还原得回来。
type privacySentMemo[T any] struct {
	mu       sync.Mutex
	cur, old map[[sha256.Size]byte]privacySent[T]
}

type privacySent[T any] struct {
	text    string
	restore T
}

func privacySentKey(route, raw string) [sha256.Size]byte {
	return sha256.Sum256([]byte(route + "\x00" + raw))
}

func (m *privacySentMemo[T]) get(route, raw string) (privacySent[T], bool) {
	if m == nil || route == "" {
		return privacySent[T]{}, false
	}
	key := privacySentKey(route, raw)
	m.mu.Lock()
	defer m.mu.Unlock()
	if sent, ok := m.cur[key]; ok {
		return sent, true
	}
	if sent, ok := m.old[key]; ok {
		m.putLocked(key, sent)
		return sent, true
	}
	return privacySent[T]{}, false
}

func (m *privacySentMemo[T]) put(route, raw string, sent privacySent[T]) {
	if m == nil || route == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putLocked(privacySentKey(route, raw), sent)
}

func (m *privacySentMemo[T]) putLocked(key [sha256.Size]byte, sent privacySent[T]) {
	if m.cur == nil {
		m.cur = map[[sha256.Size]byte]privacySent[T]{}
	}
	if len(m.cur) >= privacySentMemoEntries {
		m.old, m.cur = m.cur, map[[sha256.Size]byte]privacySent[T]{}
	}
	m.cur[key] = sent
}

// privacySentRoute 是发过的正文按哪个会话记：同一个会话的请求共用一份前缀缓存。
// 没有消息事件的调用不记，每次照本轮的规则替换。
func privacySentRoute(ctx context.Context) string {
	if state := llmUsageFromContext(ctx); state != nil {
		return sessionKey(state.event)
	}
	return ""
}
