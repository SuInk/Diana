// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// RSS 话题池的生产回放，结果逐条写进 JSONL：
//
//  1. 建池：真实 Feed 走真实路径（runClaimedRSSWatch，模型判断 + pool 送达）。给了
//     DIANA_REPLAY_POOL_IN 就直接用上一轮存下的池子，两轮比的是同一批条目。
//
//  2. 冷场开口：从生产记录里找白天真冷场过的时刻（同群相邻两次回复隔了
//     DIANA_REPLAY_IDLE 以上，且冷场时刻落在 DIANA_REPLAY_DAY_HOURS 内），用当时的
//     同群聊天，不带素材 / 话题池素材各生成一次。
//
//     DIANA_LIVE_LLM=1 与 DIANA_TEST_LLM_CONFIG     对话模型配置（见 liveLLMClient）
//     DIANA_REPLAY_BOT           机器人配置 JSON（人设、账号）
//     DIANA_REPLAY_FEEDS         空格分隔的 Feed 地址
//     DIANA_REPLAY_RULE          判断规则
//     DIANA_REPLAY_POOL_IN       可选，上一轮存下的池子 JSON
//     DIANA_REPLAY_POOL_OUT      这一轮的池子 JSON
//     DIANA_REPLAY_TRACES        debug-traces 下若干日期目录，按系统路径列表分隔（:）
//     DIANA_REPLAY_MODEL         只取这个模型的记录
//     DIANA_REPLAY_IDLE          冷场多久算数，默认 90m
//     DIANA_REPLAY_DAY_HOURS     冷场时刻的本地小时范围，默认 10-22
//     DIANA_REPLAY_MAX           最多回放几个冷场时刻，默认 20；每个群最多 3 个
//     DIANA_REPLAY_OUT           结果 JSONL
func TestLiveRSSTopicPoolReplay(t *testing.T) {
	client := liveLLMClient(t)
	var botFile struct {
		BotConfig
	}
	readReplayJSON(t, os.Getenv("DIANA_REPLAY_BOT"), &botFile)
	out, err := os.Create(os.Getenv("DIANA_REPLAY_OUT"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var outMu sync.Mutex
	write := func(record map[string]any) {
		data, _ := json.Marshal(record)
		outMu.Lock()
		out.Write(append(data, '\n'))
		outMu.Unlock()
	}

	traces := replayTopicPoolTraces(t, filepath.SplitList(os.Getenv("DIANA_REPLAY_TRACES")), os.Getenv("DIANA_REPLAY_MODEL"))
	groups := map[string]bool{}
	for _, trace := range traces {
		groups[trace.group] = true
	}
	t.Logf("traces=%d groups=%d", len(traces), len(groups))
	if len(traces) == 0 {
		t.Fatal("no traces")
	}

	cfg := botFile.BotConfig
	cfg.ID, cfg.Enabled, cfg.Platform, cfg.AgentEnabled = "bot-replay", true, "onebot-v11", false
	cfg.ReplySafetyMasterEnabled = boolPointer(false)
	cfg.ProactiveMessaging = &ProactiveMessaging{Enabled: true}
	// 生产配置把各用途绑到具体模型档，回放里只有一个对话模型。
	cfg.ModelRoles = nil
	rec := &replayRecorder{inner: client}
	newRuntime := func(tasks *stubReminderStore, channel *recordingChannel) *Runtime {
		store := &stubLLMProfileStore{set: llm.NewProfileSet(llm.ProviderConfig{Provider: llm.ProviderOpenAICompatible, APIKey: "k", BaseURL: "http://localhost/v1", Model: "m"})}
		runtime := NewRuntime(cfg, channel, NewPluginManager(NewRSSWatchPlugin(&http.Client{Timeout: 40 * time.Second})), store, tasks, nil, nil)
		runtime.SetLLMProviderConfigFactory(func(llm.ProviderConfig) (LLMProvider, error) { return rec, nil })
		return runtime
	}

	// 1. 建池。订阅投到记录里出现过的全部群。
	var targets []ReminderDeliveryTarget
	for group := range groups {
		targets = append(targets, ReminderDeliveryTarget{Platform: "onebot-v11", ProfileID: cfg.ID, GroupID: group})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].GroupID < targets[j].GroupID })
	var sources []ReminderFeedSource
	for _, feed := range strings.Fields(os.Getenv("DIANA_REPLAY_FEEDS")) {
		sources = append(sources, ReminderFeedSource{FeedURL: feed, Source: "rss"})
	}
	watch := Reminder{ID: "pool-replay", Kind: ReminderKindRSSWatch, IntervalSeconds: 900, OwnerID: "owner",
		Platform: "onebot-v11", ProfileID: cfg.ID, GroupID: targets[0].GroupID,
		FeedRuleMode: "llm", FeedJudgePrompt: os.Getenv("DIANA_REPLAY_RULE"), FeedDelivery: rssDeliveryPool,
		NotificationTargetsJSON: encodeReminderDeliveryTargets(targets)}
	applyRSSWatchSources(&watch, sources)
	tasks := &stubReminderStore{items: []Reminder{watch}}
	channel := &recordingChannel{}
	ctx := context.Background()
	if path := os.Getenv("DIANA_REPLAY_POOL_IN"); path != "" {
		var pool []rssTopicPoolItem
		readReplayJSON(t, path, &pool)
		tasks.items[0].FeedTopicPoolJSON = encodeRSSTopicPool(pool)
	} else {
		buildCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		if _, err := newRuntime(tasks, channel).runClaimedRSSWatch(buildCtx, watch); err != nil {
			t.Logf("pool build error: %v", err)
		}
		cancel()
	}
	pool := decodeRSSTopicPool(tasks.Reminders()[0].FeedTopicPoolJSON)
	if path := os.Getenv("DIANA_REPLAY_POOL_OUT"); path != "" {
		data, _ := json.Marshal(pool)
		os.WriteFile(path, data, 0o600)
	}
	write(map[string]any{"stage": "pool", "sent_to_group": len(channel.sentSnapshot()), "items": pool})
	t.Logf("pool items=%d", len(pool))
	if len(pool) == 0 {
		t.Fatal("pool is empty")
	}

	// 2. 冷场开口。
	moments := replayTopicPoolColdMoments(traces, replayDuration(os.Getenv("DIANA_REPLAY_IDLE"), 90*time.Minute), os.Getenv("DIANA_REPLAY_DAY_HOURS"), replayInt(os.Getenv("DIANA_REPLAY_MAX"), 20))
	write(map[string]any{"stage": "cold_stats", "traces": len(traces), "moments": len(moments)})
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for index, moment := range moments {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			trace := moment.trace
			// 池条目的时间戳拨到冷场时刻之前，免得按回放时的「现在」被当成过期或未来；
			// 每个时刻轮换哪几条最新，整池条目都能轮到。
			momentWatch := tasks.Reminders()[0]
			shifted := decodeRSSTopicPool(momentWatch.FeedTopicPoolJSON)
			for i := range shifted {
				rank := (i - index*rssTopicPoolOfferItems%len(shifted) + len(shifted)) % len(shifted)
				shifted[i].CreatedAt = moment.at.Unix() - int64(rank+1)*600
			}
			momentWatch.FeedTopicPoolJSON = encodeRSSTopicPool(shifted)
			cold := newRuntime(&stubReminderStore{items: []Reminder{momentWatch}}, &recordingChannel{})
			cold.now = func() time.Time { return moment.at }
			for i, line := range trace.history {
				cold.remember(MessageEvent{Kind: EventKindGroup, Platform: "onebot-v11", ProfileID: cfg.ID, SelfID: cfg.BotAccount,
					GroupID: trace.group, GroupName: "群聊", UserID: line.user, SenderName: line.user, MessageID: "c" + strconv.Itoa(index) + "-" + strconv.Itoa(i),
					RawMessage: line.text, Segments: []MessageSegment{{Type: "text", Data: map[string]string{"text": line.text}}},
					Time: trace.at - int64(len(trace.history)-1-i)*60})
			}
			target := MessageEvent{Kind: EventKindGroup, Platform: "onebot-v11", ProfileID: cfg.ID, SelfID: cfg.BotAccount, GroupID: trace.group, GroupName: "群聊"}
			lines := proactiveNewsChatLines(cold.proactiveRecentHistory(target), cfg.BotAccount)
			news := cold.rssTopicPoolNews(target)
			record := map[string]any{"stage": "cold", "index": index, "group": trace.group, "file": trace.file,
				"cold_at": moment.at.Format(time.DateTime), "idle_min": int(moment.idle / time.Minute), "chat_tail": lastN(lines, 8), "material": news.Results}
			plan := proactivePlan{IdleFor: moment.idle}
			for _, variant := range []struct {
				key  string
				news proactiveNews
			}{{"base", proactiveNews{}}, {"pool", news}} {
				if variant.key == "pool" && news.Results == "" {
					continue
				}
				genCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				text, err := cold.generateProactiveMessage(genCtx, cfg, target, plan, variant.news)
				cancel()
				if err != nil {
					text = "ERROR: " + err.Error()
				}
				record[variant.key] = text
			}
			write(record)
			t.Logf("cold %d %s done", index, trace.group)
		}()
	}
	wg.Wait()
}

type replayTopicPoolMoment struct {
	trace replayTopicPoolTrace
	at    time.Time
	idle  time.Duration
}

// replayTopicPoolColdMoments 找真冷场过的时刻：同群相邻两次回复记录隔了 idle 以上，冷场
// 时刻取上一条之后 idle，落在本地 dayHours（如 10-22）里才要。每个群最多 3 个，多的
// 取头、中、尾；各群轮流取到 limit。
func replayTopicPoolColdMoments(traces []replayTopicPoolTrace, idle time.Duration, dayHours string, limit int) []replayTopicPoolMoment {
	from, to := 10, 22
	if parts := strings.SplitN(dayHours, "-", 2); len(parts) == 2 {
		from, to = replayInt(parts[0], from), replayInt(parts[1], to)
	}
	byGroup := map[string][]replayTopicPoolTrace{}
	for _, trace := range traces {
		byGroup[trace.group] = append(byGroup[trace.group], trace)
	}
	var groupIDs []string
	for group := range byGroup {
		groupIDs = append(groupIDs, group)
	}
	sort.Strings(groupIDs)
	var perGroup [][]replayTopicPoolMoment
	for _, group := range groupIDs {
		list := byGroup[group]
		sort.Slice(list, func(i, j int) bool { return list[i].at < list[j].at })
		var found []replayTopicPoolMoment
		for i, trace := range list {
			if len(trace.history) < 5 {
				continue
			}
			at := time.Unix(trace.at, 0).Add(idle)
			if i+1 < len(list) && list[i+1].at < at.Unix() {
				continue
			}
			if hour := at.Hour(); hour < from || hour >= to {
				continue
			}
			found = append(found, replayTopicPoolMoment{trace: trace, at: at, idle: idle})
		}
		if len(found) > 3 {
			found = []replayTopicPoolMoment{found[0], found[len(found)/2], found[len(found)-1]}
		}
		perGroup = append(perGroup, found)
	}
	var out []replayTopicPoolMoment
	for round := 0; len(out) < limit; round++ {
		added := false
		for _, found := range perGroup {
			if round < len(found) && len(out) < limit {
				out = append(out, found[round])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out
}

func replayDuration(raw string, fallback time.Duration) time.Duration {
	if value, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && value > 0 {
		return value
	}
	return fallback
}

func replayInt(raw string, fallback int) int {
	if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && value > 0 {
		return value
	}
	return fallback
}

func lastN(lines []string, n int) []string {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

type replayTopicPoolLine struct{ user, text string }

type replayTopicPoolTrace struct {
	file, group string
	at          int64
	// history 是这次回复看到的同群聊天，最后一条是当时要回的那句。
	history []replayTopicPoolLine
}

var (
	replayTopicPoolHistory = regexp.MustCompile(`^\[历史 [^\]]*\] ([^（\n]*)（[^）]*）: `)
	replayTopicPoolExtra   = regexp.MustCompile(`^【当前同轮补充消息[^\n]*?】【消息时间[^】]*】[^（\n]*（[^）]*）: `)
	replayTopicPoolNoise   = regexp.MustCompile(`<diana-quote[^>]*/>|@im_(?:bot|user)_[0-9a-f]+|〔原群前后文[\s\S]*?〕|\[CQ:[^\]]*\]`)
)

// replayTopicPoolClean 去掉回放请求里的标记，剩下群友说的话，接近线上 memoryEventText。
func replayTopicPoolClean(text string) string {
	return strings.Join(strings.Fields(replayTopicPoolNoise.ReplaceAllString(text, " ")), " ")
}

// replayTopicPoolTraces 读生产记录：每次回复只取第一步请求（目录里序号最小的 reply），
// 只取指定模型。聊天取同群历史、同轮补充和当前消息的正文，跨群历史和各种标记都不要。
func replayTopicPoolTraces(t *testing.T, days []string, model string) []replayTopicPoolTrace {
	t.Helper()
	var out []replayTopicPoolTrace
	for _, day := range days {
		dirs, _ := filepath.Glob(filepath.Join(day, "group-*", "*"))
		for _, dir := range dirs {
			files, _ := filepath.Glob(filepath.Join(dir, "*-reply.json"))
			sort.Strings(files)
			if len(files) == 0 {
				continue
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				continue
			}
			var record struct {
				CreatedAt time.Time `json:"created_at"`
				Metadata  struct {
					Model   string              `json:"model"`
					Request llm.GenerateRequest `json:"request"`
				} `json:"metadata"`
			}
			if json.Unmarshal(data, &record) != nil || (model != "" && record.Metadata.Model != model) {
				continue
			}
			messages := record.Metadata.Request.Messages
			current := -1
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == llm.RoleUser && strings.HasPrefix(messages[i].Content, "【当前需要回复的消息】") {
					current = i
					break
				}
			}
			if current < 0 {
				continue
			}
			body := messages[current].Content
			if at := strings.Index(body, "\n"); at >= 0 {
				body = body[at+1:]
			}
			if at := strings.Index(body, "\n\n"); at >= 0 {
				body = body[:at]
			}
			var history []replayTopicPoolLine
			for i := 0; i < current; i++ {
				if messages[i].Role != llm.RoleUser {
					continue
				}
				if m := replayTopicPoolExtra.FindStringIndex(messages[i].Content); m != nil {
					if text := replayTopicPoolClean(messages[i].Content[m[1]:]); text != "" {
						history = append(history, replayTopicPoolLine{user: "群友", text: truncateRunes(text, 200)})
					}
					continue
				}
				m := replayTopicPoolHistory.FindStringSubmatchIndex(messages[i].Content)
				if m == nil {
					continue
				}
				if text := replayTopicPoolClean(messages[i].Content[m[1]:]); text != "" {
					history = append(history, replayTopicPoolLine{user: messages[i].Content[m[2]:m[3]], text: truncateRunes(text, 200)})
				}
			}
			if text := replayTopicPoolClean(body); text != "" {
				history = append(history, replayTopicPoolLine{user: "群友", text: truncateRunes(text, 200)})
			}
			if len(history) > 40 {
				history = history[len(history)-40:]
			}
			group := strings.TrimPrefix(filepath.Base(filepath.Dir(dir)), "group-")
			out = append(out, replayTopicPoolTrace{file: strings.TrimPrefix(files[0], day+"/"), group: group, at: record.CreatedAt.Unix(), history: history})
		}
	}
	return out
}
