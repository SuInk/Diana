// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveProactiveMessagingReplay 拿生产里真实的冷场时刻回放主动开口：同一份人设、
// 冷场前的真实群聊，用真实模型生成，看她会说什么、会不会选择不说。什么都不发，结果
// 只进日志和结果文件。账号安全审核关掉，只看生成这一步。
//
//	DIANA_LIVE_LLM=1 DIANA_TEST_LLM_API_KEY=... DIANA_TEST_LLM_BASE_URL=... \
//	  DIANA_TEST_LLM_MODEL=... DIANA_PROACTIVE_REPLAY_BUNDLE=<私有回放包> \
//	  scripts/test-serial.sh ./model/assistant -run TestLiveProactiveMessagingReplay -v
//
// 回放包是 {"profile": 机器人配置（去掉凭据）, "cases": [{"group", "at", "history"}]}，
// at 是冷场前最后一条消息的时间，history 是那之前的消息事件。
func TestLiveProactiveMessagingReplay(t *testing.T) {
	path := os.Getenv("DIANA_PROACTIVE_REPLAY_BUNDLE")
	if path == "" {
		t.Skip("set DIANA_PROACTIVE_REPLAY_BUNDLE to a private production replay bundle")
	}
	client := liveLLMClient(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Profile BotConfig `json:"profile"`
		Cases   []struct {
			Group   string         `json:"group"`
			At      int64          `json:"at"`
			History []MessageEvent `json:"history"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal("invalid private replay bundle")
	}
	var results []map[string]string
	for _, tc := range bundle.Cases {
		if len(tc.History) == 0 {
			continue
		}
		t.Setenv("APP_DB_PATH", filepath.Join(t.TempDir(), "replay.db"))
		cfg := bundle.Profile
		cfg.ReplySafetyMasterEnabled = boolPointer(false)
		cfg.ProactiveMessaging = &ProactiveMessaging{Enabled: true, GroupIDs: []string{tc.Group}}
		cfg = cfg.WithDefaults()
		channel := &recordingChannel{}
		r := NewRuntime(cfg, channel, NewPluginManager(), nil, nil, nil, func() (LLMProvider, error) { return client, nil })
		for _, event := range tc.History {
			r.remember(soulReplayWithoutImages(event))
		}
		last := tc.History[len(tc.History)-1]
		fireAt := time.Unix(tc.At, 0).Add(2*time.Hour + 20*time.Minute)
		r.now = func() time.Time { return fireAt }
		target := proactiveTarget{
			event: MessageEvent{
				Kind: EventKindGroup, MessageType: "group", GroupID: tc.Group,
				Platform: last.Platform, ProfileID: last.ProfileID, ContextNamespace: last.ContextNamespace,
				SelfID: firstNonEmpty(last.SelfID, cfg.BotAccount), Time: fireAt.Unix(),
			},
			rules: proactiveRules{Idle: 2 * time.Hour, DailyLimit: 2},
		}
		plan, ok := r.proactivePlanFor(target, fireAt)
		if !ok {
			_, reason, _ := evaluateProactive(r.proactiveRecentHistory(target.event), proactiveBotID(target.event, cfg), target.rules, fireAt, "replay")
			t.Logf("group=%s at=%s not due: %s", tc.Group, time.Unix(tc.At, 0).Format(time.DateTime), reason)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		r.runProactiveInitiative(ctx, target, plan)
		cancel()
		var texts []string
		for _, msg := range channel.sentSnapshot() {
			texts = append(texts, msg.Text)
		}
		outcome := ""
		if len(r.recent) > 0 {
			outcome = r.recent[0].Outcome + " " + r.recent[0].Error
		}
		sent := strings.Join(texts, " | ")
		t.Logf("group=%s at=%s outcome=%s sent=%s", tc.Group, time.Unix(tc.At, 0).Format(time.DateTime), outcome, sent)
		results = append(results, map[string]string{"group": tc.Group, "at": time.Unix(tc.At, 0).Format(time.DateTime), "outcome": strings.TrimSpace(outcome), "sent": sent})
	}
	if out := os.Getenv("DIANA_PROACTIVE_REPLAY_RESULTS"); out != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(out, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
