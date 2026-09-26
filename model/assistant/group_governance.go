// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/applog"
)

// 群规则防御：刷屏、违规词和退群审计。全部默认关闭，只在群配置里逐项打开。
//
// 规则是确定性的，不过模型：判刷屏靠时间窗计数，判广告靠关键词和正则。模型判得再准
// 也有延迟和成本，而刷屏最需要的是「第一时间撤掉」；误判的代价由阶梯处罚兜住——
// 第一次只警告，后面才禁言，且时长按次数递增。
//
// 处罚一律先确认机器人是本群管理员、对方不是管理员，和工具里的群管操作同一道关。

const (
	defaultSpamWindowSeconds  = 10
	defaultSpamMaxMessages    = 8
	defaultSpamMaxRepeats     = 4
	defaultStrikeResetMinutes = 24 * 60
	maxGovernanceKeywordRules = 200
	maxGovernanceRuleRunes    = 200
	maxGovernanceLadderSteps  = 10
	maxGovernanceWarningRunes = 300
	// 重连回填、积压重放进来的旧消息不再处罚：人早就不在刷了，这时禁言只会莫名其妙。
	governanceMaxEventAge = 2 * time.Minute

	governanceReasonKeyword = "keyword"
	governanceReasonFlood   = "flood"
	governanceReasonRepeat  = "repeat"

	// governanceRegexPrefix 开头的规则按正则匹配，其余按不区分大小写的子串匹配。
	governanceRegexPrefix = "re:"
)

var defaultPenaltyLadderSeconds = []int{600, 3600, 86400}

// GroupGovernance 是一个群的规则防御配置。零值全部关闭。
type GroupGovernance struct {
	// AntiSpamEnabled 开启刷屏检测：SpamWindowSeconds 秒内超过 SpamMaxMessages 条，
	// 或同一内容出现 SpamMaxRepeats 次，记一次违规。
	AntiSpamEnabled   bool `json:"anti_spam_enabled,omitempty"`
	SpamWindowSeconds int  `json:"spam_window_seconds,omitempty"`
	SpamMaxMessages   int  `json:"spam_max_messages,omitempty"`
	SpamMaxRepeats    int  `json:"spam_max_repeats,omitempty"`
	// SpamRecallEnabled 为 true 时把刷屏窗口里的消息一并撤回。
	SpamRecallEnabled bool `json:"spam_recall_enabled,omitempty"`
	// KeywordFilterEnabled 开启违规词拦截：命中的消息撤回并记一次违规。
	// 每条规则一行，re: 开头按正则，其余按不区分大小写的子串。
	KeywordFilterEnabled bool     `json:"keyword_filter_enabled,omitempty"`
	KeywordRules         []string `json:"keyword_rules,omitempty"`
	// PenaltyLadderSeconds 是第 2、3… 次违规的禁言秒数，超出的次数按最后一档。
	// 第一次违规只警告。留空用默认的 10 分钟、1 小时、1 天。
	PenaltyLadderSeconds []int `json:"penalty_ladder_seconds,omitempty"`
	// StrikeResetMinutes 是违规次数的有效期，距上次违规超过这么久就从头算。
	StrikeResetMinutes int `json:"strike_reset_minutes,omitempty"`
	// WarningMessage 是警告模板，可用 {nickname}{user_id}{reason}{penalty}{strike}。
	WarningMessage string `json:"warning_message,omitempty"`
	// MemberLeaveAuditEnabled 开启退群/踢人审计：有人离开本群时私聊通知主人。
	MemberLeaveAuditEnabled bool `json:"member_leave_audit_enabled,omitempty"`
}

// Normalized 补默认值并钳到合法范围。非法正则在这里静默丢掉，保存时的 Validate 负责报错。
func (g GroupGovernance) Normalized() GroupGovernance {
	if g.SpamWindowSeconds <= 0 {
		g.SpamWindowSeconds = defaultSpamWindowSeconds
	}
	g.SpamWindowSeconds = min(g.SpamWindowSeconds, 600)
	if g.SpamMaxMessages <= 0 {
		g.SpamMaxMessages = defaultSpamMaxMessages
	}
	g.SpamMaxMessages = max(2, min(g.SpamMaxMessages, 100))
	if g.SpamMaxRepeats <= 0 {
		g.SpamMaxRepeats = defaultSpamMaxRepeats
	}
	g.SpamMaxRepeats = max(2, min(g.SpamMaxRepeats, 100))
	if g.StrikeResetMinutes <= 0 {
		g.StrikeResetMinutes = defaultStrikeResetMinutes
	}
	g.StrikeResetMinutes = min(g.StrikeResetMinutes, 30*24*60)
	rules := make([]string, 0, len(g.KeywordRules))
	for _, rule := range cleanStrings(g.KeywordRules) {
		if _, err := compileGovernanceRule(rule); err == nil {
			rules = append(rules, rule)
		}
	}
	g.KeywordRules = rules
	ladder := make([]int, 0, len(g.PenaltyLadderSeconds))
	for _, seconds := range g.PenaltyLadderSeconds {
		if seconds > 0 {
			ladder = append(ladder, min(seconds, oneBotMaxMuteSeconds))
		}
	}
	g.PenaltyLadderSeconds = ladder
	g.WarningMessage = strings.TrimSpace(g.WarningMessage)
	return g
}

// Validate 在保存时把写错的规则挡回去，免得存进去的正则在运行时被悄悄丢掉。
func (g GroupGovernance) Validate() error {
	if len(g.KeywordRules) > maxGovernanceKeywordRules {
		return fmt.Errorf("违规词规则最多 %d 条", maxGovernanceKeywordRules)
	}
	for _, rule := range cleanStrings(g.KeywordRules) {
		if len([]rune(rule)) > maxGovernanceRuleRunes {
			return fmt.Errorf("单条违规词规则不能超过 %d 字", maxGovernanceRuleRunes)
		}
		if _, err := compileGovernanceRule(rule); err != nil {
			return fmt.Errorf("违规词规则 %q 不是合法的正则：%v", rule, err)
		}
	}
	if len(g.PenaltyLadderSeconds) > maxGovernanceLadderSteps {
		return fmt.Errorf("阶梯禁言最多 %d 档", maxGovernanceLadderSteps)
	}
	for _, seconds := range g.PenaltyLadderSeconds {
		if seconds < 0 || seconds > oneBotMaxMuteSeconds {
			return fmt.Errorf("阶梯禁言每档须在 0 到 %d 秒之间", oneBotMaxMuteSeconds)
		}
	}
	if len([]rune(strings.TrimSpace(g.WarningMessage))) > maxGovernanceWarningRunes {
		return fmt.Errorf("警告模板不能超过 %d 字", maxGovernanceWarningRunes)
	}
	return nil
}

func (g GroupGovernance) messageRulesActive() bool {
	return g.AntiSpamEnabled || (g.KeywordFilterEnabled && len(g.KeywordRules) > 0)
}

func (g GroupGovernance) ladder() []int {
	if len(g.PenaltyLadderSeconds) == 0 {
		return defaultPenaltyLadderSeconds
	}
	return g.PenaltyLadderSeconds
}

// penaltyForStrike 返回第 strike 次违规的禁言秒数，0 表示只警告。
func (g GroupGovernance) penaltyForStrike(strike int) int {
	if strike <= 1 {
		return 0
	}
	ladder := g.ladder()
	return ladder[min(strike-2, len(ladder)-1)]
}

var governanceRuleCache sync.Map // rule -> *regexp.Regexp

func compileGovernanceRule(rule string) (*regexp.Regexp, error) {
	if !strings.HasPrefix(rule, governanceRegexPrefix) {
		return nil, nil
	}
	if cached, ok := governanceRuleCache.Load(rule); ok {
		return cached.(*regexp.Regexp), nil
	}
	pattern := strings.TrimSpace(strings.TrimPrefix(rule, governanceRegexPrefix))
	if pattern == "" {
		return nil, fmt.Errorf("正则为空")
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	governanceRuleCache.Store(rule, compiled)
	return compiled, nil
}

// matchGovernanceKeyword 返回命中的那条规则，没命中返回空串。
func matchGovernanceKeyword(rules []string, text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lower := strings.ToLower(text)
	for _, rule := range rules {
		compiled, err := compileGovernanceRule(rule)
		if err != nil {
			continue
		}
		if compiled != nil {
			if compiled.MatchString(text) {
				return rule
			}
			continue
		}
		if strings.Contains(lower, strings.ToLower(rule)) {
			return rule
		}
	}
	return ""
}

// governanceTracker 记每个群成员最近的发言和违规次数，只在内存里：重启后从零开始
// 算，最坏是某人少挨一档禁言，不值得为此落库。自带锁，不受 Runtime.mu 保护。
type governanceTracker struct {
	mu      sync.Mutex
	members map[string]*governanceMemberState
}

type governanceMemberState struct {
	recent     []governanceMessage
	strikes    int
	lastStrike time.Time
	lastSeen   time.Time
}

type governanceMessage struct {
	at        time.Time
	text      string
	messageID string
}

type governanceVerdict struct {
	reason string
	rule   string
	strike int
	// recallIDs 是要撤回的消息，keyword 只有当前这条，刷屏是整个窗口。
	recallIDs []string
}

// observe 记下一条消息并判断是否违规。违规时清空窗口：同一波刷屏只罚一次，
// 不会因为后面几条还在窗口里就连着升档。
func (t *governanceTracker) observe(key string, now time.Time, text, messageID string, gov GroupGovernance) (governanceVerdict, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.members == nil {
		t.members = map[string]*governanceMemberState{}
	}
	if len(t.members) > 4096 {
		t.sweepLocked(now, gov)
	}
	state := t.members[key]
	if state == nil {
		state = &governanceMemberState{}
		t.members[key] = state
	}
	state.lastSeen = now

	verdict := governanceVerdict{}
	if gov.KeywordFilterEnabled {
		if rule := matchGovernanceKeyword(gov.KeywordRules, text); rule != "" {
			verdict = governanceVerdict{reason: governanceReasonKeyword, rule: rule, recallIDs: nonEmptyStrings([]string{messageID})}
		}
	}
	if verdict.reason == "" && gov.AntiSpamEnabled {
		window := time.Duration(gov.SpamWindowSeconds) * time.Second
		kept := state.recent[:0]
		for _, item := range state.recent {
			if now.Sub(item.at) <= window {
				kept = append(kept, item)
			}
		}
		normalized := strings.Join(strings.Fields(strings.ToLower(text)), " ")
		state.recent = append(kept, governanceMessage{at: now, text: normalized, messageID: messageID})
		repeats := 0
		for _, item := range state.recent {
			if normalized != "" && item.text == normalized {
				repeats++
			}
		}
		switch {
		case len(state.recent) > gov.SpamMaxMessages:
			verdict.reason = governanceReasonFlood
		case repeats >= gov.SpamMaxRepeats:
			verdict.reason = governanceReasonRepeat
		}
		if verdict.reason != "" {
			for i := len(state.recent) - 1; i >= 0; i-- {
				if id := state.recent[i].messageID; id != "" {
					verdict.recallIDs = append(verdict.recallIDs, id)
				}
			}
		}
	}
	if verdict.reason == "" {
		return verdict, false
	}
	if !state.lastStrike.IsZero() && now.Sub(state.lastStrike) > time.Duration(gov.StrikeResetMinutes)*time.Minute {
		state.strikes = 0
	}
	state.strikes++
	state.lastStrike = now
	state.recent = nil
	verdict.strike = state.strikes
	return verdict, true
}

func (t *governanceTracker) sweepLocked(now time.Time, gov GroupGovernance) {
	idle := time.Duration(gov.StrikeResetMinutes) * time.Minute
	for key, state := range t.members {
		if now.Sub(state.lastSeen) > idle && now.Sub(state.lastStrike) > idle {
			delete(t.members, key)
		}
	}
}

// groupGovernance 取这台机器人在这个群的规则防御配置。
func (r *Runtime) groupGovernance(event MessageEvent) (GroupGovernance, bool) {
	if r == nil || strings.TrimSpace(event.GroupID) == "" {
		return GroupGovernance{}, false
	}
	r.mu.RLock()
	store := r.groupConfigs
	r.mu.RUnlock()
	if store == nil {
		return GroupGovernance{}, false
	}
	cfg, ok := store.ConfigForGroup(strings.TrimSpace(event.ProfileID), event.GroupID)
	if !ok || cfg.Governance == nil {
		return GroupGovernance{}, false
	}
	return cfg.Governance.Normalized(), true
}

// enforceGroupGovernance 对一条群消息跑规则防御。返回 true 表示这条违规、已交给
// 处罚流程，调用方不再回复它——对着广告认真接话，等于帮它再刷一遍。
//
// 判定在当前协程里做（纯内存），撤回、禁言、警告这些要调平台接口的放到后台，
// 不拖慢入站。
func (r *Runtime) enforceGroupGovernance(ctx context.Context, event MessageEvent) bool {
	if event.Kind != EventKindGroup || strings.TrimSpace(event.GroupID) == "" || strings.TrimSpace(event.UserID) == "" {
		return false
	}
	gov, ok := r.groupGovernance(event)
	if !ok || !gov.messageRulesActive() {
		return false
	}
	now := r.clock()
	if event.Time > 0 && now.Sub(time.Unix(event.Time, 0)) > governanceMaxEventAge {
		return false
	}
	cfg := r.effectiveConfigForEvent(event)
	if cfg.IsOwnerEvent(event) || GroupRoleCanConfigure(NormalizeGroupRole(event.SenderRole)) {
		return false
	}
	text := strings.TrimSpace(PlainText(event.Segments))
	if text == "" {
		text = strings.TrimSpace(event.RawMessage)
	}
	key := strings.Join([]string{event.ProfileID, event.GroupID, event.UserID}, "|")
	verdict, violated := r.governance.observe(key, now, text, event.MessageID, gov)
	if !violated {
		return false
	}
	record := r.decisionEventRecord(event, "[规则防御]", "governance_blocked")
	record.Reason = "命中本群规则防御：" + governanceReasonLabel(verdict.reason)
	r.record(record)
	go func() {
		defer recoverGoroutinePanic("group_governance.penalty")
		penaltyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		r.applyGovernancePenalty(penaltyCtx, event, gov, verdict)
	}()
	return true
}

// applyGovernancePenalty 执行一次违规的处罚：撤回、禁言、警告。每一步失败都只记日志，
// 不影响后面几步——撤不掉也要警告，禁言失败也要让人知道自己违规了。
func (r *Runtime) applyGovernancePenalty(ctx context.Context, event MessageEvent, gov GroupGovernance, verdict governanceVerdict) {
	platform := r.currentPlatform(event)
	result := map[string]any{"reason": verdict.reason, "strike": verdict.strike, "platform": platform}
	if verdict.rule != "" {
		result["rule"] = verdict.rule
	}
	if _, err := r.botGroupRole(ctx, event, event.GroupID); err != nil {
		r.recordGovernanceAction(event, result, err)
		return
	}
	// 普通消息事件在 Telegram 上不带身份，得实时查一次；对管理员下手平台也会拒绝。
	if member, err := r.getGroupMemberInfoForEvent(ctx, event, event.GroupID, event.UserID); err == nil && GroupRoleCanConfigure(NormalizeGroupRole(member.Role)) {
		r.recordGovernanceAction(event, result, fmt.Errorf("对方是本群管理员，不处罚"))
		return
	}

	recallIDs := verdict.recallIDs
	if verdict.reason != governanceReasonKeyword && !gov.SpamRecallEnabled {
		recallIDs = nil
	}
	if len(recallIDs) > 0 && platformSupportsOperation(platform, platformOpRecallMessages) {
		recalled, failed := r.recallGroupMessages(ctx, event, recallIDs)
		result["recalled"] = len(recalled)
		if len(failed) > 0 {
			result["recall_failed"] = len(failed)
		}
	}

	duration := gov.penaltyForStrike(verdict.strike)
	if duration > 0 {
		duration = min(duration, platformMaxMuteSeconds(platform))
		if !platformSupportsOperation(platform, platformOpMute) {
			duration = 0
		} else if _, err := newDianaPlatformTool(r, event).dispatchModeration(ctx, platform, platformOpMute, event.GroupID, event.UserID, duration, false); err != nil {
			result["mute_error"] = err.Error()
			duration = 0
		} else {
			result["mute_seconds"] = duration
		}
	}

	warning := renderGovernanceWarning(gov, event, verdict, duration)
	if err := r.sendOutgoing(ctx, event, OutgoingMessage{GroupID: event.GroupID, Text: warning, MentionUserID: event.UserID}); err != nil {
		result["warning_error"] = err.Error()
	}
	r.recordGovernanceAction(event, result, nil)
}

func governanceReasonLabel(reason string) string {
	switch reason {
	case governanceReasonKeyword:
		return "消息命中了本群的违规内容规则"
	case governanceReasonFlood:
		return "发言太频繁了"
	case governanceReasonRepeat:
		return "请不要重复刷同一条内容"
	}
	return "违反了群规"
}

func renderGovernanceWarning(gov GroupGovernance, event MessageEvent, verdict governanceVerdict, muteSeconds int) string {
	penalty := "这次先提醒，再犯会被禁言"
	if muteSeconds > 0 {
		penalty = "已禁言" + governanceDurationLabel(muteSeconds)
	}
	template := gov.WarningMessage
	if template == "" {
		template = "{nickname}，{reason}。{penalty}。"
	}
	return strings.NewReplacer(
		"{nickname}", firstNonEmpty(strings.TrimSpace(event.SenderName), event.UserID),
		"{user_id}", event.UserID,
		"{reason}", governanceReasonLabel(verdict.reason),
		"{penalty}", penalty,
		"{strike}", fmt.Sprint(verdict.strike),
	).Replace(template)
}

func governanceDurationLabel(seconds int) string {
	switch {
	case seconds%86400 == 0:
		return fmt.Sprintf(" %d 天", seconds/86400)
	case seconds%3600 == 0:
		return fmt.Sprintf(" %d 小时", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf(" %d 分钟", seconds/60)
	}
	return fmt.Sprintf(" %d 秒", seconds)
}

func (r *Runtime) recordGovernanceAction(event MessageEvent, metadata map[string]any, callErr error) {
	writer := r.appLogWriter()
	if writer == nil {
		return
	}
	entry := applog.Entry{
		Kind:     applog.KindOperation,
		Level:    applog.LevelInfo,
		Action:   "group_governance",
		Message:  "群规则防御已处理一次违规",
		Actor:    oneBotEventActor(event),
		Target:   event.UserID,
		Metadata: metadata,
	}
	metadata["group_id"] = event.GroupID
	metadata["user_id"] = event.UserID
	metadata["profile_id"] = event.ProfileID
	metadata["message_id"] = event.MessageID
	if callErr != nil {
		entry.Kind = applog.KindError
		entry.Level = applog.LevelError
		entry.Message = "群规则防御未能处罚：" + callErr.Error()
	}
	logCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = writer.AppendLog(logCtx, entry)
}

// auditMemberLeave 在有人离开本群时私聊通知主人：谁走了、自己退的还是被谁踢的。
// 只看群配置里的开关，不受群准入和回复门槛约束——它不在群里说话。
func (r *Runtime) auditMemberLeave(ctx context.Context, event MessageEvent) {
	gov, ok := r.groupGovernance(event)
	if !ok || !gov.MemberLeaveAuditEnabled {
		return
	}
	cfg := r.effectiveConfigForEvent(event)
	ownerID := strings.TrimSpace(cfg.OwnerID)
	if ownerID == "" {
		return
	}
	notice := noticeSegmentData(event)
	subType := notice["sub_type"]
	operatorID := firstNonEmpty(strings.TrimSpace(event.OperatorID), notice["operator_id"])

	groupLabel := event.GroupID
	infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if info, err := r.getGroupInfoForEvent(infoCtx, event, event.GroupID); err == nil && strings.TrimSpace(info.GroupName) != "" {
		groupLabel = fmt.Sprintf("%s（%s）", strings.TrimSpace(info.GroupName), event.GroupID)
	} else if name := strings.TrimSpace(event.GroupName); name != "" {
		groupLabel = fmt.Sprintf("%s（%s）", name, event.GroupID)
	}
	cancel()
	member := event.UserID
	if name := strings.TrimSpace(event.SenderName); name != "" {
		member = fmt.Sprintf("%s（%s）", name, event.UserID)
	}
	how := "主动退群"
	switch {
	case subType == "kick_me":
		how = "机器人被移出了这个群"
	case subType == "kick" || (operatorID != "" && operatorID != event.UserID):
		how = "被移出"
		if operatorID != "" {
			how = "被 " + operatorID + " 移出"
		}
	}
	text := strings.Join([]string{"群成员变动", "群：" + groupLabel, "成员：" + member, "方式：" + how}, "\n")
	notifyEvent := MessageEvent{
		ProfileID: event.ProfileID, Platform: event.Platform, ContextNamespace: event.ContextNamespace,
		Kind: EventKindPrivate, SelfID: event.SelfID, UserID: ownerID, Time: r.clock().Unix(),
	}
	if err := r.sendNotification(ctx, notifyEvent, text); err != nil {
		log.Printf("diana member leave audit notification failed: group=%s user=%s: %v", event.GroupID, event.UserID, err)
	}
	r.record(EventRecord{
		At: r.clock(), Kind: event.Kind, Platform: event.Platform, ProfileID: event.ProfileID,
		UserID: event.UserID, GroupID: event.GroupID, MessageID: event.MessageID,
		Text: "[notice] group_decrease", Handled: true, Outcome: "member_leave_audited",
		Decision: "notified", Reason: "成员离群，已私聊通知主人",
	})
}
