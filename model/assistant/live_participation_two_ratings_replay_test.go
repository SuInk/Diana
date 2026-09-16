// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SuInk/diana/model/llm"
)

// 离线重放：删掉可回答之后，线上记录下来的接话判断还会不会得出同样的结论。
//
// 输入是从生产库副本抽出来的 JSONL，每行带当时的档位、旧评分、旧结论和完整路由请求。
// 抽样按旧逻辑分三层：旧版放行的、只被可回答挡住的（相关度或闲聊已过线）、其他原因
// 没放行的。中间那层是这次改动直接影响的人群——9-10 以来 9535 次判断里有 1142 次。
// 默认跳过，需要：
//
//	DIANA_REPLAY_ROWS  抽样 JSONL 路径
//	DIANA_LIVE_LLM=1 与 DIANA_TEST_LLM_* 真实模型配置（见 liveLLMClient）
//	DIANA_REPLAY_OUT   逐条结果 JSONL 输出路径，可选
//
// 冷却按已结束计算，新旧一致。
func TestLiveParticipationTwoRatingsReplay(t *testing.T) {
	path := os.Getenv("DIANA_REPLAY_ROWS")
	if path == "" {
		t.Skip("set DIANA_REPLAY_ROWS to a sampled replay JSONL")
	}
	client := liveLLMClient(t)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	type row struct {
		Stratum    string                            `json:"stratum"`
		GroupID    string                            `json:"group_id"`
		MessageID  string                            `json:"message_id"`
		Levels     []string                          `json:"levels"`
		OldRatings map[string]map[string]interface{} `json:"old_ratings"`
		OldAllowed bool                              `json:"old_allowed"`
		System     string                            `json:"system"`
		User       string                            `json:"user"`
		NewRel     bool                              `json:"new_directed"`
		NewChat    float64                           `json:"new_chat_in"`
		NewAnswer  float64                           `json:"new_answerability,omitempty"`
		NewReason  string                            `json:"new_reason"`
		NewAllowed bool                              `json:"new_allowed"`
		Error      string                            `json:"error,omitempty"`
	}
	var rows []*row
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		var item row
		if json.Unmarshal(scanner.Bytes(), &item) == nil {
			rows = append(rows, &item)
		}
	}
	const oldInstruction = "Intent Recognition：请为当前消息给出相关度、可回答分和闲聊适合度，各含 score 和 reason。上下文：\n"
	const newInstruction = "Intent Recognition：请为当前消息给出相关度和闲聊适合度，各含 score 和 reason。上下文：\n"
	// DIANA_REPLAY_CONTROL=1 时原样发旧请求、按旧规则判（可回答当共用闸），
	// 用来量出模型自己前后两次跑的波动，好把改动的影响和随机波动分开。
	control := os.Getenv("DIANA_REPLAY_CONTROL") == "1"

	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, item := range rows {
		wg.Add(1)
		go func(item *row) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			prefs := ParticipationPreferences{RelevanceLevel: item.Levels[0], ChatLevel: item.Levels[1]}
			// 线上几个版本的示例措辞不同，按结构定位：从档位行开始，到「只输出裸 JSON」
			// 那句示例的结尾为止，整段换成新提示词。
			start := strings.Index(item.System, "本轮相关度档位")
			tail := strings.Index(item.System, "只输出裸 JSON")
			end := -1
			if tail >= 0 {
				if offset := strings.Index(item.System[tail:], "}}。"); offset >= 0 {
					end = tail + offset + len("}}。")
				}
			}
			if start < 0 || end < 0 || !strings.HasPrefix(item.User, oldInstruction) {
				item.Error = "请求结构对不上"
				return
			}
			system := item.System[:start] + prefs.prompt() + item.System[end:]
			user := newInstruction + strings.TrimPrefix(item.User, oldInstruction)
			if control {
				system, user = item.System, item.User
			}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			resp, err := client.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{
				{Role: llm.RoleSystem, Content: system}, {Role: llm.RoleUser, Content: user},
			}})
			if err != nil {
				item.Error = err.Error()
				return
			}
			ratings, err := parseParticipationRatings(resp.Text)
			if err != nil {
				item.Error = "解析失败：" + err.Error()
				return
			}
			item.NewRel, item.NewChat = *ratings.Relevance.Directed, *ratings.ChatIn.Score
			item.NewReason = "在跟机器人说话：" + ratings.Relevance.Reason + "；闲聊：" + ratings.ChatIn.Reason
			item.NewAllowed, _ = prefs.ratingsAllow(ratings, true)
			if control {
				item.NewAnswer = replayOldAnswerabilityScore(resp.Text)
				item.NewAllowed = item.NewAllowed && replayOldAnswerabilityPasses(resp.Text, item.Levels[2])
			}
		}(item)
	}
	wg.Wait()

	type tally struct{ total, errors, allowed, flippedOn, flippedOff int }
	counts := map[string]*tally{}
	for _, item := range rows {
		c := counts[item.Stratum]
		if c == nil {
			c = &tally{}
			counts[item.Stratum] = c
		}
		c.total++
		if item.Error != "" {
			c.errors++
			continue
		}
		if item.NewAllowed {
			c.allowed++
		}
		if item.NewAllowed && !item.OldAllowed {
			c.flippedOn++
		}
		if !item.NewAllowed && item.OldAllowed {
			c.flippedOff++
		}
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		c := counts[key]
		t.Logf("%-14s 共 %d 条（失败 %d）：新版放行 %d，由不回变回复 %d，由回复变不回 %d", key, c.total, c.errors, c.allowed, c.flippedOn, c.flippedOff)
	}
	if out := os.Getenv("DIANA_REPLAY_OUT"); out != "" {
		file, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		encoder := json.NewEncoder(file)
		for _, item := range rows {
			item.System, item.User = "", fmt.Sprintf("%.200s", item.User)
			_ = encoder.Encode(item)
		}
		t.Logf("逐条结果写入 %s", out)
	}
}

// replayOldAnswerabilityPasses 按旧规则判可回答这道共用闸，只给对照组用。
func replayOldAnswerabilityPasses(raw, level string) bool {
	if level == "" {
		level = "medium"
	}
	if level == "off" || level == "always" {
		return true
	}
	text, err := participationRatingsJSON(raw)
	if err != nil {
		return false
	}
	var parsed struct {
		Answerability struct {
			Score *float64 `json:"score"`
		} `json:"answerability"`
	}
	if json.Unmarshal([]byte(text), &parsed) != nil || parsed.Answerability.Score == nil {
		return false
	}
	return ratingPasses(*parsed.Answerability.Score, level)
}

// replayOldAnswerabilityScore 取出旧协议里的可回答分，读不到记 -1。
func replayOldAnswerabilityScore(raw string) float64 {
	text, err := participationRatingsJSON(raw)
	if err != nil {
		return -1
	}
	var parsed struct {
		Answerability struct {
			Score *float64 `json:"score"`
		} `json:"answerability"`
	}
	if json.Unmarshal([]byte(text), &parsed) != nil || parsed.Answerability.Score == nil {
		return -1
	}
	return *parsed.Answerability.Score
}
