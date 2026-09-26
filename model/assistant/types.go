// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SuInk/diana/internal/secretmask"
	"github.com/SuInk/diana/model/agent"

	"github.com/google/uuid"
)

type EventKind string

const (
	EventKindPrivate EventKind = "private"
	EventKindGroup   EventKind = "group"
	EventKindNotice  EventKind = "notice"
	EventKindRequest EventKind = "request"
	EventKindMeta    EventKind = "meta"
)

type RecallReplyMode string

const (
	RecallReplyModeLLMSummary      RecallReplyMode = "llm_summary"
	RecallReplyModeOriginalForward RecallReplyMode = "original_forward"

	defaultRecallReplyTTLSeconds = 60
	maximumRecallReplyTTLSeconds = 60 * 60
)

// RefusalStrategy 决定机器人决定不正面回答时怎么说。
//
// 这一档单独拿出来配，是因为「说明为什么不能答」本身可能就是那句会出事的话：
// 群里一句「这个话题涉及敏感政治，我不方便讲」把触发点原样复述了一遍，风险比
// 闭嘴还大。不同部署对这件事的容忍度差很多，不该由一份提示词替所有人定死。
type RefusalStrategy string

const (
	// RefusalStrategySmart 让模型按四档阶梯自己判断：能改写就改写，改不动看
	// 原因性质决定说不说。默认值——大多数时候它比一刀切的档位说得更自然。
	RefusalStrategySmart RefusalStrategy = "smart"
	// RefusalStrategyRewrite 要求尽量绕开，只有实在无从下手时才拒绝。
	RefusalStrategyRewrite RefusalStrategy = "rewrite"
	// RefusalStrategyExplain 是旧行为：拒绝时把原因说清楚。
	RefusalStrategyExplain RefusalStrategy = "explain"
	// RefusalStrategyVague 一律模糊带过，任何情况下都不交代原因。
	RefusalStrategyVague RefusalStrategy = "vague"
)

func normalizeRefusalStrategy(strategy RefusalStrategy) RefusalStrategy {
	switch strategy {
	case RefusalStrategySmart, RefusalStrategyRewrite, RefusalStrategyExplain, RefusalStrategyVague:
		return strategy
	default:
		return RefusalStrategySmart
	}
}

func normalizeRecallReplyMode(mode RecallReplyMode) RecallReplyMode {
	switch mode {
	case RecallReplyModeLLMSummary, RecallReplyModeOriginalForward:
		return mode
	default:
		return RecallReplyModeOriginalForward
	}
}

// WelcomeMode 决定入群欢迎词的生成方式（关闭 #575）。
type WelcomeMode string

const (
	// WelcomeModeFixed 固定文本，替换 {user_id} 后直接发送（旧行为）。
	WelcomeModeFixed WelcomeMode = "fixed"
	// WelcomeModeTemplate 从口吻模板池随机抽一条，兼顾生动与零 Token 成本。
	WelcomeModeTemplate WelcomeMode = "template"
	// WelcomeModeLLM 按机器人设定与人设调用轻量模型实时生成一句问候，
	// 受每群冷却间隔约束，失败或限流时回落到模板池/固定文本。
	WelcomeModeLLM WelcomeMode = "llm"
)

func normalizeWelcomeMode(mode WelcomeMode) WelcomeMode {
	switch mode {
	case WelcomeModeFixed, WelcomeModeTemplate, WelcomeModeLLM:
		return mode
	default:
		return WelcomeModeFixed
	}
}

// defaultWelcomeLLMCooldownSeconds 是 LLM 欢迎词的默认每群冷却：进群/退群刷屏时
// 不会每条都烧一次 Token。
const defaultWelcomeLLMCooldownSeconds = 300

// welcomeLLMMaxChars 限制 LLM 生成的欢迎词长度，模型失控时截断兜底。
const welcomeLLMMaxChars = 200

type MessageSegment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data,omitempty"`
}

// ImageDescriptionRecord stores reusable visual facts by image content rather
// than by platform message ID, so re-sent copies can share one description.
type ImageDescriptionRecord struct {
	ContentSHA256   string `json:"content_sha256"`
	Description     string `json:"description"`
	SourceSession   string `json:"source_session,omitempty"`
	SourceMessageID string `json:"source_message_id,omitempty"`
	Source          string `json:"source,omitempty"`
	Version         string `json:"version,omitempty"`
	CreatedAt       int64  `json:"created_at,omitempty"`
	UpdatedAt       int64  `json:"updated_at,omitempty"`
}

type MessageEvent struct {
	// RetryRecovered marks an event restored from the local durable retry journal.
	RetryRecovered bool `json:"retry_recovered,omitempty"`
	// BackfillHistoryOnly 标记断线回补拉回来、但没排上回复名额的消息：只补进上下文
	// 历史，不跑语音转写、图片处理、插件、中继和回复。它要跟着事件一起落进入站队列，
	// 所以是导出字段。
	BackfillHistoryOnly bool `json:"backfill_history_only,omitempty"`
	// ManualRetry 标记主人在运行记录里手动重试的失败消息：不受「太旧不回复」限制。
	ManualRetry bool `json:"manual_retry,omitempty"`
	// SenderUsername is the platform-authenticated sender handle, not a display
	// name or a handle found in message text, mentions or forwarded content.
	SenderUsername   string           `json:"sender_username,omitempty"`
	MentionTargets   []MessageMention `json:"mention_targets,omitempty"`
	UserIDType       string           `json:"user_id_type,omitempty"`
	PlatformScope    string           `json:"platform_scope,omitempty"`
	GuildID          string           `json:"guild_id,omitempty"`
	Platform         string           `json:"platform,omitempty"`
	ProfileID        string           `json:"profile_id,omitempty"`
	ContextNamespace string           `json:"context_namespace,omitempty"`
	Kind             EventKind        `json:"kind"`
	SubType          string           `json:"sub_type,omitempty"`
	Time             int64            `json:"time,omitempty"`
	OriginalTime     int64            `json:"original_time,omitempty"`
	SelfID           string           `json:"self_id,omitempty"`
	// SelfUsername 是本机器人在平台上的用户名（Telegram 的 @xxx）。消息文本里
	// 出现的是它而不是数字 ID，路由判断得靠它才认得出「这是在叫我」。
	SelfUsername string `json:"self_username,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	// TargetID 目前只有 poke 通知在用：被戳的是谁。
	TargetID     string `json:"target_id,omitempty"`
	OperatorID   string `json:"operator_id,omitempty"`
	OperatorName string `json:"operator_name,omitempty"`
	OperatorRole string `json:"operator_role,omitempty"`
	GroupID      string `json:"group_id,omitempty"`
	// GroupName 是收到这条消息时平台给出的群名称。OneBot 可以随时用
	// get_group_list 问到群名，Telegram、钉钉这些没有「列出我加入的群」的平台
	// 只能靠消息自带的标题；控制台的群管理页要靠它显示名字而不是一串 ID。
	GroupName        string           `json:"group_name,omitempty"`
	MessageThreadID  string           `json:"message_thread_id,omitempty"`
	MessageID        string           `json:"message_id,omitempty"`
	MessageSeq       string           `json:"message_seq,omitempty"`
	MessageType      string           `json:"message_type,omitempty"`
	RawMessage       string           `json:"raw_message,omitempty"`
	Segments         []MessageSegment `json:"segments,omitempty"`
	SenderName       string           `json:"sender_name,omitempty"`
	SenderIsBot      bool             `json:"sender_is_bot,omitempty"`
	SenderRole       string           `json:"sender_role,omitempty"`
	SenderLevel      int              `json:"sender_level,omitempty"`
	SenderLevelLabel string           `json:"sender_level_label,omitempty"`
	SenderTitle      string           `json:"sender_title,omitempty"`
	Outbound         bool             `json:"outbound,omitempty"`
	ToMe             bool             `json:"to_me,omitempty"`
	Quoted           *QuotedMessage   `json:"quoted,omitempty"`
	ExternalEvent    *ExternalEvent   `json:"external_event,omitempty"`
	// PushKind 非空表示这条出站消息是订阅推送（仓库动态、RSS 这类事实卡片），
	// 是系统自动发的，不是机器人在聊天里说的话。见 subscription_push_history.go。
	PushKind string `json:"push_kind,omitempty"`
	// SemanticSourceMessageID keeps the first selected historical source for
	// compatibility with persisted events created before multi-source routing.
	SemanticSourceMessageID string `json:"semantic_source_message_id,omitempty"`
	// SemanticSourceMessageIDs preserves every historical source selected for a
	// cross-message reference, in the order the model should consume them.
	SemanticSourceMessageIDs []string `json:"semantic_source_message_ids,omitempty"`
	// botReply is an in-memory compatibility marker for assistant history entries.
	// Persisted outgoing events still use the regular message fields above.
	botReply      string
	routingReason string
	// mutedJudgeOnly 非空表示机器人在本群被禁言、但配置了照常做回复判断：判断
	// 跑完后不生成、不发送，内容是禁言说明。mutedSkipImages 表示这期间不识图。
	mutedJudgeOnly  string
	mutedSkipImages bool
	// selfSent 表示这是接入端用 post_type=message_sent 推回来的机器人自己发出的
	// 消息（NapCat、SnowLuma 都这么推），见 HandleEvent。
	selfSent bool
	// tempSessionGroupID 只在「给非好友发私聊」时有值：QQ 的临时会话要靠共同群
	// 才发得出去。它是一次投递的路由提示，不是会话身份的一部分——写成导出字段
	// 就会跟着事件落库，让这条私聊在历史里看起来像发生在那个群里。
	tempSessionGroupID string
	// backlogProbe 是这条消息所在的队列项，用来判断它是不是积压了、该交给同会话后面的消息
	// 一起接话。不走队列的消息没有它。
	backlogProbe *InboundQueueItem
	// backlogTurn 是积压合并后和这条一起作答的其他消息；backlogProactive 是积压包里
	// 还要交给主动回复路由一起判断的候选。
	backlogTurn      []proactiveReplyCandidate
	backlogProactive []proactiveReplyCandidate
	// backlogHeld 表示这条消息是从积压包里取出来当回复对象的，进包时已经记过长期记忆和用户画像。
	backlogHeld    bool
	proactiveReply bool
	// chatInReply 表示本次主动回复来自闲聊插话路径，回复阶段据此收敛语气和长度。
	chatInReply bool
	// routingDirected 记下接话评分里的 relevance.directed：这条消息在语义上是冲着
	// 机器人来的，哪怕正文里没有 @、引用和名字。空转判断靠它才看得见相关度分支放
	// 行的那些回复，见 botReplyLoopCandidate。
	routingDirected bool
	// carryOver 是这一轮要点名承接的、同一个人更早连发还没回的消息，由回复构建时
	// 的 claimCarryOver 决定（见 sender_burst.go）；carryOverSet 区分「算过、为空」
	// 和「没算过」。
	carryOver              []MessageEvent
	carryOverSet           bool
	replyDeliveryMode      replyDeliveryMode
	replyLineBreakMode     replyLineBreakMode
	replyAuditImageContext string
	avatarMatchContext     string
	imageResolutionRun     bool
	imageLoadErr           error
	// taskRequester 是替别人建提醒、订阅时真正发起的人，只在建任务那一步用，
	// 落进 Reminder.RequestedBy。
	taskRequester          string
	imageContextNotice     string
	voiceSTTErr            error
	voiceSTTTransient      bool
	recentTextReference    *recentTextReference
	replyHistory           []MessageEvent
	replyHistoryLoaded     bool
	crossGroupContext      bool
	historyRecallCandidate bool
	userProfile            UserMemoryProfile
	userProfileLoaded      bool
	oneBotRequest          *OneBotRequestEvent
}

// ExternalEvent is trusted host-generated context. It is persisted in the
// target conversation, but must never be interpreted as a user instruction.
type ExternalEvent struct {
	Source  string          `json:"source"`
	Trust   string          `json:"trust"`
	Intent  string          `json:"intent,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

type QuotedMessage struct {
	MessageID                string           `json:"message_id,omitempty"`
	UserID                   string           `json:"user_id,omitempty"`
	GroupID                  string           `json:"group_id,omitempty"`
	SenderName               string           `json:"sender_name,omitempty"`
	RawMessage               string           `json:"raw_message,omitempty"`
	Segments                 []MessageSegment `json:"segments,omitempty"`
	Semantic                 bool             `json:"semantic,omitempty"`
	SemanticSourceMessageID  string           `json:"semantic_source_message_id,omitempty"`
	SemanticSourceMessageIDs []string         `json:"semantic_source_message_ids,omitempty"`
}

type OutgoingMessage struct {
	PlatformScope   string
	GuildID         string
	Platform        string
	ProfileID       string
	GroupID         string
	MessageThreadID string
	UserID          string
	Text            string
	Segments        []MessageSegment
	ImageURLs       []string
	// ImageLabels 与 ImageURLs 一一对应，给控制台的事件记录标出每张图是什么
	// （比如哪个表情包）；不发给平台。
	ImageLabels          []string
	ImageAlbum           bool
	GeneratedImageModels []GeneratedImageModel
	VideoURLs            []string
	AudioURLs            []string
	ImagesFirst          bool
	ReplyMessageID       string
	MentionUserID        string
	// MentionNames 是正文里 [diana-at:ID] 标记要显示的昵称，按 id 索引。
	// Telegram 的 text_mention 需要一段可见文字，光有 id 显示不出来；查不到
	// 的 id 退回显示 @<id>。OneBot 不需要它——那边 at 段自己会渲染。
	MentionNames map[string]string
	ForwardName  string
	ForwardUIN   string
	ForwardTime  int64
	// TempSessionGroupID 让私聊走 QQ 的临时会话：OneBot 的 send_private_msg 带上
	// group_id 才能发给不是好友、但同在这个群里的人。只在确认不是好友时才填——
	// 对好友也走临时会话会把消息塞进另一个对话框。
	TempSessionGroupID string
}

type ReminderKind string

const (
	ReminderKindMessage         ReminderKind = "message"
	ReminderKindQuery           ReminderKind = "query"
	ReminderKindRepositoryWatch ReminderKind = "repository_watch"
	ReminderKindRSSWatch        ReminderKind = "rss_watch"
	// ReminderKindEventTrigger 不按时间触发，由入站事件点燃，见 event_trigger.go。
	// 它的 TriggerAt 恒为零，定时轮询必须跳过它。
	ReminderKindEventTrigger ReminderKind = "event_trigger"
)

type Reminder struct {
	ID               string       `json:"id"`
	Kind             ReminderKind `json:"kind,omitempty"`
	Platform         string       `json:"platform,omitempty"`
	ProfileID        string       `json:"profile_id,omitempty"`
	ContextNamespace string       `json:"context_namespace,omitempty"`
	OwnerID          string       `json:"owner_id"`
	GroupID          string       `json:"group_id,omitempty"`
	UserID           string       `json:"user_id,omitempty"`
	// RequestedBy 是在对话里让机器人建这条任务的人。主人在私聊里用 target_user_id 替
	// 别人建的提醒和订阅，投递给的是别人（UserID），发起的是主人；安全模式据此在到点时
	// 停发「往别的会话发」的任务。空值是这个字段之前的旧记录，按 UserID 本人算。
	RequestedBy string `json:"requested_by,omitempty"`
	// SafeModeHeldTriggerAt 非零表示这条一次性提醒到点时被安全模式停发过，值是它当时的
	// 原定时间。切回标准模式补发时按它注明原定时间、判断要不要过期作废；投递失败重试
	// 改了 TriggerAt 也不影响它。只有真的被停发过的提醒才有这个标记。
	SafeModeHeldTriggerAt   time.Time `json:"safe_mode_held_trigger_at,omitempty"`
	NotificationEnabled     bool      `json:"notification_enabled,omitempty"`
	NotificationTargetsJSON string    `json:"notification_targets,omitempty"`
	Message                 string    `json:"message"`
	TriggerAt               time.Time `json:"trigger_at"`
	IntervalSeconds         int64     `json:"interval_seconds,omitempty"`
	// IntervalMonths 非零表示按日历月重复（每月、每年），下一次按月份加，不按秒数；
	// 这时 IntervalSeconds 只是折算值，给「是不是周期任务」的判断和显示用。
	IntervalMonths int `json:"interval_months,omitempty"`
	// ScheduleWeekdays / ScheduleMonthDays / ScheduleWeekday+ScheduleWeekOrdinal 是
	// 周期任务的日期规则（每周一三五、每月 1 号和 15 号、每月第一个周一），三选一，
	// 见 scheduleDayRule。全为零值表示按起点排。
	ScheduleWeekdays    []string `json:"schedule_weekdays,omitempty"`
	ScheduleMonthDays   []int    `json:"schedule_month_days,omitempty"`
	ScheduleWeekday     string   `json:"schedule_weekday,omitempty"`
	ScheduleWeekOrdinal int      `json:"schedule_week_ordinal,omitempty"`
	// ScheduleAnchorAt 是周期任务的时间网格原点：每次成功后下一次落在 anchor + k*interval
	// 上，不跟着实际开跑时间或失败重试漂。「每周日 22:00」靠它一直停在 22:00。
	// 零值是这个字段之前的旧记录，仍按实际开跑时间往后排。
	ScheduleAnchorAt        time.Time `json:"schedule_anchor_at,omitempty"`
	LastRunAt               time.Time `json:"last_run_at,omitempty"`
	CancelledAt             time.Time `json:"cancelled_at,omitempty"`
	LastError               string    `json:"last_error,omitempty"`
	ConsecutiveFailures     int       `json:"consecutive_failures,omitempty"`
	LastFailureStage        string    `json:"last_failure_stage,omitempty"`
	LastErrorFingerprint    string    `json:"last_error_fingerprint,omitempty"`
	FailureAlertedAt        time.Time `json:"failure_alerted_at,omitempty"`
	RecoveryNoticePending   bool      `json:"recovery_notice_pending,omitempty"`
	PendingDelivery         string    `json:"pending_delivery,omitempty"`
	PendingDeliveredTargets []string  `json:"pending_delivered_targets,omitempty"`
	// PendingDeliveryReference 是仓库通知补投成功后生成跟评所需的私有参考资料。
	// 它不发送到会话，只避免投递失败后丢失仓库简介、正文和 diff。
	PendingDeliveryReference string    `json:"pending_delivery_reference,omitempty"`
	PendingSince             time.Time `json:"pending_since,omitempty"`
	Repository               string    `json:"repository,omitempty"`
	RepositoryBranch         string    `json:"repository_branch,omitempty"`
	WatchCommits             bool      `json:"watch_commits,omitempty"`
	WatchPullRequests        bool      `json:"watch_pull_requests,omitempty"`
	// WatchPullRequestEvents / WatchIssueEvents：nil 是未配置的旧记录，按全选兼容；
	// 非 nil 空数组表示明确全不选，因此 JSON 不能使用 omitempty。
	WatchPullRequestEvents []string `json:"watch_pull_request_events"`
	WatchIssueEvents       []string `json:"watch_issue_events"`
	WatchIssues            bool     `json:"watch_issues,omitempty"`
	WatchReleases          bool     `json:"watch_releases,omitempty"`
	// WatchReleaseKinds 同理：nil 是未配置的旧记录，按全选兼容，所以不能 omitempty。
	WatchReleaseKinds      []string  `json:"watch_release_kinds"`
	WatchStars             bool      `json:"watch_stars,omitempty"`
	StarNotifyMode         string    `json:"star_notify_mode,omitempty"`
	StarNotifyThreshold    int       `json:"star_notify_threshold,omitempty"`
	StarNotifyMilestones   []int     `json:"star_notify_milestones,omitempty"`
	LastCommitSHA          string    `json:"last_commit_sha,omitempty"`
	LastPullRequestCursor  string    `json:"last_pull_request_cursor,omitempty"`
	LastIssueCursor        string    `json:"last_issue_cursor,omitempty"`
	LastReleaseTag         string    `json:"last_release_tag,omitempty"`
	LastReleasePublishedAt time.Time `json:"last_release_published_at,omitempty"`
	LastReleaseID          int64     `json:"last_release_id,omitempty"`
	LastStarCount          int       `json:"last_star_count,omitempty"`
	LastNotifiedStarCount  int       `json:"last_notified_star_count,omitempty"`
	LastStarEventID        string    `json:"last_star_event_id,omitempty"`
	LastStarEventAt        time.Time `json:"last_star_event_at,omitempty"`
	// LastRepositoryCheckAt 是上一次仓库检查成功的开始时间。检查失败不更新，
	// 所以不能用 LastRunAt 代替：失败的那一轮之前更新的记录仍要在下次成功时算新动态。
	LastRepositoryCheckAt time.Time `json:"last_repository_check_at,omitempty"`
	// WatchAnchorsJSON 记录每个投递目标里各 PR/Issue 首次宣布消息的 ID,
	// 后续同一编号的更新推送引用它,把动态串成一条线。
	WatchAnchorsJSON    string    `json:"watch_anchors,omitempty"`
	FeedURL             string    `json:"feed_url,omitempty"`
	FeedSource          string    `json:"feed_source,omitempty"`
	FeedHandle          string    `json:"feed_handle,omitempty"`
	FeedJudgePrompt     string    `json:"feed_judge_prompt,omitempty"`
	LastFeedItemID      string    `json:"last_feed_item_id,omitempty"`
	LastFeedPublishedAt time.Time `json:"last_feed_published_at,omitempty"`
	// FeedSourcesJSON 保存一条订阅盯着的全部来源：同一套判断规则可以一次管好几个
	// Twitter 账号或几个 Feed。上面的单来源字段跟着第一个来源走，老记录和只认单
	// 来源的读取方仍然读得到东西。
	FeedSourcesJSON string `json:"feed_sources,omitempty"`
	// EventTriggerJSON 是事件触发任务的条件和动作（EventTrigger），只对
	// ReminderKindEventTrigger 有意义。
	EventTriggerJSON string    `json:"event_trigger,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// ReminderDeliveryTarget is an additional destination for recurring watch
// notifications. The legacy GroupID/UserID fields remain the primary target
// for old persisted reminders and are used as a fallback when this list is empty.
type ReminderDeliveryTarget struct {
	Platform         string `json:"platform,omitempty"`
	ProfileID        string `json:"profile_id,omitempty"`
	ContextNamespace string `json:"context_namespace,omitempty"`
	GroupID          string `json:"group_id,omitempty"`
	UserID           string `json:"user_id,omitempty"`
}

func encodeReminderDeliveryTargets(targets []ReminderDeliveryTarget) string {
	if len(targets) == 0 {
		return ""
	}
	body, _ := json.Marshal(targets)
	return string(body)
}

func decodeReminderDeliveryTargets(raw string) []ReminderDeliveryTarget {
	var targets []ReminderDeliveryTarget
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &targets) != nil {
		return nil
	}
	return targets
}

func ReminderDeliveryTargets(raw string) []ReminderDeliveryTarget {
	return decodeReminderDeliveryTargets(raw)
}

// ReminderFeedSource 是 RSS 订阅里的一个来源。多来源订阅共用一套判断规则，游标
// 却必须一人一个：合用一个游标的话，更新快的来源会把慢的顶过去，慢的那条新内容
// 永远等不到判断。
type ReminderFeedSource struct {
	FeedURL         string    `json:"feed_url"`
	Source          string    `json:"source,omitempty"`
	Handle          string    `json:"handle,omitempty"`
	Name            string    `json:"name,omitempty"`
	LastItemID      string    `json:"last_item_id,omitempty"`
	LastPublishedAt time.Time `json:"last_published_at,omitempty"`
}

func encodeReminderFeedSources(sources []ReminderFeedSource) string {
	if len(sources) == 0 {
		return ""
	}
	body, _ := json.Marshal(sources)
	return string(body)
}

func decodeReminderFeedSources(raw string) []ReminderFeedSource {
	var sources []ReminderFeedSource
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &sources) != nil {
		return nil
	}
	return sources
}

// ReminderFeedSources 读出订阅的全部来源。多来源之前存的记录只有单来源字段，
// 按它回落成一条，升级后不用迁移数据也能继续跑。
//
// 读出来的地址顺手登记给 secretmask：重启之后还没抓过一轮时，订阅的 LastError、
// 列表里的地址也要认得出里面的令牌。
func ReminderFeedSources(item Reminder) []ReminderFeedSource {
	if sources := decodeReminderFeedSources(item.FeedSourcesJSON); len(sources) > 0 {
		for _, source := range sources {
			secretmask.RegisterURL(source.FeedURL)
		}
		return sources
	}
	if strings.TrimSpace(item.FeedURL) == "" {
		return nil
	}
	secretmask.RegisterURL(item.FeedURL)
	return []ReminderFeedSource{{
		FeedURL: item.FeedURL, Source: item.FeedSource, Handle: item.FeedHandle,
		LastItemID: item.LastFeedItemID, LastPublishedAt: item.LastFeedPublishedAt,
	}}
}

type Channel interface {
	Connect(ctx context.Context, handler EventHandler) error
	Send(ctx context.Context, msg OutgoingMessage) error
	CallAPI(ctx context.Context, action string, params map[string]any) (map[string]any, error)
	Status() ChannelStatus
	Close() error
}

// ResultChannel exposes the OneBot response for sent messages. The runtime uses
// its message_id for delayed self-recall without changing the base Channel API.
type ResultChannel interface {
	SendWithResult(ctx context.Context, msg OutgoingMessage) (map[string]any, error)
}

// TextDraftChannel exposes a platform-native draft message used while an LLM
// response is still being generated. Drafts are previews only; the ordinary
// Send path remains responsible for the final, audited reply.
type TextDraftChannel interface {
	SendTextDraft(ctx context.Context, msg OutgoingMessage, draftID int64) error
}

// ChatActionChannel exposes short-lived platform status such as Telegram's
// "typing" indicator while a reply is being prepared.
type ChatActionChannel interface {
	SendChatAction(ctx context.Context, msg OutgoingMessage, action string) error
}

type ChannelStatus struct {
	ProfileID            string `json:"profile_id,omitempty"`
	Platform             string `json:"platform,omitempty"`
	Name                 string `json:"name,omitempty"`
	Connected            bool   `json:"connected"`
	AccountStatusKnown   bool   `json:"account_status_known,omitempty"`
	AccountOnline        bool   `json:"account_online"`
	AccountGood          bool   `json:"account_good"`
	AccountStatusMessage string `json:"account_status_message,omitempty"`
	Endpoint             string `json:"endpoint"`
	// AccessTokenConfigured 反映的是「运行中的监听器」手上有没有 token，
	// 用来和存储里的配置对照:两边不一致就说明运行态没跟上保存的配置。
	AccessTokenConfigured   bool       `json:"access_token_configured,omitempty"`
	SelfID                  string     `json:"self_id,omitempty"`
	LastError               string     `json:"last_error,omitempty"`
	ConnectionEpoch         uint64     `json:"connection_epoch,omitempty"`
	ConnectionOwner         string     `json:"connection_owner,omitempty"`
	DuplicateConnections    uint64     `json:"duplicate_connections,omitempty"`
	UnauthorizedConnections uint64     `json:"unauthorized_connections,omitempty"`
	LastRejectedClient      string     `json:"last_rejected_client,omitempty"`
	LastConnectionEvent     string     `json:"last_connection_event,omitempty"`
	LastConnectionEventTime *time.Time `json:"last_connection_event_time,omitempty"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

type EventHandler func(context.Context, MessageEvent) error

type BotConfig struct {
	ConnectionProfileID         string           `json:"connection_profile_id,omitempty"`
	ReplyMergeConfidencePercent int              `json:"reply_merge_confidence_percent,omitempty"`
	ID                          string           `json:"id,omitempty"`
	Name                        string           `json:"name,omitempty"`
	Platform                    string           `json:"platform,omitempty"`
	AvatarURL                   string           `json:"avatar_url,omitempty"`
	Enabled                     bool             `json:"enabled"`
	OneBotTransport             string           `json:"onebot_transport,omitempty"`
	OneBotWSEndpoint            string           `json:"onebot_ws_endpoint,omitempty"`
	OneBotHTTPURL               string           `json:"onebot_http_url,omitempty"`
	OneBotHTTPSecret            string           `json:"onebot_http_secret,omitempty"`
	OneBotReverseWSEndpoint     string           `json:"onebot_reverse_ws_endpoint"`
	OneBotAccessToken           string           `json:"onebot_access_token,omitempty"`
	TelegramBotToken            string           `json:"telegram_bot_token,omitempty"`
	TelegramAPIBaseURL          string           `json:"telegram_api_base_url,omitempty"`
	TelegramProxyURL            string           `json:"telegram_proxy_url,omitempty"`
	TelegramSuppressBotMessages *bool            `json:"telegram_suppress_bot_messages,omitempty"`
	QQTypingEnabled             *bool            `json:"qq_typing_enabled,omitempty"`
	QQAppID                     string           `json:"qq_app_id,omitempty"`
	QQAppSecret                 string           `json:"qq_app_secret,omitempty"`
	QQSandbox                   bool             `json:"qq_sandbox,omitempty"`
	DingTalkClientID            string           `json:"dingtalk_client_id,omitempty"`
	DingTalkClientSecret        string           `json:"dingtalk_client_secret,omitempty"`
	DingTalkRobotCode           string           `json:"dingtalk_robot_code,omitempty"`
	FeishuAppID                 string           `json:"feishu_app_id,omitempty"`
	FeishuAppSecret             string           `json:"feishu_app_secret,omitempty"`
	FeishuVerificationToken     string           `json:"feishu_verification_token,omitempty"`
	FeishuEncryptKey            string           `json:"feishu_encrypt_key,omitempty"`
	FeishuAPIBaseURL            string           `json:"feishu_api_base_url,omitempty"`
	WeComCorpID                 string           `json:"wecom_corp_id,omitempty"`
	WeComAgentID                string           `json:"wecom_agent_id,omitempty"`
	WeComSecret                 string           `json:"wecom_secret,omitempty"`
	WeComToken                  string           `json:"wecom_token,omitempty"`
	WeComEncodingAESKey         string           `json:"wecom_encoding_aes_key,omitempty"`
	NoneBotBridgeEnabled        bool             `json:"nonebot_bridge_enabled,omitempty"`
	NoneBotBridgeEndpoint       string           `json:"nonebot_bridge_endpoint,omitempty"`
	NoneBotBridgeToken          string           `json:"nonebot_bridge_token,omitempty"`
	BotAccount                  string           `json:"bot_account,omitempty"`
	OwnerID                     string           `json:"owner_id,omitempty"`
	OwnerLoginEnabled           bool             `json:"owner_login_enabled,omitempty"`
	OwnerLLMConfigEnabled       *bool            `json:"owner_llm_config_enabled,omitempty"`
	GroupTriggers               []string         `json:"group_triggers,omitempty"`
	GroupTriggerMode            AliasTriggerMode `json:"group_trigger_mode,omitempty"`
	DisabledGroups              []string         `json:"disabled_groups,omitempty"`
	// DisabledUsers 已废弃，只为读取旧配置保留：WithDefaults 会把它并进 ReplyGate.BlockedUsers。
	DisabledUsers             []string         `json:"disabled_users,omitempty"`
	MarkedBotIDs              []string         `json:"marked_bot_ids,omitempty"`
	GroupAdmission            GroupAdmission   `json:"group_admission,omitempty"`
	PrivateAdmission          PrivateAdmission `json:"private_admission,omitempty"`
	ReplyGate                 *ReplyGate       `json:"reply_gate,omitempty"`
	WelcomeEnabled            bool             `json:"welcome_enabled,omitempty"`
	WelcomeMessage            string           `json:"welcome_message,omitempty"`
	WelcomeMode               WelcomeMode      `json:"welcome_mode,omitempty"`
	WelcomeTemplates          []string         `json:"welcome_templates,omitempty"`
	WelcomeLLMCooldownSeconds int              `json:"welcome_llm_cooldown_seconds,omitempty"`
	// SystemPrompt 是这台机器人的 SOUL.md：她是谁、在乎什么、怎么说话。整份原样
	// 放在系统提示词最前面。JSON 键名沿用 system_prompt，存量配置不用迁移。
	SystemPrompt string       `json:"system_prompt,omitempty"`
	ResponseMode ResponseMode `json:"response_mode,omitempty"`
	ReplyStyle   ReplyStyle   `json:"reply_style,omitempty"`
	// 下面几项是 SOUL.md 之前的人设字段，只在读旧配置时出现：WithDefaults 把它们
	// 并进 SystemPrompt 后清空，见 persona_legacy.go。
	Soul                     *PersonaSoul `json:"soul,omitempty"`
	PersonaMode              PersonaMode  `json:"persona_mode,omitempty"`
	ActionDescriptionEnabled *bool        `json:"action_description_enabled,omitempty"`
	SelfReference            string       `json:"self_reference,omitempty"`
	SentenceEnders           string       `json:"sentence_enders,omitempty"`
	// DebugModeEnabled 为 nil 表示没设置过，按开启处理，见 debugModeEnabled。
	DebugModeEnabled     *bool                `json:"debug_mode_enabled,omitempty"`
	ReplyReferenceMode   ReplyDecorationMode  `json:"reply_reference_mode,omitempty"`
	ModelDisclosure      ModelDisclosure      `json:"model_disclosure,omitempty"`
	RepositoryDisclosure RepositoryDisclosure `json:"repository_disclosure,omitempty"`
	MentionUserMode      ReplyDecorationMode  `json:"mention_user_mode,omitempty"`
	MarkdownToPlain      *bool                `json:"markdown_to_plain,omitempty"`
	ErrorNotifyEnabled   *bool                `json:"error_notify_enabled,omitempty"`
	// ErrorPersonaReplyEnabled 只在错误提示关闭时起作用：回复失败后仍让模型按人设
	// 回一句，不把错误原文发进聊天。默认关：已关掉错误提示的机器人升级后保持静默。
	ErrorPersonaReplyEnabled *bool  `json:"error_persona_reply_enabled,omitempty"`
	ErrorReplyPrefix         string `json:"error_reply_prefix,omitempty"`
	// MutedReplyPauseEnabled 为空或 true 时，机器人在群里被禁言期间只记上下文、
	// 不做回复判断和生成（见 bot_mute.go）。
	MutedReplyPauseEnabled *bool `json:"muted_reply_pause_enabled,omitempty"`
	// 暂停回复期间哪些环节照常执行（见 bot_mute.go）：语音转文字、图片识别默认
	// 照常，保证解禁后上下文完整；回复判断默认不做，开了只记录判断结果。
	MutedVoiceTranscriptionEnabled *bool `json:"muted_voice_transcription_enabled,omitempty"`
	MutedImageDescriptionEnabled   *bool `json:"muted_image_description_enabled,omitempty"`
	MutedReplyJudgmentEnabled      *bool `json:"muted_reply_judgment_enabled,omitempty"`
	SendRetryAttempts              int   `json:"send_retry_attempts,omitempty"`
	// 群退避与入站重跑的五个参数，见 send_retry_policy.go。
	sendRetrySettings
	SendChunkIntervalMS  int                  `json:"send_chunk_interval_ms,omitempty"`
	AutoImageDescription *bool                `json:"auto_image_description,omitempty"`
	AutoVideoPreprocess  *bool                `json:"auto_video_preprocess,omitempty"`
	ModelRoles           map[string]ModelRole `json:"model_roles,omitempty"`
	// PrivateClosingGrace 是私聊里「对方在收尾」时仍然照常回答的轮数。
	// 第一声再见就闭嘴不像人：正常人会接一两句「拜拜」再停。到这个数之后，
	// 候选回复只是又一句告别时就不再发出去。明确要求停止不受它约束，当场生效。
	PrivateClosingGrace int `json:"private_closing_grace,omitempty"`
	// InboundGroupConcurrency / InboundPrivateConcurrency 是同一会话可以同时
	// 处理的入站事件数。以前是代码里的两个常量（群 3、私聊硬编码 1），现在提到
	// 配置里，但默认值保持不变。
	InboundGroupConcurrency      int   `json:"inbound_group_concurrency,omitempty"`
	InboundPrivateConcurrency    int   `json:"inbound_private_concurrency,omitempty"`
	BotReplyLoopDetectionEnabled *bool `json:"bot_reply_loop_detection_enabled,omitempty"`
	// ReplyRefusalSuppressionEnabled 控制「30 分钟内对同一账号拒答满 4 次就暂停响应它」。
	// 默认开；关掉后拒答照常，只是不再因此暂停。
	ReplyRefusalSuppressionEnabled *bool `json:"reply_refusal_suppression_enabled,omitempty"`
	ReplySafetyMasterEnabled       *bool `json:"reply_account_safety_audit_master_enabled,omitempty"`
	// ReplyAccountSafetyAuditPrompt 是账号安全判断的自定义规则。留空使用内置范围；
	// 非空时作为管理员规则替代默认风险范围，但不改变审核输出协议。
	ReplyAccountSafetyAuditPrompt        string `json:"reply_account_safety_audit_prompt,omitempty"`
	groupReplyAccountSafetyAuditOverride *bool
	// NotebookSharedScopeEnabled 让笔记本跟随机器人：群聊私聊共用一本，新条目写进
	// 这台机器人的全局作用域，所有会话都能查到。默认打开——笔记本记的是这台机器人
	// 学到的梗和规矩，不是某个群的私产；关掉才按会话隔离。
	NotebookSharedScopeEnabled *bool `json:"notebook_shared_scope_enabled,omitempty"`
	PromptInjectTime           *bool `json:"prompt_inject_time,omitempty"`
	PromptInjectPlaintextRules *bool `json:"prompt_inject_plaintext_rules,omitempty"`
	PromptInjectGroupSender    *bool `json:"prompt_inject_group_sender,omitempty"`
	PromptChineseSlangHint     *bool `json:"prompt_chinese_slang_hint,omitempty"`
	// ProactiveReplyExtraCriteria 是接话评分的补充判据：本群的称呼、黑话和禁区。
	// 拼在内置评分提示词尾部，不替代评分口径，也不改变裸 JSON 输出契约。
	ProactiveReplyExtraCriteria string `json:"proactive_reply_extra_criteria,omitempty"`
	// PromptOverrides 是管理员改过的内置提示词正文，按 PromptSpec.Key 存。只存改过的，
	// 没出现的键用内置默认值，见 prompt_overrides.go。
	PromptOverrides            PromptOverrides `json:"prompt_overrides,omitempty"`
	MaxInputChars              int             `json:"max_input_chars,omitempty"`
	MaxReplyChars              int             `json:"max_reply_chars,omitempty"`
	NaturalReplySplitEnabled   *bool           `json:"natural_reply_split_enabled,omitempty"`
	ReplyPreserveLineBreaks    *bool           `json:"reply_preserve_line_breaks,omitempty"`
	ReplyLineSplitEnabled      *bool           `json:"reply_line_split_enabled,omitempty"`
	TypingDelayEnabled         *bool           `json:"typing_delay_enabled,omitempty"`
	TypingDelayPerCharMS       int             `json:"typing_delay_per_char_ms,omitempty"`
	SocialReplyEnabled         *bool           `json:"social_reply_enabled,omitempty"`
	ReplyMaxBubbles            int             `json:"reply_max_bubbles,omitempty"`
	ForwardReplyChunkThreshold int             `json:"forward_reply_chunk_threshold,omitempty"`
	// ForwardReplyEnabled 是合并转发卡片的总开关；关掉时两个阈值都不生效。
	// 两个阈值只描述「超过多少触发」，留空（0）是不按这一项触发，不再兼任开关。
	ForwardReplyEnabled   *bool           `json:"forward_reply_enabled,omitempty"`
	DirectReplyChunkSize  int             `json:"direct_reply_chunk_size,omitempty"`
	ForwardReplyThreshold int             `json:"forward_reply_threshold,omitempty"`
	RecallReplyMode       RecallReplyMode `json:"recall_reply_mode,omitempty"`
	RefusalStrategy       RefusalStrategy `json:"refusal_strategy,omitempty"`
	// LLMStreamingEnabled 默认开启原生流式调用，分别接收正文、思考与工具。
	// 工具参数完整后再执行；不支持流式或请求失败时可退回普通调用。
	// 显式 false 保留用户选择，未配置时使用默认值。
	LLMStreamingEnabled          *bool `json:"llm_streaming_enabled,omitempty"`
	RecallReplyAutoDeleteEnabled *bool `json:"recall_reply_auto_delete_enabled,omitempty"`
	RecallReplyTTLSeconds        int   `json:"recall_reply_auto_delete_delay_seconds,omitempty"`
	LLMIdentityMaskingEnabled    *bool `json:"llm_identity_masking_enabled,omitempty"`
	// ModelCallQuota 是这台机器人的每群额度默认值：滚动 5 小时窗口内，单个群能
	// 发起的模型调用次数。群配置里填了就以群为准，留空跟随这里；两边都是 0 表示不限。
	//
	// 按群算而不是按机器人算：一个群刷起来不该把别的群一起饿死。
	ModelCallQuota int64 `json:"model_call_quota,omitempty"`
	// ReplySamplePercent 是这台机器人的每群回复抽样率默认值（1–100）：没 @、没引用
	// 机器人、没叫名字的群消息，只有这个比例会交给模型判断要不要接话。0 表示不抽样。
	ReplySamplePercent int `json:"reply_sample_percent,omitempty"`

	// MaxContextTokens 限定这个机器人单次请求最多用掉多少上下文 token。
	// 0 表示不额外限制，跟随提供商配置档的窗口。它只能收紧不能放宽：配置档说
	// 模型只有 32K，这里填 200K 也不会真的发出 200K 的请求。
	MaxContextTokens int64 `json:"max_context_tokens,omitempty"`
	// RecentHistoryTokenBudget 限定正式回复提示词里近期聊天历史最多占多少 token。
	// 0 表示用默认值。生效值还要再按窗口份额收一次，所以它只能收紧不能放宽。
	//
	// 这里用 token 而不是条数：要钉住的成本、窗口和延迟三样都按 token 计价，而一条
	// 群消息可能是十几 token 的表情占位，也可能是三千 token 的长粘贴——按条数配，
	// 实际开销会在一个数量级的区间里飘。RecentContextLimit 仍按条数配，因为它管的
	// 是路由、指代和记忆门控这些「往回数 N 条」的旁路，那里条数才是对的单位。
	RecentHistoryTokenBudget int64 `json:"recent_history_token_budget,omitempty"`
	RecentContextLimit       int   `json:"recent_context_limit,omitempty"`
	// HistoryBackfillMessageLimit 是断线回补后每个会话最多进回复流程的消息条数，
	// 只算会触发回复的消息。没排上名额的照样补进上下文历史，但不跑媒体处理和回复——
	// 保持它小，防的是一批积压消息同时开出一堆图片视频任务。
	HistoryBackfillMessageLimit int   `json:"history_backfill_message_limit,omitempty"`
	ContextSummaryThreshold     int   `json:"context_summary_threshold,omitempty"`
	CrossGroupMemoryEnabled     *bool `json:"cross_group_memory_enabled,omitempty"`
	CrossPlatformMemoryEnabled  *bool `json:"cross_platform_memory_enabled,omitempty"`
	// WorldBookEnabled 控制这台机器人要不要带上世界书（世界观设定库）。树是
	// 全局一棵，这里只决定用不用；树是空的时候开着也不注入任何内容，所以默认开。
	WorldBookEnabled *bool `json:"world_book_enabled,omitempty"`
	// SelfNoteEnabled 控制这台机器人能不能自己写自述（自我认知）。默认关闭：
	// 让机器人改写关于自己的描述是行为变化，不该在升级后突然发生。开着时它写的
	// 条目只进提示词尾部的自述层，改不动人设正文，也改不动任何权限。
	SelfNoteEnabled *bool `json:"self_note_enabled,omitempty"`
	// RomanceEnabled 是人机恋（恋爱模式）的总开关。开着时用户才能和机器人确立
	// 恋人关系。默认关闭：机器人愿不愿意谈恋爱是部署者该亲手做的决定，不该在
	// 升级后突然发生。
	RomanceEnabled *bool `json:"romance_enabled,omitempty"`
	// MoodEnabled 让机器人有随相处涨落、随时间回落的心情，只影响语气。
	// 默认关闭：可感知的行为变化不该在升级后突然发生。
	MoodEnabled *bool `json:"mood_enabled,omitempty"`
	// PokeReplyEnabled 让机器人在被戳一戳时回一句（OneBot poke 通知）。默认关闭。
	PokeReplyEnabled *bool `json:"poke_reply_enabled,omitempty"`
	// ExpressionLearningEnabled 让机器人按群收集高频短表达和口癖，作为说话风格
	// 参考注入。默认关闭：它会把群成员的原话喂进提示词，开不开由部署者决定。
	ExpressionLearningEnabled *bool `json:"expression_learning_enabled,omitempty"`
	// 词典分词要把整个分词词典常驻内存（约 130MB），所以默认关。开启立即生效
	// （后台加载笔记本，期间选词退回 n-gram）；关闭要重启进程才真正生效——
	// 笔记本占用的内存本来也只有重启才能归还。
	DictSegmentEnabled *bool `json:"dict_segment_enabled,omitempty"`
	// 语义检索:消息经 embedding 模型转成向量,检索时按余弦相似度召回并与
	// 词面结果融合。需要 embedding 分组的提供商配置档,默认关。
	SemanticSearchEnabled      *bool                     `json:"semantic_search_enabled,omitempty"`
	ProactiveReplyChance       float64                   `json:"proactive_reply_chance,omitempty"`
	ProactiveReplyThreshold    float64                   `json:"proactive_reply_threshold,omitempty"`
	ChatInEnabled              *bool                     `json:"chat_in_enabled,omitempty"`
	ChatInLevel                ChatInLevel               `json:"chat_in_level,omitempty"`
	Participation              *ParticipationPreferences `json:"participation,omitempty"`
	ChatInThreshold            float64                   `json:"chat_in_threshold,omitempty"`
	ChatInChance               float64                   `json:"chat_in_chance,omitempty"`
	ChatInCooldownSeconds      int                       `json:"chat_in_cooldown_seconds,omitempty"`
	NaturalInterjectionEnabled *bool                     `json:"natural_interjection_enabled,omitempty"`
	ReplyRules                 []ReplyRule               `json:"reply_rules,omitempty"`
	MaxBotConcurrency          int                       `json:"max_bot_concurrency,omitempty"`
	RequestTimeout             time.Duration             `json:"request_timeout,omitempty"`
	// AgentEnabled 是旧的「启用 Agent」开关，现在只在迁移时读：加载配置时按它换算出
	// AgentMode，之后恒为 true（见 migrateAgentMode）。AgentEnabled=false 的旧非 Agent
	// 路径还留在代码里，界面上已经走不到，待后续移除。
	AgentEnabled bool `json:"agent_enabled,omitempty"`
	// AgentMode 是 standard（标准，全部能力）或 safe（安全，关掉 AgentSafeModeRules
	// 里的高风险能力，主人也一样）。新建机器人默认 standard，safe 只在主人选择时开启。
	AgentMode             string   `json:"agent_mode,omitempty"`
	AgentMaxSteps         int      `json:"agent_max_steps,omitempty"`
	AgentSkillRoots       []string `json:"agent_skill_roots,omitempty"`
	AgentMCPConfigPath    string   `json:"agent_mcp_config_path,omitempty"`
	AgentCommandAllowlist []string `json:"agent_command_allowlist,omitempty"`
	AgentCommandTimeoutMS int      `json:"agent_command_timeout_ms,omitempty"`
	// AgentCommandSandbox 见 agent.CommandSandbox* 常量：auto 有沙盒就用、
	// require 没有就拒绝执行、off 完全不套。留空按 auto。
	AgentCommandSandbox string `json:"agent_command_sandbox,omitempty"`
	// AgentCommandSandboxAllowNetwork 放开沙盒内的网络。默认切断——命令能联网
	// 就意味着读到的东西能被发出去，白名单挡不住这一层。
	AgentCommandSandboxAllowNetwork bool `json:"agent_command_sandbox_allow_network,omitempty"`
	// AgentFileWriteEnabled 打开 write_file / edit_file / save_to_workspace 和
	// manage_files 的写操作，默认关闭。
	// 读和写是两档权限：读错文件浪费一次调用，写错文件改的是磁盘。
	AgentFileWriteEnabled bool   `json:"agent_file_write_enabled,omitempty"`
	AgentBrowserCDPURL    string `json:"agent_browser_cdp_url,omitempty"`
	AgentBrowserTimeoutMS int    `json:"agent_browser_timeout_ms,omitempty"`
	// AgentBrowserControlEnabled 允许这台机器人使用浏览器控制扩展（browser_ext_*）。
	// 默认关闭：那组工具操作的是用户日常浏览器里的登录态，多一台机器人能用
	// 就多一处能借到这份登录态的地方，所以逐台显式打开。全局的总开关、站点
	// 白名单和读写档位另由 WebUI 的浏览器控制页决定，两边都开才真的能用。
	AgentBrowserControlEnabled bool `json:"agent_browser_control_enabled,omitempty"`
	// AgentBrowserBoxDisabled 关掉这台机器人对 Diana 内置常驻浏览器
	// （model/browserbox）的使用：browser_* 那组 CDP 工具本来会接到它上面，带着
	// 用户在里面登录过的站点。
	//
	// 默认是开的，写成「关闭」而不是「启用」：内置浏览器本身就要用户先去那一页
	// 打开、还要有浏览器可用，都做到了却还要逐台机器人再点一次，等于装好了默认
	// 不能用。登录态的那层担心由身份挡着——browser_* 不在非主人的工具白名单里
	// （见 RelationshipPolicy.allowedAgentToolNames），群成员拿不到这组工具，只有
	// 主人能驱动它；用户按下接管时连主人也当场失效。
	AgentBrowserBoxDisabled bool `json:"agent_browser_box_disabled,omitempty"`
}

type ModelRole struct {
	FollowChat bool        `json:"follow_chat,omitempty"`
	ProfileID  string      `json:"profile_id,omitempty"`
	Group      string      `json:"group,omitempty"`
	Model      string      `json:"model"`
	ProviderID string      `json:"provider_id,omitempty"`
	ModelID    string      `json:"model_id,omitempty"`
	Fallbacks  []ModelRole `json:"fallbacks,omitempty"`
}

func normalizeModelRoles(roles map[string]ModelRole) map[string]ModelRole {
	out := map[string]ModelRole{}
	for key, role := range roles {
		key = strings.ToLower(strings.TrimSpace(key))
		// 「跟随对话」对每个用途都成立，唯独对话自己不能跟随自己——那会绕成死circle。
		// 以前只有视觉理解允许跟随，别的用途要么单独绑，要么隐式落到 chat；隐式回落
		// 在界面上看不出来，用户没法明确表达「这一档我就是要跟着对话走」。
		if role.FollowChat && key == "chat" {
			role.FollowChat = false
		}
		role = normalizeModelRole(role)
		// 可绑定的键从 4 个扩到「5 个分组 + 17 个用途」，见 model_binding.go。
		if isModelBindingKey(key) && modelRoleConfigured(role) {
			out[key] = role
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeModelRole(role ModelRole) ModelRole {
	if role.FollowChat {
		return ModelRole{FollowChat: true}
	}
	role.ProfileID = strings.TrimSpace(role.ProfileID)
	role.Group = strings.TrimSpace(role.Group)
	role.Model = strings.TrimSpace(role.Model)
	role.ProviderID = strings.TrimSpace(role.ProviderID)
	role.ModelID = strings.TrimSpace(role.ModelID)
	if role.ProviderID != "" || role.ModelID != "" {
		role.ProfileID = ""
		role.Group = ""
		if role.Model == "" {
			role.Model = role.ModelID
		}
	}
	if role.Group != "" {
		role.ProfileID = ""
	}
	fallbacks := make([]ModelRole, 0, len(role.Fallbacks))
	for _, fallback := range role.Fallbacks {
		fallback.FollowChat = false
		fallback.Fallbacks = nil
		fallback = normalizeModelRole(fallback)
		if modelRoleConfigured(fallback) {
			fallbacks = append(fallbacks, fallback)
		}
	}
	role.Fallbacks = fallbacks
	return role
}

func modelRoleConfigured(role ModelRole) bool {
	if role.FollowChat {
		return true
	}
	return ((role.ProfileID != "" || role.Group != "") || (role.ProviderID != "" && role.ModelID != "")) && role.Model != ""
}

type ReplyRuleAction string

const (
	ReplyRuleActionModel ReplyRuleAction = "model"
	ReplyRuleActionVoice ReplyRuleAction = "voice"
)

type ReplyRule struct {
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Enabled      bool            `json:"enabled"`
	Prompt       string          `json:"prompt,omitempty"`
	Action       ReplyRuleAction `json:"action,omitempty"`
	LLMProfileID string          `json:"llm_profile_id,omitempty"`
}

type GroupConfig struct {
	// InheritanceMigrated 标记这份群配置已经清掉旧版抄进来的机器人值快照，只做一次。
	InheritanceMigrated     bool  `json:"inheritance_migrated,omitempty"`
	ReplyPreserveLineBreaks *bool `json:"reply_preserve_line_breaks,omitempty"`
	// ReplyLineSplitEnabled 的 nil 同样保留，发送时跟随所属机器人。
	ReplyLineSplitEnabled *bool `json:"reply_line_split_enabled,omitempty"`
	TypingDelayEnabled    *bool `json:"typing_delay_enabled,omitempty"`
	// Zero follows the bot's current merge threshold.
	ReplyMergeConfidencePercent int      `json:"reply_merge_confidence_percent,omitempty"`
	MarkedBotIDs                []string `json:"marked_bot_ids,omitempty"`
	// BotProfileID 指明这份群配置属于哪台机器人。两台机器人可以同时在一个群里，
	// 各自的触发词、回复频率和人格都该各管各的。空值是升级前的老记录，迁移时会
	// 归给当时的当前配置档。
	BotProfileID     string           `json:"bot_profile_id,omitempty"`
	GroupID          string           `json:"group_id"`
	Enabled          bool             `json:"enabled"`
	EnabledSet       bool             `json:"enabled_set,omitempty"`
	GroupTriggers    []string         `json:"group_triggers,omitempty"`
	GroupTriggerMode AliasTriggerMode `json:"group_trigger_mode,omitempty"`
	// SystemPrompt 非空时整份替换机器人的 SOUL.md，只在这个群里生效；留空跟随机器人。
	SystemPrompt string       `json:"system_prompt,omitempty"`
	ResponseMode ResponseMode `json:"response_mode,omitempty"`
	ReplyStyle   ReplyStyle   `json:"reply_style,omitempty"`
	// 旧版分群的表达覆盖，只在读旧配置时出现，WithDefaults 并进 SystemPrompt 后清空。
	ActionDescriptionEnabled *bool  `json:"action_description_enabled,omitempty"`
	SelfReference            string `json:"self_reference,omitempty"`
	SentenceEnders           string `json:"sentence_enders,omitempty"`
	// 下面这批「留空跟随机器人」的字段：零值、空串、nil 都表示没单独设置，运行时用
	// 所属机器人的当前值。以前新建和归一化时会把机器人当时的值抄进来并落库，之后
	// 机器人页再怎么改都进不了这个群（见 clearInheritedSnapshots）。
	WelcomeEnabled            *bool       `json:"welcome_enabled,omitempty"`
	WelcomeMessage            string      `json:"welcome_message,omitempty"`
	WelcomeMode               WelcomeMode `json:"welcome_mode,omitempty"`
	WelcomeTemplates          []string    `json:"welcome_templates,omitempty"`
	WelcomeLLMCooldownSeconds int         `json:"welcome_llm_cooldown_seconds,omitempty"`
	// ModelCallQuota 是这个群在滚动 5 小时窗口里能发起的模型调用次数上限。留空跟随
	// 机器人那一档，两边都没填表示不限。
	//
	// 口径和用量统计一致：这个群名下所有模型调用都算，包括判定、路由和工具步，
	// 不只是最终那句回复。主人不受限——额度用完还能让主人改配置，不然就锁死了。
	ModelCallQuota int64 `json:"model_call_quota,omitempty"`
	// ReplySamplePercent 是这个群的回复抽样率（1–100），留空跟随机器人。
	ReplySamplePercent       int   `json:"reply_sample_percent,omitempty"`
	MaxContextTokens         int64 `json:"max_context_tokens,omitempty"`
	RecentHistoryTokenBudget int64 `json:"recent_history_token_budget,omitempty"`
	RecentContextLimit       int   `json:"recent_context_limit,omitempty"`
	MaxReplyChars            int   `json:"max_reply_chars,omitempty"`
	// 分条和合并转发的四个阈值加一个开关。群和群的说话节奏不一样：一个技术群
	// 里长回复整条读更省事，一个闲聊群里同样长度得拆开发才不像播报。
	// 自然分条的 nil 必须保留，发送时才跟随所属机器人的当前值。
	NaturalReplySplitEnabled *bool `json:"natural_reply_split_enabled,omitempty"`
	ReplyMaxBubbles          int   `json:"reply_max_bubbles,omitempty"`
	DirectReplyChunkSize     int   `json:"direct_reply_chunk_size,omitempty"`
	// 合并转发：ForwardReplyEnabled nil 跟随机器人，false 本群关闭，true 本群单独设置。
	// 两个阈值 nil 同样跟随机器人。以前阈值是 int，群配置一存下来就把当时的值
	// （旧群多半是 0）定死，机器人页后来改成 140 也进不了这个群。
	ForwardReplyEnabled          *bool                     `json:"forward_reply_enabled,omitempty"`
	ForwardReplyThreshold        *int                      `json:"forward_reply_threshold,omitempty"`
	ForwardReplyChunkThreshold   *int                      `json:"forward_reply_chunk_threshold,omitempty"`
	ProactiveReplyChance         float64                   `json:"proactive_reply_chance,omitempty"`
	ProactiveReplyThreshold      float64                   `json:"proactive_reply_threshold,omitempty"`
	ChatInEnabled                *bool                     `json:"chat_in_enabled,omitempty"`
	ChatInLevel                  ChatInLevel               `json:"chat_in_level,omitempty"`
	Participation                *ParticipationPreferences `json:"participation,omitempty"`
	ChatInThreshold              float64                   `json:"chat_in_threshold,omitempty"`
	ChatInChance                 float64                   `json:"chat_in_chance,omitempty"`
	ChatInCooldownSeconds        int                       `json:"chat_in_cooldown_seconds,omitempty"`
	NaturalInterjectionEnabled   *bool                     `json:"natural_interjection_enabled,omitempty"`
	SocialReplyEnabled           *bool                     `json:"social_reply_enabled,omitempty"`
	MinimumReplyMemberLevel      int                       `json:"minimum_reply_member_level,omitempty"`
	RecallReplyAutoDeleteEnabled *bool                     `json:"recall_reply_auto_delete_enabled,omitempty"`
	RecallReplyTTLSeconds        int                       `json:"recall_reply_auto_delete_delay_seconds,omitempty"`
	// nil 跟随机器人；true/false 在本群对主动和直接回复统一开启/关闭账号安全审核。
	ReplyAccountSafetyAuditEnabled *bool  `json:"reply_account_safety_audit_enabled,omitempty"`
	ReplyAccountSafetyAuditPrompt  string `json:"reply_account_safety_audit_prompt,omitempty"`
	// 本群的补充判据，留空跟随机器人级。
	ProactiveReplyExtraCriteria string `json:"proactive_reply_extra_criteria,omitempty"`
	// 本群的发送退避和入站重跑参数，每项 0 表示跟随机器人。
	sendRetrySettings
	// 本群被禁言时是否暂停回复、暂停期间是否转写语音；nil 跟随机器人。
	MutedReplyPauseEnabled         *bool `json:"muted_reply_pause_enabled,omitempty"`
	MutedVoiceTranscriptionEnabled *bool `json:"muted_voice_transcription_enabled,omitempty"`
	MutedImageDescriptionEnabled   *bool `json:"muted_image_description_enabled,omitempty"`
	MutedReplyJudgmentEnabled      *bool `json:"muted_reply_judgment_enabled,omitempty"`
	// ExtensionAccess 按群覆盖 MCP / Skill 的开放范围，键是扩展 ID，没写的跟随
	// 机器人那一档。群管理员只能往严的方向改。
	ExtensionAccess        map[string]GroupExtensionAccess `json:"extension_access,omitempty"`
	PluginOverrides        map[string]bool                 `json:"plugin_overrides,omitempty"`
	PluginSettingOverrides PluginSettingOverrides          `json:"plugin_setting_overrides,omitempty"`
	ReplyGate              *ReplyGate                      `json:"reply_gate,omitempty"`
	UpdatedAt              time.Time                       `json:"updated_at,omitempty"`
}

// GroupExtensionAccess 是一个扩展在某个群里的开放范围：一个基线档位，加一对名单。
//
// 判定顺序是「停用 > 黑名单 > 白名单 > 档位」：停用等于这个群没这个能力，谁都不给；
// 黑名单无条件挡住，压过白名单；白名单是例外放行，名单里的账号不看档位也不看身份。
type GroupExtensionAccess struct {
	// Tier 为空表示这一项的基线跟随机器人。
	Tier string `json:"tier,omitempty"`
	// Allow 是额外放行的账号，能越过档位、身份和机器人那份名单，但越不过停用。
	Allow []string `json:"allow,omitempty"`
	// Deny 是本群不给用的账号，优先级最高。
	Deny []string `json:"deny,omitempty"`
}

func (a GroupExtensionAccess) Empty() bool {
	return a.Tier == "" && len(a.Allow) == 0 && len(a.Deny) == 0
}

// Allowed 判断这个账号是否被本群白名单放行。
func (a GroupExtensionAccess) Allowed(userID string) bool { return containsAccount(a.Allow, userID) }

// Denied 判断这个账号是否被本群黑名单挡住。
func (a GroupExtensionAccess) Denied(userID string) bool { return containsAccount(a.Deny, userID) }

func containsAccount(list []string, userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	for _, item := range list {
		if strings.TrimSpace(item) == userID {
			return true
		}
	}
	return false
}

type GroupConfigSet struct {
	Groups []GroupConfig `json:"groups"`
}

type ConfigPayload struct {
	ConnectionProfileID         string          `json:"connection_profile_id,omitempty"`
	ReplyMergeConfidencePercent int             `json:"reply_merge_confidence_percent,omitempty"`
	ID                          string          `json:"id,omitempty"`
	Name                        string          `json:"name,omitempty"`
	Platform                    string          `json:"platform,omitempty"`
	AvatarURL                   string          `json:"avatar_url,omitempty"`
	Profiles                    []ConfigPayload `json:"profiles,omitempty"`
	// MessageRelays 是跨机器人的消息互通链路，读接口一并回传给 WebUI。
	MessageRelays                     []MessageRelayPair `json:"message_relays,omitempty"`
	Enabled                           bool               `json:"enabled"`
	OneBotTransport                   string             `json:"onebot_transport,omitempty"`
	OneBotWSEndpoint                  string             `json:"onebot_ws_endpoint,omitempty"`
	OneBotHTTPURL                     string             `json:"onebot_http_url,omitempty"`
	OneBotHTTPSecret                  string             `json:"onebot_http_secret,omitempty"`
	OneBotReverseWSEndpoint           string             `json:"onebot_reverse_ws_endpoint"`
	OneBotAccessToken                 string             `json:"onebot_access_token,omitempty"`
	OneBotHTTPSecretConfigured        bool               `json:"onebot_http_secret_configured,omitempty"`
	OneBotAccessTokenConfigured       bool               `json:"onebot_access_token_configured,omitempty"`
	OneBotAccessTokenPreview          string             `json:"onebot_access_token_preview,omitempty"`
	TelegramBotToken                  string             `json:"telegram_bot_token,omitempty"`
	TelegramBotTokenConfigured        bool               `json:"telegram_bot_token_configured,omitempty"`
	TelegramAPIBaseURL                string             `json:"telegram_api_base_url,omitempty"`
	TelegramProxyURL                  string             `json:"telegram_proxy_url,omitempty"`
	TelegramSuppressBotMessages       *bool              `json:"telegram_suppress_bot_messages,omitempty"`
	QQTypingEnabled                   *bool              `json:"qq_typing_enabled,omitempty"`
	QQAppID                           string             `json:"qq_app_id,omitempty"`
	QQAppSecret                       string             `json:"qq_app_secret,omitempty"`
	QQAppSecretConfigured             bool               `json:"qq_app_secret_configured,omitempty"`
	QQSandbox                         bool               `json:"qq_sandbox,omitempty"`
	DingTalkClientID                  string             `json:"dingtalk_client_id,omitempty"`
	DingTalkClientSecret              string             `json:"dingtalk_client_secret,omitempty"`
	DingTalkClientSecretConfigured    bool               `json:"dingtalk_client_secret_configured,omitempty"`
	DingTalkRobotCode                 string             `json:"dingtalk_robot_code,omitempty"`
	FeishuAppID                       string             `json:"feishu_app_id,omitempty"`
	FeishuAppSecret                   string             `json:"feishu_app_secret,omitempty"`
	FeishuAppSecretConfigured         bool               `json:"feishu_app_secret_configured,omitempty"`
	FeishuVerificationToken           string             `json:"feishu_verification_token,omitempty"`
	FeishuVerificationTokenConfigured bool               `json:"feishu_verification_token_configured,omitempty"`
	FeishuEncryptKey                  string             `json:"feishu_encrypt_key,omitempty"`
	FeishuEncryptKeyConfigured        bool               `json:"feishu_encrypt_key_configured,omitempty"`
	FeishuAPIBaseURL                  string             `json:"feishu_api_base_url,omitempty"`
	WeComCorpID                       string             `json:"wecom_corp_id,omitempty"`
	WeComAgentID                      string             `json:"wecom_agent_id,omitempty"`
	WeComSecret                       string             `json:"wecom_secret,omitempty"`
	WeComSecretConfigured             bool               `json:"wecom_secret_configured,omitempty"`
	WeComToken                        string             `json:"wecom_token,omitempty"`
	WeComTokenConfigured              bool               `json:"wecom_token_configured,omitempty"`
	WeComEncodingAESKey               string             `json:"wecom_encoding_aes_key,omitempty"`
	WeComEncodingAESKeyConfigured     bool               `json:"wecom_encoding_aes_key_configured,omitempty"`
	// CallbackPath 是回调型平台要填到对方后台的路径，只读，供 WebUI 拼完整地址。
	CallbackPath                   string               `json:"callback_path,omitempty"`
	NoneBotBridgeEnabled           bool                 `json:"nonebot_bridge_enabled,omitempty"`
	NoneBotBridgeEndpoint          string               `json:"nonebot_bridge_endpoint,omitempty"`
	NoneBotBridgeToken             string               `json:"nonebot_bridge_token,omitempty"`
	NoneBotBridgeTokenConfigured   bool                 `json:"nonebot_bridge_token_configured,omitempty"`
	BotAccount                     string               `json:"bot_account,omitempty"`
	OwnerID                        string               `json:"owner_id,omitempty"`
	OwnerLoginEnabled              bool                 `json:"owner_login_enabled,omitempty"`
	OwnerLLMConfigEnabled          *bool                `json:"owner_llm_config_enabled,omitempty"`
	GroupTriggers                  []string             `json:"group_triggers,omitempty"`
	GroupTriggerMode               AliasTriggerMode     `json:"group_trigger_mode,omitempty"`
	DisabledGroups                 []string             `json:"disabled_groups,omitempty"`
	DisabledUsers                  []string             `json:"disabled_users,omitempty"`
	MarkedBotIDs                   []string             `json:"marked_bot_ids,omitempty"`
	GroupAdmission                 GroupAdmission       `json:"group_admission,omitempty"`
	PrivateAdmission               PrivateAdmission     `json:"private_admission,omitempty"`
	ReplyGate                      *ReplyGate           `json:"reply_gate,omitempty"`
	WelcomeEnabled                 bool                 `json:"welcome_enabled,omitempty"`
	WelcomeMessage                 string               `json:"welcome_message,omitempty"`
	WelcomeMode                    WelcomeMode          `json:"welcome_mode,omitempty"`
	WelcomeTemplates               []string             `json:"welcome_templates,omitempty"`
	WelcomeLLMCooldownSeconds      int                  `json:"welcome_llm_cooldown_seconds,omitempty"`
	SystemPrompt                   string               `json:"system_prompt,omitempty"`
	ResponseMode                   ResponseMode         `json:"response_mode,omitempty"`
	ReplyStyle                     ReplyStyle           `json:"reply_style,omitempty"`
	DebugModeEnabled               *bool                `json:"debug_mode_enabled,omitempty"`
	ReplyReferenceMode             ReplyDecorationMode  `json:"reply_reference_mode,omitempty"`
	ModelDisclosure                ModelDisclosure      `json:"model_disclosure,omitempty"`
	RepositoryDisclosure           RepositoryDisclosure `json:"repository_disclosure,omitempty"`
	MentionUserMode                ReplyDecorationMode  `json:"mention_user_mode,omitempty"`
	MarkdownToPlain                *bool                `json:"markdown_to_plain,omitempty"`
	ErrorNotifyEnabled             *bool                `json:"error_notify_enabled,omitempty"`
	MutedReplyPauseEnabled         *bool                `json:"muted_reply_pause_enabled,omitempty"`
	MutedVoiceTranscriptionEnabled *bool                `json:"muted_voice_transcription_enabled,omitempty"`
	MutedImageDescriptionEnabled   *bool                `json:"muted_image_description_enabled,omitempty"`
	MutedReplyJudgmentEnabled      *bool                `json:"muted_reply_judgment_enabled,omitempty"`
	ErrorPersonaReplyEnabled       *bool                `json:"error_persona_reply_enabled,omitempty"`
	ErrorReplyPrefix               string               `json:"error_reply_prefix,omitempty"`
	SendRetryAttempts              int                  `json:"send_retry_attempts,omitempty"`
	sendRetrySettings
	SendChunkIntervalMS            int                  `json:"send_chunk_interval_ms,omitempty"`
	PrivateClosingGrace            int                  `json:"private_closing_grace,omitempty"`
	InboundGroupConcurrency        int                  `json:"inbound_group_concurrency,omitempty"`
	InboundPrivateConcurrency      int                  `json:"inbound_private_concurrency,omitempty"`
	PromptInjectTime               *bool                `json:"prompt_inject_time,omitempty"`
	PromptInjectPlaintextRules     *bool                `json:"prompt_inject_plaintext_rules,omitempty"`
	PromptInjectGroupSender        *bool                `json:"prompt_inject_group_sender,omitempty"`
	PromptChineseSlangHint         *bool                `json:"prompt_chinese_slang_hint,omitempty"`
	AutoImageDescription           *bool                `json:"auto_image_description,omitempty"`
	AutoVideoPreprocess            *bool                `json:"auto_video_preprocess,omitempty"`
	ModelRoles                     map[string]ModelRole `json:"model_roles,omitempty"`
	BotReplyLoopDetectionEnabled   *bool                `json:"bot_reply_loop_detection_enabled,omitempty"`
	ReplyRefusalSuppressionEnabled *bool                `json:"reply_refusal_suppression_enabled,omitempty"`
	ReplySafetyMasterEnabled       *bool                `json:"reply_account_safety_audit_master_enabled,omitempty"`
	ReplyAccountSafetyAuditPrompt  string               `json:"reply_account_safety_audit_prompt,omitempty"`
	// NotebookSharedScopeEnabled 让笔记本跟随机器人：群聊私聊共用一本，新条目写进
	// 这台机器人的全局作用域，所有会话都能查到。默认打开——笔记本记的是这台机器人
	// 学到的梗和规矩，不是某个群的私产；关掉才按会话隔离。
	NotebookSharedScopeEnabled  *bool           `json:"notebook_shared_scope_enabled,omitempty"`
	ProactiveReplyExtraCriteria string          `json:"proactive_reply_extra_criteria,omitempty"`
	PromptOverrides             PromptOverrides `json:"prompt_overrides,omitempty"`
	MaxInputChars               int             `json:"max_input_chars,omitempty"`
	MaxReplyChars               int             `json:"max_reply_chars,omitempty"`
	NaturalReplySplitEnabled    *bool           `json:"natural_reply_split_enabled,omitempty"`
	ReplyPreserveLineBreaks     *bool           `json:"reply_preserve_line_breaks,omitempty"`
	ReplyLineSplitEnabled       *bool           `json:"reply_line_split_enabled,omitempty"`
	TypingDelayEnabled          *bool           `json:"typing_delay_enabled,omitempty"`
	TypingDelayPerCharMS        int             `json:"typing_delay_per_char_ms,omitempty"`
	SocialReplyEnabled          *bool           `json:"social_reply_enabled,omitempty"`
	ReplyMaxBubbles             int             `json:"reply_max_bubbles,omitempty"`
	ForwardReplyChunkThreshold  int             `json:"forward_reply_chunk_threshold,omitempty"`
	ForwardReplyEnabled         *bool           `json:"forward_reply_enabled,omitempty"`
	DirectReplyChunkSize        int             `json:"direct_reply_chunk_size,omitempty"`
	ForwardReplyThreshold       int             `json:"forward_reply_threshold,omitempty"`
	RecallReplyMode             RecallReplyMode `json:"recall_reply_mode,omitempty"`
	RefusalStrategy             RefusalStrategy `json:"refusal_strategy,omitempty"`
	// LLMStreamingEnabled 默认开启原生流式调用，分别接收正文、思考与工具。
	// 工具参数完整后再执行；不支持流式或请求失败时可退回普通调用。
	// 显式 false 保留用户选择，未配置时使用默认值。
	LLMStreamingEnabled          *bool `json:"llm_streaming_enabled,omitempty"`
	RecallReplyAutoDeleteEnabled *bool `json:"recall_reply_auto_delete_enabled,omitempty"`
	RecallReplyTTLSeconds        int   `json:"recall_reply_auto_delete_delay_seconds,omitempty"`
	LLMIdentityMaskingEnabled    *bool `json:"llm_identity_masking_enabled,omitempty"`
	// MaxContextTokens 限定这个机器人单次请求最多用掉多少上下文 token。
	// 0 表示不额外限制，跟随提供商配置档的窗口。它只能收紧不能放宽：配置档说
	// 模型只有 32K，这里填 200K 也不会真的发出 200K 的请求。
	MaxContextTokens                int64                     `json:"max_context_tokens,omitempty"`
	RecentHistoryTokenBudget        int64                     `json:"recent_history_token_budget,omitempty"`
	RecentContextLimit              int                       `json:"recent_context_limit,omitempty"`
	HistoryBackfillMessageLimit     int                       `json:"history_backfill_message_limit,omitempty"`
	ContextSummaryThreshold         int                       `json:"context_summary_threshold,omitempty"`
	CrossGroupMemoryEnabled         *bool                     `json:"cross_group_memory_enabled,omitempty"`
	CrossPlatformMemoryEnabled      *bool                     `json:"cross_platform_memory_enabled,omitempty"`
	WorldBookEnabled                *bool                     `json:"world_book_enabled,omitempty"`
	SelfNoteEnabled                 *bool                     `json:"self_note_enabled,omitempty"`
	RomanceEnabled                  *bool                     `json:"romance_enabled,omitempty"`
	MoodEnabled                     *bool                     `json:"mood_enabled,omitempty"`
	PokeReplyEnabled                *bool                     `json:"poke_reply_enabled,omitempty"`
	ExpressionLearningEnabled       *bool                     `json:"expression_learning_enabled,omitempty"`
	DictSegmentEnabled              *bool                     `json:"dict_segment_enabled,omitempty"`
	SemanticSearchEnabled           *bool                     `json:"semantic_search_enabled,omitempty"`
	ProactiveReplyChance            float64                   `json:"proactive_reply_chance,omitempty"`
	ProactiveReplyThreshold         float64                   `json:"proactive_reply_threshold,omitempty"`
	ChatInEnabled                   *bool                     `json:"chat_in_enabled,omitempty"`
	ChatInLevel                     ChatInLevel               `json:"chat_in_level,omitempty"`
	Participation                   *ParticipationPreferences `json:"participation,omitempty"`
	ChatInThreshold                 float64                   `json:"chat_in_threshold,omitempty"`
	ChatInChance                    float64                   `json:"chat_in_chance,omitempty"`
	ChatInCooldownSeconds           int                       `json:"chat_in_cooldown_seconds,omitempty"`
	NaturalInterjectionEnabled      *bool                     `json:"natural_interjection_enabled,omitempty"`
	ReplyRules                      []ReplyRule               `json:"reply_rules,omitempty"`
	MaxBotConcurrency               int                       `json:"max_bot_concurrency,omitempty"`
	RequestTimeoutMS                int64                     `json:"request_timeout_ms,omitempty"`
	AgentEnabled                    bool                      `json:"agent_enabled,omitempty"`
	AgentMode                       string                    `json:"agent_mode,omitempty"`
	AgentMaxSteps                   int                       `json:"agent_max_steps,omitempty"`
	AgentSkillRoots                 []string                  `json:"agent_skill_roots,omitempty"`
	AgentMCPConfigPath              string                    `json:"agent_mcp_config_path,omitempty"`
	AgentCommandAllowlist           []string                  `json:"agent_command_allowlist,omitempty"`
	AgentCommandTimeoutMS           int                       `json:"agent_command_timeout_ms,omitempty"`
	AgentCommandSandbox             string                    `json:"agent_command_sandbox,omitempty"`
	AgentCommandSandboxAllowNetwork bool                      `json:"agent_command_sandbox_allow_network,omitempty"`
	AgentFileWriteEnabled           bool                      `json:"agent_file_write_enabled,omitempty"`
	AgentBrowserCDPURL              string                    `json:"agent_browser_cdp_url,omitempty"`
	AgentBrowserTimeoutMS           int                       `json:"agent_browser_timeout_ms,omitempty"`
	AgentBrowserControlEnabled      bool                      `json:"agent_browser_control_enabled,omitempty"`
	AgentBrowserBoxDisabled         bool                      `json:"agent_browser_box_disabled,omitempty"`
}

// DefaultGroupConfig 返回指定群的默认行为配置，只包含群作用域字段。
func DefaultGroupConfig(groupID string, base BotConfig) GroupConfig {
	base = base.WithDefaults()
	// 只放群自己的状态。触发词、欢迎、上下文预算、撤回回复这些「留空跟随机器人」的
	// 字段一律不抄：抄进来就是一份快照，存盘后机器人页改了也进不了这个群。
	return GroupConfig{
		GroupID: strings.TrimSpace(groupID),
		// 新建的群配置跟着机器人的新群默认走：白名单模式下新群默认不工作，
		// 建一条配置出来不该把它悄悄打开。
		Enabled:                 base.GroupAdmission.NewGroupEnabled(),
		EnabledSet:              true,
		InheritanceMigrated:     true,
		MinimumReplyMemberLevel: 0,
		PluginOverrides:         map[string]bool{},
		PluginSettingOverrides:  PluginSettingOverrides{},
	}
}

// clearInheritedSnapshots 清掉旧版抄进群配置的机器人值。
//
// 旧版新建群配置、每次保存归一化时都把机器人当时的值写进群里，于是「留空跟随机器人」
// 的字段全成了定死的快照。分不出哪些是用户单独填的，只能按值判断：和所属机器人现在
// 的值相同的清成跟随，清完这一刻行为完全不变。不同的保留——它可能是过期快照，也可能
// 是真的单独设置（比如机器人开着、本群特意关掉），没法替用户猜，界面上显示成本群单独
// 设置，想跟随时清空即可。不拿系统默认值比：开关类的「关」往往就是默认值，会把本群
// 特意关掉的设置误清。
//
// 入群欢迎开关旧版是 bool，false 不落盘：读回来的 nil 按旧行为当成关闭再比较，
// 免得机器人开着欢迎时，一批本来不欢迎的群升级后突然开始欢迎。
func (cfg GroupConfig) clearInheritedSnapshots(base BotConfig) GroupConfig {
	bot := base.WithDefaults()
	sameStrings := func(value []string, pick func(BotConfig) []string) bool {
		return len(value) > 0 && slices.Equal(value, cleanStrings(pick(bot)))
	}
	sameBool := func(value *bool, pick func(BotConfig) bool) bool {
		return value != nil && *value == pick(bot)
	}
	clearInt := func(value *int, pick func(BotConfig) int) {
		if *value > 0 && *value == pick(bot) {
			*value = 0
		}
	}
	clearInt64 := func(value *int64, pick func(BotConfig) int64) {
		if *value > 0 && *value == pick(bot) {
			*value = 0
		}
	}
	clearFloat := func(value *float64, pick func(BotConfig) float64) {
		if *value > 0 && *value == pick(bot) {
			*value = 0
		}
	}
	if sameStrings(cfg.GroupTriggers, func(c BotConfig) []string { return c.GroupTriggers }) {
		cfg.GroupTriggers = nil
	}
	if cfg.GroupTriggerMode != "" && cfg.GroupTriggerMode == bot.GroupTriggerMode {
		cfg.GroupTriggerMode = ""
	}
	if cfg.WelcomeEnabled == nil {
		cfg.WelcomeEnabled = boolPointer(false)
	}
	if sameBool(cfg.WelcomeEnabled, func(c BotConfig) bool { return c.WelcomeEnabled }) {
		cfg.WelcomeEnabled = nil
	}
	if cfg.WelcomeMessage != "" && cfg.WelcomeMessage == strings.TrimSpace(bot.WelcomeMessage) {
		cfg.WelcomeMessage = ""
	}
	if cfg.WelcomeMode != "" && cfg.WelcomeMode == bot.WelcomeMode {
		cfg.WelcomeMode = ""
	}
	if sameStrings(cfg.WelcomeTemplates, func(c BotConfig) []string { return c.WelcomeTemplates }) {
		cfg.WelcomeTemplates = nil
	}
	clearInt(&cfg.WelcomeLLMCooldownSeconds, func(c BotConfig) int { return c.WelcomeLLMCooldownSeconds })
	clearInt64(&cfg.MaxContextTokens, func(c BotConfig) int64 { return c.MaxContextTokens })
	clearInt64(&cfg.RecentHistoryTokenBudget, func(c BotConfig) int64 { return c.RecentHistoryTokenBudget })
	clearInt(&cfg.RecentContextLimit, func(c BotConfig) int { return c.RecentContextLimit })
	clearInt(&cfg.MaxReplyChars, func(c BotConfig) int { return c.MaxReplyChars })
	clearInt(&cfg.ReplyMaxBubbles, func(c BotConfig) int { return c.ReplyMaxBubbles })
	clearInt(&cfg.DirectReplyChunkSize, func(c BotConfig) int { return c.DirectReplyChunkSize })
	clearInt(&cfg.RecallReplyTTLSeconds, func(c BotConfig) int { return c.RecallReplyTTLSeconds })
	clearInt(&cfg.ChatInCooldownSeconds, func(c BotConfig) int { return c.ChatInCooldownSeconds })
	clearFloat(&cfg.ProactiveReplyChance, func(c BotConfig) float64 { return c.ProactiveReplyChance })
	clearFloat(&cfg.ProactiveReplyThreshold, func(c BotConfig) float64 { return c.ProactiveReplyThreshold })
	clearFloat(&cfg.ChatInThreshold, func(c BotConfig) float64 { return c.ChatInThreshold })
	clearFloat(&cfg.ChatInChance, func(c BotConfig) float64 { return c.ChatInChance })
	if sameBool(cfg.ChatInEnabled, func(c BotConfig) bool { return boolValue(c.ChatInEnabled, true) }) {
		cfg.ChatInEnabled = nil
	}
	if cfg.ChatInLevel != "" && cfg.ChatInLevel == bot.ChatInLevel.Normalized() {
		cfg.ChatInLevel = ""
	}
	if sameBool(cfg.NaturalInterjectionEnabled, func(c BotConfig) bool { return boolValue(c.NaturalInterjectionEnabled, false) }) {
		cfg.NaturalInterjectionEnabled = nil
	}
	if sameBool(cfg.SocialReplyEnabled, func(c BotConfig) bool { return boolValue(c.SocialReplyEnabled, false) }) {
		cfg.SocialReplyEnabled = nil
	}
	if sameBool(cfg.RecallReplyAutoDeleteEnabled, func(c BotConfig) bool { return boolValue(c.RecallReplyAutoDeleteEnabled, false) }) {
		cfg.RecallReplyAutoDeleteEnabled = nil
	}
	cfg.InheritanceMigrated = true
	return cfg
}

// positiveOptionalCount 复制一个可选计数；nil 和不大于 0 的值都当作没填，跟随上级。
// 关掉走 ForwardReplyEnabled，这里不让 0 兼任「关闭」。
func positiveOptionalCount(value *int) *int {
	if value == nil || *value <= 0 {
		return nil
	}
	count := *value
	return &count
}

// WithDefaults 补齐群配置的空值，避免旧数据或局部提交破坏运行时默认行为。
func (cfg GroupConfig) WithDefaults(groupID string, base BotConfig) GroupConfig {
	cfg.ReplyMergeConfidencePercent = max(0, min(100, cfg.ReplyMergeConfidencePercent))
	cfg.sendRetrySettings = cfg.sendRetrySettings.clamped()
	cfg.MarkedBotIDs = cleanStrings(append([]string(nil), cfg.MarkedBotIDs...))
	cfg.Participation = copyParticipation(cfg.Participation)
	defaults := DefaultGroupConfig(groupID, base)
	cfg.GroupID = strings.TrimSpace(cfg.GroupID)
	if cfg.GroupID == "" {
		cfg.GroupID = defaults.GroupID
	}
	cfg.SystemPrompt = strings.TrimSpace(cfg.SystemPrompt)
	if strings.TrimSpace(string(cfg.ResponseMode)) != "" {
		cfg.ResponseMode = cfg.ResponseMode.Normalized()
	}
	// 只有拿到这个群自己那台机器人的配置时才允许继承人设。批量归一化仍可能被
	// 塞进另一台机器人的 base（例如「全部机器人」视图里的单群归一化），这时
	// 宁可让人设留空、运行时按机器人解析，也不能把别人的人设抄进来。
	if cfg.SystemPrompt != "" || cfg.BotProfileID == "" || cfg.BotProfileID == base.ID {
		if knownReplyStyle(string(cfg.ReplyStyle)) && cfg.SystemPrompt == "" {
			cfg.SystemPrompt = inheritedPersonaForStyleMigration(base.SystemPrompt)
		}
		cfg.SystemPrompt = migratePersonaStyle(cfg.SystemPrompt, &cfg.ReplyStyle, &cfg.ActionDescriptionEnabled)
	}
	// 旧版分群的自称、语气词、动作描写只能并进这个群自己的 SOUL.md。群没有自己的
	// 正文时丢掉它们：抄一份机器人的正文进来会让这个群从此和机器人的人设脱钩。
	if cfg.SystemPrompt != "" {
		cfg.SystemPrompt = foldLegacyPersona(cfg.SystemPrompt, nil, cfg.SelfReference, cfg.SentenceEnders, cfg.ActionDescriptionEnabled)
	}
	cfg.ActionDescriptionEnabled = nil
	cfg.SelfReference = ""
	cfg.SentenceEnders = ""
	if !cfg.EnabledSet {
		cfg.Enabled = true
		cfg.EnabledSet = true
	}
	// 下面只做清洗和钳制，不再从机器人补值：空着就是跟随机器人，运行时再取。
	cfg.GroupTriggers = cleanStrings(cfg.GroupTriggers)
	cfg.WelcomeMessage = strings.TrimSpace(cfg.WelcomeMessage)
	if strings.TrimSpace(string(cfg.WelcomeMode)) != "" {
		cfg.WelcomeMode = normalizeWelcomeMode(cfg.WelcomeMode)
	}
	cfg.WelcomeTemplates = cleanStrings(cfg.WelcomeTemplates)
	cfg.WelcomeEnabled = copyBoolPointer(cfg.WelcomeEnabled)
	cfg.WelcomeLLMCooldownSeconds = max(0, cfg.WelcomeLLMCooldownSeconds)
	cfg.MaxContextTokens = max(0, cfg.MaxContextTokens)
	cfg.RecentHistoryTokenBudget = max(0, cfg.RecentHistoryTokenBudget)
	cfg.RecentContextLimit = max(0, cfg.RecentContextLimit)
	cfg.MaxReplyChars = max(0, cfg.MaxReplyChars)
	cfg.ReplyMaxBubbles = max(0, cfg.ReplyMaxBubbles)
	cfg.DirectReplyChunkSize = max(0, cfg.DirectReplyChunkSize)
	cfg.ForwardReplyThreshold = positiveOptionalCount(cfg.ForwardReplyThreshold)
	cfg.ForwardReplyChunkThreshold = positiveOptionalCount(cfg.ForwardReplyChunkThreshold)
	cfg.ForwardReplyEnabled = copyBoolPointer(cfg.ForwardReplyEnabled)
	cfg.ProactiveReplyChance = max(0, min(1, cfg.ProactiveReplyChance))
	cfg.ProactiveReplyThreshold = max(0, min(1, cfg.ProactiveReplyThreshold))
	cfg.ChatInEnabled = copyBoolPointer(cfg.ChatInEnabled)
	cfg.SocialReplyEnabled = copyBoolPointer(cfg.SocialReplyEnabled)
	cfg.ChatInLevel = cfg.ChatInLevel.Normalized()
	cfg.ChatInThreshold = clampChatInRatio(cfg.ChatInThreshold)
	cfg.ChatInChance = clampChatInRatio(cfg.ChatInChance)
	if cfg.ChatInCooldownSeconds < 0 {
		cfg.ChatInCooldownSeconds = 0
	}
	cfg.NaturalInterjectionEnabled = copyBoolPointer(cfg.NaturalInterjectionEnabled)
	if cfg.MinimumReplyMemberLevel < 0 {
		cfg.MinimumReplyMemberLevel = 0
	} else if cfg.MinimumReplyMemberLevel > maximumReplyMemberLevel {
		cfg.MinimumReplyMemberLevel = maximumReplyMemberLevel
	}
	cfg.RecallReplyAutoDeleteEnabled = copyBoolPointer(cfg.RecallReplyAutoDeleteEnabled)
	cfg.RecallReplyTTLSeconds = max(0, min(maximumRecallReplyTTLSeconds, cfg.RecallReplyTTLSeconds))
	if cfg.PluginOverrides == nil {
		cfg.PluginOverrides = map[string]bool{}
	}
	if cfg.PluginSettingOverrides == nil {
		cfg.PluginSettingOverrides = PluginSettingOverrides{}
	}
	if cfg.ReplyGate != nil {
		normalized := cfg.ReplyGate.WithDefaults()
		cfg.ReplyGate = &normalized
	}
	// 旧数据里抄进来的机器人值快照只清一次。必须拿到这个群自己那台机器人才动手：
	// 拿别的机器人比，会把真正的单独设置当成快照清掉。
	if !cfg.InheritanceMigrated && (cfg.BotProfileID == "" || cfg.BotProfileID == base.ID) {
		cfg = cfg.clearInheritedSnapshots(base)
	}
	if cfg.UpdatedAt.IsZero() {
		cfg.UpdatedAt = time.Now()
	}
	return cfg
}

// ConfigForGroup 返回指定群配置。
// ConfigForGroup 找这台机器人在这个群里的配置。
//
// 先按 (机器人, 群) 精确找；找不到再回落到没有机器人标记的老记录——迁移会把它们
// 填上，这里的回落只是保险，避免迁移之前的一瞬间群配置整体失效。
func (s GroupConfigSet) ConfigForGroup(botProfileID, groupID string) (GroupConfig, bool) {
	botProfileID, groupID = strings.TrimSpace(botProfileID), strings.TrimSpace(groupID)
	var legacy *GroupConfig
	for index := range s.Groups {
		cfg := s.Groups[index]
		if cfg.GroupID != groupID {
			continue
		}
		if strings.TrimSpace(cfg.BotProfileID) == botProfileID {
			return cfg, true
		}
		if strings.TrimSpace(cfg.BotProfileID) == "" && legacy == nil {
			legacy = &s.Groups[index]
		}
	}
	if legacy != nil {
		return *legacy, true
	}
	return GroupConfig{}, false
}

// ConfigForGroupAnyProfile 不区分机器人地找这个群的配置，返回排在最前的一份。
//
// 只给「还不知道是哪台机器人在问」的入口用——群管理员自助那条链路的会话里目前
// 没有机器人身份。它保持了改造前的行为；等那条链路把 profile 带上，这个方法就该
// 从调用点撤掉。
func (s GroupConfigSet) ConfigForGroupAnyProfile(groupID string) (GroupConfig, bool) {
	groupID = strings.TrimSpace(groupID)
	for _, cfg := range s.Groups {
		if cfg.GroupID == groupID {
			return cfg, true
		}
	}
	return GroupConfig{}, false
}

// GroupsForProfile 返回这台机器人的全部群配置；botProfileID 留空表示不筛。
func (s GroupConfigSet) GroupsForProfile(botProfileID string) []GroupConfig {
	botProfileID = strings.TrimSpace(botProfileID)
	if botProfileID == "" {
		return append([]GroupConfig(nil), s.Groups...)
	}
	out := make([]GroupConfig, 0, len(s.Groups))
	for _, cfg := range s.Groups {
		if strings.TrimSpace(cfg.BotProfileID) == botProfileID {
			out = append(out, cfg)
		}
	}
	return out
}

// BotConfigResolver 按机器人档案 ID 找回它自己的配置。
//
// 群配置默认跟随它所属的那台机器人，可归一化和写入这两条路径拿到的往往只有
// 「当前这台」。有了这个解析器，批量处理才能一条一条地问「这个群是谁的」，而
// 不是把一台机器人的默认值糊到所有群上。返回 false 表示这个档案已经不在了，
// 调用方回落到传进来的 base。
type BotConfigResolver func(profileID string) (BotConfig, bool)

// baseBot 挑出这条群配置真正该跟随的机器人。
func (cfg GroupConfig) baseBot(base BotConfig, resolve BotConfigResolver) BotConfig {
	profileID := strings.TrimSpace(cfg.BotProfileID)
	if profileID == "" || resolve == nil {
		return base
	}
	if owner, ok := resolve(profileID); ok {
		return owner
	}
	return base
}

// WithDefaultsResolved 先按 bot_profile_id 找出这个群所属的机器人，再补默认值。
// resolve 为空或找不到档案时退回 base，行为与 WithDefaults 一致。
func (cfg GroupConfig) WithDefaultsResolved(groupID string, base BotConfig, resolve BotConfigResolver) GroupConfig {
	return cfg.WithDefaults(groupID, cfg.baseBot(base, resolve))
}

// WithDefaults migrates and normalizes all persisted group policies.
func (s GroupConfigSet) WithDefaults(base BotConfig) GroupConfigSet {
	return s.WithDefaultsResolved(base, nil)
}

// WithDefaultsResolved 归一化整份群配置，每个群各自跟随自己那台机器人。
func (s GroupConfigSet) WithDefaultsResolved(base BotConfig, resolve BotConfigResolver) GroupConfigSet {
	groups := make([]GroupConfig, 0, len(s.Groups))
	for _, cfg := range s.Groups {
		groups = append(groups, cfg.WithDefaultsResolved(cfg.GroupID, base, resolve))
	}
	s.Groups = groups
	return s
}

// Upsert 写入或替换指定群配置。
func (s GroupConfigSet) Upsert(cfg GroupConfig, base BotConfig) GroupConfigSet {
	return s.UpsertResolved(cfg, base, nil)
}

// UpsertResolved 写入或替换指定群配置，按 bot_profile_id 决定跟随哪台机器人。
func (s GroupConfigSet) UpsertResolved(cfg GroupConfig, base BotConfig, resolve BotConfigResolver) GroupConfigSet {
	cfg = cfg.WithDefaultsResolved(cfg.GroupID, base, resolve)
	cfg.EnabledSet = true
	cfg.UpdatedAt = time.Now()
	next := make([]GroupConfig, 0, len(s.Groups)+1)
	replaced := false
	for _, existing := range s.Groups {
		// 同一个群号在不同机器人下是两份配置，只替换属于同一台的那一份。
		if existing.GroupID == cfg.GroupID && strings.TrimSpace(existing.BotProfileID) == strings.TrimSpace(cfg.BotProfileID) {
			next = append(next, cfg)
			replaced = true
			continue
		}
		next = append(next, existing)
	}
	if !replaced {
		next = append(next, cfg)
	}
	s.Groups = next
	return s
}

const (
	DefaultProfileName = "默认机器人"
	DefaultPlatform    = PlatformOneBotV11
)

// ProfileSet 是全部机器人的配置。没有「当前」或「激活」的那一台：运行时按消息所属的
// 机器人取配置，WebUI 编辑哪一台由前端自己记。旧数据里的 active_id 字段读取时直接忽略。
type ProfileSet struct {
	Profiles []BotConfig `json:"profiles"`
	// MessageRelays 是「消息互通」的链路表。它跨机器人，不属于任何一台，所以
	// 放在配置集这一层而不是单台机器人的配置里。
	MessageRelays []MessageRelayPair `json:"message_relays,omitempty"`
}

var (
	ErrMissingOneBotEndpoint  = errors.New("diana: onebot reverse websocket endpoint is required")
	ErrMissingTelegramToken   = errors.New("assistant: telegram bot token is required")
	ErrInvalidTelegramAPIBase = errors.New("assistant: telegram api base url must be http(s)")
	ErrInvalidOneBotEndpoint  = errors.New("diana: onebot reverse websocket endpoint must use ws or wss and include a host")
	ErrBotDisabled            = errors.New("diana: bot is disabled")

	ErrMissingQQCredentials       = errors.New("assistant: qq official bot app id and app secret are required")
	ErrMissingDingTalkCredentials = errors.New("assistant: dingtalk client id and client secret are required")
	ErrMissingFeishuCredentials   = errors.New("assistant: feishu app id and app secret are required")
	ErrMissingWeComCredentials    = errors.New("assistant: wecom corp id, agent id and secret are required")
	ErrInvalidWeComAgentID        = errors.New("assistant: wecom agent id must be numeric")
	ErrMissingWeComCallbackKeys   = errors.New("assistant: wecom token and encoding aes key are required to receive messages")
	ErrInvalidFeishuAPIBase       = errors.New("assistant: feishu api base url must be http(s)")
)

// NewProfileSet 基于单个机器人配置创建配置集。
func NewProfileSet(cfg BotConfig) ProfileSet {
	profile := cfg.WithDefaults()
	profile.ID = uuid.NewString()
	return ProfileSet{Profiles: []BotConfig{profile}}
}

// WithMessageRelays 换一整份互通配置。
func (s ProfileSet) WithMessageRelays(pairs []MessageRelayPair) ProfileSet {
	s.MessageRelays = pairs
	return s.WithDefaults()
}

// WithProfileEnabled 只切换单台机器人的启用状态，其余档案原样保留。
// 档案不存在时返回 ok=false，调用方按 404 处理。
func (s ProfileSet) WithProfileEnabled(id string, enabled bool) (ProfileSet, bool) {
	id = strings.TrimSpace(id)
	profiles := make([]BotConfig, len(s.Profiles))
	copy(profiles, s.Profiles)
	for i := range profiles {
		if strings.TrimSpace(profiles[i].ID) != id {
			continue
		}
		profiles[i].Enabled = enabled
		s.Profiles = profiles
		return s.WithDefaults(), true
	}
	return s, false
}

// WithAgentBrowser 只改一台机器人的交互式浏览器接入参数。
//
// 单独开一条路而不是让界面回存整份配置：交互式浏览器的入口在扩展页，那一页没有、
// 也不该有整份机器人配置。把整份配置读出来改一个字段再存回去，等于让这一页替所有
// 别的字段负责——凭据、平台连接、人设，任何一处读回来是脱敏值就会被写坏。
func (s ProfileSet) WithAgentBrowser(id, cdpURL string, timeoutMS int) (ProfileSet, bool) {
	id = strings.TrimSpace(id)
	profiles := make([]BotConfig, len(s.Profiles))
	copy(profiles, s.Profiles)
	for i := range profiles {
		if strings.TrimSpace(profiles[i].ID) != id {
			continue
		}
		profiles[i].AgentBrowserCDPURL = strings.TrimSpace(cdpURL)
		profiles[i].AgentBrowserTimeoutMS = timeoutMS
		s.Profiles = profiles
		// WithDefaults 会把空地址和越界超时补回默认值，省得界面自己复述一遍规则。
		return s.WithDefaults(), true
	}
	return s, false
}

// WithAllProfilesEnabled 把全部机器人的启用状态统一改成 enabled。
func (s ProfileSet) WithAllProfilesEnabled(enabled bool) ProfileSet {
	profiles := make([]BotConfig, len(s.Profiles))
	copy(profiles, s.Profiles)
	for i := range profiles {
		profiles[i].Enabled = enabled
	}
	s.Profiles = profiles
	return s.WithDefaults()
}

// NormalizeProfileName 规范化机器人配置名称。
func NormalizeProfileName(name string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	return DefaultProfileName
}

// ConfigForProfile 按 ID 取出这台机器人的配置。
func (s ProfileSet) ConfigForProfile(id string) (BotConfig, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return BotConfig{}, false
	}
	for _, profile := range s.Profiles {
		if strings.TrimSpace(profile.ID) == id {
			return profile.WithDefaults(), true
		}
	}
	return BotConfig{}, false
}

// Resolver 把配置集包成 BotConfigResolver，供群配置按机器人归一化使用。
func (s ProfileSet) Resolver() BotConfigResolver {
	return s.ConfigForProfile
}

// Delete 从配置集中删除指定机器人配置。
func (s ProfileSet) Delete(id string) ProfileSet {
	id = strings.TrimSpace(id)
	if len(s.Profiles) == 0 {
		return s
	}
	next := make([]BotConfig, 0, len(s.Profiles))
	for _, profile := range s.Profiles {
		if profile.ID == id {
			continue
		}
		next = append(next, profile)
	}
	s.Profiles = next
	// 机器人没了，指向它的互通链路也就断了。留着只会让转发一直往一个不存在的
	// 机器人发，然后每条消息都在日志里失败一次。
	s.MessageRelays = messageRelaysWithoutProfile(s.MessageRelays, id)
	return s
}

// WithDefaults 补齐机器人配置集的默认字段和唯一 ID。
func (s ProfileSet) WithDefaults() ProfileSet {
	s.MessageRelays = NormalizeMessageRelays(s.MessageRelays)
	if len(s.Profiles) > 0 {
		profiles := make([]BotConfig, len(s.Profiles))
		copy(profiles, s.Profiles)
		s.Profiles = profiles
	}
	seen := make(map[string]struct{}, len(s.Profiles))
	for i := range s.Profiles {
		id := strings.TrimSpace(s.Profiles[i].ID)
		if id == "" {
			id = uuid.NewString()
		}
		if _, ok := seen[id]; ok {
			id = uuid.NewString()
		}
		seen[id] = struct{}{}
		s.Profiles[i].ID = id
		s.Profiles[i] = s.Profiles[i].WithDefaults()
	}
	return s
}

// DefaultBotConfig 返回 OneBot v11 机器人默认配置。
func DefaultBotConfig() BotConfig {
	// 默认不开启机器人，避免首次启动服务就暴露 OneBot 连接面。
	return BotConfig{
		Name:                    DefaultProfileName,
		Platform:                DefaultPlatform,
		Enabled:                 false,
		OneBotReverseWSEndpoint: "ws://127.0.0.1:18080/onebot/v11/ws",
		NoneBotBridgeEndpoint:   "ws://127.0.0.1:8080/onebot/v11/ws",
		GroupTriggers:           []string{"Diana", "diana"},
		GroupTriggerMode:        defaultAliasTriggerMode,
		// 引用和 @ 默认交给模型自己判断。「每条都带」和「一条都不带」都不像真人，
		// 而 auto 拿得到插话人数这类算得出来的信号（见 replyDecorationPrompt），
		// 冷清时不带、需要点名时才带。
		ReplyReferenceMode:        ReplyDecorationAuto,
		MentionUserMode:           ReplyDecorationAuto,
		DisabledGroups:            []string{},
		DisabledUsers:             []string{},
		GroupAdmission:            GroupAdmission{}.WithDefaults(),
		PrivateAdmission:          PrivateAdmission{}.WithDefaults(),
		WelcomeEnabled:            false,
		WelcomeMessage:            "欢迎加入本群，可以直接 @我 开始聊天。",
		WelcomeMode:               WelcomeModeFixed,
		WelcomeLLMCooldownSeconds: defaultWelcomeLLMCooldownSeconds,
		SystemPrompt:              defaultSystemPrompt,
		ResponseMode:              ResponseModeStandard,
		ErrorReplyPrefix:          "出错了：",
		SendRetryAttempts:         3,
		// 连发间隔和每条长度取的是聊天体量：几百字一坨、300ms 连发怎么看都不像
		// 真人。这两个数原先是群友风格在 apply 里钳出来的，风格不再改配置之后
		// 搬到这里当默认值——想要长一点的气泡、快一点的连发就在 WebUI 里改。
		SendChunkIntervalMS:            chatSendChunkIntervalMS,
		PrivateClosingGrace:            defaultPrivateClosingGrace,
		InboundGroupConcurrency:        defaultInboundGroupConcurrency,
		InboundPrivateConcurrency:      defaultInboundPrivateConcurrency,
		ChatInEnabled:                  boolPointer(true),
		ChatInLevel:                    defaultChatInLevel,
		NaturalInterjectionEnabled:     boolPointer(false),
		MaxInputChars:                  2000,
		ReplyMergeConfidencePercent:    defaultReplyMergeConfidencePercent,
		MaxReplyChars:                  3500,
		ReplyMaxBubbles:                replyMaxChatBubbles,
		ForwardReplyChunkThreshold:     0,
		ForwardReplyEnabled:            boolPointer(true),
		DirectReplyChunkSize:           chatReplyChunkSize,
		ForwardReplyThreshold:          defaultForwardReplyThreshold,
		RecallReplyMode:                RecallReplyModeOriginalForward,
		RefusalStrategy:                RefusalStrategySmart,
		LLMStreamingEnabled:            boolPointer(true),
		RecallReplyAutoDeleteEnabled:   boolPointer(false),
		RecallReplyTTLSeconds:          defaultRecallReplyTTLSeconds,
		LLMIdentityMaskingEnabled:      boolPointer(true),
		BotReplyLoopDetectionEnabled:   boolPointer(true),
		ReplyRefusalSuppressionEnabled: boolPointer(true),
		ReplySafetyMasterEnabled:       boolPointer(true),
		TelegramSuppressBotMessages:    boolPointer(true),
		QQTypingEnabled:                boolPointer(true),
		NotebookSharedScopeEnabled:     boolPointer(true),
		RecentHistoryTokenBudget:       DefaultRecentHistoryTokenBudget,
		// 40 而不是 20：这个上限只管路由、指代消解和记忆门控这些旁路的回看深度，
		// 不进正式提示词。20 条在稍热闹一点的群里就不够被指代的消息留在窗口里，
		// 而这些调用的单条开销很小，放宽的代价远小于解不出指代的代价。
		RecentContextLimit:          40,
		HistoryBackfillMessageLimit: 3,
		ContextSummaryThreshold:     100,
		CrossGroupMemoryEnabled:     boolPointer(false),
		CrossPlatformMemoryEnabled:  boolPointer(false),
		WorldBookEnabled:            boolPointer(true),
		SelfNoteEnabled:             boolPointer(false),
		RomanceEnabled:              boolPointer(false),
		MoodEnabled:                 boolPointer(false),
		PokeReplyEnabled:            boolPointer(false),
		ExpressionLearningEnabled:   boolPointer(false),
		DictSegmentEnabled:          boolPointer(false),
		SemanticSearchEnabled:       boolPointer(false),
		ProactiveReplyChance:        defaultProactiveReplyChance,
		ProactiveReplyThreshold:     defaultProactiveReplyThreshold,
		ReplyRules:                  []ReplyRule{},
		MaxBotConcurrency:           8,
		RequestTimeout:              180 * time.Second,
		AgentEnabled:                true,
		// 新建的机器人默认标准模式，安全模式由主人在设置里明确选择。
		// 存量机器人不受影响，它们的模式由 migrateAgentMode 按旧开关换算。
		AgentMode:       AgentModeStandard,
		AgentMaxSteps:   agent.DefaultMaxSteps,
		AgentSkillRoots: []string{},
		// 新建配置直接带上一组只读诊断命令，装完就能用。
		//
		// 只影响新建：WithDefaults 对白名单只做清洗、不回填，对写入开关根本不碰，
		// 所以已经在跑的部署升级后不会凭空多出这两项能力——当初特意留空的仍然是空的。
		// 这两件事必须分开：默认值是给新用户的便利，不该变成对存量部署的静默扩权。
		AgentCommandAllowlist: agent.DefaultCommandAllowlist(),
		AgentCommandTimeoutMS: agent.DefaultCommandTimeoutMS,
		AgentCommandSandbox:   agent.CommandSandboxAuto,
		// 写入锁在数据目录下的 workspace 里，碰不到配置和数据库，所以默认打开。
		AgentFileWriteEnabled: true,
		AgentBrowserCDPURL:    defaultAgentBrowserCDPURL,
		AgentBrowserTimeoutMS: agent.DefaultBrowserTimeoutMS,
	}
}

// WithDefaults 补齐 OneBot v11 机器人配置默认值。
func (cfg BotConfig) WithDefaults() BotConfig {
	cfg.ReplyMergeConfidencePercent = normalizeReplyMergeConfidencePercent(cfg.ReplyMergeConfidencePercent)
	cfg.Participation = copyParticipation(cfg.Participation)
	defaults := DefaultBotConfig()
	hasResponseMode := strings.TrimSpace(string(cfg.ResponseMode)) != ""
	// WithDefaults 会补齐运行所需的安全默认值，同时清理重复触发词/禁用群。
	cfg.Name = NormalizeProfileName(cfg.Name)
	cfg.Platform = NormalizePlatformID(cfg.Platform)
	if cfg.OneBotTransport == "" {
		cfg.OneBotTransport = OneBotTransportReverseWS
	}
	if strings.TrimSpace(cfg.OneBotReverseWSEndpoint) == "" {
		cfg.OneBotReverseWSEndpoint = defaults.OneBotReverseWSEndpoint
	}
	if strings.TrimSpace(cfg.NoneBotBridgeEndpoint) == "" {
		cfg.NoneBotBridgeEndpoint = defaults.NoneBotBridgeEndpoint
	}
	if len(cfg.GroupTriggers) == 0 {
		cfg.GroupTriggers = append([]string(nil), defaults.GroupTriggers...)
	}
	if cfg.DisabledGroups == nil {
		cfg.DisabledGroups = append([]string(nil), defaults.DisabledGroups...)
	}
	cfg = cfg.migrateDisabledUsers()
	cfg.GroupAdmission = cfg.GroupAdmission.WithDefaults()
	cfg.PrivateAdmission = cfg.PrivateAdmission.WithDefaults()
	if cfg.ReplyGate != nil {
		normalized := cfg.ReplyGate.WithDefaults()
		cfg.ReplyGate = &normalized
	}
	if strings.TrimSpace(cfg.SystemPrompt) == "" {
		cfg.SystemPrompt = defaults.SystemPrompt
	} else {
		cfg.SystemPrompt = strings.TrimSpace(cfg.SystemPrompt)
	}
	if hasResponseMode {
		cfg.ResponseMode = cfg.ResponseMode.Normalized()
	} else {
		// Existing installations may already have hand-tuned chat-in values.
		cfg.ResponseMode = ResponseModeCustom
	}
	cfg.SystemPrompt = migratePersonaStyle(cfg.SystemPrompt, &cfg.ReplyStyle, &cfg.ActionDescriptionEnabled)
	// 旧版的品格层、自称、语气词、动作描写并进 SOUL.md 后清空，见 persona_legacy.go。
	cfg.SystemPrompt = foldLegacyPersona(cfg.SystemPrompt, cfg.Soul.Normalized(), cfg.SelfReference, cfg.SentenceEnders, cfg.ActionDescriptionEnabled)
	cfg.Soul = nil
	cfg.PersonaMode = ""
	cfg.ActionDescriptionEnabled = nil
	cfg.SelfReference = ""
	cfg.SentenceEnders = ""
	cfg.PromptOverrides = normalizePromptOverrides(cfg.PromptOverrides)
	if cfg.ChatInEnabled == nil {
		cfg.ChatInEnabled = defaults.ChatInEnabled
	}
	if !cfg.ChatInLevel.Valid() {
		cfg.ChatInLevel = defaults.ChatInLevel
	} else {
		cfg.ChatInLevel = cfg.ChatInLevel.Normalized()
	}
	cfg.ChatInThreshold = clampChatInRatio(cfg.ChatInThreshold)
	cfg.ChatInChance = clampChatInRatio(cfg.ChatInChance)
	if cfg.ChatInCooldownSeconds < 0 {
		cfg.ChatInCooldownSeconds = 0
	}
	if cfg.NaturalInterjectionEnabled == nil {
		cfg.NaturalInterjectionEnabled = copyBoolPointer(defaults.NaturalInterjectionEnabled)
	}
	if hasResponseMode {
		cfg.ResponseMode.apply(&cfg)
	}
	if strings.TrimSpace(cfg.WelcomeMessage) == "" {
		cfg.WelcomeMessage = defaults.WelcomeMessage
	}
	cfg.WelcomeMode = normalizeWelcomeMode(cfg.WelcomeMode)
	cfg.WelcomeTemplates = cleanStrings(cfg.WelcomeTemplates)
	if cfg.WelcomeLLMCooldownSeconds <= 0 {
		cfg.WelcomeLLMCooldownSeconds = defaults.WelcomeLLMCooldownSeconds
	}
	if strings.TrimSpace(cfg.ErrorReplyPrefix) == "" {
		cfg.ErrorReplyPrefix = defaults.ErrorReplyPrefix
	}
	if cfg.SendRetryAttempts <= 0 {
		cfg.SendRetryAttempts = defaults.SendRetryAttempts
	}
	if cfg.SendRetryAttempts > 5 {
		cfg.SendRetryAttempts = 5
	}
	cfg.sendRetrySettings = cfg.sendRetrySettings.clamped().withFallback(defaultSendRetrySettings())
	if cfg.ReplyReferenceMode == "" {
		cfg.ReplyReferenceMode = defaults.ReplyReferenceMode
	}
	cfg.ModelDisclosure = normalizeModelDisclosure(cfg.ModelDisclosure)
	cfg.RepositoryDisclosure = normalizeRepositoryDisclosure(cfg.RepositoryDisclosure)
	if cfg.MentionUserMode == "" {
		cfg.MentionUserMode = defaults.MentionUserMode
	}
	if cfg.SendChunkIntervalMS <= 0 {
		cfg.SendChunkIntervalMS = defaults.SendChunkIntervalMS
	}
	if cfg.SendChunkIntervalMS > 5000 {
		cfg.SendChunkIntervalMS = 5000
	}
	cfg.TypingDelayPerCharMS = max(0, min(maxTypingDelayPerCharMS, cfg.TypingDelayPerCharMS))
	// 0 表示没配过，用默认；负数是明显的错值，同样退回默认。想「第一声再见就
	// 不回」的人把它设成 1，那是配置的自由，不是这里该纠正的。
	if cfg.PrivateClosingGrace < 0 {
		cfg.PrivateClosingGrace = 0
	}
	if cfg.PrivateClosingGrace == 0 {
		cfg.PrivateClosingGrace = defaults.PrivateClosingGrace
	}
	if cfg.InboundGroupConcurrency <= 0 {
		cfg.InboundGroupConcurrency = defaults.InboundGroupConcurrency
	}
	if cfg.InboundGroupConcurrency > maxInboundSessionConcurrency {
		cfg.InboundGroupConcurrency = maxInboundSessionConcurrency
	}
	if cfg.InboundPrivateConcurrency <= 0 {
		cfg.InboundPrivateConcurrency = defaults.InboundPrivateConcurrency
	}
	if cfg.InboundPrivateConcurrency > maxInboundSessionConcurrency {
		cfg.InboundPrivateConcurrency = maxInboundSessionConcurrency
	}
	if cfg.MaxInputChars <= 0 {
		cfg.MaxInputChars = defaults.MaxInputChars
	}
	if cfg.MaxReplyChars <= 0 {
		cfg.MaxReplyChars = defaults.MaxReplyChars
	}
	if cfg.DirectReplyChunkSize <= 0 {
		cfg.DirectReplyChunkSize = defaults.DirectReplyChunkSize
	}
	if cfg.ReplyMaxBubbles <= 0 {
		cfg.ReplyMaxBubbles = defaults.ReplyMaxBubbles
	}
	// 两个合并转发阈值上 0 是「关掉这条触发」，不是「没填」：这里不能回落到
	// 默认值，否则用户清空输入框就被默认值顶回去，关不掉。新建配置的默认值由
	// DefaultBotConfig 给，群级覆盖同样只做钳零。
	cfg.ForwardReplyChunkThreshold = max(0, cfg.ForwardReplyChunkThreshold)
	cfg.ForwardReplyThreshold = max(0, cfg.ForwardReplyThreshold)
	// 旧配置没有总开关：两个阈值都是 0 的本来就不会出卡片，记成关闭；填过阈值的
	// 记成开启。这样升级后行为不变，开关显示的也是实际状态。
	if cfg.ForwardReplyEnabled == nil {
		cfg.ForwardReplyEnabled = boolPointer(cfg.ForwardReplyThreshold > 0 || cfg.ForwardReplyChunkThreshold > 0)
	}
	cfg.RecallReplyMode = normalizeRecallReplyMode(cfg.RecallReplyMode)
	cfg.RefusalStrategy = normalizeRefusalStrategy(cfg.RefusalStrategy)
	if cfg.LLMStreamingEnabled == nil {
		cfg.LLMStreamingEnabled = copyBoolPointer(defaults.LLMStreamingEnabled)
	}
	if cfg.RecallReplyAutoDeleteEnabled == nil {
		cfg.RecallReplyAutoDeleteEnabled = copyBoolPointer(defaults.RecallReplyAutoDeleteEnabled)
	}
	if cfg.RecallReplyTTLSeconds <= 0 {
		cfg.RecallReplyTTLSeconds = defaults.RecallReplyTTLSeconds
	} else if cfg.RecallReplyTTLSeconds > maximumRecallReplyTTLSeconds {
		cfg.RecallReplyTTLSeconds = maximumRecallReplyTTLSeconds
	}
	if cfg.LLMIdentityMaskingEnabled == nil {
		cfg.LLMIdentityMaskingEnabled = boolPointer(true)
	}
	if cfg.ReplySafetyMasterEnabled == nil {
		cfg.ReplySafetyMasterEnabled = boolPointer(true)
	}
	if cfg.NotebookSharedScopeEnabled == nil {
		cfg.NotebookSharedScopeEnabled = boolPointer(true)
	}
	if cfg.BotReplyLoopDetectionEnabled == nil {
		cfg.BotReplyLoopDetectionEnabled = boolPointer(true)
	}
	if cfg.ReplyRefusalSuppressionEnabled == nil {
		cfg.ReplyRefusalSuppressionEnabled = boolPointer(true)
	}
	if cfg.TelegramSuppressBotMessages == nil {
		cfg.TelegramSuppressBotMessages = boolPointer(true)
	}
	if cfg.QQTypingEnabled == nil {
		cfg.QQTypingEnabled = boolPointer(true)
	}
	if cfg.MaxContextTokens < 0 {
		cfg.MaxContextTokens = 0
	}
	if cfg.RecentHistoryTokenBudget < 0 {
		cfg.RecentHistoryTokenBudget = 0
	}
	if cfg.RecentContextLimit < 0 {
		cfg.RecentContextLimit = defaults.RecentContextLimit
	}
	if cfg.HistoryBackfillMessageLimit <= 0 {
		cfg.HistoryBackfillMessageLimit = defaults.HistoryBackfillMessageLimit
	}
	if cfg.HistoryBackfillMessageLimit > 100 {
		cfg.HistoryBackfillMessageLimit = 100
	}
	if cfg.ContextSummaryThreshold <= 0 {
		cfg.ContextSummaryThreshold = defaults.ContextSummaryThreshold
	}
	cfg.ContextSummaryThreshold = contextSummaryTriggerThreshold(cfg.RecentContextLimit, cfg.ContextSummaryThreshold)
	if cfg.CrossGroupMemoryEnabled == nil {
		cfg.CrossGroupMemoryEnabled = boolPointer(false)
	}
	if cfg.CrossPlatformMemoryEnabled == nil {
		cfg.CrossPlatformMemoryEnabled = boolPointer(false)
	}
	if cfg.WorldBookEnabled == nil {
		cfg.WorldBookEnabled = boolPointer(true)
	}
	if cfg.SelfNoteEnabled == nil {
		cfg.SelfNoteEnabled = boolPointer(false)
	}
	if cfg.RomanceEnabled == nil {
		cfg.RomanceEnabled = boolPointer(false)
	}
	if cfg.MoodEnabled == nil {
		cfg.MoodEnabled = boolPointer(false)
	}
	if cfg.PokeReplyEnabled == nil {
		cfg.PokeReplyEnabled = boolPointer(false)
	}
	if cfg.ExpressionLearningEnabled == nil {
		cfg.ExpressionLearningEnabled = boolPointer(false)
	}
	if cfg.DictSegmentEnabled == nil {
		cfg.DictSegmentEnabled = boolPointer(false)
	}
	if cfg.SemanticSearchEnabled == nil {
		cfg.SemanticSearchEnabled = boolPointer(false)
	}
	if cfg.ProactiveReplyChance <= 0 {
		cfg.ProactiveReplyChance = defaults.ProactiveReplyChance
	}
	if cfg.ProactiveReplyChance > 1 {
		cfg.ProactiveReplyChance = 1
	}
	if cfg.ProactiveReplyThreshold <= 0 {
		cfg.ProactiveReplyThreshold = defaults.ProactiveReplyThreshold
	}
	if cfg.ProactiveReplyThreshold > 1 {
		cfg.ProactiveReplyThreshold = 1
	}
	if cfg.MaxBotConcurrency <= 0 {
		cfg.MaxBotConcurrency = defaults.MaxBotConcurrency
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaults.RequestTimeout
	}
	// 空值留空：那表示还没迁移过，由加载路径按旧开关换算，这里不替它做决定。
	cfg.AgentMode = NormalizeAgentMode(cfg.AgentMode)
	if cfg.AgentMaxSteps <= 0 {
		cfg.AgentMaxSteps = defaults.AgentMaxSteps
	}
	if cfg.AgentMaxSteps > agent.MaxAllowedSteps {
		// Agent 步数硬上限防止模型陷入长循环工具调用。
		cfg.AgentMaxSteps = agent.MaxAllowedSteps
	}
	if cfg.AgentCommandTimeoutMS <= 0 {
		cfg.AgentCommandTimeoutMS = defaults.AgentCommandTimeoutMS
	}
	if cfg.AgentCommandTimeoutMS > agent.MaxAllowedCommandTimeoutMS {
		cfg.AgentCommandTimeoutMS = agent.MaxAllowedCommandTimeoutMS
	}
	// 未知值按 auto 处理而不是静默关掉：配置写错不该变成「沙盒没了」。
	cfg.AgentCommandSandbox = agent.NormalizeCommandSandboxMode(cfg.AgentCommandSandbox)
	if cfg.AgentBrowserTimeoutMS <= 0 {
		cfg.AgentBrowserTimeoutMS = defaults.AgentBrowserTimeoutMS
	}
	if cfg.AgentBrowserTimeoutMS > agent.MaxAllowedBrowserTimeoutMS {
		cfg.AgentBrowserTimeoutMS = agent.MaxAllowedBrowserTimeoutMS
	}
	if strings.TrimSpace(cfg.AgentBrowserCDPURL) == "" {
		cfg.AgentBrowserCDPURL = defaults.AgentBrowserCDPURL
	}
	agentDefaults := agent.Config{
		WorkDir:       AgentWorkspaceDir(),
		SkillRoots:    cfg.AgentSkillRoots,
		MCPConfigPath: cfg.AgentMCPConfigPath,
	}.WithDefaults()
	cfg.AgentSkillRoots = cleanStrings(agentDefaults.SkillRoots)
	cfg.AgentMCPConfigPath = agentDefaults.MCPConfigPath
	cfg.AgentCommandAllowlist = cleanStrings(cfg.AgentCommandAllowlist)
	cfg.GroupTriggers = cleanStrings(cfg.GroupTriggers)
	cfg.DisabledGroups = cleanStrings(cfg.DisabledGroups)
	cfg.MarkedBotIDs = cleanStrings(append([]string(nil), cfg.MarkedBotIDs...))
	cfg.ReplyRules = normalizeReplyRules(cfg.ReplyRules)
	cfg.ModelRoles = normalizeModelRoles(cfg.ModelRoles)
	return cfg
}

// Validate 校验 OneBot v11 机器人配置是否可运行。
func (cfg BotConfig) Validate() error {
	if len(cfg.WelcomeTemplates) > 50 {
		return fmt.Errorf("欢迎词模板最多 50 条")
	}
	for _, template := range cfg.WelcomeTemplates {
		if len([]rune(template)) > 200 {
			return fmt.Errorf("欢迎词模板不能超过 200 字")
		}
	}
	if cfg.WelcomeLLMCooldownSeconds < 0 || cfg.WelcomeLLMCooldownSeconds > 24*60*60 {
		return fmt.Errorf("欢迎词 LLM 冷却必须在 0 到 86400 秒之间")
	}
	if criteria := strings.TrimSpace(cfg.ProactiveReplyExtraCriteria); len([]rune(criteria)) > routerCriteriaMaxRunes {
		return fmt.Errorf("主动回复补充判据不能超过 %d 字", routerCriteriaMaxRunes)
	}
	if err := validatePromptOverrides(cfg.PromptOverrides); err != nil {
		return err
	}
	if cfg.OneBotTransport == "" {
		cfg.OneBotTransport = OneBotTransportReverseWS
	}
	if err := ValidatePlatform(cfg.Platform); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ConnectionProfileID) != "" {
		if !IsOneBotPlatform(cfg.Platform) {
			return fmt.Errorf("只有 OneBot 机器人支持复用连接")
		}
		return nil
	}
	// 每个平台的必填凭据都不一样，按平台分支校验。这里不能写成「不是 OneBot
	// 就当 Telegram」——新增平台后那种写法会拿 Telegram 的规则去校验飞书。
	switch NormalizePlatformID(cfg.Platform) {
	case PlatformTelegram:
		if cfg.Enabled && strings.TrimSpace(cfg.TelegramBotToken) == "" {
			return ErrMissingTelegramToken
		}
		if base := strings.TrimSpace(cfg.TelegramAPIBaseURL); base != "" {
			if !isHTTPURL(base) {
				return ErrInvalidTelegramAPIBase
			}
		}
		return nil
	case PlatformQQOfficial:
		if cfg.Enabled && (strings.TrimSpace(cfg.QQAppID) == "" || strings.TrimSpace(cfg.QQAppSecret) == "") {
			return ErrMissingQQCredentials
		}
		return nil
	case PlatformDingTalk:
		if cfg.Enabled && (strings.TrimSpace(cfg.DingTalkClientID) == "" || strings.TrimSpace(cfg.DingTalkClientSecret) == "") {
			return ErrMissingDingTalkCredentials
		}
		return nil
	case PlatformFeishu:
		if cfg.Enabled && (strings.TrimSpace(cfg.FeishuAppID) == "" || strings.TrimSpace(cfg.FeishuAppSecret) == "") {
			return ErrMissingFeishuCredentials
		}
		if base := strings.TrimSpace(cfg.FeishuAPIBaseURL); base != "" {
			if !isHTTPURL(base) {
				return ErrInvalidFeishuAPIBase
			}
		}
		return nil
	case PlatformWeCom:
		if !cfg.Enabled {
			return nil
		}
		if strings.TrimSpace(cfg.WeComCorpID) == "" || strings.TrimSpace(cfg.WeComAgentID) == "" || strings.TrimSpace(cfg.WeComSecret) == "" {
			return ErrMissingWeComCredentials
		}
		if _, err := strconv.Atoi(strings.TrimSpace(cfg.WeComAgentID)); err != nil {
			return ErrInvalidWeComAgentID
		}
		// 企业微信只能靠回调收消息，缺了验签凭据就是「只能发不能收」，
		// 这种半可用状态不如直接拦下来说清楚。
		if strings.TrimSpace(cfg.WeComToken) == "" || strings.TrimSpace(cfg.WeComEncodingAESKey) == "" {
			return ErrMissingWeComCallbackKeys
		}
		return nil
	}

	if cfg.OneBotTransport == OneBotTransportHTTP {
		if !isHTTPURL(cfg.OneBotHTTPURL) {
			return fmt.Errorf("OneBot HTTP API 地址必须是 http:// 或 https:// URL")
		}
		if cfg.Enabled && strings.TrimSpace(cfg.OneBotHTTPSecret) == "" {
			return fmt.Errorf("OneBot HTTP 事件上报需要配置签名密钥")
		}
		return nil
	}
	if cfg.OneBotTransport != OneBotTransportReverseWS && cfg.OneBotTransport != OneBotTransportForwardWS {
		return fmt.Errorf("未知 OneBot 连接方式: %s", cfg.OneBotTransport)
	}
	// 反向 WS 的 token 不是「可选」：server 侧 token 为空会拒绝一切握手
	//（reason=server_token_unset），保存成启用的空 token 配置等于让机器人静默
	// 掉线。正向 WS / HTTP 是 Diana 主动外连，token 发不发由接入端决定，
	// 留空合法，不做限制。
	if cfg.OneBotTransport == OneBotTransportReverseWS && cfg.Enabled && strings.TrimSpace(cfg.OneBotAccessToken) == "" {
		return fmt.Errorf("OneBot 反向 WebSocket 必须配置 Access Token，且需与接入端填写的 token 一致")
	}
	endpoint := strings.TrimSpace(cfg.OneBotReverseWSEndpoint)
	if cfg.OneBotTransport == OneBotTransportForwardWS {
		endpoint = strings.TrimSpace(cfg.OneBotWSEndpoint)
	}
	if cfg.Enabled && endpoint == "" {
		return ErrMissingOneBotEndpoint
	}
	if endpoint != "" {
		parsed, err := url.Parse(endpoint)
		if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" {
			return ErrInvalidOneBotEndpoint
		}
	}
	return nil
}

// isHTTPURL 判断是不是一个带主机名的 http(s) 地址。
func isHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

// maskSecretPreview 把密钥渲染成「前两位…后两位」的掩码预览：只够辨认是不是
// 自己刚填的那一个，又不泄露多少凭据。太短的 token 一律不露。
// 只用于展示，回传明文预览等同于泄露凭据。
func maskSecretPreview(value string) string {
	key := []rune(strings.TrimSpace(value))
	if len(key) == 0 {
		return ""
	}
	if len(key) < 5 {
		return "••••"
	}
	return string(key[:2]) + "…" + string(key[len(key)-2:])
}

// PayloadFromConfig 把内部机器人配置转换为前端安全 payload。
func PayloadFromConfig(cfg BotConfig) ConfigPayload {
	cfg = cfg.WithDefaults()
	// token 只返回 configured 标志，不把保存的密钥明文暴露给普通配置接口。
	return ConfigPayload{
		ConnectionProfileID:         cfg.ConnectionProfileID,
		ID:                          cfg.ID,
		Name:                        cfg.Name,
		Platform:                    cfg.Platform,
		AvatarURL:                   cfg.AvatarURL,
		Enabled:                     cfg.Enabled,
		OneBotTransport:             cfg.OneBotTransport,
		OneBotWSEndpoint:            cfg.OneBotWSEndpoint,
		OneBotHTTPURL:               cfg.OneBotHTTPURL,
		OneBotHTTPSecretConfigured:  cfg.OneBotHTTPSecret != "",
		OneBotReverseWSEndpoint:     cfg.OneBotReverseWSEndpoint,
		OneBotAccessTokenConfigured: cfg.OneBotAccessToken != "",
		// 只回传掩码预览（前几位 + 后几位），与 LLM API Key 的 api_key_preview
		// 同一约定：方便用户核对配置的是哪一个 token，又不泄露完整凭据。
		OneBotAccessTokenPreview:    maskSecretPreview(cfg.OneBotAccessToken),
		TelegramBotTokenConfigured:  cfg.TelegramBotToken != "",
		TelegramAPIBaseURL:          cfg.TelegramAPIBaseURL,
		TelegramProxyURL:            cfg.TelegramProxyURL,
		TelegramSuppressBotMessages: copyBoolPointer(cfg.TelegramSuppressBotMessages),
		QQTypingEnabled:             copyBoolPointer(cfg.QQTypingEnabled),
		// 密钥一律只回 configured 标志或掩码预览（见 OneBotAccessTokenPreview），
		// 不回明文。AppID/CorpID 这类公开标识可以回显，
		// 方便用户核对填的是不是同一个应用。
		QQAppID:                           cfg.QQAppID,
		QQAppSecretConfigured:             cfg.QQAppSecret != "",
		QQSandbox:                         cfg.QQSandbox,
		DingTalkClientID:                  cfg.DingTalkClientID,
		DingTalkClientSecretConfigured:    cfg.DingTalkClientSecret != "",
		DingTalkRobotCode:                 cfg.DingTalkRobotCode,
		FeishuAppID:                       cfg.FeishuAppID,
		FeishuAppSecretConfigured:         cfg.FeishuAppSecret != "",
		FeishuVerificationTokenConfigured: cfg.FeishuVerificationToken != "",
		FeishuEncryptKeyConfigured:        cfg.FeishuEncryptKey != "",
		FeishuAPIBaseURL:                  cfg.FeishuAPIBaseURL,
		WeComCorpID:                       cfg.WeComCorpID,
		WeComAgentID:                      cfg.WeComAgentID,
		WeComSecretConfigured:             cfg.WeComSecret != "",
		WeComTokenConfigured:              cfg.WeComToken != "",
		WeComEncodingAESKeyConfigured:     cfg.WeComEncodingAESKey != "",
		CallbackPath:                      CallbackPathFor(cfg.Platform),
		NoneBotBridgeEnabled:              cfg.NoneBotBridgeEnabled,
		NoneBotBridgeEndpoint:             cfg.NoneBotBridgeEndpoint,
		NoneBotBridgeTokenConfigured:      cfg.NoneBotBridgeToken != "",
		BotAccount:                        cfg.BotAccount,
		OwnerID:                           cfg.OwnerID,
		OwnerLoginEnabled:                 cfg.OwnerLoginEnabled,
		OwnerLLMConfigEnabled:             copyBoolPointer(cfg.OwnerLLMConfigEnabled),
		GroupTriggers:                     append([]string(nil), cfg.GroupTriggers...),
		GroupTriggerMode:                  cfg.GroupTriggerMode,
		DisabledGroups:                    append([]string(nil), cfg.DisabledGroups...),
		DisabledUsers:                     append([]string(nil), cfg.DisabledUsers...),
		MarkedBotIDs:                      append([]string(nil), cfg.MarkedBotIDs...),
		GroupAdmission:                    cfg.GroupAdmission.WithDefaults(),
		PrivateAdmission:                  cfg.PrivateAdmission.WithDefaults(),
		ReplyGate:                         cfg.ReplyGate.Clone(),
		WelcomeEnabled:                    cfg.WelcomeEnabled,
		WelcomeMessage:                    cfg.WelcomeMessage,
		WelcomeMode:                       cfg.WelcomeMode,
		WelcomeTemplates:                  append([]string(nil), cfg.WelcomeTemplates...),
		WelcomeLLMCooldownSeconds:         cfg.WelcomeLLMCooldownSeconds,
		SystemPrompt:                      cfg.SystemPrompt,
		ResponseMode:                      cfg.ResponseMode,
		ReplyStyle:                        cfg.ReplyStyle,
		DebugModeEnabled:                  copyBoolPointer(cfg.DebugModeEnabled),
		ReplyReferenceMode:                cfg.ReplyReferenceMode,
		ModelDisclosure:                   cfg.ModelDisclosure,
		RepositoryDisclosure:              cfg.RepositoryDisclosure,
		MentionUserMode:                   cfg.MentionUserMode,
		MarkdownToPlain:                   copyBoolPointer(cfg.MarkdownToPlain),
		ErrorNotifyEnabled:                copyBoolPointer(cfg.ErrorNotifyEnabled),
		MutedReplyPauseEnabled:            copyBoolPointer(cfg.MutedReplyPauseEnabled),
		MutedVoiceTranscriptionEnabled:    copyBoolPointer(cfg.MutedVoiceTranscriptionEnabled),
		MutedImageDescriptionEnabled:      copyBoolPointer(cfg.MutedImageDescriptionEnabled),
		MutedReplyJudgmentEnabled:         copyBoolPointer(cfg.MutedReplyJudgmentEnabled),
		ErrorPersonaReplyEnabled:          copyBoolPointer(cfg.ErrorPersonaReplyEnabled),
		ErrorReplyPrefix:                  cfg.ErrorReplyPrefix,
		SendRetryAttempts:                 cfg.SendRetryAttempts,
		sendRetrySettings:                 cfg.sendRetrySettings,
		SendChunkIntervalMS:               cfg.SendChunkIntervalMS,
		PrivateClosingGrace:               cfg.PrivateClosingGrace,
		InboundGroupConcurrency:           cfg.InboundGroupConcurrency,
		InboundPrivateConcurrency:         cfg.InboundPrivateConcurrency,
		PromptInjectTime:                  copyBoolPointer(cfg.PromptInjectTime),
		PromptInjectPlaintextRules:        copyBoolPointer(cfg.PromptInjectPlaintextRules),
		PromptInjectGroupSender:           copyBoolPointer(cfg.PromptInjectGroupSender),
		PromptChineseSlangHint:            copyBoolPointer(cfg.PromptChineseSlangHint),
		AutoImageDescription:              copyBoolPointer(cfg.AutoImageDescription),
		AutoVideoPreprocess:               copyBoolPointer(cfg.AutoVideoPreprocess),
		ModelRoles:                        normalizeModelRoles(cfg.ModelRoles),
		BotReplyLoopDetectionEnabled:      copyBoolPointer(cfg.BotReplyLoopDetectionEnabled),
		ReplyRefusalSuppressionEnabled:    copyBoolPointer(cfg.ReplyRefusalSuppressionEnabled),
		ReplySafetyMasterEnabled:          copyBoolPointer(cfg.ReplySafetyMasterEnabled),
		ReplyAccountSafetyAuditPrompt:     strings.TrimSpace(cfg.ReplyAccountSafetyAuditPrompt),
		NotebookSharedScopeEnabled:        copyBoolPointer(cfg.NotebookSharedScopeEnabled),
		ProactiveReplyExtraCriteria:       cfg.ProactiveReplyExtraCriteria,
		PromptOverrides:                   normalizePromptOverrides(cfg.PromptOverrides),
		MaxInputChars:                     cfg.MaxInputChars,
		MaxReplyChars:                     cfg.MaxReplyChars,
		NaturalReplySplitEnabled:          copyBoolPointer(cfg.NaturalReplySplitEnabled),
		ReplyMergeConfidencePercent:       cfg.ReplyMergeConfidencePercent,
		ReplyPreserveLineBreaks:           copyBoolPointer(cfg.ReplyPreserveLineBreaks),
		ReplyLineSplitEnabled:             copyBoolPointer(cfg.ReplyLineSplitEnabled),
		TypingDelayEnabled:                copyBoolPointer(cfg.TypingDelayEnabled),
		TypingDelayPerCharMS:              cfg.TypingDelayPerCharMS,
		SocialReplyEnabled:                copyBoolPointer(cfg.SocialReplyEnabled),
		ReplyMaxBubbles:                   cfg.ReplyMaxBubbles,
		ForwardReplyChunkThreshold:        cfg.ForwardReplyChunkThreshold,
		ForwardReplyEnabled:               copyBoolPointer(cfg.ForwardReplyEnabled),
		DirectReplyChunkSize:              cfg.DirectReplyChunkSize,
		ForwardReplyThreshold:             cfg.ForwardReplyThreshold,
		RecallReplyMode:                   cfg.RecallReplyMode,
		RefusalStrategy:                   cfg.RefusalStrategy,
		LLMStreamingEnabled:               copyBoolPointer(cfg.LLMStreamingEnabled),
		RecallReplyAutoDeleteEnabled:      copyBoolPointer(cfg.RecallReplyAutoDeleteEnabled),
		RecallReplyTTLSeconds:             cfg.RecallReplyTTLSeconds,
		LLMIdentityMaskingEnabled:         copyBoolPointer(cfg.LLMIdentityMaskingEnabled),
		MaxContextTokens:                  cfg.MaxContextTokens,
		RecentHistoryTokenBudget:          cfg.RecentHistoryTokenBudget,
		RecentContextLimit:                cfg.RecentContextLimit,
		HistoryBackfillMessageLimit:       cfg.HistoryBackfillMessageLimit,
		ContextSummaryThreshold:           cfg.ContextSummaryThreshold,
		CrossGroupMemoryEnabled:           copyBoolPointer(cfg.CrossGroupMemoryEnabled),
		CrossPlatformMemoryEnabled:        copyBoolPointer(cfg.CrossPlatformMemoryEnabled),
		WorldBookEnabled:                  copyBoolPointer(cfg.WorldBookEnabled),
		SelfNoteEnabled:                   copyBoolPointer(cfg.SelfNoteEnabled),
		RomanceEnabled:                    copyBoolPointer(cfg.RomanceEnabled),
		MoodEnabled:                       copyBoolPointer(cfg.MoodEnabled),
		PokeReplyEnabled:                  copyBoolPointer(cfg.PokeReplyEnabled),
		ExpressionLearningEnabled:         copyBoolPointer(cfg.ExpressionLearningEnabled),
		DictSegmentEnabled:                copyBoolPointer(cfg.DictSegmentEnabled),
		SemanticSearchEnabled:             copyBoolPointer(cfg.SemanticSearchEnabled),
		ProactiveReplyChance:              cfg.ProactiveReplyChance,
		ProactiveReplyThreshold:           cfg.ProactiveReplyThreshold,
		ChatInEnabled:                     copyBoolPointer(cfg.ChatInEnabled),
		ChatInLevel:                       cfg.ChatInLevel,
		Participation:                     copyParticipation(cfg.Participation),
		ChatInThreshold:                   cfg.ChatInThreshold,
		ChatInChance:                      cfg.ChatInChance,
		ChatInCooldownSeconds:             cfg.ChatInCooldownSeconds,
		NaturalInterjectionEnabled:        copyBoolPointer(cfg.NaturalInterjectionEnabled),
		ReplyRules:                        append([]ReplyRule(nil), cfg.ReplyRules...),
		MaxBotConcurrency:                 cfg.MaxBotConcurrency,
		RequestTimeoutMS:                  cfg.RequestTimeout.Milliseconds(),
		AgentEnabled:                      cfg.AgentEnabled,
		AgentMode:                         cfg.AgentMode,
		AgentMaxSteps:                     cfg.AgentMaxSteps,
		AgentSkillRoots:                   append([]string(nil), cfg.AgentSkillRoots...),
		AgentMCPConfigPath:                cfg.AgentMCPConfigPath,
		AgentCommandAllowlist:             append([]string(nil), cfg.AgentCommandAllowlist...),
		AgentCommandTimeoutMS:             cfg.AgentCommandTimeoutMS,
		AgentCommandSandbox:               cfg.AgentCommandSandbox,
		AgentCommandSandboxAllowNetwork:   cfg.AgentCommandSandboxAllowNetwork,
		AgentFileWriteEnabled:             cfg.AgentFileWriteEnabled,
		AgentBrowserCDPURL:                cfg.AgentBrowserCDPURL,
		AgentBrowserTimeoutMS:             cfg.AgentBrowserTimeoutMS,
		AgentBrowserControlEnabled:        cfg.AgentBrowserControlEnabled,
		AgentBrowserBoxDisabled:           cfg.AgentBrowserBoxDisabled,
	}
}

// PayloadFromConfigWithSecrets 在 PayloadFromConfig 之上带回真实 token。
// 常规配置接口每次打开页面都会拉,凭据跟着到处跑没必要;但控制台的主人本来
// 就有权改这些 token,不给看反而只能去翻配置文件。所以做成显式索取:
// 调用方带上 include_secrets 才返回,和 LLM API Key 那套一致。
func PayloadFromConfigWithSecrets(cfg BotConfig) ConfigPayload {
	payload := PayloadFromConfig(cfg)
	cfg = cfg.WithDefaults()
	payload.OneBotHTTPSecret = cfg.OneBotHTTPSecret
	payload.OneBotAccessToken = cfg.OneBotAccessToken
	payload.TelegramBotToken = cfg.TelegramBotToken
	payload.NoneBotBridgeToken = cfg.NoneBotBridgeToken
	payload.QQAppSecret = cfg.QQAppSecret
	payload.DingTalkClientSecret = cfg.DingTalkClientSecret
	payload.FeishuAppSecret = cfg.FeishuAppSecret
	payload.FeishuVerificationToken = cfg.FeishuVerificationToken
	payload.FeishuEncryptKey = cfg.FeishuEncryptKey
	payload.WeComSecret = cfg.WeComSecret
	payload.WeComToken = cfg.WeComToken
	payload.WeComEncodingAESKey = cfg.WeComEncodingAESKey
	return payload
}

// PayloadFromProfileSet 把机器人配置集转换为前端可直接消费的 payload。顶层字段是
// focusID 指的那台机器人（这次请求操作的那台）；focusID 为空或找不到时是第一台。
func PayloadFromProfileSet(set ProfileSet, focusID string) ConfigPayload {
	return payloadFromProfileSet(set, focusID, PayloadFromConfig)
}

// PayloadFromProfileSetWithSecrets 与 PayloadFromProfileSet 相同,但带回真实 token。
func PayloadFromProfileSetWithSecrets(set ProfileSet, focusID string) ConfigPayload {
	return payloadFromProfileSet(set, focusID, PayloadFromConfigWithSecrets)
}

func payloadFromProfileSet(set ProfileSet, focusID string, convert func(BotConfig) ConfigPayload) ConfigPayload {
	set = set.WithDefaults()
	if len(set.Profiles) == 0 {
		return ConfigPayload{}
	}
	focus := set.Profiles[0]
	if profile, ok := set.ConfigForProfile(focusID); ok {
		focus = profile
	}
	payload := convert(focus)
	payload.MessageRelays = append([]MessageRelayPair(nil), set.MessageRelays...)
	payload.Profiles = make([]ConfigPayload, 0, len(set.Profiles))
	for _, profile := range set.Profiles {
		payload.Profiles = append(payload.Profiles, convert(profile))
	}
	return payload
}

// ConfigFromPayload 把前端 payload 合并旧密钥后转为内部配置。
func ConfigFromPayload(payload ConfigPayload, existing BotConfig) BotConfig {
	cfg := BotConfig{
		ConnectionProfileID:         strings.TrimSpace(payload.ConnectionProfileID),
		ID:                          strings.TrimSpace(payload.ID),
		Name:                        payload.Name,
		Platform:                    payload.Platform,
		AvatarURL:                   strings.TrimSpace(payload.AvatarURL),
		Enabled:                     payload.Enabled,
		OneBotReverseWSEndpoint:     payload.OneBotReverseWSEndpoint,
		OneBotTransport:             payload.OneBotTransport,
		OneBotWSEndpoint:            payload.OneBotWSEndpoint,
		OneBotHTTPURL:               payload.OneBotHTTPURL,
		OneBotHTTPSecret:            payload.OneBotHTTPSecret,
		OneBotAccessToken:           payload.OneBotAccessToken,
		TelegramBotToken:            payload.TelegramBotToken,
		TelegramAPIBaseURL:          payload.TelegramAPIBaseURL,
		TelegramProxyURL:            payload.TelegramProxyURL,
		TelegramSuppressBotMessages: copyBoolPointer(payload.TelegramSuppressBotMessages),
		QQTypingEnabled:             copyBoolPointer(payload.QQTypingEnabled),
		QQAppID:                     payload.QQAppID,
		QQAppSecret:                 payload.QQAppSecret,
		QQSandbox:                   payload.QQSandbox,
		DingTalkClientID:            payload.DingTalkClientID,
		DingTalkClientSecret:        payload.DingTalkClientSecret,
		DingTalkRobotCode:           payload.DingTalkRobotCode,
		FeishuAppID:                 payload.FeishuAppID,
		FeishuAppSecret:             payload.FeishuAppSecret,
		FeishuVerificationToken:     payload.FeishuVerificationToken,
		FeishuEncryptKey:            payload.FeishuEncryptKey,
		FeishuAPIBaseURL:            payload.FeishuAPIBaseURL,
		WeComCorpID:                 payload.WeComCorpID,
		WeComAgentID:                payload.WeComAgentID,
		WeComSecret:                 payload.WeComSecret,
		WeComToken:                  payload.WeComToken,
		WeComEncodingAESKey:         payload.WeComEncodingAESKey,
		NoneBotBridgeEnabled:        payload.NoneBotBridgeEnabled,
		NoneBotBridgeEndpoint:       payload.NoneBotBridgeEndpoint,
		NoneBotBridgeToken:          payload.NoneBotBridgeToken,
		BotAccount:                  payload.BotAccount,
		OwnerID:                     payload.OwnerID,
		OwnerLoginEnabled:           payload.OwnerLoginEnabled,
		OwnerLLMConfigEnabled:       copyBoolPointer(payload.OwnerLLMConfigEnabled),
		GroupTriggers:               payload.GroupTriggers,
		GroupTriggerMode:            payload.GroupTriggerMode,
		DisabledGroups:              payload.DisabledGroups,
		DisabledUsers:               payload.DisabledUsers,
		MarkedBotIDs:                append([]string(nil), payload.MarkedBotIDs...),
		// 逐群开关只认群配置：接口传上来的白名单一律丢掉，老客户端也写不回来。
		GroupAdmission:                  GroupAdmission{Mode: payload.GroupAdmission.Mode}.WithDefaults(),
		PrivateAdmission:                payload.PrivateAdmission.WithDefaults(),
		ReplyGate:                       payload.ReplyGate.Clone(),
		WelcomeEnabled:                  payload.WelcomeEnabled,
		WelcomeMessage:                  payload.WelcomeMessage,
		WelcomeMode:                     payload.WelcomeMode,
		WelcomeTemplates:                append([]string(nil), payload.WelcomeTemplates...),
		WelcomeLLMCooldownSeconds:       payload.WelcomeLLMCooldownSeconds,
		SystemPrompt:                    payload.SystemPrompt,
		ResponseMode:                    payload.ResponseMode,
		ReplyStyle:                      payload.ReplyStyle,
		DebugModeEnabled:                copyBoolPointer(payload.DebugModeEnabled),
		ReplyReferenceMode:              payload.ReplyReferenceMode,
		ModelDisclosure:                 payload.ModelDisclosure,
		RepositoryDisclosure:            payload.RepositoryDisclosure,
		MentionUserMode:                 payload.MentionUserMode,
		MarkdownToPlain:                 copyBoolPointer(payload.MarkdownToPlain),
		ErrorNotifyEnabled:              copyBoolPointer(payload.ErrorNotifyEnabled),
		MutedReplyPauseEnabled:          copyBoolPointer(payload.MutedReplyPauseEnabled),
		MutedVoiceTranscriptionEnabled:  copyBoolPointer(payload.MutedVoiceTranscriptionEnabled),
		MutedImageDescriptionEnabled:    copyBoolPointer(payload.MutedImageDescriptionEnabled),
		MutedReplyJudgmentEnabled:       copyBoolPointer(payload.MutedReplyJudgmentEnabled),
		ErrorPersonaReplyEnabled:        copyBoolPointer(payload.ErrorPersonaReplyEnabled),
		ErrorReplyPrefix:                payload.ErrorReplyPrefix,
		SendRetryAttempts:               payload.SendRetryAttempts,
		sendRetrySettings:               payload.sendRetrySettings,
		SendChunkIntervalMS:             payload.SendChunkIntervalMS,
		PrivateClosingGrace:             payload.PrivateClosingGrace,
		InboundGroupConcurrency:         payload.InboundGroupConcurrency,
		InboundPrivateConcurrency:       payload.InboundPrivateConcurrency,
		PromptInjectTime:                copyBoolPointer(payload.PromptInjectTime),
		PromptInjectPlaintextRules:      copyBoolPointer(payload.PromptInjectPlaintextRules),
		PromptInjectGroupSender:         copyBoolPointer(payload.PromptInjectGroupSender),
		PromptChineseSlangHint:          copyBoolPointer(payload.PromptChineseSlangHint),
		AutoImageDescription:            copyBoolPointer(firstNonNilBoolPointer(payload.AutoImageDescription, existing.AutoImageDescription)),
		AutoVideoPreprocess:             copyBoolPointer(firstNonNilBoolPointer(payload.AutoVideoPreprocess, existing.AutoVideoPreprocess)),
		ModelRoles:                      normalizeModelRoles(payload.ModelRoles),
		BotReplyLoopDetectionEnabled:    copyBoolPointer(payload.BotReplyLoopDetectionEnabled),
		ReplyRefusalSuppressionEnabled:  copyBoolPointer(payload.ReplyRefusalSuppressionEnabled),
		ReplySafetyMasterEnabled:        copyBoolPointer(payload.ReplySafetyMasterEnabled),
		ReplyAccountSafetyAuditPrompt:   strings.TrimSpace(payload.ReplyAccountSafetyAuditPrompt),
		NotebookSharedScopeEnabled:      copyBoolPointer(payload.NotebookSharedScopeEnabled),
		ProactiveReplyExtraCriteria:     strings.TrimSpace(payload.ProactiveReplyExtraCriteria),
		PromptOverrides:                 normalizePromptOverrides(payload.PromptOverrides),
		MaxInputChars:                   payload.MaxInputChars,
		MaxReplyChars:                   payload.MaxReplyChars,
		NaturalReplySplitEnabled:        copyBoolPointer(payload.NaturalReplySplitEnabled),
		ReplyMergeConfidencePercent:     payload.ReplyMergeConfidencePercent,
		ReplyPreserveLineBreaks:         copyBoolPointer(payload.ReplyPreserveLineBreaks),
		ReplyLineSplitEnabled:           copyBoolPointer(payload.ReplyLineSplitEnabled),
		TypingDelayEnabled:              copyBoolPointer(payload.TypingDelayEnabled),
		TypingDelayPerCharMS:            payload.TypingDelayPerCharMS,
		SocialReplyEnabled:              copyBoolPointer(payload.SocialReplyEnabled),
		ReplyMaxBubbles:                 payload.ReplyMaxBubbles,
		ForwardReplyChunkThreshold:      payload.ForwardReplyChunkThreshold,
		ForwardReplyEnabled:             copyBoolPointer(payload.ForwardReplyEnabled),
		DirectReplyChunkSize:            payload.DirectReplyChunkSize,
		ForwardReplyThreshold:           payload.ForwardReplyThreshold,
		RecallReplyMode:                 payload.RecallReplyMode,
		RefusalStrategy:                 payload.RefusalStrategy,
		LLMStreamingEnabled:             copyBoolPointer(payload.LLMStreamingEnabled),
		RecallReplyAutoDeleteEnabled:    copyBoolPointer(payload.RecallReplyAutoDeleteEnabled),
		RecallReplyTTLSeconds:           payload.RecallReplyTTLSeconds,
		LLMIdentityMaskingEnabled:       copyBoolPointer(payload.LLMIdentityMaskingEnabled),
		MaxContextTokens:                payload.MaxContextTokens,
		RecentHistoryTokenBudget:        payload.RecentHistoryTokenBudget,
		RecentContextLimit:              payload.RecentContextLimit,
		HistoryBackfillMessageLimit:     payload.HistoryBackfillMessageLimit,
		ContextSummaryThreshold:         payload.ContextSummaryThreshold,
		CrossGroupMemoryEnabled:         copyBoolPointer(payload.CrossGroupMemoryEnabled),
		CrossPlatformMemoryEnabled:      copyBoolPointer(payload.CrossPlatformMemoryEnabled),
		WorldBookEnabled:                copyBoolPointer(payload.WorldBookEnabled),
		SelfNoteEnabled:                 copyBoolPointer(payload.SelfNoteEnabled),
		RomanceEnabled:                  copyBoolPointer(payload.RomanceEnabled),
		MoodEnabled:                     copyBoolPointer(payload.MoodEnabled),
		PokeReplyEnabled:                copyBoolPointer(payload.PokeReplyEnabled),
		ExpressionLearningEnabled:       copyBoolPointer(payload.ExpressionLearningEnabled),
		DictSegmentEnabled:              copyBoolPointer(payload.DictSegmentEnabled),
		SemanticSearchEnabled:           copyBoolPointer(payload.SemanticSearchEnabled),
		ProactiveReplyChance:            payload.ProactiveReplyChance,
		ProactiveReplyThreshold:         payload.ProactiveReplyThreshold,
		ChatInEnabled:                   copyBoolPointer(payload.ChatInEnabled),
		ChatInLevel:                     payload.ChatInLevel,
		Participation:                   copyParticipation(payload.Participation),
		ChatInThreshold:                 payload.ChatInThreshold,
		ChatInChance:                    payload.ChatInChance,
		ChatInCooldownSeconds:           payload.ChatInCooldownSeconds,
		NaturalInterjectionEnabled:      copyBoolPointer(payload.NaturalInterjectionEnabled),
		ReplyRules:                      append([]ReplyRule(nil), payload.ReplyRules...),
		MaxBotConcurrency:               payload.MaxBotConcurrency,
		RequestTimeout:                  time.Duration(payload.RequestTimeoutMS) * time.Millisecond,
		AgentEnabled:                    payload.AgentEnabled,
		AgentMode:                       agentModeFromPayload(payload, existing),
		AgentMaxSteps:                   payload.AgentMaxSteps,
		AgentSkillRoots:                 append([]string(nil), payload.AgentSkillRoots...),
		AgentMCPConfigPath:              payload.AgentMCPConfigPath,
		AgentCommandAllowlist:           append([]string(nil), payload.AgentCommandAllowlist...),
		AgentCommandTimeoutMS:           payload.AgentCommandTimeoutMS,
		AgentCommandSandbox:             payload.AgentCommandSandbox,
		AgentCommandSandboxAllowNetwork: payload.AgentCommandSandboxAllowNetwork,
		AgentFileWriteEnabled:           payload.AgentFileWriteEnabled,
		AgentBrowserCDPURL:              payload.AgentBrowserCDPURL,
		AgentBrowserTimeoutMS:           payload.AgentBrowserTimeoutMS,
		AgentBrowserControlEnabled:      payload.AgentBrowserControlEnabled,
		AgentBrowserBoxDisabled:         payload.AgentBrowserBoxDisabled,
	}.WithDefaults()
	if cfg.OneBotHTTPSecret == "" {
		cfg.OneBotHTTPSecret = existing.OneBotHTTPSecret
	}
	if cfg.OneBotAccessToken == "" {
		// 前端留空 token 表示沿用旧值，不表示删除鉴权。
		cfg.OneBotAccessToken = existing.OneBotAccessToken
	}
	if cfg.NoneBotBridgeToken == "" {
		// NoneBot bridge token 与 OneBot token 语义一致，也保留旧值。
		cfg.NoneBotBridgeToken = existing.NoneBotBridgeToken
	}
	if cfg.TelegramBotToken == "" {
		cfg.TelegramBotToken = existing.TelegramBotToken
	}
	// 新增平台的密钥同理：前端为了不回显明文会把这些字段留空，留空一律沿用旧值。
	// 漏掉任何一个,用户改个无关设置就会把凭据清空,机器人随之掉线。
	if cfg.QQAppSecret == "" {
		cfg.QQAppSecret = existing.QQAppSecret
	}
	if cfg.DingTalkClientSecret == "" {
		cfg.DingTalkClientSecret = existing.DingTalkClientSecret
	}
	if cfg.FeishuAppSecret == "" {
		cfg.FeishuAppSecret = existing.FeishuAppSecret
	}
	if cfg.FeishuVerificationToken == "" {
		cfg.FeishuVerificationToken = existing.FeishuVerificationToken
	}
	if cfg.FeishuEncryptKey == "" {
		cfg.FeishuEncryptKey = existing.FeishuEncryptKey
	}
	if cfg.WeComSecret == "" {
		cfg.WeComSecret = existing.WeComSecret
	}
	if cfg.WeComToken == "" {
		cfg.WeComToken = existing.WeComToken
	}
	if cfg.WeComEncodingAESKey == "" {
		cfg.WeComEncodingAESKey = existing.WeComEncodingAESKey
	}
	// 界面保存出来的配置一律是迁移过的：Agent 恒开，模式二选一。
	return migrateAgentMode(cfg)
}

// agentModeFromPayload 决定保存时用哪个模式。请求里写了就用请求的；没写时，编辑已有
// 机器人沿用它现在的模式（旧版前端只会带回 agent_enabled=true，不能因此把安全模式
// 悄悄升成标准模式）；新建的机器人按默认的标准模式，旧版前端新建时只带
// agent_enabled=true 也一样。config.yaml 播种要按旧开关换算，由调用方先把模式写进 payload。
func agentModeFromPayload(payload ConfigPayload, existing BotConfig) string {
	if mode := NormalizeAgentMode(payload.AgentMode); mode != "" {
		return mode
	}
	if strings.TrimSpace(existing.ID) != "" {
		if mode := NormalizeAgentMode(existing.AgentMode); mode != "" {
			return mode
		}
	}
	return AgentModeStandard
}

func normalizeReplyRules(rules []ReplyRule) []ReplyRule {
	out := make([]ReplyRule, 0, len(rules))
	seen := map[string]bool{}
	for _, rule := range rules {
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Prompt = strings.TrimSpace(rule.Prompt)
		rule.LLMProfileID = strings.TrimSpace(rule.LLMProfileID)
		if rule.Prompt == "" {
			continue
		}
		switch rule.Action {
		case ReplyRuleActionVoice, ReplyRuleActionModel:
		default:
			rule.Action = ReplyRuleActionModel
		}
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			rule.ID = uuid.NewString()[:8]
		}
		for seen[rule.ID] {
			rule.ID = uuid.NewString()[:8]
		}
		seen[rule.ID] = true
		if rule.Name == "" {
			rule.Name = "回复规则"
		}
		out = append(out, rule)
	}
	return out
}

// cleanStrings 清理字符串列表中的空值和重复项。
func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		// 配置保存时顺手去空白和去重，避免触发词列表被前端重复提交污染。
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// debugModeEnabled 默认开启：排查问题时最常见的死胡同就是「那条消息没有调试记录」，
// 事后已经补不回来。轨迹存在库外的文件里、按天过期，开着的代价只是磁盘。
func debugModeEnabled(cfg BotConfig) bool {
	return cfg.DebugModeEnabled == nil || *cfg.DebugModeEnabled
}

func boolPointer(value bool) *bool {
	return &value
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

// firstNonNilBoolPointer 取第一个非空指针，用于新旧配置键共存期间的回落读取。
func firstNonNilBoolPointer(values ...*bool) *bool {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func intPointer(value int) *int {
	return &value
}

func intValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func copyIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	return intPointer(*value)
}

func copyBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	return boolPointer(*value)
}

// PersonaMode 是 SOUL.md 之前「填空题 / 接管」两档的遗留字段，只在读旧配置时出现。
// 现在人设只有一份 SOUL.md，没有档位可选，WithDefaults 读到后直接清掉。
type PersonaMode string

const (
	defaultPromptChineseSlang = "中文聊天里常有谐音梗、音近字、故意错别字、拼音缩写和圈内称呼；回复前先按上下文理解用户真正想表达的梗，能接梗就自然接，不要把梗当错字生硬纠正，也不要过度解释。在闲聊、叙事、氛围描写和开放式表达中，可以遵循当前人设与用户要求，使用贴合语境的比喻、拟人、意象、节奏感和角色口吻，写出有画面感、有辨识度的句子；风格化表达必须带来新的观察、情绪、观点或笑点，不要只堆形容词、套用网感模板或为了文艺牺牲准确。事实、技术和操作说明仍以清楚准确为先。"
	// defaultPromptPlaintextRules 只管排版：聊天窗口不渲染 Markdown。
	// 「什么时候分成几条消息发」是投递机制，归 replySegmentationRule 这条内置规则，
	// 不放在这个可编辑文本框里——挂在用户文案上的开关，改一次就再也没人打开了。
	defaultPromptPlaintextRules      = "OneBot v11 消息不渲染 Markdown，默认按纯文本显示，不要使用 Markdown 语法，例如 **加粗**、# 标题、表格或代码围栏；需要列点时用简短中文句子或普通序号。消息边界和消息内部换行均使用运行时专用标记，正文不要输出真实换行符。"
	defaultPromptTimeTemplate        = "当前时间：{datetime} {weekday}"
	defaultPromptGroupSenderTemplate = "当前是 群聊，正在和你说话的是「{sender}」；历史消息以“昵称（用户 ID）: 内容”标注发言者，回复时不要把这个前缀带进去。群聊里尽量简短。"
	defaultPromptImageOnly           = "请分析这张图片，并直接回答用户关于图片的问题。"
	defaultPromptWakeOnly            = "对方只是叫了你一声（@ 你或者喊了你的名字），没说别的。这不是在问你在不在——别回「我在」「在呢」「怎么了」这类应答，那是接线员不是熟人。先看前面几条在聊什么：话没说完就接着说，刚才在闹就继续闹，对方像是要你注意某件事就说那件事。实在没有上文可接，就说一句有内容的短话——一句吐槽、一个反应、一个具体的问题都行，别只报到。不要复述这条规则，也不要解释自己为什么被叫。"
)

const defaultProactiveReplyPrompt = "本次回复已通过语义相关性与可回答性判断：只回应路由器选中的当前一轮。若存在【当前同轮补充消息】，必须结合【当前需要回复的消息】覆盖这一轮里的全部实质问题、要求和约束；最终给出一轮简洁完整的回答，需要分条时可以使用 " + notificationSplitMarker + "，不要遗漏前面补发的内容。不要回答轮外历史，不要总结全局上下文，不要解释来龙去脉。"

const (
	defaultProactiveReplyChance    = 1.0
	defaultProactiveReplyThreshold = 0.9
)

const (
	// defaultPrivateClosingGrace 是默认答完几轮告别就不再追加。2 来自
	// 「第一声再见还会接一句，第二声也接得住，第三声就只剩复读」。
	defaultPrivateClosingGrace = 2
	// 群 3、私聊 2。私聊以前是 1：那时私聊没有「并入正在生成的回复」这一层，并发只会让
	// 两条回复同时生成、互相看不见。现在私聊也合并，第二条消息得在第一条还在生成时
	// 就开始处理，才赶得上并进去；串行时它永远等到上一条发完，连发几句就回几遍。
	defaultInboundGroupConcurrency   = 3
	defaultInboundPrivateConcurrency = 2
	// maxRecurringFailureAlertThreshold 是周期订阅失败告警次数的上限：真到几百次的
	// 量级，订阅早就该当成坏了，而不是继续安静地重试。
	maxRecurringFailureAlertThreshold = 100
	// maxInboundSessionConcurrency 只挡明显的错值。同一会话真开到几十路并发，
	// 回复顺序和上下文都会乱成一团，不是配置该允许的范围。
	maxInboundSessionConcurrency = 16
)

const defaultProactiveReplyRouterPrompt = `你是群聊机器人 Diana 的 Intent Recognition（意图识别）模块。你的职责仅是判断 candidates 中是否存在需要回应或值得插话的消息，并选择目标；不要审核答案准确度，不要规划工具调用、工具参数或最终回答步骤。后续 Agent 会读取完整上下文、搜索或调用工具，候选答案生成后还有独立的发送前准确度审核。最多选择一条。默认保持沉默，但明确提问、求助、指派和继续追问不能因为当前不知道答案而被拦截。

必须遵守：
1. directed_at_bot 只有在当前消息从语义上明确承接、评价、纠正或继续追问机器人时才为 true；直接引用机器人是强证据，但纯确认、结束语或借引用转向别人仍不是需要回复的追问。
2. answerable 只作日志观察，不参与 should_reply。当前短上下文不足、术语陌生、需要搜索、需要工具或暂时不知道答案，都不能成为拦截明确请求的理由；正式 Agent 和发送前准确度审核负责处理。
3. 私人行程、未公开决定、个人偏好或意图等问题，如果明确向机器人提出，也应进入正式回复，由 Agent 如实说明限制；不得在 Intent Recognition 阶段直接吞掉。
4. 没有点名对象不等于不需要回复。面向全群提出的定义、解释、辨析或求助问题应使用 needs_response；不得仅因句子短、没有问号、没有 @、术语陌生或信息不足而拒绝。群友之间的反问、随口确认和接梗不属于 needs_response。
5. last_bot_message 是最近一条机器人消息。只有当前消息与该机器人回复存在清楚的语义承接时才用 bot_related。针对机器人答案的具体追问、纠正或反驳应优先回复；结束性确认、纯情绪反应和要求机器人停止回复的消息不需要再回。
6. 回复或 @ 其他群友、两个人之间的对话、普通闲聊、感叹、寒暄、分享和玩梗默认不回复。只有满足第 6.1 至 6.4 条时才可使用 chat_in：机器人此刻确实有一句有实质内容的话可说，插进去比沉默更好。除此之外，向机器人提出的独立请求仍按 needs_response 处理。
6.1 substantive 是 chat_in 唯一的内容闸门，判断对象是"机器人打算说的那句话"，不是"这条群消息像不像话题"。只有当机器人的插话能提供以下之一时才为 true：具体且可核实的事实或数据；对错误说法的明确纠正；群友正在找的具体信息、名称、做法或取舍建议；对已抛出的开放邀请（"有人知道吗""求推荐"）的实际回答；围绕上下文中可识别的话题补充具体新信息；顺着 recent_messages 或 last_bot_message 的明确话题轻松调侃、反问或接梗，并且能给出贴合上下文的新回应；用具体、新颖且贴合话题的比喻、拟人、意象、节奏或角色化短句，为当前话题增加新的观察、画面、情绪或笑点。风格化表达不要求包含可核实事实，但套话换皮、无关抒情、同义复述和形容词堆砌仍然 substantive=false。短语省略问号或谓语本身不能作为 substantive=false 的理由。群友说“你”或使用反问句式不代表在直接问机器人；例如机器人刚建议看离线小说，群友说“你不是最喜欢看小说吗”，应保持 directed_at_bot=false，并可按 chat_in 放行。短语若承接或重复 recent_messages 中尚未回答的公开问题，应视为该问题仍在等待回答并使用 needs_response，而不是降级为随机插话。
6.2 以下一律 substantive=false，无论话题多合适：附和与捧场（"确实""哈哈""我也是""太对了""笑死"）；把别人刚说过的话换个说法复述；纯表情、纯语气词、纯感叹；寒暄与客套；没有新增信息的泛泛感想和总结；硬凑的玩梗和强行接话；对别人生活、消费、外貌、选择的评价。宁可沉默也不要凑数。
6.3 即使 substantive=true，以下场景仍必须 should_reply=false：两人正在进行的私密或深入对话；争执、抱怨、情绪宣泄和寻求安慰；涉及群友隐私、健康、感情和收入的话题；有人已经在给出答案且不需要补充；机器人最近已经插过话而话题没有实质推进。
6.4 chat_in 的 directed_at_bot 必须为 false。若消息其实指向机器人，应归入 bot_related；answerable 仍只作观察，不是触发门槛。
7. 单独图片通常不回复。仅当机器人刚明确要求当前发送者提供图片，而且图片确实在完成该请求并仍需要机器人处理时，才可使用 bot_related；不能仅因 recent_image_count 大于零或图片紧邻机器人消息就回复。
8. should_reply=true 只允许三种情况：A）category=bot_related、directed_at_bot=true，且当前消息仍需要回应；B）category=needs_response，消息明确要求回应；C）category=chat_in、substantive=true，且满足第 6.1 至 6.4 条。不要在这里预测最终答案是否准确；三者同时成立时优先级为 bot_related、needs_response、chat_in。
9. candidates 是最近 15 秒内最多 3 条候选，按时间从早到晚排列。结合 user_id、文本、图片和上下文从语义上判断它们是否为同一轮表达；不能仅凭同一发送者或时间相邻就合并。用 turn_message_ids 返回目标所属同一轮的全部消息 ID，顺序必须与 candidates 一致，并且必须包含 target_message_id。连续补充的多个问题、约束、算式、图片与说明都属于同一轮，最终回复要覆盖整轮；“不是 X”“不要按 X 解释”“我的意思是 Y”这类后续句子通常是在收窄或纠正问题范围，只要仍能用稳定常识给出有价值回答，就保持 answerable=true，而不是因为排除一个方向就判为上下文不明。彼此独立的话题不要放进 turn_message_ids。若为同一轮，target_message_id 选择其中最后一条。若 last_bot_message 已实质回答同一内容，且候选没有新增问题、纠正或必须处理的信息，则 should_reply=false，禁止换一种说法重复回答。
10. confidence 表示对“这条消息是否需要回应”的置信度，不表示答案准确度。若多条独立消息都满足条件，只选价值最高的一轮。
11. requests_response 只描述发言者的诉求，与 should_reply 是两件事：这句话本身在要求得到回应（提问、指派任务、追问依据、要求继续）就为 true；只是附和、道谢、玩梗、闲聊，或者明确让机器人别再说话（“闭嘴”“不用回复”这类意思，不限于这些字面）则为 false。即使你最终判断不回复，也要如实填写它。
12. blocker 取值固定为 none、missing_context、no_capability、not_addressed、low_value。missing_context 和 no_capability 不能拦截 requests_response=true 的明确请求，正式回复会处理；not_addressed 和 low_value 仍可保持沉默。
13. 只输出单个合法 JSON 对象，不要解释、Markdown 或额外文本。字段固定为 should_reply（布尔值）、confidence（0 到 1）、category（needs_response、bot_related、chat_in 或 none）、target_message_id（字符串）、turn_message_ids（字符串数组）、directed_at_bot（布尔值）、answerable（布尔值）、substantive（布尔值）、requests_response（布尔值）、blocker（字符串）、reason（简短中文理由）。例如：{"should_reply":true,"confidence":0.96,"category":"needs_response","target_message_id":"125","turn_message_ids":["123","124","125"],"directed_at_bot":false,"answerable":true,"substantive":true,"requests_response":true,"blocker":"none","reason":"同一发送者连续补充了三个需要统一回答的问题"}；闲聊插话例如：{"should_reply":true,"confidence":0.91,"category":"chat_in","target_message_id":"131","turn_message_ids":["131"],"directed_at_bot":false,"answerable":true,"substantive":true,"requests_response":false,"blocker":"none","reason":"群友把两款机型的续航记反了，可以直接给出正确参数"}；不回复时例如：{"should_reply":false,"confidence":0.98,"category":"none","target_message_id":"","turn_message_ids":[],"directed_at_bot":false,"answerable":false,"substantive":false,"requests_response":false,"blocker":"low_value","reason":"只是互相附和，插话只能是没有新增信息的捧场"}。`

// migrateDisabledUsers 把遗留的 DisabledUsers 并进回复门禁的屏蔽名单。
//
// 以前「不回复某个人」有两套名单：老的 DisabledUsers（WebUI 早已不展示，而且只读主配置）
// 和按机器人的 ReplyGate.BlockedUsers（聊天里「屏蔽某人」写的就是它）。两套名单的生效
// 范围不一样：链接解析和插件入口只认前者，于是被聊天屏蔽的人发个链接照样有回复。现在只
// 剩屏蔽名单一处。主人和机器人自己的账号在老名单里从来不生效，迁移时同样跳过。
func (cfg BotConfig) migrateDisabledUsers() BotConfig {
	legacy := cleanStrings(cfg.DisabledUsers)
	cfg.DisabledUsers = []string{}
	blocked := make([]string, 0, len(legacy))
	for _, userID := range legacy {
		if userID != strings.TrimSpace(cfg.OwnerID) && userID != strings.TrimSpace(cfg.BotAccount) {
			blocked = append(blocked, userID)
		}
	}
	if len(blocked) == 0 {
		return cfg
	}
	var existing []string
	if cfg.ReplyGate != nil {
		existing = cfg.ReplyGate.BlockedUsers
	}
	cfg.ReplyGate = cfg.ReplyGate.WithBlockedUsers(unionStrings(existing, blocked))
	return cfg
}
