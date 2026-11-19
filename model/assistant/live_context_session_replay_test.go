// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
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
	client := liveLLMClient(t)
	withFastSendTiming(t)
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
				t.Logf("IN  %s: %s", event.SenderName, truncateRunes(strings.ReplaceAll(event.RawMessage, "\n", " / "), 80))
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
				t.Logf("OUT %02d chars=%-4d %s", i, size, truncateRunes(strings.ReplaceAll(msg.text, "\n", " / "), 120))
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
