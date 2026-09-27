// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 候选冷却的几档时长。
//
// 降级 provider 是每次调用临时建的，它记不住「上一次这个候选刚被限流」，于是每次
// 调用都从第一个候选开始撞：线上一个配置档连续 3 天回「All accounts limited」，
// 就白撞了 1100 次、每次还多等一轮重试。冷却表挂在 Runtime 上跨请求保留，挑候选时
// 把冷却中的挪到最后。
const (
	// llmCooldownDefault 是额度、限流、鉴权这类失败没给等待时间时的冷却。
	llmCooldownDefault = 60 * time.Second
	// llmCooldownTransient 是普通 5xx、连接被拒这类偶发失败的冷却，只求接下来
	// 几秒别再先撞它；只有一个候选时冷却不会拦住它，见 order。
	llmCooldownTransient = 10 * time.Second
	// llmCooldownMax 给上游提示的等待时间封顶：「Wait 311000s」照单全收的话，上游
	// 提前恢复了也要三天多才会再用它。到期先探一次，还不行再冷却。
	llmCooldownMax = 30 * time.Minute
	// llmCooldownSkipLogInterval 是同一个候选「冷却中被跳过」两次写终端日志的最短
	// 间隔。每次调用都打的话，冷却本身就成了刷屏源。
	llmCooldownSkipLogInterval = 10 * time.Minute
)

// llmCooldownTable 记每个候选（配置档 + 模型）冷却到什么时候。零值可用，nil 时
// 所有方法都退化成「没有冷却」，直接构造 provider 的测试不用管它。
type llmCooldownTable struct {
	mu      sync.Mutex
	entries map[string]*llmCooldownEntry
	now     func() time.Time
}

type llmCooldownEntry struct {
	until       time.Time
	lastSkipLog time.Time
}

// llmCooldownKey 按配置档 ID 加模型区分候选：同一个配置档下挂好几个模型时，一个
// 模型的额度用完不代表别的模型也不能用。
func llmCooldownKey(profile llm.Profile) string {
	return strings.TrimSpace(profile.ID) + "\x00" + strings.TrimSpace(profile.Config.Model)
}

func (t *llmCooldownTable) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// remaining 返回候选还要冷却多久，不在冷却中返回 0。
func (t *llmCooldownTable) remaining(profile llm.Profile) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[llmCooldownKey(profile)]
	if entry == nil {
		return 0
	}
	if left := entry.until.Sub(t.clock()); left > 0 {
		return left
	}
	return 0
}

// markFailure 按失败原因给候选记冷却。返回冷却时长和这是不是一次新进入的冷却；
// 已经在冷却中又失败（所有候选都冷却、只好硬试）只顺延截止时间，不算新进入，
// 调用方据此决定要不要打日志。时长为 0 表示这类失败不该冷却。
func (t *llmCooldownTable) markFailure(profile llm.Profile, err error) (time.Duration, bool) {
	if t == nil {
		return 0, false
	}
	duration := llmCooldownDuration(err)
	if duration <= 0 {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	if t.entries == nil {
		t.entries = map[string]*llmCooldownEntry{}
	}
	// 顺手清掉过期的，表的大小始终跟配置档数量同级。
	for key, entry := range t.entries {
		if !entry.until.After(now) {
			delete(t.entries, key)
		}
	}
	key := llmCooldownKey(profile)
	entry, cooling := t.entries[key]
	if !cooling {
		entry = &llmCooldownEntry{}
		t.entries[key] = entry
	}
	if until := now.Add(duration); until.After(entry.until) {
		entry.until = until
	}
	return duration, !cooling
}

// clear 在候选调用成功后解除它的冷却：硬试成功说明上游已经恢复，下一次就该照常
// 排在前面。
func (t *llmCooldownTable) clear(profile llm.Profile) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, llmCooldownKey(profile))
}

// order 给出这一轮的尝试顺序：从 start 开始按原顺序轮一圈，冷却中的候选挪到最后，
// 彼此仍保持原顺序。冷却只调整先后、不剔除候选：全都在冷却时就是原顺序，不能让
// 冷却把整条链路直接判死；只有一个候选时它也照常被调用。
func (t *llmCooldownTable) order(group string, count int, start int, profileAt func(int) llm.Profile) []int {
	ready := make([]int, 0, count)
	var cooling []int
	if count == 0 {
		return ready
	}
	if start < 0 || start >= count {
		start = 0
	}
	for offset := 0; offset < count; offset++ {
		index := (start + offset) % count
		if t != nil && count > 1 {
			if t.remaining(profileAt(index)) > 0 {
				cooling = append(cooling, index)
				continue
			}
		}
		ready = append(ready, index)
	}
	if len(ready) > 0 {
		for _, index := range cooling {
			profile := profileAt(index)
			if left, ok := t.skipLogDue(profile); ok {
				log.Printf("diana llm provider cooldown skip: group=%q profile=%q remaining=%s", group, llmCandidateLabel(profile, true), left.Round(time.Second))
			}
		}
	}
	return append(ready, cooling...)
}

// skipLogDue 判断这次跳过要不要写日志，同一个候选按 llmCooldownSkipLogInterval 节流。
func (t *llmCooldownTable) skipLogDue(profile llm.Profile) (time.Duration, bool) {
	if t == nil {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[llmCooldownKey(profile)]
	if entry == nil {
		return 0, false
	}
	now := t.clock()
	left := entry.until.Sub(now)
	if left <= 0 {
		return 0, false
	}
	if !entry.lastSkipLog.IsZero() && now.Sub(entry.lastSkipLog) < llmCooldownSkipLogInterval {
		return left, false
	}
	entry.lastSkipLog = now
	return left, true
}

// noteLLMCandidateFailure 给失败的候选记冷却，新进入冷却时打一行说明冷却多久。
func noteLLMCandidateFailure(cooldowns *llmCooldownTable, group string, profile llm.Profile, err error) {
	duration, fresh := cooldowns.markFailure(profile, err)
	if duration <= 0 || !fresh {
		return
	}
	log.Printf(
		"diana llm provider cooldown: group=%q profile=%q for=%s err=%s",
		group,
		llmCandidateLabel(profile, true),
		duration.Round(time.Second),
		truncateRunes(errorText(err), llmFailoverLogErrLimit),
	)
}

// llmCooldownDuration 决定一次失败让候选冷却多久。
//
// 只冷却「换个请求也一样会失败」的上游状态：额度、限流、鉴权、模型不可用给默认
// 时长或上游提示的时长；普通 5xx、连接被拒给很短的冷却。内容拦截、上下文超限、
// 空输出、单次超时这些跟请求本身有关，不冷却——换一句话它可能就好了。
func llmCooldownDuration(err error) time.Duration {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0
	}
	if errors.Is(err, llm.ErrUnverifiedRejection) || errors.Is(err, llm.ErrDecisionRequired) {
		return 0
	}
	if errors.Is(err, errContentPolicyRejection) || isContentPolicyRejection(err) {
		return 0
	}
	if errors.Is(err, llm.ErrCompletionEmpty) || errors.Is(err, llm.ErrCompletionHasNoText) || errors.Is(err, llm.ErrCompletionTruncatedNoText) {
		return 0
	}
	if llm.IsContextOverflowError(err) {
		return 0
	}
	fallback := time.Duration(0)
	switch {
	case isRateLimitedLLMError(err), isCredentialRejectedLLMError(err), isModelUnavailableLLMError(err):
		fallback = llmCooldownDefault
	case isUpstreamUnavailableLLMError(err):
		fallback = llmCooldownTransient
	default:
		return 0
	}
	if hint := llmRetryAfterHint(err); hint > 0 {
		return min(hint, llmCooldownMax)
	}
	return fallback
}

// isRateLimitedLLMError 认出额度、账号池耗尽、限流这类失败。它们短时间内原地重试
// 没有意义，应当直接切下一个候选。
func isRateLimitedLLMError(err error) bool {
	if err == nil {
		return false
	}
	return containsAnyMarker(strings.ToLower(err.Error()), []string{
		"429", "too many requests",
		"rate limit", "rate_limit", "ratelimit", "rate-limited", "rate limited",
		"quota", "insufficient_quota", "resource_exhausted", "resource exhausted",
		"accounts limited", "account limited", "accounts are limited", "usage limit",
		"余额不足", "额度", "限流", "请求过于频繁",
	})
}

// isCredentialRejectedLLMError 认出密钥失效、未授权这类失败，改配置之前每次都一样。
func isCredentialRejectedLLMError(err error) bool {
	if err == nil {
		return false
	}
	return containsAnyMarker(strings.ToLower(err.Error()), []string{
		"401", "unauthorized", "invalid api key", "invalid_api_key", "incorrect api key",
		"authentication_error", "未授权",
	})
}

// isUpstreamUnavailableLLMError 认出上游 5xx、连接被拒这类偶发失败。超时不算：
// 大请求撞上单次超时不代表下一个小请求也会超时。
func isUpstreamUnavailableLLMError(err error) bool {
	if err == nil {
		return false
	}
	return containsAnyMarker(strings.ToLower(err.Error()), []string{
		"500 internal server error", "error 500,", "502", "503",
		"bad gateway", "service unavailable",
		"connection refused", "connection reset",
	})
}

// llmRetryAfterTextPattern 匹配各家写在错误正文里的等待提示：
// 「Wait 311000s」「retry after 30」「Please retry in 34.5s」「try again in 1m30s」、
// Gemini RetryInfo 的 "retryDelay": "30s"。
var llmRetryAfterTextPattern = regexp.MustCompile(`(?i)(?:\bwait|retry[ _-]?after|retry in|try again in|retrydelay)["':=\s]*([0-9]+(?:\.[0-9]+)?)\s*(ms|milliseconds?|s|secs?|seconds?|m|mins?|minutes?|h|hours?)?((?:[0-9]+(?:\.[0-9]+)?(?:ms|s|m|h))*)`)

// llmRetryAfterHint 取上游给的等待时间：先认协议层的 Retry-After 头，再认正文里的提示。
func llmRetryAfterHint(err error) time.Duration {
	if err == nil {
		return 0
	}
	if wait := llm.RetryAfterHint(err); wait > 0 {
		return wait
	}
	return parseLLMRetryAfterText(err.Error())
}

func parseLLMRetryAfterText(text string) time.Duration {
	match := llmRetryAfterTextPattern.FindStringSubmatch(text)
	if match == nil {
		return 0
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil || value <= 0 {
		return 0
	}
	unit := strings.ToLower(match[2])
	var wait time.Duration
	switch {
	case unit == "ms" || strings.HasPrefix(unit, "milli"):
		wait = time.Duration(value * float64(time.Millisecond))
	case unit == "m" || strings.HasPrefix(unit, "min"):
		wait = time.Duration(value * float64(time.Minute))
	case unit == "h" || strings.HasPrefix(unit, "hour"):
		wait = time.Duration(value * float64(time.Hour))
	default:
		// 不带单位的数字按 HTTP Retry-After 的约定当秒。
		wait = time.Duration(value * float64(time.Second))
	}
	// 「1m30s」这种组合写法，后半截交给标准库解析。
	if rest := match[3]; rest != "" && unit != "" {
		if extra, parseErr := time.ParseDuration(rest); parseErr == nil {
			wait += extra
		}
	}
	return wait
}

// llmFailoverLogErrLimit 是切换、冷却日志里错误正文的截断长度。上游错误常带整段
// JSON 甚至 HTML，全打出来一行就看不清了；完整错误仍会出现在最终的调用失败里。
const llmFailoverLogErrLimit = 200

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// llmCandidateLabel 是日志里给人看的候选名：「配置档名(ID 前 8 位)」，没有名字时
// 退回完整 ID。withModel 时再带上模型，同一个配置档下不同模型之间切换才分得清。
func llmCandidateLabel(profile llm.Profile, withModel bool) string {
	id := strings.TrimSpace(profile.ID)
	label := id
	if name := strings.TrimSpace(profile.Name); name != "" {
		short := id
		if len(short) > 8 {
			short = short[:8]
		}
		label = name
		if short != "" && short != name {
			label = fmt.Sprintf("%s(%s)", name, short)
		}
	}
	if model := strings.TrimSpace(profile.Config.Model); withModel && model != "" {
		label += "/" + model
	}
	return label
}

// llmFailoverLabels 给一次切换的两端取名。两端是同一个配置档时带上模型，
// 不然日志里是「从 A 切到 A」。
func llmFailoverLabels(from, to llm.Profile) (string, string) {
	sameProfile := strings.TrimSpace(from.ID) == strings.TrimSpace(to.ID)
	return llmCandidateLabel(from, sameProfile), llmCandidateLabel(to, sameProfile)
}

// logLLMFailover 打一行切换日志。stream 区分流式打开阶段的切换。
func logLLMFailover(group string, stream bool, from, to llm.Profile, err error) {
	fromLabel, toLabel := llmFailoverLabels(from, to)
	kind := "provider failover"
	if stream {
		kind = "stream provider failover"
	}
	log.Printf(
		"diana llm %s: group=%q model=%q from=%q to=%q err=%s",
		kind,
		group,
		from.Config.Model,
		fromLabel,
		toLabel,
		truncateRunes(errorText(err), llmFailoverLogErrLimit),
	)
}
