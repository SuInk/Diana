// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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

// GroupStatsRank 是排行里的一行：某人在时间段里的条数。
type GroupStatsRank struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name,omitempty"`
	Count    int    `json:"count"`
}

// GroupStatsStore 由持久化层实现。session 是 sessionKey 的结果；时间都是 Unix 秒，
// until 为 0 表示到现在。排行返回的第二个值是时间段里参与的总人数。
type GroupStatsStore interface {
	RecordMessageReaction(ctx context.Context, reaction MessageReaction) error
	ListMessageReactions(ctx context.Context, session, messageID string) ([]MessageReactionRow, error)
	RankReactionGivers(ctx context.Context, session string, since, until int64, limit int) ([]GroupStatsRank, int, error)
	RankGroupSpeakers(ctx context.Context, session string, since, until int64, limit int) ([]GroupStatsRank, int, error)
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
	maximumGroupStatsLimit = 200
	// defaultGroupStatsDays 是没给时间范围时统计的天数。发言排行只看最近一天通常太短。
	defaultGroupStatsDays = 7
)

type groupStatsResult struct {
	OK      bool   `json:"ok"`
	Action  string `json:"action"`
	Message string `json:"message"`
	Window  string `json:"window,omitempty"`
	// Total 是时间段里参与的人数；Ranks 只列前 limit 个。
	Total     int                  `json:"total"`
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
	if t.event.Kind != EventKindGroup || strings.TrimSpace(t.event.GroupID) == "" {
		return nil, fmt.Errorf("群统计只在群聊中可用")
	}
	t.runtime.mu.RLock()
	store, ok := t.runtime.messageStore.(GroupStatsStore)
	t.runtime.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("本地聊天记录没有开启持久化，无法统计")
	}
	return store, nil
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
	from, through := t.statsWindow(input)
	limit := chatHistoryPositiveInt(input, "limit", defaultGroupStatsLimit, maximumGroupStatsLimit)
	ranks, total, err := store.RankGroupSpeakers(ctx, sessionKey(t.event), from, through, limit)
	if err != nil {
		return "", fmt.Errorf("统计发言失败：%w", err)
	}
	message := fmt.Sprintf("这是本群在这个时间段里的发言排行（按本地记下的消息条数），共 %d 人发过言。", total)
	if total == 0 {
		message = "这个时间段本地没有记到任何群消息，可能机器人当时不在群里，不要编造排行。"
	} else if total > len(ranks) {
		message += fmt.Sprintf("只列出前 %d 人，要看全部把 limit 调大。", len(ranks))
	}
	message += "is_bot=true 的是机器人（包括本群标记过的其他机器人）。这是发言条数，不是点赞数。"
	return codingToolJSONAny(groupStatsResult{
		OK: true, Action: "speakers", Message: message, Window: chatHistoryWindowLabel(from, through),
		Total: total, Ranks: t.rankItems(ranks),
	})
}

// reactionStats 是表情回应统计：给了消息号就列出这条消息上谁挂了什么，否则出排行。
func (t *dianaChatHistoryTool) reactionStats(ctx context.Context, input map[string]any) (string, error) {
	store, err := t.groupStatsStore()
	if err != nil {
		return "", err
	}
	const coverage = "表情回应是从功能上线后才开始记录的，更早的点赞查不到；Telegram 只有机器人是群管理员时才收得到；取消掉的表情不计入；QQ 的表情是表情编号（例如 76 是赞）。"
	if messageID := configToolString(input, "message_id"); messageID != "" {
		rows, err := store.ListMessageReactions(ctx, sessionKey(t.event), messageID)
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
		return codingToolJSONAny(groupStatsResult{
			OK: true, Action: "reactions", Message: message + coverage, MessageID: messageID,
			Total: len(reactions), Reactions: reactions,
		})
	}
	from, through := t.statsWindow(input)
	limit := chatHistoryPositiveInt(input, "limit", defaultGroupStatsLimit, maximumGroupStatsLimit)
	ranks, total, err := store.RankReactionGivers(ctx, sessionKey(t.event), from, through, limit)
	if err != nil {
		return "", fmt.Errorf("统计表情回应失败：%w", err)
	}
	message := fmt.Sprintf("这是本群在这个时间段里挂表情回应（点赞）的排行，count 是挂出去的表情个数，共 %d 人。", total)
	if total == 0 {
		message = "这个时间段没有记到表情回应。不要拿发言排行或别人贴过的名单冒充点赞名单。"
	} else if total > len(ranks) {
		message += fmt.Sprintf("只列出前 %d 人，要看全部把 limit 调大。", len(ranks))
	}
	return codingToolJSONAny(groupStatsResult{
		OK: true, Action: "reactions", Message: message + coverage, Window: chatHistoryWindowLabel(from, through),
		Total: total, Ranks: t.rankItems(ranks),
	})
}

func codingToolJSONAny(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func cloneToolInput(input map[string]any) map[string]any {
	out := make(map[string]any, len(input)+1)
	for key, value := range input {
		out[key] = value
	}
	return out
}
