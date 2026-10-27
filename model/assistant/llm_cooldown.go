// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 候选健康状态跨请求保留；冷却期跳过，全部冷却时只探测最早恢复的一档。
const (
	// llmCooldownDefault 是额度、限流、鉴权这类失败没给等待时间时的冷却。
	llmCooldownDefault = 60 * time.Second
	// llmCooldownTransient 是普通 5xx、连接被拒这类偶发失败的冷却，只求接下来
	// 几秒别再先撞它；只有一个候选时冷却不会拦住它，见 order。
	llmCooldownTransient = 10 * time.Second
	// llmCooldownTimeout 是候选连续超时攒够次数后的冷却。一直卡住的上游每次都要
	// 白等一整份预算，比报错的上游代价大得多，冷却也要长一些；取值同 sub2api
	// 流超时的默认临时停调 5 分钟。
	llmCooldownTimeout = 5 * time.Minute
	// llmCooldownMax 给上游提示的等待时间封顶：「Wait 311000s」照单全收的话，上游
	// 提前恢复了也要三天多才会再用它。到期先探一次，还不行再冷却。
	llmCooldownMax = 30 * time.Minute
	// llmCooldownSkipLogInterval 是同一个候选「冷却中被跳过」两次写终端日志的最短
	// 间隔。每次调用都打的话，冷却本身就成了刷屏源。
	llmCooldownSkipLogInterval = 10 * time.Minute
	// llmFailuresBeforeCooldownDefault 是连续失败几次才把候选冷却掉。偶发一次限流
	// 就把主模型停掉、后面一分钟全走备用，比多试一次原模型难受得多。
	llmFailuresBeforeCooldownDefault = 3
	// llmFailureStreakWindow 是「连续失败」的有效期：上次失败（或冷却结束）之后隔了
	// 这么久才又失败，按新的一轮从 1 数起，昨天的零星失败不该攒到今天。
	llmFailureStreakWindow = 10 * time.Minute
)

type llmRecoveryPolicy struct {
	transientRetries  int
	retryDelay        time.Duration
	transientCooldown time.Duration
	statusCooldown    time.Duration
	maxCooldown       time.Duration
	respectRetryAfter bool
	// failuresBeforeCooldown 是连续失败多少次才进入冷却；1 等于一失败就冷却。
	failuresBeforeCooldown int
}

func defaultLLMRecoveryPolicy() llmRecoveryPolicy {
	return llmRecoveryPolicy{
		transientRetries:  llmTransientMaxRetries,
		retryDelay:        llmTransientRetryDelay,
		transientCooldown: llmCooldownTransient,
		statusCooldown:    llmCooldownDefault,
		maxCooldown:       llmCooldownMax,
		respectRetryAfter: true,

		failuresBeforeCooldown: llmFailuresBeforeCooldownDefault,
	}
}

func effectiveLLMRecoveryPolicy(policy *ModelRecoveryPolicy) llmRecoveryPolicy {
	if policy == nil {
		return defaultLLMRecoveryPolicy()
	}
	normalized := normalizeModelRecoveryPolicy(policy)
	return llmRecoveryPolicy{
		transientRetries:  normalized.TransientRetries,
		retryDelay:        time.Duration(normalized.RetryDelayMilliseconds) * time.Millisecond,
		transientCooldown: time.Duration(normalized.TransientCooldownSeconds) * time.Second,
		statusCooldown:    time.Duration(normalized.StatusCooldownSeconds) * time.Second,
		maxCooldown:       time.Duration(normalized.MaxCooldownSeconds) * time.Second,
		respectRetryAfter: normalized.RespectRetryAfter,

		failuresBeforeCooldown: normalized.FailuresBeforeCooldown,
	}
}

// llmCooldownTable 记每个候选（配置档 + 模型）冷却到什么时候。零值可用，nil 时
// 所有方法都退化成「没有冷却」，直接构造 provider 的测试不用管它。
type llmCooldownTable struct {
	mu      sync.Mutex
	entries map[string]*llmCooldownEntry
	// calls 记每个候选最近一次调用的结果，跟冷却状态分开存：调用成功会清掉冷却
	// 条目、手动恢复也会清，但「上次什么时候用过它」不该跟着没了。
	calls map[string]llmCallRecord
	now   func() time.Time
}

type llmCallRecord struct {
	at time.Time
	ok bool
}

type llmCooldownEntry struct {
	until       time.Time
	retainUntil time.Time
	failures    int
	lastFailure time.Time
	lastSkipLog time.Time
}

// ModelCandidateHealth 是控制台可读的单个模型候选运行时健康状态。只返回近期
// 失败过的候选；不在列表里的候选按健康状态处理。
type ModelCandidateHealth struct {
	ProfileID     string     `json:"profile_id"`
	Model         string     `json:"model"`
	FailureCount  int        `json:"failure_count"`
	LastFailure   time.Time  `json:"last_failure_at"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
}

// ModelCandidateCall 是单个候选最近一次调用的时间和结果，只在进程内保留。
type ModelCandidateCall struct {
	ProfileID string    `json:"profile_id"`
	Model     string    `json:"model"`
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
}

// ModelHealthSnapshot 带上服务端取样时间，前端可据此判断 cooldown_until 是否仍有效。
type ModelHealthSnapshot struct {
	CheckedAt  time.Time              `json:"checked_at"`
	Candidates []ModelCandidateHealth `json:"candidates"`
	Calls      []ModelCandidateCall   `json:"calls"`
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

type llmCandidateHealth struct {
	remaining time.Duration
	failures  int
}

func (t *llmCooldownTable) snapshot(profiles []llm.Profile) []llmCandidateHealth {
	health := make([]llmCandidateHealth, len(profiles))
	if t == nil {
		return health
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	for index, profile := range profiles {
		entry := t.entries[llmCooldownKey(profile)]
		if entry == nil || !entry.retainUntil.After(now) {
			continue
		}
		health[index] = llmCandidateHealth{
			remaining: max(0, entry.until.Sub(now)),
			failures:  entry.failures,
		}
	}
	return health
}

func (t *llmCooldownTable) publicSnapshot() ModelHealthSnapshot {
	now := time.Now()
	result := ModelHealthSnapshot{CheckedAt: now, Candidates: []ModelCandidateHealth{}, Calls: []ModelCandidateCall{}}
	if t == nil {
		return result
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now = t.clock()
	result.CheckedAt = now
	for key, entry := range t.entries {
		if !entry.retainUntil.After(now) {
			delete(t.entries, key)
			continue
		}
		profileID, model, ok := strings.Cut(key, "\x00")
		if !ok {
			continue
		}
		item := ModelCandidateHealth{
			ProfileID:    profileID,
			Model:        model,
			FailureCount: entry.failures,
			LastFailure:  entry.lastFailure,
		}
		if entry.until.After(now) {
			until := entry.until
			item.CooldownUntil = &until
		}
		result.Candidates = append(result.Candidates, item)
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].ProfileID != result.Candidates[j].ProfileID {
			return result.Candidates[i].ProfileID < result.Candidates[j].ProfileID
		}
		return result.Candidates[i].Model < result.Candidates[j].Model
	})
	for key, call := range t.calls {
		profileID, model, ok := strings.Cut(key, "\x00")
		if !ok {
			continue
		}
		result.Calls = append(result.Calls, ModelCandidateCall{ProfileID: profileID, Model: model, At: call.at, OK: call.ok})
	}
	sort.Slice(result.Calls, func(i, j int) bool {
		if result.Calls[i].ProfileID != result.Calls[j].ProfileID {
			return result.Calls[i].ProfileID < result.Calls[j].ProfileID
		}
		return result.Calls[i].Model < result.Calls[j].Model
	})
	return result
}

// noteCall 记下候选这次调用的结果。键的数量受配置档和模型个数约束，不用淘汰。
func (t *llmCooldownTable) noteCall(profile llm.Profile, ok bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.calls == nil {
		t.calls = map[string]llmCallRecord{}
	}
	t.calls[llmCooldownKey(profile)] = llmCallRecord{at: t.clock(), ok: ok}
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

// llmFailureMark 是一次失败记账的结果，切换日志据此告诉人「什么时候再试它」。
type llmFailureMark struct {
	// cooldown 为 0 表示这次没有进入冷却：要么这类失败不冷却，要么连续失败还没到阈值。
	cooldown time.Duration
	until    time.Time
	// fresh 表示这是一次新进入的冷却；已经在冷却中又失败只顺延截止时间。
	fresh     bool
	failures  int
	threshold int
	// counted 为 false 表示这类失败跟请求本身有关，不计入连续失败。
	counted bool
}

// markFailure 按失败原因给候选记一次失败，连续失败到阈值才冷却。
func (t *llmCooldownTable) markFailure(profile llm.Profile, err error) (time.Duration, bool) {
	mark := t.markFailureWithPolicy(profile, err, defaultLLMRecoveryPolicy())
	return mark.cooldown, mark.fresh
}

func (t *llmCooldownTable) markFailureWithPolicy(profile llm.Profile, err error, policy llmRecoveryPolicy) llmFailureMark {
	threshold := max(policy.failuresBeforeCooldown, 1)
	if t == nil {
		return llmFailureMark{threshold: threshold}
	}
	duration := llmCooldownDurationWithPolicy(err, policy)
	if duration <= 0 {
		return llmFailureMark{threshold: threshold}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	if t.entries == nil {
		t.entries = map[string]*llmCooldownEntry{}
	}
	// 冷却结束仍保留失败次数；长期未访问的状态才被清理。
	for key, entry := range t.entries {
		if !entry.retainUntil.After(now) {
			delete(t.entries, key)
		}
	}
	key := llmCooldownKey(profile)
	entry := t.entries[key]
	cooling := entry != nil && entry.until.After(now)
	if entry == nil {
		entry = &llmCooldownEntry{}
		t.entries[key] = entry
	}
	if !cooling && entry.failures > 0 && now.Sub(laterTime(entry.lastFailure, entry.until)) > llmFailureStreakWindow {
		entry.failures = 0
	}
	entry.failures++
	entry.lastFailure = now
	entry.retainUntil = now.Add(24 * time.Hour)
	mark := llmFailureMark{failures: entry.failures, threshold: threshold, counted: true}
	// 上游明说了要等多久就照办，不必攒够次数：它已经告诉我们现在发过去也是白发。
	// 鉴权失败、额度用完同理。
	hinted := policy.respectRetryAfter && llmRetryAfterHint(err) > 0
	if !hinted && !llmErrorPolicy(err).immediate && !cooling && entry.failures < threshold {
		return mark
	}
	if !hinted {
		first := threshold
		if llmErrorPolicy(err).immediate {
			first = 1
		}
		for n := first; n < entry.failures && duration < policy.maxCooldown; n++ {
			duration = min(duration*2, policy.maxCooldown)
		}
	}
	if until := now.Add(duration); until.After(entry.until) {
		entry.until = until
	}
	mark.cooldown, mark.until, mark.fresh = duration, entry.until, !cooling
	return mark
}

func laterTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
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

func (t *llmCooldownTable) clearMatching(profileIDs []string, model string) int {
	if t == nil {
		return 0
	}
	allowed := make(map[string]struct{}, len(profileIDs))
	for _, id := range profileIDs {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = struct{}{}
		}
	}
	model = strings.TrimSpace(model)
	if len(allowed) == 0 || model == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	removed := 0
	for key := range t.entries {
		profileID, candidateModel, ok := strings.Cut(key, "\x00")
		if !ok || candidateModel != model {
			continue
		}
		if _, ok := allowed[profileID]; ok {
			delete(t.entries, key)
			removed++
		}
	}
	return removed
}

// order 在原排序后过滤冷却候选；全部冷却时保留最早恢复的一档。
func (t *llmCooldownTable) order(group string, count int, start int, profileAt func(int) llm.Profile) []int {
	ready := make([]int, 0, count)
	var cooling []int
	if count == 0 {
		return ready
	}
	if start < 0 || start >= count {
		start = 0
	}
	profiles := make([]llm.Profile, count)
	for index := range profiles {
		profiles[index] = profileAt(index)
	}
	health := t.snapshot(profiles)
	for offset := 0; offset < count; offset++ {
		index := (start + offset) % count
		if count > 1 {
			if health[index].remaining > 0 {
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
	if len(ready) > 0 {
		return ready
	}
	earliest := cooling[0]
	for _, index := range cooling[1:] {
		if health[index].remaining < health[earliest].remaining {
			earliest = index
		}
	}
	return []int{earliest}
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

// noteLLMCandidateFailure 给失败的候选记一次失败，新进入冷却时打一行说明冷却多久。
func noteLLMCandidateFailure(cooldowns *llmCooldownTable, group string, profile llm.Profile, err error, policies ...llmRecoveryPolicy) llmFailureMark {
	policy := defaultLLMRecoveryPolicy()
	if len(policies) > 0 {
		policy = policies[0]
	}
	cooldowns.noteCall(profile, false)
	mark := cooldowns.markFailureWithPolicy(profile, err, policy)
	if mark.cooldown <= 0 || !mark.fresh {
		return mark
	}
	log.Printf(
		"diana llm provider cooldown: group=%q profile=%q for=%s failures=%d err=%s",
		group,
		llmCandidateLabel(profile, true),
		mark.cooldown.Round(time.Second),
		mark.failures,
		truncateRunes(errorText(err), llmFailoverLogErrLimit),
	)
	return mark
}

// llmCooldownDuration 决定一次失败让候选冷却多久。
//
// 只冷却「换个请求也一样会失败」的上游状态：额度、限流、鉴权、模型不可用给默认
// 时长或上游提示的时长；普通 5xx、连接被拒给很短的冷却。内容拦截、上下文超限、
// 空输出、单次超时这些跟请求本身有关，不冷却——换一句话它可能就好了。
func llmCooldownDuration(err error) time.Duration {
	return llmCooldownDurationWithPolicy(err, defaultLLMRecoveryPolicy())
}

func llmCooldownDurationWithPolicy(err error, policy llmRecoveryPolicy) time.Duration {
	fallback := llmErrorPolicy(err).cooldown
	if fallback == 0 {
		return 0
	}
	switch fallback {
	case llmCooldownTransient:
		fallback = policy.transientCooldown
	case llmCooldownTimeout:
	default:
		fallback = policy.statusCooldown
	}
	if fallback <= 0 {
		return 0
	}
	if policy.respectRetryAfter {
		if hint := llmRetryAfterHint(err); hint > 0 {
			return min(hint, policy.maxCooldown)
		}
	}
	return min(fallback, policy.maxCooldown)
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
