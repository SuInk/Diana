// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package llm

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// contextOverflowMarkers 覆盖各家供应商对「请求超出模型上下文」的措辞。窗口靠
// 模型名推断出来的部分终究是推断：推错时必须能识别出来并收缩重试，而不是让这
// 一轮回复直接失败。
var contextOverflowMarkers = []string{
	"context_length_exceeded",
	"context length exceeded",
	"maximum context length",
	"reduce the length of the messages",
	"prompt is too long",
	"input is too long",
	"input length and `max_tokens` exceed context limit",
	"too many tokens",
	"exceeds the maximum number of tokens",
	"exceeds model context",
	"request exceeds the maximum allowed number of tokens",
	"token count exceeds",
	"exceed context window",
	"上下文长度",
	"超出模型上下文",
	// HuggingFace TGI 的措辞，智谱 glm-4v-flash 这类部署原样透出：
	// `inputs` tokens + `max_new_tokens` must be <= 16384。
	"`max_new_tokens` must be <=",
	"inputs tokens + max_new_tokens must be <=",
}

// contextOverflowLimitPatterns 从超限报错里读出模型真实的窗口。读得出来就一次缩到位，
// 不必从 128K 一路减半、每一步都白吃一个 400。
var contextOverflowLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile("max_new_tokens`?\\s*must be <=\\s*(\\d+)"),
	regexp.MustCompile(`maximum context length is (\d+)`),
	regexp.MustCompile(`context (?:length|window|limit) (?:is |of )?(\d+)`),
}

// ContextOverflowLimit 返回超限报错里写明的窗口大小。
func ContextOverflowLimit(err error) (int64, bool) {
	if !IsContextOverflowError(err) {
		return 0, false
	}
	text := strings.ToLower(err.Error())
	for _, pattern := range contextOverflowLimitPatterns {
		match := pattern.FindStringSubmatch(text)
		if len(match) < 2 {
			continue
		}
		if limit, parseErr := strconv.ParseInt(match[1], 10, 64); parseErr == nil && limit >= minContextWindowTokens {
			return limit, true
		}
	}
	return 0, false
}

// ContextLimitMemoTTL 是学到的窗口的保质期，过期后按配置重新来，由下一次超限再学。
const ContextLimitMemoTTL = 6 * time.Hour

// contextLimitMemo 记住「某个端点的某个模型真实窗口多大」。和参数降级一样必须活在
// 进程级：每条消息都会新建 client，记在 client 上等于每条都要先撞一次 400。
type contextLimitMemo struct {
	mu      sync.RWMutex
	learned map[string]learnedContextLimit
	now     func() time.Time
}

type learnedContextLimit struct {
	limit int64
	at    time.Time
}

var rememberedContextLimits = &contextLimitMemo{}

func (m *contextLimitMemo) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// contextLimitKey 和参数降级同一套键；没传模型时按配置档的默认模型记，读写两边
// 才对得上。
func contextLimitKey(cfg ProviderConfig, model string) string {
	if strings.TrimSpace(model) == "" {
		model = cfg.Model
	}
	return downgradeMemoKey(cfg, model)
}

func (m *contextLimitMemo) get(cfg ProviderConfig, model string) (int64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.learned[contextLimitKey(cfg, model)]
	if !ok || m.clock().Sub(entry.at) >= ContextLimitMemoTTL {
		return 0, false
	}
	return entry.limit, true
}

// learn 从超限报错里读出窗口并记住；读不出来就什么都不记，交给调用方减半重试。
func (m *contextLimitMemo) learn(cfg ProviderConfig, model string, err error) {
	limit, ok := ContextOverflowLimit(err)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.learned == nil {
		m.learned = map[string]learnedContextLimit{}
	}
	m.learned[contextLimitKey(cfg, model)] = learnedContextLimit{limit: limit, at: m.clock()}
}

// IsContextOverflowError 判断错误是否为「请求超出模型上下文窗口」。
func IsContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range contextOverflowMarkers {
		if strings.Contains(text, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}
