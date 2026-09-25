// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 离线重放：拿生产上机器人真实回过话的时刻，换一份 SOUL.md 重新走一遍完整回复链路，
// 看口吻会不会被聊天历史带偏（喵、然然、括号动作），夜里会不会主动催人睡觉。
//
// 输入是从生产库抽出来的 JSONL，每行一个回复时刻：触发消息、它之前的几十条群消息
// （机器人自己以前的回复也在里面，会以 assistant 身份回放——这正是口吻传染的来源）、
// 机器人当时实际发出去的话、当时注入的本群常用表达。机器人配置取生产那份（去掉凭据），
// 走和线上一样的旧字段折叠。长期记忆没有接进来：它按话题召回，重放里查不到。
//
// 默认跳过，需要：
//
//	DIANA_REPLAY_SOUL_ROWS     抽样 JSONL 路径
//	DIANA_REPLAY_SOUL_PROFILE  生产机器人配置 JSON（去掉凭据）
//	DIANA_REPLAY_SOUL_GROUPS   生产群配置 JSON 数组，可选
//	DIANA_REPLAY_SOUL_ARMS     逗号分隔：prod（配置里原来的正文）或内置人设 ID 去掉 builtin: 前缀
//	DIANA_REPLAY_SOUL_SET      只跑 train 或 test，可选
//	DIANA_REPLAY_SOUL_OUT      逐条结果 JSONL 输出路径，可选
//	DIANA_REPLAY_SOUL_NO_LENGTH_NORM=1  不注入「本群消息长度」，做对照
//	DIANA_REPLAY_SOUL_TRIM_BOT=1  把历史里机器人以前的回复截成第一句，模拟换人设一阵子之后
//	                              历史里已经全是新口吻的稳态；不设就用线上当时的原话
//	DIANA_LIVE_LLM=1 与 DIANA_TEST_LLM_*（见 liveLLMClient）；DIANA_TEST_LLM_PROVIDER 可设为 gemini
func TestLiveSoulReplay(t *testing.T) {
	rowsPath := os.Getenv("DIANA_REPLAY_SOUL_ROWS")
	if rowsPath == "" {
		t.Skip("set DIANA_REPLAY_SOUL_ROWS to a sampled replay JSONL")
	}
	client := liveSoulReplayClient(t)
	type sample struct {
		Set         string         `json:"set"`
		GroupID     string         `json:"group_id"`
		Hour        int            `json:"hour"`
		Time        int64          `json:"time"`
		Trigger     MessageEvent   `json:"trigger"`
		History     []MessageEvent `json:"history"`
		Original    []string       `json:"original"`
		Expressions []string       `json:"expressions"`
	}
	var samples []sample
	file, err := os.Open(rowsPath)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<26)
	only := os.Getenv("DIANA_REPLAY_SOUL_SET")
	for scanner.Scan() {
		var item sample
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if only == "" || item.Set == only {
			samples = append(samples, item)
		}
	}
	_ = file.Close()

	rawProfile, err := os.ReadFile(os.Getenv("DIANA_REPLAY_SOUL_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	groupConfigs := map[string]GroupConfig{}
	if path := os.Getenv("DIANA_REPLAY_SOUL_GROUPS"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var groups []GroupConfig
		if err := json.Unmarshal(raw, &groups); err != nil {
			t.Fatal(err)
		}
		for _, group := range groups {
			groupConfigs[group.GroupID] = group
		}
	}
	trimBot := os.Getenv("DIANA_REPLAY_SOUL_TRIM_BOT") == "1"
	if os.Getenv("DIANA_REPLAY_SOUL_NO_LENGTH_NORM") == "1" {
		groupLengthNormEnabled = false
		defer func() { groupLengthNormEnabled = true }()
	}
	arms := strings.Split(firstNonEmpty(os.Getenv("DIANA_REPLAY_SOUL_ARMS"), "prod"), ",")
	souls := map[string]string{}
	for _, persona := range BuiltinPersonas() {
		souls[strings.TrimPrefix(persona.ID, "builtin:")] = persona.SystemPrompt
	}

	type result struct {
		Arm      string   `json:"arm"`
		Set      string   `json:"set"`
		GroupID  string   `json:"group_id"`
		Hour     int      `json:"hour"`
		Trigger  string   `json:"trigger"`
		Original []string `json:"original"`
		Reply    []string `json:"reply"`
		Error    string   `json:"error,omitempty"`
	}
	var (
		mu      sync.Mutex
		results []result
		wg      sync.WaitGroup
	)
	sem := make(chan struct{}, 4)
	for _, arm := range arms {
		arm = strings.TrimSpace(arm)
		for _, item := range samples {
			wg.Add(1)
			go func(arm string, item sample) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				var cfg BotConfig
				if err := json.Unmarshal(rawProfile, &cfg); err != nil {
					t.Error(err)
					return
				}
				if arm != "prod" {
					soul, ok := souls[arm]
					if !ok {
						t.Errorf("unknown arm %q", arm)
						return
					}
					cfg.SystemPrompt = soul
				}
				cfg = cfg.WithDefaults()
				channel := &recordingChannel{}
				runtime := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
				runtime.SetProfiles(ProfileSet{Profiles: []BotConfig{cfg}})
				runtime.SetGroupConfigStore(&stubGroupConfigStore{configs: groupConfigs})
				runtime.SetExpressionStyleStore(fixedExpressionStore(item.Expressions))
				at := time.Unix(item.Trigger.Time, 0)
				runtime.now = func() time.Time { return at }
				for _, event := range item.History {
					event = soulReplayWithoutImages(event)
					if trimBot && event.UserID == cfg.BotAccount {
						event = soulReplayFirstSentence(event)
					}
					runtime.remember(event)
				}
				event := soulReplayWithoutImages(item.Trigger)
				text := event.RawMessage
				if !runtime.shouldHandleChat(event, text) {
					// 线上这一条是插话接的，不是被点名的。
					event.proactiveReply = true
				}
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
				_, replyErr := runtime.replyTo(ctx, event, text)
				cancel()
				row := result{Arm: arm, Set: item.Set, GroupID: item.GroupID, Hour: item.Hour, Trigger: text, Original: item.Original}
				for _, sent := range channel.sentSnapshot() {
					row.Reply = append(row.Reply, sent.Text)
				}
				if replyErr != nil {
					row.Error = replyErr.Error()
				}
				mu.Lock()
				results = append(results, row)
				mu.Unlock()
			}(arm, item)
		}
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool {
		if results[i].Arm != results[j].Arm {
			return results[i].Arm < results[j].Arm
		}
		return results[i].Trigger < results[j].Trigger
	})
	if path := os.Getenv("DIANA_REPLAY_SOUL_OUT"); path != "" {
		out, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range results {
			line, _ := json.Marshal(row)
			_, _ = out.Write(append(line, '\n'))
		}
		_ = out.Close()
	}
	// 基线是线上当时真的发出去的话，同一批样本只算一次。
	baseline := map[string]*soulReplayTally{}
	for _, item := range samples {
		key := "线上原话/" + item.Set
		if baseline[key] == nil {
			baseline[key] = &soulReplayTally{}
		}
		baseline[key].add(item.Original, item.Trigger.RawMessage, item.History, item.Hour)
	}
	tallies := map[string]*soulReplayTally{}
	errors := 0
	for _, row := range results {
		key := row.Arm + "/" + row.Set
		if tallies[key] == nil {
			tallies[key] = &soulReplayTally{}
		}
		if row.Error != "" {
			errors++
			t.Logf("%s 出错：%s", key, row.Error)
			continue
		}
		var sample sample
		for _, item := range samples {
			if item.Trigger.RawMessage == row.Trigger && item.GroupID == row.GroupID {
				sample = item
				break
			}
		}
		tallies[key].add(row.Reply, row.Trigger, sample.History, row.Hour)
	}
	for _, group := range []map[string]*soulReplayTally{baseline, tallies} {
		keys := make([]string, 0, len(group))
		for key := range group {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			t.Logf("%-18s %s", key, group[key])
		}
	}
	if errors > 0 {
		t.Logf("%d 条重放出错", errors)
	}
}

func liveSoulReplayClient(t *testing.T) llm.LLMClient {
	t.Helper()
	if os.Getenv("DIANA_TEST_LLM_PROVIDER") == "" {
		return liveLLMClient(t)
	}
	if os.Getenv("DIANA_LIVE_LLM") != "1" || strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY")) == "" {
		t.Skip("set DIANA_LIVE_LLM=1 and DIANA_TEST_LLM_API_KEY to run the replay against a real model")
	}
	client, err := llm.NewClient(llm.ProviderConfig{
		Provider: llm.Provider(os.Getenv("DIANA_TEST_LLM_PROVIDER")),
		APIKey:   strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_API_KEY")),
		BaseURL:  strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_BASE_URL")),
		Model:    strings.TrimSpace(os.Getenv("DIANA_TEST_LLM_MODEL")),
		Timeout:  120 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// soulReplayWithoutImages 把图片换成文字占位：QQ 的图片链接过期了，重放里拉不到原图，
// 而这组重放只看口吻，不看识图。
func soulReplayWithoutImages(event MessageEvent) MessageEvent {
	event.RawMessage = soulReplayCQImage.ReplaceAllString(event.RawMessage, "[图片]")
	segments := make([]MessageSegment, 0, len(event.Segments))
	for _, segment := range event.Segments {
		if segment.Type == "image" {
			segment = MessageSegment{Type: "text", Data: map[string]string{"text": "[图片]"}}
		}
		segments = append(segments, segment)
	}
	event.Segments = segments
	if event.Quoted != nil {
		quoted := *event.Quoted
		quoted.RawMessage = soulReplayCQImage.ReplaceAllString(quoted.RawMessage, "[图片]")
		kept := quoted.Segments[:0:0]
		for _, segment := range quoted.Segments {
			if segment.Type != "image" {
				kept = append(kept, segment)
			}
		}
		quoted.Segments = kept
		event.Quoted = &quoted
	}
	return event
}

// soulReplayFirstSentence 只留回复的第一句（按句末标点或换行切），最多 30 字。
func soulReplayFirstSentence(event MessageEvent) MessageEvent {
	runes := []rune(strings.TrimSpace(event.RawMessage))
	cut := len(runes)
	for index, r := range runes {
		if strings.ContainsRune("。！？!?\n", r) {
			cut = index + 1
			break
		}
	}
	if cut > 30 {
		cut = 30
	}
	text := strings.TrimSpace(string(runes[:cut]))
	event.RawMessage = text
	event.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": text}}}
	return event
}

type fixedExpressionStore []string

func (s fixedExpressionStore) BumpGroupExpression(context.Context, string, string, string, time.Time) error {
	return nil
}

func (s fixedExpressionStore) TopGroupExpressions(context.Context, string, time.Time, int, int, int) ([]GroupExpression, error) {
	out := make([]GroupExpression, 0, len(s))
	for _, phrase := range s {
		out = append(out, GroupExpression{Phrase: phrase, Count: 10})
	}
	return out, nil
}

func (s fixedExpressionStore) PruneGroupExpressions(context.Context, time.Time) error { return nil }

var (
	// 括号动作是（笑）（揉揉你的头）这种短的；带标点、字母数字的多半是解释性括号。
	soulReplayAction  = regexp.MustCompile(`（[^（），。、,.a-zA-Z0-9]{1,10}）`)
	soulReplayCQImage = regexp.MustCompile(`\[CQ:image[^\]]*\]`)
	soulReplaySleep   = regexp.MustCompile(`睡|熬夜|晚安|早点休息|去休息`)
	// 结尾揽活：最后一句在主动提供下一步帮忙，客服收尾的味道。
	soulReplayOffer = regexp.MustCompile(`(随时|有问题|有啥|需要的话|要不要我|我再帮你|我帮你|我来帮|给你(重|再)|再(给你|帮你)|喊我|叫我一声|告诉我)[^\n]{0,30}$`)
)

// soulReplayTally 数口吻被带偏的几种表现。催睡只算「对方最近没提作息、她自己提了」。
type soulReplayTally struct {
	turns, silent, meow, ranran, action, sleepNag, offer, period, newline, chars int
}

func (t *soulReplayTally) add(reply []string, trigger string, history []MessageEvent, hour int) {
	t.turns++
	text := strings.TrimSpace(strings.Join(reply, "\n"))
	if text == "" {
		t.silent++
		return
	}
	t.chars += len([]rune(text))
	if strings.Contains(text, "喵") {
		t.meow++
	}
	// 被问名字时答「叫我然然」是正常作答，不算口吻被带偏。
	if strings.Contains(text, "然然") && !strings.Contains(trigger, "名字") && !strings.Contains(trigger, "叫什么") {
		t.ranran++
	}
	if soulReplayAction.MatchString(text) {
		t.action++
	}
	if soulReplayOffer.MatchString(strings.TrimRight(text, "。！!～~ ")) {
		t.offer++
	}
	// 带换行：同一条气泡里分了行。发送层把每条气泡当一条消息，这里按原始回复逐条看。
	for _, bubble := range reply {
		if strings.Contains(strings.TrimSpace(bubble), "\n") {
			t.newline++
			break
		}
	}
	// 句号收尾：任何一条气泡、任何一行以「。」结束就算。
	for _, line := range strings.Split(text, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "。") {
			t.period++
			break
		}
	}
	recent := trigger
	for index := len(history) - 1; index >= 0 && index >= len(history)-4; index-- {
		recent += "\n" + history[index].RawMessage
	}
	if soulReplaySleep.MatchString(text) && !soulReplaySleep.MatchString(recent) && !strings.ContainsAny(recent, "困") && !strings.Contains(recent, "闭眼") {
		t.sleepNag++
	}
}

func (t *soulReplayTally) String() string {
	spoke := t.turns - t.silent
	if spoke <= 0 {
		return fmt.Sprintf("%d 条全部沉默", t.turns)
	}
	return fmt.Sprintf("开口 %d/%d  喵 %d  然然 %d  括号动作 %d  主动催睡 %d  结尾揽活 %d  句号收尾 %d  带换行 %d  平均 %d 字",
		spoke, t.turns, t.meow, t.ranran, t.action, t.sleepNag, t.offer, t.period, t.newline, t.chars/spoke)
}
