// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	telegramRecallProbeOperatorRole = "telegram_recall_probe"
	telegramRecallProbeCallTimeout  = 3 * time.Second
	telegramRecallProbeTotalTimeout = 12 * time.Second
	telegramRecallProbeRepeatAfter  = 30 * time.Second
	telegramRecallProbeFailureDelay = 30 * time.Second
	telegramRecallProbeCallInterval = 40 * time.Millisecond
)

// telegramRecallProbeSummary is returned with chat_history so the model cannot
// turn a bounded, best-effort scan into the stronger claim that no deletion
// happened. Counts exclude the one current-message guard request.
type telegramRecallProbeSummary struct {
	EligibleCount             int    `json:"eligible_count"`
	CandidateCount            int    `json:"candidate_count"`
	AttemptedCount            int    `json:"attempted_count"`
	ReachableCount            int    `json:"reachable_count"`
	ConfirmedUnavailableCount int    `json:"confirmed_unavailable_count"`
	RecalledCount             int    `json:"recorded_count"`
	PersistenceFailureCount   int    `json:"persistence_failure_count"`
	UnknownCount              int    `json:"unknown_count"`
	UncheckedCount            int    `json:"unchecked_count"`
	DeferredCount             int    `json:"deferred_count"`
	RecentlyCheckedCount      int    `json:"recently_checked_count"`
	KnownRecallCount          int    `json:"known_recall_count"`
	RetryAfterSeconds         int    `json:"retry_after_seconds,omitempty"`
	GuardAttempted            bool   `json:"guard_attempted"`
	GuardVerified             bool   `json:"guard_verified"`
	Complete                  bool   `json:"complete"`
	Limited                   bool   `json:"limited"`
	Note                      string `json:"note,omitempty"`
}

type telegramRecallProbeRegistry struct {
	mu       sync.Mutex
	accounts map[string]*telegramRecallProbeAccount
}

// telegramRecallProbeAccount owns a context-aware lease for one bot account.
// All mutable fields below are accessed only while that lease is held.
type telegramRecallProbeAccount struct {
	lease      chan struct{}
	checkedAt  map[string]time.Time
	retryUntil time.Time
}

type telegramRecallProbeDueCandidate struct {
	event     MessageEvent
	checkedAt time.Time
}

func (t *dianaChatHistoryTool) probeTelegramRecalls(ctx context.Context, input map[string]any) telegramRecallProbeSummary {
	if strings.TrimSpace(t.event.ProfileID) == "" || strings.TrimSpace(t.event.SelfID) == "" {
		return telegramRecallProbeSummary{
			Complete: false,
			Limited:  true,
			Note:     "当前消息缺少机器人档案或账号标识，为避免跨机器人误判，没有检查旧消息。",
		}
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, telegramRecallProbeTotalTimeout)
	defer probeCancel()
	account, release, acquireErr := t.runtime.acquireTelegramRecallProbeAccount(probeCtx, t.event)
	if acquireErr != nil {
		return telegramRecallProbeSummary{
			Complete: false,
			Limited:  true,
			Note:     "同一 Telegram 账号已有撤回探测在运行，本次等待已取消，没有检查旧消息。",
		}
	}
	defer release()

	limit := chatHistoryPositiveInt(input, "limit", defaultChatHistoryRecentLimit, maximumChatHistoryResultLimit)
	eligible, knownRecallCount, timelineLimited, loadErr := t.telegramRecallProbeCandidates(probeCtx)
	now := t.runtime.clock()
	candidates, recentlyChecked, deferred := account.selectCandidates(t.event.ProfileID, t.event.GroupID, eligible, limit, now)
	summary := telegramRecallProbeSummary{
		EligibleCount:        len(eligible),
		CandidateCount:       len(candidates),
		RecentlyCheckedCount: recentlyChecked,
		DeferredCount:        deferred,
		KnownRecallCount:     knownRecallCount,
		Complete:             loadErr == nil && !timelineLimited && recentlyChecked == 0 && deferred == 0,
		Limited:              timelineLimited || deferred > 0,
	}
	if loadErr != nil {
		appendTelegramRecallProbeNote(&summary, "持久化历史读取失败，本次只检查仍在内存中的缓存消息。")
	} else if timelineLimited && !t.runtime.messageTimelineStoreAvailable() {
		appendTelegramRecallProbeNote(&summary, "当前历史存储不支持按时间读取，本次只检查仍在内存中的缓存消息。")
	}
	if account.retryUntil.After(now) {
		summary.Complete = false
		summary.UncheckedCount = len(candidates)
		summary.RetryAfterSeconds = telegramRecallProbeRetrySeconds(now, account.retryUntil)
		appendTelegramRecallProbeNote(&summary, "该 Telegram 账号仍在上次失败或限流的冷却期，本次没有发起新的探测请求。")
		return summary
	}
	if len(candidates) == 0 {
		return summary
	}

	channel, platform, routeErr := t.runtime.outboundChannelForEvent(t.event)
	if routeErr != nil || platform != PlatformTelegram {
		summary.Complete = false
		summary.UncheckedCount = len(candidates)
		account.deferRetry(now, 0)
		summary.RetryAfterSeconds = telegramRecallProbeRetrySeconds(now, account.retryUntil)
		appendTelegramRecallProbeNote(&summary, "当前 Telegram 连接不可用，本次没有把缓存消息判定为已不可访问。")
		return summary
	}

	// A successful request against the message that triggered this tool proves
	// that the selected profile, chat, and method are usable right now. Without
	// this guard, a wrong route or a chat-wide permission failure could make an
	// entire cache look unavailable.
	guardID, guardOK := telegramRecallProbeMessageID(t.event.MessageID)
	if !guardOK {
		summary.Complete = false
		summary.UncheckedCount = len(candidates)
		appendTelegramRecallProbeNote(&summary, "当前消息没有可验证的 Telegram message_id，为避免误判，没有检查旧消息。")
		return summary
	}

	guardCtx, guardCancel := context.WithTimeout(probeCtx, telegramRecallProbeCallTimeout)
	summary.GuardAttempted = true
	guardErr := telegramRecallProbeCall(guardCtx, channel, t.event.GroupID, guardID)
	guardCancel()
	if guardErr != nil {
		failureAt := t.runtime.clock()
		summary.Complete = false
		summary.UncheckedCount = len(candidates)
		_, _, retryAfter := telegramRecallProbeError(guardErr)
		account.deferRetry(failureAt, retryAfter)
		summary.RetryAfterSeconds = telegramRecallProbeRetrySeconds(failureAt, account.retryUntil)
		appendTelegramRecallProbeNote(&summary, "当前消息的存在性校验失败，为避免把连接、权限或接口错误误判为消息不可访问，没有继续检查旧消息。")
		return summary
	}
	summary.GuardVerified = true
	lastCallAt := time.Now()

	for index, candidate := range candidates {
		if probeCtx.Err() != nil {
			summary.Complete = false
			summary.UncheckedCount += len(candidates) - index
			appendTelegramRecallProbeNote(&summary, "探测达到本次时间上限，剩余消息没有检查。")
			break
		}
		messageID, ok := telegramRecallProbeMessageID(candidate.MessageID)
		if !ok {
			continue
		}
		if waitErr := waitTelegramRecallProbePace(probeCtx, channel, lastCallAt); waitErr != nil {
			summary.Complete = false
			summary.UncheckedCount += len(candidates) - index
			appendTelegramRecallProbeNote(&summary, "探测达到本次时间上限，剩余消息没有检查。")
			break
		}
		callCtx, callCancel := context.WithTimeout(probeCtx, telegramRecallProbeCallTimeout)
		err := telegramRecallProbeCall(callCtx, channel, t.event.GroupID, messageID)
		callCancel()
		lastCallAt = time.Now()
		checkedAt := t.runtime.clock()
		summary.AttemptedCount++
		if err == nil {
			summary.ReachableCount++
			account.noteChecked(t.event.ProfileID, t.event.GroupID, candidate.MessageID, checkedAt)
			continue
		}

		missing, stop, retryAfter := telegramRecallProbeError(err)
		if missing {
			summary.ConfirmedUnavailableCount++
			marker := candidate
			marker.Platform = firstNonEmpty(t.event.Platform, candidate.Platform, PlatformTelegram)
			marker.ProfileID = firstNonEmpty(t.event.ProfileID, candidate.ProfileID)
			marker.ContextNamespace = firstNonEmpty(t.event.ContextNamespace, candidate.ContextNamespace)
			marker.SelfID = firstNonEmpty(t.event.SelfID, candidate.SelfID)
			marker.OperatorRole = telegramRecallProbeOperatorRole
			marker.Time = checkedAt.Unix()
			recorded, recordErr := t.runtime.recordTelegramProbedRecall(probeCtx, marker, candidate)
			if recordErr == nil && recorded {
				summary.RecalledCount++
				account.noteChecked(t.event.ProfileID, t.event.GroupID, candidate.MessageID, checkedAt)
			} else if recordErr != nil {
				summary.PersistenceFailureCount++
				summary.Complete = false
				appendTelegramRecallProbeNote(&summary, "Telegram 已确认消息当前不可访问，但本地记录保存失败；下次查询会重试保存。")
			} else {
				account.noteChecked(t.event.ProfileID, t.event.GroupID, candidate.MessageID, checkedAt)
			}
			continue
		}

		summary.UnknownCount++
		summary.Complete = false
		if !stop {
			account.noteChecked(t.event.ProfileID, t.event.GroupID, candidate.MessageID, checkedAt)
		}
		if retryAfter > summary.RetryAfterSeconds {
			summary.RetryAfterSeconds = retryAfter
		}
		if stop {
			summary.Complete = false
			summary.UncheckedCount += len(candidates) - index - 1
			account.deferRetry(checkedAt, retryAfter)
			summary.RetryAfterSeconds = telegramRecallProbeRetrySeconds(checkedAt, account.retryUntil)
			appendTelegramRecallProbeNote(&summary, "Telegram 限流、连接、权限或服务端错误使本次探测提前停止；未检查的消息不会被记为不可访问。")
			break
		}
	}
	return summary
}

func (t *dianaChatHistoryTool) telegramRecallProbeCandidates(ctx context.Context) ([]MessageEvent, int, bool, error) {
	referenceTime := t.runtime.clock().Unix()
	cutoff := referenceTime - int64(recallDefaultWindow/time.Second)
	t.runtime.mu.RLock()
	store := t.runtime.messageStore
	memory := append([]MessageEvent(nil), t.runtime.history[sessionKey(t.event)]...)
	t.runtime.mu.RUnlock()

	events, loadErr := t.timeline(ctx, cutoff, referenceTime)
	timelineComplete := false
	if _, ok := store.(MessageTimelineStore); ok && loadErr == nil {
		timelineComplete = true
	}
	if loadErr != nil {
		events = memory
	}

	knownRecallIDs := make(map[string]struct{})
	for _, recall := range t.runtime.recallHistory(t.event) {
		if !sameRecallScope(t.event, recall) {
			continue
		}
		if id := strings.TrimSpace(recall.MessageID); id != "" {
			knownRecallIDs[id] = struct{}{}
		}
	}
	currentMessageID := strings.TrimSpace(t.event.MessageID)
	currentProfileID := strings.TrimSpace(t.event.ProfileID)
	currentSelfID := strings.TrimSpace(t.event.SelfID)
	seen := make(map[string]struct{})
	candidates := make([]MessageEvent, 0, len(events))
	limited := !timelineComplete
	for index := len(events) - 1; index >= 0; index-- {
		candidate := events[index]
		messageID := strings.TrimSpace(candidate.MessageID)
		if candidate.Kind != EventKindGroup || candidate.GroupID != t.event.GroupID || candidate.Outbound ||
			NormalizePlatformID(candidate.Platform) != PlatformTelegram || strings.TrimSpace(candidate.ProfileID) != currentProfileID ||
			strings.TrimSpace(candidate.SelfID) != currentSelfID || strings.TrimSpace(candidate.UserID) == currentSelfID ||
			candidate.Time <= 0 || candidate.Time < cutoff || candidate.Time > referenceTime || !telegramRecallProbeHasContent(candidate) ||
			messageID == "" || messageID == currentMessageID {
			continue
		}
		if _, ok := telegramRecallProbeMessageID(messageID); !ok {
			continue
		}
		if _, ok := knownRecallIDs[messageID]; ok {
			continue
		}
		if _, ok := seen[messageID]; ok {
			continue
		}
		seen[messageID] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return candidates, len(knownRecallIDs), limited, loadErr
}

func (r *Runtime) acquireTelegramRecallProbeAccount(ctx context.Context, event MessageEvent) (*telegramRecallProbeAccount, func(), error) {
	key := strings.Join([]string{
		NormalizePlatformID(event.Platform),
		strings.TrimSpace(event.SelfID),
	}, "\x00")
	registry := &r.telegramRecallProbes
	registry.mu.Lock()
	if registry.accounts == nil {
		registry.accounts = make(map[string]*telegramRecallProbeAccount)
	}
	account := registry.accounts[key]
	if account == nil {
		account = &telegramRecallProbeAccount{
			lease:     make(chan struct{}, 1),
			checkedAt: make(map[string]time.Time),
		}
		account.lease <- struct{}{}
		registry.accounts[key] = account
	}
	registry.mu.Unlock()
	select {
	case <-account.lease:
		return account, func() { account.lease <- struct{}{} }, nil
	case <-ctx.Done():
		return nil, func() {}, ctx.Err()
	}
}

func (a *telegramRecallProbeAccount) selectCandidates(profileID, groupID string, eligible []MessageEvent, limit int, now time.Time) ([]MessageEvent, int, int) {
	if a == nil || limit <= 0 {
		return nil, 0, len(eligible)
	}
	retentionCutoff := now.Add(-recallDefaultWindow - telegramRecallProbeRepeatAfter)
	for key, checkedAt := range a.checkedAt {
		if checkedAt.Before(retentionCutoff) {
			delete(a.checkedAt, key)
		}
	}
	unprobed := make([]MessageEvent, 0, len(eligible))
	due := make([]telegramRecallProbeDueCandidate, 0, len(eligible))
	recentlyChecked := 0
	for _, candidate := range eligible {
		checkedAt, ok := a.checkedAt[telegramRecallProbeCheckedKey(profileID, groupID, candidate.MessageID)]
		switch {
		case !ok:
			unprobed = append(unprobed, candidate)
		case now.Sub(checkedAt) >= telegramRecallProbeRepeatAfter:
			due = append(due, telegramRecallProbeDueCandidate{event: candidate, checkedAt: checkedAt})
		default:
			recentlyChecked++
		}
	}
	// Once every message has been seen, recheck the least recently inspected
	// ones first. New unprobed messages still keep newest-first order.
	sort.SliceStable(due, func(i, j int) bool { return due[i].checkedAt.Before(due[j].checkedAt) })
	selected := make([]MessageEvent, 0, min(limit, len(unprobed)+len(due)))
	for _, candidate := range unprobed {
		if len(selected) >= limit {
			break
		}
		selected = append(selected, candidate)
	}
	for _, candidate := range due {
		if len(selected) >= limit {
			break
		}
		selected = append(selected, candidate.event)
	}
	deferred := len(unprobed) + len(due) - len(selected)
	return selected, recentlyChecked, deferred
}

func (a *telegramRecallProbeAccount) noteChecked(profileID, groupID, messageID string, at time.Time) {
	if a == nil {
		return
	}
	if a.checkedAt == nil {
		a.checkedAt = make(map[string]time.Time)
	}
	a.checkedAt[telegramRecallProbeCheckedKey(profileID, groupID, messageID)] = at
}

func (a *telegramRecallProbeAccount) deferRetry(now time.Time, retryAfterSeconds int) {
	if a == nil {
		return
	}
	delay := telegramRecallProbeFailureDelay
	if retryAfterSeconds > 0 {
		delay = time.Duration(retryAfterSeconds) * time.Second
	}
	until := now.Add(delay)
	if until.After(a.retryUntil) {
		a.retryUntil = until
	}
}

func telegramRecallProbeCheckedKey(profileID, groupID, messageID string) string {
	return strings.Join([]string{
		strings.TrimSpace(profileID),
		strings.TrimSpace(groupID),
		strings.TrimSpace(messageID),
	}, "\x00")
}

func telegramRecallProbeRetrySeconds(now, until time.Time) int {
	if !until.After(now) {
		return 0
	}
	return max(1, int((until.Sub(now)+time.Second-1)/time.Second))
}

func telegramRecallProbeHasContent(event MessageEvent) bool {
	if strings.TrimSpace(event.RawMessage) != "" || strings.TrimSpace(PlainText(event.Segments)) != "" {
		return true
	}
	for _, segment := range event.Segments {
		switch segment.Type {
		case "image", "video", "record", "file", "forward":
			return true
		}
	}
	return false
}

func telegramRecallProbeMessageID(value string) (int64, bool) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed, err == nil && parsed > 0
}

func telegramRecallProbeCall(ctx context.Context, channel Channel, chatID string, messageID int64) error {
	_, err := channel.CallAPI(ctx, "setMessageReaction", map[string]any{
		"chat_id":    strings.TrimSpace(chatID),
		"message_id": messageID,
		"reaction":   []any{},
	})
	return err
}

// Keep production Bot API probing below Telegram's general per-bot request
// ceiling. Fake channels stay immediate so unit tests remain deterministic.
// The account lease applies the same cadence across groups using one bot ID.
func waitTelegramRecallProbePace(ctx context.Context, channel Channel, lastCallAt time.Time) error {
	if _, ok := channel.(*TelegramChannel); !ok {
		return nil
	}
	wait := time.Until(lastCallAt.Add(telegramRecallProbeCallInterval))
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// telegramRecallProbeError returns (missing, stop, retryAfterSeconds). Only the
// exact reaction-specific not-found response proves only that this bot cannot
// read the message at probe time. It is not proof of who deleted it or why. Every
// other 400 remains unknown; connection-wide failures stop the remaining scan.
func telegramRecallProbeError(err error) (bool, bool, int) {
	if err == nil {
		return false, false, 0
	}
	var apiErr *telegramAPIError
	if errors.As(err, &apiErr) {
		description := strings.ToLower(strings.TrimSpace(apiErr.Description))
		description = strings.TrimSpace(strings.TrimPrefix(description, "bad request:"))
		if apiErr.Code == 400 && description == "message to react not found" {
			return true, false, 0
		}
		stop := apiErr.Code == 401 || apiErr.Code == 403 || apiErr.Code == 404 || apiErr.Code == 429 || apiErr.Code >= 500
		if strings.Contains(description, "method not found") {
			stop = true
		}
		return false, stop, apiErr.RetryAfterSeconds
	}
	// Transport and context errors affect the connection rather than one
	// message. Stop so a temporary outage cannot generate a burst of requests.
	return false, true, 0
}

func appendTelegramRecallProbeNote(summary *telegramRecallProbeSummary, note string) {
	if summary == nil || strings.TrimSpace(note) == "" {
		return
	}
	if summary.Note == "" {
		summary.Note = note
		return
	}
	summary.Note += " " + note
}

func telegramRecallProbeMessage(summary telegramRecallProbeSummary) string {
	var message string
	switch {
	case summary.CandidateCount == 0 && summary.KnownRecallCount > 0:
		message = "Telegram 不会实时推送普通群消息删除；当前没有尚未确认的新候选，已有的不可访问记录见本次结果。"
	case summary.EligibleCount == 0:
		message = "Telegram 不会实时推送普通群消息删除；本地最近 24 小时没有带有效 message_id 的新候选消息，因此本次无法新增确认。"
	case summary.CandidateCount == 0 && summary.RecentlyCheckedCount > 0:
		message = "Telegram 不会实时推送普通群消息删除；符合条件的缓存消息刚刚检查过，本次没有重复请求 Telegram。"
	case summary.RetryAfterSeconds > 0 && !summary.GuardAttempted:
		message = "Telegram 不会实时推送普通群消息删除；该机器人账号仍在探测冷却期，本次没有重复请求 Telegram。"
	case summary.GuardAttempted && !summary.GuardVerified:
		message = "Telegram 不会实时推送普通群消息删除；当前消息的安全校验没有通过，本次未检查旧消息，也没有把任何缓存消息记为不可访问。"
	case !summary.GuardAttempted && summary.UncheckedCount > 0:
		message = "Telegram 不会实时推送普通群消息删除；本次未能发起安全探测，缓存候选均保持未确认状态。"
	default:
		message = "Telegram 不会实时推送普通群消息删除；已按需探测本群本地缓存中的 " + strconv.Itoa(summary.AttemptedCount) + " 条消息，"
		if summary.ConfirmedUnavailableCount > 0 {
			message += "确认 " + strconv.Itoa(summary.ConfirmedUnavailableCount) + " 条在查询时已不可访问。"
			if summary.RecalledCount > 0 {
				message += " 其中 " + strconv.Itoa(summary.RecalledCount) + " 条已保存到本地记录。"
			}
		} else {
			message += "本次覆盖范围内没有确认当前已不可访问的消息；这不能证明群里没有发生撤回。"
		}
	}
	if summary.PersistenceFailureCount > 0 {
		message += " " + strconv.Itoa(summary.PersistenceFailureCount) + " 条虽已确认不可访问，但本地记录保存失败。"
	}
	if summary.UnknownCount > 0 {
		message += " " + strconv.Itoa(summary.UnknownCount) + " 条因接口错误状态未知，没有按撤回处理。"
	}
	if summary.UncheckedCount > 0 {
		message += " " + strconv.Itoa(summary.UncheckedCount) + " 条因探测提前停止而未检查。"
	}
	if summary.DeferredCount > 0 {
		message += " 还有 " + strconv.Itoa(summary.DeferredCount) + " 条可检查候选留待后续查询；每次查询会轮转到尚未检查或最久未检查的消息，也可把 limit 调大到单次最多 50 条。"
	} else if summary.Limited {
		message += " 本次只覆盖了当前可读取的缓存范围。"
	}
	if summary.RecentlyCheckedCount > 0 {
		message += " " + strconv.Itoa(summary.RecentlyCheckedCount) + " 条在 30 秒内已经检查过，本次没有重复请求。"
	}
	if summary.Note != "" {
		message += " " + summary.Note
	}
	message += " 探测只能说明查询时消息是否仍可访问，无法得知实际删除时间、删除者或删除原因。"
	return strings.TrimSpace(message)
}

// recordTelegramProbedRecall persists a synthetic notice without feeding it
// back through the inbound event pipeline. A probe happens inside the current
// reply turn; it should update recall history, but it is not a newly received
// platform event and must not alter inbound reply state.
func (r *Runtime) recordTelegramProbedRecall(ctx context.Context, candidate, original MessageEvent) (bool, error) {
	recall := buildRecoveredGroupRecallEvent(candidate, original)
	if candidate.Time > 0 {
		recall.Time = candidate.Time
	}
	r.mu.RLock()
	store := r.messageStore
	noticeStore, hasNoticeStore := r.messageStore.(NoticeAuditStore)
	r.mu.RUnlock()
	if store == nil {
		return false, errors.New("保存 Telegram 撤回探测记录失败：消息历史存储未配置")
	}
	if !hasNoticeStore || noticeStore == nil {
		return false, errors.New("保存 Telegram 撤回探测记录失败：事件审计存储不可用")
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("保存 Telegram 撤回探测记录失败: %w", err)
	}
	// Write the idempotent audit row first. If message history then fails, the
	// next query can safely upsert the same audit ID and retry the durable recall.
	if err := noticeStore.RecordNoticeEvent(ctx, sessionKey(recall), withoutReplyRuntimeState(recall)); err != nil {
		return false, fmt.Errorf("保存 Telegram 撤回探测审计记录失败: %w", err)
	}
	if err := store.AppendMessageEvent(ctx, sessionKey(recall), withoutReplyRuntimeState(recall)); err != nil {
		return false, fmt.Errorf("保存 Telegram 撤回探测历史失败: %w", err)
	}
	r.pruneStickerLibrary(ctx, store, recall)
	r.enqueueSemanticIndex(recall)
	if r.plugins != nil {
		recall = r.plugins.ObserveEventWithOverrides(ctx, recall, r.pluginOverridesForEvent(recall))
	}
	return true, nil
}

func (r *Runtime) messageTimelineStoreAvailable() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.messageStore.(MessageTimelineStore)
	return ok
}
