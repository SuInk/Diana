// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// DashboardOverviewQuery 是总览页大跨度综合分析的查询条件。
type DashboardOverviewQuery struct {
	Since      time.Time
	Until      time.Time
	ProfileID  string
	ErrorLimit int
}

// DashboardWindowSummary 是当前筛选窗口内的完整聚合数据。
type DashboardWindowSummary struct {
	Since           time.Time           `json:"since"`
	Until           time.Time           `json:"until"`
	Messages        int64               `json:"messages"`
	Handled         int64               `json:"handled"`
	NotReplied      int64               `json:"not_replied"`
	Pending         int64               `json:"pending"`
	Errors          int64               `json:"errors"`
	ActiveGroups    int64               `json:"active_groups"`
	ActiveUsers     int64               `json:"active_users"`
	AvgReplyMS      int64               `json:"avg_reply_ms"`
	RepliesMeasured int64               `json:"replies_measured"`
	AppErrorLogs    int64               `json:"app_error_logs"`
	Usage           applog.UsageSummary `json:"usage"`
}

// DashboardPreviousSummary 是紧邻前一段等长窗口的核心指标，用于计算环比涨跌。
type DashboardPreviousSummary struct {
	Since           time.Time `json:"since"`
	Until           time.Time `json:"until"`
	Messages        int64     `json:"messages"`
	Handled         int64     `json:"handled"`
	Errors          int64     `json:"errors"`
	AvgReplyMS      int64     `json:"avg_reply_ms"`
	RepliesMeasured int64     `json:"replies_measured"`
	RecordedCalls   int64     `json:"recorded_calls"`
	FailedCalls     int64     `json:"failed_calls"`
	TotalTokens     int64     `json:"total_tokens"`
	AppErrorLogs    int64     `json:"app_error_logs"`
}

// DashboardGroupStat 汇总某个群在当前窗口内的消息处理与 Token 消耗排行。
type DashboardGroupStat struct {
	ProfileID         string `json:"profile_id,omitempty"`
	Platform          string `json:"platform,omitempty"`
	GroupID           string `json:"group_id"`
	GroupName         string `json:"group_name,omitempty"`
	Messages          int64  `json:"messages"`
	Handled           int64  `json:"handled"`
	Errors            int64  `json:"errors"`
	Calls             int64  `json:"recorded_calls"`
	FailedCalls       int64  `json:"failed_calls"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
}

// DashboardErrorCategory 按错误来源（事件处理、模型调用、运行日志）及细分类别聚合异常计数。
type DashboardErrorCategory struct {
	Source         string    `json:"source"`
	Key            string    `json:"key"`
	Label          string    `json:"label"`
	Model          string    `json:"model,omitempty"`
	Purpose        string    `json:"purpose,omitempty"`
	Action         string    `json:"action,omitempty"`
	Count          int64     `json:"count"`
	RecoveredCount int64     `json:"recovered_count,omitempty"`
	LatestAt       time.Time `json:"latest_at"`
	Sample         string    `json:"sample,omitempty"`
}

// DashboardErrorItem 是一条可直接点进对应日志或事件详情的异常摘要。
type DashboardErrorItem struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Source    string    `json:"source"` // "event" | "llm" | "log"
	Action    string    `json:"action,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	Message   string    `json:"message"`
	Detail    string    `json:"detail,omitempty"`
	Model     string    `json:"model,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Purpose   string    `json:"purpose,omitempty"`
	ProfileID string    `json:"profile_id,omitempty"`
	GroupID   string    `json:"group_id,omitempty"`
	GroupName string    `json:"group_name,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	Recovered bool      `json:"recovered,omitempty"`
}

// DashboardErrorOverview 汇总当前筛选窗口内的全部异常计数、分类和最近样本。
type DashboardErrorOverview struct {
	EventErrors       int64                    `json:"event_errors"`
	LLMFailedCalls    int64                    `json:"llm_failed_calls"`
	LLMRecoveredCalls int64                    `json:"llm_recovered_calls"`
	AppErrorLogs      int64                    `json:"app_error_logs"`
	Categories        []DashboardErrorCategory `json:"categories"`
	Recent            []DashboardErrorItem     `json:"recent"`
}

// DashboardOverviewData 是总览页大跨度查询接口的持久层返回结果。
type DashboardOverviewData struct {
	Current  DashboardWindowSummary   `json:"current"`
	Previous DashboardPreviousSummary `json:"previous"`
	Groups   []DashboardGroupStat     `json:"groups"`
	Errors   DashboardErrorOverview   `json:"errors"`
}

// DashboardOverview 按任意时间跨度和机器人作用域聚合消息、回复、Token、群排行及异常明细。
func (s *SQLiteStore) DashboardOverview(ctx context.Context, query DashboardOverviewQuery) (DashboardOverviewData, error) {
	defer s.observeStorage(ctx, "DashboardOverview", "read")()
	if s == nil || s.db == nil {
		return DashboardOverviewData{}, fmt.Errorf("overview storage unavailable")
	}
	if !query.Since.Before(query.Until) {
		return DashboardOverviewData{}, fmt.Errorf("invalid overview window")
	}
	errorLimit := query.ErrorLimit
	if errorLimit <= 0 {
		errorLimit = 20
	}
	if errorLimit > 100 {
		errorLimit = 100
	}
	profileID := strings.TrimSpace(query.ProfileID)
	span := query.Until.Sub(query.Since)
	prevSince := query.Since.Add(-span)
	prevUntil := query.Since

	out := DashboardOverviewData{
		Current:  DashboardWindowSummary{Since: query.Since, Until: query.Until},
		Previous: DashboardPreviousSummary{Since: prevSince, Until: prevUntil},
		Groups:   []DashboardGroupStat{},
		Errors: DashboardErrorOverview{
			Categories: []DashboardErrorCategory{},
			Recent:     []DashboardErrorItem{},
		},
	}

	groupMap := map[string]*DashboardGroupStat{}
	ensureGroup := func(profile, platform, groupID string) *DashboardGroupStat {
		groupID = strings.TrimSpace(groupID)
		if groupID == "" {
			return nil
		}
		item := groupMap[groupID]
		if item == nil {
			item = &DashboardGroupStat{
				ProfileID: strings.TrimSpace(profile),
				Platform:  strings.TrimSpace(platform),
				GroupID:   groupID,
			}
			groupMap[groupID] = item
		} else {
			if item.ProfileID == "" && strings.TrimSpace(profile) != "" {
				item.ProfileID = strings.TrimSpace(profile)
			}
			if item.Platform == "" && strings.TrimSpace(platform) != "" {
				item.Platform = strings.TrimSpace(platform)
			}
		}
		return item
	}

	// 1. 当前窗口入站事件统计与群消息统计
	if err := s.fillOverviewEvents(ctx, query.Since, query.Until, profileID, ensureGroup, &out.Current); err != nil {
		return out, err
	}
	// 2. 前一段等长窗口入站事件统计（供环比对照）
	if err := s.fillOverviewPreviousEvents(ctx, prevSince, prevUntil, profileID, &out.Previous); err != nil {
		return out, err
	}
	// 3. 当前窗口 LLM 用量、模型/用途拆解、群 Token 归属
	usageReport, err := s.LLMUsageReport(ctx, applog.UsageFilter{ProfileID: profileID}, query.Since, query.Until)
	if err != nil {
		return out, err
	}
	out.Current.Usage = usageReport.Usage
	for _, grp := range usageReport.Groups {
		stat := ensureGroup(grp.ProfileID, grp.Platform, grp.GroupID)
		if stat == nil {
			continue
		}
		stat.Calls += grp.Usage.Calls
		stat.FailedCalls += grp.Usage.FailedCalls
		stat.InputTokens += grp.Usage.InputTokens
		stat.OutputTokens += grp.Usage.OutputTokens
		stat.CachedInputTokens += grp.Usage.CachedInputTokens
		stat.TotalTokens += grp.Usage.TotalTokens
	}

	// 4. 当前窗口模型调用失败与运行错误聚合
	categories := map[string]*DashboardErrorCategory{}
	var recentErrors []DashboardErrorItem
	if err := s.fillOverviewLogsAndErrors(ctx, query.Since, query.Until, profileID, categories, &recentErrors, errorLimit, &out.Current); err != nil {
		return out, err
	}
	// 5. 当前窗口入站事件异常分类与最近样本
	if err := s.fillOverviewEventErrors(ctx, query.Since, query.Until, profileID, categories, &recentErrors, errorLimit, ensureGroup); err != nil {
		return out, err
	}
	// 6. 补齐群聊历史名称（兼容 Telegram 等无实时群列表的平台）与前一周期日志指标
	s.fillOverviewGroupNames(ctx, groupMap)
	if err := s.fillOverviewPreviousLogs(ctx, prevSince, prevUntil, profileID, &out.Previous); err != nil {
		return out, err
	}

	for _, grp := range groupMap {
		out.Groups = append(out.Groups, *grp)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		if a.TotalTokens != b.TotalTokens {
			return a.TotalTokens > b.TotalTokens
		}
		if a.Messages != b.Messages {
			return a.Messages > b.Messages
		}
		return a.GroupID < b.GroupID
	})

	for _, cat := range categories {
		out.Errors.Categories = append(out.Errors.Categories, *cat)
	}
	sort.Slice(out.Errors.Categories, func(i, j int) bool {
		a, b := out.Errors.Categories[i], out.Errors.Categories[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if !a.LatestAt.Equal(b.LatestAt) {
			return a.LatestAt.After(b.LatestAt)
		}
		return a.Key < b.Key
	})

	sort.Slice(recentErrors, func(i, j int) bool {
		if !recentErrors[i].At.Equal(recentErrors[j].At) {
			return recentErrors[i].At.After(recentErrors[j].At)
		}
		return recentErrors[i].ID > recentErrors[j].ID
	})
	if len(recentErrors) > errorLimit {
		recentErrors = recentErrors[:errorLimit]
	}
	out.Errors.Recent = recentErrors
	out.Errors.EventErrors = out.Current.Errors
	out.Errors.LLMFailedCalls = out.Current.Usage.FailedCalls
	out.Errors.LLMRecoveredCalls = out.Current.Usage.RecoveredCalls
	out.Errors.AppErrorLogs = out.Current.AppErrorLogs

	return out, nil
}

func (s *SQLiteStore) fillOverviewEvents(
	ctx context.Context,
	since, until time.Time,
	profileID string,
	ensureGroup func(profile, platform, groupID string) *DashboardGroupStat,
	summary *DashboardWindowSummary,
) error {
	sinceSec, untilSec := since.Unix(), until.Unix()
	sinceNanos, untilNanos := since.UnixNano(), until.UnixNano()
	scope, scopeArgs := dashboardProfileScope(profileID)

	if err := s.eventReader().QueryRowContext(ctx, `
SELECT
  COUNT(*),
  COUNT(DISTINCT NULLIF(TRIM(group_id), '')),
  COUNT(DISTINCT NULLIF(TRIM(user_id), '')),
  COALESCE(SUM(CASE WHEN status != 'done' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status = 'done' AND NOT `+inboundHandledPredicate+` AND NOT `+inboundFailedPredicate+` AND COALESCE(decision, '') != 'notice' THEN 1 ELSE 0 END), 0)
FROM inbound_events
WHERE event_time >= ? AND event_time < ?`+scope,
		append([]any{sinceSec, untilSec}, scopeArgs...)...).Scan(
		&summary.Messages,
		&summary.ActiveGroups,
		&summary.ActiveUsers,
		&summary.Pending,
		&summary.NotReplied,
	); err != nil {
		return fmt.Errorf("overview inbound summary: %w", err)
	}

	var avg sql.NullFloat64
	if err := s.eventReader().QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN `+inboundHandledPredicate+` THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN `+inboundFailedPredicate+` THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN `+inboundHandledPredicate+` AND COALESCE(duration_ms, 0) > 0 THEN 1 ELSE 0 END), 0),
  AVG(CASE WHEN `+inboundHandledPredicate+` AND COALESCE(duration_ms, 0) > 0 THEN duration_ms END)
FROM inbound_events
WHERE completed_at >= ? AND completed_at < ?`+scope,
		append([]any{sinceNanos, untilNanos}, scopeArgs...)...).Scan(
		&summary.Handled,
		&summary.Errors,
		&summary.RepliesMeasured,
		&avg,
	); err != nil {
		return fmt.Errorf("overview outcome summary: %w", err)
	}
	if avg.Valid {
		summary.AvgReplyMS = int64(avg.Float64)
	}

	// 按群聚合收到消息
	msgRows, err := s.eventReader().QueryContext(ctx, `
SELECT COALESCE(TRIM(profile_id), ''), COALESCE(TRIM(group_id), ''), COUNT(*)
FROM inbound_events
WHERE event_time >= ? AND event_time < ?`+scope+`
GROUP BY COALESCE(TRIM(profile_id), ''), COALESCE(TRIM(group_id), '')`,
		append([]any{sinceSec, untilSec}, scopeArgs...)...)
	if err != nil {
		return fmt.Errorf("overview message buckets: %w", err)
	}
	defer msgRows.Close()
	for msgRows.Next() {
		var count int64
		var rowProfile, groupID string
		if err := msgRows.Scan(&rowProfile, &groupID, &count); err != nil {
			return err
		}
		if grp := ensureGroup(rowProfile, "", groupID); grp != nil {
			grp.Messages += count
		}
	}
	if err := msgRows.Err(); err != nil {
		return err
	}

	// 按群聚合已回复与异常完成数
	doneRows, err := s.eventReader().QueryContext(ctx, `
SELECT COALESCE(TRIM(profile_id), ''),
  COALESCE(TRIM(group_id), ''),
  COALESCE(SUM(CASE WHEN `+inboundHandledPredicate+` THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN `+inboundFailedPredicate+` THEN 1 ELSE 0 END), 0)
FROM inbound_events
WHERE completed_at >= ? AND completed_at < ?`+scope+`
GROUP BY COALESCE(TRIM(profile_id), ''), COALESCE(TRIM(group_id), '')`,
		append([]any{sinceNanos, untilNanos}, scopeArgs...)...)
	if err != nil {
		return fmt.Errorf("overview outcome buckets: %w", err)
	}
	defer doneRows.Close()
	for doneRows.Next() {
		var handled, failed int64
		var rowProfile, groupID string
		if err := doneRows.Scan(&rowProfile, &groupID, &handled, &failed); err != nil {
			return err
		}
		if grp := ensureGroup(rowProfile, "", groupID); grp != nil {
			grp.Handled += handled
			grp.Errors += failed
		}
	}
	return doneRows.Err()
}

func (s *SQLiteStore) fillOverviewPreviousEvents(
	ctx context.Context,
	since, until time.Time,
	profileID string,
	prev *DashboardPreviousSummary,
) error {
	sinceSec, untilSec := since.Unix(), until.Unix()
	sinceNanos, untilNanos := since.UnixNano(), until.UnixNano()
	scope, scopeArgs := dashboardProfileScope(profileID)

	if err := s.eventReader().QueryRowContext(ctx, `
SELECT COUNT(*) FROM inbound_events WHERE event_time >= ? AND event_time < ?`+scope,
		append([]any{sinceSec, untilSec}, scopeArgs...)...).Scan(&prev.Messages); err != nil {
		return fmt.Errorf("overview previous messages: %w", err)
	}
	var avg sql.NullFloat64
	if err := s.eventReader().QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN `+inboundHandledPredicate+` THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN `+inboundFailedPredicate+` THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN `+inboundHandledPredicate+` AND COALESCE(duration_ms, 0) > 0 THEN 1 ELSE 0 END), 0),
  AVG(CASE WHEN `+inboundHandledPredicate+` AND COALESCE(duration_ms, 0) > 0 THEN duration_ms END)
FROM inbound_events
WHERE completed_at >= ? AND completed_at < ?`+scope,
		append([]any{sinceNanos, untilNanos}, scopeArgs...)...).Scan(
		&prev.Handled,
		&prev.Errors,
		&prev.RepliesMeasured,
		&avg,
	); err != nil {
		return fmt.Errorf("overview previous outcomes: %w", err)
	}
	if avg.Valid {
		prev.AvgReplyMS = int64(avg.Float64)
	}
	return nil
}

func (s *SQLiteStore) fillOverviewLogsAndErrors(
	ctx context.Context,
	since, until time.Time,
	profileID string,
	categories map[string]*DashboardErrorCategory,
	recentErrors *[]DashboardErrorItem,
	errorLimit int,
	summary *DashboardWindowSummary,
) error {
	const seconds = "2006-01-02T15:04:05"
	query := `
SELECT id, kind, action, message, COALESCE(detail, ''), COALESCE(target, ''), COALESCE(metadata, ''), created_at
FROM app_logs
WHERE created_at >= ? AND created_at < ?
  AND (action = 'llm_call_failed' OR (kind = 'error' AND action != 'llm_usage'))`
	args := []any{since.UTC().Format(seconds), until.UTC().Add(time.Second).Format(seconds)}
	if profileID != "" {
		query += ` AND json_valid(metadata) AND trim(json_extract(metadata, '$.profile_id')) = ?`
		args = append(args, profileID)
	}
	query += ` ORDER BY created_at DESC, id DESC`

	rows, err := s.eventReader().QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("overview logs and errors: %w", err)
	}
	defer rows.Close()

	llmErrorSamples := 0
	logErrorSamples := 0
	for rows.Next() {
		var id, kind, action, message, detail, target, rawMeta, createdAt string
		if err := rows.Scan(&id, &kind, &action, &message, &detail, &target, &rawMeta, &createdAt); err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil || at.Before(since) || !at.Before(until) {
			continue
		}
		var meta map[string]any
		if strings.TrimSpace(rawMeta) != "" {
			_ = json.Unmarshal([]byte(rawMeta), &meta)
		}
		str := func(key string) string { v, _ := meta[key].(string); return strings.TrimSpace(v) }
		rowProfile := str("profile_id")
		if profileID != "" && rowProfile != profileID {
			continue
		}
		switch action {
		case "llm_call_failed":
			model := str("model")
			if model == "" {
				model = "unknown"
			}
			purpose := str("purpose")
			provider := str("provider")
			recovered, _ := meta["recovered"].(bool)
			catKey := "llm:" + model
			cat := categories[catKey]
			if cat == nil {
				cat = &DashboardErrorCategory{
					Source:  "llm",
					Key:     catKey,
					Label:   "模型调用失败 · " + model,
					Model:   model,
					Purpose: purpose,
					Action:  action,
				}
				categories[catKey] = cat
			}
			cat.Count++
			if recovered {
				cat.RecoveredCount++
			}
			if at.After(cat.LatestAt) {
				cat.LatestAt = at
			}
			if cat.Sample == "" {
				cat.Sample = firstNonEmptyOverview(detail, message)
			}
			if llmErrorSamples < errorLimit {
				llmErrorSamples++
				*recentErrors = append(*recentErrors, DashboardErrorItem{
					ID:        id,
					At:        at,
					Source:    "llm",
					Action:    action,
					Message:   message,
					Detail:    detail,
					Model:     model,
					Provider:  provider,
					Purpose:   purpose,
					ProfileID: rowProfile,
					GroupID:   appLogOverviewGroupID(meta),
					UserID:    str("user_id"),
					MessageID: firstNonEmptyOverview(str("message_id"), strings.TrimSpace(target)),
					Recovered: recovered,
				})
			}
		default:
			if kind != string(LogKindError) {
				continue
			}
			summary.AppErrorLogs++
			act := strings.TrimSpace(action)
			if act == "" {
				act = "system_error"
			}
			catKey := "log:" + act
			cat := categories[catKey]
			if cat == nil {
				cat = &DashboardErrorCategory{
					Source: "log",
					Key:    catKey,
					Label:  "运行错误 · " + act,
					Action: act,
				}
				categories[catKey] = cat
			}
			cat.Count++
			if at.After(cat.LatestAt) {
				cat.LatestAt = at
			}
			if cat.Sample == "" {
				cat.Sample = firstNonEmptyOverview(detail, message)
			}
			if logErrorSamples < errorLimit {
				logErrorSamples++
				*recentErrors = append(*recentErrors, DashboardErrorItem{
					ID:        id,
					At:        at,
					Source:    "log",
					Action:    act,
					Message:   message,
					Detail:    detail,
					ProfileID: rowProfile,
					GroupID:   appLogOverviewGroupID(meta),
					UserID:    str("user_id"),
					MessageID: str("message_id"),
				})
			}
		}
	}
	return rows.Err()
}

func (s *SQLiteStore) fillOverviewEventErrors(
	ctx context.Context,
	since, until time.Time,
	profileID string,
	categories map[string]*DashboardErrorCategory,
	recentErrors *[]DashboardErrorItem,
	errorLimit int,
	ensureGroup func(profile, platform, groupID string) *DashboardGroupStat,
) error {
	sinceNanos, untilNanos := since.UnixNano(), until.UnixNano()
	scope, scopeArgs := dashboardProfileScope(profileID)

	// 按 outcome 汇总事件处理失败分类
	catRows, err := s.eventReader().QueryContext(ctx, `
SELECT
  COALESCE(NULLIF(TRIM(outcome), ''), 'processing_error') AS outcome_key,
  COUNT(*),
  COALESCE(MAX(completed_at), 0),
  COALESCE(MAX(COALESCE(NULLIF(TRIM(processing_error), ''), NULLIF(TRIM(delivery_error), ''), NULLIF(TRIM(last_error), ''), '')), '')
FROM inbound_events
WHERE completed_at >= ? AND completed_at < ? AND `+inboundFailedPredicate+scope+`
GROUP BY outcome_key`,
		append([]any{sinceNanos, untilNanos}, scopeArgs...)...)
	if err != nil {
		return fmt.Errorf("overview event error categories: %w", err)
	}
	defer catRows.Close()
	for catRows.Next() {
		var outcomeKey, sample string
		var count, maxCompleted int64
		if err := catRows.Scan(&outcomeKey, &count, &maxCompleted, &sample); err != nil {
			return err
		}
		catKey := "event:" + outcomeKey
		categories[catKey] = &DashboardErrorCategory{
			Source:   "event",
			Key:      catKey,
			Label:    "消息处理异常 · " + describeOverviewEventOutcome(outcomeKey),
			Action:   outcomeKey,
			Count:    count,
			LatestAt: time.Unix(0, maxCompleted).UTC(),
			Sample:   strings.TrimSpace(sample),
		}
	}
	if err := catRows.Err(); err != nil {
		return err
	}

	// 取最近 errorLimit 条异常入站事件，供直接点击进入事件详情与调试链
	recentRows, err := s.eventReader().QueryContext(ctx, `
SELECT
  id, COALESCE(completed_at, 0), event_time,
  COALESCE(TRIM(profile_id), ''), COALESCE(TRIM(group_id), ''), COALESCE(TRIM(user_id), ''),
  COALESCE(TRIM(message_id), ''), COALESCE(TRIM(outcome), ''),
  COALESCE(NULLIF(TRIM(processing_error), ''), NULLIF(TRIM(delivery_error), ''), NULLIF(TRIM(last_error), ''), COALESCE(TRIM(decision_reason), '')),
  COALESCE(payload, '')
FROM inbound_events
WHERE completed_at >= ? AND completed_at < ? AND `+inboundFailedPredicate+scope+`
ORDER BY completed_at DESC, id DESC
LIMIT ?`,
		append(append([]any{sinceNanos, untilNanos}, scopeArgs...), errorLimit)...)
	if err != nil {
		return fmt.Errorf("overview recent event errors: %w", err)
	}
	defer recentRows.Close()
	for recentRows.Next() {
		var id, rowProfile, groupID, userID, messageID, outcome, errText, payload string
		var completedNanos, eventSec int64
		if err := recentRows.Scan(&id, &completedNanos, &eventSec, &rowProfile, &groupID, &userID, &messageID, &outcome, &errText, &payload); err != nil {
			return err
		}
		at := time.Unix(eventSec, 0).UTC()
		if completedNanos > 0 {
			at = time.Unix(0, completedNanos).UTC()
		}
		groupName := groupNameFromEventPayload(payload)
		if grp := ensureGroup(rowProfile, "", groupID); grp != nil && grp.GroupName == "" && groupName != "" {
			grp.GroupName = groupName
		}
		label := describeOverviewEventOutcome(outcome)
		msg := "消息" + label
		if errText != "" {
			msg = label + "：" + errText
		}
		*recentErrors = append(*recentErrors, DashboardErrorItem{
			ID:        id,
			At:        at,
			Source:    "event",
			Outcome:   outcome,
			Message:   msg,
			Detail:    errText,
			ProfileID: rowProfile,
			GroupID:   groupID,
			GroupName: groupName,
			UserID:    userID,
			MessageID: messageID,
		})
	}
	return recentRows.Err()
}

func (s *SQLiteStore) fillOverviewGroupNames(ctx context.Context, groupMap map[string]*DashboardGroupStat) {
	var missing []any
	for groupID, stat := range groupMap {
		if stat.GroupName == "" && strings.TrimSpace(groupID) != "" {
			missing = append(missing, groupID)
		}
	}
	if len(missing) == 0 {
		return
	}
	values := strings.TrimSuffix(strings.Repeat("(?),", len(missing)), ",")
	rows, err := s.eventReader().QueryContext(ctx, `
WITH targets(group_id) AS (VALUES `+values+`)
SELECT targets.group_id, COALESCE((
  SELECT i.payload FROM inbound_events AS i
  WHERE i.group_id = targets.group_id
  ORDER BY i.event_time DESC, i.id DESC
  LIMIT 1
), '')
FROM targets`, missing...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var groupID, payload string
		if err := rows.Scan(&groupID, &payload); err != nil {
			return
		}
		if stat := groupMap[groupID]; stat != nil && stat.GroupName == "" {
			stat.GroupName = groupNameFromEventPayload(payload)
		}
	}
}

func (s *SQLiteStore) fillOverviewPreviousLogs(
	ctx context.Context,
	since, until time.Time,
	profileID string,
	prev *DashboardPreviousSummary,
) error {
	const seconds = "2006-01-02T15:04:05"
	query := `
SELECT kind, action, COALESCE(metadata, ''), created_at
FROM app_logs
WHERE created_at >= ? AND created_at < ?
  AND (action IN ('llm_usage', 'llm_call_failed') OR kind = 'error')`
	args := []any{since.UTC().Format(seconds), until.UTC().Add(time.Second).Format(seconds)}
	if profileID != "" {
		query += ` AND json_valid(metadata) AND trim(json_extract(metadata, '$.profile_id')) = ?`
		args = append(args, profileID)
	}
	rows, err := s.eventReader().QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("overview previous logs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind, action, rawMeta, createdAt string
		if err := rows.Scan(&kind, &action, &rawMeta, &createdAt); err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil || at.Before(since) || !at.Before(until) {
			continue
		}
		var meta map[string]any
		if strings.TrimSpace(rawMeta) != "" {
			_ = json.Unmarshal([]byte(rawMeta), &meta)
		}
		if profileID != "" {
			owner, _ := meta["profile_id"].(string)
			if strings.TrimSpace(owner) != profileID {
				continue
			}
		}
		switch action {
		case "llm_usage":
			prev.RecordedCalls++
			input, output := int64FromAny(meta["input_tokens"]), int64FromAny(meta["output_tokens"])
			total := int64FromAny(meta["total_tokens"])
			if total <= 0 {
				total = input + output
			}
			prev.TotalTokens += total
		case "llm_call_failed":
			prev.FailedCalls++
		default:
			if kind == string(LogKindError) {
				prev.AppErrorLogs++
			}
		}
	}
	return rows.Err()
}

func appLogOverviewGroupID(meta map[string]any) string {
	switch value := meta["group_id"].(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		if value > 0 {
			return fmt.Sprintf("%.0f", value)
		}
	}
	return ""
}

func firstNonEmptyOverview(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func describeOverviewEventOutcome(outcome string) string {
	switch strings.TrimSpace(outcome) {
	case "error_replied":
		return "处理报错并回执"
	case "error_send_unconfirmed":
		return "错误回执未确认"
	case "processing_error":
		return "消息处理失败"
	case "dropped_outbound_delivery", "dropped_outbound_unconfirmed":
		return "回复投递失败"
	case "dropped_send_rejected":
		return "上游拒绝发送"
	case "dropped_retries_exhausted":
		return "重试耗尽丢弃"
	default:
		if strings.TrimSpace(outcome) != "" {
			return strings.TrimSpace(outcome)
		}
		return "处理异常"
	}
}
