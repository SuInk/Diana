// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// 群统计：谁发言多、谁给哪条消息贴了表情。
//
// 线上：群友让机器人「导出一下点赞的名单」，机器人没有任何点赞数据——Telegram 没订阅
// 表情回应，QQ 的贴表情通知收到就丢了——只好去聊天记录里搜「点赞」，把群里有人之前
// 贴过的一份发言排行原样转述，还说成了点赞名单。发言排行本来就能从本地记录里算出来，
// 表情回应要先收下来才有得统计。

// MessageReaction 是一次表情回应的变化。
//
// Telegram 每次推的是这个人对这条消息挂着的完整表情集合，Replace 为 true，整体替换；
// QQ 每次推一个表情的贴上或取消，Replace 为 false，按 Added 增删。
type MessageReaction struct {
	// Session 是 sessionKey 算出来的会话键（机器人命名空间 + 群），和聊天记录同一套。
	Session   string
	Platform  string
	MessageID string
	UserID    string
	UserName  string
	Emojis    []string
	Replace   bool
	Added     bool
	At        int64
}

// MessageReactionRow 是某人当前挂在某条消息上的一个表情。
type MessageReactionRow struct {
	MessageID string `json:"message_id"`
	UserID    string `json:"user_id"`
	UserName  string `json:"user_name,omitempty"`
	Emoji     string `json:"emoji"`
	At        int64  `json:"at"`
}

// GroupStatsRank 是排行里的一行：某人在时间段里的条数。存储层按会话分开给，Session 是
// 这一行所在的群会话；跨群统计时由工具按权限筛掉读不了的群，再按人合并。
type GroupStatsRank struct {
	Session  string `json:"session,omitempty"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name,omitempty"`
	Count    int    `json:"count"`
}

// GroupStatsQuery 是排行的统计范围。给了 SessionPrefix 就统计这个前缀下的所有群（跨群），
// 否则只统计 Session 这一个群。时间都是 Unix 秒，Until 为 0 表示到现在。
type GroupStatsQuery struct {
	Session       string
	SessionPrefix string
	Since         int64
	Until         int64
}

// GroupStatsStore 由持久化层实现。session 是 sessionKey 的结果。排行按会话和人分组，
// 返回全部行，按条数从多到少。
type GroupStatsStore interface {
	RecordMessageReaction(ctx context.Context, reaction MessageReaction) error
	ListMessageReactions(ctx context.Context, session, messageID string) ([]MessageReactionRow, error)
	RankReactionGivers(ctx context.Context, query GroupStatsQuery) ([]GroupStatsRank, error)
	RankGroupSpeakers(ctx context.Context, query GroupStatsQuery) ([]GroupStatsRank, error)
}

const (
	// messageReactionSubType 是表情回应通知的子类型，两个平台共用。
	messageReactionSubType = "message_reaction"

	messageReactionReplace = "replace"
	messageReactionAdd     = "add"
	messageReactionRemove  = "remove"
)

// messageReactionSegment 把表情回应变化放进一个 reaction 段，平台适配层只负责填它。
func messageReactionSegment(messageID string, emojis []string, mode string) MessageSegment {
	return MessageSegment{Type: "reaction", Data: map[string]string{
		"message_id": messageID,
		"emojis":     strings.Join(normalizeReactionEmojis(emojis), "\n"),
		"mode":       mode,
	}}
}

// messageReactionFromEvent 从统一的通知里取出要落库的表情回应，不是表情回应就返回 false。
func messageReactionFromEvent(event MessageEvent, platform string) (MessageReaction, bool) {
	if event.Kind != EventKindNotice || event.SubType != messageReactionSubType || strings.TrimSpace(event.GroupID) == "" {
		return MessageReaction{}, false
	}
	for _, segment := range event.Segments {
		if segment.Type != "reaction" {
			continue
		}
		mode := segment.Data["mode"]
		var emojis []string
		if raw := segment.Data["emojis"]; raw != "" {
			emojis = normalizeReactionEmojis(strings.Split(raw, "\n"))
		}
		reaction := MessageReaction{
			Session:   sessionKey(event),
			Platform:  platform,
			MessageID: firstNonEmpty(segment.Data["message_id"], event.MessageID),
			UserID:    strings.TrimSpace(event.UserID),
			UserName:  strings.TrimSpace(event.SenderName),
			Emojis:    emojis,
			Replace:   mode == messageReactionReplace,
			Added:     mode == messageReactionAdd,
			At:        event.Time,
		}
		if reaction.MessageID == "" || reaction.UserID == "" || (mode != messageReactionReplace && len(emojis) == 0) {
			return MessageReaction{}, false
		}
		return reaction, true
	}
	return MessageReaction{}, false
}

// recordMessageReaction 把表情回应落库。存储不支持时静默跳过：统计是附加能力，
// 不该因为它让事件处理报错。
func (r *Runtime) recordMessageReaction(reaction MessageReaction) {
	r.mu.RLock()
	store, ok := r.messageStore.(GroupStatsStore)
	r.mu.RUnlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.RecordMessageReaction(ctx, reaction); err != nil {
		log.Printf("diana message reaction save failed: session=%s message_id=%s user_id=%s: %v", reaction.Session, reaction.MessageID, reaction.UserID, err)
	}
}

// normalizeReactionEmojis 去掉空白和重复，保持原顺序。
func normalizeReactionEmojis(emojis []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(emojis))
	for _, emoji := range emojis {
		emoji = strings.TrimSpace(emoji)
		if emoji == "" || seen[emoji] {
			continue
		}
		seen[emoji] = true
		out = append(out, emoji)
	}
	return out
}

const (
	defaultGroupStatsLimit = 50
	// defaultGroupStatsDays 是没给时间范围时统计的天数。发言排行只看最近一天通常太短。
	defaultGroupStatsDays = 7
)

type groupStatsResult struct {
	OK      bool   `json:"ok"`
	Action  string `json:"action"`
	Message string `json:"message"`
	Window  string `json:"window,omitempty"`
	// Total 是时间段里参与的人数（单条消息的表情回应时是挂了表情的人数）。排行按整条
	// 分页，Page 写明这一页是第几到第几名、怎么接着读。
	Total int `json:"total"`
	// Groups 是跨群统计时实际计入的群数。
	Groups    int                  `json:"groups,omitempty"`
	Page      *toolResultPage      `json:"page,omitempty"`
	Ranks     []groupStatsRankItem `json:"ranks,omitempty"`
	MessageID string               `json:"message_id,omitempty"`
	Reactions []groupReactionItem  `json:"reactions,omitempty"`
}

type groupStatsRankItem struct {
	Rank     int    `json:"rank"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name,omitempty"`
	Count    int    `json:"count"`
	IsBot    bool   `json:"is_bot,omitempty"`
}

type groupReactionItem struct {
	UserID   string   `json:"user_id"`
	UserName string   `json:"user_name,omitempty"`
	Emojis   []string `json:"emojis"`
}

func (t *dianaChatHistoryTool) groupStatsStore() (GroupStatsStore, error) {
	t.runtime.mu.RLock()
	store, ok := t.runtime.messageStore.(GroupStatsStore)
	t.runtime.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("本地聊天记录没有开启持久化，无法统计")
	}
	return store, nil
}

// groupStatsScope 是一次统计实际覆盖的范围。
type groupStatsScope struct {
	query GroupStatsQuery
	// prefix 非空表示跨群：会话键去掉它就是群号。
	prefix string
	label  string
	guard  *crossGroupReadGuard
}

// resolveGroupStatsScope 照 search 的口径认 scope 和 group_id：
// 默认当前群；group_id 指定某个群；scope=all_groups 统计这个机器人所在的全部群。
// 以前统计只看当前群，模型传了 scope=all_groups 也照样只拿到本群人数，还当成了
// 跨群结果（生产 2026-11-20「算跨群」回了同一个 39 人）。
func (t *dianaChatHistoryTool) resolveGroupStatsScope(ctx context.Context, input map[string]any) (groupStatsScope, error) {
	scope := strings.TrimSpace(configToolString(input, "scope"))
	crossGroup := scope == "all_groups" || scope == "cross_group" || scope == "groups"
	if scope != "" && scope != "current" && !crossGroup {
		return groupStatsScope{}, fmt.Errorf("scope 必须是 current 或 all_groups")
	}
	groupID := strings.TrimSpace(configToolString(input, "group_id"))
	if groupID != "" && crossGroup {
		return groupStatsScope{}, fmt.Errorf("指定 group_id 时 scope 必须为 current；all_groups 表示全部可见群，不能同时指定单群")
	}
	current := t.event.Kind == EventKindGroup && strings.TrimSpace(t.event.GroupID) != ""
	switch {
	case crossGroup:
		if err := t.crossGroupSearchError(ctx); err != nil {
			return groupStatsScope{}, err
		}
		prefix := groupHistorySessionPrefix(t.event)
		return groupStatsScope{query: GroupStatsQuery{SessionPrefix: prefix}, prefix: prefix, label: "同一机器人的所有群", guard: newCrossGroupReadGuard(ctx, t)}, nil
	case groupID != "" && (!current || groupID != strings.TrimSpace(t.event.GroupID)):
		if err := t.crossGroupSearchError(ctx); err != nil {
			return groupStatsScope{}, err
		}
		if !newCrossGroupReadGuard(ctx, t).allows(ctx, groupID) {
			return groupStatsScope{}, fmt.Errorf("你不在群 %s 里，不能统计这个群", groupID)
		}
		scoped := t.event
		scoped.Kind, scoped.GroupID = EventKindGroup, groupID
		return groupStatsScope{query: GroupStatsQuery{Session: sessionKey(scoped)}, label: "群 " + groupID}, nil
	case current:
		return groupStatsScope{query: GroupStatsQuery{Session: sessionKey(t.event)}, label: "本群"}, nil
	}
	return groupStatsScope{}, fmt.Errorf("私聊里没有本群可统计；要统计群，传 group_id 指定群，或 scope=all_groups 统计全部群（需开启跨群记忆，私聊仅主人）")
}

// mergeRanks 把存储层按会话分开的行按人合并。跨群时先筛掉提问者读不了的群；
// 名字取这个人发言最多的那个群里的名字。返回合并后的排行、覆盖的群数和被筛掉的群数。
func (scope groupStatsScope) mergeRanks(ctx context.Context, rows []GroupStatsRank) ([]GroupStatsRank, int, int) {
	groups, dropped := map[string]bool{}, map[string]bool{}
	byUser := map[string]*GroupStatsRank{}
	best := map[string]int{}
	var order []string
	for _, row := range rows {
		if scope.prefix != "" {
			groupID := strings.TrimPrefix(row.Session, scope.prefix)
			if scope.guard != nil && !scope.guard.allows(ctx, groupID) {
				dropped[row.Session] = true
				continue
			}
		}
		groups[row.Session] = true
		merged, ok := byUser[row.UserID]
		if !ok {
			merged = &GroupStatsRank{UserID: row.UserID}
			byUser[row.UserID] = merged
			order = append(order, row.UserID)
		}
		merged.Count += row.Count
		if row.UserName != "" && row.Count > best[row.UserID] {
			best[row.UserID] = row.Count
			merged.UserName = row.UserName
		}
	}
	out := make([]GroupStatsRank, 0, len(order))
	for _, userID := range order {
		out = append(out, *byUser[userID])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].UserID < out[j].UserID
	})
	return out, len(groups), len(dropped)
}

// coverageNote 说明统计覆盖了几个群，跨群时还要说清楚是不是全部。
func (scope groupStatsScope) coverageNote(groups, dropped int) string {
	if scope.prefix == "" {
		return ""
	}
	note := fmt.Sprintf("跨群统计覆盖本地有记录的 %d 个群，同一个人在多个群的条数已合并、只算一人。", groups)
	if dropped > 0 {
		note += fmt.Sprintf("另有 %d 个群你不在里面，没有计入。", dropped)
	}
	return note + "只算得到机器人在群里时本地记下的消息。"
}

// statsWindow 解析统计时间段：没给任何时间参数时默认最近 7 天。
func (t *dianaChatHistoryTool) statsWindow(input map[string]any) (int64, int64) {
	if !hasChatHistoryTimeValue(input, "from_time") && intFromAny(input["days"]) <= 0 && intFromAny(input["hours"]) <= 0 && !chatHistoryBool(input, "all_time") {
		input = cloneToolInput(input)
		input["days"] = defaultGroupStatsDays
	}
	return t.resolveWindow(input)
}

func (t *dianaChatHistoryTool) rankItems(ranks []GroupStatsRank) []groupStatsRankItem {
	cfg := t.runtime.effectiveConfigForEvent(t.event)
	bots := map[string]bool{}
	if account := firstNonEmpty(cfg.BotAccount, t.event.SelfID); account != "" {
		bots[account] = true
	}
	// 群里标记过的其他机器人也标出来，免得把机器人的刷屏当成群友发言多。
	for _, id := range cfg.MarkedBotIDs {
		if id = strings.TrimSpace(id); id != "" {
			bots[id] = true
		}
	}
	items := make([]groupStatsRankItem, 0, len(ranks))
	for index, rank := range ranks {
		items = append(items, groupStatsRankItem{
			Rank: index + 1, UserID: rank.UserID, UserName: rank.UserName, Count: rank.Count,
			IsBot: bots[rank.UserID],
		})
	}
	return items
}

// speakerStats 是发言排行：本地记下的群消息按人计数。
func (t *dianaChatHistoryTool) speakerStats(ctx context.Context, input map[string]any) (string, error) {
	store, err := t.groupStatsStore()
	if err != nil {
		return "", err
	}
	scope, err := t.resolveGroupStatsScope(ctx, input)
	if err != nil {
		return "", err
	}
	from, through := t.statsWindow(input)
	scope.query.Since, scope.query.Until = from, through
	rows, err := store.RankGroupSpeakers(ctx, scope.query)
	if err != nil {
		return "", fmt.Errorf("统计发言失败：%w", err)
	}
	ranks, groups, dropped := scope.mergeRanks(ctx, rows)
	total := len(ranks)
	message := fmt.Sprintf("这是%s在这个时间段里的发言排行（按本地记下的消息条数），共 %d 人发过言。", scope.label, total)
	if total == 0 {
		message = "这个时间段本地没有记到任何群消息，可能机器人当时不在群里，不要编造排行。"
	}
	message += scope.coverageNote(groups, dropped)
	message += "is_bot=true 的是机器人（包括标记过的其他机器人）。这是发言条数，不是点赞数。"
	return t.rankPage(ctx, input, groupStatsResult{
		OK: true, Action: "speakers", Message: message, Window: chatHistoryWindowLabel(from, through, profileLocation(t.event.ProfileID)),
		Total: total, Groups: groups,
	}, ranks)
}

// rankPage 把排行按整条分页：名次按全部排行算，从 offset 起最多 limit 条，装不下单次
// 上限的整条留到下一页。
func (t *dianaChatHistoryTool) rankPage(ctx context.Context, input map[string]any, result groupStatsResult, ranks []GroupStatsRank) (string, error) {
	items := t.rankItems(ranks)
	limit := chatHistoryCount(input, "limit", defaultGroupStatsLimit)
	return marshalToolResultPage(ctx, len(items), toolPageOffset(input), limit, func(n int, page toolResultPage) any {
		start := min(page.Offset, len(items))
		out := result
		out.Page = &page
		out.Ranks = items[start : start+n]
		return out
	})
}

// reactionStats 是表情回应统计：给了消息号就列出这条消息上谁挂了什么，否则出排行。
func (t *dianaChatHistoryTool) reactionStats(ctx context.Context, input map[string]any) (string, error) {
	store, err := t.groupStatsStore()
	if err != nil {
		return "", err
	}
	scope, err := t.resolveGroupStatsScope(ctx, input)
	if err != nil {
		return "", err
	}
	const coverage = "表情回应是从功能上线后才开始记录的，更早的点赞查不到；Telegram 只有机器人是群管理员时才收得到；取消掉的表情不计入；QQ 的表情是表情编号（例如 76 是赞）。"
	if messageID := configToolString(input, "message_id"); messageID != "" {
		if scope.prefix != "" {
			return "", fmt.Errorf("查单条消息的表情回应要传它所在群的 group_id，不能用 scope=all_groups")
		}
		rows, err := store.ListMessageReactions(ctx, scope.query.Session, messageID)
		if err != nil {
			return "", fmt.Errorf("读取表情回应失败：%w", err)
		}
		byUser := map[string]*groupReactionItem{}
		var order []string
		for _, row := range rows {
			item, ok := byUser[row.UserID]
			if !ok {
				item = &groupReactionItem{UserID: row.UserID, UserName: row.UserName}
				byUser[row.UserID] = item
				order = append(order, row.UserID)
			}
			item.Emojis = append(item.Emojis, row.Emoji)
		}
		reactions := make([]groupReactionItem, 0, len(order))
		for _, userID := range order {
			reactions = append(reactions, *byUser[userID])
		}
		message := fmt.Sprintf("这条消息现在有 %d 人挂着表情回应。", len(reactions))
		if len(reactions) == 0 {
			message = "这条消息上没有记到表情回应。"
		}
		result := groupStatsResult{OK: true, Action: "reactions", Message: message + coverage, MessageID: messageID, Total: len(reactions)}
		// 单条消息上的人也按整条分页，不设条数上限（不传 limit 就是全部）。
		return marshalToolResultPage(ctx, len(reactions), toolPageOffset(input), chatHistoryCount(input, "limit", 0), func(n int, page toolResultPage) any {
			start := min(page.Offset, len(reactions))
			out := result
			out.Page = &page
			out.Reactions = reactions[start : start+n]
			return out
		})
	}
	from, through := t.statsWindow(input)
	scope.query.Since, scope.query.Until = from, through
	rows, err := store.RankReactionGivers(ctx, scope.query)
	if err != nil {
		return "", fmt.Errorf("统计表情回应失败：%w", err)
	}
	ranks, groups, dropped := scope.mergeRanks(ctx, rows)
	total := len(ranks)
	message := fmt.Sprintf("这是%s在这个时间段里挂表情回应（点赞）的排行，count 是挂出去的表情个数，共 %d 人。", scope.label, total)
	if total == 0 {
		message = "这个时间段没有记到表情回应。不要拿发言排行或别人贴过的名单冒充点赞名单。"
	}
	return t.rankPage(ctx, input, groupStatsResult{
		OK: true, Action: "reactions", Message: message + scope.coverageNote(groups, dropped) + coverage, Window: chatHistoryWindowLabel(from, through, profileLocation(t.event.ProfileID)),
		Total: total, Groups: groups,
	}, ranks)
}

func cloneToolInput(input map[string]any) map[string]any {
	out := make(map[string]any, len(input)+1)
	for key, value := range input {
		out[key] = value
	}
	return out
}
