// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// TestLiveContextSessionReplay 把一段生产群聊按原来的先后和间隔重新喂给完整的 Runtime，
// 机器人这段里的原话全部去掉、由模型现场重答，自己发出的话再按生产那样回流成历史。
// 同一份数据分别在两个分支上跑，比上下文规则对「重复发送」和「回复长度」的影响。默认跳过：
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_* DIANA_SESSION_REPLAY=<目录> go test ./model/assistant -run TestLiveContextSessionReplay
//
// 目录里放 events.json（{群号: [{"payload": MessageEvent JSON}]}，按时间排好）、
// bot_config.json（生产机器人配置，去掉密钥）、group_configs.json（GroupConfigSet）、
// windows.json（[{"group": 群号, "from": unix秒, "to": unix秒}]，窗口前的消息只进历史）。
// 间隔超过 DIANA_SESSION_GAP_CAP 秒（默认 15）按上限等，免得一段跑十几分钟。
func TestLiveContextSessionReplay(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("DIANA_SESSION_REPLAY"))
	if dir == "" {
		t.Skip("set DIANA_SESSION_REPLAY to a directory of exported production sessions")
	}
	// swe-2 这类和生产共用的免费额度：DIANA_SESSION_MAX_CALLS 给整次回放的模型调用封顶，
	// 上游一回 429 就不再发请求，剩下的消息照样喂进去但不会再调模型。
	// 设了 DIANA_SESSION_AUX_GEMINI_KEY 时，只有主回复（带 send 工具的请求）走被测模型，
	// 接话判断、选表情这些辅助请求走 Gemini：生产上它们本来就不是聊天模型在做，也省被测模型的额度。
	client := &sessionReplayBudgetClient{LLMClient: liveLLMClient(t), max: envInt("DIANA_SESSION_MAX_CALLS", 0)}
	if key := strings.TrimSpace(os.Getenv("DIANA_SESSION_AUX_GEMINI_KEY")); key != "" {
		aux, err := llm.NewClient(llm.ProviderConfig{Provider: llm.ProviderGemini, APIKey: key, Model: firstNonEmpty(os.Getenv("DIANA_SESSION_AUX_MODEL"), "gemini-3.8-flash"), Timeout: 90 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		client.aux = aux
	}
	defer func() {
		t.Logf("LLM calls=%d aux_calls=%d refused=%d rate_limited=%v", client.calls, client.auxCalls, client.refused, client.limited)
	}()
	withFastSendTiming(t)
	// 回放要看生产上真实的分条：打开整包关着的 MaiBot 断句，换回真随机数。
	withMaiBotSplit(t)
	maiBotSplitRandom = rand.Float64
	t.Cleanup(func() { maiBotSplitRandom = func() float64 { return 0.5 } })
	gapCap := time.Duration(envInt("DIANA_SESSION_GAP_CAP", 15)) * time.Second

	var groups map[string][]struct {
		Payload string `json:"payload"`
	}
	readSessionReplayJSON(t, filepath.Join(dir, "events.json"), &groups)
	var cfg BotConfig
	readSessionReplayJSON(t, filepath.Join(dir, "bot_config.json"), &cfg)
	var groupConfigs GroupConfigSet
	readSessionReplayJSON(t, filepath.Join(dir, "group_configs.json"), &groupConfigs)
	var windows []struct {
		Group string `json:"group"`
		From  int64  `json:"from"`
		To    int64  `json:"to"`
	}
	readSessionReplayJSON(t, filepath.Join(dir, "windows.json"), &windows)
	cfg = cfg.WithDefaults()

	for _, window := range windows {
		t.Run(fmt.Sprintf("%s_%d", window.Group, window.From), func(t *testing.T) {
			var events []MessageEvent
			for _, row := range groups[window.Group] {
				var event MessageEvent
				if err := json.Unmarshal([]byte(row.Payload), &event); err != nil {
					t.Fatal(err)
				}
				if event.Time <= window.To {
					events = append(events, event)
				}
			}
			sort.SliceStable(events, func(i, j int) bool { return events[i].Time < events[j].Time })
			// 整段平移到现在，窗口起点对齐当前时刻：积压判断、合并窗口都按墙上时间走。
			shift := time.Now().Unix() - window.From
			channel := &recordingChannel{}
			rt := NewRuntime(cfg, channel, NewDefaultPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
			rt.SetGroupConfigStore(groupConfigs.WithDefaults(cfg))
			// 没出回复时要知道是没接话、出错还是被拦：每条入站的判决都打出来。子测试结束后
			// 才到的判决（慢的回复）写到标准错误，t.Logf 在那时已经不能用。
			var finished sync.Once
			done := make(chan struct{})
			defer finished.Do(func() { close(done) })
			var decidedMu sync.Mutex
			decided := map[string]bool{}
			rt.SetEventListener(func(record EventRecord) {
				if record.Decision == "" && record.Error == "" {
					return
				}
				decidedMu.Lock()
				decided[record.MessageID] = true
				decidedMu.Unlock()
				line := fmt.Sprintf("EVT %s decision=%s reason=%s error=%s", record.MessageID, record.Decision, truncateRunes(record.Reason, 200), truncateRunes(record.Error, 200))
				select {
				case <-done:
					fmt.Fprintln(os.Stderr, "late "+line)
				default:
					t.Log(line)
				}
			})
			var self MessageEvent
			for _, event := range events {
				if event.Outbound {
					self = event
					break
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			type sentAt struct {
				at   time.Time
				text string
			}
			var (
				mu      sync.Mutex
				sent    []sentAt
				echoed  int
				pending sync.WaitGroup
			)
			// 生产上机器人自己发的话会作为 message_sent 回流；这里照样回流成历史。
			stopEcho := make(chan struct{})
			echoDone := make(chan struct{})
			go func() {
				defer close(echoDone)
				ticker := time.NewTicker(200 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stopEcho:
						return
					case <-ticker.C:
					}
					snapshot := channel.sentSnapshot()
					for ; echoed < len(snapshot); echoed++ {
						msg := snapshot[echoed]
						mu.Lock()
						sent = append(sent, sentAt{at: time.Now(), text: msg.Text})
						mu.Unlock()
						echo := self
						echo.Outbound = true
						echo.Time = time.Now().Unix()
						echo.MessageID = fmt.Sprintf("replay-out-%d", echoed)
						echo.RawMessage = msg.Text
						echo.Segments = []MessageSegment{{Type: "text", Data: map[string]string{"text": msg.Text}}}
						echo.Quoted = nil
						rt.remember(echo)
					}
				}
			}()

			var last int64
			var fed []string
			started := time.Now()
			for _, event := range events {
				event.Time += shift
				if event.Time < window.From+shift {
					rt.remember(event)
					continue
				}
				if event.Outbound {
					continue // 机器人的原话去掉，由模型现场重答
				}
				if last != 0 {
					gap := time.Duration(event.Time-last) * time.Second
					time.Sleep(min(gap, gapCap))
				}
				last = event.Time
				fed = append(fed, event.MessageID)
				t.Logf("IN  +%s %s: %s", time.Since(started).Round(time.Second), event.SenderName, truncateRunes(strings.ReplaceAll(event.RawMessage, "\n", " / "), 120))
				pending.Add(1)
				go func(event MessageEvent) {
					defer pending.Done()
					if err := rt.HandleEvent(ctx, event); err != nil {
						t.Logf("HandleEvent %s: %v", event.MessageID, err)
					}
				}(event)
			}
			pending.Wait()
			stable, count := 0, -1
			// 先等每条喂进去的消息都有了判决（慢模型的回复不会被提前收尾），再等发言停下来。
			waitForCondition(t, 10*time.Minute, func() bool {
				decidedMu.Lock()
				defer decidedMu.Unlock()
				for _, id := range fed {
					if !decided[id] {
						return false
					}
				}
				return true
			})
			waitForCondition(t, 10*time.Minute, func() bool {
				n := len(channel.sentSnapshot())
				if n == count {
					stable++
				} else {
					stable, count = 0, n
				}
				return stable >= 1500 // 约 15 秒没有新发言
			})
			close(stopEcho)
			<-echoDone

			mu.Lock()
			defer mu.Unlock()
			total, long := 0, 0
			for i, msg := range sent {
				size := len([]rune(msg.text))
				total += size
				if size > 80 {
					long++
				}
				// 原文全量打出来给人判断，换行写成 ⏎ 才看得出一条里是不是挤了几行。
				t.Logf("OUT %02d +%s chars=%-4d %s", i, msg.at.Sub(started).Round(time.Second), size, strings.ReplaceAll(msg.text, "\n", "⏎"))
			}
			dup := 0
			for i := range sent {
				for j := 0; j < i; j++ {
					if sent[i].at.Sub(sent[j].at) < 5*time.Minute && sessionReplaySimilar(sent[i].text, sent[j].text) {
						dup++
						t.Logf("DUP %02d ~ %02d", j, i)
						break
					}
				}
			}
			avg := 0
			if len(sent) > 0 {
				avg = total / len(sent)
			}
			t.Logf("SUMMARY group=%s messages=%d total_chars=%d avg_chars=%d over80=%d near_dup=%d", window.Group, len(sent), total, avg, long, dup)
		})
	}
}

type sessionReplayBudgetClient struct {
	llm.LLMClient
	aux      llm.LLMClient
	mu       sync.Mutex
	max      int
	calls    int
	auxCalls int
	refused  int
	limited  bool
}

func (c *sessionReplayBudgetClient) Generate(ctx context.Context, req llm.GenerateRequest) (*llm.GenerateResponse, error) {
	// DIANA_SESSION_REASONING 给没指定思考档的请求补一个，复现生产「gemini-3.8-flash-low」这类按档位起名的配置。
	if effort := strings.TrimSpace(os.Getenv("DIANA_SESSION_REASONING")); effort != "" && req.ReasoningEffort == "" {
		req.ReasoningEffort = effort
	}
	if c.aux != nil && !sessionReplayPrimaryRequest(req) {
		c.mu.Lock()
		c.auxCalls++
		c.mu.Unlock()
		return c.aux.Generate(ctx, req)
	}
	c.mu.Lock()
	if c.limited || (c.max > 0 && c.calls >= c.max) {
		c.refused++
		c.mu.Unlock()
		return nil, fmt.Errorf("session replay: call budget spent or upstream rate limited")
	}
	c.calls++
	c.mu.Unlock()
	resp, err := c.LLMClient.Generate(ctx, req)
	if err != nil && (strings.Contains(err.Error(), "429") || strings.Contains(strings.ToLower(err.Error()), "rate limit")) {
		c.mu.Lock()
		c.limited = true
		c.mu.Unlock()
	}
	return resp, err
}

// sessionReplayPrimaryRequest 判断是不是主回复。默认看有没有 send 工具；旧版本还没有
// send，设 DIANA_SESSION_PRIMARY_MIN_TOOLS 改成按工具数认（主回复带整套工具，辅助请求只有零到一个）。
func sessionReplayPrimaryRequest(req llm.GenerateRequest) bool {
	if minTools := envIntValue("DIANA_SESSION_PRIMARY_MIN_TOOLS"); minTools > 0 {
		return len(req.Tools) >= minTools
	}
	return slices.ContainsFunc(req.Tools, func(tool llm.ToolDefinition) bool { return tool.Name == "send" })
}

func envIntValue(name string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	return value
}

func readSessionReplayJSON(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// sessionReplaySimilar 按字二元组的重合度判断两条发言是不是换个说法的同一句。
func sessionReplaySimilar(a, b string) bool {
	grams := func(text string) map[string]bool {
		runes := []rune(strings.Join(strings.Fields(text), ""))
		set := map[string]bool{}
		for i := 0; i+1 < len(runes); i++ {
			set[string(runes[i:i+2])] = true
		}
		return set
	}
	left, right := grams(a), grams(b)
	if len(left) < 6 || len(right) < 6 {
		return false
	}
	shared := 0
	for gram := range left {
		if right[gram] {
			shared++
		}
	}
	return float64(shared)/float64(min(len(left), len(right))) >= 0.5
}
