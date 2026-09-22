// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// fakeGroupStatsStore 记下收到的表情回应，并按调用方给的数据回排行。
type fakeGroupStatsStore struct {
	mu        sync.Mutex
	reactions []MessageReaction
	speakers  []GroupStatsRank
	givers    []GroupStatsRank
	rows      []MessageReactionRow
	lastRange [2]int64
	appended  int
}

func (s *fakeGroupStatsStore) AppendMessageEvent(context.Context, string, MessageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appended++
	return nil
}
func (s *fakeGroupStatsStore) ListRecentMessageEvents(context.Context, string, int) ([]MessageEvent, error) {
	return nil, nil
}
func (s *fakeGroupStatsStore) RecordMessageReaction(_ context.Context, r MessageReaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reactions = append(s.reactions, r)
	return nil
}
func (s *fakeGroupStatsStore) ListMessageReactions(context.Context, string, string) ([]MessageReactionRow, error) {
	return s.rows, nil
}
func (s *fakeGroupStatsStore) RankReactionGivers(_ context.Context, _ string, since, until int64, limit int) ([]GroupStatsRank, int, error) {
	s.lastRange = [2]int64{since, until}
	return limitRanks(s.givers, limit), len(s.givers), nil
}
func (s *fakeGroupStatsStore) RankGroupSpeakers(_ context.Context, _ string, since, until int64, limit int) ([]GroupStatsRank, int, error) {
	s.lastRange = [2]int64{since, until}
	return limitRanks(s.speakers, limit), len(s.speakers), nil
}
func limitRanks(ranks []GroupStatsRank, limit int) []GroupStatsRank {
	if limit > 0 && len(ranks) > limit {
		return ranks[:limit]
	}
	return ranks
}

// Telegram 推的 new_reaction 是完整集合：整体替换，普通表情、自定义表情和付费星星都要认出来。
func TestTelegramReactionBecomesReplaceNotice(t *testing.T) {
	var update telegramUpdate
	raw := `{"update_id":1,"message_reaction":{"chat":{"id":-100123,"type":"supergroup","title":"测试群"},"message_id":662,
		"user":{"id":42,"first_name":"雨","last_name":"夹雪"},"date":1789500000,
		"old_reaction":[{"type":"emoji","emoji":"👍"}],
		"new_reaction":[{"type":"emoji","emoji":"❤"},{"type":"custom_emoji","custom_emoji_id":"5368"},{"type":"paid"}]}}`
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	event := telegramReactionToEvent(update.MessageReaction, "7")
	event.ContextNamespace = "tg"
	reaction, ok := messageReactionFromEvent(event, PlatformTelegram)
	if !ok {
		t.Fatalf("没认出表情回应：%#v", event)
	}
	want := MessageReaction{Session: "tg:group:-100123", Platform: PlatformTelegram, MessageID: "662", UserID: "42", UserName: "雨 夹雪",
		Emojis: []string{"❤", "custom:5368", "⭐"}, Replace: true, At: 1789500000}
	if !reflect.DeepEqual(reaction, want) {
		t.Fatalf("reaction = %#v\nwant %#v", reaction, want)
	}
	// 取消全部表情时 new_reaction 为空，也要当成一次替换（清空）。
	update.MessageReaction.NewReaction = nil
	if cleared, ok := messageReactionFromEvent(telegramReactionToEvent(update.MessageReaction, "7"), PlatformTelegram); !ok || !cleared.Replace || len(cleared.Emojis) != 0 {
		t.Fatalf("清空表情没被识别：%#v %v", cleared, ok)
	}
	// 匿名管理员以群身份挂的表情没有 user，无法统计到人。
	update.MessageReaction.User = nil
	if event := telegramReactionToEvent(update.MessageReaction, "7"); event.Kind != "" {
		t.Fatalf("没有 user 的回应不该产生事件：%#v", event)
	}
}

// NapCat 每次推一个表情的贴上或取消。
func TestOneBotEmojiLikeBecomesReactionNotice(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		added bool
	}{
		{`{"time":1789500000,"self_id":90001,"post_type":"notice","notice_type":"group_msg_emoji_like","group_id":20003,"user_id":30003,"message_id":-1281528691,"likes":[{"emoji_id":"76","count":1}],"is_add":true,"message_seq":123}`, true},
		{`{"time":1789500001,"self_id":90001,"post_type":"notice","notice_type":"group_msg_emoji_like","group_id":20003,"user_id":30003,"message_id":-1281528691,"likes":[{"emoji_id":76,"count":1}],"is_add":false}`, false},
	} {
		var envelope oneBotEnvelope
		if err := json.Unmarshal([]byte(tc.raw), &envelope); err != nil {
			t.Fatal(err)
		}
		reaction, ok := messageReactionFromEvent(messageEventFromEnvelope(envelope), PlatformOneBotV11)
		if !ok {
			t.Fatalf("没认出贴表情：%s", tc.raw)
		}
		if reaction.MessageID != "-1281528691" || reaction.UserID != "30003" || !reflect.DeepEqual(reaction.Emojis, []string{"76"}) ||
			reaction.Replace || reaction.Added != tc.added || !strings.HasSuffix(reaction.Session, "group:20003") {
			t.Fatalf("reaction = %#v", reaction)
		}
	}
}

// 表情回应只落库统计，不进回复流程，也不当成一条聊天记录。
func TestRuntimeRecordsReactionWithoutReplying(t *testing.T) {
	store := &fakeGroupStatsStore{}
	channel := &recordingChannel{}
	runtime := NewRuntime(BotConfig{ID: "qq", BotAccount: "90001"}.WithDefaults(), channel, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	event := MessageEvent{
		Kind: EventKindNotice, SubType: messageReactionSubType, GroupID: "100", UserID: "20002", SenderName: "Alice", MessageID: "m1", Time: 10,
		Segments: []MessageSegment{messageReactionSegment("m1", []string{"76"}, messageReactionAdd)},
	}
	if err := runtime.HandleEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(store.reactions) != 1 || store.reactions[0].MessageID != "m1" || !store.reactions[0].Added {
		t.Fatalf("表情回应没落库：%#v", store.reactions)
	}
	if store.appended != 0 || len(channel.sent) != 0 {
		t.Fatalf("表情回应不该记成消息或触发回复：appended=%d sent=%d", store.appended, len(channel.sent))
	}
}

// 线上：让机器人「导出点赞名单」，它拿发言排行冒充。两个统计要分开给，说明里写清是什么。
func TestChatHistorySpeakerAndReactionStats(t *testing.T) {
	store := &fakeGroupStatsStore{
		speakers: []GroupStatsRank{{UserID: "a", UserName: "Mio", Count: 6317}, {UserID: "90001", UserName: "Diana", Count: 500}, {UserID: "b", UserName: "RR", Count: 3208}},
		givers:   []GroupStatsRank{{UserID: "b", UserName: "RR", Count: 12}},
		rows:     []MessageReactionRow{{MessageID: "662", UserID: "b", UserName: "RR", Emoji: "👍"}, {MessageID: "662", UserID: "b", UserName: "RR", Emoji: "❤"}, {MessageID: "662", UserID: "c", Emoji: "🔥"}},
	}
	runtime := NewRuntime(BotConfig{ID: "qq", BotAccount: "90001", MarkedBotIDs: []string{"b"}}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	runtime.SetMessageHistoryStore(store)
	tool := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "100", UserID: "u", Time: 1_000_000})

	decode := func(raw string) groupStatsResult {
		t.Helper()
		var result groupStatsResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("%v: %s", err, raw)
		}
		return result
	}
	raw, err := tool.Run(context.Background(), map[string]any{"operation": "speakers", "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	speakers := decode(raw)
	if speakers.Total != 3 || len(speakers.Ranks) != 2 || speakers.Ranks[0].Rank != 1 || speakers.Ranks[0].IsBot || !speakers.Ranks[1].IsBot || !strings.Contains(speakers.Message, "不是点赞数") {
		t.Fatalf("发言排行 = %#v", speakers)
	}
	// 没给时间范围默认最近 7 天。
	if store.lastRange[1]-store.lastRange[0] != 7*24*3600 {
		t.Fatalf("默认时间段 = %v", store.lastRange)
	}

	raw, err = tool.Run(context.Background(), map[string]any{"operation": "reactions", "days": 30})
	if err != nil {
		t.Fatal(err)
	}
	givers := decode(raw)
	// 标记过的机器人 b 在点赞排行里同样标出来。
	if givers.Total != 1 || givers.Ranks[0].Count != 12 || !givers.Ranks[0].IsBot || !strings.Contains(givers.Message, "更早的点赞查不到") {
		t.Fatalf("点赞排行 = %#v", givers)
	}

	raw, err = tool.Run(context.Background(), map[string]any{"operation": "reactions", "message_id": "662"})
	if err != nil {
		t.Fatal(err)
	}
	onMessage := decode(raw)
	want := []groupReactionItem{{UserID: "b", UserName: "RR", Emojis: []string{"👍", "❤"}}, {UserID: "c", Emojis: []string{"🔥"}}}
	if onMessage.Total != 2 || !reflect.DeepEqual(onMessage.Reactions, want) {
		t.Fatalf("单条消息的表情回应 = %#v", onMessage)
	}

	// 私聊里没有群统计。
	private := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindPrivate, UserID: "u"})
	if _, err := private.Run(context.Background(), map[string]any{"operation": "speakers"}); err == nil {
		t.Fatal("私聊不该能做群统计")
	}
}

// 真实模型：点赞相关的请求必须走 reactions，发言排行走 speakers，两者不能混。
// 线上那次就是把发言排行当成了点赞名单。
func TestLiveChatHistoryPicksReactionsForLikeList(t *testing.T) {
	client := liveLLMClient(t)
	runtime := NewRuntime(BotConfig{ID: "tg"}.WithDefaults(), nilChannel{}, NewPluginManager(), nil, nil, nil, nil)
	tool := newDianaChatHistoryTool(runtime, MessageEvent{Kind: EventKindGroup, GroupID: "1"})
	definition := llm.ToolDefinition{Name: tool.Name(), Description: tool.Description(), Parameters: tool.InputSchema()}
	ask := func(text string) (string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		resp, err := client.Generate(ctx, llm.GenerateRequest{
			Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: "你是群聊机器人，需要数据时调用工具。"},
				{Role: llm.RoleUser, Content: text},
			},
			Tools: []llm.ToolDefinition{definition},
		})
		if err != nil {
			t.Fatalf("%s：%v", text, err)
		}
		for _, call := range resp.ToolCalls {
			if op, _ := call.Arguments["operation"].(string); op != "" {
				return op, resp.Text
			}
		}
		return "", resp.Text
	}
	for _, tc := range []struct {
		ask, want string
	}{
		{"导出一下 662 这条消息的点赞名单", "reactions"},
		{"看看 662 这条消息都有谁点了表情", "reactions"},
		{"统计一下最近谁点赞最多", "reactions"},
		{"这周群里谁发言最多，列个排行", "speakers"},
		{"这个月水群最多的几个人是谁", "speakers"},
	} {
		wrong := 0
		for i := 0; i < 3; i++ {
			if got, text := ask(tc.ask); got != tc.want {
				wrong++
				t.Logf("「%s」第 %d 次选了 %q，应当是 %q；回复：%.100s", tc.ask, i+1, got, tc.want, text)
			}
		}
		if wrong > 1 {
			t.Errorf("「%s」3 次里有 %d 次没选 %s", tc.ask, wrong, tc.want)
		}
	}
	// 没说是哪条消息的点赞名单：可以追问或者直接出点赞排行，但绝不能拿发言排行冒充。
	for i := 0; i < 3; i++ {
		if got, text := ask("你导出一下点赞的名单"); got == "speakers" {
			t.Errorf("第 %d 次把点赞名单做成了发言排行；回复：%.100s", i+1, text)
		}
	}
}
