// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SuInk/diana/model/llm"
	"github.com/google/uuid"
)

// 主动开口（试验性）：没人找她的时候，自己在群里或给主人先说一句。
//
// 其余的发言全是对某条消息的回应；这里补上「先开口」的那一半。做法参照几个主流项目
// （MaiBot 的主动思考、AstrBot 的 proactive_chat、ChatLuna 的空闲触发）共有的骨架：
//   - 按空闲时长触发，不按钟点：群里冷场 group_idle_minutes、主人私聊安静
//     owner_idle_hours 之后才考虑，带 ±10% 抖动，不会每次都卡在同一分钟。
//   - 没人接就退避：每多一次没人接，要等的空闲时长翻倍；群里一次没人接、主人那边
//     两次没人接就停，直到对方再说话。每个会话每天另有条数上限，夜间时段不发。
//   - 判断和生成在同一次调用里，模型可以选择不说（输出 [SKIP]）。AstrBot 那个插件
//     强制生成，最后满屏「大家在干嘛呀」，这是它的短板。
//   - 发送前复核：生成的十几秒里有人说话了，这条作废，交给正常回复链路。
//
// 冷场只认「本来在聊、后来停了」：群里冷场前几个小时得有人在说话，几天没人说话的群
// 不去叫醒。主人私聊不看这条，主人就是要她主动找。
//
// 机器人这边一开，所在的群全都参与；单个群在群管理里关掉，或者机器人没开时单独给
// 某个群打开（GroupConfig.ProactiveMessagingEnabled）。候选群从最近有人说话的会话里
// 找，冷场判断本来就要求冷场前有人在聊，没聊过的群不用去问平台。
//
// 群里开口前默认先联网找点新鲜事（proactive_news.go）：按群里平时聊的东西定一个
// 搜索词，搜最近几天的新闻，交给生成那一步当素材，用不上也可以不用。
//
// 发出去的消息带 Initiative 标记写进历史，退避、每日上限都从历史里数，进程重启不会
// 把计数清零再来一遍。

const (
	defaultProactiveGroupIdleMinutes = 120
	defaultProactiveOwnerIdleHours   = 8
	defaultProactiveDailyLimit       = 2
	defaultProactiveQuietStart       = "23:00"
	defaultProactiveQuietEnd         = "08:00"

	// proactiveGroupMaxUnanswered / OwnerMaxUnanswered 是连着几次没人接就停。群里
	// 自说自话一次已经够尴尬；主人私聊可以再追一句，第二句也没回就别再打扰。
	proactiveGroupMaxUnanswered = 1
	proactiveOwnerMaxUnanswered = 2

	// proactiveWarmWindow 内至少 proactiveWarmMessages 条群友发言，才算「本来在聊」。
	proactiveWarmWindow   = 3 * time.Hour
	proactiveWarmMessages = 3
	// proactiveGroupStaleAfter 之后还没人说话的群不去叫醒：那已经不是冷场，是散了。
	proactiveGroupStaleAfter = 48 * time.Hour

	proactiveCheckInterval = 5 * time.Minute
	// proactiveStartupDelay 让启动后的第一轮晚一点：连接、历史回补都还没就绪时
	// 看到的「最后一条消息」不准。
	proactiveStartupDelay = 3 * time.Minute
	proactiveHistoryLimit = 200
	// proactivePromptHistoryLimit 是带给模型的最近消息条数，够看出之前在聊什么就行。
	proactivePromptHistoryLimit = 30
	proactiveRunTimeout         = 3 * time.Minute
	proactiveMaxReplyRunes      = 400

	proactiveSkipMarker = "[SKIP]"
)

// ProactiveMessaging 是一台机器人的主动开口设置。
type ProactiveMessaging struct {
	Enabled bool `json:"enabled"`
	// GroupIDs 是旧版的群名单（v0.9.8）：非空时只有名单里的群跟随机器人开，其余群
	// 要在群管理里单独打开。控制台清空它就变成所有群都开。
	GroupIDs []string `json:"group_ids,omitempty"`
	// SearchNews 让群里开口前先联网搜最近的新闻当话题；nil 按开启算。
	SearchNews *bool `json:"search_news,omitempty"`
	// GroupIdleMinutes 是群里安静多久之后考虑开口。
	GroupIdleMinutes int `json:"group_idle_minutes,omitempty"`
	// OwnerEnabled 打开后，主人私聊安静 OwnerIdleHours 小时会主动找主人。
	OwnerEnabled   bool `json:"owner_enabled,omitempty"`
	OwnerIdleHours int  `json:"owner_idle_hours,omitempty"`
	// DailyLimit 是每个会话每天最多主动开口几次。
	DailyLimit int `json:"daily_limit,omitempty"`
	// QuietStart / QuietEnd 是不主动开口的时段（HH:MM，按机器人时区），可以跨午夜；
	// 两个相同表示不设免打扰。
	QuietStart string `json:"quiet_start,omitempty"`
	QuietEnd   string `json:"quiet_end,omitempty"`
}

// Clone 深拷贝，nil 保持 nil。
func (p *ProactiveMessaging) Clone() *ProactiveMessaging {
	if p == nil {
		return nil
	}
	copied := *p
	copied.GroupIDs = append([]string(nil), p.GroupIDs...)
	copied.SearchNews = copyBoolPointer(p.SearchNews)
	return &copied
}

// normalized 补默认值、清理群号。nil 保持 nil：没配过就是没开。
func (p *ProactiveMessaging) normalized() *ProactiveMessaging {
	if p == nil {
		return nil
	}
	out := p.Clone()
	seen := map[string]bool{}
	groups := make([]string, 0, len(out.GroupIDs))
	for _, id := range out.GroupIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		groups = append(groups, id)
	}
	out.GroupIDs = groups
	if out.GroupIdleMinutes <= 0 {
		out.GroupIdleMinutes = defaultProactiveGroupIdleMinutes
	}
	if out.OwnerIdleHours <= 0 {
		out.OwnerIdleHours = defaultProactiveOwnerIdleHours
	}
	if out.DailyLimit <= 0 {
		out.DailyLimit = defaultProactiveDailyLimit
	}
	out.QuietStart = strings.TrimSpace(out.QuietStart)
	out.QuietEnd = strings.TrimSpace(out.QuietEnd)
	if out.QuietStart == "" && out.QuietEnd == "" {
		out.QuietStart, out.QuietEnd = defaultProactiveQuietStart, defaultProactiveQuietEnd
	}
	return out
}

func (p *ProactiveMessaging) validate() error {
	if p == nil {
		return nil
	}
	if p.GroupIdleMinutes != 0 && (p.GroupIdleMinutes < 30 || p.GroupIdleMinutes > 7*24*60) {
		return fmt.Errorf("主动开口的群冷场时长必须在 30 到 10080 分钟之间")
	}
	if p.OwnerIdleHours != 0 && (p.OwnerIdleHours < 1 || p.OwnerIdleHours > 14*24) {
		return fmt.Errorf("主动找主人的间隔必须在 1 到 336 小时之间")
	}
	if p.DailyLimit < 0 || p.DailyLimit > 10 {
		return fmt.Errorf("主动开口的每日上限必须在 1 到 10 次之间")
	}
	for _, value := range []string{p.QuietStart, p.QuietEnd} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, ok := parseClockMinutes(value); !ok {
			return fmt.Errorf("免打扰时段要写成 HH:MM，例如 23:00")
		}
	}
	if (strings.TrimSpace(p.QuietStart) == "") != (strings.TrimSpace(p.QuietEnd) == "") {
		return fmt.Errorf("免打扰时段的开始和结束要一起填")
	}
	if len(p.GroupIDs) > 200 {
		return fmt.Errorf("主动开口的群最多 200 个")
	}
	for _, id := range p.GroupIDs {
		if id = strings.TrimSpace(id); id != "" && validatePlatformTargetID(id) != nil {
			return fmt.Errorf("主动开口的群要填群号，不能填名称：%s", id)
		}
	}
	return nil
}

// proactiveQuietAt 报告 now 是否落在免打扰时段里。时段可以跨午夜（23:00–08:00）。
func proactiveQuietAt(p *ProactiveMessaging, now time.Time) bool {
	start, okStart := parseClockMinutes(p.QuietStart)
	end, okEnd := parseClockMinutes(p.QuietEnd)
	if !okStart || !okEnd || start == end {
		return false
	}
	minute := now.Hour()*60 + now.Minute()
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

type proactiveContextKey struct{}

// withProactiveInitiative 标记这次投递是主动开口，出站写历史时会带上编号。
func withProactiveInitiative(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, proactiveContextKey{}, id)
}

func proactiveInitiativeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(proactiveContextKey{}).(string)
	return strings.TrimSpace(id)
}

// proactiveMessagingState 是进程内的状态：哪些会话正在生成，以及每个会话上次问过
// 模型的时间。模型说了 [SKIP] 之后，至少再等一整段空闲时长才问下一次，不会每五分钟
// 烧一次 Token。重启后清空，最多多问一次。
type proactiveMessagingState struct {
	mu          sync.Mutex
	inflight    map[string]bool
	lastAttempt map[string]time.Time
	proactiveSessionCache
}

func (s *proactiveMessagingState) claim(key string, now time.Time, wait time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight == nil {
		s.inflight = map[string]bool{}
		s.lastAttempt = map[string]time.Time{}
	}
	if s.inflight[key] {
		return false
	}
	if last, ok := s.lastAttempt[key]; ok && now.Sub(last) < wait {
		return false
	}
	s.inflight[key] = true
	s.lastAttempt[key] = now
	return true
}

func (s *proactiveMessagingState) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, key)
}

// proactiveRules 是一个会话的触发规则。
type proactiveRules struct {
	Owner         bool
	Idle          time.Duration
	MaxUnanswered int
	DailyLimit    int
}

// proactivePlan 是判定可以开口时带给生成那一步的情况说明。
type proactivePlan struct {
	IdleFor    time.Duration
	Unanswered int
	// Required 是这一次要等够的空闲时长（已翻倍、已抖动），也是下次再问模型前的冷却。
	Required time.Duration
	// Watermark 是判定时最新一条消息的时间，发送前复核用。
	Watermark int64
}

// proactiveHumanEvent 判断一条历史是不是对方（群友或主人）说的话。
func proactiveHumanEvent(event MessageEvent, botID string) bool {
	if event.Time <= 0 || event.SenderIsBot || isNarrationHistoryEvent(event) {
		return false
	}
	return !assistantHistoryEvent(event, botID)
}

// evaluateProactive 是触发判断，只看历史和时间，不碰网络。返回 ok=false 时 reason
// 说明为什么这次不开口（只进调试日志，不逐轮写事件）。
func evaluateProactive(history []MessageEvent, botID string, rules proactiveRules, now time.Time, jitterKey string) (proactivePlan, string, bool) {
	var lastAny, lastHuman int64
	for _, event := range history {
		if event.Time > lastAny {
			lastAny = event.Time
		}
		if proactiveHumanEvent(event, botID) && event.Time > lastHuman {
			lastHuman = event.Time
		}
	}
	if lastHuman == 0 {
		return proactivePlan{}, "这个会话里还没人说过话", false
	}
	if !rules.Owner && now.Sub(time.Unix(lastHuman, 0)) > proactiveGroupStaleAfter {
		return proactivePlan{}, "群里很久没人说话了，不去叫醒", false
	}
	today := now.Format("2006-01-02")
	unanswered := map[string]bool{}
	todayCount := map[string]bool{}
	warm := 0
	for _, event := range history {
		if id := strings.TrimSpace(event.Initiative); id != "" && event.Outbound {
			if event.Time > lastHuman {
				unanswered[id] = true
			}
			if time.Unix(event.Time, 0).In(now.Location()).Format("2006-01-02") == today {
				todayCount[id] = true
			}
		}
		if proactiveHumanEvent(event, botID) && event.Time <= lastHuman && lastHuman-event.Time <= int64(proactiveWarmWindow/time.Second) {
			warm++
		}
	}
	if len(todayCount) >= rules.DailyLimit {
		return proactivePlan{}, "今天主动开口的次数用完了", false
	}
	maxUnanswered := proactiveGroupMaxUnanswered
	if rules.Owner {
		maxUnanswered = proactiveOwnerMaxUnanswered
	}
	if rules.MaxUnanswered > 0 {
		maxUnanswered = rules.MaxUnanswered
	}
	if len(unanswered) >= maxUnanswered {
		return proactivePlan{}, "上次主动说的话还没人接", false
	}
	// 群里只在「本来在聊、后来停了」时开口。已经主动说过一次、有人接了一句又停下的
	// 也算不上热闹，要重新聊起来才行。
	if !rules.Owner && warm < proactiveWarmMessages {
		return proactivePlan{}, "冷场前群里本来就没怎么聊", false
	}
	required := rules.Idle << len(unanswered)
	required = proactiveJitter(required, fmt.Sprintf("%s|%d", jitterKey, lastAny))
	idle := now.Sub(time.Unix(lastAny, 0))
	if idle < required {
		return proactivePlan{}, "还没安静够", false
	}
	return proactivePlan{IdleFor: idle, Unanswered: len(unanswered), Required: required, Watermark: lastAny}, "", true
}

// proactiveJitter 给等待时长加 ±10% 的抖动。按会话和最后一条消息取哈希，同一段冷场
// 每一轮算出来都一样，不会因为每五分钟重抽一次而实际只等最短的那个。
func proactiveJitter(base time.Duration, key string) time.Duration {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(key))
	fraction := float64(hasher.Sum32()%2001)/10000 - 0.1 // [-0.1, 0.1]
	return base + time.Duration(float64(base)*fraction)
}

func (r *Runtime) runProactiveMessagingLoop(ctx context.Context) {
	timer := time.NewTimer(proactiveStartupDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r.dispatchProactiveMessages(ctx)
		timer.Reset(proactiveCheckInterval)
	}
}

// proactiveEnabledConfigs 列出开了主动开口、也没被停用的机器人。机器人那边没开、
// 但有群单独打开的也算，只是只看那几个群。
func (r *Runtime) proactiveEnabledConfigs() []BotConfig {
	r.mu.RLock()
	profiles := r.orderedProfilesLocked()
	r.mu.RUnlock()
	out := make([]BotConfig, 0, len(profiles))
	for _, cfg := range profiles {
		if r.profileDisabled(cfg.ID) {
			continue
		}
		if proactiveMasterEnabled(cfg) || len(r.proactiveGroupOptIns(cfg.ID)) > 0 {
			out = append(out, cfg)
		}
	}
	return out
}

func proactiveMasterEnabled(cfg BotConfig) bool {
	return cfg.ProactiveMessaging != nil && cfg.ProactiveMessaging.Enabled
}

// proactiveSettings 是这台机器人生效的主动开口参数；没配过时全用默认值，给单独
// 打开的群用。
func proactiveSettings(cfg BotConfig) *ProactiveMessaging {
	if cfg.ProactiveMessaging == nil {
		return (&ProactiveMessaging{}).normalized()
	}
	return cfg.ProactiveMessaging.normalized()
}

// proactiveGroupOptIns 列出群管理里给这台机器人单独打开主动开口的群。
func (r *Runtime) proactiveGroupOptIns(profileID string) []string {
	r.mu.RLock()
	lister, _ := r.groupConfigs.(groupConfigLister)
	r.mu.RUnlock()
	if lister == nil {
		return nil
	}
	var ids []string
	for _, groupCfg := range lister.Groups().Groups {
		if strings.TrimSpace(groupCfg.BotProfileID) == profileID && groupCfg.ProactiveMessagingEnabled != nil && *groupCfg.ProactiveMessagingEnabled {
			ids = append(ids, groupCfg.GroupID)
		}
	}
	return cleanStrings(ids)
}

// proactiveGroupAllowed 判断这个群要不要主动开口：群里单独设置过就听群的，否则跟随
// 机器人；旧版群名单非空时只有名单里的群跟随。
func proactiveGroupAllowed(cfg BotConfig, groupCfg BotConfig, settings *ProactiveMessaging, groupID string) bool {
	if groupCfg.groupProactiveMessagingOverride != nil {
		return *groupCfg.groupProactiveMessagingOverride
	}
	if !proactiveMasterEnabled(cfg) {
		return false
	}
	if len(settings.GroupIDs) == 0 {
		return true
	}
	for _, id := range settings.GroupIDs {
		if id == groupID {
			return true
		}
	}
	return false
}

type proactiveTarget struct {
	event MessageEvent
	rules proactiveRules
}

func (r *Runtime) proactiveTargets(cfg BotConfig) []proactiveTarget {
	settings := proactiveSettings(cfg)
	candidates := r.proactiveRecentGroups(cfg.ID)
	candidates = append(candidates, settings.GroupIDs...)
	candidates = append(candidates, r.proactiveGroupOptIns(cfg.ID)...)
	var targets []proactiveTarget
	for _, groupID := range cleanStrings(candidates) {
		target, err := r.crossBotEvent(cfg.ID, EventKindGroup, groupID)
		if err != nil {
			continue
		}
		if !proactiveGroupAllowed(cfg, r.effectiveConfigForEvent(target.event), settings, groupID) {
			continue
		}
		targets = append(targets, proactiveTarget{event: target.event, rules: proactiveRules{
			Idle:       time.Duration(settings.GroupIdleMinutes) * time.Minute,
			DailyLimit: settings.DailyLimit,
		}})
	}
	if owner := strings.TrimSpace(cfg.OwnerID); proactiveMasterEnabled(cfg) && settings.OwnerEnabled && owner != "" {
		if target, err := r.crossBotEvent(cfg.ID, EventKindPrivate, owner); err == nil {
			targets = append(targets, proactiveTarget{event: target.event, rules: proactiveRules{
				Owner:      true,
				Idle:       time.Duration(settings.OwnerIdleHours) * time.Hour,
				DailyLimit: settings.DailyLimit,
			}})
		}
	}
	return targets
}

// dispatchProactiveMessages 扫一遍所有开了主动开口的会话，到点的各自开 goroutine 去问模型。
func (r *Runtime) dispatchProactiveMessages(ctx context.Context) {
	for _, cfg := range r.proactiveEnabledConfigs() {
		now := r.clock().In(cfg.Location())
		if proactiveQuietAt(proactiveSettings(cfg), now) {
			continue
		}
		for _, target := range r.proactiveTargets(cfg) {
			plan, ok := r.proactivePlanFor(target, now)
			if !ok {
				continue
			}
			key := sessionKey(target.event) + "|" + target.event.ProfileID
			if !r.proactive.claim(key, now, plan.Required) {
				continue
			}
			go func() {
				defer recoverGoroutinePanic("runtime.proactiveInitiative")
				defer r.proactive.release(key)
				r.runProactiveInitiative(ctx, target, plan)
			}()
		}
	}
}

// proactivePlanFor 检查这个会话现在能不能开口：准入、禁言、暂停这些和正常回复同一套，
// 再按历史判断冷场和退避。
func (r *Runtime) proactivePlanFor(target proactiveTarget, now time.Time) (proactivePlan, bool) {
	event := target.event
	if !r.proactiveSessionOpen(event, now) {
		return proactivePlan{}, false
	}
	cfg := r.effectiveConfigForEvent(event)
	history := r.proactiveRecentHistory(event)
	plan, _, ok := evaluateProactive(history, proactiveBotID(event, cfg), target.rules, now, sessionKey(event)+"|"+event.ProfileID)
	return plan, ok
}

// proactiveSessionOpen 检查准入、禁言、回复暂停、群里叫停和发不出去的群，和正常回复
// 同一套；生成完、发送前还要再查一遍。
func (r *Runtime) proactiveSessionOpen(event MessageEvent, now time.Time) bool {
	cfg := r.effectiveConfigForEvent(event)
	if !r.admitsNotice(cfg, event) {
		return false
	}
	if _, muted := r.botMutedForReply(event); muted {
		return false
	}
	if _, blocked := r.activeReplySuppression(event, now); blocked {
		return false
	}
	if event.Kind == EventKindGroup {
		if _, stopped := r.activeGroupStop(event, now); stopped {
			return false
		}
		if r.blockedGroupSendError(event) != nil {
			return false
		}
	}
	return true
}

func proactiveBotID(event MessageEvent, cfg BotConfig) string {
	return firstNonEmpty(strings.TrimSpace(event.SelfID), strings.TrimSpace(cfg.BotAccount))
}

// proactiveRecentHistory 读这个会话最近的消息：内存里的窗口加库里的记录。只用来数
// 时间和标记，不走 promptContextHistory 的锚点，免得巡检挪动正常回复的缓存窗口。
func (r *Runtime) proactiveRecentHistory(event MessageEvent) []MessageEvent {
	session := sessionKey(event)
	r.mu.RLock()
	memory := append([]MessageEvent(nil), r.history[session]...)
	if event.Kind == EventKindGroup {
		if buffer, ok := r.groupPromptHistory[groupPromptSessionKey(event)]; ok {
			memory = append(memory, buffer.Events...)
		}
	}
	store := r.messageStore
	r.mu.RUnlock()
	var stored []MessageEvent
	if store != nil {
		loadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var err error
		stored, err = listContextMessageEvents(loadCtx, store, session, proactiveHistoryLimit)
		cancel()
		if err != nil {
			log.Printf("diana proactive history load failed: %v", err)
		}
	}
	history := mergeMessageHistory(memory, stored, proactiveHistoryLimit)
	// 会话键已经定位到这个群，这里只去掉跨群带进来的参考。不比 SelfID：接入端还没报
	// 账号时，自己发出的消息记的是兜底的 "bot"，按 SelfID 筛会把它们筛掉，
	// 「没人接就别再说」也就跟着失效。
	visible := history[:0]
	for _, item := range history {
		if !item.crossGroupContext && (event.Kind != EventKindGroup || item.GroupID == "" || item.GroupID == event.GroupID) {
			visible = append(visible, item)
		}
	}
	return visible
}

const proactiveGroupPrompt = `群里已经安静了一阵，现在没人在找你。想一想要不要自己开个口，像群里的老成员那样随口说一句。
- 只在真有话想说时才开口：接着之前没聊完的话题、问一句之前有人提过的事后来怎么样了、说一件和这个群聊过的东西相关的小事。
- 不要「大家好」「有人在吗」「好安静啊」「大家在干嘛」这类空喊，不要总结或播报之前的聊天，不要介绍自己的功能。
- 一两句就够，长短和语气照你平时在这个群里的习惯。
- 想不出自然的话、或者现在开口显得突兀，就只输出 [SKIP]，这次不说话。不说话完全没问题。
- 直接给出要发的话，不要解释你为什么开口。`

const proactiveOwnerPrompt = `主人有一阵没来找你了，现在是你自己决定要不要先给主人发条私聊。
- 像熟人之间随手发一句：接着上次聊到的事、关心一下主人之前提过的安排、或者分享一个你觉得主人会感兴趣的小念头。
- 不要写成问候模板或客服回访，不要「在吗」「好久不见，最近怎么样」这类套话，也不要罗列自己能做什么。
- 一到三句，口吻照你和主人平时的相处。
- 想不出自然的话、或者现在找主人显得打扰，就只输出 [SKIP]，这次不说话。
- 直接给出要发的话，不要解释你为什么开口。`

var promptProactiveGroupSpec = registerPrompt(PromptSpec{
	Key:     "social.proactive_group",
	Group:   PromptGroupSocial,
	Title:   "主动开口 · 群聊冷场",
	Usage:   "开了试验性的主动开口后，允许的群冷场够久时用这段让模型决定要不要先说一句。它和普通回复一样带着人设和最近的群聊，模型只输出 [SKIP] 就是这次不说。",
	Default: proactiveGroupPrompt,
})

var promptProactiveOwnerSpec = registerPrompt(PromptSpec{
	Key:     "social.proactive_owner",
	Group:   PromptGroupSocial,
	Title:   "主动开口 · 找主人私聊",
	Usage:   "开了试验性的主动开口和「主动找主人」后，主人私聊安静够久时用这段让模型决定要不要先发一条。带着人设和最近的私聊，模型只输出 [SKIP] 就是这次不说。",
	Default: proactiveOwnerPrompt,
})

// proactiveRequestText 是这一轮的「当前消息」：一条内部事件，说清这不是谁发来的消息、
// 为什么现在醒来、已经连着几次没人接。
func proactiveRequestText(cfg BotConfig, event MessageEvent, plan proactivePlan, news proactiveNews) string {
	var builder strings.Builder
	where := "这个群"
	spec := promptProactiveGroupSpec
	if event.Kind == EventKindPrivate {
		where = "和主人的私聊"
		spec = promptProactiveOwnerSpec
	}
	builder.WriteString("【当前需要回复的消息】【内部事件，不是任何人发来的消息】" + where + "已经 " + formatProactiveIdle(plan.IdleFor) + " 没有新消息了。")
	if plan.Unanswered > 0 {
		builder.WriteString(fmt.Sprintf("你上次主动说的话还没人接（连着 %d 次），这次更要想清楚值不值得再说。", plan.Unanswered))
	}
	builder.WriteString("\n\n")
	builder.WriteString(strings.TrimSpace(cfg.prompt(spec)))
	if news.Results != "" {
		builder.WriteString("\n\n" + proactiveNewsMaterial(cfg, news))
	}
	return builder.String()
}

func formatProactiveIdle(idle time.Duration) string {
	if idle >= 48*time.Hour {
		return fmt.Sprintf("%d 天", int(idle/(24*time.Hour)))
	}
	if idle >= time.Hour {
		return fmt.Sprintf("%d 小时", int(idle/time.Hour))
	}
	return fmt.Sprintf("%d 分钟", max(1, int(idle/time.Minute)))
}

// normalizeProactiveOutput 清洗模型输出。返回空串表示这次不说。
func normalizeProactiveOutput(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" || strings.Contains(strings.ToUpper(text), proactiveSkipMarker) {
		return ""
	}
	text = strings.TrimSpace(strings.Trim(text, `"'“”‘’「」`))
	if runes := []rune(text); len(runes) > proactiveMaxReplyRunes {
		text = strings.TrimSpace(string(runes[:proactiveMaxReplyRunes]))
	}
	return text
}

// runProactiveInitiative 问模型要不要开口，要就复核、审核后发出去。每一次问过模型
// 都写一条事件，控制台里能看到发了什么、为什么没发。
func (r *Runtime) runProactiveInitiative(parent context.Context, target proactiveTarget, plan proactivePlan) {
	ctx, cancel := context.WithTimeout(parent, proactiveRunTimeout)
	defer cancel()
	event := target.event
	started := time.Now()
	record := EventRecord{
		At:        started,
		Kind:      event.Kind,
		Platform:  event.Platform,
		ProfileID: event.ProfileID,
		UserID:    event.UserID,
		GroupID:   event.GroupID,
		Text:      "[proactive] idle " + formatProactiveIdle(plan.IdleFor),
		Decision:  "not_replied",
	}
	finish := func(outcome, reason string, err error) {
		record.Outcome = outcome
		record.Reason = reason
		if err != nil {
			record.Error = err.Error()
		}
		record.Duration = time.Since(started).Milliseconds()
		r.record(record)
	}

	cfg := r.effectiveConfigForEvent(event)
	// 搜索放在模型名额外面：联网可能要一分多钟，不该占着对话模型的并发。
	news := r.findProactiveNews(ctx, cfg, event)
	text, err := r.runTaskQueryWithSlot(ctx, func(ctx context.Context) (string, error) {
		return r.generateProactiveMessage(ctx, cfg, event, plan, news)
	})
	if news.Query != "" {
		record.Text += " · 搜索「" + news.Query + "」"
		if news.Results == "" {
			record.Text += "（没搜到）"
		}
	}
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("diana proactive message generation failed for %s: %v", sessionKey(event), err)
		}
		finish("initiative_failed", "主动开口时模型调用失败，这次不说", err)
		return
	}
	if text == "" {
		finish("initiative_skipped", "主动开口：模型觉得现在没什么值得说的，这次不说", nil)
		return
	}
	record.Reply = text
	// 生成要十几秒，这期间有人说话了，就轮不到「冷场开口」了，交给正常回复。
	for _, item := range r.proactiveRecentHistory(event) {
		if item.Time > plan.Watermark && proactiveHumanEvent(item, proactiveBotID(event, cfg)) {
			finish("initiative_superseded", "主动开口：生成期间有人说话了，这条作废", nil)
			return
		}
	}
	if !r.proactiveSessionOpen(event, r.clock()) {
		finish("initiative_superseded", "主动开口：生成期间会话状态变了（被叫停、禁言或不再允许），这条作废", nil)
		return
	}
	if blocked, reason := r.proactiveSafetyBlocked(ctx, cfg, event, text); blocked {
		finish("initiative_blocked", reason, nil)
		return
	}
	sendCtx := withProactiveInitiative(ctx, uuid.NewString())
	if _, err := r.sendDecorated(sendCtx, event, text, outboundDecoration{}); err != nil {
		finish("initiative_failed", "主动开口的消息没发出去", err)
		return
	}
	record.Handled = true
	record.Decision = "replied"
	reason := "群里冷场了一阵，机器人主动开口"
	if event.Kind == EventKindPrivate {
		reason = "主人有一阵没来，机器人主动发了私聊"
	}
	finish("initiative_sent", reason, nil)
}

// generateProactiveMessage 像入群欢迎一样生成：同一份系统提示词（人设、关系）、最近的
// 会话历史、对话模型；不开工具，一轮出结果。新闻是事先由 Runner 搜好的素材。
func (r *Runtime) generateProactiveMessage(ctx context.Context, cfg BotConfig, event MessageEvent, plan proactivePlan, news proactiveNews) (string, error) {
	ctx = withLLMUsagePurpose(withLLMUsageContext(ctx, event), PurposeProactiveMessage)
	source := event
	if source.Kind == EventKindGroup && strings.TrimSpace(source.GroupName) == "" {
		lookupCtx, lookupCancel := context.WithTimeout(ctx, 5*time.Second)
		if info, err := r.getGroupInfoForEvent(lookupCtx, source, source.GroupID); err == nil {
			source.GroupName = strings.TrimSpace(info.GroupName)
		}
		lookupCancel()
	}
	relationship := r.relationshipPolicy(ctx, source)
	messages := []llm.Message{{
		Role:     llm.RoleSystem,
		Content:  r.systemPromptWithRelationship(source, nil, false, relationship),
		Priority: llm.MessagePrioritySystem,
	}}
	history := sessionOnlyHistory(r.promptContextHistory(source, cfg))
	if len(history) > proactivePromptHistoryLimit {
		history = history[len(history)-proactivePromptHistoryLimit:]
	}
	for _, item := range history {
		if source.Kind == EventKindGroup && !samePromptGroupScope(source, item) {
			continue
		}
		messages = append(messages, r.renderPromptHistoryEvent(ctx, source, item, cfg, false)...)
	}
	messages = compactHistoryLineDates(messages)
	if clock := r.runtimeClockPrompt(source); clock != "" {
		messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: clock, Priority: llm.MessagePrioritySystem})
	}
	messages = append(messages, llm.Message{
		Role:     llm.RoleUser,
		Content:  proactiveRequestText(cfg, source, plan, news),
		Priority: llm.MessagePriorityCurrent,
	})
	replyCfg := cfg
	replyCfg.AgentEnabled = false
	raw, err := r.generateReply(ctx, replyCfg, source, relationship, messages, nil)
	if err != nil {
		return "", err
	}
	return normalizeProactiveOutput(raw), nil
}

// proactiveSafetyBlocked 在群里发之前过一遍账号安全审核，开关和正常回复同一个。
// 审核挂掉时不发：主动开口本来就可有可无，不值得冒险。主人私聊不审，和正常回复一致。
func (r *Runtime) proactiveSafetyBlocked(ctx context.Context, cfg BotConfig, event MessageEvent, text string) (bool, string) {
	if event.Kind != EventKindGroup {
		return false, ""
	}
	enabled := boolValue(cfg.ReplySafetyMasterEnabled, true)
	if cfg.groupReplyAccountSafetyAuditOverride != nil {
		enabled = *cfg.groupReplyAccountSafetyAuditOverride
	}
	if !enabled {
		return false, ""
	}
	auditCtx := withLLMUsagePurpose(ctx, PurposeReplyAccountSafety)
	decision, err := r.runReplyAudit(auditCtx, event, "（群里冷场，机器人主动开口）", text, cfg, botReplyLoopEvidence{}, replyAuditNeed{AccountSafety: true})
	if err != nil {
		return true, "主动开口：账号安全审核没跑成，这条不发"
	}
	if accountSafetyError(decision) != nil {
		return true, "主动开口：账号安全审核没通过，这条不发"
	}
	return false, ""
}
