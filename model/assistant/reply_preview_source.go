// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/SuInk/diana/model/llm"
)

// replyPreviewSource 决定一次模型调用的流里哪一段推成草稿（Telegram 草稿、QQ 私聊流式）。
//
// 草稿观察者挂在整轮回复的 ctx 上，这一轮里的每次调用都看得到它，但只有「要说给对方的
// 那句话」该推出去：
//   - 带着 send 工具的请求，话在 send 的 text 参数里；这时的普通文本不会发出（Runner
//     会退回去让模型改用 send），推出去对方只会看到一段话露出来又消失。
//   - 不带 send 的请求只在不走 Agent 的回复里才是正文；复核、审核、识图这类旁路调用
//     的输出只给程序读，用 llm.WithHiddenOutput 或各自的用途标签排除。
//
// send 的正文要等「说了却没做」复核、联网细节复核放行才算数，被打回就换一版。Telegram
// 草稿只是预览，换一版就是替换掉；QQ 私聊流式的帧发出去就是正式消息，没过复核的话
// 不能流，带 send 的请求在 QQ 上一律不推。
type replyPreviewSource struct {
	observer textDeltaObserver
	// speech 为真表示请求带着 send：只认 send 参数里的 text，不认普通文本。
	speech bool
	// callID 是这次调用里第一个 send 的 ID，同一步里再出别的 send 不去覆盖它。
	callID string
}

func newReplyPreviewSource(ctx context.Context, req llm.GenerateRequest) replyPreviewSource {
	observer := textDeltaObserverFromContext(ctx)
	if observer == nil || llm.HiddenOutput(ctx) || !replyPreviewPurpose(llmUsagePurposeFromContext(ctx)) {
		return replyPreviewSource{}
	}
	source := replyPreviewSource{observer: observer}
	for _, tool := range req.Tools {
		if isSpeechToolName(tool.Name) {
			source.speech = true
			break
		}
	}
	if draft, ok := observer.(*replyDraft); ok && draft != nil && draft.qqStream && source.speech {
		return replyPreviewSource{}
	}
	return source
}

// replyPreviewPurpose 认出生成正式回复的用途；没打标签的按回复处理（测试和旧调用点）。
func replyPreviewPurpose(purpose string) bool {
	switch purpose {
	case "", PurposeReply, PurposeScheduledQuery, PurposeEventTrigger, PurposeFeedReply:
		return true
	}
	return false
}

func (s *replyPreviewSource) text(ctx context.Context, accumulated string) bool {
	if s.observer == nil || s.speech {
		return false
	}
	return s.observer.ObserveTextDelta(ctx, accumulated)
}

func (s *replyPreviewSource) toolInput(ctx context.Context, input *llm.ToolInputProgress) bool {
	if s.observer == nil || !s.speech || input == nil || !isSpeechToolName(input.Name) {
		return false
	}
	if s.callID == "" {
		s.callID = input.ID
	} else if input.ID != s.callID {
		return false
	}
	speech, ok := partialJSONStringField(input.Arguments, "text")
	if !ok || strings.TrimSpace(speech) == "" {
		return false
	}
	return s.observer.ObserveTextDelta(ctx, speech)
}

// partialJSONStringField 从还没收完的 JSON 对象里读出顶层字符串字段 key 目前收到的部分。
// 字段还没出现、值不是字符串、或者对象本身坏了时返回 false。转义序列收到一半就停在它
// 前面，不吐半个字符。
func partialJSONStringField(raw, key string) (string, bool) {
	p := partialJSONScanner{s: raw}
	p.space()
	if !p.take('{') {
		return "", false
	}
	for {
		p.space()
		if p.done() || p.peek() == '}' {
			return "", false
		}
		name, complete := p.str()
		if !complete {
			return "", false
		}
		p.space()
		if !p.take(':') {
			return "", false
		}
		p.space()
		if p.done() {
			return "", false
		}
		if name == key {
			if p.peek() != '"' {
				return "", false
			}
			value, _ := p.str()
			return value, true
		}
		if !p.skipValue() {
			return "", false
		}
		p.space()
		if !p.take(',') {
			return "", false
		}
	}
}

type partialJSONScanner struct {
	s string
	i int
}

func (p *partialJSONScanner) done() bool { return p.i >= len(p.s) }
func (p *partialJSONScanner) peek() byte { return p.s[p.i] }

func (p *partialJSONScanner) space() {
	for !p.done() && strings.IndexByte(" \t\r\n", p.peek()) >= 0 {
		p.i++
	}
}

func (p *partialJSONScanner) take(c byte) bool {
	if p.done() || p.peek() != c {
		return false
	}
	p.i++
	return true
}

// str 读一个字符串，返回解码后的内容和它是否已经收完。没收完时内容截在最后一个完整
// 字符处。
func (p *partialJSONScanner) str() (string, bool) {
	if !p.take('"') {
		return "", false
	}
	start := p.i
	safe := p.i
	for !p.done() {
		switch c := p.peek(); {
		case c == '"':
			value := decodeJSONStringBody(p.s[start:p.i])
			p.i++
			return value, true
		case c == '\\':
			width := jsonEscapeWidth(p.s[p.i:])
			if width == 0 {
				return decodeJSONStringBody(p.s[start:safe]), false
			}
			p.i += width
		default:
			p.i++
		}
		safe = p.i
	}
	return decodeJSONStringBody(p.s[start:safe]), false
}

// jsonEscapeWidth 返回 s 开头那个转义序列的字节数；还没收完时返回 0。代理对的高位
// 要连着低位一起收完，单独解出来是乱码。
func jsonEscapeWidth(s string) int {
	if len(s) < 2 {
		return 0
	}
	if s[1] != 'u' {
		return 2
	}
	if len(s) < 6 {
		return 0
	}
	if high := strings.ToLower(s[2:4]); high >= "d8" && high <= "db" {
		if len(s) < 12 {
			return 0
		}
		return 12
	}
	return 6
}

func decodeJSONStringBody(body string) string {
	var value string
	if err := json.Unmarshal([]byte(`"`+body+`"`), &value); err != nil {
		return ""
	}
	return value
}

// skipValue 跳过一个已经收完的值；没收完时返回 false。
func (p *partialJSONScanner) skipValue() bool {
	if p.done() {
		return false
	}
	switch p.peek() {
	case '"':
		_, complete := p.str()
		return complete
	case '{', '[':
		depth := 0
		for !p.done() {
			switch p.peek() {
			case '"':
				if _, complete := p.str(); !complete {
					return false
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			p.i++
			if depth == 0 {
				return true
			}
		}
		return false
	default:
		start := p.i
		for !p.done() && strings.IndexByte(",}] \t\r\n", p.peek()) < 0 {
			p.i++
		}
		// 数字和字面量后面要跟着分隔符才算收完，否则 tru、12 这种半截也会被当成完整值。
		return p.i > start && !p.done()
	}
}
