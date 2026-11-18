// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/SuInk/diana/model/llm"
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

// privacySentRecord 是跟着续接记录落盘的一段发过的正文（append_context_persist.go）。
// 续接记录读回的是换号之前的原文，重启后号码表是空的，同一段原文会换出另一个样子：
// 11-18 那次重启后几个小时边学边断，就是这么来的。所以前缀里每段正文发出去的样子也
// 一起存下，读回时放回 privacySentMemo，重启前后发出去的字节一样。Same 表示发出去的
// 就是原文，不再重复存一遍。
type privacySentRecord struct {
	Key     string            `json:"key"`
	Text    string            `json:"text,omitempty"`
	Same    bool              `json:"same,omitempty"`
	Restore map[string]string `json:"restore,omitempty"`
}

// records 取出 texts 里在 route 上发过的那些。
func (m *privacySentMemo[T]) records(route string, texts []string, encode func(T) map[string]string) []privacySentRecord {
	var out []privacySentRecord
	seen := map[[sha256.Size]byte]bool{}
	for _, text := range texts {
		key := privacySentKey(route, text)
		if seen[key] {
			continue
		}
		seen[key] = true
		sent, ok := m.get(route, text)
		if !ok {
			continue
		}
		record := privacySentRecord{Key: hex.EncodeToString(key[:]), Restore: encode(sent.restore)}
		if sent.text == text {
			record.Same = true
		} else {
			record.Text = sent.text
		}
		out = append(out, record)
	}
	return out
}

// restoreRecords 把落盘的发过的正文放回来。texts 是同一份前缀的原文，Same 的记录靠它
// 找回正文。
func (m *privacySentMemo[T]) restoreRecords(route string, texts []string, records []privacySentRecord, decode func(map[string]string) T) {
	if m == nil || route == "" || len(records) == 0 {
		return
	}
	raw := map[string]string{}
	for _, text := range texts {
		key := privacySentKey(route, text)
		raw[hex.EncodeToString(key[:])] = text
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, record := range records {
		text, ok := raw[record.Key]
		if !ok {
			continue
		}
		sent := privacySent[T]{text: record.Text, restore: decode(record.Restore)}
		if record.Same {
			sent.text = text
		}
		var key [sha256.Size]byte
		if decoded, err := hex.DecodeString(record.Key); err == nil && len(decoded) == len(key) {
			copy(key[:], decoded)
			m.putLocked(key, sent)
		}
	}
}

// privacySentTexts 是一份前缀里隐私代理会逐段替换的正文：消息正文、多段内容的文字和
// 工具调用参数里的字符串（密钥代理连参数一起换）。
func privacySentTexts(messages []llm.Message) []string {
	var texts []string
	add := func(text string) {
		if strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
	}
	for _, message := range messages {
		add(message.Content)
		for _, part := range message.Parts {
			add(part.Text)
		}
		for _, call := range message.ToolCalls {
			walkStrings(call.Arguments, add)
		}
	}
	return texts
}

func identitySentRestore(restore map[string]string) map[string]string { return restore }

func encodeSecretSentRestore(restore map[string]secretPrivacyEntry) map[string]string {
	if len(restore) == 0 {
		return nil
	}
	out := make(map[string]string, len(restore))
	for placeholder, entry := range restore {
		out[placeholder] = entry.kind + "\x00" + entry.value
	}
	return out
}

func decodeSecretSentRestore(stored map[string]string) map[string]secretPrivacyEntry {
	out := make(map[string]secretPrivacyEntry, len(stored))
	for placeholder, packed := range stored {
		kind, value, _ := strings.Cut(packed, "\x00")
		out[placeholder] = secretPrivacyEntry{kind: kind, value: value}
	}
	return out
}

// notePrivacySent 把这份前缀发出去的样子记进续接记录，随它落盘。
func (r *Runtime) notePrivacySent(log *appendContextLog, route string) {
	if log == nil || route == "" {
		return
	}
	texts := privacySentTexts(log.prefix)
	log.identitySent = r.identitySent.records(route, texts, identitySentRestore)
	log.secretSent = r.secretSent.records(route, texts, encodeSecretSentRestore)
}

// restorePrivacySent 把读回的续接记录里发过的正文放回隐私代理，每份记录只放一次。
func (r *Runtime) restorePrivacySent(log *appendContextLog, route string) {
	if log == nil || route == "" || (len(log.identitySent) == 0 && len(log.secretSent) == 0) {
		return
	}
	log.privacyRestored.Do(func() {
		texts := privacySentTexts(log.prefix)
		r.identitySent.restoreRecords(route, texts, log.identitySent, identitySentRestore)
		r.secretSent.restoreRecords(route, texts, log.secretSent, decodeSecretSentRestore)
	})
}
